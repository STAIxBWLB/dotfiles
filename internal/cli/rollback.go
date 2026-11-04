package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

var upgradeExecutable = os.Executable
var dotInstallProcesses = aitooling.ListInstallProcesses

func standaloneDotRoot(home string) string {
	root := filepath.Join(home, ".local", "share", "dotfiles", "tools", "dot")
	// os.Executable/EvalSymlinks returns physical paths (notably /private/var
	// for macOS temp homes). Compare like paths before replacing a flat install.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		return filepath.Join(resolved, ".local", "share", "dotfiles", "tools", "dot")
	}
	return root
}

func standaloneDotBinary(home, version string) string {
	return filepath.Join(standaloneDotRoot(home), version, "bin", "dot")
}

func homebrewVersionControlError(brew homebrewDot) error {
	return fmt.Errorf("this dot version is owned by Homebrew; --version and rollback cannot change it; use `%s upgrade %s` for the formula version or `brew extract --version=<version> %s <your-tap>` to manage an older release with Homebrew", brew.brew(), brew.formula, brew.formula)
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
		return fmt.Errorf("locating the running dot binary: %w; run rollback from an installed standalone dot binary", err)
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolving running dot %s: %w; repair that executable path before retrying rollback", self, err)
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
		return fmt.Errorf("determining the native rollback target: %w; run rollback on a supported native Linux or macOS host", err)
	}
	if err := verifySavedDot(cmd.Context(), home, prev, goos, goarch); err != nil {
		return fmt.Errorf("saved dot %s is invalid: %w; run dot update --version X.Y.Z to install a verified release", prev, err)
	}
	if err := switchDotVersion(home, current, prev); err != nil {
		return err
	}
	p.Line("Activated dot %s (previous: %s)", prev, current)
	pruneDotVersions(cmd.Context(), p, home, explicitUpdateHome(cmd), prev, current, resolved)
	return nil
}

