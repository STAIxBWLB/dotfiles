package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aischedule"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/spf13/cobra"
)

// GUARD-04: every `--json` surface is captured byte-for-byte under
// internal/cli/testdata/golden/, so a field that disappears or changes shape
// during the Phase 3 decomposition shows up as a diff in review instead of
// breaking a downstream consumer that still compiles.
//
// Regenerate after an intentional output change:
//
//	go test ./internal/cli -run 'TestJSONGolden' -update
//
// The -update flag is new machinery: this repo had no golden files and no
// TestMain before this test.
//
// What normalizeGolden erases is exactly what the goldens no longer protect.
// Currently two tokens, both unavoidable:
//
//	@HOME@    the fixture's temp HOME prefix
//	@ROOT@    the fixture's temp workspace-root prefix
//	@MACHINE@ the whole machineNames array, collapsed to one element
//
// @MACHINE@ is a concession, not a preference. syncer.MachineNames() reads
// os.Hostname() and, on darwin, shells out to scutil, so both its values and
// its element count are host- and OS-specific with no config or env seam to
// seed. Only the field's presence and position stay asserted. The `owner`
// field next to it IS seeded (to golden-machine) and stays fully protected,
// as does canPush, which is derived from the two.

var goldenUpdate = flag.Bool("update", false, "rewrite internal/cli/testdata/golden/*.json from live output")

const goldenDir = "testdata/golden"

// goldenMachineOwner is the seeded sync owner. It is a real state-file value,
// not a post-hoc substitution, so the owner-derived fields (canPush) stay real.
const goldenMachineOwner = "golden-machine"

// jsonCommandSurfaces walks the live cobra tree and returns the space-joined
// path of every command carrying a --json flag, without the `dot` root token.
//
// This is the matrix source, and it is a walk rather than a grep on purpose:
// addSkillScanFlags registers --json once and is called from two commands, so
// counting registration sites finds 12 surfaces and silently omits
// `dot ai skills list`. The walk finds 13.
func jsonCommandSurfaces(t *testing.T) []string {
	t.Helper()
	var surfaces []string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		if len(path) > 0 && c.Flags().Lookup("json") != nil {
			surfaces = append(surfaces, strings.Join(path, " "))
		}
		for _, sub := range c.Commands() {
			walk(sub, append(append([]string{}, path...), sub.Name()))
		}
	}
	walk(NewRootCmd("dev", "test"), nil)
	sort.Strings(surfaces)
	return surfaces
}

// goldenCase is one --json surface: which command to run, with what stdin and
// which sandbox, and which golden file holds the expected document.
type goldenCase struct {
	surface string // space-joined command path, matching jsonCommandSurfaces
	variant string // distinguishes multiple cases on one surface (admit deferred)
	args    []string
	stdin   string
	// wantExit expects an *ExitCodeError with this code instead of a nil
	// error (the admission defer contract: JSON on stdout, exit 75).
	wantExit int
	// useErr reads the document from stderr instead of stdout (the
	// admission completion record: child stdout stays the payload).
	useErr  bool
	fixture func(t *testing.T) (home, root string)
}

func (tc goldenCase) goldenPath() string {
	name := strings.ReplaceAll(tc.surface, " ", "-")
	if tc.variant != "" {
		name += "-" + tc.variant
	}
	return filepath.Join(goldenDir, name+".json")
}

