package appsettings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config/catalog"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
)

// Per-command entry points for the `dot apps` family. Each entry takes an
// Options struct of plain values and returns a typed result; wording,
// styling, prompting, and flag reading stay in internal/cli.

// installedCasks reports the installed cask set, or nil when Homebrew could
// not be queried at all. Callers distinguish "not installed" from "could not
// tell", so the nil map is meaningful and must not be normalized away.
func installedCasks(brew *exec.Brew) map[string]bool {
	if brew == nil || !brew.IsAvailable() {
		return nil
	}
	return brew.InstalledCasks()
}

// --- list ---

// ListOptions carries what `dot apps list` needs beyond the embedded
// catalog. Brew is nil when Homebrew cannot be queried (non-macOS host, or
// the binary is missing).
type ListOptions struct {
	Brew *exec.Brew
}

// ListApp is one catalog entry with its membership flags resolved.
type ListApp struct {
	Token       string
	Name        string
	Default     bool
	Recommended bool
	Installed   bool
}

// ListGroup mirrors one catalog group, in catalog order.
type ListGroup struct {
	Name string
	Apps []ListApp
}

// ListResult is the catalog with every entry's flags resolved.
type ListResult struct {
	Groups []ListGroup
}

// List loads the cask catalog and resolves the default/recommended/installed
// flags for every entry.
func List(opts ListOptions) (*ListResult, error) {
	cat, err := catalog.LoadMacApps()
	if err != nil {
		return nil, err
	}
	installed := installedCasks(opts.Brew)
	defaults := make(map[string]bool, len(cat.Defaults))
	for _, t := range cat.Defaults {
		defaults[t] = true
	}
	recommended := make(map[string]bool, len(cat.Recommended))
	for _, t := range cat.Recommended {
		recommended[t] = true
	}

	res := &ListResult{Groups: make([]ListGroup, 0, len(cat.Groups))}
	for _, g := range cat.Groups {
		lg := ListGroup{Name: g.Name, Apps: make([]ListApp, 0, len(g.Apps))}
		for _, a := range g.Apps {
			lg.Apps = append(lg.Apps, ListApp{
				Token:       a.Token,
				Name:        a.Name,
				Default:     defaults[a.Token],
				Recommended: recommended[a.Token],
				Installed:   installed != nil && installed[a.Token],
			})
		}
		res.Groups = append(res.Groups, lg)
	}
	return res, nil
}

// --- status ---

// StatusOptions carries what `dot apps status` needs beyond the Engine's own
// fields. Brew is nil when Homebrew cannot be queried.
type StatusOptions struct {
	Brew *exec.Brew
}

// StatusApp pairs one app's live/archive counts with its install state.
// InstallKnown is false when Homebrew could not be queried at all, which is
// a third state distinct from "installed" and "not installed".
type StatusApp struct {
	AppStatus
	Installed    bool
	InstallKnown bool
}

// StatusResult is the whole report, apps sorted by token.
type StatusResult struct {
	Hostname string
	HostRoot string
	Apps     []StatusApp
}

// StatusReport collects install and backup presence for every manifest app.
func (e *Engine) StatusReport(opts StatusOptions) *StatusResult {
	installed := installedCasks(opts.Brew)

	statuses := e.Status(nil)
	tokens := make([]string, 0, len(statuses))
	byToken := make(map[string]AppStatus, len(statuses))
	for _, s := range statuses {
		tokens = append(tokens, s.Token)
		byToken[s.Token] = s
	}
	sort.Strings(tokens)

	res := &StatusResult{
		Hostname: e.Hostname,
		HostRoot: e.HostRoot(),
		Apps:     make([]StatusApp, 0, len(tokens)),
	}
	for _, token := range tokens {
		res.Apps = append(res.Apps, StatusApp{
			AppStatus:    byToken[token],
			Installed:    installed[token],
			InstallKnown: installed != nil,
		})
	}
	return res
}

// --- progress events ---

// EventKind names one observable step outcome. The engine emits kinds and
// internal/cli renders them, so wording and styling never cross the seam.
type EventKind int

