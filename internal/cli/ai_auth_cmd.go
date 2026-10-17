package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	execrun "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAIAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Inspect and refresh OAuth credentials for MCP servers",
		Long: `Manage MCP server authentication.

OAuth-backed MCP servers (Cloudflare plugin servers, claude.ai connectors)
periodically lose their credentials. 'status' reports which servers need
re-auth, 'login' authenticates them, and 'relogin' clears stale credentials
before logging in again.

Server names containing spaces must be quoted:
  dot ai auth relogin "claude.ai Canva"`,
	}
	cmd.AddCommand(newAIAuthStatusCmd())
	cmd.AddCommand(newAIAuthLoginCmd())
	cmd.AddCommand(newAIAuthReloginCmd())
	return cmd
}

func newAIAuthStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show MCP servers and which ones need re-authentication",
		Long: `List configured MCP servers and flag the ones needing re-authentication.

For claude, the pending set is read from Claude Code's own cache
(~/.claude/mcp-needs-auth-cache.json), so it reflects the last state Claude
recorded: a server re-authenticated outside dot can still appear as pending
until Claude rewrites the file. Pass --probe for live state.

For codex (--tool codex), servers come from 'codex mcp list --json'. Codex
keeps MCP OAuth in the macOS Keychain keyed by server config hash and exposes
no stale-credential state, so the oauth column only classifies the transport;
--probe streams 'codex doctor'.`,
		Args: cobra.NoArgs,
		RunE: runAIAuthStatus,
	}
	c.Flags().Bool("json", false, "Emit machine-readable JSON")
	c.Flags().Bool("probe", false, "Also stream live state ('claude mcp list' / 'codex doctor')")
	c.Flags().String("tool", "claude", "CLI owning the MCP servers (claude or codex)")
	return c
}

// runAIAuthStatus is file-driven by default (no exec) so it stays testable and
// fast: the pending-auth set comes from Claude's own cache file.
func runAIAuthStatus(cmd *cobra.Command, _ []string) error {
	bin, err := authToolBinary(cmd)
	if err != nil {
		return err
	}
	if bin == "codex" {
		return runAIAuthStatusCodex(cmd)
	}
	home := homeFromCmd(cmd)
	runner := newAuthRunnerFromCmd(cmd)

	pending := []string{}
	if data, err := runner.ReadFile(filepath.Join(home, ".claude", "mcp-needs-auth-cache.json")); err == nil {
		names, perr := parseNeedsAuth(data)
		if perr != nil {
			return perr
		}
		pending = names
	}
	pendingSet := make(map[string]bool, len(pending))
	for _, name := range pending {
		pendingSet[name] = true
	}

	var servers []mcpServer
	if data, err := runner.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
		parsed, perr := parseMCPServers(data)
		if perr != nil {
			return perr
		}
		servers = parsed
	}
	// A server can be pending without appearing in .claude.json (claude.ai
	// connectors live server-side), so surface those too.
	known := make(map[string]bool, len(servers))
	for _, s := range servers {
		known[s.Name] = true
	}
	for _, name := range pending {
		if !known[name] {
			servers = append(servers, mcpServer{Name: name, Type: "connector"})
		}
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })

	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		items := make([]map[string]any, 0, len(servers))
		for _, s := range servers {
			items = append(items, map[string]any{
				"name":       s.Name,
				"type":       s.Type,
				"url":        s.URL,
				"needs_auth": pendingSet[s.Name],
			})
		}
		return printJSON(cmd, map[string]any{"servers": items, "pending_count": len(pending)})
	}

	p := printerFrom(cmd)
	p.Header("MCP Auth Status")
	if len(servers) == 0 {
		p.Bullet(ui.StyleHint.Render(ui.MarkAbsent), "no MCP servers configured")
	}
	for _, s := range servers {
		marker, style := ui.MarkPresent, interface{ Render(...string) string }(ui.StyleSuccess)
		state := "ok"
		if pendingSet[s.Name] {
			marker, style = ui.MarkWarn, ui.StyleWarning
			state = "needs auth"
		}
		label := fmt.Sprintf("%-28s %-11s %s", ui.StyleValue.Render(s.Name), state, ui.StyleHint.Render(s.detail()))
		p.Bullet(style.Render(marker), label)
	}
	if len(pending) > 0 {
		p.Blank()
		p.Line("Run 'dot ai auth login --all-needed' to authenticate %d server(s).", len(pending))
	}

	if probe, _ := cmd.Flags().GetBool("probe"); probe {
		if !runner.CommandExists("claude") {
			p.Warn("  --probe requires the claude CLI in PATH")
			return nil
		}
		p.Section("Live probe (claude mcp list)")
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		// RunQuery, not RunAttached: the probe is read-only and must still run
		// under --dry-run.
		res, err := runner.RunQuery(ctx, "claude", "mcp", "list")
		if out := strings.TrimSpace(res.Stdout); out != "" {
			p.Line("%s", out)
		}
		if err != nil {
			p.Warn("  probe reported errors: %v", err)
		}
	}
	return nil
}