// goldenCases covers every --json surface the cobra walk finds; the matrix
// completeness test below is what keeps this list exhaustive. Loop variable
// is tc, per the repo's table-driven convention
// (internal/config/detector_test.go:26).
func goldenCases() []goldenCase {
	return []goldenCase{
		{surface: "admit", args: []string{"admit", "--json", "--wait", "0", "--", "true"}, useErr: true, fixture: goldenAdmitFixture},
		{surface: "admit", variant: "deferred", args: []string{"admit", "--json", "--wait", "0", "--", "true"}, wantExit: ExitDeferred, fixture: goldenAdmitDeferFixture},
		{surface: "admit status", args: []string{"admit", "status", "--json"}, fixture: goldenAdmitStatusFixture},
		{surface: "ai policy inspect", args: []string{"ai", "policy", "inspect", "--json"}, fixture: goldenAIPolicyFixture},
		{surface: "ai policy resolve", args: []string{"ai", "policy", "resolve", "--task", "status", "--project", "@ROOT@", "--json"}, fixture: goldenAIPolicyFixture},
		{surface: "ai policy diff", args: []string{"ai", "policy", "diff", "--json"}, fixture: goldenAIPolicyFixture},
		{surface: "ai policy status", args: []string{"ai", "policy", "status", "--json"}, fixture: goldenAIPolicyFixture},
		{surface: "ai policy apply", args: []string{"ai", "policy", "apply", "--json", "--dry-run"}, fixture: goldenAIPolicyFixture},
		{surface: "ai policy rollback", args: []string{"ai", "policy", "rollback", "--json", "--dry-run"}, fixture: goldenAIPolicyFixture},
		{surface: "ai handoff show", args: []string{"ai", "handoff", "show", "--json"}, fixture: goldenAIHandoffFixture},
		{surface: "ai tools list", args: []string{"ai", "tools", "list", "--json"}, fixture: goldenAIFixture},
		{surface: "ai tools apply", args: []string{"ai", "tools", "apply", "--json", "--dry-run"}, fixture: goldenAIEmptySelectionFixture},
		{surface: "ai tools status", args: []string{"ai", "tools", "status", "--json"}, fixture: goldenAIEmptySelectionFixture},
		{surface: "ai update schedule enable", args: []string{"ai", "update", "schedule", "enable", "--json", "--dry-run"}, fixture: goldenAIScheduleFixture},
		{surface: "ai update schedule disable", args: []string{"ai", "update", "schedule", "disable", "--json", "--dry-run"}, fixture: goldenAIScheduleFixture},
		{surface: "ai update schedule status", args: []string{"ai", "update", "schedule", "status", "--json", "--dry-run"}, fixture: goldenAIScheduleFixture},
		{surface: "ai auth status", args: []string{"ai", "auth", "status", "--json"}, fixture: goldenAIFixture},
		{surface: "ai skills list", args: []string{"ai", "skills", "list", "--json"}, fixture: goldenAIFixture},
		{surface: "ai skills status", args: []string{"ai", "skills", "status", "--json"}, fixture: goldenAIFixture},
		{surface: "ai skills validate", args: []string{"ai", "skills", "validate", "--json"}, fixture: goldenAIFixture},
		{surface: "ai update", args: []string{"ai", "update", "--json", "--dry-run", "--tool", "ponytail"}, fixture: goldenAIUpdateFixture},
		{surface: "peer home-paths get", args: []string{"peer", "home-paths", "get", "--json"}, fixture: goldenSyncFixture},
		{surface: "peer home-paths set", args: []string{"peer", "home-paths", "set", "--json"}, stdin: "peer-host-a\npeer-host-b\n", fixture: goldenSyncFixture},
		{surface: "peer home-paths tracked get", args: []string{"peer", "home-paths", "tracked", "get", "--json"}, fixture: goldenSyncFixture},
		{surface: "peer home-paths tracked set", args: []string{"peer", "home-paths", "tracked", "set", "--json"}, stdin: ".claude/projects/-x-y/memory\n", fixture: goldenSyncFixture},
		{surface: "peer status", args: []string{"peer", "status", "--json"}, fixture: goldenSyncFixture},
		{surface: "peer git status", args: []string{"peer", "git", "status", "--json"}, fixture: goldenPeerGitFixture},
		{surface: "peer diff", args: []string{"peer", "diff", "--json"}, fixture: goldenPeerPlanFixture},
		{surface: "peer sync", args: []string{"peer", "sync", "--json"}, fixture: goldenPeerPlanFixture},
		{surface: "sync configure", args: []string{"sync", "configure", "--json", "--yes"}, fixture: goldenSyncFixture},
		{surface: "sync filters get", args: []string{"sync", "filters", "get", "include", "--json"}, fixture: goldenSyncFixture},
		{surface: "sync filters set", args: []string{"sync", "filters", "set", "exclude", "--json"}, stdin: "*.tmp\nbuild/\n", fixture: goldenSyncFixture},
		{surface: "sync log", args: []string{"sync", "log", "--json"}, fixture: goldenSyncFixture},
		{surface: "sync status", args: []string{"sync", "status", "--json"}, fixture: goldenSyncFixture},
	}
}

