package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

const githubRepo = "STAIxBWLB/dotfiles"

func newUpgradeCmd(currentVersion string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update dot binary to latest version",
		Long: `Download and install the latest dot release from GitHub.

A Homebrew install is upgraded through the brew that owns it instead: when the
installed tap recipe matches the verified native release, dot uses its bounded
prebuilt update policy. Stale or changed tap metadata uses the full maintenance
gate before brew update. A pinned formula is left alone. Use --homebrew to
target an existing Homebrew install from a standalone dot binary. --check only
reports the latest version and which path an update would take, and why the
verified-prebuilt path is skipped when it is. --wait 30m waits for a busy
maintenance slot instead of deferring at once.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(cmd, currentVersion)
		},
	}
	cmd.Flags().Bool("check", false, "Only check for updates without installing")
	cmd.Flags().Bool("homebrew", false, "Target the installed Homebrew dot formula")
	cmd.Flags().Duration("wait", 0, "wait up to this long for the maintenance slot instead of deferring (e.g. 30m)")
	return cmd
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

func runUpgrade(cmd *cobra.Command, currentVersion string) error {
	ctx := cmd.Context()
	checkOnly, _ := cmd.Flags().GetBool("check")
	homebrewTarget, _ := cmd.Flags().GetBool("homebrew")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	wait, _ := cmd.Flags().GetDuration("wait")
	p := printerFrom(cmd)

	p.Line("Current version: %s", currentVersion)

	var execPath string
	var brew homebrewDot
	var viaBrew bool
	if homebrewTarget {
		var err error
		brew, currentVersion, err = discoverHomebrewDot(ctx)
		if err != nil {
			return err
		}
		viaBrew = true
		p.Line("Homebrew target version: %s", currentVersion)
	} else {
		// Resolve the install target before downloading anything: a binary inside
		// a package manager's tree is not dot's to replace. On a Homebrew install
		// EvalSymlinks resolves ~/.local/bin/dot → /opt/homebrew/bin/dot →
		// Cellar/dotfiles/<ver>/bin/dot, and renaming over that file leaves the
		// recorded brew version disagreeing with the bytes on disk (#233), so
		// brew upgrades it instead (#235).
		var err error
		execPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("finding executable path: %w", err)
		}
		execPath, err = filepath.EvalSymlinks(execPath)
		if err != nil {
			return fmt.Errorf("resolving executable path: %w", err)
		}
		brew, viaBrew = homebrewDotFor(execPath)
	}

	latest, err := fetchLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("checking latest version: %w", err)
	}

	latestVersion := strings.TrimPrefix(latest.TagName, "v")
	currentClean := strings.TrimPrefix(currentVersion, "v")

	p.Line("Latest version:  %s", latestVersion)

	cmp := compareSemver(currentClean, latestVersion)
	switch {
	case cmp == 0:
		p.Line("Already up to date.")
		if checkOnly {
			p.Line("Update path: %s", updatePath(ctx, brew, viaBrew, currentClean, latestVersion, latest.TagName))
		}
		return nil
	case cmp > 0:
		p.Line("Current version %s is newer than latest release %s.", currentClean, latestVersion)
		if checkOnly {
			return nil
		}
		p.Line("Nothing to upgrade.")
		return nil
	}

	if checkOnly {
		p.Line("\nUpdate available: %s → %s", currentClean, latestVersion)
		p.Line("Update path: %s", updatePath(ctx, brew, viaBrew, currentClean, latestVersion, latest.TagName))
		if homebrewTarget {
			p.Line("Run 'dot update --homebrew' to install.")
		} else {
			p.Line("Run 'dot update' to install.")
		}
		return nil
	}
	return performVerifiedUpdate(ctx, p, execPath, brew, viaBrew, currentClean, latestVersion, latest.TagName, dryRun, wait)
}

// updatePath says, read-only, which path `dot update` would take (#266).
func updatePath(ctx context.Context, brew homebrewDot, viaBrew bool, current, latest, tag string) string {
	if !viaBrew {
		goos, goarch, err := supportedNativeReleaseTarget()
		if err == nil {
			_, err = planVerifiedPrebuilt(tag, goos, goarch)
		}
		if err != nil {
			return "none: " + err.Error()
		}
		return "verified prebuilt download"
	}
	if _, _, why := homebrewPrebuiltPlan(ctx, brew, current, latest, tag); why != "" {
		return "full maintenance gate, because " + why
	}
	return "verified prebuilt (Homebrew)"
}

// homebrewPrebuiltPlan returns the release plan and checksum list when a
// Homebrew install can take the verified-prebuilt path, or the reason it
// cannot.
func homebrewPrebuiltPlan(ctx context.Context, brew homebrewDot, current, latest, tag string) (verifiedPrebuiltPlan, []byte, string) {
	osName, archName, err := supportedNativeReleaseTarget()
	if err != nil {
		return verifiedPrebuiltPlan{}, nil, err.Error()
	}
	plan, err := planVerifiedPrebuilt(tag, osName, archName)
	if err != nil {
		return plan, nil, "no verified release asset: " + err.Error()
	}
	checksums, err := fetchPrebuiltChecksums(ctx, plan.ChecksumsURL)
	if err != nil {
		return plan, nil, "cannot read the release checksum list: " + err.Error()
	}
	return plan, checksums, prebuiltIneligibility(ctx, brew, current, latest, checksums)
}

// slotRetryInterval paces --wait retries; tests shorten it.
var slotRetryInterval = 15 * time.Second

// acquireMaintenanceSlot takes the maintenance slot through acquire. With
// wait > 0 a deferral is retried until wait runs out instead of failing at
// once (#266); the first deferral reason is printed while it waits. No
// attempt starts after the wait ends, and each one runs under its deadline.
func acquireMaintenanceSlot(ctx context.Context, p *Printer, wait time.Duration, acquire func(context.Context) (func(), error)) (func(), error) {
	if wait <= 0 {
		return acquire(ctx)
	}
	// The first attempt runs under the wait too: the prebuilt gate itself
	// samples for a while before it answers.
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	release, err := acquire(waitCtx)
	var deferred *resourceguard.DeferredError
	if err == nil || !errors.As(err, &deferred) {
		return release, err
	}
	p.Line("The maintenance slot is busy (%s); waiting up to %s...", deferred.Reason, wait)
	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		case <-time.After(slotRetryInterval):
		}
		release, err = acquire(waitCtx)
		if err == nil || !errors.As(err, &deferred) {
			return release, err
		}
	}
}

func retrySlotHint(wait time.Duration) string {
	if wait > 0 {
		return "the wait of " + wait.String() + " ended"
	}
	return "retry later, or wait for it with dot update --wait 30m"
}

func performVerifiedUpdate(ctx context.Context, p *Printer, execPath string, brew homebrewDot, viaBrew bool, current, latest, tag string, dryRun bool, wait time.Duration) error {
	if dryRun {
		if viaBrew {
			return upgradeHomebrew(ctx, p, brew, current, latest, true, wait)
		}
		p.Line("[dry-run] would download and verify dot %s, then replace %s under verified-prebuilt admission", latest, execPath)
		return nil
	}

	fullHomebrewFallback := func(reason string) error {
		p.Line("Verified-prebuilt Homebrew update unavailable: %s. Using the full maintenance gate.", reason)
		return upgradeHomebrew(ctx, p, brew, current, latest, false, wait)
	}
	osName, archName, err := supportedNativeReleaseTarget()
	var plan verifiedPrebuiltPlan
	var checksums []byte
	if viaBrew {
		var why string
		if plan, checksums, why = homebrewPrebuiltPlan(ctx, brew, current, latest, tag); why != "" {
			return fullHomebrewFallback(why)
		}
	} else {
		if err != nil {
			return err
		}
		if plan, err = planVerifiedPrebuilt(tag, osName, archName); err != nil {
			return err
		}
	}
	p.Line("\nDownloading %s...", plan.AssetName)

	tmpDir, err := os.MkdirTemp("", "dot-upgrade-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	p.Line("Verifying checksum...")
	var newBinary string
	if viaBrew {
		newBinary, checksums, err = downloadVerifiedPrebuiltWithChecksums(ctx, plan, tmpDir, checksums)
	} else {
		newBinary, checksums, err = downloadVerifiedPrebuilt(ctx, plan, tmpDir)
	}
	if err != nil {
		return err
	}
	if err := verifyExactPrebuiltBinary(ctx, newBinary, latest, osName, archName); err != nil {
		return fmt.Errorf("downloaded binary failed exact version validation: %w", err)
	}
	if viaBrew {
		err := upgradeHomebrewPrebuilt(ctx, p, brew, current, latest, checksums, false, wait)
		if !errors.Is(err, errHomebrewNeedsFullGate) {
			return err
		}
		return fullHomebrewFallback("the formula changed while the update was being prepared")
	}
	return installStandalonePrebuilt(ctx, p, newBinary, execPath, latest, osName, archName, wait)
}

// downloadVerifiedArchive downloads the release archive and the release's
// checksums.txt, verifies the archive's sha256, then extracts into destDir.
// Any failure leaves destDir without an extracted binary — a missing or
// unfetchable checksums.txt is a hard failure, never a silent skip.
func downloadVerifiedArchive(ctx context.Context, runner *exec.Runner, downloadURL, checksumsURL, assetName, destDir string) error {
	if runner.DryRun {
		runner.Logger.Info("dry-run: download+verify+extract", "url", downloadURL, "dest", destDir)
		return nil
	}

	archivePath := filepath.Join(destDir, assetName)
	gotSum, err := fileutil.DownloadFile(ctx, runner, downloadURL, archivePath)
	if err != nil {
		return fmt.Errorf("downloading release: %w", err)
	}

	checksumsPath := filepath.Join(destDir, "checksums.txt")
	if _, err := fileutil.DownloadMetadataFile(ctx, runner, checksumsURL, checksumsPath); err != nil {
		return fmt.Errorf("fetching checksums.txt: %w — refusing to install unverified binary", err)
	}
	body, err := os.ReadFile(checksumsPath)
	if err != nil {
		return fmt.Errorf("reading checksums.txt: %w", err)
	}

	wantSum, err := parseChecksums(body, assetName)
	if err != nil {
		return err
	}
	if !strings.EqualFold(gotSum, wantSum) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s — aborting upgrade", assetName, wantSum, gotSum)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening verified archive: %w", err)
	}
	defer f.Close()
	return fileutil.ExtractTarGz(f, destDir, 0)
}

// parseChecksums finds assetName in a GoReleaser checksums.txt body
// ("<64-hex-sha256>  <asset>" per line, optional '*' binary-mode prefix)
// and returns its hex sha256.
func parseChecksums(data []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sum := fields[0]
		if len(sum) != 64 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == assetName {
			return sum, nil
		}
	}
	return "", fmt.Errorf("asset %s not listed in checksums.txt", assetName)
}

func fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpDoWithRetry(req, fileutil.MetadataHTTPClient(), 3)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading release metadata: %w", err)
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &release, nil
}

func httpDoWithRetry(req *http.Request, client *http.Client, attempts int) (*http.Response, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode < 500 {
				return resp, nil
			}
			closeErr := resp.Body.Close()
			lastErr = errors.Join(fmt.Errorf("HTTP %d", resp.StatusCode), closeErr)
		} else {
			lastErr = err
		}
		if i < attempts-1 {
			select {
			case <-time.After(time.Duration(1<<i) * time.Second):
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", attempts, lastErr)
}

// parseSemver parses "major.minor.patch" (with optional leading "v" already stripped).
// Returns (0, 0, 0, false) for non-semver inputs like "dev" or empty.
func parseSemver(s string) (major, minor, patch int, ok bool) {
	// Strip any pre-release/build metadata
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var err error
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, false
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, false
	}
	if patch, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, 0, false
	}
	return major, minor, patch, true
}

// compareSemver returns -1 if a < b, 0 if equal, 1 if a > b.
// Non-semver versions (e.g. "dev") are treated as older than any real version,
// so dev < 0.0.0 → -1, triggering upgrade.
func compareSemver(a, b string) int {
	am, an, ap, aOK := parseSemver(a)
	bm, bn, bp, bOK := parseSemver(b)

	if !aOK && !bOK {
		if a == b {
			return 0
		}
		return -1 // treat unparseable as older
	}
	if !aOK {
		return -1 // dev is older than any release
	}
	if !bOK {
		return 1
	}

	if am != bm {
		if am < bm {
			return -1
		}
		return 1
	}
	if an != bn {
		if an < bn {
			return -1
		}
		return 1
	}
	if ap != bp {
		if ap < bp {
			return -1
		}
		return 1
	}
	return 0
}
