package cli

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

var errHomebrewNeedsFullGate = errors.New("homebrew metadata or recipe needs a full maintenance gate")

var prebuiltHomebrewEnvironment = []string{
	"HOMEBREW_NO_AUTO_UPDATE=1",
	"HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK=1",
	"HOMEBREW_NO_INSTALL_CLEANUP=1",
}

var portableRubyVersionName = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(_[0-9]+)?$`)

// upgradeHomebrewPrebuilt uses the warning-tolerant profile only when the
// current Homebrew tap contains the exact GoReleaser-generated native-only
// formula for this verified release. Any drift returns to the full-gate path.
func upgradeHomebrewPrebuilt(ctx context.Context, p *Printer, h homebrewDot, current, version string, checksums []byte, dryRun bool, wait time.Duration) error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("this dot binary belongs to Homebrew, which does not run as root; run dot update as the user who owns %s", h.prefix)
	}
	if !prebuiltFormulaEligible(ctx, h, current, version, checksums) {
		return errHomebrewNeedsFullGate
	}
	if dryRun {
		p.Line("[dry-run] verified Homebrew formula is eligible for dot %s", version)
		return nil
	}
	release, err := acquireMaintenanceSlot(ctx, p, wait, upgradeAcquirePrebuilt)
	if err != nil {
		return fmt.Errorf("the verified prebuilt Homebrew update needs the maintenance slot: %w; %s", err, retrySlotHint(wait))
	}
	defer release()
	// Re-read the raw formula after admission and before any Homebrew command
	// can load it as Ruby code.
	if !prebuiltFormulaEligible(context.WithoutCancel(ctx), h, current, version, checksums) {
		return errHomebrewNeedsFullGate
	}
	before, _ := filepath.EvalSymlinks(h.optDot())
	args := append(append([]string{}, prebuiltHomebrewEnvironment...), h.brew(), "upgrade", "--formula", "staixbwlb/cask/dotfiles")
	p.Line("\nRunning verified Homebrew update for dotfiles...")
	_, upgradeErr := upgradeRun(context.WithoutCancel(ctx), true, "/usr/bin/env", args...)
	if upgradeErr != nil {
		upgradeErr = fmt.Errorf("verified brew upgrade dotfiles: %w", upgradeErr)
	}
	var readbackErr error
	if upgradeErr == nil {
		goos, goarch, targetErr := supportedNativeReleaseTarget()
		if targetErr != nil {
			readbackErr = targetErr
		} else if readbackErr = verifyNativeBinaryTarget(h.optDot(), goos, goarch); readbackErr == nil {
			var installed string
			installed, readbackErr = upgradedVersion(context.WithoutCancel(ctx), h, version)
			if readbackErr == nil && installed != version {
				readbackErr = fmt.Errorf("homebrew installed %s, expected exact prebuilt version %s", installed, version)
			}
		}
		if readbackErr == nil {
			p.Line("Upgraded to %s (Homebrew)", version)
		}
	}
	if after, _ := filepath.EvalSymlinks(h.optDot()); after == before {
		return errors.Join(upgradeErr, readbackErr)
	}
	return errors.Join(upgradeErr, readbackErr, upgradeReloadLaunchAgents(context.WithoutCancel(ctx), p, h, false))
}

func prebuiltFormulaSourceEligible(h homebrewDot, version string, checksums []byte) bool {
	source, err := readTapFormulaSource(h)
	return err == nil && canonicalFormulaMatches(string(source), version, checksums)
}

func canonicalFormulaMatches(source, version string, checksums []byte) bool {
	want, err := canonicalGoReleaserFormula(version, checksums)
	return err == nil && strings.TrimSpace(source) == strings.TrimSpace(want)
}

func prebuiltFormulaEligible(ctx context.Context, h homebrewDot, current, version string, checksums []byte) bool {
	return prebuiltIneligibility(ctx, h, current, version, checksums) == ""
}

// prebuiltIneligibility names the first check that keeps a Homebrew install
// off the verified-prebuilt policy, or returns "" when every check passes
// (#266). The fallback line and `dot update --check` print it.
func prebuiltIneligibility(ctx context.Context, h homebrewDot, current, version string, checksums []byte) string {
	if !prebuiltFormulaSourceEligible(h, version, checksums) {
		return "the tap formula is not the canonical release formula for " + version + " (run brew update, or the tap was edited)"
	}
	if homebrewRubyOverridesPresent(h) {
		return "HOMEBREW_* environment variables or a brew.env file override Homebrew's defaults"
	}
	if !homebrewRuntimeReady(h) {
		return "Homebrew's portable Ruby is not installed as expected (run brew update once)"
	}
	pinned, err := homebrewFormulaPinned(h)
	if err != nil {
		return "cannot read the formula's pin state: " + err.Error()
	}
	if pinned {
		return "the " + h.formula + " formula is pinned"
	}
	goos, goarch, err := supportedNativeReleaseTarget()
	if err != nil {
		return err.Error()
	}
	if runtime.GOARCH != goarch {
		return fmt.Sprintf("this dot is %s, but the native release for this Mac is %s", runtime.GOARCH, goarch)
	}
	optBinary, kegVersion, err := managedHomebrewOptBinary(h)
	if err != nil {
		return "the Homebrew keg is not a managed dot install: " + err.Error()
	}
	if err := verifyNativeBinaryTarget(optBinary, goos, goarch); err != nil {
		return "the installed Homebrew binary is not a native " + goos + "/" + goarch + " build"
	}
	installed, err := readDotBinaryVersion(ctx, optBinary)
	if err != nil {
		return "cannot read the installed Homebrew dot version: " + err.Error()
	}
	if kegVersion != current || installed != current {
		return fmt.Sprintf("the Homebrew keg holds %s (binary reports %s), not the running %s", kegVersion, installed, current)
	}
	return ""
}

func homebrewRuntimeReady(h homebrewDot) bool {
	repository, err := resolvedHomebrewRepository(h)
	if err != nil {
		return false
	}
	vendorDir := filepath.Join(repository, "Library", "Homebrew", "vendor")
	vendorRoot := filepath.Join(vendorDir, "portable-ruby")
	versionPath := filepath.Join(vendorDir, "portable-ruby-version")
	versionFile, err := os.Open(versionPath)
	if err != nil {
		return false
	}
	versionInfo, err := versionFile.Stat()
	if err != nil || !versionInfo.Mode().IsRegular() || versionInfo.Size() <= 0 || versionInfo.Size() > 128 {
		_ = versionFile.Close()
		return false
	}
	versionData, err := io.ReadAll(io.LimitReader(versionFile, 129))
	closeErr := versionFile.Close()
	if err != nil || closeErr != nil || len(versionData) > 128 {
		return false
	}
	version := strings.TrimSpace(string(versionData))
	if !portableRubyVersionName.MatchString(version) || filepath.Base(version) != version {
		return false
	}
	current := filepath.Join(vendorRoot, "current")
	linkInfo, err := os.Lstat(current)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		return false
	}
	expectedVersion := filepath.Join(vendorRoot, version)
	expectedInfo, err := os.Lstat(expectedVersion)
	if err != nil || !expectedInfo.IsDir() {
		return false
	}
	currentInfo, err := os.Stat(current)
	if err != nil || !os.SameFile(currentInfo, expectedInfo) {
		return false
	}
	resolvedCurrent, err := filepath.EvalSymlinks(current)
	if err != nil {
		return false
	}
	for _, binary := range []string{"ruby", "bundle"} {
		path := filepath.Join(resolvedCurrent, "bin", binary)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return false
		}
	}
	return !homebrewRubyOverridesPresent(h)
}

func homebrewRubyOverridesPresent(h homebrewDot) bool {
	for _, name := range []string{"HOMEBREW_DEVELOPER", "HOMEBREW_TESTS", "HOMEBREW_FORCE_VENDOR_RUBY", "HOMEBREW_USE_RUBY_FROM_PATH", "HOMEBREW_XDG_CONFIG_HOME"} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	for _, name := range []string{"HOMEBREW_NO_AUTO_UPDATE", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK", "HOMEBREW_NO_INSTALL_CLEANUP"} {
		if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "1" {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return true
	}
	configHome := config.ConfigHome(home, false)
	if !filepath.IsAbs(configHome) {
		return true
	}
	paths := []string{
		"/etc/homebrew/brew.env",
		filepath.Join(h.prefix, "etc", "homebrew", "brew.env"),
		filepath.Join(configHome, "homebrew", "brew.env"),
		filepath.Join(home, ".homebrew", "brew.env"),
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
			return true
		}
		f, err := os.Open(path)
		if err != nil {
			return true
		}
		data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
		closeErr := f.Close()
		if err != nil || closeErr != nil || len(data) > 64<<10 {
			return true
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSuffix(line, "\r")
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "HOMEBREW_") {
				return true
			}
		}
	}
	return false
}

func resolvedHomebrewRepository(h homebrewDot) (string, error) {
	brewScript, err := filepath.EvalSymlinks(h.brew())
	if err != nil || filepath.Base(filepath.Dir(brewScript)) != "bin" {
		return "", fmt.Errorf("resolving Homebrew repository from its brew script")
	}
	repository := filepath.Dir(filepath.Dir(brewScript))
	info, err := os.Stat(filepath.Join(repository, "Library", "Homebrew"))
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("resolved Homebrew repository has no Library/Homebrew directory")
	}
	return repository, nil
}

func homebrewFormulaPinned(h homebrewDot) (bool, error) {
	path := filepath.Join(h.prefix, "var", "homebrew", "pinned", h.formula)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return true, fmt.Errorf("unexpected non-symlink pin record at %s", path)
	}
	return true, nil
}

func readManagedHomebrewVersion(ctx context.Context, h homebrewDot) (string, error) {
	optBinary, kegVersion, err := managedHomebrewOptBinary(h)
	if err != nil {
		return "", err
	}
	goos, goarch, err := binaryBuildTarget(optBinary)
	if err != nil || goos != runtime.GOOS || (goarch != "amd64" && goarch != "arm64") {
		return "", fmt.Errorf("homebrew opt binary has unsupported target %s/%s", goos, goarch)
	}
	version, err := readDotBinaryVersion(ctx, optBinary)
	if err != nil {
		return "", err
	}
	if version != kegVersion {
		return "", fmt.Errorf("homebrew Cellar version %s does not match executable version %s", kegVersion, version)
	}
	return version, nil
}

func managedHomebrewOptBinary(h homebrewDot) (string, string, error) {
	resolved, err := filepath.EvalSymlinks(h.optDot())
	if err != nil {
		return "", "", err
	}
	managed, ok := homebrewDotFor(resolved)
	if !ok || managed.formula != h.formula || !sameCanonicalHomebrewPath(managed.prefix, h.prefix) {
		return "", "", fmt.Errorf("homebrew opt binary does not resolve into the managed %s formula", h.formula)
	}
	cellarRoot, err := filepath.EvalSymlinks(filepath.Join(h.prefix, "Cellar", h.formula))
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(cellarRoot, resolved)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 3 || parts[0] == ".." || filepath.IsAbs(rel) || parts[1] != "bin" || parts[2] != "dot" {
		return "", "", fmt.Errorf("unexpected Homebrew opt binary path %s", resolved)
	}
	return resolved, parts[0], nil
}

func sameCanonicalHomebrewPath(left, right string) bool {
	leftResolved, err := filepath.EvalSymlinks(left)
	if err != nil {
		return false
	}
	rightResolved, err := filepath.EvalSymlinks(right)
	if err != nil {
		return false
	}
	return filepath.Clean(leftResolved) == filepath.Clean(rightResolved)
}

func canonicalGoReleaserFormula(version string, checksums []byte) (string, error) {
	type target struct{ os, arch string }
	targets := []target{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}
	var sums []string
	for _, target := range targets {
		name := fmt.Sprintf("dot_%s_%s_%s.tar.gz", version, target.os, target.arch)
		sum, err := parseUniqueChecksum(checksums, name)
		if err != nil {
			return "", err
		}
		sums = append(sums, sum)
	}
	return fmt.Sprintf(`# typed: false
