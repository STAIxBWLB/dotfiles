package cli

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
)

// defaultSecretsBackupDest derives the secrets backup destination from the
// shared backup root (which now prefers Dropbox via DetectCloudCandidate),
// matching the one-stop wizard's <root>/secrets-age/<host> layout. Used when
// `dot secrets backup` is called without an explicit destination.
func defaultSecretsBackupDest(cmd *cobra.Command, ses *secretsSession) (string, error) {
	// Only this branch needs state: it is what names the root. An explicit
	// destination is resolved without ever reading the state file.
	state, err := ses.requireState()
	if err != nil {
		return "", err
	}
	// resolveBackupRoot reads --to/--from (not registered here — the guards
	// skip them safely), then state.BackupRoot, then cloud-detect, then local.
	root := resolveBackupRoot(cmd, state, ses.Home)
	host, _ := os.Hostname()
	if i := strings.Index(host, "."); i > 0 {
		host = host[:i]
	}
	return filepath.Join(root, "secrets-age", host), nil
}

// newSecretsBackupCmd copies *.age files to a destination directory.
func newSecretsBackupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backup [destination]",
		Short: "Copy encrypted secrets to a destination directory",
		Long: `Copy the encrypted *.age files from the local store to a destination.

With no destination, defaults to <backup-root>/secrets-age/<host> — the
same cloud root (Dropbox-preferred) the rest of dot backs up to.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ses, err := secretsSessionFrom(cmd)
			if err != nil {
				return err
			}
			storeDir := ses.StoreDir

			runner := secretsRunner(cmd)
			p := printerFrom(cmd)

			var dest string
			if len(args) == 1 {
				dest = args[0]
			} else {
				dest, err = defaultSecretsBackupDest(cmd, ses)
				if err != nil {
					return err
				}
				p.Line("Destination (default): %s", dest)
			}

			if _, err := os.Stat(storeDir); os.IsNotExist(err) {
				p.Line("No secrets store found. Run 'dot secrets init' first.")
				return nil
			}

			res, err := secrets.Backup(secrets.BackupOptions{
				Runner:   runner,
				StoreDir: storeDir,
				Dest:     dest,
				Progress: renderSecretsEvent(p),
			})
			if err != nil {
				return err
			}
			if res.Copied == 0 {
				p.Line("No .age files found to backup.")
				return nil
			}
			p.Line("Backup complete: %d file(s) -> %s", res.Copied, dest)

			// Record last-backup location (skip in dry-run — nothing was copied).
			if runner.DryRun {
				return nil
			}
			absDest, err := filepath.Abs(dest)
			if err != nil {
				absDest = dest
			}
			// Re-read and write back through the session: a backup run under
			// --home must record in the TARGET home's state file, not the
			// invoking user's (BUG-08).
			state, err := ses.loadState()
			if err != nil {
				p.Warn("warning: could not load state to record backup: %v", err)
				return nil
			}
			state.Secrets.LastBackup = &config.BackupRecord{
				Path:  absDest,
				Time:  time.Now(),
				Files: res.Copied,
			}
			if err := ses.saveState(state); err != nil {
				p.Warn("warning: could not save last-backup record: %v", err)
			}
			return nil
		},
	}
}
