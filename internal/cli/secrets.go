package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage encrypted secrets",
		Long:  "Encrypt, backup, restore, and inspect dot secrets using age.",
	}

	cmd.AddCommand(newSecretsInitCmd())
	cmd.AddCommand(newSecretsBackupCmd())
	cmd.AddCommand(newSecretsRestoreCmd())
	cmd.AddCommand(newSecretsStatusCmd())
	cmd.AddCommand(newSecretsListCmd())

	return cmd
}

// secretsRunner returns a runner suitable for secrets operations.
// Honors the global --dry-run flag: when dry-run, commands are printed but not executed.
func secretsRunner(cmd *cobra.Command) *exec.Runner {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	return exec.NewRunner(dryRun, logger)
}

// renderSecretsEvent turns one engine step outcome into the line the report
// shows. Every string a secrets flow prints is written here, so the engine
// stays free of presentation and the three call sites (secrets, the one-stop
// backup, the one-stop restore) cannot drift apart.
func renderSecretsEvent(p *Printer) func(secrets.Event) {
	return func(ev secrets.Event) {
		switch ev.Kind {
		case secrets.EventVerificationSkipped:
			p.Warn("  decrypt verification skipped: %s — restore from this archive is unverified", ev.Reason)
		case secrets.EventScaffoldPreview:
			p.Line("  [dry-run] Would scaffold: %s (0600)", ev.Path)
		case secrets.EventScaffolded:
			p.Line("  Scaffolded: %s (0600)", ev.Path)
		case secrets.EventPlaintextMissing:
			p.Line("  %s not found, skipping: %s", ev.Label, ev.Path)
		case secrets.EventEncrypted:
			p.Line("  Encrypted: %s -> %s", ev.Path, ev.Dest)
		case secrets.EventArchiveCopied:
			p.Line("  Copied: %s", ev.Name)
		case secrets.EventArchiveMissing:
			p.Line("  %s archive not found, skipping: %s", ev.Label, ev.Path)
		case secrets.EventRestored:
			p.Line("  Restored: %s", ev.Path)
		case secrets.EventRestoreBackup:
			p.Line("  Backup:   %s", ev.Path)
		case secrets.EventRestoreUnchanged:
			p.Line("  Unchanged: %s", ev.Path)
		case secrets.EventRestoreSkipped:
			p.Warn("  Skipped (declined overwrite): %s", ev.Path)
		case secrets.EventUnmatchedArchives:
			p.Warn("  %d archive(s) in %s matched no known secret and were NOT restored: %s",
				len(ev.Names), ev.Path, strings.Join(ev.Names, ", "))
			p.Warn("  (an SSH key from another host? set ssh.key_name accordingly and re-run restore)")
		}
	}
}

// confirmSecrets answers the engine's yes/no questions. The wording and the
// unattended (--yes) policy live here, never in the engine (D-09).
func confirmSecrets(unattended bool) func(secrets.ConfirmKind, string) (bool, error) {
	return func(kind secrets.ConfirmKind, dest string) (bool, error) {
		if kind != secrets.ConfirmOverwriteRestore {
			return false, fmt.Errorf("unknown secrets confirmation %d", kind)
		}
		return ui.Confirm(fmt.Sprintf(
			"%s exists and differs — overwrite? (a timestamped .bak copy will be saved)", dest), unattended)
	}
}

