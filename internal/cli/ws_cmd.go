package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/ws"
)

// newWorkspaceDualCmd returns the `dot ws` command with subcommands for
// dual-workspace (work + cloud mirror) folder operations.
func newWorkspaceDualCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ws",
		Short: "Dual-workspace (work + cloud mirror) folder operations",
		Long: `Operate on both the workspace root and the gsync mirror simultaneously.

Subcommands keep the two trees in structural sync:
  init       Clone configured workspace repos (recursive)
  mkdir      Create a folder on both sides
  mv         Rename/move on both sides
  rm         Remove on both sides (use --recursive for non-empty)
  audit      Report structural mismatches (read-only)
  reconcile  Interactively resolve mismatches`,
	}
	cmd.AddCommand(newWsInitCmd())
	cmd.AddCommand(newWsMkdirCmd())
	cmd.AddCommand(newWsMvCmd())
	cmd.AddCommand(newWsRmCmd())
	cmd.AddCommand(newWsAuditCmd())
	cmd.AddCommand(newWsReconcileCmd())
	return cmd
}

// wsInitBootstrap loads state for `ws init`. Unlike wsBootstrap it does not
// require the dual-workspace roots to exist — init may need to create them.
func wsInitBootstrap(cmd *cobra.Command) (workspacePath string, repos []config.RepoConfig, runner *exec.Runner, yes bool, vaultPath string, err error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	yes, _ = cmd.Flags().GetBool("yes")
	homeOverride, _ := cmd.Flags().GetString("home")

	var state *config.UserState
	if homeOverride != "" {
		state, err = config.LoadStateForHome(homeOverride)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return "", nil, nil, false, "", fmt.Errorf("loading state: %w", err)
	}

	cfgPath := state.Modules.Workspace.Path
	if cfgPath == "" {
		return "", nil, nil, false, "", fmt.Errorf("workspace.path not configured; run 'dot reconfigure'")
	}
	home, _ := os.UserHomeDir()
	if homeOverride != "" {
		home = homeOverride
	}
	vaultPath = config.ResolveVaultCloneTarget(state.Modules.Workspace.Vault, cfgPath, home)

	if strings.HasPrefix(cfgPath, "~/") {
		cfgPath = filepath.Join(home, cfgPath[2:])
	}
	if strings.HasPrefix(vaultPath, "~/") {
		vaultPath = filepath.Join(home, vaultPath[2:])
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runner = exec.NewRunner(dryRun, logger)
	return cfgPath, state.Modules.Workspace.Repos, runner, yes, vaultPath, nil
}

func newWsInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Clone configured workspace repos recursively",
		Long: `Clone each configured workspace repo (e.g. work, vault) into <workspace.path>/<name>
with --recurse-submodules. The repo named "vault" clones into the configured
vault location instead (workspace.vault; detected as <workspace.path>/work/vault
or <workspace.path>/vault when unset).

Targets that are missing, empty, or contain only a .gdrive symlink are cloned
without --force (the .gdrive symlink is preserved). Populated targets are
skipped unless --force is given, in which case contents are deleted and the
repo is re-cloned.`,
		Args: cobra.NoArgs,
		RunE: runWsInit,
	}
	cmd.Flags().Bool("force", false, "Re-clone over populated targets (destructive)")
	return cmd
}

func runWsInit(cmd *cobra.Command, args []string) error {
	workspacePath, repos, runner, yes, vaultPath, err := wsInitBootstrap(cmd)
	if err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")

	msgs, err := ws.Init(context.Background(), runner, workspacePath, repos, ws.InitOptions{
		Force:     force,
		Yes:       yes,
		VaultPath: vaultPath,
	})
	p := printerFrom(cmd)
	for _, m := range msgs {
		p.Line("%s", m)
	}
	return err
}

