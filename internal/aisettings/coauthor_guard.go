package aisettings

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

const (
	CoauthorGuardOff   = "off"
	CoauthorGuardWarn  = "warn"
	CoauthorGuardBlock = "block"

	coauthorGuardStart = "<!-- dotfiles:coauthor-guard:start -->"
	coauthorGuardEnd   = "<!-- dotfiles:coauthor-guard:end -->"

	// coauthorGuardHookCommand is written verbatim into the git config as
	// hook.coauthor-guard.command (git >= 2.54 config-based hooks, paired
	// with event = commit-msg). Configured hooks run in addition to
	// .git/hooks and a repo-local core.hooksPath, which is what the global
	// core.hooksPath wiring could never do. Git expands the leading tilde
	// when it runs the hook.
	coauthorGuardHookCommand = "~/.config/git/hooks/commit-msg"
	// coauthorGuardHooksRelPath is the legacy global core.hooksPath value dot
	// used to set; apply migrates it away.
	coauthorGuardHooksRelPath = "~/.config/git/hooks"
)

// CoauthorGuardManager manages the AGENTS instruction and Git commit-msg guard
// that discourage or block unwanted Co-authored trailers.
type CoauthorGuardManager struct {
	Runner       *dotexec.Runner
	HomeDir      string
	ExplicitHome bool
}

// CoauthorGuardOptions controls guard application.
type CoauthorGuardOptions struct {
	Mode        string
	DryRun      bool
	ApplyAgents bool
}

// CoauthorGuardStatus describes the live guard state.
type CoauthorGuardStatus struct {
	Mode              string
	HookPath          string
	GitConfigPath     string
	AgentsPath        string
	HookDrift         string
	HookConfigDrift   string
	HooksPathLeftover string // a dot-managed core.hooksPath still present (drift when non-empty)
	AgentsDrift       string
	GitVersion        string
	// GitHooksSupported is false when the installed git predates config-based
	// hooks (2.54); the hook file is still written but nothing points at it.
	GitHooksSupported bool
}

// CoauthorGuardResult summarizes guard application.
type CoauthorGuardResult struct {
	Status        CoauthorGuardStatus
	HookChanged   bool
	ConfigChanged bool
	AgentsChanged bool
	AgentsApplied bool
	DryRun        bool
	Warning       string
}

// NewCoauthorGuardManager returns a manager rooted at homeDir.
func NewCoauthorGuardManager(runner *dotexec.Runner, homeDir string, explicitHome ...bool) *CoauthorGuardManager {
	return &CoauthorGuardManager{Runner: runner, HomeDir: homeDir, ExplicitHome: len(explicitHome) > 0 && explicitHome[0]}
}

// NormalizeCoauthorGuardMode returns the effective guard mode. The default is
// block: the AGENTS instruction already forbids the trailer, and a warning
// that scrolls past never stopped an agent commit.
func NormalizeCoauthorGuardMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = CoauthorGuardBlock
	}
	switch mode {
	case CoauthorGuardOff, CoauthorGuardWarn, CoauthorGuardBlock:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid coauthor guard mode %q (must be off, warn, or block)", mode)
	}
}

// Status reports whether the guard is installed and active.
func (m *CoauthorGuardManager) Status(mode string) (CoauthorGuardStatus, error) {
	mode, err := NormalizeCoauthorGuardMode(mode)
	if err != nil {
		return CoauthorGuardStatus{}, err
	}
	st := CoauthorGuardStatus{
		Mode:            mode,
		HookPath:        m.hookPath(),
		GitConfigPath:   m.gitConfigPath(),
		AgentsPath:      m.SSOTPath(),
		HookDrift:       "off",
		HookConfigDrift: "off",
		AgentsDrift:     "off",
	}
	if mode == CoauthorGuardOff {
		return st, nil
	}
	if data, err := os.ReadFile(st.HookPath); err == nil {
		if string(data) == coauthorGuardHookScript(mode) {
			st.HookDrift = "in-sync"
		} else {
			st.HookDrift = "out-of-sync"
		}
	} else if os.IsNotExist(err) {
		st.HookDrift = "missing"
	} else {
		return st, fmt.Errorf("read %s: %w", st.HookPath, err)
	}
	st.GitVersion, st.GitHooksSupported = m.gitHooksCapability()
	data, readErr := os.ReadFile(st.GitConfigPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return st, fmt.Errorf("read %s: %w", st.GitConfigPath, readErr)
	}
	content := string(data)
	st.HooksPathLeftover = m.dotManagedHooksPath(content)
	switch {
	case !st.GitHooksSupported:
		st.HookConfigDrift = "unsupported"
	default:
		st.HookConfigDrift = hookConfigDrift(content, m.homeDir())
	}
	st.AgentsDrift = m.agentsInstructionDrift()
	return st, nil
}

