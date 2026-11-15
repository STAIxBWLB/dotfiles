package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// everything written to it. The state loaders warn straight to os.Stderr and
// take no injectable writer, so the pipe is the only way to assert on the
// wording the differential harness compares byte for byte.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

func writeStateFile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSaveState_StampsSchemaVersion pins DEBT-01's whole claim: a state that
// never touched the field is still written with a real version key, and that
// key is first in the file. Without this a file written by v1.0 would be
// indistinguishable from one written before the field existed.
func TestSaveState_StampsSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := saveStateAt(path, &UserState{Name: "Test", Profile: "full"}); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "schema_version:") {
		t.Fatalf("schema_version is not the first key:\n%s", data)
	}
	if strings.Contains(string(data), "schema_version: 0") {
		t.Fatalf("an unstamped save wrote version 0:\n%s", data)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if loaded.SchemaVersion != currentSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", loaded.SchemaVersion, currentSchemaVersion)
	}
}

// TestSaveState_StampsOverAnUnversionedFile proves the stamp is applied to a
// state loaded from a pre-version file, not only to a fresh struct.
func TestSaveState_StampsOverAnUnversionedFile(t *testing.T) {
	path := writeStateFile(t, t.TempDir(), "name: Test\nprofile: full\n")
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if loaded.SchemaVersion != 0 {
		t.Fatalf("unversioned file loaded as version %d, want 0", loaded.SchemaVersion)
	}
	if err := saveStateAt(path, loaded); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), fmt.Sprintf("schema_version: %d\n", currentSchemaVersion)) {
		t.Fatalf("rewrite did not stamp the current version:\n%s", data)
	}
}

// TestLoadState_UnversionedFileIsSilent pins version 0 as a normal state:
// "written before the field existed", not an error and not a warning.
func TestLoadState_UnversionedFileIsSilent(t *testing.T) {
	path := writeStateFile(t, t.TempDir(), "name: Test\nprofile: full\n")
	var loaded *UserState
	var err error
	out := captureStderr(t, func() { loaded, err = loadStateAt(path) })
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if loaded.SchemaVersion != 0 {
		t.Fatalf("SchemaVersion = %d, want 0", loaded.SchemaVersion)
	}
	if out != "" {
		t.Fatalf("unversioned load wrote to stderr: %q", out)
	}
}

// TestLoadState_ForwardVersionWarnsAndStillWorks is DEBT-02's read half on the
// loadStateAt entry point: an additively-forward file (every field this binary
// knows still decodes) warns and returns state the caller can use.
func TestLoadState_ForwardVersionWarnsAndStillWorks(t *testing.T) {
	path := writeStateFile(t, t.TempDir(),
		"schema_version: 99\nname: Test\nprofile: full\nunknown_future_key: whatever\n")
	var loaded *UserState
	var err error
	out := captureStderr(t, func() { loaded, err = loadStateAt(path) })
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if loaded.Name != "Test" || loaded.Profile != "full" {
		t.Fatalf("forward-version state is not usable: %#v", loaded)
	}
	if !strings.Contains(out, "warning:") {
		t.Fatalf("no warning for a forward-version file: %q", out)
	}
	if !strings.Contains(out, "99") || !strings.Contains(out, "dot update") {
		t.Fatalf("warning names neither the version nor the remedy: %q", out)
	}
}

// TestLoadState_IncompatiblyForwardWarnsBeforeTheDecodeError covers the case a
// struct field alone cannot: the full decode FAILS, so the field that would
// carry the version is never populated. The peek still recovers it, and the
// warning must be emitted before the decode error is returned so the user
// learns the file came from a newer dot instead of getting a bare yaml error.
func TestLoadState_IncompatiblyForwardWarnsBeforeTheDecodeError(t *testing.T) {
	// profile: arrives as a mapping where UserState.Profile is a string.
	path := writeStateFile(t, t.TempDir(),
		"schema_version: 99\nname: Test\nprofile:\n  name: full\n  variant: server\n")
	var loaded *UserState
	var err error
	out := captureStderr(t, func() { loaded, err = loadStateAt(path) })
	if err == nil {
		t.Fatalf("expected a decode error, got state %#v", loaded)
	}
	if !strings.HasPrefix(err.Error(), "parsing state: ") {
		t.Fatalf("decode error wording changed: %v", err)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "99") {
		t.Fatalf("no forward-version warning before the decode error: %q", out)
	}
	if !strings.Contains(out, "dot update") {
		t.Fatalf("warning does not point at the remedy: %q", out)
	}
}