const (
	EventAllInstalled     EventKind = iota // every selected cask is already present
	EventSkippedExisting                   // Tokens: casks skipped, their .app already exists
	EventNothingToInstall                  // the skip filter consumed the whole batch
	EventDryRunTap                         // Tokens: taps a real run would add
	EventDryRunInstall                     // Tokens: casks a real run would install
	EventTapping                           // Tokens: taps being added now
	EventInstalling                        // Tokens: casks being installed now
	EventInstallComplete                   // brew finished without error
	EventAppDiscovered                     // Token, Paths: non-manifest app resolved on disk
	EventAppNotFound                       // Token: neither in the manifest nor on disk
)

// Event is one step outcome. Only the fields its kind documents are set.
type Event struct {
	Kind   EventKind
	Token  string
	Tokens []string
	Paths  int
}

// emit delivers an event when the caller asked for progress. A nil Progress
// is the unattended case, not an error.
func emit(progress func(Event), e Event) {
	if progress != nil {
		progress(e)
	}
}

// --- install ---

// InstallSelectOptions carries the flag reads and saved-state slices that
// decide what `dot apps install` installs.
type InstallSelectOptions struct {
	Args        []string
	All         bool
	Defaults    bool
	Recommended bool
	ForceSelect bool
	Yes         bool

	SavedCasks        []string
	SavedCasksExtra   []string
	SavedTerminalApps []string
}

// InstallSelection is either a resolved token list or a request for the
// interactive picker, in which case PickTokens/Preselect/CatalogGroups carry
// the picker's inputs. The picker itself stays in internal/cli (D-09).
type InstallSelection struct {
	Tokens      []string
	Interactive bool

	PickTokens    []string
	Preselect     []string
	CatalogGroups int
}

// SelectInstall resolves the install set from flags and saved state, or
// reports that the interactive picker is needed.
func SelectInstall(opts InstallSelectOptions) (*InstallSelection, error) {
	cat, err := catalog.LoadMacApps()
	if err != nil {
		return nil, err
	}

	sel := &InstallSelection{}
	switch {
	case opts.All:
		sel.Tokens = cat.AllTokens()
	case opts.Defaults:
		sel.Tokens = cat.Defaults
	case opts.Recommended:
		sel.Tokens = cat.Recommended
	case len(opts.Args) > 0:
		// Trust explicit args; merge with user's stored extras if asked later.
		sel.Tokens = sliceutil.Dedupe(opts.Args)
	case opts.ForceSelect || !opts.Yes:
		sel.Interactive = true
	default:
		// --yes without args: use saved state, fall back to recommended.
		want := append([]string(nil), opts.SavedCasks...)
		if len(want) == 0 {
			want = cat.Recommended
		}
		want = append(want, opts.SavedTerminalApps...)
		want = append(want, opts.SavedCasksExtra...)
		sel.Tokens = sliceutil.Dedupe(want)
	}
	if !sel.Interactive {
		return sel, nil
	}

	sel.PickTokens = cat.AllTokens()
	sort.Strings(sel.PickTokens)
	preselect := append([]string(nil), opts.SavedCasks...)
	if len(preselect) == 0 {
		preselect = cat.Recommended
	}
	sel.Preselect = sliceutil.Dedupe(append(preselect, opts.SavedTerminalApps...))
	sel.CatalogGroups = len(cat.Groups)
	return sel, nil
}

// InstallOptions carries the resolved install set and the flags that change
// how it is applied.
type InstallOptions struct {
	Brew     *exec.Brew
	Tokens   []string
	Force    bool
	DryRun   bool
	Progress func(Event)
}