// goldenSyncFixture builds on newSyncCLIFixture (sync_cli_test.go:22) and adds
// the two store configs the sync and peer profiles read, with owner seeded to a
// fixed value so the owner-derived fields are deterministic by construction
// rather than by post-processing.
func goldenSyncFixture(t *testing.T) (home, root string) {
	t.Helper()
	f := newSyncCLIFixture(t)
	sandboxGoldenPATH(t)
	storeCfg := "target: local:" + f.mirror + "\nowner: " + goldenMachineOwner + "\npropagation:\n  create: true\n  update: true\n  delete: false\ninterval: 600\n"
	for _, profile := range []string{"sync", "peer"} {
		writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", profile, "config.yaml"), storeCfg)
	}
	return f.home, f.local
}

// goldenPeerPlanFixture gives `peer diff --json` and `peer sync --json` an
// ssh peer that cannot be reached (PATH holds no ssh), so both print the
// deterministic unreachable plan document. The owner is this machine so the
// sync gets past its owner guard.
func goldenPeerPlanFixture(t *testing.T) (home, root string) {
	t.Helper()
	f := newSyncCLIFixture(t)
	sandboxGoldenPATH(t)
	storeCfg := "target: ssh:golden-peer:/remote/work\nowner: " + syncer.PreferredMachineName() + "\npropagation:\n  create: true\n  update: true\n  delete: true\n"
	writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "peer", "config.yaml"), storeCfg)
	return f.home, f.local
}

// goldenPeerGitFixture gives `peer git status` a deterministic non-git
// workspace: a git that always fails makes the root report "not a git
// repository", and PATH lives under HOME so the resolved absolute git path
// normalizes to @HOME@ like every other path in the document.
func goldenPeerGitFixture(t *testing.T) (home, root string) {
	t.Helper()
	home, root = goldenSyncFixture(t)
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return home, root
}

// goldenAIFixture reuses newOnestopFixture (onestop_cli_test.go:20), which
// already seeds the AI tool settings the `dot ai` surfaces read.
// goldenAIFixture reuses newOnestopFixture (onestop_cli_test.go:20) and adds
// one MCP server, one pending-auth entry, and one valid skill.
//
// The seeding is not decoration. An unseeded sandbox makes `ai auth status`
// emit "servers": [] and `ai skills list` emit "items": null, and an empty
// array pins nothing about the shape of its elements - which is the exact
// drift GUARD-04 exists to catch. One populated element per collection puts
// every per-item field under the golden.
func goldenAIFixture(t *testing.T) (home, root string) {
	t.Helper()
	f := newOnestopFixture(t)
	sandboxGoldenPATH(t)
	writeCLITestFile(t, filepath.Join(f.home, ".claude.json"),
		`{"mcpServers":{"golden-http":{"type":"http","url":"https://mcp.golden.invalid/sse"}}}`)
	writeCLITestFile(t, filepath.Join(f.home, ".claude", "mcp-needs-auth-cache.json"),
		`{"golden-connector":{}}`)
	skill := "---\nname: golden-skill\ndescription: A seeded skill so the scan report has one real item.\nschema_version: v1\n---\n\nBody.\n"
	writeCLITestFile(t, filepath.Join(f.home, ".claude", "skills", "golden-skill", "SKILL.md"), skill)
	writeCLITestFile(t, filepath.Join(f.home, ".maru", "skills", "golden-skill", "SKILL.md"), skill)
	return f.home, f.root
}

