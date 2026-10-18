package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
)

// runDotForTestStdin is runDotForTest with an injected stdin, for the
// hidden --serve path that reads the transport request from it.
func runDotForTestStdin(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd("dev", "test")
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

// seedCLISyncDB creates a claude-mem DB under a temp home with one session,
// observation, and summary, via the package's own writer path.
func seedCLISyncDB(t *testing.T, home string) *aisettings.SyncDB {
	t.Helper()
	dbPath := aisettings.DefaultSyncDBPath(home)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := aisettings.OpenSyncDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := seedSyncTestSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedCLISyncDBSchemaOnly creates the empty schema for the import-target side.
func seedCLISyncDBSchemaOnly(t *testing.T, home string) *aisettings.SyncDB {
	t.Helper()
	dbPath := aisettings.DefaultSyncDBPath(home)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := aisettings.OpenSyncDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := sql.Open("sqlite", "file:"+db.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(cliSyncSchemaDDL); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMemorySyncServe_CountMaxExportImport(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // a regression to os.UserHomeDir() must not reach the real worker
	home := t.TempDir()
	seedCLISyncDB(t, home)

	out, _, err := runDotForTest("--home", home, "ai", "memory", "sync", "--serve", "count")
	if err != nil {
		t.Fatalf("serve count: %v", err)
	}
	var countResp struct {
		Counts *aisettings.TableCounts `json:"counts"`
	}
	if err := json.Unmarshal([]byte(out), &countResp); err != nil {
		t.Fatalf("serve count output is not the response envelope: %q", out)
	}
	if countResp.Counts.Obs != 1 || countResp.Counts.Sums != 1 || countResp.Counts.Sessions != 1 || countResp.Counts.ObsWithoutSession != 0 {
		t.Fatalf("counts = %#v", countResp.Counts)
	}

	out, _, err = runDotForTest("--home", home, "ai", "memory", "sync", "--serve", "max")
	if err != nil {
		t.Fatalf("serve max: %v", err)
	}
	var maxResp struct {
		Max map[string]string `json:"max"`
	}
	if err := json.Unmarshal([]byte(out), &maxResp); err != nil {
		t.Fatalf("serve max output: %q", out)
	}
	if maxResp.Max["observations"] != "2026-09-20T10:01:00.000Z" {
		t.Fatalf("max = %v", maxResp.Max)
	}

	out, _, err = runDotForTest("--home", home, "ai", "memory", "sync", "--serve", "export")
	if err != nil {
		t.Fatalf("serve export: %v", err)
	}
	var exportResp struct {
		Bundle *aisettings.Bundle `json:"bundle"`
	}
	if err := json.Unmarshal([]byte(out), &exportResp); err != nil {
		t.Fatalf("serve export output: %q", out)
	}
	if len(exportResp.Bundle.Observations) != 1 || len(exportResp.Bundle.Sessions) != 1 {
		t.Fatalf("bundle = %d obs %d sessions", len(exportResp.Bundle.Observations), len(exportResp.Bundle.Sessions))
	}

	// Import the bundle into a second home's DB over the serve path: schema
	// present, no rows yet.
	home2 := t.TempDir()
	seedCLISyncDBSchemaOnly(t, home2)
	// --home decides which worker the import kicks (#160): point home2's
	// settings at a fake worker and expect exactly one restart request.
	kicks := 0
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/admin/restart" {
			kicks++
		}
	}))
	defer worker.Close()
	settings, _ := json.Marshal(map[string]string{"CLAUDE_MEM_WORKER_PORT": strings.TrimPrefix(worker.URL, "http://127.0.0.1:")})
	if err := os.WriteFile(filepath.Join(home2, ".claude-mem", "settings.json"), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	reqBody, _ := json.Marshal(map[string]any{"bundle": exportResp.Bundle})
	out, _, err = runDotForTestStdin(t, string(reqBody), "--home", home2, "ai", "memory", "sync", "--serve", "import")
	if err != nil {
		t.Fatalf("serve import: %v", err)
	}
	var importResp struct {
		Result *aisettings.ImportResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &importResp); err != nil {
		t.Fatalf("serve import output: %q", out)
	}
	if importResp.Result.Obs != 1 || importResp.Result.Sessions != 1 {
		t.Fatalf("import result = %#v", importResp.Result)
	}
	if kicks != 1 {
		t.Fatalf("worker under --home received %d restart kicks, want 1", kicks)
	}

	// Without --home (the peer side: SSHTransport never forwards it), the
	// kick follows the process home.
	home3 := t.TempDir()
	seedCLISyncDBSchemaOnly(t, home3)
	kicks3 := 0
	worker3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/admin/restart" {
			kicks3++
		}
	}))
	defer worker3.Close()
	settings3, _ := json.Marshal(map[string]string{"CLAUDE_MEM_WORKER_PORT": strings.TrimPrefix(worker3.URL, "http://127.0.0.1:")})
	if err := os.WriteFile(filepath.Join(home3, ".claude-mem", "settings.json"), settings3, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOTFILES_HOME", "")
	t.Setenv("HOME", home3)
	if _, _, err = runDotForTestStdin(t, string(reqBody), "ai", "memory", "sync", "--serve", "import"); err != nil {
		t.Fatalf("serve import without --home: %v", err)
	}
	if kicks3 != 1 || kicks != 1 {
		t.Fatalf("serve import without --home: process-home worker kicks = %d, --home worker kicks = %d; want 1, 1", kicks3, kicks)
	}
}