// newSecretsInitCmd encrypts local secrets files with age.
func newSecretsInitCmd() *cobra.Command {
	var scaffold bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Encrypt SSH key and shell secrets with age",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ses, err := secretsSessionFrom(cmd)
			if err != nil {
				return err
			}
			state, err := ses.requireState()
			if err != nil {
				return err
			}

			// Checked before any path lookup: a state with no recipients
			// cannot produce a decryptable archive under any of them.
			if len(state.Secrets.AgeRecipients) == 0 {
				return fmt.Errorf("no age recipients configured; set secrets.age_recipients in state")
			}

			p := printerFrom(cmd)
			if err := secrets.Init(context.Background(), secrets.InitOptions{
				Runner:   secretsRunner(cmd),
				State:    state,
				Home:     ses.Home,
				StoreDir: ses.StoreDir,
				Scaffold: scaffold,
				Progress: renderSecretsEvent(p),
			}); err != nil {
				return err
			}

			p.Line("Done. Run 'dot secrets list' to verify.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&scaffold, "scaffold", false, "Create empty ~/.config/shell/90-secrets.sh template (0600) if missing")
	return cmd
}

// newSecretsRestoreCmd decrypts secrets from a source directory.
func newSecretsRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <source>",
		Short: "Decrypt secrets from a source directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ses, err := secretsSessionFrom(cmd)
			if err != nil {
				return err
			}
			state, err := ses.requireState()
			if err != nil {
				return err
			}

			p := printerFrom(cmd)
			yes, _ := cmd.Flags().GetBool("yes")

			if _, err := secrets.Restore(context.Background(), secrets.RestoreOptions{
				Runner:   secretsRunner(cmd),
				State:    state,
				Home:     ses.Home,
				Src:      args[0],
				Confirm:  confirmSecrets(yes),
				Progress: renderSecretsEvent(p),
			}); err != nil {
				return err
			}
			p.Line("Restore complete.")
			return nil
		},
	}
}

// newSecretsStatusCmd checks whether plaintext and encrypted secrets files exist.
func newSecretsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check status of secrets files",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ses, err := secretsSessionFrom(cmd)
			if err != nil {
				return err
			}
			state, err := ses.requireState()
			if err != nil {
				return err
			}

			res, err := secrets.Status(secrets.StatusOptions{
				Runner:   secretsRunner(cmd),
				State:    state,
				Home:     ses.Home,
				StoreDir: ses.StoreDir,
			})
			if err != nil {
				return err
			}

			p := printerFrom(cmd)
			checkFile := func(f secrets.FileStatus) {
				mark := "missing"
				if f.Present {
					mark = "present"
				}
				p.Line("  %-30s  %s", f.Label, mark)
			}

			p.Line("Plaintext files:")
			for _, f := range res.Plaintext {
				checkFile(f)
			}

			p.Line("")
			p.Line("Encrypted files:")
			for _, f := range res.Encrypted {
				checkFile(f)
			}

			p.Line("")
			p.Line("  Age identity: %s", res.AgeIdentity)
			if len(state.Secrets.AgeRecipients) > 0 {
				p.Line("  Age recipients:")
				for _, r := range state.Secrets.AgeRecipients {
					p.Line("    %s", r)
				}
			} else {
				p.Line("  Age recipients: (none configured)")
			}

			p.Line("")
			if lb := state.Secrets.LastBackup; lb != nil && lb.Path != "" {
				p.Line("Last backup:")
				p.Line("  Path:  %s", lb.Path)
				p.Line("  When:  %s (%s ago)", lb.Time.Format(time.RFC3339), humanDuration(time.Since(lb.Time)))
				p.Line("  Files: %d", lb.Files)
			} else {
				p.Line("Last backup: (none recorded)")
			}

			return nil
		},
	}
}

// humanDuration formats a duration as a short human-readable string.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// newSecretsListCmd lists encrypted files in the secrets store.
func newSecretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List encrypted secrets files",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ses, err := secretsSessionFrom(cmd)
			if err != nil {
				return err
			}

			res, err := secrets.List(secrets.ListOptions{StoreDir: ses.StoreDir})
			p := printerFrom(cmd)
			if err != nil {
				return err
			}
			if res.Missing {
				p.Line("No secrets store found. Run 'dot secrets init' first.")
				return nil
			}

			p.Line("Secrets store: %s\n", ses.StoreDir)
			for _, e := range res.Entries {
				p.Line("  %-30s  %d bytes", e.Name, e.Size)
			}
			if len(res.Entries) == 0 {
				p.Line("  (empty)")
			}
			return nil
		},
	}
}