// runAIAuthStatusCodex lists codex MCP servers via 'codex mcp list --json'.
// Unlike claude there is no pending-auth cache: codex stores MCP OAuth in the
// macOS Keychain keyed <server>|<config-hash>, so auth_status only classifies
// the transport (o_auth vs unsupported) and cannot distinguish valid from
// stale credentials.
func runAIAuthStatusCodex(cmd *cobra.Command) error {
	runner := newAuthRunnerFromCmd(cmd)
	p := printerFrom(cmd)
	if !runner.CommandExists("codex") {
		p.Warn("codex not found in PATH")
		return nil
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// RunQuery, not RunAttached: read-only, must still run under --dry-run.
	res, err := runner.RunQuery(ctx, "codex", "mcp", "list", "--json")
	if err != nil {
		return fmt.Errorf("codex mcp list: %w", err)
	}
	servers, err := parseCodexMCPList([]byte(res.Stdout))
	if err != nil {
		return err
	}

	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		items := make([]map[string]any, 0, len(servers))
		for _, s := range servers {
			items = append(items, map[string]any{
				"name":            s.Name,
				"enabled":         s.Enabled,
				"disabled_reason": s.DisabledReason,
				"transport":       s.Transport.Type,
				"url":             s.Transport.URL,
				"command":         s.Transport.Command,
				"auth_status":     s.AuthStatus,
			})
		}
		return printJSON(cmd, map[string]any{"tool": "codex", "servers": items})
	}

	p.Header("MCP Auth Status (codex)")
	if len(servers) == 0 {
		p.Bullet(ui.StyleHint.Render(ui.MarkAbsent), "no MCP servers configured")
	}
	for _, s := range servers {
		marker, style := ui.MarkPresent, interface{ Render(...string) string }(ui.StyleSuccess)
		state := "ok"
		switch {
		case !s.Enabled:
			marker, style, state = ui.MarkWarn, ui.StyleWarning, "disabled"
		case s.AuthStatus == "o_auth":
			style, state = ui.StyleHint, "oauth"
		case s.AuthStatus != "" && s.AuthStatus != "unsupported":
			marker, style, state = ui.MarkPartial, ui.StyleHint, s.AuthStatus
		}
		detail := s.detail()
		if !s.Enabled && s.DisabledReason != "" {
			detail = s.DisabledReason + "; " + detail
		}
		label := fmt.Sprintf("%-28s %-11s %s", ui.StyleValue.Render(s.Name), state, ui.StyleHint.Render(detail))
		p.Bullet(style.Render(marker), label)
	}
	p.Blank()
	p.Line("Codex cannot report stale OAuth; if a server misbehaves run 'dot ai auth relogin --tool codex <server>'.")

	if probe, _ := cmd.Flags().GetBool("probe"); probe {
		p.Section("Live probe (codex doctor)")
		res, err := runner.RunQuery(ctx, "codex", "doctor")
		if out := strings.TrimSpace(res.Stdout); out != "" {
			p.Line("%s", out)
		}
		if err != nil {
			p.Warn("  probe reported errors: %v", err)
		}
	}
	return nil
}

func newAIAuthLoginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "login [server...]",
		Short: "Authenticate one or more MCP servers",
		RunE:  runAIAuthLogin,
	}
	c.Flags().Bool("all-needed", false, "Log in to every server currently flagged as needing auth")
	c.Flags().String("tool", "claude", "CLI owning the MCP server (claude or codex)")
	c.Flags().Bool("no-browser", false, "Print the authorization URL instead of opening a browser")
	return c
}

