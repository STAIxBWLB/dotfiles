package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Brew wraps Homebrew operations.
type Brew struct {
	Runner *Runner
}

var formulaTaps = map[string]string{
	"maru-cli": "staixbwlb/cask",
}

var darwinOnlyFormulas = map[string]bool{
	"maru-cli": true,
}

// NewBrew creates a new Brew wrapper.
func NewBrew(runner *Runner) *Brew {
	return &Brew{Runner: runner}
}

// brewPrefixDirs lists the directories a Homebrew install puts `brew` in on this
// platform. Single source for brewProgram below and for RefreshPath, which
// otherwise repeated the same runtime.GOOS switch a few hundred lines apart.
func brewPrefixDirs() []string {
	if runtime.GOOS == "darwin" {
		return []string{"/opt/homebrew/bin"}
	}
	return []string{"/home/linuxbrew/.linuxbrew/bin", "/home/linuxbrew/.linuxbrew/sbin"}
}

// brewProgram resolves what to invoke brew as, or "" when brew is not installed.
//
// PATH is asked first and answers with the bare name, so a machine where brew is
// already on PATH keeps producing byte-identical commands and log lines. Only
// when PATH cannot find it does this fall back to stat-ing the platform prefix,
// and then it answers with an absolute path — which is what lets a fresh process
// whose PATH has not been set up yet still DETECT Homebrew (D-01) without the
// process-global os.Setenv that used to buy the same thing (BUG-06).
//
// NAMED CEILING (T-05-11), and it is the direct consequence of D-01 choosing
// detection over sandbox purity: this stops production code from widening the
// process PATH; it does NOT stop a read-only probe from EXECUTING brew. RunQuery
// (runner.go) has no dry-run branch, and a subprocess invoked by absolute path is
// not confined by a PATH sandbox at all, so `dot check` and `dot apply --dry-run`
// still run real brew, which still writes under ~/Library/Caches/Homebrew. The
// unconditional empty-HOME assertion is carried by
// tests/scenarios/dry-run-empty-home.sh, which CI's linux job runs in a clean
// container with no Homebrew prefix.
func (b *Brew) brewProgram() string {
	if b.Runner.CommandExists("brew") {
		return "brew"
	}
	for _, dir := range brewPrefixDirs() {
		candidate := filepath.Join(dir, "brew")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// brewCmd is what every Brew invocation below passes to the runner. When brew
// cannot be found at all it falls back to the bare name so a missing brew keeps
// failing with the exec lookup error it fails with today, rather than with an
// empty program name.
func (b *Brew) brewCmd() string {
	if prog := b.brewProgram(); prog != "" {
		return prog
	}
	return "brew"
}

// IsAvailable checks if brew is installed.
func (b *Brew) IsAvailable() bool {
	return b.brewProgram() != ""
}

// IsInstalled checks if a formula is installed.
func (b *Brew) IsInstalled(formula string) bool {
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), "list", "--formula", formula)
	if err != nil {
		return false
	}
	return result.ExitCode == 0
}

// IsCaskInstalled checks if a cask is installed.
func (b *Brew) IsCaskInstalled(cask string) bool {
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), "list", "--cask", cask)
	if err != nil {
		return false
	}
	return result.ExitCode == 0
}

// ServiceRegistered reports whether `brew services` has a launchd entry for
// formula, whether or not the job is running right now.
func (b *Brew) ServiceRegistered(formula string) bool {
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), "services", "info", formula, "--json")
	if err != nil {
		return false
	}
	var info []struct {
		Registered bool `json:"registered"`
	}
	return json.Unmarshal([]byte(result.Stdout), &info) == nil && len(info) > 0 && info[0].Registered
}

// StartService registers formula with `brew services start`, which runs it
// now and at every login.
func (b *Brew) StartService(ctx context.Context, formula string) error {
	_, err := b.Runner.Run(ctx, b.brewCmd(), "services", "start", formula)
	return err
}