// Install installs the selected casks: legacy tokens are normalized, casks
// whose bundle is already present are filtered out unless Force is set,
// missing taps are added, and the remainder is handed to brew. Nothing is
// reported back beyond the progress events, so the result is a bare error.
func Install(ctx context.Context, opts InstallOptions) error {
	if len(opts.Tokens) == 0 {
		return fmt.Errorf("nothing to install")
	}
	cat, err := catalog.LoadMacApps()
	if err != nil {
		return err
	}

	// Rewrite legacy tokens from pre-rename state (anchor -> maru-workspace)
	// before any brew call, same as the macapps module does.
	want := catalog.NormalizeCaskTokens(opts.Tokens)

	missing := opts.Brew.MissingCasks(want)
	if len(missing) == 0 {
		emit(opts.Progress, Event{Kind: EventAllInstalled})
		return nil
	}

	// Filter out casks whose .app already exists under /Applications (e.g.
	// installed via the App Store). Without this, brew aborts the whole batch
	// on the first conflict. Force bypasses the skip and reinstalls.
	if !opts.Force {
		existing := opts.Brew.ExistingCaskTargets(missing)
		for _, cask := range missing {
			if app := cat.AppBundle(cask); app != "" {
				if _, err := os.Stat(filepath.Join("/Applications", app)); err == nil {
					existing[cask] = true
				}
			}
		}
		if len(existing) > 0 {
			var toInstall, skipped []string
			for _, c := range missing {
				if existing[c] {
					skipped = append(skipped, c)
				} else {
					toInstall = append(toInstall, c)
				}
			}
			emit(opts.Progress, Event{Kind: EventSkippedExisting, Tokens: skipped})
			missing = toInstall
		}
	}

	if len(missing) == 0 {
		emit(opts.Progress, Event{Kind: EventNothingToInstall})
		return nil
	}
	missingTaps := opts.Brew.MissingTaps(cat.TapsForTokens(missing))
	if opts.DryRun {
		if len(missingTaps) > 0 {
			emit(opts.Progress, Event{Kind: EventDryRunTap, Tokens: missingTaps})
		}
		emit(opts.Progress, Event{Kind: EventDryRunInstall, Tokens: missing})
		return nil
	}
	if len(missingTaps) > 0 {
		emit(opts.Progress, Event{Kind: EventTapping, Tokens: missingTaps})
		if err := opts.Brew.Tap(ctx, missingTaps); err != nil {
			return fmt.Errorf("tap homebrew repositories: %w", err)
		}
	}
	emit(opts.Progress, Event{Kind: EventInstalling, Tokens: missing})
	if err := opts.Brew.InstallCask(ctx, missing, opts.Force); err != nil {
		return fmt.Errorf("install casks: %w", err)
	}
	emit(opts.Progress, Event{Kind: EventInstallComplete})
	return nil
}

// --- backup selection ---

// AdoptRequestedApps resolves explicitly requested tokens. A token the
// manifest does not carry (e.g. a display name like "Moom Classic") is
// looked up on disk and appended to the in-memory manifest, because
// selectTokens would otherwise drop it without a word. Unresolvable tokens
// are reported and left out.
func (e *Engine) AdoptRequestedApps(args []string, progress func(Event)) []string {
	tokens := sliceutil.Dedupe(args)
	for _, t := range tokens {
		if e.Manifest.App(t) != nil {
			continue
		}
		discovered := DiscoverApp(e.HomeDir, t)
		if discovered == nil {
			emit(progress, Event{Kind: EventAppNotFound, Token: t})
			continue
		}
		e.Manifest.Apps = append(e.Manifest.Apps, *discovered)
		emit(progress, Event{Kind: EventAppDiscovered, Token: t, Paths: len(discovered.Paths)})
	}
	return tokens
}

// AdoptSavedApps re-resolves saved selections the embedded manifest does not
// carry (apps discovered by name in an earlier interactive run) so the
// manifest intersection does not silently drop them. Silent by design: a
// token whose app is gone from disk still restores from the archive layout
// via AdoptArchivedApps.
func (e *Engine) AdoptSavedApps(saved []string) {
	for _, t := range saved {
		if e.Manifest.App(t) != nil {
			continue
		}
		if discovered := DiscoverApp(e.HomeDir, t); discovered != nil {
			e.Manifest.Apps = append(e.Manifest.Apps, *discovered)
		}
	}
}

// StampLastBackup writes the last-backup stamp for a completed run. A dry
// run and a run with failed paths are not stamped: the stamp claims the
// archive is current and neither of those leaves it current. Reports whether
// the stamp applied, so the caller can record the same fact in user state.
func (e *Engine) StampLastBackup(sum *Summary, tokens []string) (bool, error) {
	if e.Runner.DryRun || sum.Failed > 0 {
		return false, nil
	}
	err := e.WriteLastBackupStamp(BackupStamp{
		CreatedAt: time.Now().UTC(),
		Tokens:    tokens,
		Files:     sum.Files,
	})
	return err == nil, err
}