# frozen_string_literal: true

# This file was generated by GoReleaser. DO NOT EDIT.
class Dotfiles < Formula
  desc "Declarative dotfiles manager and AI tmux workspace orchestrator"
  homepage "https://github.com/STAIxBWLB/dotfiles"
  version "%s"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/STAIxBWLB/dotfiles/releases/download/v%s/dot_%s_darwin_amd64.tar.gz"
      sha256 "%s"

      define_method(:install) do
        bin.install "dot"
        bin.install_symlink "dot" => "dotfiles"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/STAIxBWLB/dotfiles/releases/download/v%s/dot_%s_darwin_arm64.tar.gz"
      sha256 "%s"

      define_method(:install) do
        bin.install "dot"
        bin.install_symlink "dot" => "dotfiles"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/STAIxBWLB/dotfiles/releases/download/v%s/dot_%s_linux_amd64.tar.gz"
      sha256 "%s"
      define_method(:install) do
        bin.install "dot"
        bin.install_symlink "dot" => "dotfiles"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/STAIxBWLB/dotfiles/releases/download/v%s/dot_%s_linux_arm64.tar.gz"
      sha256 "%s"
      define_method(:install) do
        bin.install "dot"
        bin.install_symlink "dot" => "dotfiles"
      end
    end
  end

  test do
    system "#{bin}/dot", "version"
  end
