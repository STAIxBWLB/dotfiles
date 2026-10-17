package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/profilesnap"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// onestopCtx carries the shared session for the one-stop `dot backup` /
// `dot restore` wizards. The root is resolved once and then pinned: even
// when a restored config.yaml carries another machine's backup_root, the
// session keeps operating on the root the user confirmed.
type onestopCtx struct {
	cmd    *cobra.Command
	p      *Printer
	state  *config.UserState
	home   string
	host   string // engine host scope (the source host during restore)
	root   string // pinned backup root for the whole session
	runner *exec.Runner
	yes    bool
	dryRun bool
}

func newOnestopCtx(cmd *cobra.Command) (*onestopCtx, error) {
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
		return nil, fmt.Errorf("load state: %w", err)
	}
	home, _ := os.UserHomeDir()
	if homeOverride != "" {
		home = homeOverride
	}
	hostname, _ := os.Hostname()
	if idx := strings.Index(hostname, "."); idx > 0 {
		hostname = hostname[:idx]
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return &onestopCtx{
		cmd:    cmd,
		p:      printerFrom(cmd),
		state:  state,
		home:   home,
		host:   hostname,
		root:   resolveBackupRoot(cmd, state, home),
		runner: exec.NewRunner(dryRun, logger),
		yes:    yes,
		dryRun: dryRun,
	}, nil
}

func (o *onestopCtx) statePath() string {
	if over, _ := o.cmd.Flags().GetString("home"); over != "" {
		return config.StatePathForHome(over)
	}
	return config.StatePath()
}

// reloadState re-reads the state file — used after a profile restore
// rewrote config.yaml so later steps see the restored configuration.
func (o *onestopCtx) reloadState() error {
	var state *config.UserState
	var err error
	if over, _ := o.cmd.Flags().GetString("home"); over != "" {
		state, err = config.LoadStateForHome(over)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return fmt.Errorf("reload state: %w", err)
	}
	o.state = state
	return nil
}

// loadStateFromSnapshot loads in-memory state directly from a snapshot's
// config.yaml. Used in dry-run restore, where the profile step does not
// write config.yaml, so reloadState would re-read the un-restored local
// state and make later previews (secrets identity, app list) misleading.
func (o *onestopCtx) loadStateFromSnapshot(configPath string) error {
	if configPath == "" {
		return nil
	}
	state, err := config.LoadStateFrom(configPath)
	if err != nil {
		return fmt.Errorf("load snapshot state: %w", err)
	}
	o.state = state
	return nil
}

// --- engine builders pinned to the session root + host ---

func (o *onestopCtx) profileEngine() *profilesnap.Engine {
	return &profilesnap.Engine{
		Runner:     o.runner,
		HomeDir:    o.home,
		Root:       o.root,
		Hostname:   o.host,
		User:       os.Getenv("USER"),
		StatePath:  o.statePath(),
		SecretsDir: filepath.Join(o.home, ".ssh"),
	}
}

func (o *onestopCtx) appsEngine() (*appsettings.Engine, error) {
	mf, err := appsettings.LoadManifest()
	if err != nil {
		return nil, err
	}
	return &appsettings.Engine{
		Runner:   o.runner,
		HomeDir:  o.home,
		Root:     o.root,
		Hostname: o.host,
		Manifest: mf,
	}, nil
}

func (o *onestopCtx) aiEngine() *aisettings.Engine {
	return &aisettings.Engine{
		Runner:   o.runner,
		HomeDir:  o.home,
		Root:     o.root,
		Hostname: o.host,
		User:     os.Getenv("USER"),
	}
}

// secretsArchiveDir is where one-stop backups place the encrypted .age
// payloads, sibling to the other per-host trees.
func (o *onestopCtx) secretsArchiveDir() string {
	return filepath.Join(o.root, "secrets-age", o.host)
}

// secretsStoreDir is the local encrypted store, resolved against the
// session home so --home overrides reach the secrets domain too. The
// standalone `dot secrets` subcommands now join the same constant against
// their own session home (BUG-08).
func (o *onestopCtx) secretsStoreDir() string {
	return filepath.Join(o.home, secrets.StoreDirRel)
}

// rootSource explains where the initial root came from, for the wizard's
// root-confirmation display.
func (o *onestopCtx) rootSource() string {
	if v, err := o.cmd.Flags().GetString("to"); err == nil && v != "" {
		return "--to flag"
	}
	if v, err := o.cmd.Flags().GetString("from"); err == nil && v != "" {
		return "--from flag"
	}
	if o.state.Modules.MacApps.BackupRoot != "" {
		return "state (backup_root)"
	}
	if appsettings.DetectCloudCandidate(o.home) != "" {
		return "auto-detected (cloud)"
	}
	return "local default"
}

// confirmRoot shows the resolved root and lets the user adjust it. When
// offerSave is true (backup), a newly chosen root may be persisted to
// state; restore never writes state before its preflight has passed.
func (o *onestopCtx) confirmRoot(offerSave bool) error {
	o.p.KV("Root", o.root)
	o.p.KV("Source", o.rootSource())
	if o.yes {
		return nil
	}
	hadSaved := o.state.Modules.MacApps.BackupRoot != ""
	edited, err := ui.Input("Backup root", o.root, false)
	if err != nil {
		return err
	}
	edited = appsettings.ExpandHome(strings.TrimSpace(edited), o.home)
	if edited == "" {
		edited = o.root
	}
	changed := edited != o.root
	o.root = edited
	if offerSave && changed && !hadSaved {
		if o.dryRun {
			o.p.Line("  %s", ui.StyleHint.Render("[dry-run] would offer to save this root to state"))
		} else {
			save, err := ui.ConfirmBool("Save this root to state for future runs?", true, false)
			if err != nil {
				return err
			}
			if save {
				o.state.Modules.MacApps.BackupRoot = edited
				if err := persistUserState(o.cmd, o.state); err != nil {
					o.p.Warn("  could not save backup root: %v", err)
				}
			}
		}
	}
	return nil
}

// isCloudPath guesses whether a path lives inside a cloud-synced folder —
// used to warn before writing auth tokens there.
func isCloudPath(path string) bool {
	for _, marker := range []string{"CloudStorage", "GoogleDrive", "Google Drive", "My Drive", "Dropbox", "OneDrive"} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

// --- step bookkeeping ---

// onestopStep records one wizard step outcome for the final summary.
type onestopStep struct {
	Name   string
	Detail string
	Err    error
}

// printOnestopSummary renders the ✓/✗ table and returns the failure count.
func printOnestopSummary(p *Printer, steps []onestopStep) int {
	p.Header("Summary")
	failed := 0
	for _, s := range steps {
		marker := ui.StyleSuccess.Render(ui.MarkPresent)
		detail := s.Detail
		if s.Err != nil {
			failed++
			marker = ui.StyleError.Render(ui.MarkFail)
			detail = s.Err.Error()
		}
		p.Bullet(marker, fmt.Sprintf("%-8s %s", ui.StyleValue.Render(s.Name), ui.StyleHint.Render(detail)))
	}
	p.Blank()
	return failed
}

// parseScope validates a --scope CSV against the available domains.
func parseScope(raw string, available []string) ([]string, error) {
	avail := make(map[string]bool, len(available))
	for _, s := range available {
		avail[s] = true
	}
	seen := make(map[string]bool)
	var out []string
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" || seen[s] {
			continue
		}
		if !avail[s] {
			return nil, fmt.Errorf("unknown or unavailable scope %q (available: %s)", s, strings.Join(available, ","))
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--scope selected nothing (available: %s)", strings.Join(available, ","))
	}
	return out, nil
}