// Install installs formulas.
func (b *Brew) Install(ctx context.Context, formulas []string) error {
	if len(formulas) == 0 {
		return nil
	}
	for _, group := range formulaInstallGroups(formulas) {
		args := b.installArgs(group)
		if _, err := b.Runner.Run(ctx, b.brewCmd(), args...); err != nil {
			if stillMissing := b.MissingFormulas(group); len(stillMissing) > 0 {
				return err
			}
			b.Runner.Logger.Warn("brew install exited with error but formulas are present", "formulas", group, "err", err)
		}
	}
	return nil
}

// InstallCask installs casks. When force is true, `--force` is passed to brew
// so it reinstalls over an existing /Applications/<Name>.app target.
func (b *Brew) InstallCask(ctx context.Context, casks []string, force bool) error {
	if len(casks) == 0 {
		return nil
	}
	args := []string{"install", "--cask"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, casks...)
	_, err := b.Runner.Run(ctx, b.brewCmd(), args...)
	return err
}

// Tap ensures Homebrew taps are configured.
func (b *Brew) Tap(ctx context.Context, taps []string) error {
	for _, tap := range dedupeOrdered(taps) {
		if _, err := b.Runner.Run(ctx, b.brewCmd(), "tap", tap); err != nil {
			return err
		}
	}
	return nil
}

// TrustTaps marks non-official taps as trusted so Homebrew will load their
// formulae and casks.
//
// Homebrew 6 gates loading anything from a non-official tap behind
// `brew trust` (HOMEBREW_REQUIRE_TAP_TRUST). Without it the install fails with
// "Refusing to load <cask> from untrusted tap <tap>", which aborts the whole
// apply on a fresh machine. A tap the active config asks for is a tap the user
// asked for, so trust it here instead of making them run `brew trust` by hand
// for each one.
//
// Callers pass every tap their targets need, not just the ones just added:
// a tap that is already present but untrusted hits the same refusal, and that
// is the normal state on a machine where the tap arrived some other way.
//
// Best-effort by design. `brew trust` does not exist on older Homebrew, where
// there is no trust gate to satisfy, so a failure is logged and never fatal —
// if trust really was required, the install that follows reports it.
func (b *Brew) TrustTaps(ctx context.Context, taps []string) {
	for _, tap := range dedupeOrdered(taps) {
		if _, err := b.Runner.Run(ctx, b.brewCmd(), "trust", "--tap", tap); err != nil {
			b.Runner.Logger.Warn("brew trust failed; installs from this tap may be refused", "tap", tap, "err", err)
		}
	}
}

// TrustFormulaTaps trusts the taps required by the given formulas.
func (b *Brew) TrustFormulaTaps(ctx context.Context, formulas []string) {
	b.TrustTaps(ctx, TapsForFormulas(formulas))
}

// InstalledTaps returns the set of configured Homebrew taps. The bool is false
// when the brew query failed, so callers can distinguish "missing" from
// "unknown".
func (b *Brew) InstalledTaps() (map[string]bool, bool) {
	installed := make(map[string]bool)
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), "tap")
	if err != nil || result.ExitCode != 0 {
		return installed, false
	}
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			installed[s] = true
		}
	}
	return installed, true
}

// MissingTaps returns taps from the list that are not configured.
func (b *Brew) MissingTaps(taps []string) []string {
	taps = dedupeOrdered(taps)
	if len(taps) == 0 {
		return nil
	}
	installed, ok := b.InstalledTaps()
	if !ok {
		return taps
	}
	return missingFromInstalled(installed, taps)
}

// TapsForFormulas returns Homebrew taps required by unqualified formula names.
func TapsForFormulas(formulas []string) []string {
	var taps []string
	for _, formula := range formulas {
		if tap := formulaTaps[formulaName(formula)]; tap != "" {
			taps = append(taps, tap)
		}
	}
	return dedupeOrdered(taps)
}

