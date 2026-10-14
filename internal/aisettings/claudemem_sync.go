package aisettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // cgo-free SQLite driver (database/sql name "sqlite")
)

// claude-mem cross-machine sync. The wire semantics mirror the
// _meta/scripts/claude-mem-sync bash script so the two can run in parallel
// during rollout: incremental export off the receiver's MAX(created_at)
// minus an overlap window, natural-key dedupe, column intersection across
// plugin version skew, and one-off attribution repair on previously copied
// rows.

// SyncPeer names the remote side of a sync run.
type SyncPeer struct {
	Target   string `json:"target"`              // ssh target, e.g. user@host
	RemoteDB string `json:"remote_db,omitempty"` // remote DB path; empty = peer default
	// RemoteDot pins the peer's dot, from the peer profile's remote_dot when
	// the target is that profile's host; empty picks the newest release.
	RemoteDot string `json:"-"`
}

// Bundle is the unit of replication: the rows one side offers the other,
// plus the cutoff they were exported at (informational).
type Bundle struct {
	Observations []map[string]any `json:"observations,omitempty"`
	Summaries    []map[string]any `json:"summaries,omitempty"`
	Sessions     []map[string]any `json:"sessions,omitempty"`
	Cutoff       string           `json:"cutoff"`
}

// ImportResult counts rows added and references relinked by one import.
// Skipped names the columns the bundle carried but the receiver lacks (plugin
// version skew); the caller logs each once.
type ImportResult struct {
	Obs                  int      `json:"obs"`
	Sums                 int      `json:"sums"`
	Sessions             int      `json:"sessions"`
	Repaired             int      `json:"repaired"`
	RelinkedObservations int      `json:"relinked_observations,omitempty"`
	RelinkedSummaries    int      `json:"relinked_summaries,omitempty"`
	Skipped              []string `json:"skipped,omitempty"`
}

// TableCounts is the serve `count` op and the status row's per-side view.
type TableCounts struct {
	Obs               int `json:"obs"`
	Sums              int `json:"sums"`
	Sessions          int `json:"sessions"`
	ObsWithoutSession int `json:"obs_without_session"`
}

// overlapWindow keeps the incremental cutoff deliberately behind the
// receiver's newest row: rows created while a previous sync was mid-flight
// (or backdated) are re-offered and absorbed by dedupe instead of lost.
const overlapWindow = 7 * 24 * time.Hour

// syncSkipColumns never crosses the wire: identity and local-bookkeeping
// columns. id is receiver-assigned; the rest are this machine's own sync
// metadata, worker binding, and usage counters.
var syncSkipColumns = map[string]bool{
	"id":               true,
	"synced_at":        true,
	"origin_device_id": true,
	"origin_local_id":  true,
	"sync_rev":         true,
	"relevance_count":  true,
	"worker_port":      true,
}

// repairColumns are the attribution fields a one-off backfill fixes on rows
// an earlier copy brought over without them.
var repairColumns = []string{"agent_type", "agent_id", "metadata", "content_hash", "generated_by_model"}

// SyncDB wraps a claude-mem SQLite database.
type SyncDB struct {
	db   *sql.DB
	Path string
}

// OpenSyncDB opens a claude-mem DB with a busy timeout so a live worker's
// WAL writes never fail a read outright. Journal mode is deliberately left
// alone: the worker owns it, and busy_timeout covers the contention.
func OpenSyncDB(path string) (*SyncDB, error) {
	return openSyncDB(path, "")
}

// OpenSyncDBReadOnly opens the store for the read paths (status, serve
// max/export/count). SQLite's default mode creates a missing file, which
// would make a status probe conjure an empty claude-mem.db that later
// passes for a real (empty) store.
func OpenSyncDBReadOnly(path string) (*SyncDB, error) {
	return openSyncDB(path, "ro")
}

func openSyncDB(path, mode string) (*SyncDB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path)
	if mode != "" {
		dsn += "&mode=" + mode
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return &SyncDB{db: db, Path: path}, nil
}

// Close releases the database handle.
func (d *SyncDB) Close() error { return d.db.Close() }

