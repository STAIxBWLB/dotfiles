package aisettings

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/claudecfg"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

func newHUDManagerForTest(t *testing.T) *HUDManager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return &HUDManager{Runner: dotexec.NewRunner(false, logger), HomeDir: t.TempDir()}
}

func writeHUDFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedDotCorrectSettings writes the settings document dot itself would write,
// so the settings half is already in sync and Mutate must hash-skip it.
func seedDotCorrectSettings(t *testing.T, m *HUDManager) {
	t.Helper()
	data, refused, err := m.mergedClaudeSettings(false)
	if err != nil {
		t.Fatal(err)
	}
	if refused != "" {
		t.Fatalf("seeding must not hit the refusal branch: %s", refused)
	}
	writeHUDFile(t, claudecfg.SettingsPath(m.homeDir()), string(data))
}

// TestApplyClaudeHUD_ReportsThePostLockOutcome is the row that discriminates.
//
// The settings half is already dot-correct and the script half is stale, so
// EnsureFile writes the script and reports true while claudecfg.Mutate
// hash-skips and reports false. It needs no concurrency: it is simply the
// case where the two writes disagree, and it goes red the moment either
// result is discarded and the item is built from the pre-lock read instead.
//
// It is also Defect A's (STATE.md WR-02) acceptance. This is precisely the
// script-landed / settings-skipped shape, so an item naming only the half
// that landed is what proves the report tracks a partial application rather
// than predicting a complete one.
func TestApplyClaudeHUD_ReportsThePostLockOutcome(t *testing.T) {
	m := newHUDManagerForTest(t)
	seedDotCorrectSettings(t, m)
	settingsPath := claudecfg.SettingsPath(m.homeDir())
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	writeHUDFile(t, m.claudeScriptPath(), "# stale statusline from an older dot\n")

	item, err := m.applyClaudeHUD(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !item.Changed || item.Drift != "out-of-sync" {
		t.Fatalf("item = %+v, want Changed=true Drift=out-of-sync", item)
	}
	if item.Detail != "write statusline-dot.py" {
		t.Fatalf("item.Detail = %q, want only the half that landed (%q)", item.Detail, "write statusline-dot.py")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the settings half was rewritten:\nbefore %q\nafter  %q", before, after)
	}
	script, err := os.ReadFile(m.claudeScriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(script) != claudeHUDScript {
		t.Fatal("the script half should have been rewritten")
	}
}

// TestApplyClaudeHUD_FreshHomeReportsBothHalves is the non-vacuity row: when
// both writes really do land, the report is what it has always been.
func TestApplyClaudeHUD_FreshHomeReportsBothHalves(t *testing.T) {
	m := newHUDManagerForTest(t)

	item, err := m.applyClaudeHUD(false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := HUDItem{
		ToolID:     "claude",
		TargetPath: claudecfg.SettingsPath(m.homeDir()),
		Changed:    true,
		Drift:      "out-of-sync",
		Detail:     "write statusLine and statusline-dot.py",
	}
	if item != want {
		t.Fatalf("item = %+v, want %+v", item, want)
	}
}

// TestApplyClaudeHUD_InSyncHomeReportsUnchanged: a home already carrying both
// halves reports in-sync with no detail, byte-identical to before this change.
func TestApplyClaudeHUD_InSyncHomeReportsUnchanged(t *testing.T) {
	m := newHUDManagerForTest(t)
	seedDotCorrectSettings(t, m)
	writeHUDFile(t, m.claudeScriptPath(), claudeHUDScript)

	item, err := m.applyClaudeHUD(false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := HUDItem{
		ToolID:     "claude",
		TargetPath: claudecfg.SettingsPath(m.homeDir()),
		Changed:    false,
		Drift:      "in-sync",
	}
	if item != want {
		t.Fatalf("item = %+v, want %+v", item, want)
	}
}

// TestApplyClaudeHUD_ForeignStatusLineReportsAConflict covers the refusal
// shape shared by both refusal branches.
//
// The pre-lock branch is the one reachable in-process: mergedClaudeSettings
// runs the same applyDotStatusLine check on the same file before the lock, so
// a foreign entry planted here can never reach the closure. The under-lock
// branch needs a foreign writer landing between that read and the lock
// acquire, which is not reproducible without adding a production seam. It is
// covered structurally instead: both branches call one constructor, and this
// row pins what that constructor produces.
func TestApplyClaudeHUD_ForeignStatusLineReportsAConflict(t *testing.T) {
	m := newHUDManagerForTest(t)
	settingsPath := claudecfg.SettingsPath(m.homeDir())
	writeHUDFile(t, settingsPath, `{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/gsd-statusline.js"
  }
}
`)

	item, err := m.applyClaudeHUD(false, false)
	if err != nil {
		t.Fatal(err)
	}
	reason := foreignStatusLineReason("~/.claude/gsd-statusline.js")
	if want := claudeConflictItem(settingsPath, reason); item != want {
		t.Fatalf("item = %+v, want %+v", item, want)
	}
	if item.Changed || item.Drift != "out-of-sync" || item.Detail == "" {
		t.Fatalf("a refusal must render as a conflict, not a write: %+v", item)
	}
	// The refusal wrote nothing: no statusline script, no lock left behind.
	if _, err := os.Stat(m.claudeScriptPath()); !os.IsNotExist(err) {
		t.Fatalf("a refused apply wrote the script: %v", err)
	}
}

// TestClaudeConflictItem_ShapeIsShared pins the constructor both refusal
// branches call, so "the two refusals report identically" is a property of
// the code rather than a convention two literals happen to agree on.
func TestClaudeConflictItem_ShapeIsShared(t *testing.T) {
	const reason = "statusLine owned by another tool (other.js); rerun with --force to take it over"
	got := claudeConflictItem("/home/u/.claude/settings.json", reason)
	want := HUDItem{
		ToolID:     "claude",
		TargetPath: "/home/u/.claude/settings.json",
		Changed:    false,
		Drift:      "out-of-sync",
		Detail:     reason,
	}
	if got != want {
		t.Fatalf("claudeConflictItem = %+v, want %+v", got, want)
	}
}

// TestApplyClaudeHUD_ReformatsAndSaysSo is the row that discriminates the
// SETTINGS half specifically, and it needs no race to do it.
//
// The two comparisons are not the same predicate: the pre-lock prediction
// uses jsonBytesEqual, which normalizes, while claudecfg.Mutate persists
// through a byte-exact hash comparison. A settings.json another tool left in
// compact form is therefore semantically in sync and byte-stale at once, so
// the prediction says "script only" while the write really does rewrite both
// halves. Reporting the prediction here under-reports a file dot rewrote in
// a third party's tree.
//
// The SCRIPT half has no equivalent row on purpose: scriptChanged and
// EnsureFile's own comparison are the same byte comparison over the same
// bytes, so outside a genuine race they cannot disagree, and manufacturing a
// disagreement would mean adding a production seam.
func TestApplyClaudeHUD_ReformatsAndSaysSo(t *testing.T) {
	m := newHUDManagerForTest(t)
	settingsPath := claudecfg.SettingsPath(m.homeDir())
	writeHUDFile(t, settingsPath,
		`{"statusLine":{"type":"command","command":"~/.claude/statusline-dot.py # dot-hud","refreshInterval":5}}`)
	writeHUDFile(t, m.claudeScriptPath(), "# stale statusline from an older dot\n")

	item, err := m.applyClaudeHUD(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !item.Changed || item.Drift != "out-of-sync" {
		t.Fatalf("item = %+v, want Changed=true Drift=out-of-sync", item)
	}
	if item.Detail != "write statusLine and statusline-dot.py" {
		t.Fatalf("item.Detail = %q, want both halves named: the settings file really was rewritten", item.Detail)
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 || string(after)[0:2] != "{\n" {
		t.Fatalf("the settings file should have been reindented, got %q", after)
	}
}
