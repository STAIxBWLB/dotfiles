package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/spf13/cobra"
)

func TestBuildSyncStatusJSONReportsStableSchemaAndJobs(t *testing.T) {
	root := t.TempDir()
	paths := syncer.ResolveLocalPaths(root)
	if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("propagation:\n  create: true\n  update: true\n  delete: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lastPush := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	cfg := &syncer.Config{
		Profile:     syncer.DefaultProfile,
		LocalPath:   root + "/",
		Target:      syncer.Target{Kind: syncer.TargetLocal, Path: t.TempDir()},
		LocalPaths:  paths,
		Propagation: syncer.DefaultPropagationPolicy(),
		Interval:    600,
		PushMode:    syncer.ModeClean,
		PullMode:    syncer.ModeClean,
	}
	status := &syncer.Status{
		Profile:        syncer.DefaultProfile,
		LocalPath:      root,
		StoreDir:       paths.StoreDir,
		Target:         cfg.Target,
		LocalExists:    true,
		MirrorExists:   true,
		Propagation:    cfg.Propagation,
		Interval:       600,
		PushMode:       syncer.ModeClean,
		PullMode:       syncer.ModeClean,
		SchedulerState: syncer.SchedulerRunning,
		LastPush:       lastPush,
		RsyncPath:      "/opt/homebrew/bin/rsync",
		RsyncVersion:   "rsync version 3.4.4",
	}
	scheduler := &syncer.Scheduler{Paths: &syncer.Paths{
		LaunchdPlist: filepath.Join(root, "com.dotfiles.sync.plist"),
	}}
	document := buildSyncStatusJSON(cfg, status, scheduler)
	if document.RsyncPath != status.RsyncPath || document.RsyncVersion != status.RsyncVersion {
		t.Fatalf("selected rsync missing from JSON: %+v", document)
	}
	if document.SchemaVersion != syncStatusSchemaVersion || document.Kind != "mirror" || !document.Configured {
		t.Fatalf("unexpected status document: %+v", document)
	}
	if len(document.Jobs) != 2 || document.Jobs[0].IntervalSeconds != 600 {
		t.Fatalf("unexpected jobs: %+v", document.Jobs)
	}
	if document.Jobs[0].LastRunAt == nil || *document.Jobs[0].LastRunAt != lastPush.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected last run: %+v", document.Jobs[0].LastRunAt)
	}
	since := lastPush.Add(time.Hour)
	status.LastPushAttempt, status.LastPushError, status.LastPushErrorSince = since, "push refused: 1 conflict(s)", since
	failing := buildSyncStatusJSON(cfg, status, scheduler)
	if failing.LastPushError != "push refused: 1 conflict(s)" || failing.LastPushErrorSince == nil ||
		*failing.LastPushErrorSince != since.Format(time.RFC3339Nano) || failing.LastPushAttemptAt == nil {
		t.Fatalf("push error fields = %q, %v, %v", failing.LastPushError, failing.LastPushErrorSince, failing.LastPushAttemptAt)
	}
	cfg.Profile = "archive"
	if got := buildSyncStatusJSON(cfg, status, scheduler).Kind; got != "mirror" {
		t.Fatalf("non-peer profile kind = %q, want mirror", got)
	}
	cfg.Profile = syncer.PeerProfile
	if got := buildSyncStatusJSON(cfg, status, scheduler).Kind; got != "peer-profile" {
		t.Fatalf("peer profile kind = %q, want peer-profile", got)
	}
}

// v6: a refused .sync-conflicts (a symlinked backups dir) reaches the JSON
// document, not only the text renderer (#233).
func TestBuildSyncStatusJSONCarriesTheConflictsError(t *testing.T) {
	cfg := &syncer.Config{Profile: syncer.DefaultProfile, LocalPath: t.TempDir(), Target: syncer.Target{Kind: syncer.TargetLocal, Path: t.TempDir()}, Propagation: syncer.DefaultPropagationPolicy()}
	scheduler := &syncer.Scheduler{Paths: &syncer.Paths{LaunchdPlist: filepath.Join(t.TempDir(), "com.dotfiles.sync.plist")}}

	clean := buildSyncStatusJSON(cfg, &syncer.Status{LocalPath: cfg.LocalPath, Target: cfg.Target}, scheduler)
	if clean.SchemaVersion != 7 {
		t.Fatalf("schemaVersion = %d, want 7", clean.SchemaVersion)
	}
	live := buildSyncStatusJSON(cfg, &syncer.Status{LocalPath: cfg.LocalPath, Target: cfg.Target, LiveConflicts: 2, BackupBytes: 9, Conflicts: []syncer.ConflictEntry{{Timestamp: "t"}}}, scheduler)
	if live.LiveConflictCount != 2 || live.BackupBytes != 9 || live.ConflictCount != 1 {
		t.Fatalf("live = %d, backupBytes = %d, conflictCount = %d; want 2, 9, 1", live.LiveConflictCount, live.BackupBytes, live.ConflictCount)
	}
	if clean.ConflictsError != "" {
		t.Fatalf("clean document conflictsError = %q, want empty", clean.ConflictsError)
	}

	status := &syncer.Status{LocalPath: cfg.LocalPath, Target: cfg.Target, ConflictsError: "unsafe backup directory: .sync-conflicts is a symlink"}
	document := buildSyncStatusJSON(cfg, status, scheduler)
	if document.ConflictsError != status.ConflictsError || document.ConflictCount != 0 {
		t.Fatalf("conflicts fields = %q, %d; want the refusal carried with count 0", document.ConflictsError, document.ConflictCount)
	}
}