// DefaultSyncDBPath is the claude-mem store location.
func DefaultSyncDBPath(homeDir string) string {
	return filepath.Join(homeDir, ".claude-mem", "claude-mem.db")
}

// MaxCreatedAt is the serve `max` op: the receiver's newest created_at per
// replicating table ("" when the table is empty).
func (d *SyncDB) MaxCreatedAt() (map[string]string, error) {
	out := map[string]string{}
	for _, table := range []string{"observations", "session_summaries"} {
		var max sql.NullString
		if err := d.db.QueryRow("SELECT MAX(created_at) FROM " + table).Scan(&max); err != nil {
			return nil, fmt.Errorf("max created_at on %s: %w", table, err)
		}
		if max.Valid {
			out[table] = max.String
		}
	}
	return out, nil
}

// Counts is the serve `count` op.
func (d *SyncDB) Counts() (*TableCounts, error) {
	var c TableCounts
	queries := []struct {
		dst  *int
		stmt string
	}{
		{&c.Obs, "SELECT COUNT(*) FROM observations"},
		{&c.Sums, "SELECT COUNT(*) FROM session_summaries"},
		{&c.Sessions, "SELECT COUNT(*) FROM sdk_sessions"},
		{&c.ObsWithoutSession, `SELECT COUNT(*) FROM observations o
			LEFT JOIN sdk_sessions s ON o.memory_session_id = s.memory_session_id
			WHERE s.id IS NULL`},
	}
	for _, q := range queries {
		if err := d.db.QueryRow(q.stmt).Scan(q.dst); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// syncTimeFormat is the created_at wire format the claude-mem writer uses:
// RFC3339 with fixed milliseconds, so lexicographic comparison is
// chronological and cutoff arithmetic round-trips losslessly.
const syncTimeFormat = "2006-01-02T15:04:05.000Z"

// CutoffFor converts a receiver max into the export cutoff: the overlap
// window behind it. Empty max means "no history", and the cutoff
// stays empty, which exports everything (same as --full for that table).
func CutoffFor(receiverMax string, now time.Time) (string, error) {
	if receiverMax == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, receiverMax)
	if err != nil {
		return "", fmt.Errorf("receiver max created_at %q is not RFC3339: %w", receiverMax, err)
	}
	cutoff := t.Add(-overlapWindow)
	if cutoff.After(now) {
		cutoff = now
	}
	return cutoff.UTC().Format(syncTimeFormat), nil
}

// Export is the serve `export` op. cutoffs is a per-table created_at lower
// bound (exclusive); a missing/empty table entry exports that whole table.
// Per-table cutoffs matter: when one table races ahead of the other, a
// single shared cutoff would silently strand the lagging table's gap rows.
// Sessions ride along when they are newer than the oldest cutoff OR back an
// exported observation/summary. onlySessions is the sessions-mode pass:
// every session, no content rows.
func (d *SyncDB) Export(cutoffs map[string]string, onlySessions bool) (*Bundle, error) {
	b := &Bundle{Cutoff: oldestCutoff(cutoffs)}
	var err error
	if onlySessions {
		b.Sessions, err = d.selectRows("SELECT * FROM sdk_sessions")
		if err == nil {
			stripSkipColumns(b.Sessions)
		}
		return b, err
	}
	obsWhere, obsArgs := cutoffWhere(cutoffs["observations"])
	if b.Observations, err = d.selectRows("SELECT * FROM observations"+obsWhere, obsArgs...); err != nil {
		return nil, err
	}
	sumWhere, sumArgs := cutoffWhere(cutoffs["session_summaries"])
	if b.Summaries, err = d.selectRows("SELECT * FROM session_summaries"+sumWhere, sumArgs...); err != nil {
		return nil, err
	}
	sessionSQL := `SELECT * FROM sdk_sessions WHERE memory_session_id IN (
		SELECT memory_session_id FROM observations` + obsWhere + `
		UNION SELECT memory_session_id FROM session_summaries` + sumWhere + `)`
	sessionArgs := append(append([]any{}, obsArgs...), sumArgs...)
	if oldest := oldestCutoff(cutoffs); oldest != "" {
		sessionSQL += " OR started_at > ?"
		sessionArgs = append(sessionArgs, oldest)
	}
	if b.Sessions, err = d.selectRows(sessionSQL, sessionArgs...); err != nil {
		return nil, err
	}
	// The skip set must not cross the wire either: id is receiver-assigned
	// and the rest are this machine's own sync metadata, worker binding, and
	// usage counters. The import-side intersection stays as the second line
	// of defense for bundles from older exporters.
	for _, rows := range [][]map[string]any{b.Observations, b.Summaries, b.Sessions} {
		stripSkipColumns(rows)
	}
	return b, nil
}

func stripSkipColumns(rows []map[string]any) {
	for _, row := range rows {
		for col := range syncSkipColumns {
			delete(row, col)
		}
	}
}

// cutoffWhere renders the WHERE clause for one table's cutoff ("" → no
// clause, whole table).
func cutoffWhere(cutoff string) (string, []any) {
	if cutoff == "" {
		return "", nil
	}
	return " WHERE created_at > ?", []any{cutoff}
}

// oldestCutoff is the earliest bound in play: sessions older than every
// content cutoff are still offered when they back nothing, since started_at
// newer than the oldest cutoff keeps them on the wire.
func oldestCutoff(cutoffs map[string]string) string {
	oldest := ""
	for _, c := range cutoffs {
		if c == "" {
			return "" // a full table export makes session age checks pointless
		}
		if oldest == "" || c < oldest {
			oldest = c
		}
	}
	return oldest
}

// selectRows runs a query and returns every row as a column→value map.
// []byte values are normalized to strings so a Bundle is JSON-clean.
func (d *SyncDB) selectRows(query string, args ...any) ([]map[string]any, error) {
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, col := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = vals[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// tableColumns is the receiver-side column set for the import intersection.
func (d *SyncDB) tableColumns(table string) (map[string]bool, error) {
	rows, err := d.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// intersect keeps the columns present on BOTH sides, minus the skip set.
// skewed (bundle-only) columns come back sorted so the caller logs each once.
func intersect(row map[string]any, receiverCols map[string]bool) (keep []string, skewed []string) {
	for col := range row {
		switch {
		case syncSkipColumns[col]:
		case !receiverCols[col]:
			skewed = append(skewed, col)
		default:
			keep = append(keep, col)
		}
	}
	sort.Strings(keep)
	sort.Strings(skewed)
	return keep, skewed
}

// Import is the serve `import` op: sessions first (content rows reference
// them), then observations, then summaries, in one transaction.
func (d *SyncDB) Import(b *Bundle) (*ImportResult, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	res := &ImportResult{}
	sessionIDMap := map[string]string{}
	skewed := map[string]bool{}
	logSkew := func(cols []string) {
		for _, c := range cols {
			skewed[c] = true
		}
	}

	sessionCols, err := d.tableColumns("sdk_sessions")
	if err != nil {
		return nil, err
	}
	for _, row := range b.Sessions {
		canonicalID, added, err := importSession(tx, sessionCols, row, logSkew)
		if err != nil {
			return nil, fmt.Errorf("importing sdk_sessions: %w", err)
		}
		if added {
			res.Sessions++
		}
		incomingID := rowString(row, "memory_session_id")
		if incomingID != "" && canonicalID != "" && incomingID != canonicalID {
			if previous, exists := sessionIDMap[incomingID]; exists && previous != canonicalID {
				return nil, fmt.Errorf("incoming memory session %q maps to multiple receiver sessions", incomingID)
			}
			sessionIDMap[incomingID] = canonicalID
		}
	}
	for from, to := range sessionIDMap {
		result, err := tx.Exec("UPDATE observations SET memory_session_id = ? WHERE memory_session_id = ?", to, from)
		if err != nil {
			return nil, fmt.Errorf("relinking observations for session %q: %w", from, err)
		}
		if n, err := result.RowsAffected(); err != nil {
			return nil, fmt.Errorf("counting relinked observations for session %q: %w", from, err)
		} else {
			res.RelinkedObservations += int(n)
		}
		result, err = tx.Exec("UPDATE session_summaries SET memory_session_id = ? WHERE memory_session_id = ?", to, from)
		if err != nil {
			return nil, fmt.Errorf("relinking summaries for session %q: %w", from, err)
		}
		if n, err := result.RowsAffected(); err != nil {
			return nil, fmt.Errorf("counting relinked summaries for session %q: %w", from, err)
		} else {
			res.RelinkedSummaries += int(n)
		}
	}
	obsCols, err := d.tableColumns("observations")
	if err != nil {
		return nil, err
	}
	for _, row := range b.Observations {
		mapped := remapSessionRow(row, sessionIDMap)
		added, repaired, err := importObservation(tx, obsCols, mapped, logSkew)
		if err != nil {
			return nil, fmt.Errorf("importing observations: %w", err)
		}
		if added {
			res.Obs++
		}
		res.Repaired += repaired
	}
	sumCols, err := d.tableColumns("session_summaries")
	if err != nil {
		return nil, err
	}
	for _, row := range b.Summaries {
		mapped := remapSessionRow(row, sessionIDMap)
		added, err := importDeduped(tx, sumCols, "session_summaries", "request", mapped, logSkew)
		if err != nil {
			return nil, fmt.Errorf("importing session_summaries: %w", err)
		}
		if added {
			res.Sums++
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing import: %w", err)
	}
	for col := range skewed {
		res.Skipped = append(res.Skipped, col)
	}
	sort.Strings(res.Skipped)
	return res, nil
}

func rowString(row map[string]any, col string) string {
	if v, ok := row[col].(string); ok {
		return v
	}
	return ""
}

// dedupeExists is the natural-key lookup: (created_at, COALESCE(key,”)).
func dedupeExists(tx *sql.Tx, table, keyCol string, row map[string]any) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(1) FROM `+table+` WHERE created_at = ? AND COALESCE(`+keyCol+`, '') = ?`,
		rowString(row, "created_at"), rowString(row, keyCol)).Scan(&n)
	return n > 0, err
}

// insertRow inserts one row; inserted=false means a receiver-side unique
// index the natural key does not cover (e.g. ux_observations_session_hash)
// settled for "already here": the row is by definition not new.
func insertRow(tx *sql.Tx, table string, cols []string, row map[string]any) (bool, error) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	stmt := `INSERT INTO ` + table + ` (` + strings.Join(cols, ", ") + `) VALUES (` + marks + `)`
	args := make([]any, len(cols))
	for i, col := range cols {
		args[i] = row[col]
	}
	_, err := tx.Exec(stmt, args...)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return false, nil
	}
	return err == nil, err
}

func importSession(tx *sql.Tx, receiverCols map[string]bool, row map[string]any, logSkew func([]string)) (string, bool, error) {
	incomingID := rowString(row, "memory_session_id")
	incomingPlatform := rowString(row, "platform_source")
	incomingContentID := rowString(row, "content_session_id")
	if incomingID != "" {
		var platform, contentID sql.NullString
		err := tx.QueryRow(`SELECT platform_source, content_session_id FROM sdk_sessions WHERE memory_session_id = ?`, incomingID).Scan(&platform, &contentID)
		if err == nil {
			if incomingPlatform == "" || incomingContentID == "" {
				return "", false, fmt.Errorf("memory session %q has an incomplete platform/content identity", incomingID)
			}
			if !platform.Valid || !contentID.Valid || platform.String != incomingPlatform || contentID.String != incomingContentID {
				return "", false, fmt.Errorf("memory session %q already exists with a different platform/content identity", incomingID)
			}
			return incomingID, false, nil
		}
		if err != sql.ErrNoRows {
			return "", false, err
		}
	}

	// Only a complete identity can establish equivalence. Blank or NULL fields
	// are deliberately left unmapped because they cannot safely identify a session.
	if incomingPlatform != "" && incomingContentID != "" {
		rows, err := tx.Query(`SELECT memory_session_id FROM sdk_sessions
			WHERE platform_source = ? AND content_session_id = ?`, incomingPlatform, incomingContentID)
		if err != nil {
			return "", false, err
		}
		var matches []string
		for rows.Next() {
			var id sql.NullString
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return "", false, err
			}
			if id.Valid && id.String != "" {
				matches = append(matches, id.String)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", false, err
		}
		if err := rows.Close(); err != nil {
			return "", false, err
		}
		if len(matches) > 1 {
			return "", false, fmt.Errorf("platform/content identity matches multiple receiver sessions")
		}
		if len(matches) == 1 {
			return matches[0], false, nil
		}
	}

	keep, skewed := intersect(row, receiverCols)
	logSkew(skewed)
	added, err := insertRowStrict(tx, "sdk_sessions", keep, row)
	if err != nil {
		return "", false, err
	}
	if incomingID == "" {
		return "", added, nil
	}
	return incomingID, added, nil
}

// insertRowStrict is used for backing sessions: silently treating a unique
// collision as a duplicate could leave incoming content pointing at no session.
func insertRowStrict(tx *sql.Tx, table string, cols []string, row map[string]any) (bool, error) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	stmt := `INSERT INTO ` + table + ` (` + strings.Join(cols, ", ") + `) VALUES (` + marks + `)`
	args := make([]any, len(cols))
	for i, col := range cols {
		args[i] = row[col]
	}
	result, err := tx.Exec(stmt, args...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func remapSessionRow(row map[string]any, sessionIDMap map[string]string) map[string]any {
	from := rowString(row, "memory_session_id")
	to := sessionIDMap[from]
	if from == "" || to == "" || from == to {
		return row
	}
	remapped := make(map[string]any, len(row))
	for col, value := range row {
		remapped[col] = value
	}
	remapped["memory_session_id"] = to
	return remapped
}

// importObservation dedupes on (created_at, title); a dedupe hit runs the
// one-off attribution repair, backfilling repairColumns the local row lacks
// when the incoming bundle carries them.
func importObservation(tx *sql.Tx, receiverCols map[string]bool, row map[string]any, logSkew func([]string)) (bool, int, error) {
	exists, err := dedupeExists(tx, "observations", "title", row)
	if err != nil {
		return false, 0, err
	}
	if !exists {
		keep, skewed := intersect(row, receiverCols)
		logSkew(skewed)
		added, err := insertRow(tx, "observations", keep, row)
		return added, 0, err
	}
	repaired, err := repairAttribution(tx, receiverCols, row)
	return false, repaired, err
}

func repairAttribution(tx *sql.Tx, receiverCols map[string]bool, row map[string]any) (int, error) {
	repaired := 0
	for _, col := range repairColumns {
		incoming := rowString(row, col)
		if incoming == "" || !receiverCols[col] {
			continue
		}
		r, err := tx.Exec(`UPDATE observations SET `+col+` = ?
			WHERE created_at = ? AND COALESCE(title, '') = ? AND (`+col+` IS NULL OR `+col+` = '')`,
			incoming, rowString(row, "created_at"), rowString(row, "title"))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				continue // content_hash would collide with the session-hash index
			}
			return repaired, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			repaired += int(n)
		}
	}
	return repaired, nil
}

func importDeduped(tx *sql.Tx, receiverCols map[string]bool, table, keyCol string, row map[string]any, logSkew func([]string)) (bool, error) {
	exists, err := dedupeExists(tx, table, keyCol, row)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	keep, skewed := intersect(row, receiverCols)
	logSkew(skewed)
	return insertRow(tx, table, keep, row)
}

// KickWorkerRestart asks the local claude-mem worker to rebuild its search
// index after an import added rows. Best-effort: an absent settings file,
// an unset port, and a worker that is not running all silently skip.
func KickWorkerRestart(ctx context.Context, homeDir string, client *http.Client) error {
	raw, err := os.ReadFile(filepath.Join(homeDir, ".claude-mem", "settings.json"))
	if err != nil {
		return nil
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(raw, &settings) != nil {
		return nil
	}
	var port string
	if json.Unmarshal(settings["CLAUDE_MEM_WORKER_PORT"], &port) != nil || port == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+port+"/api/admin/restart", nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil // worker not running: nothing to kick
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