func runAIAuthLogin(cmd *cobra.Command, args []string) error {
	bin, err := authToolBinary(cmd)
	if err != nil {
		return err
	}
	runner := newAuthRunnerFromCmd(cmd)
	p := printerFrom(cmd)

	servers, err := resolveAuthServers(cmd, runner, args, bin)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		p.Line("No servers need authentication.")
		return nil
	}
	if !runner.CommandExists(bin) {
		return fmt.Errorf("%s not found in PATH", bin)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	noBrowser, _ := cmd.Flags().GetBool("no-browser")

	p.Header("MCP Login")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		for _, name := range servers {
			p.Line("  %s", ui.StyleHint.Render("dry-run: would run "+bin+" mcp login "+name))
		}
		p.Line("Interactive OAuth cannot be simulated; re-run without --dry-run.")
		return nil
	}
	failed := 0
	for _, name := range servers {
		loginArgs := []string{"mcp", "login", name}
		if noBrowser && bin == "claude" {
			loginArgs = append(loginArgs, "--no-browser")
		}
		p.Line("  %s", ui.StyleHint.Render("login "+name))
		// OAuth needs a real TTY.
		if err := runner.RunInteractive(ctx, bin, loginArgs...); err != nil {
			failed++
			p.Fail("  login %s failed: %v", name, err)
		}
	}
	auditAIEventBestEffort(cmd, "ai.auth.login", map[string]any{
		"tool": bin, "servers": servers, "failed": failed,
	})
	if failed > 0 {
		return fmt.Errorf("%d server login(s) failed", failed)
	}
	p.Success("authenticated %d server(s)", len(servers))
	return nil
}

func newAIAuthReloginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "relogin <server...>",
		Short: "Clear stored credentials and re-authenticate MCP servers",
		Args:  cobra.MinimumNArgs(1),
		RunE:  runAIAuthRelogin,
	}
	c.Flags().String("tool", "claude", "CLI owning the MCP server (claude or codex)")
	c.Flags().Bool("no-browser", false, "Print the authorization URL instead of opening a browser")
	return c
}

func runAIAuthRelogin(cmd *cobra.Command, args []string) error {
	bin, err := authToolBinary(cmd)
	if err != nil {
		return err
	}
	runner := newAuthRunnerFromCmd(cmd)
	p := printerFrom(cmd)
	if !runner.CommandExists(bin) {
		return fmt.Errorf("%s not found in PATH", bin)
	}

	// Logout is destructive: a failed re-login leaves the server unauthenticated.
	if yes, _ := cmd.Flags().GetBool("yes"); !yes {
		p.Line("About to clear and re-authenticate: %s", strings.Join(args, ", "))
		ok, err := ui.ConfirmBool("Continue?", false, false)
		if err != nil {
			return err
		}
		if !ok {
			p.Line("aborted")
			return nil
		}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	noBrowser, _ := cmd.Flags().GetBool("no-browser")

	p.Header("MCP Re-login")
	if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
		for _, name := range args {
			p.Line("  %s", ui.StyleHint.Render("dry-run: would run "+bin+" mcp logout/login "+name))
		}
		p.Line("Interactive OAuth cannot be simulated; re-run without --dry-run.")
		return nil
	}
	failed := 0
	for _, name := range args {
		p.Line("  %s", ui.StyleHint.Render("logout "+name))
		if _, err := runner.Run(ctx, bin, "mcp", "logout", name); err != nil {
			// Not fatal: a server with no stored credentials still needs login.
			p.Warn("  logout %s: %v", name, err)
		}
		loginArgs := []string{"mcp", "login", name}
		if noBrowser && bin == "claude" {
			loginArgs = append(loginArgs, "--no-browser")
		}
		p.Line("  %s", ui.StyleHint.Render("login "+name))
		if err := runner.RunInteractive(ctx, bin, loginArgs...); err != nil {
			failed++
			p.Fail("  login %s failed: %v", name, err)
		}
	}
	auditAIEventBestEffort(cmd, "ai.auth.relogin", map[string]any{
		"tool": bin, "servers": args, "failed": failed,
	})
	if failed > 0 {
		return fmt.Errorf("%d server re-login(s) failed", failed)
	}
	p.Success("re-authenticated %d server(s)", len(args))
	return nil
}