// Apply installs or updates the guard.
func (m *CoauthorGuardManager) Apply(opts CoauthorGuardOptions) (*CoauthorGuardResult, error) {
	mode, err := NormalizeCoauthorGuardMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	effectiveDryRun := opts.DryRun || m.runner().DryRun
	result := &CoauthorGuardResult{DryRun: effectiveDryRun}
	st, err := m.Status(mode)
	if err != nil {
		return nil, err
	}
	result.Status = st
	if mode == CoauthorGuardOff {
		return result, nil
	}

	hookContent := []byte(coauthorGuardHookScript(mode))
	if st.HookDrift != "in-sync" {
		result.HookChanged = true
		if !effectiveDryRun {
			if _, err := fileutil.EnsureFile(m.runner(), m.homeDir(), st.HookPath, hookContent, 0o755); err != nil {
				return nil, err
			}
			if err := os.Chmod(st.HookPath, 0o755); err != nil {
				return nil, fmt.Errorf("chmod %s: %w", st.HookPath, err)
			}
		}
	}
	if !st.GitHooksSupported {
		result.Warning = fmt.Sprintf("git %s predates config-based hooks (2.54); the commit-msg hook is installed but nothing points at it — upgrade git to enforce the guard", firstWord(st.GitVersion, "unknown"))
	}
	if (st.HookConfigDrift == "missing" || st.HookConfigDrift == "out-of-sync") || st.HooksPathLeftover != "" {
		result.ConfigChanged = true
		if !effectiveDryRun {
			current := ""
			if data, err := os.ReadFile(st.GitConfigPath); err == nil {
				current = string(data)
			} else if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("read %s: %w", st.GitConfigPath, err)
			}
			next := patchGitHookConfig(current, m.homeDir(), st.GitHooksSupported)
			if _, err := fileutil.EnsureFile(m.runner(), m.homeDir(), st.GitConfigPath, []byte(next), 0o644); err != nil {
				return nil, err
			}
		}
	}
	if st.AgentsDrift != "in-sync" {
		result.AgentsChanged = true
		if !effectiveDryRun {
			if err := m.ensureAgentsInstruction(); err != nil {
				return nil, err
			}
		}
	}
	if opts.ApplyAgents && !effectiveDryRun {
		agents := NewAgentsManager(m.runner(), m.homeDir(), m.ExplicitHome)
		apply, err := agents.Apply(ApplyOptions{Tools: agents.DefaultApplyTools()})
		if err != nil {
			return nil, err
		}
		for _, item := range apply.Items {
			if item.Changed {
				result.AgentsApplied = true
				break
			}
		}
	}
	if latest, err := m.Status(mode); err == nil {
		result.Status = latest
	}
	return result, nil
}

// dotManagedHooksPath returns the core.hooksPath value when it is the one dot
// used to manage, else "". A core.hooksPath pointing anywhere else is not
// dot's to touch: config-based hooks coexist with it, so it is left alone.
func (m *CoauthorGuardManager) dotManagedHooksPath(content string) string {
	value := gitConfigValue(content, "core", "hooksPath")
	if value == "" {
		return ""
	}
	if normalizeGitPath(value, m.homeDir()) == normalizeGitPath(coauthorGuardHooksRelPath, m.homeDir()) {
		return value
	}
	return ""
}

// gitHooksCapability reports the installed git version and whether it has
// config-based hooks (hook.<name>.command/.event, git 2.54). The probe uses
// its own non-dry-run runner: under `dot check` or `apply --dry-run` the
// caller's runner never executes, which would misreport every host as
// unsupported.
func (m *CoauthorGuardManager) gitHooksCapability() (version string, supported bool) {
	res, err := dotexec.NewProbeRunner().Run(context.Background(), "git", "version")
	if err != nil {
		return "", false
	}
	version = strings.TrimSpace(res.Stdout)
	return version, gitVersionAtLeast(version, 2, 54)
}

var gitVersionPattern = regexp.MustCompile(`^git version (\d+)\.(\d+)`)

func gitVersionAtLeast(version string, major, minor int) bool {
	match := gitVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return false
	}
	gotMajor, err1 := strconv.Atoi(match[1])
	gotMinor, err2 := strconv.Atoi(match[2])
	if err1 != nil || err2 != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