func goldenAIUpdateFixture(t *testing.T) (home, root string) {
	home, root = goldenAIFixture(t)
	state, err := config.LoadStateForHome(home)
	if err != nil {
		t.Fatal(err)
	}
	state.Modules.AI.Tooling = &config.AIToolingConfig{Agents: []string{"grok"}, Tools: []string{"ponytail"}}
	if err = config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	return home, root
}

// sandboxGoldenPATH empties PATH so every CommandExists probe answers the same
// on a developer laptop and on a CI runner.
//
// This is not belt-and-braces: `dot ai update --json` branches on
// CommandExists(claude), CommandExists(codex) and six more, and `dot sync
// status --json` emits rsyncVersion only when rsync is reachable. Without this,
// the captured documents differ in SHAPE, not just in values, between a machine
// that has those tools and one that does not - and no normalizer can repair
// that. The goldens therefore record the tool-absent shape, which is the one
// shape both environments can reproduce.
func sandboxGoldenPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// TestJSONGoldenMatrixIsComplete is the fail-closed half of GUARD-04: the
// invocation table below carries judgement (which argv and which sandbox make a
// surface produce a real document) and this test carries the completeness the
// table's author will forget. A new command that grows a --json flag has no
// golden until someone adds one here, and that is what turns red.
func TestJSONGoldenMatrixIsComplete(t *testing.T) {
	surfaces := jsonCommandSurfaces(t)
	if len(surfaces) == 0 {
		t.Fatal("the cobra walk found no --json surfaces; the guard would pass vacuously")
	}

	covered := map[string]bool{}
	for _, tc := range goldenCases() {
		covered[tc.surface] = true
	}

	for _, surface := range surfaces {
		if !covered[surface] {
			t.Errorf("command %q carries --json but has no entry in goldenCases()", surface)
			continue
		}
	}

	live := map[string]bool{}
	for _, surface := range surfaces {
		live[surface] = true
	}
	for _, tc := range goldenCases() {
		if !live[tc.surface] {
			t.Errorf("goldenCases() lists %q, which is no longer a command carrying --json", tc.surface)
			continue
		}
		if _, err := os.Stat(tc.goldenPath()); err != nil {
			t.Errorf("surface %q has no golden at %s: run `go test ./internal/cli -run TestJSONGolden -update`", tc.surface, tc.goldenPath())
		}
	}
}

// TestJSONGoldens asserts every --json document byte-for-byte. A field-by-field
// assertion (sync_json_test.go:12) passes when a field is dropped; a whole-
// document comparison does not, which is the entire point before Phase 3 moves
// 16,387 lines out of this package.
//
// Runs serially, per the repo convention: every fixture sets HOME, PATH and
// XDG_* via t.Setenv, which a parallel test may not do.
func TestJSONGoldens(t *testing.T) {
	for _, tc := range goldenCases() {
		name := tc.surface
		if tc.variant != "" {
			name += "/" + tc.variant
		}
		t.Run(name, func(t *testing.T) {
			// Resolve the golden path BEFORE the fixture runs: admission
			// fixtures chdir into their sandbox repo, and a relative golden
			// path would then read/write inside the sandbox instead of
			// testdata/golden.
			path, err := filepath.Abs(tc.goldenPath())
			if err != nil {
				t.Fatal(err)
			}
			home, root := tc.fixture(t)
			// Fixture directory arguments keep runtime policy inspection out of
			// the developer's project settings without changing the process cwd.
			tc.args = append([]string{}, tc.args...)
			for i, arg := range tc.args {
				if arg == "@ROOT@" {
					tc.args[i] = root
				}
			}
			out, errOut, err := runGoldenSurface(tc)
			if tc.wantExit != 0 {
				var exitErr *ExitCodeError
				if !errors.As(err, &exitErr) || exitErr.Code != tc.wantExit {
					t.Fatalf("%s: err = %v, want exit code %d\nstderr=%s", strings.Join(tc.args, " "), err, tc.wantExit, errOut)
				}
			} else if err != nil {
				t.Fatalf("%s: %v\nstderr=%s", strings.Join(tc.args, " "), err, errOut)
			}
			raw := out
			if tc.useErr {
				raw = errOut
			}
			got := normalizeGolden(raw, home, root)

			if *goldenUpdate {
				assertNonDegenerateGolden(t, tc.surface, got)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading golden: %v (run with -update to capture)", err)
			}
			wantText := string(want)
			if tc.surface == "peer status" && runtime.GOOS != "darwin" {
				wantText = strings.Replace(
					wantText,
					`"state": "not installed"`,
					`"state": "`+peerSchedulerUnsupportedState+`"`,
					1,
				)
			}
			if got != wantText {
				t.Errorf("%s --json drifted from its golden %s\ngot:\n%s\nwant:\n%s", tc.surface, path, got, wantText)
			}
		})
	}
}