// wsBootstrap loads workspace config and builds a Runner.
func wsBootstrap(cmd *cobra.Command) (ws.Roots, *exec.Runner, bool, error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	yes, _ := cmd.Flags().GetBool("yes")
	homeOverride, _ := cmd.Flags().GetString("home")

	var state *config.UserState
	var err error
	if homeOverride != "" {
		state, err = config.LoadStateForHome(homeOverride)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return ws.Roots{}, nil, false, fmt.Errorf("loading state: %w", err)
	}

	if state.Modules.Workspace.Path == "" {
		return ws.Roots{}, nil, false, fmt.Errorf("workspace.path not configured; run 'dot reconfigure'")
	}

	home, _ := os.UserHomeDir()
	if homeOverride != "" {
		home = homeOverride
	}
	expand := func(p string) string {
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}

	gsyncCfg, err := syncer.ResolveConfigReadOnlyForHomeProfile(state, home, syncer.DefaultProfile)
	if err != nil {
		return ws.Roots{}, nil, false, fmt.Errorf("resolving gsync mirror: %w", err)
	}
	mirrorRoot := strings.TrimRight(gsyncCfg.MirrorPath, "/")
	// Legacy dual-workspace configs pointed the mirror at
	// workspace.gdrive_symlink before gsync owned mirror resolution. Honor
	// that key while no gsync mirror is explicitly configured, so existing
	// setups keep targeting their real mirror instead of the gsync default.
	if gsyncCfg.MirrorIsDefault && state.Modules.Workspace.GdriveSymlink != "" {
		mirrorRoot = filepath.Join(expand(state.Modules.Workspace.GdriveSymlink), "work")
	}

	roots := ws.Roots{
		Work:   filepath.Join(expand(state.Modules.Workspace.Path), "work"),
		Gdrive: mirrorRoot,
	}

	if fi, err := os.Stat(roots.Work); err != nil || !fi.IsDir() {
		return ws.Roots{}, nil, false, fmt.Errorf("work root not accessible: %s", roots.Work)
	}
	if fi, err := os.Stat(roots.Gdrive); err != nil || !fi.IsDir() {
		return ws.Roots{}, nil, false, fmt.Errorf("mirror root not accessible: %s (is the cloud mirror mounted?)", roots.Gdrive)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runner := exec.NewRunner(dryRun, logger)
	return roots, runner, yes, nil
}

// --- Subcommands ---

func newWsMkdirCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mkdir <path>",
		Short: "Create a directory on both workspaces",
		Args:  cobra.ExactArgs(1),
		RunE:  runWsMkdir,
	}
}

func runWsMkdir(cmd *cobra.Command, args []string) error {
	roots, runner, _, err := wsBootstrap(cmd)
	if err != nil {
		return err
	}
	msgs, err := ws.Mkdir(runner, roots, args[0])
	p := printerFrom(cmd)
	for _, m := range msgs {
		p.Line("%s", m)
	}
	return err
}

func newWsMvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mv <src> <dst>",
		Short: "Rename/move a directory on both workspaces",
		Args:  cobra.ExactArgs(2),
		RunE:  runWsMv,
	}
}

func runWsMv(cmd *cobra.Command, args []string) error {
	roots, runner, _, err := wsBootstrap(cmd)
	if err != nil {
		return err
	}
	msgs, err := ws.Move(context.Background(), runner, roots, args[0], args[1])
	p := printerFrom(cmd)
	for _, m := range msgs {
		p.Line("%s", m)
	}
	return err
}

func newWsRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <path>",
		Short: "Remove a directory from both workspaces",
		Args:  cobra.ExactArgs(1),
		RunE:  runWsRm,
	}
	cmd.Flags().BoolP("recursive", "r", false, "Remove non-empty directories")
	return cmd
}

func runWsRm(cmd *cobra.Command, args []string) error {
	roots, runner, yes, err := wsBootstrap(cmd)
	if err != nil {
		return err
	}
	recursive, _ := cmd.Flags().GetBool("recursive")
	p := printerFrom(cmd)

	// Safety confirm for recursive delete (unless --yes)
	if recursive && !yes {
		workAbs, gdriveAbs := roots.ResolvePair(args[0])
		p.Line("Recursively delete:\n  %s\n  %s", workAbs, gdriveAbs)
		ok, err := ui.ConfirmBool("Continue?", false, false)
		if err != nil {
			return err
		}
		if !ok {
			p.Line("Aborted.")
			return nil
		}
	}

	msgs, err := ws.Remove(context.Background(), runner, roots, args[0], recursive)
	for _, m := range msgs {
		p.Line("%s", m)
	}
	return err
}

func newWsAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "audit [scope]",
		Short: "Report structural mismatches between workspaces",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runWsAudit,
	}
}

func runWsAudit(cmd *cobra.Command, args []string) error {
	roots, _, _, err := wsBootstrap(cmd)
	if err != nil {
		return err
	}
	scope := ""
	if len(args) == 1 {
		scope = args[0]
	}

	mismatches, err := ws.Audit(roots, ws.AuditOptions{Scope: scope})
	if err != nil {
		return err
	}

	p := printerFrom(cmd)
	p.Header("Workspace Audit")
	p.KV("Work", roots.Work)
	p.KV("Mirror", roots.Gdrive)
	if scope != "" {
		p.KV("Scope", scope)
	}

	if len(mismatches) == 0 {
		p.Blank()
		p.Success("%s Trees are in sync.", ui.MarkPresent)
		return nil
	}

	var workOnly, gdriveOnly []ws.Mismatch
	for _, m := range mismatches {
		if m.OnlyOn == ws.SideWork {
			workOnly = append(workOnly, m)
		} else {
			gdriveOnly = append(gdriveOnly, m)
		}
	}

	if len(workOnly) > 0 {
		p.Section(fmt.Sprintf("Only on work (%d)", len(workOnly)))
		for _, m := range workOnly {
			printMismatch(p, m)
		}
	}
	if len(gdriveOnly) > 0 {
		p.Section(fmt.Sprintf("Only on mirror (%d)", len(gdriveOnly)))
		for _, m := range gdriveOnly {
			printMismatch(p, m)
		}
	}
	p.Blank()
	p.Line("  %d mismatch(es). Run 'dot ws reconcile' to resolve.", len(mismatches))
	return nil
}