func authToolBinary(cmd *cobra.Command) (string, error) {
	tool, _ := cmd.Flags().GetString("tool")
	switch tool {
	case "", "claude":
		return "claude", nil
	case "codex":
		return "codex", nil
	default:
		return "", fmt.Errorf("unknown tool %q (valid: claude, codex)", tool)
	}
}

// resolveAuthServers returns the explicit server args, or the pending-auth set
// when --all-needed is given. The pending cache is written by Claude Code only,
// so --all-needed is rejected for other tools rather than feeding them the
// wrong server list.
func resolveAuthServers(cmd *cobra.Command, runner *execrun.Runner, args []string, bin string) ([]string, error) {
	allNeeded, _ := cmd.Flags().GetBool("all-needed")
	if !allNeeded {
		if len(args) == 0 {
			return nil, fmt.Errorf("specify at least one server name or pass --all-needed")
		}
		return args, nil
	}
	if len(args) > 0 {
		return nil, fmt.Errorf("--all-needed cannot be combined with explicit server names")
	}
	if bin != "claude" {
		return nil, fmt.Errorf("--all-needed is claude-only: %s reports no stale-credential state; run 'dot ai auth status --tool %s' then 'dot ai auth relogin --tool %s <server>'", bin, bin, bin)
	}
	data, err := runner.ReadFile(filepath.Join(homeFromCmd(cmd), ".claude", "mcp-needs-auth-cache.json"))
	if err != nil {
		return nil, nil
	}
	return parseNeedsAuth(data)
}

func newAuthRunnerFromCmd(cmd *cobra.Command) *execrun.Runner {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	return execrun.NewRunner(dryRun, logger)
}

// ── parsers ───────────────────────────────────────────────────────────────

// parseNeedsAuth returns the sorted server names in
// ~/.claude/mcp-needs-auth-cache.json. Only the keys are read — the value
// schema is undocumented and varies, so anything JSON-shaped works.
func parseNeedsAuth(data []byte) ([]string, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse mcp-needs-auth-cache.json: %w", err)
	}
	if len(doc) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(doc))
	for name := range doc {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// mcpServer is one entry of the .mcpServers map in ~/.claude.json.
type mcpServer struct {
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (s mcpServer) detail() string {
	switch {
	case s.URL != "":
		return s.URL
	case s.Type != "":
		return s.Type
	default:
		return "stdio"
	}
}

// parseMCPServers reads the .mcpServers map from ~/.claude.json. A missing key
// yields nil, not an error — a fresh install has no servers.
func parseMCPServers(claudeJSON []byte) ([]mcpServer, error) {
	var doc struct {
		MCPServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(claudeJSON, &doc); err != nil {
		return nil, fmt.Errorf("parse .claude.json: %w", err)
	}
	if len(doc.MCPServers) == 0 {
		return nil, nil
	}
	out := make([]mcpServer, 0, len(doc.MCPServers))
	for name, cfg := range doc.MCPServers {
		out = append(out, mcpServer{Name: name, Type: cfg.Type, URL: cfg.URL})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// codexMCPServer is one entry of 'codex mcp list --json'. disabled_reason is
// null when unset; unmarshalling null into a plain string leaves it empty.
type codexMCPServer struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabled_reason"`
	Transport      struct {
		Type    string   `json:"type"`
		URL     string   `json:"url"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"transport"`
	AuthStatus string `json:"auth_status"`
}

func (s codexMCPServer) detail() string {
	switch {
	case s.Transport.URL != "":
		return s.Transport.URL
	case s.Transport.Command != "":
		// Include args: bare interpreter commands ("node") are ambiguous.
		return strings.TrimSpace(s.Transport.Command + " " + strings.Join(s.Transport.Args, " "))
	default:
		return s.Transport.Type
	}
}

func parseCodexMCPList(data []byte) ([]codexMCPServer, error) {
	var servers []codexMCPServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse codex mcp list --json: %w", err)
	}
	if len(servers) == 0 {
		return nil, nil
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, nil
}
