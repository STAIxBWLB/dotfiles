package aisettings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// Sync transport: the remote side is `ssh <target> dot ai memory sync
// --serve <op>` with one JSON request on stdin and one JSON response on
// stdout. The interface is the test seam; an in-process fake answers from
// a second SyncDB.

// SyncTransport carries the four serve ops.
type SyncTransport interface {
	Max(ctx context.Context, peer SyncPeer) (map[string]string, error)
	Export(ctx context.Context, peer SyncPeer, cutoffs map[string]string, onlySessions bool) (*Bundle, error)
	Import(ctx context.Context, peer SyncPeer, b *Bundle) (*ImportResult, error)
	Counts(ctx context.Context, peer SyncPeer) (*TableCounts, error)
}

// serveRequest is the stdin payload for every op; each op reads what it needs.
type serveRequest struct {
	Cutoffs      map[string]string `json:"cutoffs,omitempty"`
	OnlySessions bool              `json:"only_sessions,omitempty"`
	Bundle       *Bundle           `json:"bundle,omitempty"`
}

// serveResponse is the stdout payload; Error is set on failure.
type serveResponse struct {
	Error  string            `json:"error,omitempty"`
	Max    map[string]string `json:"max,omitempty"`
	Bundle *Bundle           `json:"bundle,omitempty"`
	Result *ImportResult     `json:"result,omitempty"`
	Counts *TableCounts      `json:"counts,omitempty"`
}

// SSHRunner runs the serve command and returns its stdout. Injectable so
// the SSH transport itself is testable without sshd.
type SSHRunner func(ctx context.Context, target string, serveArgs []string, stdin []byte) ([]byte, error)

// SSHTransport reaches the peer over ssh. The peer needs nothing but the
// dot binary — no python, no jq, no claude-mem tooling.
type SSHTransport struct {
	Run  SSHRunner         // nil uses the real ssh
	dots map[string]string // the peer's dot per target, resolved once
}

// peerDot resolves the peer's dot the way dot peer does (#176, #195): the
// newest release among its install locations, not the first on PATH, so a
// stale dev build at ~/.local/bin does not shadow it.
func (t *SSHTransport) peerDot(ctx context.Context, peer SyncPeer) (string, error) {
	key := peer.Target + "\x00" + peer.RemoteDot
	if dot, ok := t.dots[key]; ok {
		return dot, nil
	}
	dot, err := syncer.ResolvePeerDotPath(ctx, dotexec.NewRunner(false, slog.New(slog.DiscardHandler)), peer.Target, peer.RemoteDot)
	if err != nil {
		return "", err
	}
	if t.dots == nil {
		t.dots = map[string]string{}
	}
	t.dots[key] = dot
	return dot, nil
}