func dotVersionLink(home, name string) (string, error) {
	version, err := os.Readlink(filepath.Join(standaloneDotRoot(home), name))
	if err != nil || !validSavedDotVersion(version) {
		return "", fmt.Errorf("dot has no valid %s version; run dot update --version X.Y.Z with a different release to create a saved previous version", name)
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
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
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
		return dotInstallError("reading saved dot", dir, err)
	}
	if err := os.MkdirAll(standaloneDotRoot(home), 0o755); err != nil {
		return dotInstallError("creating dot install root", standaloneDotRoot(home), err)
	}
	staging, err := os.MkdirTemp(standaloneDotRoot(home), ".staging-"+version+"-")
	if err != nil {
		return dotInstallError("creating dot staging directory", standaloneDotRoot(home), err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.Mkdir(filepath.Join(staging, "bin"), 0o755); err != nil {
		return dotInstallError("creating staged bin directory", staging, err)
	}
	if err := os.WriteFile(filepath.Join(staging, "bin", "dot"), data, 0o755); err != nil {
		return dotInstallError("writing staged dot binary", staging, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return dotInstallError("publishing staged dot version", dir, err)
	}
	return nil
}

// activateStandalonePrebuilt is called only after native/version/checksum
// validation and admission. Prepare all entry points against the old current
// first, then activate the selected version with one final current rename.
func activateStandalonePrebuilt(ctx context.Context, p *Printer, home string, explicitHome bool, data []byte, destination, version string) error {
	runningPath, _ := upgradeExecutable()
	runningPath, _ = filepath.EvalSymlinks(runningPath)
	stable := filepath.Join(home, ".local", "bin", "dot")
	// Do not silently adopt another standalone or Homebrew install on PATH.
	if _, err := os.Lstat(stable); err == nil {
		got, resolveErr := filepath.EvalSymlinks(stable)
		if resolveErr != nil || got != destination {
			return fmt.Errorf("%s belongs to another install; run that dot's update command or remove the conflicting PATH entry before retrying", stable)
		}
	} else if !os.IsNotExist(err) {
		return dotInstallError("inspecting PATH entry", stable, err)
	}
	cur, linkErr := dotVersionLink(home, "current")
	if linkErr != nil {
		// Refuse a malformed existing link rather than overwrite foreign state.
		if _, err := os.Lstat(filepath.Join(standaloneDotRoot(home), "current")); !os.IsNotExist(err) {
			return linkErr
		}
	}
	// make install can replace the PATH link with a new flat binary while
	// leaving saved current intact. Preserve the actual predecessor, not that
	// stale link. Existing version files still stay immutable.
	adoptingFlat := linkErr != nil || destination != standaloneDotBinary(home, cur)
	if adoptingFlat {
		old, err := os.ReadFile(destination)
		if err != nil {
			return dotInstallError("preserving previous dot", destination, err)
		}
		cur, err = readDotBinaryVersion(ctx, destination)
		if err == nil && stableReleaseTag.MatchString("v"+cur) {
			if saved, readErr := os.ReadFile(standaloneDotBinary(home, cur)); readErr == nil && !bytes.Equal(saved, old) {
				cur = "" // same reported release, different development bytes
			}
		}
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
	if adoptingFlat {
		if err := aitooling.SwapInstallLink(filepath.Join(standaloneDotRoot(home), "current"), cur); err != nil {
			return dotInstallError("initializing current link", standaloneDotRoot(home), err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		return dotInstallError("creating PATH directory", filepath.Dir(stable), err)
	}
	// Preserve legacy absolute hook/launchd paths when adopting a flat install.
	// Never replace an immutable version file when invoked through current.
	managed := standaloneDotBinary(home, cur)
	if !explicitHome && destination != managed && !strings.HasPrefix(destination, standaloneDotRoot(home)+string(os.PathSeparator)) {
		if err := aitooling.SwapInstallLink(destination, standaloneDotBinary(home, "current")); err != nil {
			return dotInstallError("linking legacy executable to current", destination, err)
		}
	}
	if err := aitooling.SwapInstallLink(stable, filepath.Join("..", "share", "dotfiles", "tools", "dot", "current", "bin", "dot")); err != nil {
		return dotInstallError("linking PATH entry to current", stable, err)
	}
	if err := switchDotVersion(home, cur, version); err != nil {
		return err
	}
	p.Line("Activated dot %s (previous: %s)", version, cur)
	p.Line("Binary: %s", stable)
	pruneDotVersions(ctx, p, home, explicitHome, version, cur, runningPath)
	return nil
}

func switchDotVersion(home, current, selected string) error {
	if current == selected {
		return nil
	}
	root := standaloneDotRoot(home)
	if err := aitooling.SwapInstallLink(filepath.Join(root, "previous"), current); err != nil {
		return dotInstallError("saving previous link", root, err)
	}
	if err := aitooling.SwapInstallLink(filepath.Join(root, "current"), selected); err != nil {
		return dotInstallError("activating current link", root, err)
	}
	return nil
}

// dotVersions returns only owned install directories, never links or staging.
func dotVersions(home string) ([]string, error) {
	entries, err := os.ReadDir(standaloneDotRoot(home))
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && validSavedDotVersion(entry.Name()) {
			versions = append(versions, entry.Name())
		}
	}
	return versions, nil
}

// The family retention rule handles deletion and ambiguous processes. Native
// dot only supplies its version directories and relevant process-table lines.
func pruneDotVersions(ctx context.Context, p *Printer, home string, explicitHome bool, current, previous, runningPath string) {
	versions, err := dotVersions(home)
	if err != nil {
		p.Line("Kept dot versions: cannot list %s: %v; check permissions before the next update or rollback", standaloneDotRoot(home), err)
		return
	}
	out, err := dotInstallProcesses(ctx)
	if err != nil {
		p.Line("Kept dot versions: process inspection failed: %v; restore ps access before the next update or rollback", err)
		return
	}
	root := standaloneDotRoot(home)
	aliasRoot := filepath.Join(home, ".local", "share", "dotfiles", "tools", "dot")
	stable := filepath.Join(home, ".local", "bin", "dot")
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == strconv.Itoa(os.Getpid()) {
			if !strings.HasPrefix(runningPath, root+string(os.PathSeparator)) {
				continue // a flat executable uses none of the saved copies
			}
			// argv can be current or a bare PATH name. The updater knows its
			// loaded binary's physical path from before the activation.
			line = fields[0] + " " + runningPath
		}
		line = strings.ReplaceAll(line, aliasRoot+string(os.PathSeparator), root+string(os.PathSeparator))
		if strings.Contains(line, root+string(os.PathSeparator)) || strings.Contains(line, stable) || (!explicitHome && filepath.Base(fields[1]) == "dot") {
			lines = append(lines, line)
		}
	}
	notes := aitooling.PruneInstallVersions("dot", root, current, previous, stable, versions, lines, func(line string) bool {
		for _, version := range versions {
			if strings.Contains(line, filepath.Join(root, version)+string(os.PathSeparator)) {
				return true
			}
		}
		return false
	})
	for _, note := range notes {
		p.Line("%s", note)
	}
}

func dotInstallError(step, target string, err error) error {
	return fmt.Errorf("%s at %s: %w; inspect that path and fix permissions or conflicting install entries before retrying", step, target, err)
}