// TestLoadStateFrom_ForwardVersionWarnsAndStillWorks is the same claim on the
// entry point D-03's original wording missed. dot init --from, onestop and
// profilesnap all read through here.
func TestLoadStateFrom_ForwardVersionWarnsAndStillWorks(t *testing.T) {
	path := writeStateFile(t, t.TempDir(),
		"schema_version: 99\nname: Test\nprofile: full\n")
	var loaded *UserState
	var err error
	out := captureStderr(t, func() { loaded, err = LoadStateFrom(path) })
	if err != nil {
		t.Fatalf("LoadStateFrom: %v", err)
	}
	if loaded.Name != "Test" || loaded.Profile != "full" {
		t.Fatalf("forward-version import is not usable: %#v", loaded)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "99") || !strings.Contains(out, "dot update") {
		t.Fatalf("LoadStateFrom did not warn about a forward-version file: %q", out)
	}
}

// TestLoadStateFrom_KeepsItsThreeBehaviours pins what the merge into a shared
// read body must not change: a missing file is a hard error here, the two
// error wordings say "config" and not "state", and an import with neither a
// name nor a profile is refused.
func TestLoadStateFrom_KeepsItsThreeBehaviours(t *testing.T) {
	dir := t.TempDir()

	_, err := LoadStateFrom(filepath.Join(dir, "absent.yaml"))
	if err == nil {
		t.Fatal("missing file must be an error for LoadStateFrom")
	}
	if !strings.HasPrefix(err.Error(), "reading config: ") {
		t.Fatalf("read error wording = %q, want prefix %q", err.Error(), "reading config: ")
	}

	broken := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("{{ broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadStateFrom(broken)
	if err == nil {
		t.Fatal("unparseable import must be an error")
	}
	if !strings.HasPrefix(err.Error(), "parsing config: ") {
		t.Fatalf("parse error wording = %q, want prefix %q", err.Error(), "parsing config: ")
	}

	empty := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(empty, []byte("email: nobody@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadStateFrom(empty)
	if err == nil || err.Error() != "imported config is empty (no name or profile set)" {
		t.Fatalf("emptiness assertion changed: %v", err)
	}
}

// TestLoadStateFrom_ValidationWarningWording pins the one-line form. loadStateAt
// prints a second remedy line and LoadStateFrom does not; the differential
// harness compares stderr byte for byte, so the difference is load-bearing.
func TestLoadStateFrom_ValidationWarningWording(t *testing.T) {
	path := writeStateFile(t, t.TempDir(), "name: Test\nprofile: not-a-profile\n")
	out := captureStderr(t, func() { _, _ = LoadStateFrom(path) })
	if !strings.HasPrefix(out, "dot: warning: imported config has invalid values: ") {
		t.Fatalf("import warning wording changed: %q", out)
	}
	if strings.Contains(out, "dot reconfigure") {
		t.Fatalf("LoadStateFrom must not print loadStateAt's remedy line: %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("import warning must be one line, got %q", out)
	}
}

// TestLoadState_ValidationWarningWording pins loadStateAt's two-line form.
func TestLoadState_ValidationWarningWording(t *testing.T) {
	path := writeStateFile(t, t.TempDir(), "name: Test\nprofile: not-a-profile\n")
	out := captureStderr(t, func() { _, _ = loadStateAt(path) })
	if !strings.HasPrefix(out, "dot: warning: state file has invalid values: ") {
		t.Fatalf("state warning wording changed: %q", out)
	}
	if !strings.HasSuffix(out, "dot reconfigure\n") {
		t.Fatalf("state warning lost its remedy line: %q", out)
	}
}

// TestLoadState_MissingFileIsEmptyState pins loadStateAt's own fork, the one
// LoadStateFrom must NOT inherit from the shared body.
func TestLoadState_MissingFileIsEmptyState(t *testing.T) {
	state, err := loadStateAt(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("missing state file must not error: %v", err)
	}
	if state == nil || state.Name != "" || state.Profile != "" || state.SchemaVersion != 0 {
		t.Fatalf("missing state file = %#v, want the zero state", state)
	}
}

// TestPeekSchemaVersion_SwallowsItsOwnErrors: every malformed input peeks as 0
// so the real decode error surfaces instead of a complaint about a one-field
// struct. Measured in 07-RESEARCH.md Q3b.
func TestPeekSchemaVersion_SwallowsItsOwnErrors(t *testing.T) {
	cases := map[string]string{
		"sequence document": "- a\n- b\n",
		"scalar document":   "just a scalar\n",
		"empty document":    "",
		"broken document":   "{{ broken\n",
		"non-integer":       "schema_version: notanint\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := peekSchemaVersion([]byte(doc)); got != 0 {
				t.Fatalf("peek(%q) = %d, want 0", doc, got)
			}
		})
	}
}

// TestPeekSchemaVersion_TopLevelOnly rules out the two implementations a future
// reader might reach for. A line scanner or a regex over the raw bytes gets the
// nested case wrong.
func TestPeekSchemaVersion_TopLevelOnly(t *testing.T) {
	nested := "name: Test\nmodules:\n  schema_version: 42\n"
	if got := peekSchemaVersion([]byte(nested)); got != 0 {
		t.Fatalf("nested schema_version peeked as %d, want 0", got)
	}
	anchored := "defaults: &d\n  name: Test\nschema_version: 5\nmodules:\n  <<: *d\n"
	if got := peekSchemaVersion([]byte(anchored)); got != 5 {
		t.Fatalf("anchored document peeked as %d, want 5", got)
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// TestSaveState_RefusesForwardVersionDestination is DEBT-02's write half: a
// v1.0 binary must not silently overwrite a file a v1.1 binary wrote, because
// yaml.v3 drops the keys it does not know and the loss is silent.
func TestSaveState_RefusesForwardVersionDestination(t *testing.T) {
	path := writeStateFile(t, t.TempDir(),
		"schema_version: 99\nname: Newer\nprofile: full\n")
	before := hashFile(t, path)

	err := saveStateAt(path, &UserState{Name: "Older", Profile: "minimal"})
	if err == nil {
		t.Fatal("save over a newer state file must be refused")
	}
	if after := hashFile(t, path); after != before {
		t.Fatalf("refused save still modified the file:\n%s", mustRead(t, path))
	}

	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("refusal does not name the file path: %q", msg)
	}
	if !strings.Contains(msg, "99") {
		t.Errorf("refusal does not name the on-disk version: %q", msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("%d", currentSchemaVersion)) {
		t.Errorf("refusal does not name this binary's version: %q", msg)
	}
	if !strings.Contains(msg, "dot update") {
		t.Errorf("refusal does not name the remedy: %q", msg)
	}
	if !strings.Contains(msg, "DOT_SCHEMA_FORCE") {
		t.Errorf("refusal does not name the override variable: %q", msg)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSaveState_ForceOverridesTheRefusal pins the one documented escape hatch.
func TestSaveState_ForceOverridesTheRefusal(t *testing.T) {
	path := writeStateFile(t, t.TempDir(),
		"schema_version: 99\nname: Newer\nprofile: full\n")
	t.Setenv("DOT_SCHEMA_FORCE", "1")

	if err := saveStateAt(path, &UserState{Name: "Older", Profile: "minimal"}); err != nil {
		t.Fatalf("DOT_SCHEMA_FORCE=1 did not override the refusal: %v", err)
	}
	data := mustRead(t, path)
	if !strings.HasPrefix(data, fmt.Sprintf("schema_version: %d\n", currentSchemaVersion)) {
		t.Fatalf("forced save did not rewrite with this binary's version:\n%s", data)
	}
	if !strings.Contains(data, "name: Older") {
		t.Fatalf("forced save did not write the new state:\n%s", data)
	}
}

// TestSaveState_ForceRequiresExactlyOne: an escape hatch that any non-empty
// value satisfies is an escape hatch nobody set on purpose.
func TestSaveState_ForceRequiresExactlyOne(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes"} {
		t.Run("value="+value, func(t *testing.T) {
			path := writeStateFile(t, t.TempDir(),
				"schema_version: 99\nname: Newer\nprofile: full\n")
			t.Setenv("DOT_SCHEMA_FORCE", value)
			if err := saveStateAt(path, &UserState{Name: "Older", Profile: "minimal"}); err == nil {
				t.Fatalf("DOT_SCHEMA_FORCE=%q satisfied the override", value)
			}
		})
	}
}

// TestSaveState_FirstWriteToMissingDestinationSucceeds: there is nothing to
// compare against and a first write is not a downgrade.
func TestSaveState_FirstWriteToMissingDestinationSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	if err := saveStateAt(path, &UserState{Name: "Test", Profile: "full"}); err != nil {
		t.Fatalf("first write refused: %v", err)
	}
}

// TestSaveState_EqualAndOlderDestinationsSucceed keeps the guard from firing on
// the ordinary case, which is every write this release performs.
func TestSaveState_EqualAndOlderDestinationsSucceed(t *testing.T) {
	cases := map[string]string{
		"equal version":  fmt.Sprintf("schema_version: %d\nname: Old\nprofile: full\n", currentSchemaVersion),
		"v1 migration":   "schema_version: 1\nname: Old\nprofile: full\n",
		"older version":  "schema_version: 0\nname: Old\nprofile: full\n",
		"no version key": "name: Old\nprofile: full\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeStateFile(t, t.TempDir(), body)
			if err := saveStateAt(path, &UserState{Name: "New", Profile: "full"}); err != nil {
				t.Fatalf("ordinary overwrite refused: %v", err)
			}
			if !strings.Contains(mustRead(t, path), "name: New") {
				t.Fatal("overwrite did not land")
			}
		})
	}
}

// TestSaveState_UnparseableDestinationIsOverwrittenNotRefused: refusing to
// write over a file we cannot read would brick recovery from a corrupt state
// file, so a destination that peeks as 0 is overwritten deliberately.
func TestSaveState_UnparseableDestinationIsOverwrittenNotRefused(t *testing.T) {
	path := writeStateFile(t, t.TempDir(), "{{ this is not yaml\n")
	if err := saveStateAt(path, &UserState{Name: "Recovered", Profile: "full"}); err != nil {
		t.Fatalf("save over corrupt state file refused: %v", err)
	}
	if !strings.Contains(mustRead(t, path), "name: Recovered") {
		t.Fatal("recovery write did not land")
	}
}

// TestLoadState_ExplicitEmptyAppsBeatsLegacyCasks characterizes the one
// semantic the deleted UserTerminalAppsState.UnmarshalYAML did more than
// legacy handling for: it told "apps absent" apart from "apps present and
// empty", so an explicit empty list wins over casks. A plain struct decode
// keeps the distinction (measured: an absent apps decodes to a nil slice, an
// explicit empty one to a non-nil zero-length slice), so the legacy overlay
// must apply casks only when the canonical slice is nil.
func TestLoadState_ExplicitEmptyAppsBeatsLegacyCasks(t *testing.T) {
	path := writeStateFile(t, t.TempDir(),
		"name: Test\nprofile: full\nmodules:\n  terminal_apps:\n    enabled: true\n    apps: []\n    casks: [warp]\n")
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if len(loaded.Modules.TerminalApps.Apps) != 0 {
		t.Fatalf("explicit empty apps lost to casks: %#v", loaded.Modules.TerminalApps.Apps)
	}
}

// TestLoadState_AIShorthandStillAccepted guards the shim that is NOT a legacy
// shim: `ai: true` is a supported input form, not a migration.
func TestLoadState_AIShorthandStillAccepted(t *testing.T) {
	path := writeStateFile(t, t.TempDir(), "name: Test\nprofile: full\nmodules:\n  ai: true\n")
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if !loaded.Modules.AI.Enabled {
		t.Fatal("ai: true shorthand no longer enables the AI module")
	}
}

// TestSaveState_NotesTheLegacyMigration: a file whose keys moved says so once,
// on the write that persists the move.
func TestSaveState_NotesTheLegacyMigration(t *testing.T) {
	path := writeStateFile(t, t.TempDir(), "name: Test\nprofile: full\nmodules:\n  warp: true\n")
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	out := captureStderr(t, func() {
		if err := saveStateAt(path, loaded); err != nil {
			t.Errorf("saveStateAt: %v", err)
		}
	})
	if !strings.HasPrefix(out, "dot: ") {
		t.Fatalf("no migration note on a converted write: %q", out)
	}
	if !strings.Contains(out, "modules.warp") {
		t.Fatalf("note does not name the key that moved: %q", out)
	}
	if strings.Count(out, "dot: ") != 1 {
		t.Fatalf("note fired %d times, want once: %q", strings.Count(out, "dot: "), out)
	}
}

// TestSaveState_SilentOnACanonicalFile is the other half: a file with nothing
// to migrate must not print a note anybody would have to learn to ignore.
func TestSaveState_SilentOnACanonicalFile(t *testing.T) {
	path := writeStateFile(t, t.TempDir(),
		"schema_version: 1\nname: Test\nprofile: full\nmodules:\n  terminal_apps:\n    enabled: true\n    apps: [warp]\n")
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	out := captureStderr(t, func() {
		if err := saveStateAt(path, loaded); err != nil {
			t.Errorf("saveStateAt: %v", err)
		}
	})
	if out != "" {
		t.Fatalf("canonical write wrote to stderr: %q", out)
	}
}