// hookConfigDrift reports whether the [hook "coauthor-guard"] table points at
// the guard. The command may be the tilde form dot writes or the absolute
// path an operator wrote by hand; both normalize to the same file.
func hookConfigDrift(content, home string) string {
	table := findHookTable(content)
	if table == nil {
		return "missing"
	}
	command := gitConfigValue(*table, `hook "coauthor-guard"`, "command")
	event := gitConfigValue(*table, `hook "coauthor-guard"`, "event")
	if normalizeGitPath(command, home) == normalizeGitPath(coauthorGuardHookCommand, home) && event == "commit-msg" {
		return "in-sync"
	}
	return "out-of-sync"
}

// findHookTable returns the config body narrowed to the
// [hook "coauthor-guard"] table so gitConfigValue can read its keys.
func findHookTable(content string) *string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	start, end := findTOMLTable(lines, `hook "coauthor-guard"`)
	if start < 0 {
		return nil
	}
	body := strings.Join(lines[start:end], "\n")
	return &body
}

// patchGitHookConfig ensures the [hook "coauthor-guard"] table and removes a
// dot-managed core.hooksPath. A non-dot core.hooksPath is preserved: config
// hooks run in addition to it. When supported is false (git < 2.54) only the
// migration runs — never fall back to setting core.hooksPath.
func patchGitHookConfig(content, home string, supported bool) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	// Migration: drop the dot-managed core.hooksPath key.
	if start, end := findTOMLTable(lines, "core"); start >= 0 {
		for _, key := range []string{"hooksPath", "hookspath"} {
			keyStart, keyEnd := findTOMLKey(lines, start+1, end, key)
			if keyStart < 0 {
				continue
			}
			if normalizeGitPath(gitConfigLineValue(lines[keyStart]), home) != normalizeGitPath(coauthorGuardHooksRelPath, home) {
				continue // not dot's; leave it
			}
			next := append([]string{}, lines[:keyStart]...)
			next = append(next, lines[keyEnd:]...)
			lines = next
			break
		}
	}

	if !supported {
		return strings.Join(lines, "\n") + "\n"
	}

	desiredCommand := "    command = " + coauthorGuardHookCommand
	desiredEvent := "    event = commit-msg"
	start, end := findTOMLTable(lines, `hook "coauthor-guard"`)
	if start < 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		return strings.Join(append(lines, `[hook "coauthor-guard"]`, desiredCommand, desiredEvent), "\n") + "\n"
	}
	// Rewrite the table's keys in place; an operator's hand-written absolute
	// command path is kept as-is when it already points at the hook.
	if ks, _ := findTOMLKey(lines, start+1, end, "command"); ks >= 0 {
		if normalizeGitPath(gitConfigLineValue(lines[ks]), home) != normalizeGitPath(coauthorGuardHookCommand, home) {
			lines[ks] = desiredCommand
		}
	} else {
		next := append([]string{}, lines[:end]...)
		next = append(next, desiredCommand)
		lines = append(next, lines[end:]...)
		end++
	}
	if ks, _ := findTOMLKey(lines, start+1, end, "event"); ks >= 0 {
		lines[ks] = desiredEvent
	} else {
		next := append([]string{}, lines[:end]...)
		next = append(next, desiredEvent)
		lines = append(next, lines[end:]...)
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m *CoauthorGuardManager) agentsInstructionDrift() string {
	data, err := os.ReadFile(m.SSOTPath())
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "error"
	}
	if strings.Contains(string(data), coauthorGuardStart) &&
		strings.Contains(string(data), "Co-authored-by") &&
		strings.Contains(string(data), "commit messages in English") {
		return "in-sync"
	}
	return "out-of-sync"
}

