package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestPlanVerifiedPrebuiltPinsNativeStableRelease(t *testing.T) {
	plan, err := planVerifiedPrebuilt("v2.70.35", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if plan.AssetName != "dot_2.70.35_darwin_arm64.tar.gz" || plan.DownloadURL != "https://github.com/STAIxBWLB/dotfiles/releases/download/v2.70.35/dot_2.70.35_darwin_arm64.tar.gz" {
		t.Fatalf("unexpected release plan: %+v", plan)
	}
	for _, tag := range []string{"v2.70.35-rc.1", "v2.70.35+build", "latest", ""} {
		if _, err := planVerifiedPrebuilt(tag, "darwin", "arm64"); err == nil {
			t.Errorf("accepted non-stable release tag %q", tag)
		}
	}
	if _, err := planVerifiedPrebuilt("v2.70.35", "windows", "amd64"); err == nil {
		t.Fatal("accepted unsupported native platform")
	}
}

func TestNativeTargetUsesHostCapabilityAndBinaryBuildInfo(t *testing.T) {
	if got, err := darwinNativeArch("1"); err != nil || got != "arm64" {
		t.Fatalf("Apple Silicon capability under a potentially translated process = %q, %v", got, err)
	}
	if got, err := darwinNativeArch("0"); err != nil || got != "amd64" {
		t.Fatalf("Intel capability = %q, %v", got, err)
	}
	if _, err := darwinNativeArch("unknown"); err == nil {
		t.Fatal("accepted unknown Darwin host architecture")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeBinaryTarget(self, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("rejected current Go test binary target: %v", err)
	}
	wrongArch := "arm64"
	if runtime.GOARCH == wrongArch {
		wrongArch = "amd64"
	}
	if err := verifyNativeBinaryTarget(self, runtime.GOOS, wrongArch); err == nil {
		t.Fatalf("accepted executable mislabeled as %s/%s", runtime.GOOS, wrongArch)
	}
}

func TestSameCanonicalHomebrewPathFollowsPrefixAliases(t *testing.T) {
	realPrefix := t.TempDir()
	aliasRoot := t.TempDir()
	alias := filepath.Join(aliasRoot, "prefix-alias")
	if err := os.Symlink(realPrefix, alias); err != nil {
		t.Fatal(err)
	}
	if !sameCanonicalHomebrewPath(alias, realPrefix) {
		t.Fatalf("failed to match Homebrew prefix alias %s to %s", alias, realPrefix)
	}
}

func TestParseUniqueChecksumRejectsAmbiguousOrMalformedEntries(t *testing.T) {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got, err := parseUniqueChecksum([]byte(sum+"  dot.tar.gz\n"), "dot.tar.gz"); err != nil || got != sum {
		t.Fatalf("parseUniqueChecksum = %q, %v", got, err)
	}
	for _, body := range []string{
		sum + "  dot.tar.gz\n" + sum + "  dot.tar.gz\n",
		"xyz  dot.tar.gz\n",
		sum + "  other.tar.gz\n",
	} {
		if _, err := parseUniqueChecksum([]byte(body), "dot.tar.gz"); err == nil {
			t.Errorf("accepted invalid checksum listing %q", body)
		}
	}
}

func TestStandalonePromotionRequiresExactRuntimeVersion(t *testing.T) {
	goos, goarch, err := supportedNativeReleaseTarget()
	if err != nil {
		t.Skip(err)
	}
	if runtime.GOARCH != goarch {
		t.Skip("the current test process is translated from the native host architecture")
	}
	run, acquire := upgradeRun, upgradeAcquirePrebuilt
	t.Cleanup(func() {
		upgradeRun, upgradeAcquirePrebuilt = run, acquire
	})
	upgradeRun = func(context.Context, bool, string, ...string) (string, error) {
		return "dot version 2.70.34 (abc)\n", nil
	}
	acquired := 0
	upgradeAcquirePrebuilt = func(context.Context) (func(), error) {
		acquired++
		return func() {}, nil
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "new-dot")
	destination := filepath.Join(dir, "dot")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installStandalonePrebuilt(context.Background(), &Printer{}, source, destination, "2.70.35", goos, goarch, 0); err == nil || !strings.Contains(err.Error(), "expected exact dot version") {
		t.Fatal("accepted binary reporting the wrong release version")
	}
	if acquired != 0 {
		t.Fatalf("admission acquired %d times before exact version validation", acquired)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "old executable" {
		t.Fatalf("destination changed after rejected binary: %q, %v", got, err)
	}
}

func TestExtractSinglePrebuiltRequiresOneRootExecutable(t *testing.T) {
	archive := tarGzFixture(t, []tar.Header{
		{Name: "LICENSE", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "README.md", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "dot", Typeflag: tar.TypeReg, Mode: 0o755, Size: 4},
	}, [][]byte{[]byte("MIT\n"), []byte("help"), []byte("ELF!")})
	path := filepath.Join(t.TempDir(), "dot")
	if err := extractSinglePrebuilt(bytes.NewReader(archive), path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "ELF!" {
		t.Fatalf("extracted bytes = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("executable mode = %v, %v", info, err)
	}

	for _, tc := range []struct {
		name    string
		headers []tar.Header
		bodies  [][]byte
	}{
		{"path traversal", []tar.Header{{Name: "../dot", Typeflag: tar.TypeReg, Mode: 0o755, Size: 4}}, [][]byte{[]byte("ELF!")}},
		{"extra member", []tar.Header{{Name: "dot", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1}, {Name: "unexpected", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}}, [][]byte{[]byte("x"), []byte("y")}},
		{"symlink", []tar.Header{{Name: "dot", Typeflag: tar.TypeSymlink, Linkname: "elsewhere", Mode: 0o777}}, [][]byte{nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "dot")
			archive := tarGzFixture(t, tc.headers, tc.bodies)
			if err := extractSinglePrebuilt(bytes.NewReader(archive), dest); err == nil {
				t.Fatal("accepted unsafe archive")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatalf("unsafe archive left output behind: %v", err)
			}
		})
	}
	for _, member := range []rawTarMember{
		{Name: "dot", Typeflag: tar.TypeReg, Size: prebuiltMaxBinaryBytes + 1, Truncated: true},
		{Name: "README.md", Typeflag: tar.TypeReg, Size: prebuiltMaxDocBytes + 1, Truncated: true},
	} {
		dest := filepath.Join(t.TempDir(), "dot")
		archive := rawTarGzFixture(t, []rawTarMember{member})
		if err := extractSinglePrebuilt(bytes.NewReader(archive), dest); err == nil {
			t.Fatalf("accepted oversized member %q", member.Name)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("oversized member left output behind: %v", err)
		}
	}
}

func TestExtractSinglePrebuiltAllowsOmittedReleaseDocuments(t *testing.T) {
	archive := tarGzFixture(t, []tar.Header{{Name: "dot", Typeflag: tar.TypeReg, Mode: 0o755, Size: 4}}, [][]byte{[]byte("ELF!")})
	path := filepath.Join(t.TempDir(), "dot")
	if err := extractSinglePrebuilt(bytes.NewReader(archive), path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "ELF!" {
		t.Fatalf("dot-only release archive extraction = %q, %v", data, err)
	}
}

func TestScanPrebuiltTarRejectsHiddenExtensionHeadersAndCountsWholeOutput(t *testing.T) {
	for _, typeflag := range []byte{tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink} {
		t.Run(fmt.Sprintf("type-%d", typeflag), func(t *testing.T) {
			archive := rawTarGzFixture(t, []rawTarMember{
				{Name: "metadata", Typeflag: typeflag, Body: []byte("hidden")},
				{Name: "dot", Typeflag: tar.TypeReg, Body: []byte("ELF!")},
			})
			gz, err := gzip.NewReader(bytes.NewReader(archive))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = scanPrebuiltTar(&expandedLimitReader{reader: gz, limit: prebuiltMaxExpandedBytes})
			_ = gz.Close()
			if err == nil {
				t.Fatal("accepted hidden tar extension metadata")
			}
		})
	}

	limited := &expandedLimitReader{reader: strings.NewReader("012345"), limit: 4}
	if _, err := io.ReadAll(limited); !errors.Is(err, errPrebuiltExpansionLimit) || limited.total != 5 {
		t.Fatalf("expanded reader = %d bytes, %v; want 5 and limit error", limited.total, err)
	}
}

func tarGzFixture(t *testing.T, headers []tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tr := tar.NewWriter(gz)
	for i := range headers {
		h := headers[i]
		if err := tr.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if i < len(bodies) && len(bodies[i]) > 0 {
			if _, err := tr.Write(bodies[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

type rawTarMember struct {
	Name      string
	Typeflag  byte
	Body      []byte
	Size      int64
	Truncated bool
}

func rawTarGzFixture(t *testing.T, members []rawTarMember) []byte {
	t.Helper()
	var raw bytes.Buffer
	for _, member := range members {
		size := int64(len(member.Body))
		if member.Size > 0 {
			size = member.Size
		}
		header := make([]byte, 512)
		copy(header[:100], member.Name)
		copy(header[100:108], "0000644\x00")
		copy(header[108:116], "0000000\x00")
		copy(header[116:124], "0000000\x00")
		copy(header[124:136], fmt.Sprintf("%011o\x00", size))
		copy(header[136:148], "00000000000\x00")
		header[156] = member.Typeflag
		copy(header[257:263], "ustar\x00")
		copy(header[263:265], "00")
		for i := 148; i < 156; i++ {
			header[i] = ' '
		}
		var sum int
		for _, b := range header {
			sum += int(b)
		}
		copy(header[148:156], fmt.Sprintf("%06o\x00 ", sum))
		_, _ = raw.Write(header)
		if member.Truncated {
			continue
		}
		_, _ = raw.Write(member.Body)
		if rem := int(size) % 512; rem > 0 {
			_, _ = raw.Write(make([]byte, 512-rem))
		}
	}
	_, _ = raw.Write(make([]byte, 1024))
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestCanonicalHomebrewRecipeIsNativeOnlyAndExact(t *testing.T) {
	assets := []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"}
	var checksums strings.Builder
	for i, asset := range assets {
		fmt.Fprintf(&checksums, "%064x  dot_2.70.34_%s.tar.gz\n", i+1, asset)
	}
	source, err := canonicalGoReleaserFormula("2.70.34", []byte(checksums.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !canonicalFormulaMatches(source, "2.70.34", []byte(checksums.String())) {
		t.Fatal("accepted canonical generated formula")
	}
	for _, mutation := range []string{
		strings.Replace(source, `bin.install "dot"`, `system "make", "install"`, 1),
		strings.Replace(source, "class Dotfiles < Formula", "class Dotfiles < Formula\n  depends_on \"openssl\"", 1),
		strings.Replace(source, "  test do", "  post_install do\n    system \"sh\", \"-c\", \"touch /tmp/pwned\"\n  end\n\n  test do", 1),
		strings.Replace(source, "dot_2.70.34_darwin_arm64.tar.gz", "attacker.tar.gz", 1),
	} {
		if canonicalFormulaMatches(mutation, "2.70.34", []byte(checksums.String())) {
			t.Errorf("accepted changed Homebrew formula source:\n%s", mutation)
		}
	}
}

func TestPrebuiltHomebrewEligibilityChecksRawSourcePinAndInstalledKeg(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the Homebrew update path refuses root")
	}
	_, goarch, err := supportedNativeReleaseTarget()
	if err != nil {
		t.Skip(err)
	}
	if runtime.GOARCH != goarch {
		t.Skip("the current test process is translated from the native host architecture")
	}
	for _, name := range []string{"HOMEBREW_DEVELOPER", "HOMEBREW_TESTS", "HOMEBREW_FORCE_VENDOR_RUBY", "HOMEBREW_USE_RUBY_FROM_PATH", "HOMEBREW_XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	for _, name := range []string{"HOMEBREW_NO_AUTO_UPDATE", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK", "HOMEBREW_NO_INSTALL_CLEANUP"} {
		t.Setenv(name, "1")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	run := upgradeRun
	acquire := upgradeAcquirePrebuilt
	t.Cleanup(func() { upgradeRun, upgradeAcquirePrebuilt = run, acquire })
	upgradeRun = func(ctx context.Context, show bool, name string, args ...string) (string, error) {
		if len(args) == 1 && args[0] == "--version" && filepath.Base(name) == "dot" {
			return "dot version 2.70.34 (abc)\n", nil
		}
		return run(ctx, show, name, args...)
	}
	prefix := t.TempDir()
	h := homebrewDot{prefix: prefix, formula: "dotfiles"}
	for _, dir := range []string{
		filepath.Join(prefix, "bin"), filepath.Join(prefix, "Library", "Homebrew"),
		filepath.Join(prefix, "Library", "Taps", "staixbwlb", "homebrew-cask", "Formula"),
		filepath.Join(prefix, "Cellar", "dotfiles", "2.70.34", "bin"),
		filepath.Join(prefix, "opt", "dotfiles", "bin"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeReadyHomebrewRuby(t, prefix)
	if err := os.WriteFile(h.brew(), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(prefix, "Cellar", "dotfiles", "2.70.34", "bin", "dot")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, h.optDot()); err != nil {
		t.Fatal(err)
	}
	checksums := formulaChecksums("2.70.35")
	formula, err := canonicalGoReleaserFormula("2.70.35", checksums)
	if err != nil {
		t.Fatal(err)
	}
	formulaPath := filepath.Join(prefix, "Library", "Taps", "staixbwlb", "homebrew-cask", "Formula", "dotfiles.rb")
	if err := os.WriteFile(formulaPath, []byte(formula), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := prebuiltIneligibility(context.Background(), h, "2.70.34", "2.70.35", checksums); why != "" {
		t.Fatalf("rejected canonical unpinned formula with the exact installed opt version: %s", why)
	}
	if why := prebuiltIneligibility(context.Background(), h, "2.70.33", "2.70.35", checksums); !strings.Contains(why, "not the running 2.70.33") {
		t.Fatalf("a keg that is not the running version must be refused with its own reason, got %q", why)
	}
	brewEnv := filepath.Join(prefix, "etc", "homebrew", "brew.env")
	if err := os.MkdirAll(filepath.Dir(brewEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brewEnv, []byte("HOMEBREW_XDG_CONFIG_HOME=/tmp/custom-homebrew\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := prebuiltIneligibility(context.Background(), h, "2.70.34", "2.70.35", checksums); !strings.Contains(why, "brew.env") {
		t.Fatalf("an environment-file redirect must be refused with its own reason, got %q", why)
	}
	if err := os.WriteFile(brewEnv, []byte("# no Homebrew runtime overrides\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !prebuiltFormulaEligible(context.Background(), h, "2.70.34", "2.70.35", checksums) {
		t.Fatal("rejected a ready Homebrew runtime after clearing the override")
	}

	mutated := strings.Replace(formula, `bin.install "dot"`, `system "make"`, 1)
	if err := os.WriteFile(formulaPath, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := prebuiltIneligibility(context.Background(), h, "2.70.34", "2.70.35", checksums); !strings.Contains(why, "not the canonical release formula") {
		t.Fatalf("a modified formula must be refused with its own reason, got %q", why)
	}
	acquired := 0
	upgradeAcquirePrebuilt = func(context.Context) (func(), error) {
		acquired++
		return func() {}, nil
	}
	if err := upgradeHomebrewPrebuilt(context.Background(), &Printer{}, h, "2.70.34", "2.70.35", checksums, false, 0); !errors.Is(err, errHomebrewNeedsFullGate) {
		t.Fatalf("changed raw formula did not route to full gate: %v", err)
	}
	if acquired != 0 {
		t.Fatal("admission began with a changed formula source")
	}
	if err := os.WriteFile(formulaPath, []byte(formula), 0o644); err != nil {
		t.Fatal(err)
	}
	pinDir := filepath.Join(prefix, "var", "homebrew", "pinned")
	if err := os.MkdirAll(pinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(filepath.Dir(binary)), filepath.Join(pinDir, "dotfiles")); err != nil {
		t.Fatal(err)
	}
	if why := prebuiltIneligibility(context.Background(), h, "2.70.34", "2.70.35", checksums); !strings.Contains(why, "pinned") {
		t.Fatalf("a pinned formula must be refused with its own reason, got %q", why)
	}
}

func TestUpgradeHomebrewPrebuiltReloadsMovedOptAfterBrewFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the Homebrew update path refuses root")
	}
	_, goarch, err := supportedNativeReleaseTarget()
	if err != nil || runtime.GOARCH != goarch {
		t.Skip("the current process cannot run the native Homebrew prebuilt path")
	}
	for _, name := range []string{"HOMEBREW_DEVELOPER", "HOMEBREW_TESTS", "HOMEBREW_FORCE_VENDOR_RUBY", "HOMEBREW_USE_RUBY_FROM_PATH", "HOMEBREW_XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	for _, name := range []string{"HOMEBREW_NO_AUTO_UPDATE", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK", "HOMEBREW_NO_INSTALL_CLEANUP"} {
		t.Setenv(name, "1")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	run, acquire, reload := upgradeRun, upgradeAcquirePrebuilt, upgradeReloadLaunchAgents
	t.Cleanup(func() { upgradeRun, upgradeAcquirePrebuilt, upgradeReloadLaunchAgents = run, acquire, reload })
	prefix := t.TempDir()
	h := homebrewDot{prefix: prefix, formula: "dotfiles"}
	currentKeg := filepath.Join(prefix, "Cellar", "dotfiles", "2.70.34")
	newKeg := filepath.Join(prefix, "Cellar", "dotfiles", "2.70.35")
	opt := filepath.Join(prefix, "opt", "dotfiles")
	for _, path := range []string{
		filepath.Join(prefix, "bin"), filepath.Join(prefix, "Library", "Homebrew"),
		filepath.Join(prefix, "Library", "Taps", "staixbwlb", "homebrew-cask", "Formula"),
		filepath.Join(currentKeg, "bin"), filepath.Join(newKeg, "bin"), filepath.Dir(opt),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeReadyHomebrewRuby(t, prefix)
	if err := os.WriteFile(h.brew(), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryData, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(currentKeg, "bin", "dot"), filepath.Join(newKeg, "bin", "dot")} {
		if err := os.WriteFile(path, binaryData, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(currentKeg, opt); err != nil {
		t.Fatal(err)
	}
	checksums := formulaChecksums("2.70.35")
	formula, err := canonicalGoReleaserFormula("2.70.35", checksums)
	if err != nil {
		t.Fatal(err)
	}
	formulaPath := filepath.Join(prefix, "Library", "Taps", "staixbwlb", "homebrew-cask", "Formula", "dotfiles.rb")
	if err := os.WriteFile(formulaPath, []byte(formula), 0o644); err != nil {
		t.Fatal(err)
	}
	brewErr := errors.New("brew failed after relinking")
	reloadErr := errors.New("reload failed")
	upgradeCalls, acquired, reloaded := 0, 0, 0
	upgradeRun = func(_ context.Context, _ bool, name string, args ...string) (string, error) {
		switch {
		case name == "/usr/sbin/sysctl":
			if goarch == "arm64" {
				return "1\n", nil
			}
			return "0\n", nil
		case filepath.Base(name) == "uname":
			if goarch == "arm64" {
				return "aarch64\n", nil
			}
			return "x86_64\n", nil
		case len(args) == 1 && args[0] == "--version":
			return "dot version 2.70.34 (abc)\n", nil
		case name == "/usr/bin/env":
			brewIndex := slices.Index(args, h.brew())
			if brewIndex < 0 || !slices.Contains(args, "HOMEBREW_NO_AUTO_UPDATE=1") || !slices.Contains(args, "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK=1") || !slices.Contains(args, "HOMEBREW_NO_INSTALL_CLEANUP=1") {
				return "", fmt.Errorf("unsafe brew environment: %v", args)
			}
			if !slices.Equal(args[brewIndex+1:], []string{"upgrade", "--formula", "staixbwlb/cask/dotfiles"}) {
				return "", fmt.Errorf("unexpected brew upgrade argv: %v", args[brewIndex+1:])
			}
			upgradeCalls++
			_ = os.Remove(opt)
			if err := os.Symlink(newKeg, opt); err != nil {
				return "", err
			}
			return "", brewErr
		default:
			return "", fmt.Errorf("unexpected command %s %v", name, args)
		}
	}
	upgradeAcquirePrebuilt = func(context.Context) (func(), error) {
		acquired++
		return func() {}, nil
	}
	upgradeReloadLaunchAgents = func(context.Context, *Printer, homebrewDot, bool) error {
		reloaded++
		return reloadErr
	}
	got := upgradeHomebrewPrebuilt(context.Background(), &Printer{Out: io.Discard, Err: io.Discard}, h, "2.70.34", "2.70.35", checksums, false, 0)
	if !errors.Is(got, brewErr) || !errors.Is(got, reloadErr) {
		t.Fatalf("error after opt move = %v, want brew and reload errors", got)
	}
	if upgradeCalls != 1 || acquired != 1 || reloaded != 1 {
		t.Fatalf("calls: brew=%d admission=%d reload=%d, want one each", upgradeCalls, acquired, reloaded)
	}
	resolved, err := filepath.EvalSymlinks(h.optDot())
	if err != nil || !sameCanonicalHomebrewPath(resolved, filepath.Join(newKeg, "bin", "dot")) {
		t.Fatalf("opt link after partial brew failure = %s, %v", resolved, err)
	}
}

func writeReadyHomebrewRuby(t *testing.T, prefix string) {
	t.Helper()
	vendor := filepath.Join(prefix, "Library", "Homebrew", "vendor")
	version := "4.0.7"
	versionRoot := filepath.Join(vendor, "portable-ruby", version)
	if err := os.MkdirAll(filepath.Join(versionRoot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "portable-ruby-version"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{"ruby", "bundle"} {
		if err := os.WriteFile(filepath.Join(versionRoot, "bin", binary), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(version, filepath.Join(vendor, "portable-ruby", "current")); err != nil {
		t.Fatal(err)
	}
}

func formulaChecksums(version string) []byte {
	assets := []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"}
	var checksums strings.Builder
	for i, asset := range assets {
		fmt.Fprintf(&checksums, "%064x  dot_%s_%s.tar.gz\n", i+1, version, asset)
	}
	return []byte(checksums.String())
}