func (t *SSHTransport) call(ctx context.Context, peer SyncPeer, op string, req serveRequest) (*serveResponse, error) {
	if peer.Target == "" {
		return nil, fmt.Errorf("sync peer target is empty")
	}
	args := []string{"ai", "memory", "sync", "--serve", op}
	if peer.RemoteDB != "" {
		args = append(args, "--remote-db", peer.RemoteDB)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	run := t.Run
	if run == nil {
		dot, err := t.peerDot(ctx, peer)
		if err != nil {
			return nil, fmt.Errorf("ssh %s serve %s: %w", peer.Target, op, err)
		}
		run = func(ctx context.Context, target string, serveArgs []string, stdin []byte) ([]byte, error) {
			return sshServe(ctx, target, dot, serveArgs, stdin)
		}
	}
	out, err := run(ctx, peer.Target, args, body)
	if err != nil {
		return nil, fmt.Errorf("ssh %s serve %s: %w", peer.Target, op, err)
	}
	var resp serveResponse
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		return nil, fmt.Errorf("ssh %s serve %s returned invalid response: %w", peer.Target, op, err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("peer %s: %s", peer.Target, resp.Error)
	}
	return &resp, nil
}

// Max implements SyncTransport.
func (t *SSHTransport) Max(ctx context.Context, peer SyncPeer) (map[string]string, error) {
	resp, err := t.call(ctx, peer, "max", serveRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Max, nil
}

// Export implements SyncTransport.
func (t *SSHTransport) Export(ctx context.Context, peer SyncPeer, cutoffs map[string]string, onlySessions bool) (*Bundle, error) {
	resp, err := t.call(ctx, peer, "export", serveRequest{Cutoffs: cutoffs, OnlySessions: onlySessions})
	if err != nil {
		return nil, err
	}
	if resp.Bundle == nil {
		return nil, fmt.Errorf("peer %s: export returned no bundle", peer.Target)
	}
	return resp.Bundle, nil
}

// Import implements SyncTransport.
func (t *SSHTransport) Import(ctx context.Context, peer SyncPeer, b *Bundle) (*ImportResult, error) {
	resp, err := t.call(ctx, peer, "import", serveRequest{Bundle: b})
	if err != nil {
		return nil, err
	}
	if resp.Result == nil {
		return nil, fmt.Errorf("peer %s: import returned no result", peer.Target)
	}
	return resp.Result, nil
}

// Counts implements SyncTransport.
func (t *SSHTransport) Counts(ctx context.Context, peer SyncPeer) (*TableCounts, error) {
	resp, err := t.call(ctx, peer, "count", serveRequest{})
	if err != nil {
		return nil, err
	}
	if resp.Counts == nil {
		return nil, fmt.Errorf("peer %s: count returned no counts", peer.Target)
	}
	return resp.Counts, nil
}

// sshServe is the production SSHRunner: BatchMode so a missing key fails
// fast instead of hanging a scheduled run on a password prompt. dot is the
// absolute path the peer's own probe reported (peerDot), so the minimal PATH
// sshd gives non-interactive shells does not matter: the peer really does
// need nothing but the dot binary on disk.
func sshServe(ctx context.Context, target, dot string, serveArgs []string, stdin []byte) ([]byte, error) {
	remote := "exec " + shellQuote(dot)
	for _, arg := range serveArgs {
		remote += " " + shellQuote(arg)
	}
	cmd := osexec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", target, remote)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

// shellQuote single-quotes one shell word. The serve args are constants
// except --remote-db, which comes from the operator's sync config.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ServeOp handles one --serve invocation against the local database: read
// the request, run the op, write the response. It is the remote end of
// SSHTransport and never prints anything but the response envelope. Read
// ops open the store read-only so a probe can never create an empty
// claude-mem.db; import is the only writer. home is the caller's home
// directory; an import that lands rows kicks the worker configured under
// it, and an empty home skips the kick.
func ServeOp(ctx context.Context, dbPath, home, op string, stdin io.Reader, stdout io.Writer) error {
	var db *SyncDB
	var err error
	if op == "import" {
		db, err = OpenSyncDB(dbPath)
	} else {
		db, err = OpenSyncDBReadOnly(dbPath)
	}
	if err != nil {
		return writeServeResponse(stdout, serveResponse{Error: err.Error()})
	}
	defer db.Close()
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return writeServeResponse(stdout, serveResponse{Error: err.Error()})
	}
	var req serveRequest
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return writeServeResponse(stdout, serveResponse{Error: "bad request: " + err.Error()})
		}
	}
	resp := serveResponse{}
	switch op {
	case "max":
		if resp.Max, err = db.MaxCreatedAt(); err != nil {
			resp.Error = err.Error()
		}
	case "export":
		if resp.Bundle, err = db.Export(req.Cutoffs, req.OnlySessions); err != nil {
			resp.Error = err.Error()
		}
	case "import":
		if req.Bundle == nil {
			resp.Error = "import request carries no bundle"
			break
		}
		if resp.Result, err = db.Import(req.Bundle); err != nil {
			resp.Error = err.Error()
			break
		}
		// Backfill kick on the importing side whenever content rows landed.
		// Settings live under the caller's home regardless of any custom
		// --remote-db path.
		if home != "" && resp.Result.NeedsBackfill() {
			_ = KickWorkerRestart(ctx, home, nil)
		}
	case "count":
		if resp.Counts, err = db.Counts(); err != nil {
			resp.Error = err.Error()
		}
	default:
		resp.Error = fmt.Sprintf("unknown serve op %q", op)
	}
	return writeServeResponse(stdout, resp)
}

func writeServeResponse(w io.Writer, resp serveResponse) error {
	return json.NewEncoder(w).Encode(resp)
}

// SyncOptions selects the direction and scope of one run.
type SyncOptions struct {
	Full         bool   // disable the incremental cutoff
	OnlySessions bool   // sessions-mode pass: all sessions, no content rows
	Direction    string // push | pull | sync
}

// SyncReport is one run's outcome, persisted for the status row.
type SyncReport struct {
	Direction   string        `json:"direction"`
	Pushed      *ImportResult `json:"pushed,omitempty"`
	Pulled      *ImportResult `json:"pulled,omitempty"`
	LocalCounts *TableCounts  `json:"local_counts,omitempty"`
	PeerCounts  *TableCounts  `json:"peer_counts,omitempty"`
}