func (m *CoauthorGuardManager) ensureAgentsInstruction() error {
	path := m.SSOTPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, err := NewAgentsManager(m.runner(), m.homeDir(), m.ExplicitHome).Init(InitOptions{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next := patchAgentsCoauthorInstruction(string(data))
	if next == string(data) {
		return nil
	}
	return m.runner().WriteFile(path, []byte(next), 0o644)
}

func patchAgentsCoauthorInstruction(content string) string {
	block := coauthorGuardStart + "\n" +
		"- Do not add `Co-authored by` or `Co-authored-by:` commit trailers unless the user explicitly requests them. If another hook or tool proposes one, surface it before committing.\n" +
		"- Always write git commit messages in English, regardless of the conversation language.\n" +
		coauthorGuardEnd
	content = strings.TrimRight(content, "\r\n")
	start := strings.Index(content, coauthorGuardStart)
	end := strings.Index(content, coauthorGuardEnd)
	if start >= 0 && end >= start {
		end += len(coauthorGuardEnd)
		return strings.TrimRight(content[:start]+block+content[end:], "\n") + "\n"
	}
	lines := strings.Split(content, "\n")
	sectionStart, sectionEnd := findMarkdownSection(lines, "Tool-Specific Notes")
	if sectionStart >= 0 {
		next := append([]string{}, lines[:sectionEnd]...)
		if sectionEnd > sectionStart+1 && strings.TrimSpace(lines[sectionEnd-1]) != "" {
			next = append(next, "")
		}
		next = append(next, block)
		next = append(next, lines[sectionEnd:]...)
		return strings.TrimRight(strings.Join(next, "\n"), "\n") + "\n"
	}
	if content == "" {
		return "## Tool-Specific Notes\n\n" + block + "\n"
	}
	return content + "\n\n## Tool-Specific Notes\n\n" + block + "\n"
}

func coauthorGuardHookScript(mode string) string {
	mode, _ = NormalizeCoauthorGuardMode(mode)
	return fmt.Sprintf(`#!/bin/sh
# Managed by dot ai coauthor-guard. Edit dotfiles config, not this file.
msg_file="$1"
[ -n "$DOTFILES_COAUTHOR_GUARD_ALLOW" ] && exit 0
[ -n "$msg_file" ] && [ -f "$msg_file" ] || exit 0

if grep -Eiq '^[[:space:]]*Co-authored[ -]by[[:space:]]*:?' "$msg_file"; then
  cat >&2 <<'EOF'
dotfiles coauthor guard: commit message contains a Co-authored trailer.
AI agents and hooks should not add Co-authored by / Co-authored-by trailers unless explicitly requested.
Set DOTFILES_COAUTHOR_GUARD_ALLOW=1 for a one-off bypass.
EOF
  [ %q = "block" ] && exit 1
fi
exit 0
`, mode)
}

// PreserveForeignHooksPath carries a core.hooksPath dot does not manage from
// the existing git config into the rendered one: the git module rewrites
// ~/.config/git/config wholesale from its template, and config-based hooks
// coexist with a foreign hooksPath, so an apply must not drop it. A
// dot-managed value is not preserved — removing it is the migration.
func PreserveForeignHooksPath(rendered, existing, home string) string {
	value := gitConfigValue(existing, "core", "hooksPath")
	if value == "" || normalizeGitPath(value, home) == normalizeGitPath(coauthorGuardHooksRelPath, home) {
		return rendered
	}
	if gitConfigValue(rendered, "core", "hooksPath") != "" {
		return rendered
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(rendered, "\r\n", "\n"), "\n"), "\n")
	desired := "    hooksPath = " + value
	start, end := findTOMLTable(lines, "core")
	if start < 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		return strings.Join(append(lines, "[core]", desired), "\n") + "\n"
	}
	next := append([]string{}, lines[:end]...)
	next = append(next, desired)
	next = append(next, lines[end:]...)
	return strings.Join(next, "\n") + "\n"
}

// gitConfigLineValue extracts the value of one `key = value` config line,
// stripping quotes and a trailing comment, mirroring gitConfigValue's regex
// for callers that already located the line.
func gitConfigLineValue(line string) string {
	pattern := regexp.MustCompile(`^\s*[^=]+?=\s*(.+?)\s*(#.*)?$`)
	match := pattern.FindStringSubmatch(line)
	if len(match) < 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(match[1]), `"'`)
}

func gitConfigValue(content, table, key string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	start, end := findTOMLTable(lines, table)
	if start < 0 {
		return ""
	}
	pattern := regexp.MustCompile(`(?i)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(.+?)\s*(#.*)?$`)
	for i := start + 1; i < end; i++ {
		match := pattern.FindStringSubmatch(lines[i])
		if len(match) > 1 {
			return strings.Trim(strings.TrimSpace(match[1]), `"'`)
		}
	}
	return ""
}

func normalizeGitPath(path, home string) string {
	path = strings.Trim(strings.TrimSpace(path), `"'`)
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	return filepath.Clean(path)
}

func firstWord(s, fallback string) string {
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

func (m *CoauthorGuardManager) runner() *dotexec.Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return dotexec.NewRunner(false, slog.Default())
}

func (m *CoauthorGuardManager) homeDir() string {
	if m.HomeDir != "" {
		return m.HomeDir
	}
	home, _ := os.UserHomeDir()
	return home
}

func (m *CoauthorGuardManager) SSOTPath() string {
	return filepath.Join(m.homeDir(), AgentsSSOTRelPath, AgentsSSOTName)
}

func (m *CoauthorGuardManager) hookPath() string {
	return filepath.Join(m.homeDir(), ".config", "git", "hooks", "commit-msg")
}

func (m *CoauthorGuardManager) gitConfigPath() string {
	return filepath.Join(m.homeDir(), ".config", "git", "config")
}