func TestMemorySync_UnknownAction(t *testing.T) {
	_, _, err := runDotForTest("--home", t.TempDir(), "ai", "memory", "sync", "teleport")
	if err == nil || !strings.Contains(err.Error(), "unknown sync action") {
		t.Fatalf("unknown action = %v", err)
	}
}

func TestMemorySync_NoPeerConfigured(t *testing.T) {
	_, _, err := runDotForTest("--home", t.TempDir(), "ai", "memory", "sync")
	if err == nil || !strings.Contains(err.Error(), "--peer") {
		t.Fatalf("missing peer must name the remedy: %v", err)
	}
}

// TestMemorySync_UnreachablePeerFailsInteractive: a direct (non-scheduled)
// run against an unreachable peer exits non-zero before opening the DB.
func TestMemorySync_UnreachablePeerFailsInteractive(t *testing.T) {
	_, _, err := runDotForTest("--home", t.TempDir(), "ai", "memory", "sync", "--peer", "192.0.2.1")
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("unreachable peer = %v", err)
	}
}

// TestMemorySync_ScheduledUnreachableSkips: DOT_SCHEDULED_RUN=1 converts
// the same failure into a quiet exit 0 (launchd records no failure).
func TestMemorySync_ScheduledUnreachableSkips(t *testing.T) {
	t.Setenv(scheduledRunEnv, "1")
	_, _, err := runDotForTest("--home", t.TempDir(), "ai", "memory", "sync", "--peer", "192.0.2.1")
	if err != nil {
		t.Fatalf("scheduled run with unreachable peer must exit 0: %v", err)
	}
}

// TestMemorySyncStatus_UnreachablePeerStillRenders: status never fails on
// an away peer; it reports the unreachable counts row instead.
func TestMemorySyncStatus_UnreachablePeerStillRenders(t *testing.T) {
	home := t.TempDir()
	seedCLISyncDB(t, home)
	out, _, err := runDotForTest("--home", home, "ai", "memory", "sync", "status", "--peer", "192.0.2.1")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"Peer", "192.0.2.1", "obs 1", "unreachable"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestMemoryInstall_DryRunMentionsSyncAgent(t *testing.T) {
	home := t.TempDir()
	out, _, err := runDotForTest("--home", home, "ai", "memory", "install", "--dry-run", "--peer", "user@mac2")
	if err != nil {
		t.Fatalf("install --dry-run: %v", err)
	}
	if !strings.Contains(out, "com.dotfiles.claude-mem-sync.plist") || !strings.Contains(out, "user@mac2") {
		t.Fatalf("install dry-run must preview the sync agent:\n%s", out)
	}
}

func TestMemoryUpdate_NotInstalledErrors(t *testing.T) {
	home := t.TempDir()
	// A marketplace checkout but no installed plugin: update refuses with a hint.
	pluginDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin", ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"version":"13.28.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runDotForTest("--home", home, "ai", "memory", "update", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("update without install = %v", err)
	}
}

func TestMemoryUpdate_CurrentPrintsVersion(t *testing.T) {
	home := t.TempDir()
	pluginDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin", ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"version":"13.28.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	doc := fmt.Sprintf(`{"version":2,"plugins":{"claude-mem@thedotmack":[{"scope":"user","installPath":%q,"version":"13.28.0"}]}}`,
		filepath.Join(pluginsDir, "cache", "thedotmack", "claude-mem", "13.28.0"))
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runDotForTest("--home", home, "ai", "memory", "update", "--dry-run")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, "13.28.0") || !strings.Contains(out, "current") {
		t.Fatalf("update output = %q", out)
	}
}

