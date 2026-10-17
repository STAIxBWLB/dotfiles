package module

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config/catalog"
)

// MacAppsModule installs macOS cask applications selected during init or
// listed in the active profile. No-op on non-darwin.
type MacAppsModule struct{}

func (m *MacAppsModule) Name() string { return "macapps" }

// resolveCasks returns the de-duplicated cask list the module should ensure.
// Config.Casks wins; when empty we fall back to the embedded catalog defaults
// so a profile can opt in without repeating the list. Legacy tokens from
// pre-rename state files are normalized to their current cask names.
func (m *MacAppsModule) resolveCasks(rc *RunContext) []string {
	if len(rc.Config.Casks) > 0 {
		return catalog.NormalizeCaskTokens(rc.Config.AllCasks())
	}
	cat, err := catalog.LoadMacApps()
	if err != nil {
		rc.Runner.Logger.Warn("macapps catalog load", "err", err)
		return nil
	}
	cfg := *rc.Config
	cfg.Casks = cat.Defaults
	return catalog.NormalizeCaskTokens(cfg.AllCasks())
}

func (m *MacAppsModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	if runtime.GOOS != "darwin" {
		return &CheckResult{Satisfied: true}, nil
	}
	if rc.Brew == nil || !rc.Brew.IsAvailable() {
		return &CheckResult{
			Satisfied: false,
			Changes: []Change{{
				Description: "homebrew not available; install brew first",
				Command:     "(see scripts/install.sh)",
			}},
		}, nil
	}

	casks := m.resolveCasks(rc)
	if len(casks) == 0 {
		return &CheckResult{Satisfied: true}, nil
	}

	missing := rc.Brew.MissingCasks(casks)
	missing, _ = m.splitExistingCaskTargets(rc, missing)
	missingTaps := m.missingTaps(rc, missing)
	if len(missing) == 0 && len(missingTaps) == 0 {
		return &CheckResult{Satisfied: true}, nil
	}

	var changes []Change
	for _, tap := range missingTaps {
		changes = append(changes, Change{
			Description: fmt.Sprintf("tap Homebrew repository %q", tap),
			Command:     "brew tap " + tap,
		})
	}
	if len(missing) > 0 {
		changes = append(changes, Change{
			Description: fmt.Sprintf("install %d cask(s): %s", len(missing), strings.Join(missing, ", ")),
			Command:     "brew install --cask " + strings.Join(missing, " "),
		})
	}
	return &CheckResult{Satisfied: false, Changes: changes}, nil
}

func (m *MacAppsModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	if runtime.GOOS != "darwin" {
		return &ApplyResult{}, nil
	}
	if rc.Brew == nil || !rc.Brew.IsAvailable() {
		return nil, fmt.Errorf("homebrew not available")
	}

	casks := m.resolveCasks(rc)
	if len(casks) == 0 {
		return &ApplyResult{}, nil
	}
	missing := rc.Brew.MissingCasks(casks)
	if len(missing) == 0 {
		return &ApplyResult{}, nil
	}

	// Skip casks whose .app already exists under /Applications. apply is
	// idempotent and should not fail when an app was installed outside brew.
	missing, skipped := m.splitExistingCaskTargets(rc, missing)
	if len(skipped) > 0 {
		rc.Runner.Logger.Info("macapps: skipping externally-installed casks", "tokens", skipped)
	}

	if len(missing) == 0 {
		msgs := []string{"no casks to install"}
		if len(skipped) > 0 {
			msgs = append(msgs, fmt.Sprintf("skipped %d already-present: %s", len(skipped), strings.Join(skipped, ", ")))
		}
		return &ApplyResult{Messages: msgs}, nil
	}

	var msgs []string
	if missingTaps := m.missingTaps(rc, missing); len(missingTaps) > 0 {
		if err := rc.Brew.Tap(ctx, missingTaps); err != nil {
			return nil, fmt.Errorf("tap homebrew repositories: %w", err)
		}
		msgs = append(msgs, fmt.Sprintf("tapped %d Homebrew repo(s): %s", len(missingTaps), strings.Join(missingTaps, ", ")))
	}
	// Every tap these casks need, not just the ones tapped above: an
	// already-present tap can still be untrusted, which fails the install.
	rc.Brew.TrustTaps(ctx, m.requiredTaps(rc, missing))

	if err := rc.Brew.InstallCask(ctx, missing, false); err != nil {
		return nil, fmt.Errorf("install casks: %w", err)
	}
	msgs = append(msgs, fmt.Sprintf("installed %d cask(s): %s", len(missing), strings.Join(missing, ", ")))
	if len(skipped) > 0 {
		msgs = append(msgs, fmt.Sprintf("skipped %d already-present: %s", len(skipped), strings.Join(skipped, ", ")))
	}
	return &ApplyResult{Changed: true, Messages: msgs}, nil
}

func (m *MacAppsModule) splitExistingCaskTargets(rc *RunContext, casks []string) ([]string, []string) {
	if len(casks) == 0 || rc.Brew == nil {
		return casks, nil
	}
	existing := rc.Brew.ExistingCaskTargets(casks)
	if cat, err := catalog.LoadMacApps(); err == nil {
		for _, cask := range casks {
			if app := cat.AppBundle(cask); app != "" &&
				rc.Runner.FileExists(filepath.Join("/Applications", app)) {
				existing[cask] = true
			}
		}
	}
	if len(existing) == 0 {
		return casks, nil
	}
	var toInstall, skipped []string
	for _, c := range casks {
		if existing[c] {
			skipped = append(skipped, c)
		} else {
			toInstall = append(toInstall, c)
		}
	}
	return toInstall, skipped
}

// requiredTaps returns every tap the given cask tokens declare in the catalog,
// whether or not it is already configured.
func (m *MacAppsModule) requiredTaps(rc *RunContext, casks []string) []string {
	if len(casks) == 0 || rc.Brew == nil {
		return nil
	}
	cat, err := catalog.LoadMacApps()
	if err != nil {
		rc.Runner.Logger.Warn("macapps catalog load", "err", err)
		return nil
	}
	return cat.TapsForTokens(casks)
}

func (m *MacAppsModule) missingTaps(rc *RunContext, casks []string) []string {
	taps := m.requiredTaps(rc, casks)
	if len(taps) == 0 {
		return nil
	}
	return rc.Brew.MissingTaps(taps)
}