// receiverCutoffs reduces the max-op answer to per-table export cutoffs.
// Cutoffs are per table by design: with a single shared cutoff, a table
// that stops advancing for longer than the overlap window would strand
// every gap row the receiver is missing (dedupe can only absorb rows the
// wire actually offers).
func receiverCutoffs(maxes map[string]string, full bool, now time.Time) (map[string]string, error) {
	if full {
		return nil, nil
	}
	out := map[string]string{}
	for _, table := range []string{"observations", "session_summaries"} {
		cutoff, err := CutoffFor(maxes[table], now)
		if err != nil {
			return nil, err
		}
		out[table] = cutoff
	}
	return out, nil
}

// RunMemorySync executes one sync: push (local → peer), pull (peer →
// local), or both. onlySessions exports every session both ways with no
// content rows and no cutoff.
func RunMemorySync(ctx context.Context, local *SyncDB, t SyncTransport, peer SyncPeer, opts SyncOptions, now time.Time) (*SyncReport, error) {
	report := &SyncReport{Direction: opts.Direction}
	push := opts.Direction == "push" || opts.Direction == "sync"
	pull := opts.Direction == "pull" || opts.Direction == "sync"
	if opts.OnlySessions {
		push, pull = true, true
	}
	if !push && !pull {
		return nil, fmt.Errorf("unknown sync direction %q", opts.Direction)
	}

	if push {
		maxes, err := t.Max(ctx, peer)
		if err != nil {
			return nil, err
		}
		cutoffs, err := receiverCutoffs(maxes, opts.Full || opts.OnlySessions, now)
		if err != nil {
			return nil, err
		}
		bundle, err := local.Export(cutoffs, opts.OnlySessions)
		if err != nil {
			return nil, err
		}
		if report.Pushed, err = t.Import(ctx, peer, bundle); err != nil {
			return nil, err
		}
	}
	if pull {
		maxes, err := local.MaxCreatedAt()
		if err != nil {
			return nil, err
		}
		cutoffs, err := receiverCutoffs(maxes, opts.Full || opts.OnlySessions, now)
		if err != nil {
			return nil, err
		}
		bundle, err := t.Export(ctx, peer, cutoffs, opts.OnlySessions)
		if err != nil {
			return nil, err
		}
		if report.Pulled, err = local.Import(bundle); err != nil {
			return nil, err
		}
	}

	// Final counts are best-effort: a peer that drops after the import must
	// not turn a completed sync into a reported failure (the rows landed;
	// the counts are the status row's convenience, not the result).
	var err error
	if report.LocalCounts, err = local.Counts(); err != nil {
		report.LocalCounts = nil
	}
	if report.PeerCounts, err = t.Counts(ctx, peer); err != nil {
		report.PeerCounts = nil
	}
	return report, nil
}

// SyncStateEntry is one peer's last-run record for the status row.
type SyncStateEntry struct {
	LastRun    time.Time `json:"last_run"`
	LastResult string    `json:"last_result"`
}

// SyncStatePath is the sync bookkeeping file beside the DB.
func SyncStatePath(homeDir string) string {
	return filepath.Join(homeDir, ".claude-mem", "sync-state.json")
}

// LoadSyncState reads the per-peer last-run records; a missing file is empty.
func LoadSyncState(path string) (map[string]SyncStateEntry, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]SyncStateEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Peers map[string]SyncStateEntry `json:"peers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing sync state %s: %w", path, err)
	}
	if doc.Peers == nil {
		doc.Peers = map[string]SyncStateEntry{}
	}
	return doc.Peers, nil
}

// SaveSyncState records one peer's run, preserving the others. The write is
// atomic so a concurrent scheduled/manual run can lose at most a last-run
// record — never leave a half-written file for `dot ai memory status`.
func SaveSyncState(path, peer string, entry SyncStateEntry) error {
	peers, err := LoadSyncState(path)
	if err != nil {
		return err
	}
	peers[peer] = entry
	doc := struct {
		Peers map[string]SyncStateEntry `json:"peers"`
	}{Peers: peers}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWriteFile(path, raw, 0o644)
}

// Summarize renders a one-line last-result for the status row.
func (r *SyncReport) Summarize() string {
	var parts []string
	if r.Pushed != nil {
		parts = append(parts, fmt.Sprintf("pushed +%d obs +%d sums +%d sessions", r.Pushed.Obs, r.Pushed.Sums, r.Pushed.Sessions))
	}
	if r.Pulled != nil {
		parts = append(parts, fmt.Sprintf("pulled +%d obs +%d sums +%d sessions", r.Pulled.Obs, r.Pulled.Sums, r.Pulled.Sessions))
	}
	for _, result := range []*ImportResult{r.Pushed, r.Pulled} {
		if result == nil || result.RelinkedObservations+result.RelinkedSummaries == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("relinked %d obs + %d summaries", result.RelinkedObservations, result.RelinkedSummaries))
	}
	if len(parts) == 0 {
		return "no rows transferred"
	}
	return strings.Join(parts, "; ")
}