// cliSyncSchemaDDL is the trimmed claude-mem schema the serve ops touch,
// mirroring internal/aisettings/claudemem_sync_test.go's fixture.
const cliSyncSchemaDDL = `
CREATE TABLE sdk_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content_session_id TEXT NOT NULL,
    memory_session_id TEXT UNIQUE,
    project TEXT NOT NULL,
    platform_source TEXT NOT NULL DEFAULT 'claude',
    started_at TEXT NOT NULL,
    started_at_epoch INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    worker_port INTEGER
);
CREATE TABLE observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_session_id TEXT NOT NULL,
    project TEXT NOT NULL,
    type TEXT NOT NULL,
    title TEXT,
    created_at TEXT NOT NULL,
    created_at_epoch INTEGER NOT NULL,
    agent_type TEXT,
    agent_id TEXT,
    metadata TEXT,
    content_hash TEXT,
    generated_by_model TEXT
);
CREATE TABLE session_summaries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_session_id TEXT NOT NULL,
    project TEXT NOT NULL,
    request TEXT,
    created_at TEXT NOT NULL,
    created_at_epoch INTEGER NOT NULL
);
`

// seedSyncTestSchema creates the tables via database/sql (the sqlite driver
// is registered by the aisettings import) and seeds one row per table
// through the package's own Import.
func seedSyncTestSchema(db *aisettings.SyncDB) error {
	raw, err := sql.Open("sqlite", "file:"+db.Path)
	if err != nil {
		return err
	}
	defer raw.Close()
	if _, err := raw.Exec(cliSyncSchemaDDL); err != nil {
		return err
	}
	bundle := &aisettings.Bundle{
		Sessions: []map[string]any{{
			"content_session_id": "c1", "memory_session_id": "m1", "project": "proj",
			"platform_source": "claude", "started_at": "2026-09-20T10:00:00.000Z", "started_at_epoch": 0, "status": "active",
		}},
		Observations: []map[string]any{{
			"memory_session_id": "m1", "project": "proj", "type": "discovery", "title": "T1",
			"created_at": "2026-09-20T10:01:00.000Z", "created_at_epoch": 0,
			"agent_type": "claude", "agent_id": "a1", "metadata": `{"k":"v"}`, "content_hash": "h1", "generated_by_model": "haiku",
		}},
		Summaries: []map[string]any{{
			"memory_session_id": "m1", "project": "proj", "request": "req-1",
			"created_at": "2026-09-20T10:02:00.000Z", "created_at_epoch": 0,
		}},
	}
	_, err = db.Import(context.Background(), bundle)
	return err
}

// The peer profile's remote_dot follows its host: the default target and an
// explicit --peer naming that host carry it, any other target runs the
// newest release (#199 review).
func TestMemorySyncPeerCarriesThePeerProfilesPin(t *testing.T) {
	f := newSyncCLIFixture(t)
	writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "peer", "config.yaml"),
		"target: ssh:peer-alias:/remote/work\nremote_dot: ~/.local/bin/dot\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	for _, tc := range []struct{ flag, target, pin string }{
		{"", "peer-alias", "~/.local/bin/dot"},
		{"peer-alias", "peer-alias", "~/.local/bin/dot"},
		{"other-host", "other-host", ""},
	} {
		cmd := &cobra.Command{}
		cmd.Flags().String("peer", tc.flag, "")
		cmd.Flags().String("remote-db", "", "")
		cmd.Flags().String("home", "", "")
		peer, err := memorySyncPeer(cmd)
		if err != nil || peer.Target != tc.target || peer.RemoteDot != tc.pin {
			t.Errorf("--peer %q: %+v %v, want %s with pin %q", tc.flag, peer, err, tc.target, tc.pin)
		}
	}
}
