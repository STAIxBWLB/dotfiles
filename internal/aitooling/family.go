package aitooling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

// Family tools (#319): amem and vmem (and acon and ares once published) install side by side, one npm prefix per
// version under ~/.local/share/dotfiles/tools/<id>/<version>/. The `current` link names the active
// version and `previous` the one before it; ~/.local/bin/<bin> points through `current`. Switching
// `current` is one rename: new sessions start the new version and running ones keep their files.

// FamilyTool is one family tool; an empty Package means it has no published npm package yet. A
// non-empty External names the manager that owns the tool's installs, and dot installs none of it.
type FamilyTool struct{ ID, Package, Binary, External string }

func FamilyTools() []FamilyTool {
	return []FamilyTool{
		// ahub stages its own releases and a PATH link here would shadow every later ahub upgrade, so it
		// stays out until agent-hub#1039 defines the hand-off.
		{"ahub", "@staix/agent-hub", "ahub", "managed by ahub upgrade (~/.agenthub/releases)"},
		{"amem", "@staix/agent-mem", "amem", ""},
		{"vmem", "@staix/vault-mem", "vmem", ""},
		{"acon", "", "acon", ""},
		{"ares", "", "ares", ""},
	}
}

// FamilyResult reports one install, rollback or status line.
type FamilyResult struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Current  string   `json:"current,omitempty"`
	Previous string   `json:"previous,omitempty"`
	Versions []string `json:"versions,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

func familyTool(id string) (FamilyTool, error) {
	for _, t := range FamilyTools() {
		if t.ID == id {
			return t, nil
		}
	}
	ids := []string{}
	for _, t := range FamilyTools() {
		ids = append(ids, t.ID)
	}
	return FamilyTool{}, fmt.Errorf("unknown family tool %q (valid: %s)", id, strings.Join(ids, ", "))
}

// FamilyRoot is ~/.local/share/dotfiles/tools.
func (e *Engine) FamilyRoot() string {
	return filepath.Join(e.opts.HomeDir, ".local", "share", "dotfiles", "tools")
}

func (e *Engine) familyLink(t FamilyTool) string {
	return filepath.Join(e.opts.HomeDir, ".local", "bin", t.Binary)
}

// FamilyInstall installs version (the registry's latest when empty) into its own directory, then
// makes it current. An existing verified directory for that version is reused.
func (e *Engine) FamilyInstall(ctx context.Context, id, version string, adopt bool) (FamilyResult, error) {
	t, err := familyTool(id)
	if err != nil {
		return FamilyResult{}, err
	}
	r := FamilyResult{ID: id}
	if t.External != "" {
		r.Status = "skipped"
		r.Notes = []string{t.External + "; dot installs nothing for it"}
		return r, nil
	}
	if t.Package == "" {
		r.Status = "skipped"
		r.Notes = []string{id + " has no published npm package yet; nothing was installed"}
		return r, nil
	}
	if version == "" {
		if version, err = e.latest(ctx, "https://registry.npmjs.org/"+t.Package+"/latest"); err != nil {
			return r, fmt.Errorf("%s latest version: %w", t.Package, err)
		}
	}
	version = strings.TrimPrefix(version, "v")
	if !stableVersion.MatchString(version) {
		return r, fmt.Errorf("version for %s must be a stable X.Y.Z version", id)
	}
	dir := filepath.Join(e.FamilyRoot(), id, version)
	if e.opts.DryRun {
		r.Status = "planned"
		r.Notes = []string{fmt.Sprintf("would install %s@%s into %s and make it current", t.Package, version, dir)}
		return r, nil
	}
	release, deferredR, err := e.familySlot(ctx, r)
	if release == nil {
		return deferredR, err
	}
	defer release()
	if _, err := os.Lstat(dir); err == nil {
		if err := verifyFamilyDir(t, dir, version); err != nil {
			return r, fmt.Errorf("%s exists but is not a verified install (%v); remove it and retry", dir, err)
		}
		r.Notes = append(r.Notes, "reused the existing install in "+dir)
	} else if err := e.familyNPMInstall(ctx, t, version, dir); err != nil {
		return r, err
	}
	return e.familyActivate(ctx, t, version, adopt, r)
}

// familySlot takes the maintenance slot `dot ai update` uses, so installs, rollbacks and their
// pruning never interleave. A nil release means the caller returns the result and error as they are.
func (e *Engine) familySlot(ctx context.Context, r FamilyResult) (func(), FamilyResult, error) {
	release, err := e.acquire(ctx, resourceguard.Options{HomeDir: e.opts.HomeDir, Purpose: "family tool " + r.ID, ScopeKey: "tooling"})
	if err != nil {
		var deferred *resourceguard.DeferredError
		if errors.As(err, &deferred) {
			r.Status = "deferred-resource-pressure"
			r.Notes = []string{deferred.Reason}
			return nil, r, nil
		}
		return nil, r, err
	}
	return release, r, nil
}

// familyNPMInstall installs into a staging prefix and renames it into place, so a failed install
// never leaves a half-written version directory. npm's bin links are relative, so the rename keeps
// them valid.
func (e *Engine) familyNPMInstall(ctx context.Context, t FamilyTool, version, dir string) error {
	npm := e.find("npm")
	if npm == "" {
		return fmt.Errorf("npm prerequisite missing; install Node.js before %s", t.ID)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".staging-"+version+"-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if _, err := e.run(ctx, npm, "install", "--global", "--prefix", staging, "--registry=https://registry.npmjs.org", "--no-fund", "--no-audit", t.Package+"@"+version); err != nil {
		return err
	}
	if err := verifyFamilyDir(t, staging, version); err != nil {
		return fmt.Errorf("npm returned, but the install was not verified: %w", err)
	}
	return os.Rename(staging, dir)
}

// verifyFamilyDir checks the installed package name and version and the bin entry without running it.
func verifyFamilyDir(t FamilyTool, dir, version string) error {
	data, err := os.ReadFile(filepath.Join(dir, "lib", "node_modules", t.Package, "package.json"))
	if err != nil {
		return errors.New("package.json missing")
	}
	var pkg struct{ Name, Version string }
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Name != t.Package || pkg.Version != version {
		return fmt.Errorf("package.json does not name %s@%s", t.Package, version)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", t.Binary)); err != nil {
		return fmt.Errorf("bin/%s missing", t.Binary)
	}
	return nil
}

// FamilyRollback makes `previous` current again; the version it leaves becomes `previous`.
func (e *Engine) FamilyRollback(ctx context.Context, id string) (FamilyResult, error) {
	t, err := familyTool(id)
	if err != nil {
		return FamilyResult{}, err
	}
	r := FamilyResult{ID: id}
	if t.External != "" {
		r.Status = "skipped"
		r.Notes = []string{t.External + "; roll it back there"}
		return r, nil
	}
	noPrevious := fmt.Errorf("%s has no previous version to roll back to", id)
	if readVersionLink(filepath.Join(e.FamilyRoot(), id, "previous")) == "" {
		return r, noPrevious
	}
	if !e.opts.DryRun {
		release, deferredR, err := e.familySlot(ctx, r)
		if release == nil {
			return deferredR, err
		}
		defer release()
	}
	prev := readVersionLink(filepath.Join(e.FamilyRoot(), id, "previous"))
	if prev == "" {
		return r, noPrevious
	}
	if err := verifyFamilyDir(t, filepath.Join(e.FamilyRoot(), id, prev), prev); err != nil {
		return r, fmt.Errorf("previous %s %s is not a verified install: %w", id, prev, err)
	}
	if e.opts.DryRun {
		r.Status = "planned"
		r.Notes = []string{"would make " + prev + " current"}
		return r, nil
	}
	return e.familyActivate(ctx, t, prev, false, r)
}

// familyActivate moves `previous` to the old current and `current` to version, each by an atomic
// rename. Running processes are reported and never stopped; versions other than current and previous
// are removed only when no running process uses them.
func (e *Engine) familyActivate(ctx context.Context, t FamilyTool, version string, adopt bool, r FamilyResult) (FamilyResult, error) {
	toolDir := filepath.Join(e.FamilyRoot(), t.ID)
	cur := readVersionLink(filepath.Join(toolDir, "current"))
	lines := e.familyProcesses(ctx, t)
	r.Status = "active"
	if cur != version {
		if cur != "" {
			if err := swapLink(filepath.Join(toolDir, "previous"), cur); err != nil {
				return r, err
			}
		}
		if err := swapLink(filepath.Join(toolDir, "current"), version); err != nil {
			return r, err
		}
		r.Status = "activated"
		if note := drainNote(pidsOf(lines)); note != "" {
			r.Notes = append(r.Notes, strings.TrimPrefix(note, "; "))
		}
	}
	r.Current, r.Previous = version, readVersionLink(filepath.Join(toolDir, "previous"))
	r.Notes = append(r.Notes, e.familyPathEntry(t, adopt)...)
	r.Notes = append(r.Notes, e.familyPrune(t, toolDir, r.Current, r.Previous, lines)...)
	r.Versions = familyVersions(toolDir)
	if t.ID == "amem" || t.ID == "vmem" {
		root := filepath.Join(toolDir, "current", "lib", "node_modules", t.Package)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		note := fmt.Sprintf("to register this release with an agent, run: %s setup <target> --root '%s' (a preview; add --apply to write). dot did not run it", t.Binary, root)
		if t.ID == "amem" {
			note += "; --root needs an amem release that has it (agent-mem#199)"
		}
		r.Notes = append(r.Notes, note)
	}
	return r, nil
}

// familyPathEntry links ~/.local/bin/<bin> through `current`. A link it did not make is replaced only
// with adopt, and a regular file never.
func (e *Engine) familyPathEntry(t FamilyTool, adopt bool) []string {
	link := e.familyLink(t)
	want := filepath.Join(e.FamilyRoot(), t.ID, "current", "bin", t.Binary)
	var notes []string
	st, err := os.Lstat(link)
	switch {
	case err != nil:
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return []string{"PATH entry not written: " + err.Error()}
		}
		if err := swapLink(link, want); err != nil {
			return []string{"PATH entry not written: " + err.Error()}
		}
		notes = append(notes, link+" now points at "+want)
	case st.Mode()&os.ModeSymlink == 0:
		return []string{link + " is a file from another install and was left alone; new sessions keep starting it"}
	default:
		got := readLinkAbs(link)
		if got != want {
			if !adopt {
				return []string{fmt.Sprintf("%s points at %s, another install, and was left alone; rerun with --adopt to point it at %s", link, got, want)}
			}
			if err := swapLink(link, want); err != nil {
				return []string{"PATH entry not written: " + err.Error()}
			}
			notes = append(notes, fmt.Sprintf("%s now points at %s (was %s; that install is untouched)", link, want, got))
		}
	}
	if found := e.find(t.Binary); found != "" && found != link {
		notes = append(notes, fmt.Sprintf("PATH finds %s at %s first; new sessions start that one until it is removed or %s comes earlier on PATH", t.Binary, found, filepath.Dir(link)))
	}
	return notes
}

// familyPrune keeps current and previous and every version a running process uses.
func (e *Engine) familyPrune(t FamilyTool, toolDir, current, previous string, lines []string) []string {
	return PruneInstallVersions(t.ID, toolDir, current, previous, e.familyLink(t), familyVersions(toolDir), lines, func(line string) bool {
		return strings.Contains(line, "/node_modules/"+t.Package+"/")
	})
}

// PruneInstallVersions keeps current, previous and versions used by processes.
// If any process cannot be tied to a version, all versions stay. Callers supply
// validated directory names and hold their install's maintenance slot.
func PruneInstallVersions(id, toolDir, current, previous, pathEntry string, versions, lines []string, versioned func(string) bool) []string {
	var notes []string
	if slices.ContainsFunc(lines, func(l string) bool { return !versioned(l) }) {
		for _, v := range versions {
			if v != current && v != previous {
				notes = append(notes, fmt.Sprintf("kept %s %s: a running process started through %s cannot be tied to one version", id, v, pathEntry))
			}
		}
		return notes
	}
	for _, v := range versions {
		if v == current || v == previous {
			continue
		}
		marker := filepath.Join(toolDir, v) + string(os.PathSeparator)
		if slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, marker) }) {
			notes = append(notes, fmt.Sprintf("kept %s %s: a running process uses it", id, v))
			continue
		}
		if err := os.RemoveAll(filepath.Join(toolDir, v)); err != nil {
			notes = append(notes, fmt.Sprintf("could not remove %s %s: %v; check permissions at %s before the next update or rollback", id, v, err, filepath.Join(toolDir, v)))
			continue
		}
		notes = append(notes, fmt.Sprintf("removed %s %s", id, v))
	}
	return notes
}

// familyProcesses lists process-table lines that run the tool: its package file from any install,
// or (when the runtime keeps the invoked path) the PATH link or the `current` link, or, for the
// person's own home, the bare command name.
func (e *Engine) familyProcesses(ctx context.Context, t FamilyTool) []string {
	out, err := e.processes(ctx)
	if err != nil {
		return nil
	}
	markers := []string{"/node_modules/" + t.Package + "/", e.familyLink(t), filepath.Join(e.FamilyRoot(), t.ID, "current") + string(os.PathSeparator)}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		match := len(fields) > 1 && fields[1] == t.Binary && !e.opts.ExplicitHome
		for _, m := range markers {
			match = match || strings.Contains(line, m)
		}
		if match {
			lines = append(lines, line)
		}
	}
	return lines
}

func pidsOf(lines []string) []int {
	var pids []int
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) == 0 {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil && pid != os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids
}

// FamilyStatus reports every family tool's current and previous versions and installed directories.
func (e *Engine) FamilyStatus() []FamilyResult {
	var out []FamilyResult
	for _, t := range FamilyTools() {
		toolDir := filepath.Join(e.FamilyRoot(), t.ID)
		r := FamilyResult{ID: t.ID, Current: readVersionLink(filepath.Join(toolDir, "current")), Previous: readVersionLink(filepath.Join(toolDir, "previous")), Versions: familyVersions(toolDir)}
		switch {
		case t.External != "":
			r.Status = "external"
			r.Notes = []string{t.External}
		case t.Package == "":
			r.Status = "unpublished"
		case r.Current == "":
			r.Status = "not-installed"
		default:
			r.Status = "active"
			if got := readLinkAbs(e.familyLink(t)); got != filepath.Join(toolDir, "current", "bin", t.Binary) {
				r.Notes = []string{e.familyLink(t) + " does not point at current"}
			}
		}
		out = append(out, r)
	}
	return out
}

// familyVersions lists the installed version directories, skipping links and staging dirs.
func familyVersions(toolDir string) []string {
	entries, _ := os.ReadDir(toolDir)
	var out []string
	for _, en := range entries {
		if en.IsDir() && stableVersion.MatchString(en.Name()) {
			out = append(out, en.Name())
		}
	}
	return out
}

func readVersionLink(link string) string {
	v, err := os.Readlink(link)
	if err != nil || !stableVersion.MatchString(v) {
		return ""
	}
	return v
}

// readLinkAbs returns link's target as an absolute path, resolving a relative target against the
// link's directory, so ../share/dotfiles/tools/<id>/current/bin/<bin> matches its absolute form.
func readLinkAbs(link string) string {
	got, err := os.Readlink(link)
	if err != nil || got == "" {
		return got
	}
	if !filepath.IsAbs(got) {
		got = filepath.Join(filepath.Dir(link), got)
	}
	return filepath.Clean(got)
}

// swapLink points link at target with one rename, so a reader sees the old or the new link, never none.
func swapLink(link, target string) error {
	tmp := fmt.Sprintf("%s.tmp-%d", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SwapInstallLink activates a staged install link by the same atomic rename used
// for family current/previous links. The caller must hold its maintenance slot.
func SwapInstallLink(link, target string) error { return swapLink(link, target) }