// runGoldenSurface invokes the real cobra tree in-process. Surfaces that do not
// read stdin go through runDotForTest (ai_agentos_cmd_test.go:289) unchanged;
// the two `set` surfaces read cmd.InOrStdin() and need it bound, or the test
// process would read its own stdin and could block.
func runGoldenSurface(tc goldenCase) (string, string, error) {
	if tc.stdin == "" {
		return runDotForTest(tc.args...)
	}
	root := NewRootCmd("dev", "test")
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(tc.stdin))
	root.SetArgs(tc.args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

// normalizeGolden replaces only values that cannot be made deterministic by
// seeding a fixture. It does NOT touch schema versions, booleans, counts, or
// any structural field: those stay under the goldens' protection. Every token
// added here is a field the goldens stop guarding, so add one only after
// establishing there is no seam to seed instead.
func normalizeGolden(raw, home, root string) string {
	// Longest prefix first, so a root nested under home is not half-substituted.
	prefixes := []struct{ path, token string }{
		{home, "@HOME@"},
		{root, "@ROOT@"},
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i].path) > len(prefixes[j].path) })
	for _, p := range prefixes {
		if p.path != "" {
			raw = strings.ReplaceAll(raw, p.path, p.token)
		}
	}
	return normalizeMachineNames(raw)
}

// normalizeMachineNames collapses every machineNames array to the single
// element @MACHINE@. syncer.MachineNames() derives its values from
// os.Hostname() and, on darwin, from scutil --get LocalHostName/ComputerName -
// so both the values AND the element count are host- and OS-specific (3 on a
// configured Mac, 1 on a Linux runner) with no config or env seam to seed.
// Presence and position of the field stay asserted; contents and length do not.
func normalizeMachineNames(raw string) string {
	const open = "\"machineNames\": ["
	var b strings.Builder
	for {
		start := strings.Index(raw, open)
		if start < 0 {
			break
		}
		end := strings.Index(raw[start:], "]")
		if end < 0 {
			break
		}
		b.WriteString(raw[:start+len(open)])
		b.WriteString("\"@MACHINE@\"")
		raw = raw[start+end:]
	}
	b.WriteString(raw)
	return b.String()
}

// assertNonDegenerateGolden stops a degenerate capture from becoming the
// baseline. T-01-11's mitigation was recorded as shipped but only its human
// halves existed: -update wrote whatever the surface produced.
//
// The failure it closes runs opposite to the drift check. Overwriting a golden
// with `{}` already fails, because live output disagrees with it. The unguarded
// direction is a surface that STARTS emitting a degenerate document -- an empty
// object, a bare null, an error string -- at the moment somebody runs -update.
// That capture becomes the reference, and every later run compares degenerate
// against degenerate and passes. The guard then reports green while protecting
// nothing, which is T-01-11 itself and this phase's central prohibition.
//
// Deliberately narrow: this asserts a golden is a JSON document carrying at
// least one key, not that its contents are correct. Judging correctness is what
// review before commit is for.
func assertNonDegenerateGolden(t *testing.T, surface, got string) {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("refusing to capture a golden for %q: output is not valid JSON: %v\ngot:\n%s",
			surface, err, got)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("refusing to capture a golden for %q: output is %T, not a JSON object\ngot:\n%s",
			surface, doc, got)
	}
	if len(obj) == 0 {
		t.Fatalf("refusing to capture a golden for %q: output is an empty JSON object, which would pin nothing\ngot:\n%s",
			surface, got)
	}
}

