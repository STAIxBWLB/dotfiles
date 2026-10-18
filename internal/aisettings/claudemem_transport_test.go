package aisettings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTransport serves SyncTransport from an in-process SyncDB — the two
// ends of a sync as two temp databases.
type fakeTransport struct {
	db *SyncDB
}

func (f *fakeTransport) Max(ctx context.Context, _ SyncPeer) (map[string]string, error) {
	return f.db.MaxCreatedAt(ctx)
}
func (f *fakeTransport) Export(ctx context.Context, _ SyncPeer, cutoffs map[string]string, onlySessions bool) (*Bundle, error) {
	return f.db.Export(ctx, cutoffs, onlySessions)
}
func (f *fakeTransport) Import(ctx context.Context, _ SyncPeer, b *Bundle) (*ImportResult, error) {
	return f.db.Import(ctx, b)
}
func (f *fakeTransport) Counts(ctx context.Context, _ SyncPeer) (*TableCounts, error) {
	return f.db.Counts(ctx)
}

func seedPair(t *testing.T) (local, remote *SyncDB) {
	t.Helper()
	local = newTestSyncDB(t)
	remote = newTestSyncDB(t)
	seedSession(t, local, "c1", "m1", "2026-09-20T10:00:00.000Z")
	seedObs(t, local, "m1", "2026-09-20T10:01:00.000Z", "local-obs")
	seedSummary(t, local, "m1", "2026-09-20T10:02:00.000Z", "local-req")
	seedSession(t, remote, "c2", "m2", "2026-09-21T10:00:00.000Z")
	seedObs(t, remote, "m2", "2026-09-21T10:01:00.000Z", "remote-obs")
	return local, remote
}