// MissingFormulaTaps returns required formula taps that are not configured.
func (b *Brew) MissingFormulaTaps(formulas []string) []string {
	return b.MissingTaps(TapsForFormulas(formulas))
}

// InstallableFormulas returns formulas supported on the current OS.
func (b *Brew) InstallableFormulas(formulas []string) []string {
	return installableFormulasForGOOS(formulas, runtime.GOOS)
}

// ExistingCaskTargets returns the subset of casks whose .app artifact already
// exists under /Applications. Used to skip casks that would otherwise trip
// brew's "It seems there is already an App at ..." error when the app was
// installed outside of Homebrew (App Store, manual download, etc.).
//
// Returns an empty map on query failure so the caller proceeds with install
// and brew surfaces the real error.
func (b *Brew) ExistingCaskTargets(casks []string) map[string]bool {
	out := make(map[string]bool)
	if len(casks) == 0 {
		return out
	}
	args := append([]string{"info", "--cask", "--json=v2"}, casks...)
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), args...)
	if err != nil || result.ExitCode != 0 {
		return out
	}

	var payload struct {
		Casks []struct {
			Token     string            `json:"token"`
			Artifacts []json.RawMessage `json:"artifacts"`
		} `json:"casks"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return out
	}

	for _, c := range payload.Casks {
		for _, raw := range c.Artifacts {
			// Each artifact is an object like {"app": [...]} or {"uninstall": [...]}.
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				continue
			}
			appRaw, ok := obj["app"]
			if !ok {
				continue
			}
			var appList []json.RawMessage
			if err := json.Unmarshal(appRaw, &appList); err != nil {
				continue
			}
			for _, entry := range appList {
				name := extractAppName(entry)
				if name == "" {
					continue
				}
				if _, err := os.Stat(filepath.Join("/Applications", name)); err == nil {
					out[c.Token] = true
					break
				}
			}
			if out[c.Token] {
				break
			}
		}
	}
	return out
}

// extractAppName pulls the .app name from a brew cask `app` artifact entry,
// which is either a plain string ("Raycast.app") or a 2-tuple
// ("Source.app", {"target": "Target.app"}). The target path, when present,
// is what actually lands under /Applications.
func extractAppName(entry json.RawMessage) string {
	var s string
	if err := json.Unmarshal(entry, &s); err == nil {
		return filepath.Base(s)
	}
	var tuple []json.RawMessage
	if err := json.Unmarshal(entry, &tuple); err != nil || len(tuple) == 0 {
		return ""
	}
	var source string
	_ = json.Unmarshal(tuple[0], &source)
	if len(tuple) >= 2 {
		var meta struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(tuple[1], &meta); err == nil && meta.Target != "" {
			return filepath.Base(meta.Target)
		}
	}
	return filepath.Base(source)
}

// InstallBrew installs Homebrew non-interactively.
func (b *Brew) InstallBrew(ctx context.Context) error {
	script := `NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`
	_, err := b.Runner.RunShell(ctx, script)
	if err != nil {
		return fmt.Errorf("install homebrew: %w", err)
	}
	// Add brew to PATH for this process
	b.RefreshPath()
	return nil
}

// RefreshPath adds the Homebrew bin directory to PATH for the current process.
//
// This mutates process-global state, so it belongs only on a WRITE path (D-02).
// Its two remaining callers are PackagesModule.Apply and InstallBrew just above,
// where making a just-installed brew reachable to everything that follows is the
// point. The third caller, PackagesModule.Check, was removed with BUG-06:
// detection no longer needs this because brewProgram resolves the prefix itself.
func (b *Brew) RefreshPath() {
	for _, p := range brewPrefixDirs() {
		if _, err := os.Stat(p); err == nil {
			os.Setenv("PATH", p+":"+os.Getenv("PATH"))
		}
	}
	// Clear cached lookups so IsAvailable() picks up the new PATH
	_, _ = osexec.LookPath("brew") // warm cache
}

// MissingFormulas returns formulas from the list that are not installed.
func (b *Brew) MissingFormulas(formulas []string) []string {
	// Use brew list --formula -1 to get all installed formulas at once
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), "list", "--formula", "-1")
	if err != nil {
		return formulas // assume all missing if we can't check
	}

	installed := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if formula := strings.TrimSpace(line); formula != "" {
			installed[formula] = true
		}
	}

	var missing []string
	for _, f := range formulas {
		if !isFormulaInstalled(installed, f) {
			missing = append(missing, f)
		}
	}
	return missing
}

func isFormulaInstalled(installed map[string]bool, formula string) bool {
	if installed[formula] {
		return true
	}
	return installed[formulaName(formula)]
}

func formulaName(formula string) string {
	if i := strings.LastIndex(formula, "/"); i >= 0 && i+1 < len(formula) {
		return formula[i+1:]
	}
	return formula
}

func installableFormulasForGOOS(formulas []string, goos string) []string {
	out := make([]string, 0, len(formulas))
	for _, formula := range formulas {
		if darwinOnlyFormulas[formulaName(formula)] && goos != "darwin" {
			continue
		}
		out = append(out, formula)
	}
	return out
}

func (b *Brew) installArgs(formulas []string) []string {
	args := []string{"install"}
	if needsExplicitBrewCompiler(formulas) {
		if cc := b.brewGCCCompiler(); cc != "" {
			args = append(args, "--cc="+cc)
		}
	}
	return append(args, formulas...)
}

func needsExplicitBrewCompiler(formulas []string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	for _, formula := range formulas {
		if formula == "oven-sh/bun/bun" {
			return true
		}
	}
	return false
}

func (b *Brew) brewGCCCompiler() string {
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), "list", "--versions", "gcc")
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	return gccCompilerFromBrewVersions(result.Stdout)
}

func gccCompilerFromBrewVersions(output string) string {
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "gcc" {
		return ""
	}
	major := fields[1]
	if i := strings.Index(major, "."); i >= 0 {
		major = major[:i]
	}
	if major == "" {
		return ""
	}
	for _, r := range major {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "gcc-" + major
}

func formulaInstallGroups(formulas []string) [][]string {
	var groups [][]string
	var current []string
	for _, formula := range formulas {
		if isTapQualifiedFormula(formula) {
			if len(current) > 0 {
				groups = append(groups, current)
				current = nil
			}
			groups = append(groups, []string{formula})
			continue
		}
		current = append(current, formula)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func isTapQualifiedFormula(formula string) bool {
	return strings.Count(formula, "/") >= 2
}

func missingFromInstalled(installed map[string]bool, values []string) []string {
	var missing []string
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		if !installed[v] {
			missing = append(missing, v)
		}
	}
	return missing
}

func dedupeOrdered(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// InstalledCasks returns the set of all currently installed casks, or nil
// when the query itself failed.
//
// The nil is load-bearing and must not be normalized into an empty map: a
// caller cannot otherwise tell "Homebrew says nothing is installed" from
// "Homebrew could not be asked", and `dot apps status` renders those two as
// different states (BUG-10). Returning an empty map on error made every
// tracked app read as confidently absent whenever brew was unhealthy.
func (b *Brew) InstalledCasks() map[string]bool {
	result, err := b.Runner.RunQuery(context.Background(), b.brewCmd(), "list", "--cask", "-1")
	if err != nil {
		return nil
	}
	installed := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			installed[s] = true
		}
	}
	return installed
}

// MissingCasks returns casks from the list that are not installed.
func (b *Brew) MissingCasks(casks []string) []string {
	installed := b.InstalledCasks()
	if len(installed) == 0 && len(casks) > 0 {
		// Query failed; assume all missing so caller can attempt install.
		return casks
	}
	var missing []string
	for _, c := range casks {
		if !installed[c] {
			missing = append(missing, c)
		}
	}
	return missing
}
