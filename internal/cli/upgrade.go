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
reports the latest version.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(cmd, currentVersion)
		},
	}
	cmd.Flags().Bool("check", false, "Only check for updates without installing")
	cmd.Flags().Bool("homebrew", false, "Target the installed Homebrew dot formula")
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
	} else if !checkOnly {
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
		if homebrewTarget {
			p.Line("Run 'dot update --homebrew' to install.")
		} else {
			p.Line("Run 'dot update' to install.")
		}
		return nil
	}
	return performVerifiedUpdate(ctx, p, execPath, brew, viaBrew, currentClean, latestVersion, latest.TagName, dryRun)
}

func performVerifiedUpdate(ctx context.Context, p *Printer, execPath string, brew homebrewDot, viaBrew bool, current, latest, tag string, dryRun bool) error {
	if dryRun {
		if viaBrew {
			return upgradeHomebrew(ctx, p, brew, current, latest, true)
		}
		p.Line("[dry-run] would download and verify dot %s, then replace %s under verified-prebuilt admission", latest, execPath)
		return nil
	}

	fullHomebrewFallback := func(reason string) error {
		p.Line("Verified-prebuilt Homebrew update unavailable (%s); using the full maintenance gate.", reason)
		return upgradeHomebrew(ctx, p, brew, current, latest, false)
	}
	osName, archName, err := supportedNativeReleaseTarget()
	if err != nil {
		if viaBrew {
			return fullHomebrewFallback("cannot use the verified-prebuilt policy")
		}
		return err
	}
	plan, err := planVerifiedPrebuilt(tag, osName, archName)
	if err != nil {
		if viaBrew {
			return fullHomebrewFallback("cannot use the verified-prebuilt policy")
		}
		return err
	}
	var checksums []byte
	if viaBrew {
		checksums, err = fetchPrebuiltChecksums(ctx, plan.ChecksumsURL)
		if err != nil {
			return fullHomebrewFallback("cannot validate the release checksum list")
		}
		if !prebuiltFormulaEligible(ctx, brew, current, latest, checksums) {
			return fullHomebrewFallback("is not eligible for the verified-prebuilt policy")
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
		err := upgradeHomebrewPrebuilt(ctx, p, brew, current, latest, checksums, false)
		if !errors.Is(err, errHomebrewNeedsFullGate) {
			return err
		}
		return fullHomebrewFallback("changed while the update was being prepared")
	}
	return installStandalonePrebuilt(ctx, p, newBinary, execPath, latest, osName, archName)
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
