package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

var upgradeExecutable = os.Executable

func standaloneDotRoot(home string) string {
	return filepath.Join(home, ".local", "share", "dotfiles", "tools", "dot")
}

func standaloneDotBinary(home, version string) string {
	return filepath.Join(standaloneDotRoot(home), version, "bin", "dot")
}

func homebrewVersionControlError(brew homebrewDot) error {
	return fmt.Errorf("Homebrew owns this dot version; --version and rollback cannot change it; use `%s upgrade %s` for the formula version or `brew extract --version=<version> %s <your-tap>` to manage an older release with Homebrew", brew.brew(), brew.formula, brew.formula)
}

func newRollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Activate the previous standalone dot binary",
		Long: `Restore the previous standalone dot binary without downloading.

Versions live under ~/.local/share/dotfiles/tools/dot/; current and previous
are switched by atomic link renames. Existing guard hooks keep their stable
path. The version being left becomes previous, so another rollback restores it.
Homebrew installs must manage versions through brew instead.`,
		Args: cobra.NoArgs,
		RunE: runDotRollback,
	}
	cmd.Flags().Duration("wait", 0, "wait up to this long for the maintenance slot instead of deferring (e.g. 30m)")
	return cmd
}

func runDotRollback(cmd *cobra.Command, _ []string) error {
	self, err := upgradeExecutable()
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return err
	}
	if brew, ok := homebrewDotFor(resolved); ok {
		return homebrewVersionControlError(brew)
	}
	home := homeFromCmd(cmd)
	p := printerFrom(cmd)
	wait, _ := cmd.Flags().GetDuration("wait")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		prev, err := dotVersionLink(home, "previous")
		if err != nil {
			return err
		}
		p.Line("[dry-run] would activate dot %s", prev)
		return nil
	}
	// Rollback does no download. It takes the ordinary maintenance slot, not
	// the exception reserved for a newly verified release archive.
	release, err := acquireMaintenanceSlot(cmd.Context(), p, wait, func(ctx context.Context) (func(), error) {
		return upgradeAcquire(ctx, resourceguard.Options{HomeDir: home, Purpose: "dot rollback", ScopeKey: "tooling"})
	})
	if err != nil {
		return fmt.Errorf("dot rollback needs the maintenance slot: %w; %s", err, retrySlotHint(wait))
	}
	defer release()
	prev, err := dotVersionLink(home, "previous")
	if err != nil {
		return err
	}
	current, err := dotVersionLink(home, "current")
	if err != nil {
		return err
	}
	goos, goarch, err := supportedNativeReleaseTarget()
	if err != nil {
		return err
	}
	if err := verifySavedDot(cmd.Context(), home, prev, goos, goarch); err != nil {
		return fmt.Errorf("saved dot %s is invalid: %w; run dot update --version X.Y.Z to install a verified release", prev, err)
	}
	if err := switchDotVersion(home, current, prev); err != nil {
		return err
	}
	p.Line("Activated dot %s (previous: %s)", prev, current)
	return nil
}

func dotVersionLink(home, name string) (string, error) {
	version, err := os.Readlink(filepath.Join(standaloneDotRoot(home), name))
	if err != nil || !validSavedDotVersion(version) {
		return "", fmt.Errorf("dot has no valid %s version; run dot update to create a versioned standalone install", name)
	}
	return version, nil
}

func validSavedDotVersion(version string) bool {
	if stableReleaseTag.MatchString("v" + version) {
		return true
	}
	// A development binary is retained by content hash rather than pretending
	// it is a published version. No arbitrary path components are accepted.
	digest, ok := strings.CutPrefix(version, "snapshot-")
	if !ok || len(digest) != 64 {
		return false
	}
	for _, ch := range digest {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func verifySavedDot(ctx context.Context, home, version, goos, goarch string) error {
	binary := standaloneDotBinary(home, version)
	if strings.HasPrefix(version, "snapshot-") {
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		if fmt.Sprintf("snapshot-%x", sha256.Sum256(data)) != version {
			return fmt.Errorf("saved snapshot checksum changed")
		}
		return verifyNativeBinaryTarget(binary, goos, goarch)
	}
	return verifyExactPrebuiltBinary(ctx, binary, version, goos, goarch)
}

// stageDotBinary never overwrites an existing version, including one still in
// use by a running process. A conflicting file is an error, not an update.
func stageDotBinary(home, version string, data []byte) error {
	dir := filepath.Join(standaloneDotRoot(home), version)
	if existing, err := os.ReadFile(filepath.Join(dir, "bin", "dot")); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("dot %s already exists with different bytes; inspect %s before retrying", version, dir)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(standaloneDotRoot(home), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(standaloneDotRoot(home), ".staging-"+version+"-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.Mkdir(filepath.Join(staging, "bin"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, "bin", "dot"), data, 0o755); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

// activateStandalonePrebuilt is called only after native/version/checksum
// validation and admission. Prepare all entry points against the old current
// first, then activate the selected version with one final current rename.
func activateStandalonePrebuilt(ctx context.Context, p *Printer, home string, data []byte, destination, version string) error {
	stable := filepath.Join(home, ".local", "bin", "dot")
	// Do not silently adopt another standalone or Homebrew install on PATH.
	if _, err := os.Lstat(stable); err == nil {
		got, resolveErr := filepath.EvalSymlinks(stable)
		if resolveErr != nil || got != destination {
			return fmt.Errorf("%s belongs to another install; run that dot's update command or remove the conflicting PATH entry before retrying", stable)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	cur, linkErr := dotVersionLink(home, "current")
	if linkErr != nil {
		// Refuse a malformed existing link rather than overwrite foreign state.
		if _, err := os.Lstat(filepath.Join(standaloneDotRoot(home), "current")); !os.IsNotExist(err) {
			return linkErr
		}
		old, err := os.ReadFile(destination)
		if err != nil {
			return fmt.Errorf("preserving previous dot: %w", err)
		}
		cur, err = readDotBinaryVersion(ctx, destination)
		if err != nil || !stableReleaseTag.MatchString("v"+cur) {
			cur = fmt.Sprintf("snapshot-%x", sha256.Sum256(old))
		}
		if err := stageDotBinary(home, cur, old); err != nil {
			return err
		}
	}
	if err := stageDotBinary(home, version, data); err != nil {
		return err
	}
	if linkErr != nil {
		if err := aitooling.SwapInstallLink(filepath.Join(standaloneDotRoot(home), "current"), cur); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		return err
	}
	// Preserve legacy absolute hook/launchd paths when adopting a flat install.
	// Never replace an immutable version file when invoked through current.
	managed := standaloneDotBinary(home, cur)
	if destination != managed && !strings.HasPrefix(destination, standaloneDotRoot(home)+string(os.PathSeparator)) {
		if err := aitooling.SwapInstallLink(destination, standaloneDotBinary(home, "current")); err != nil {
			return err
		}
	}
	if err := aitooling.SwapInstallLink(stable, filepath.Join("..", "share", "dotfiles", "tools", "dot", "current", "bin", "dot")); err != nil {
		return err
	}
	if err := switchDotVersion(home, cur, version); err != nil {
		return err
	}
	p.Line("Activated dot %s (previous: %s)", version, cur)
	p.Line("Binary: %s", stable)
	return nil
}

func switchDotVersion(home, current, selected string) error {
	if current == selected {
		return nil
	}
	root := standaloneDotRoot(home)
	if err := aitooling.SwapInstallLink(filepath.Join(root, "previous"), current); err != nil {
		return err
	}
	return aitooling.SwapInstallLink(filepath.Join(root, "current"), selected)
}