func TestSyncStatusJSONSensitiveOverridesZeroOneMany(t *testing.T) {
	cfg := &syncer.Config{Profile: syncer.DefaultProfile, LocalPath: t.TempDir(), Target: syncer.Target{Kind: syncer.TargetLocal, Path: t.TempDir()}, Propagation: syncer.DefaultPropagationPolicy()}
	scheduler := &syncer.Scheduler{Paths: &syncer.Paths{LaunchdPlist: filepath.Join(t.TempDir(), "com.dotfiles.sync.plist")}}

	zero := buildSyncStatusJSON(cfg, &syncer.Status{LocalPath: cfg.LocalPath, Target: cfg.Target}, scheduler)
	if zero.SensitiveOverrides == nil || len(zero.SensitiveOverrides) != 0 {
		t.Fatalf("zero overrides = %#v, want non-nil empty array", zero.SensitiveOverrides)
	}

	status := &syncer.Status{
		LocalPath: cfg.LocalPath,
		Target:    cfg.Target,
		SensitiveOverrides: []syncer.SensitiveOverride{
			{AllowPattern: "/.aws/credentials", DenyPattern: "/.aws/credentials"},
			{AllowPattern: "/.secrets/app.env", DenyPattern: "/.secrets/**"},
		},
	}
	document := buildSyncStatusJSON(cfg, status, scheduler)
	if got, want := document.SensitiveOverrides, []syncSensitiveOverrideJSON{
		{AllowPattern: "/.aws/credentials", DenyPattern: "/.aws/credentials"},
		{AllowPattern: "/.secrets/app.env", DenyPattern: "/.secrets/**"},
	}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sensitive overrides = %#v, want %#v", got, want)
	}
}

func TestSyncStatusJSONSensitiveOverridesPurityAndEscaping(t *testing.T) {
	cfg := &syncer.Config{Profile: syncer.DefaultProfile, LocalPath: t.TempDir(), Target: syncer.Target{Kind: syncer.TargetLocal, Path: t.TempDir()}, Propagation: syncer.DefaultPropagationPolicy()}
	status := &syncer.Status{
		LocalPath: cfg.LocalPath,
		Target:    cfg.Target,
		SensitiveOverrides: []syncer.SensitiveOverride{{
			AllowPattern: "/.secrets/" + strings.Repeat("long-", 64) + "\x1b[31m\napp.env",
			DenyPattern:  "/.secrets/**\t",
		}},
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := writeSyncStatusJSON(cmd, cfg, status, &syncer.Scheduler{Paths: &syncer.Paths{LaunchdPlist: filepath.Join(t.TempDir(), "com.dotfiles.sync.plist")}}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "\x1b") || strings.Contains(got, "Sensitive overrides:") {
		t.Fatalf("JSON stdout is contaminated: %q", got)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("JSON stdout is incomplete or invalid: %v\n%s", err, got)
	}
	if !strings.Contains(got, "\\u001b") || !strings.Contains(got, "\\napp.env") || !strings.Contains(got, "\\t") {
		t.Fatalf("control values were not JSON escaped completely:\n%s", got)
	}
	if strings.Index(got, "\"allowCount\"") > strings.Index(got, "\"sensitiveOverrides\"") || strings.Index(got, "\"sensitiveOverrides\"") > strings.Index(got, "\"submoduleCount\"") {
		t.Fatalf("sensitiveOverrides field order drifted:\n%s", got)
	}
}

func TestActivePatternCountIgnoresCommentsAndBlanks(t *testing.T) {
	content := "# header\n\n*.md\n  # comment\n/.secrets/**\n"
	if got := activePatternCount(content); got != 2 {
		t.Fatalf("activePatternCount = %d, want 2", got)
	}
}

func TestAddsActivePatternChecksPatternIdentityNotCount(t *testing.T) {
	current := "# approved\n/.secrets/narrow.env\n"
	if !addsActivePattern(current, "/**\n") {
		t.Fatal("same-count replacement with a broader new pattern must require acknowledgement")
	}
	if addsActivePattern(current, "\n/.secrets/narrow.env\n# reordered\n") {
		t.Fatal("reordering the same active patterns must not require acknowledgement")
	}
}

func TestWritePatternFileAtomicReplacesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "allow.txt")
	if err := writePatternFileAtomic(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := writePatternFileAtomic(path, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "two\n" {
		t.Fatalf("content = %q", body)
	}
}

func TestTailSyncLogKeepsNewestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := tailSyncLog(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "two\nthree" {
		t.Fatalf("tail = %q", got)
	}
}

func TestTailSyncLogSurfacesReadErrors(t *testing.T) {
	if _, err := tailSyncLog(t.TempDir(), 2); err == nil {
		t.Fatal("reading a directory as a log must return an error")
	}
}