func printMismatch(p *Printer, m ws.Mismatch) {
	tag := ui.StyleHint.Render("(empty)")
	if !m.IsEmpty {
		tag = ui.StyleHint.Render(fmt.Sprintf("(%s)", ws.FormatSize(m.Size)))
	}
	p.Bullet(ui.StyleHint.Render(ui.MarkPartial),
		fmt.Sprintf("%s  %s", ui.StyleValue.Render(m.RelPath), tag))
}

func newWsReconcileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile [scope]",
		Short: "Interactively resolve workspace mismatches",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runWsReconcile,
	}
}

func runWsReconcile(cmd *cobra.Command, args []string) error {
	roots, runner, yes, err := wsBootstrap(cmd)
	if err != nil {
		return err
	}
	scope := ""
	if len(args) == 1 {
		scope = args[0]
	}

	mismatches, err := ws.Audit(roots, ws.AuditOptions{Scope: scope})
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	if len(mismatches) == 0 {
		p.Line("%s", ui.StyleSuccess.Render("✓ Trees are in sync. Nothing to reconcile."))
		return nil
	}

	ctx := context.Background()
	var copied, deleted, skipped int
	for i, m := range mismatches {
		srcSide := m.OnlyOn
		otherSide := srcSide.Other()
		tag := "empty"
		if !m.IsEmpty {
			tag = ws.FormatSize(m.Size)
		}
		p.Section(fmt.Sprintf("[%d/%d] %s — only on %s (%s)",
			i+1, len(mismatches), m.RelPath, srcSide.Name(), tag))

		copyLabel := fmt.Sprintf("Copy to %s", otherSide.Name())
		deleteLabel := fmt.Sprintf("Delete from %s", srcSide.Name())

		var choice string
		if yes {
			// Unattended: always copy (safe — never delete)
			choice = copyLabel
			p.Line("  %s (--yes)", choice)
		} else {
			options := []string{copyLabel, deleteLabel, "Skip", "Quit"}
			choice, err = ui.Select("Action?", options, "Skip", false)
			if err != nil {
				return err
			}
		}

		switch choice {
		case copyLabel:
			srcAbs, dstAbs := resolveSidePair(roots, m.RelPath, srcSide)
			dstParent := filepath.Dir(dstAbs)
			if !fileutil.IsDir(dstParent) {
				if err := runner.MkdirAll(dstParent, 0755); err != nil {
					p.Line("  ⚠ mkdir parent %s: %v", dstParent, err)
					skipped++
					continue
				}
			}
			if _, err := runner.Run(ctx, "cp", "-R", srcAbs, dstAbs); err != nil {
				p.Line("  ⚠ cp failed: %v", err)
				skipped++
				continue
			}
			p.Line("  ✓ copied %s → %s", srcAbs, dstAbs)
			copied++
		case deleteLabel:
			srcAbs, _ := resolveSidePair(roots, m.RelPath, srcSide)
			if !m.IsEmpty {
				p.Line("  About to delete non-empty dir: %s (%s)", srcAbs, ws.FormatSize(m.Size))
				confirm, err := ui.ConfirmBool("Really delete?", false, false)
				if err != nil {
					return err
				}
				if !confirm {
					p.Line("  skipped")
					skipped++
					continue
				}
			}
			if _, err := runner.Run(ctx, "rm", "-rf", srcAbs); err != nil {
				p.Line("  ⚠ rm failed: %v", err)
				skipped++
				continue
			}
			p.Line("  ✓ deleted %s", srcAbs)
			deleted++
		case "Skip":
			skipped++
		case "Quit":
			p.Line("  (quit)")
			goto summary
		}
	}
summary:
	p.Line("")
	p.Line("Reconciled: %d copied, %d deleted, %d skipped (of %d).",
		copied, deleted, skipped, len(mismatches))
	return nil
}

// resolveSidePair returns (srcAbs, dstAbs) where src is on srcSide and dst is on the other side.
func resolveSidePair(roots ws.Roots, rel string, srcSide ws.Side) (string, string) {
	workAbs, gdriveAbs := roots.ResolvePair(rel)
	if srcSide == ws.SideWork {
		return workAbs, gdriveAbs
	}
	return gdriveAbs, workAbs
}