// TestAssertNonDegenerateGolden pins the guard that stops a degenerate capture
// from becoming a baseline (T-01-11).
//
// The register recorded that guard as shipped when only its human halves
// existed. It now exists, but the three injections that proved it were run by
// hand -- and a one-time proof does not survive a refactor, which is the same
// mistake T-01-11 was. This is the standing version.
//
// The helper calls t.Fatalf, so each case runs in a subtest whose failure is
// the expected outcome; the assertion is on WHICH cases fail, not on the call
// returning.
func TestAssertNonDegenerateGolden(t *testing.T) {
	for _, tc := range []struct {
		name      string
		got       string
		wantFatal bool
	}{
		{name: "empty object pins nothing", got: "{}\n", wantFatal: true},
		{name: "bare null", got: "null\n", wantFatal: true},
		{name: "not JSON at all", got: "error: store unavailable\n", wantFatal: true},
		{name: "empty output", got: "", wantFatal: true},
		{name: "JSON array is not an object", got: "[]\n", wantFatal: true},
		{name: "one key is enough", got: `{"conflictCount":0}` + "\n", wantFatal: false},
		{name: "a real document", got: `{"owner":"golden-machine","canPush":true}` + "\n", wantFatal: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotFatal := didFatal(func(sub *testing.T) {
				assertNonDegenerateGolden(sub, "test surface", tc.got)
			})
			if gotFatal != tc.wantFatal {
				t.Errorf("assertNonDegenerateGolden(%q): fatal=%v, want fatal=%v",
					tc.got, gotFatal, tc.wantFatal)
			}
		})
	}
}

// didFatal runs fn on a throwaway *testing.T and reports whether it failed.
// t.Fatalf unwinds via runtime.Goexit, so fn runs on its own goroutine and the
// helper waits for it to finish either way.
func didFatal(fn func(*testing.T)) bool {
	var inner *testing.T
	done := make(chan struct{})
	t2 := &testing.T{}
	go func() {
		defer close(done)
		fn(t2)
	}()
	<-done
	inner = t2
	return inner.Failed()
}

func goldenAIEmptySelectionFixture(t *testing.T) (home, root string) {
	home, root = goldenAIFixture(t)
	state, err := config.LoadStateForHome(home)
	if err != nil {
		t.Fatal(err)
	}
	state.Modules.AI.Tooling = &config.AIToolingConfig{Agents: []string{}}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	return home, root
}
func goldenAIPinnedSelectionFixture(t *testing.T) (home, root string) {
	home, root = goldenAIEmptySelectionFixture(t)
	state, err := config.LoadStateForHome(home)
	if err != nil {
		t.Fatal(err)
	}
	state.Modules.AI.Tooling = &config.AIToolingConfig{Agents: []string{"grok"}, Pins: map[string]string{"grok": "1.0.0"}}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	return home, root
}
func goldenAIScheduleFixture(t *testing.T) (home, root string) {
	home, root = goldenAIPinnedSelectionFixture(t)
	original := newAIScheduleManager
	t.Cleanup(func() { newAIScheduleManager = original })
	newAIScheduleManager = func(home string, dry bool) *aischedule.Manager {
		manager := aischedule.New(home, dry)
		manager.GOOS = "darwin"
		manager.CurrentHome = home
		manager.Executable = "/usr/local/bin/dot"
		manager.Environment = map[string]string{"HOME": home, "PATH": "/usr/bin:/bin"}
		return manager
	}
	return home, root
}
func goldenAIHandoffFixture(t *testing.T) (home, root string) {
	home, root = goldenAIFixture(t)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonical
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\ncase \"$*\" in\n*--show-toplevel*) printf '%s\\n' " + quote(root) + ";;\n*--git-common-dir*) printf '%s\\n' " + quote(filepath.Join(root, ".git")) + ";;\n*) printf '%s\\n' 0123456789012345678901234567890123456789;;\nesac\n"
	bin := filepath.Join(os.Getenv("PATH"), "git")
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return home, root
}