end
`, version, version, version, sums[0], version, version, sums[1], version, version, sums[2], version, version, sums[3]), nil
}

func supportedNativeReleaseTarget() (string, string, error) {
	if err := validatePrebuiltPlatform(runtime.GOOS); err != nil {
		return "", "", err
	}
	arch, err := nativeHostArchitecture(context.Background())
	if err != nil {
		return "", "", err
	}
	if arch != "amd64" && arch != "arm64" {
		return "", "", fmt.Errorf("verified prebuilt updates are unsupported on %s/%s", runtime.GOOS, arch)
	}
	return runtime.GOOS, arch, nil
}

func discoverHomebrewDot(ctx context.Context) (homebrewDot, string, error) {
	brewPath, err := execLookPath("brew")
	if err != nil {
		return homebrewDot{}, "", fmt.Errorf("finding Homebrew for --homebrew: %w", err)
	}
	brewPath, err = filepath.Abs(brewPath)
	if err != nil {
		return homebrewDot{}, "", fmt.Errorf("resolving Homebrew path: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	prefixOut, err := upgradeRun(probeCtx, false, "/usr/bin/env", "HOMEBREW_NO_AUTO_UPDATE=1", brewPath, "--prefix")
	if err != nil {
		return homebrewDot{}, "", fmt.Errorf("reading homebrew prefix: %w", err)
	}
	prefix := strings.TrimSpace(prefixOut)
	if !filepath.IsAbs(prefix) {
		return homebrewDot{}, "", fmt.Errorf("homebrew returned an invalid prefix %q", prefix)
	}
	h := homebrewDot{prefix: prefix, formula: "dotfiles"}
	dot, err := filepath.EvalSymlinks(h.optDot())
	if err != nil {
		return homebrewDot{}, "", fmt.Errorf("resolving installed Homebrew dotfiles binary: %w", err)
	}
	managed, ok := homebrewDotFor(dot)
	if !ok || managed.formula != h.formula || !sameCanonicalHomebrewPath(managed.prefix, h.prefix) {
		return homebrewDot{}, "", fmt.Errorf("homebrew opt link does not resolve into the expected dotfiles formula")
	}
	version, err := readManagedHomebrewVersion(ctx, h)
	if err != nil {
		return homebrewDot{}, "", fmt.Errorf("verifying installed Homebrew dotfiles version: %w", err)
	}
	return h, version, nil
}

func readTapFormulaSource(h homebrewDot) ([]byte, error) {
	repository, err := resolvedHomebrewRepository(h)
	if err != nil {
		return nil, err
	}
	tapRoot := filepath.Join(repository, "Library", "Taps", "staixbwlb", "homebrew-cask")
	formulaPath := filepath.Join(tapRoot, "Formula", "dotfiles.rb")
	rootInfo, err := os.Lstat(tapRoot)
	if err != nil || !rootInfo.IsDir() {
		return nil, fmt.Errorf("trusted Homebrew tap directory is unavailable")
	}
	root, err := filepath.EvalSymlinks(tapRoot)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(formulaPath)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("trusted formula source is missing or not a regular file")
	}
	if before.Size() <= 0 || before.Size() > 1<<20 {
		return nil, fmt.Errorf("trusted formula source size %d is outside the limit", before.Size())
	}
	resolved, err := filepath.EvalSymlinks(formulaPath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("trusted formula source resolves outside its tap")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		return nil, fmt.Errorf("trusted formula source changed while opening")
	}
	source, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(source) == 0 || len(source) > 1<<20 {
		return nil, fmt.Errorf("trusted formula source exceeds the limit")
	}
	return source, nil
}

func nativeHostArchitecture(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		out, err := upgradeRun(ctx, false, "/usr/sbin/sysctl", "-n", "hw.optional.arm64")
		if err != nil {
			return "", fmt.Errorf("cannot verify native Darwin architecture: %w", err)
		}
		return darwinNativeArch(strings.TrimSpace(out))
	}
	if runtime.GOOS == "linux" {
		out, err := upgradeRun(ctx, false, "uname", "-m")
		if err != nil {
			return "", fmt.Errorf("reading native Linux architecture: %w", err)
		}
		switch strings.TrimSpace(out) {
		case "x86_64", "amd64":
			return "amd64", nil
		case "aarch64", "arm64":
			return "arm64", nil
		default:
			return "", fmt.Errorf("unsupported native Linux architecture %q", strings.TrimSpace(out))
		}
	}
	return "", fmt.Errorf("cannot verify native architecture on %s", runtime.GOOS)
}

func darwinNativeArch(arm64Capability string) (string, error) {
	switch arm64Capability {
	case "1":
		return "arm64", nil
	case "0":
		return "amd64", nil
	default:
		return "", fmt.Errorf("cannot verify native Darwin architecture from hw.optional.arm64=%q", arm64Capability)
	}
}

func verifyNativeBinaryTarget(path, goos, goarch string) error {
	actualOS, actualArch, err := binaryBuildTarget(path)
	if err != nil {
		return err
	}
	if actualOS != goos || actualArch != goarch {
		return fmt.Errorf("release executable target is %s/%s, expected native %s/%s", actualOS, actualArch, goos, goarch)
	}
	return nil
}

func binaryBuildTarget(path string) (string, string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("reading release Go build information: %w", err)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	goos, goarch := settings["GOOS"], settings["GOARCH"]
	if goos == "" || goarch == "" {
		return "", "", fmt.Errorf("release executable is missing Go OS/architecture build settings")
	}
	return goos, goarch, nil
}

func readDotBinaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := upgradeRun(ctx, false, path, "--version")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) < 3 || fields[0] != "dot" || fields[1] != "version" {
		return "", fmt.Errorf("unexpected version output from %s: %q", path, strings.TrimSpace(out))
	}
	return strings.TrimPrefix(fields[2], "v"), nil
}

var execLookPath = exec.LookPath