func TestRunMemorySync_BothDirections(t *testing.T) {
	local, remote := seedPair(t)
	peer := SyncPeer{Target: "fake"}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	report, err := RunMemorySync(context.Background(), local, &fakeTransport{db: remote}, peer, SyncOptions{Direction: "sync"}, now)
	if err != nil {
		t.Fatalf("RunMemorySync: %v", err)
	}
	if report.Pushed.Obs != 1 || report.Pushed.Sums != 1 || report.Pushed.Sessions != 1 {
		t.Fatalf("pushed = %#v", report.Pushed)
	}
	if report.Pulled.Obs != 1 || report.Pulled.Sessions != 1 {
		t.Fatalf("pulled = %#v", report.Pulled)
	}
	// Both sides now hold each other's rows.
	for _, db := range []*SyncDB{local, remote} {
		counts, err := db.Counts(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if counts.Obs != 2 || counts.Sessions != 2 {
			t.Fatalf("counts after sync = %#v", counts)
		}
	}
	// The second run is a no-op: incremental cutoff + dedupe.
	report2, err := RunMemorySync(context.Background(), local, &fakeTransport{db: remote}, peer, SyncOptions{Direction: "sync"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if report2.Pushed.Obs != 0 || report2.Pulled.Obs != 0 {
		t.Fatalf("second run must import 0: %#v / %#v", report2.Pushed, report2.Pulled)
	}
}

func TestRunMemorySync_PushPullOnly(t *testing.T) {
	local, remote := seedPair(t)
	peer := SyncPeer{Target: "fake"}
	now := time.Now()

	report, err := RunMemorySync(context.Background(), local, &fakeTransport{db: remote}, peer, SyncOptions{Direction: "push"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pushed == nil || report.Pulled != nil {
		t.Fatalf("push-only report = %#v", report)
	}
	if counts, _ := local.Counts(context.Background()); counts.Obs != 1 {
		t.Fatalf("push must not pull: %#v", counts)
	}

	report, err = RunMemorySync(context.Background(), local, &fakeTransport{db: remote}, peer, SyncOptions{Direction: "pull"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pulled == nil || report.Pushed != nil {
		t.Fatalf("pull-only report = %#v", report)
	}
	if counts, _ := local.Counts(context.Background()); counts.Obs != 2 {
		t.Fatalf("pull must land remote rows: %#v", counts)
	}

	if _, err := RunMemorySync(context.Background(), local, &fakeTransport{db: remote}, peer, SyncOptions{Direction: "sideways"}, now); err == nil {
		t.Fatal("unknown direction must error")
	}
}

func TestRunMemorySync_SessionsMode(t *testing.T) {
	local, remote := seedPair(t)
	report, err := RunMemorySync(context.Background(), local, &fakeTransport{db: remote}, SyncPeer{Target: "fake"},
		SyncOptions{Direction: "sessions", OnlySessions: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Sessions cross; content rows do not.
	if counts, _ := local.Counts(context.Background()); counts.Sessions != 2 || counts.Obs != 1 {
		t.Fatalf("local after sessions pass = %#v", counts)
	}
	if counts, _ := remote.Counts(context.Background()); counts.Sessions != 2 || counts.Obs != 1 {
		t.Fatalf("remote after sessions pass = %#v", counts)
	}
	_ = report
}

// TestSSHTransportOverServe drives the whole wire protocol against a temp
// DB: SSHTransport with a runner that hands the request to ServeOp.
func TestSSHTransportOverServe(t *testing.T) {
	remote := newTestSyncDB(t)
	seedSession(t, remote, "c1", "m1", "2026-09-20T10:00:00.000Z")
	seedObs(t, remote, "m1", "2026-09-20T10:01:00.000Z", "x")

	t.Setenv("HOME", t.TempDir()) // a regression to os.UserHomeDir() must not reach the real worker
	var lastArgs []string
	// This test's import dedupes to zero rows, so the kick branch is not
	// reached; the settings-free temp home keeps it local if that changes.
	home := t.TempDir()
	transport := &SSHTransport{Run: func(ctx context.Context, target string, serveArgs []string, stdin []byte) ([]byte, error) {
		lastArgs = serveArgs
		// serveArgs: ai memory sync --serve <op> [--remote-db <path>]
		op := serveArgs[4]
		var out bytes.Buffer
		if err := ServeOp(ctx, remote.Path, home, op, bytes.NewReader(stdin), &out); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}}
	peer := SyncPeer{Target: "user@mac2"}

	maxes, err := transport.Max(context.Background(), peer)
	if err != nil {
		t.Fatalf("Max: %v", err)
	}
	if maxes["observations"] == "" {
		t.Fatalf("maxes = %v", maxes)
	}
	bundle, err := transport.Export(context.Background(), peer, nil, false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(bundle.Observations) != 1 || len(bundle.Sessions) != 1 {
		t.Fatalf("bundle = %d obs %d sessions", len(bundle.Observations), len(bundle.Sessions))
	}
	counts, err := transport.Counts(context.Background(), peer)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.Obs != 1 || counts.ObsWithoutSession != 0 {
		t.Fatalf("counts = %#v", counts)
	}

	// Import over the wire into the same DB dedupes to zero.
	res, err := transport.Import(context.Background(), peer, bundle)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Obs != 0 || res.Sessions != 0 {
		t.Fatalf("self-import must dedupe: %#v", res)
	}
	if !strings.Contains(strings.Join(lastArgs, " "), "--serve import") {
		t.Fatalf("serve args = %v", lastArgs)
	}

	// A failing op comes back as an error, not a zero value.
	bad := &SSHTransport{Run: func(context.Context, string, []string, []byte) ([]byte, error) {
		return []byte(`{"error":"boom"}`), nil
	}}
	if _, err := bad.Counts(context.Background(), peer); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("serve error must propagate: %v", err)
	}
	if _, err := transport.Counts(context.Background(), SyncPeer{}); err == nil {
		t.Fatal("empty target must error")
	}
}

func TestServeOp_UnknownOpAndBadDB(t *testing.T) {
	var out bytes.Buffer
	if err := ServeOp(context.Background(), filepath_join(t), "", "frobulate", strings.NewReader("{}"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unknown serve op") {
		t.Fatalf("response = %s", out.String())
	}
	out.Reset()
	if err := ServeOp(context.Background(), "/nonexistent/dir/db.sqlite", "", "count", strings.NewReader("{}"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "error") {
		t.Fatalf("response = %s", out.String())
	}
	// Serve writes ONLY the response envelope to stdout.
	if strings.Contains(out.String(), "\n\n") {
		t.Fatalf("serve output must be one JSON document: %q", out.String())
	}
}

func filepath_join(t *testing.T) string {
	t.Helper()
	return newTestSyncDB(t).Path
}

func TestReceiverCutoffs_ArePerTable(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	maxes := map[string]string{
		"observations":      "2026-09-27T00:00:00.000Z",
		"session_summaries": "2026-01-01T00:00:00.000Z",
	}
	cutoffs, err := receiverCutoffs(maxes, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if cutoffs["observations"] != "2026-09-20T00:00:00.000Z" {
		t.Errorf("observations cutoff = %q", cutoffs["observations"])
	}
	if cutoffs["session_summaries"] != "2025-12-25T00:00:00.000Z" {
		t.Errorf("summaries cutoff = %q — a shared cutoff would strand this table", cutoffs["session_summaries"])
	}
	if full, err := receiverCutoffs(maxes, true, now); err != nil || full != nil {
		t.Errorf("--full must disable cutoffs: %v %v", full, err)
	}
}

func TestSyncState_RoundTrip(t *testing.T) {
	path := SyncStatePath(t.TempDir())
	entry := SyncStateEntry{LastRun: time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC), LastResult: "pushed +1 obs"}
	if err := SaveSyncState(path, "mac2", entry); err != nil {
		t.Fatal(err)
	}
	// A second peer preserves the first.
	if err := SaveSyncState(path, "mac3", entry); err != nil {
		t.Fatal(err)
	}
	peers, err := LoadSyncState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 || peers["mac2"].LastResult != "pushed +1 obs" || !peers["mac2"].LastRun.Equal(entry.LastRun) {
		t.Fatalf("peers = %#v", peers)
	}
	if got, err := LoadSyncState(t.TempDir() + "/absent.json"); err != nil || len(got) != 0 {
		t.Fatalf("missing file = %#v, %v", got, err)
	}
}

func TestSyncReportSummarize(t *testing.T) {
	r := &SyncReport{Pushed: &ImportResult{Obs: 1, Sums: 2, Sessions: 3}}
	if got := r.Summarize(); got != "pushed +1 obs +2 sums +3 sessions" {
		t.Errorf("Summarize = %q", got)
	}
	if got := (&SyncReport{}).Summarize(); got != "no rows transferred" {
		t.Errorf("empty Summarize = %q", got)
	}
}

// workerHome returns a home whose claude-mem settings point at a fake worker,
// and a counter of the restart kicks that worker received.
func workerHome(t *testing.T) (string, *int) {
	t.Helper()
	return workerHomeAt(t, t.TempDir())
}

// workerHomeAt writes fake-worker claude-mem settings under home.
func workerHomeAt(t *testing.T, home string) (string, *int) {
	t.Helper()
	hits := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/admin/restart" {
			*hits++
		}
	}))
	t.Cleanup(srv.Close)
	memDir := filepath.Join(home, ".claude-mem")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	settings, _ := json.Marshal(map[string]string{"CLAUDE_MEM_WORKER_PORT": port})
	if err := os.WriteFile(filepath.Join(memDir, "settings.json"), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	return home, hits
}

// TestServeOp_ImportKicksOnlyTheGivenHome: an import that lands rows kicks
// the worker configured under the passed home and no other (#160).
func TestServeOp_ImportKicksOnlyTheGivenHome(t *testing.T) {
	src := newTestSyncDB(t)
	seedSession(t, src, "c1", "m1", "2026-09-20T10:00:00.000Z")
	seedObs(t, src, "m1", "2026-09-20T10:01:00.000Z", "x")
	bundle, err := src.Export(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(serveRequest{Bundle: bundle})

	// HOME points at a trap worker: a regression to os.UserHomeDir() inside
	// ServeOp would kick it.
	trap, trapHits := workerHome(t)
	t.Setenv("HOME", trap)
	home, hits := workerHome(t)
	dst := newTestSyncDB(t)
	var out bytes.Buffer
	if err := ServeOp(context.Background(), dst.Path, home, "import", bytes.NewReader(req), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"result"`) {
		t.Fatalf("import response = %s", out.String())
	}
	if *hits != 1 || *trapHits != 0 {
		t.Fatalf("kicks: given home = %d, process HOME = %d; want 1, 0", *hits, *trapHits)
	}

	// An empty home skips the kick entirely: neither the process HOME nor the
	// DB's grandparent (the fallback removed for #160) may be kicked.
	_, grandHits := workerHomeAt(t, filepath.Dir(filepath.Dir(dst.Path)))
	*hits = 0
	src2 := newTestSyncDB(t)
	seedSession(t, src2, "c2", "m2", "2026-09-21T10:00:00.000Z")
	seedObs(t, src2, "m2", "2026-09-21T10:01:00.000Z", "y")
	bundle2, err := src2.Export(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	req2, _ := json.Marshal(serveRequest{Bundle: bundle2})
	out.Reset()
	if err := ServeOp(context.Background(), dst.Path, "", "import", bytes.NewReader(req2), &out); err != nil {
		t.Fatal(err)
	}
	// The kick branch is only reached when rows land; without this the
	// no-kick assertion below could pass vacuously.
	var resp2 serveResponse
	if err := json.Unmarshal(out.Bytes(), &resp2); err != nil || resp2.Result == nil || resp2.Result.Obs != 1 {
		t.Fatalf("second import must land one observation: %s", out.String())
	}
	if *hits != 0 || *trapHits != 0 || *grandHits != 0 {
		t.Fatalf("empty home kicked: given home = %d, process HOME = %d, DB grandparent = %d; want 0, 0, 0", *hits, *trapHits, *grandHits)
	}
}

// #195: the real ssh transport runs the peer's dot the way dot peer picks it
// (#176): the release at /opt/homebrew/bin, not a stale dev build that comes
// first on PATH. The probe runs once per target.
func TestSSHTransportRunsThePeersReleaseDot(t *testing.T) {
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "ssh.log")
	script := "#!/bin/sh\n" +
		"for last; do :; done\n" +
		"printf '%s\\n' \"$last\" | head -n 1 >> '" + log + "'\n" +
		"case \"$last\" in\n" +
		"  *'list dot candidates'*) printf '/h/.local/bin/dot\\tdot version dev (f467e65)\\n/opt/homebrew/bin/dot\\tdot version 2.70.22 (388a398)\\n' ;;\n" +
		"  *) cat >/dev/null; printf '{\"max\":{}}\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tr := &SSHTransport{}
	for i := 0; i < 2; i++ {
		if _, err := tr.Max(context.Background(), SyncPeer{Target: "peer-mac"}); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	var probes, serves int
	for _, line := range lines {
		switch {
		case strings.Contains(line, "list dot candidates"):
			probes++
		case strings.HasPrefix(line, "exec '/opt/homebrew/bin/dot' 'ai' 'memory' 'sync' '--serve' 'max'"):
			serves++
		default:
			t.Errorf("unexpected remote command %q", line)
		}
	}
	if probes != 1 || serves != 2 {
		t.Fatalf("probes %d, serves %d:\n%s", probes, serves, body)
	}
}

// A pinned remote_dot (the peer profile's, for its host) is run as pinned,
// a dev build included, as dot peer runs it (#199 review).
func TestSSHTransportHonorsThePin(t *testing.T) {
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "ssh.log")
	script := "#!/bin/sh\n" +
		"for last; do :; done\n" +
		"printf '%s\\n' \"$last\" >> '" + log + "'\n" +
		"case \"$last\" in\n" +
		"  *'list dot candidates'*) printf '/h/.local/bin/dot\\tdot version dev (f467e65)\\n' ;;\n" +
		"  *) cat >/dev/null; printf '{\"max\":{}}\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := (&SSHTransport{}).Max(context.Background(), SyncPeer{Target: "peer-mac", RemoteDot: "~/.local/bin/dot"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(log)
	if !strings.Contains(string(body), `for c in "$HOME"/'.local/bin/dot'; do`) || !strings.Contains(string(body), "exec '/h/.local/bin/dot' 'ai' 'memory' 'sync' '--serve' 'max'") {
		t.Fatalf("the pin was not probed and run:\n%s", body)
	}
}

func TestServeOpSessionRelinkBackfill(t *testing.T) {
	dst := newTestSyncDB(t)
	seedSession(t, dst, "same-content", "receiver-memory", "2026-09-20T10:00:00.000Z")
	seedObs(t, dst, "sender-memory", "2026-09-20T11:00:00.000Z", "known-orphan")
	home := t.TempDir()
	kicks := make(chan struct{}, 2)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/admin/restart" {
			kicks <- struct{}{}
		}
	}))
	defer worker.Close()
	mustWriteJSON(t, filepath.Join(home, ".claude-mem", "settings.json"), map[string]string{"CLAUDE_MEM_WORKER_PORT": strings.TrimPrefix(worker.URL, "http://127.0.0.1:")})
	body, err := json.Marshal(serveRequest{Bundle: &Bundle{Sessions: []map[string]any{{"memory_session_id": "sender-memory", "content_session_id": "same-content", "platform_source": "claude"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		var out bytes.Buffer
		if err := ServeOp(context.Background(), dst.Path, home, "import", bytes.NewReader(body), &out); err != nil {
			t.Fatal(err)
		}
		var response serveResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error != "" {
			t.Fatal(response.Error)
		}
		if response.Result.Obs != 0 || response.Result.Sums != 0 || response.Result.Sessions != 0 {
			t.Fatalf("unexpected inserted rows: %+v", response.Result)
		}
		wantRelinks := 1 - pass
		if response.Result.RelinkedObservations != wantRelinks {
			t.Fatalf("pass %d result: %+v", pass, response.Result)
		}
	}
	if len(kicks) != 1 {
		t.Fatalf("worker got %d backfill kicks, want one relink kick and no idempotent kick", len(kicks))
	}
}

// #289: a store whose write lock another connection holds fails the run
// after the busy timeout, naming the blocked phase, instead of spinning.
func TestRunMemorySync_LockedStoreFailsFastNamingPhase(t *testing.T) {
	local, remote := seedPair(t)
	holder, err := sql.Open("sqlite", "file:"+local.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	conn, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	start := time.Now()
	_, err = RunMemorySync(context.Background(), local, &fakeTransport{db: remote}, SyncPeer{Target: "fake"}, SyncOptions{Direction: "pull"}, time.Now())
	if err == nil {
		t.Fatal("pull into a locked store succeeded")
	}
	if !strings.Contains(err.Error(), "pull: local import") || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("error should name the phase and SQLITE_BUSY: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("locked run took %s", elapsed)
	}
}

// stallTransport never answers an export until its context ends.
type stallTransport struct{ fakeTransport }

func (s *stallTransport) Export(ctx context.Context, _ SyncPeer, _ map[string]string, _ bool) (*Bundle, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// #289: the deadline ends a stalled phase and the error names it.
func TestRunMemorySync_DeadlineEndsStalledPhase(t *testing.T) {
	local, remote := seedPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := RunMemorySync(ctx, local, &stallTransport{fakeTransport{db: remote}}, SyncPeer{Target: "fake"}, SyncOptions{Direction: "pull"}, time.Now())
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "pull: peer export") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stalled run outlived its deadline by %s", elapsed)
	}
}

// cancelOnTable cancels the import when it reaches a table, standing in for
// a SIGTERM that arrives after earlier tables were written in the tx.
type cancelOnTable struct {
	slog.Handler
	table  string
	cancel context.CancelFunc
}

func (h cancelOnTable) Handle(ctx context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "table" && a.Value.String() == h.table {
			h.cancel()
		}
		return true
	})
	return h.Handler.Handle(ctx, r)
}

// #289: canceling mid-import rolls the whole transaction back.
func TestImport_CancelMidwayRollsBack(t *testing.T) {
	sender, receiver := seedPair(t)
	bundle, err := sender.Export(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prev := slog.Default()
	slog.SetDefault(slog.New(cancelOnTable{Handler: slog.NewTextHandler(os.Stderr, nil), table: "observations", cancel: cancel}))
	defer slog.SetDefault(prev)

	if _, err := receiver.Import(ctx, bundle); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	counts, err := receiver.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// seedPair gave the receiver one session and one observation of its own.
	if counts.Sessions != 1 || counts.Obs != 1 || counts.Sums != 0 {
		t.Fatalf("partial import left behind: %+v", counts)
	}
}

// The in-memory key set must see rows inserted earlier in the same import.
func TestImport_DedupesWithinOneBundle(t *testing.T) {
	sender, receiver := seedPair(t)
	bundle, err := sender.Export(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Observations = append(bundle.Observations, bundle.Observations[0])
	bundle.Summaries = append(bundle.Summaries, bundle.Summaries[0])
	res, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if res.Obs != 1 || res.Sums != 1 {
		t.Fatalf("duplicate rows in one bundle imported twice: %+v", res)
	}
}
