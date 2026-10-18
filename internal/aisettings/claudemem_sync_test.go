package aisettings

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
	"time"
)

// syncSchemaDDL mirrors the real 13.28 claude-mem schema for the three
// replicated tables (FTS triggers excluded: the fixture has no FTS tables).
const syncSchemaDDL = `
CREATE TABLE sdk_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content_session_id TEXT NOT NULL,
    memory_session_id TEXT UNIQUE,
    project TEXT NOT NULL,
    platform_source TEXT NOT NULL DEFAULT 'claude',
    user_prompt TEXT,
    started_at TEXT NOT NULL,
    started_at_epoch INTEGER NOT NULL,
    completed_at TEXT,
    completed_at_epoch INTEGER,
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'completed', 'failed')),
    worker_port INTEGER,
    prompt_counter INTEGER DEFAULT 0,
    custom_title TEXT,
    observed_model TEXT,
    observed_billing TEXT
);
CREATE UNIQUE INDEX ux_sdk_sessions_platform_content ON sdk_sessions(platform_source, content_session_id);

CREATE TABLE observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_session_id TEXT NOT NULL,
    project TEXT NOT NULL,
    text TEXT,
    type TEXT NOT NULL,
    title TEXT,
    subtitle TEXT,
    facts TEXT,
    narrative TEXT,
    concepts TEXT,
    files_read TEXT,
    files_modified TEXT,
    prompt_number INTEGER,
    discovery_tokens INTEGER DEFAULT 0,
    created_at TEXT NOT NULL,
    created_at_epoch INTEGER NOT NULL,
    content_hash TEXT,
    generated_by_model TEXT,
    relevance_count INTEGER DEFAULT 0,
    merged_into_project TEXT,
    agent_type TEXT,
    agent_id TEXT,
    metadata TEXT,
    synced_at INTEGER,
    origin_device_id TEXT,
    origin_local_id TEXT,
    sync_rev TEXT NOT NULL DEFAULT '1'
);
CREATE UNIQUE INDEX ux_observations_session_hash ON observations(memory_session_id, content_hash);
CREATE UNIQUE INDEX ux_observations_origin ON observations(origin_device_id, origin_local_id) WHERE origin_device_id IS NOT NULL;

CREATE TABLE session_summaries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_session_id TEXT NOT NULL,
    project TEXT NOT NULL,
    request TEXT,
    investigated TEXT,
    learned TEXT,
    completed TEXT,
    next_steps TEXT,
    files_read TEXT,
    files_edited TEXT,
    notes TEXT,
    prompt_number INTEGER,
    discovery_tokens INTEGER DEFAULT 0,
    created_at TEXT NOT NULL,
    created_at_epoch INTEGER NOT NULL,
    merged_into_project TEXT,
    synced_at INTEGER,
    origin_device_id TEXT,
    origin_local_id TEXT,
    sync_rev TEXT NOT NULL DEFAULT '1'
);
`

// newTestSyncDB creates a temp DB with the sync schema; extraDDL mutates it
// for the skew cases (e.g. "ALTER TABLE observations DROP COLUMN metadata").
func newTestSyncDB(t *testing.T, extraDDL ...string) *SyncDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-mem.db")
	db, err := OpenSyncDB(path)
	if err != nil {
		t.Fatalf("OpenSyncDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.db.Exec(syncSchemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, ddl := range extraDDL {
		if _, err := db.db.Exec(ddl); err != nil {
			t.Fatalf("extra DDL %q: %v", ddl, err)
		}
	}
	return db
}

func execIns(t *testing.T, db *SyncDB, stmt string, args ...any) {
	t.Helper()
	if _, err := db.db.Exec(stmt, args...); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func seedSession(t *testing.T, db *SyncDB, contentID, memID, startedAt string) {
	t.Helper()
	execIns(t, db, `INSERT INTO sdk_sessions (content_session_id, memory_session_id, project, platform_source, started_at, started_at_epoch, status)
		VALUES (?, ?, 'proj', 'claude', ?, 0, 'active')`, contentID, memID, startedAt)
}

func seedObs(t *testing.T, db *SyncDB, memID, createdAt, title string) {
	t.Helper()
	execIns(t, db, `INSERT INTO observations (memory_session_id, project, type, title, created_at, created_at_epoch)
		VALUES (?, 'proj', 'discovery', ?, ?, 0)`, memID, title, createdAt)
}

func seedSummary(t *testing.T, db *SyncDB, memID, createdAt, request string) {
	t.Helper()
	execIns(t, db, `INSERT INTO session_summaries (memory_session_id, project, request, created_at, created_at_epoch)
		VALUES (?, 'proj', ?, ?, 0)`, memID, request, createdAt)
}

// TestSyncRoundTrip seeds a sender, exports full, imports into an empty
// receiver, and expects content parity including the attribution columns
// and the backing sdk_sessions row.
func TestSyncRoundTrip(t *testing.T) {
	sender := newTestSyncDB(t)
	receiver := newTestSyncDB(t)

	seedSession(t, sender, "content-1", "mem-1", "2026-09-20T10:00:00.000Z")
	execIns(t, sender, `INSERT INTO observations (memory_session_id, project, type, title, text, created_at, created_at_epoch,
		agent_type, agent_id, metadata, content_hash, generated_by_model, synced_at, origin_device_id, origin_local_id, relevance_count)
		VALUES ('mem-1', 'proj', 'discovery', 'T1', 'body', '2026-09-20T10:01:00.000Z', 0,
		'claude', 'agent-7', '{"k":"v"}', 'hash-1', 'haiku', 12345, 'macbook', 42, 9)`)
	seedSummary(t, sender, "mem-1", "2026-09-20T10:02:00.000Z", "req-1")

	bundle, err := sender.Export(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(bundle.Observations) != 1 || len(bundle.Summaries) != 1 || len(bundle.Sessions) != 1 {
		t.Fatalf("bundle = %d obs %d sums %d sessions", len(bundle.Observations), len(bundle.Summaries), len(bundle.Sessions))
	}
	// The skip set must not cross the wire at all (id is receiver-assigned,
	// the rest is sender-local bookkeeping).
	for _, col := range []string{"id", "synced_at", "origin_device_id", "origin_local_id", "sync_rev", "relevance_count", "worker_port"} {
		if _, ok := bundle.Observations[0][col]; ok {
			t.Errorf("bundle carries skip column %q", col)
		}
	}
	res, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Obs != 1 || res.Sums != 1 || res.Sessions != 1 {
		t.Fatalf("result = %#v", res)
	}

	// The acceptance row: peer-origin observation arrives with its
	// attribution and its sdk_sessions backing row.
	var agentType, agentID, metadata, contentHash, model string
	var syncedAt any
	var origin any
	err = receiver.db.QueryRow(`SELECT agent_type, agent_id, metadata, content_hash, generated_by_model, synced_at, origin_device_id
		FROM observations WHERE title = 'T1'`).Scan(&agentType, &agentID, &metadata, &contentHash, &model, &syncedAt, &origin)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if agentType != "claude" || agentID != "agent-7" || metadata != `{"k":"v"}` || contentHash != "hash-1" || model != "haiku" {
		t.Errorf("attribution lost: %q %q %q %q %q", agentType, agentID, metadata, contentHash, model)
	}
	if syncedAt != nil || origin != nil {
		t.Errorf("sync bookkeeping crossed the wire: synced_at=%v origin=%v", syncedAt, origin)
	}
	var sid string
	if err := receiver.db.QueryRow(`SELECT memory_session_id FROM sdk_sessions WHERE content_session_id = 'content-1'`).Scan(&sid); err != nil {
		t.Fatalf("backing session missing: %v", err)
	}
	if sid != "mem-1" {
		t.Errorf("session id = %q", sid)
	}

	// Idempotency: a second import of the same bundle adds nothing.
	res2, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if res2.Obs != 0 || res2.Sums != 0 || res2.Sessions != 0 {
		t.Fatalf("second import = %#v, want all zero", res2)
	}
}

func equivalentSessionRow(memID, contentID, platform string) map[string]any {
	return map[string]any{
		"memory_session_id": memID, "content_session_id": contentID,
		"platform_source": platform, "project": "proj",
		"started_at": "2026-09-20T10:00:00.000Z", "started_at_epoch": 0,
		"status": "active",
	}
}

func TestImportSessionsRelinksExistingReferences(t *testing.T) {
	receiver := newTestSyncDB(t)
	seedSession(t, receiver, "content-1", "receiver-id", "2026-09-20T10:00:00.000Z")
	seedObs(t, receiver, "incoming-id", "2026-09-20T10:01:00.000Z", "orphan observation")
	seedSummary(t, receiver, "incoming-id", "2026-09-20T10:02:00.000Z", "orphan summary")
	bundle := &Bundle{Sessions: []map[string]any{equivalentSessionRow("incoming-id", "content-1", "claude")}}
	before, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}

	result, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatalf("Import sessions: %v", err)
	}
	if result.Sessions != 0 || result.RelinkedObservations != 1 || result.RelinkedSummaries != 1 {
		t.Fatalf("result = %#v, want one relink per content table and no new session", result)
	}
	for _, table := range []string{"observations", "session_summaries"} {
		var got string
		if err := receiver.db.QueryRow("SELECT memory_session_id FROM " + table).Scan(&got); err != nil || got != "receiver-id" {
			t.Fatalf("%s memory_session_id = %q, err = %v", table, got, err)
		}
	}
	var sessionCount int
	if err := receiver.db.QueryRow("SELECT COUNT(*) FROM sdk_sessions").Scan(&sessionCount); err != nil || sessionCount != 1 {
		t.Fatalf("sdk_sessions count = %d, err = %v", sessionCount, err)
	}
	unknown := "unknown-history"
	seedObs(t, receiver, unknown, "2026-09-20T10:03:00.000Z", "unknown observation")
	result, err = receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatalf("second Import sessions: %v", err)
	}
	if result.RelinkedObservations != 0 || result.RelinkedSummaries != 0 {
		t.Fatalf("second import relink counts = %#v, want zero", result)
	}
	var unknownID string
	if err := receiver.db.QueryRow("SELECT memory_session_id FROM observations WHERE title = 'unknown observation'").Scan(&unknownID); err != nil || unknownID != unknown {
		t.Fatalf("unknown reference changed to %q, err = %v", unknownID, err)
	}
	after, err := json.Marshal(bundle)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("Import mutated caller bundle: err=%v", err)
	}
}

func TestImportMapsIncomingContentToEquivalentSession(t *testing.T) {
	receiver := newTestSyncDB(t)
	seedSession(t, receiver, "content-1", "receiver-id", "2026-09-20T10:00:00.000Z")
	observation := map[string]any{
		"memory_session_id": "incoming-id", "project": "proj", "type": "discovery",
		"title": "incoming observation", "created_at": "2026-09-20T10:01:00.000Z",
		"created_at_epoch": 0, "content_hash": "incoming-hash",
	}
	summary := map[string]any{
		"memory_session_id": "incoming-id", "project": "proj", "request": "incoming summary",
		"created_at": "2026-09-20T10:02:00.000Z", "created_at_epoch": 0,
	}
	bundle := &Bundle{
		Sessions:     []map[string]any{equivalentSessionRow("incoming-id", "content-1", "claude")},
		Observations: []map[string]any{observation},
		Summaries:    []map[string]any{summary},
	}
	before, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	result, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Obs != 1 || result.Sums != 1 || result.Sessions != 0 {
		t.Fatalf("result = %#v", result)
	}
	for _, table := range []string{"observations", "session_summaries"} {
		var got string
		if err := receiver.db.QueryRow("SELECT memory_session_id FROM " + table).Scan(&got); err != nil || got != "receiver-id" {
			t.Fatalf("%s memory_session_id = %q, err = %v", table, got, err)
		}
	}
	after, err := json.Marshal(bundle)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("Import mutated caller bundle: err=%v", err)
	}
	result, err = receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if result.Obs != 0 || result.Sums != 0 || result.RelinkedObservations != 0 || result.RelinkedSummaries != 0 {
		t.Fatalf("second import was not idempotent: %#v", result)
	}
}

func TestImportDoesNotMapBlankOrCrossPlatformIdentity(t *testing.T) {
	receiver := newTestSyncDB(t)
	seedSession(t, receiver, "content-1", "receiver-id", "2026-09-20T10:00:00.000Z")
	bundle := &Bundle{
		Sessions: []map[string]any{
			equivalentSessionRow("cross-platform-id", "content-1", "gemini"),
			equivalentSessionRow("blank-identity-id", "", ""),
		},
		Observations: []map[string]any{
			{"memory_session_id": "cross-platform-id", "project": "proj", "type": "discovery", "title": "cross-platform", "created_at": "2026-09-20T10:01:00.000Z", "created_at_epoch": 0},
			{"memory_session_id": "blank-identity-id", "project": "proj", "type": "discovery", "title": "blank-identity", "created_at": "2026-09-20T10:02:00.000Z", "created_at_epoch": 0},
		},
	}
	if _, err := receiver.Import(context.Background(), bundle); err != nil {
		t.Fatalf("Import: %v", err)
	}
	for title, want := range map[string]string{"cross-platform": "cross-platform-id", "blank-identity": "blank-identity-id"} {
		var got string
		if err := receiver.db.QueryRow("SELECT memory_session_id FROM observations WHERE title = ?", title).Scan(&got); err != nil || got != want {
			t.Errorf("%s memory_session_id = %q, err = %v", title, got, err)
		}
	}
}

func TestImportConflictingMemoryIdentityFails(t *testing.T) {
	receiver := newTestSyncDB(t)
	seedSession(t, receiver, "content-1", "existing-id", "2026-09-20T10:00:00.000Z")
	bundle := &Bundle{Sessions: []map[string]any{
		equivalentSessionRow("new-id", "content-2", "claude"),
		equivalentSessionRow("existing-id", "different-content", "claude"),
	}}
	if _, err := receiver.Import(context.Background(), bundle); err == nil {
		t.Fatal("Import accepted an existing memory ID with a different identity")
	}
	var count int
	if err := receiver.db.QueryRow("SELECT COUNT(*) FROM sdk_sessions WHERE memory_session_id = 'new-id'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("earlier session insert survived rollback: count=%d err=%v", count, err)
	}
	if _, err := receiver.Import(context.Background(), &Bundle{Sessions: []map[string]any{
		equivalentSessionRow("existing-id", "", ""),
	}}); err == nil {
		t.Fatal("Import accepted an incomplete identity for an existing memory ID")
	}
}

func TestImportAmbiguousSessionIdentityFails(t *testing.T) {
	receiver := newTestSyncDB(t, "DROP INDEX ux_sdk_sessions_platform_content")
	seedSession(t, receiver, "content-1", "receiver-one", "2026-09-20T10:00:00.000Z")
	seedSession(t, receiver, "content-1", "receiver-two", "2026-09-20T10:01:00.000Z")
	bundle := &Bundle{Sessions: []map[string]any{
		equivalentSessionRow("incoming-id", "content-1", "claude"),
	}}
	if _, err := receiver.Import(context.Background(), bundle); err == nil {
		t.Fatal("Import accepted an ambiguous platform/content identity")
	}
	var count int
	if err := receiver.db.QueryRow("SELECT COUNT(*) FROM sdk_sessions WHERE memory_session_id = 'incoming-id'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("ambiguous import inserted a session: count=%d err=%v", count, err)
	}
}

func TestImportRelinkUniqueConflictRollsBack(t *testing.T) {
	receiver := newTestSyncDB(t)
	seedSession(t, receiver, "content-1", "receiver-id", "2026-09-20T10:00:00.000Z")
	seedObs(t, receiver, "receiver-id", "2026-09-20T10:01:00.000Z", "canonical")
	execIns(t, receiver, "UPDATE observations SET content_hash = 'same-hash' WHERE title = 'canonical'")
	seedObs(t, receiver, "incoming-id", "2026-09-20T10:02:00.000Z", "orphan")
	execIns(t, receiver, "UPDATE observations SET content_hash = 'same-hash' WHERE title = 'orphan'")
	bundle := &Bundle{Sessions: []map[string]any{
		equivalentSessionRow("new-id", "content-2", "claude"),
		equivalentSessionRow("incoming-id", "content-1", "claude"),
	}}
	if _, err := receiver.Import(context.Background(), bundle); err == nil {
		t.Fatal("Import accepted a relink that violates the observation session/hash index")
	}
	var got string
	if err := receiver.db.QueryRow("SELECT memory_session_id FROM observations WHERE title = 'orphan'").Scan(&got); err != nil || got != "incoming-id" {
		t.Fatalf("failed relink changed orphan reference to %q, err=%v", got, err)
	}
	var count int
	if err := receiver.db.QueryRow("SELECT COUNT(*) FROM sdk_sessions WHERE memory_session_id = 'new-id'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("earlier session insert survived rollback: count=%d err=%v", count, err)
	}
}

func TestSyncReportSummarizesRelinkedReferences(t *testing.T) {
	report := &SyncReport{Direction: "sessions", Pulled: &ImportResult{RelinkedObservations: 73, RelinkedSummaries: 2}}
	if got, want := report.Summarize(), "relinked 73 obs + 2 summaries"; !strings.Contains(got, want) {
		t.Fatalf("Summarize() = %q, want it to include %q", got, want)
	}
}

// TestSyncSchemaSkew: a receiver on an older plugin (no metadata column)
// imports successfully, skips the column, and reports it once.
func TestSyncSchemaSkew(t *testing.T) {
	sender := newTestSyncDB(t)
	receiver := newTestSyncDB(t, "ALTER TABLE observations DROP COLUMN metadata")

	seedSession(t, sender, "c1", "m1", "2026-09-20T10:00:00.000Z")
	execIns(t, sender, `INSERT INTO observations (memory_session_id, project, type, title, created_at, created_at_epoch, metadata, agent_type)
		VALUES ('m1', 'proj', 'discovery', 'T1', '2026-09-20T10:01:00.000Z', 0, '{"k":"v"}', 'claude')`)

	bundle, err := sender.Export(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatalf("skewed Import must succeed: %v", err)
	}
	if res.Obs != 1 {
		t.Fatalf("result = %#v", res)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "metadata" {
		t.Fatalf("Skipped = %v, want [metadata] exactly once", res.Skipped)
	}
	var agentType string
	if err := receiver.db.QueryRow(`SELECT agent_type FROM observations`).Scan(&agentType); err != nil || agentType != "claude" {
		t.Fatalf("shared columns must land: %q %v", agentType, err)
	}
}

// TestSyncAttributionRepair: a row copied before the attribution columns
// existed gets them backfilled when the bundle carries them again.
func TestSyncAttributionRepair(t *testing.T) {
	sender := newTestSyncDB(t)
	receiver := newTestSyncDB(t)

	seedSession(t, sender, "c1", "m1", "2026-09-20T10:00:00.000Z")
	seedSession(t, receiver, "c1", "m1", "2026-09-20T10:00:00.000Z")
	// The previously copied row, lacking attribution.
	execIns(t, receiver, `INSERT INTO observations (memory_session_id, project, type, title, created_at, created_at_epoch)
		VALUES ('m1', 'proj', 'discovery', 'T1', '2026-09-20T10:01:00.000Z', 0)`)
	execIns(t, sender, `INSERT INTO observations (memory_session_id, project, type, title, created_at, created_at_epoch,
		agent_type, agent_id, metadata, content_hash, generated_by_model)
		VALUES ('m1', 'proj', 'discovery', 'T1', '2026-09-20T10:01:00.000Z', 0,
		'kimi', 'agent-9', '{"m":1}', 'hash-9', 'k2')`)

	bundle, err := sender.Export(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if res.Obs != 0 {
		t.Fatalf("dedupe must hold: %#v", res)
	}
	if res.Repaired != 5 {
		t.Fatalf("Repaired = %d, want 5 (one per attribution column)", res.Repaired)
	}
	var agentType, agentID, metadata, hash, model string
	if err := receiver.db.QueryRow(`SELECT agent_type, agent_id, metadata, content_hash, generated_by_model FROM observations`).Scan(&agentType, &agentID, &metadata, &hash, &model); err != nil {
		t.Fatal(err)
	}
	if agentType != "kimi" || agentID != "agent-9" || metadata != `{"m":1}` || hash != "hash-9" || model != "k2" {
		t.Fatalf("repair incomplete: %q %q %q %q %q", agentType, agentID, metadata, hash, model)
	}
	// Repair is one-off: a third pass repairs nothing.
	res2, err := receiver.Import(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Repaired != 0 {
		t.Fatalf("second repair = %d, want 0", res2.Repaired)
	}
}

// TestExportCutoff: incremental export keeps only rows newer than the
// cutoff; the backing-sessions rule keeps referenced and newer sessions.
func TestExportCutoff(t *testing.T) {
	db := newTestSyncDB(t)
	seedSession(t, db, "c-old", "m-old", "2026-09-01T00:00:00.000Z")
	seedSession(t, db, "c-new", "m-new", "2026-09-25T00:00:00.000Z")
	seedObs(t, db, "m-old", "2026-09-01T01:00:00.000Z", "old")
	seedObs(t, db, "m-new", "2026-09-25T01:00:00.000Z", "new")
	seedSummary(t, db, "m-old", "2026-09-01T02:00:00.000Z", "old-req")
	seedSummary(t, db, "m-new", "2026-09-25T02:00:00.000Z", "new-req")

	bundle, err := db.Export(context.Background(), map[string]string{"observations": "2026-09-20T00:00:00.000Z", "session_summaries": "2026-09-20T00:00:00.000Z"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Observations) != 1 || rowString(bundle.Observations[0], "title") != "new" {
		t.Fatalf("obs = %v", bundle.Observations)
	}
	if len(bundle.Summaries) != 1 || rowString(bundle.Summaries[0], "request") != "new-req" {
		t.Fatalf("sums = %v", bundle.Summaries)
	}
	// Only the new session: the old one is neither referenced nor newer.
	if len(bundle.Sessions) != 1 || rowString(bundle.Sessions[0], "memory_session_id") != "m-new" {
		t.Fatalf("sessions = %v", bundle.Sessions)
	}

	// An old session IS included when an exported (overlapping) row backs it.
	seedObs(t, db, "m-old", "2026-09-26T01:00:00.000Z", "overlap")
	bundle, err = db.Export(context.Background(), map[string]string{"observations": "2026-09-20T00:00:00.000Z", "session_summaries": "2026-09-20T00:00:00.000Z"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Sessions) != 2 {
		t.Fatalf("referenced old session must ride along: %v", bundle.Sessions)
	}
}

// TestExportOnlySessions is the sessions-mode pass: all sessions, nothing else.
// Per-table cutoffs: when one table races ahead, a shared cutoff would
// strand the lagging table's gap rows (the receiver's overlap only reaches
// 7 days behind ITS OWN newest row per table).
func TestExportPerTableCutoffs(t *testing.T) {
	db := newTestSyncDB(t)
	seedSession(t, db, "c1", "m1", "2026-09-01T00:00:00.000Z")
	seedObs(t, db, "m1", "2026-09-25T01:00:00.000Z", "obs-new")
	seedObs(t, db, "m1", "2026-09-10T01:00:00.000Z", "obs-old")
	seedSummary(t, db, "m1", "2026-01-01T00:00:00.000Z", "sum-old")

	// Observations are exported against a September cutoff while summaries
	// use their own January one: the ancient summary must still be offered.
	bundle, err := db.Export(context.Background(), map[string]string{
		"observations":      "2026-09-20T00:00:00.000Z",
		"session_summaries": "2025-12-25T00:00:00.000Z",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Observations) != 1 || rowString(bundle.Observations[0], "title") != "obs-new" {
		t.Fatalf("obs = %v", bundle.Observations)
	}
	if len(bundle.Summaries) != 1 || rowString(bundle.Summaries[0], "request") != "sum-old" {
		t.Fatalf("the lagging table's rows must not be stranded: %v", bundle.Summaries)
	}
}

func TestExportOnlySessions(t *testing.T) {
	db := newTestSyncDB(t)
	seedSession(t, db, "c1", "m1", "2026-09-01T00:00:00.000Z")
	seedSession(t, db, "c2", "m2", "2026-09-25T00:00:00.000Z")
	seedObs(t, db, "m1", "2026-09-26T01:00:00.000Z", "x")

	bundle, err := db.Export(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Sessions) != 2 || len(bundle.Observations) != 0 || len(bundle.Summaries) != 0 {
		t.Fatalf("sessions-mode bundle = %d sessions %d obs %d sums", len(bundle.Sessions), len(bundle.Observations), len(bundle.Summaries))
	}
}

func TestCutoffFor(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cutoff, err := CutoffFor("2026-09-27T02:51:08.182Z", now)
	if err != nil {
		t.Fatal(err)
	}
	if cutoff != "2026-09-20T02:51:08.182Z" {
		t.Errorf("cutoff = %q", cutoff)
	}
	if c, err := CutoffFor("", now); err != nil || c != "" {
		t.Errorf("empty max = %q, %v; want empty (full export)", c, err)
	}
	// A receiver clock ahead of ours clamps to now, not the future.
	if c, err := CutoffFor("2027-01-01T00:00:00.000Z", now); err != nil || c != now.UTC().Format(syncTimeFormat) {
		t.Errorf("future max = %q, %v", c, err)
	}
	if _, err := CutoffFor("not-a-time", now); err == nil {
		t.Error("garbage max must error")
	}
}

func TestMaxAndCounts(t *testing.T) {
	db := newTestSyncDB(t)
	seedSession(t, db, "c1", "m1", "2026-09-20T10:00:00.000Z")
	seedObs(t, db, "m1", "2026-09-20T10:01:00.000Z", "with-session")
	execIns(t, db, `INSERT INTO observations (memory_session_id, project, type, title, created_at, created_at_epoch)
		VALUES ('m-ghost', 'proj', 'discovery', 'orphan', '2026-09-21T10:01:00.000Z', 0)`)
	seedSummary(t, db, "m1", "2026-09-22T10:02:00.000Z", "req")

	maxes, err := db.MaxCreatedAt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if maxes["observations"] != "2026-09-21T10:01:00.000Z" || maxes["session_summaries"] != "2026-09-22T10:02:00.000Z" {
		t.Fatalf("maxes = %v", maxes)
	}
	counts, err := db.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Obs != 2 || counts.Sums != 1 || counts.Sessions != 1 || counts.ObsWithoutSession != 1 {
		t.Fatalf("counts = %#v", counts)
	}
}

// TestKickWorkerRestart: the kick POSTs when the settings point at a live
// worker and silently skips every absent-worker shape.
func TestKickWorkerRestart(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != http.MethodPost || r.URL.Path != "/api/admin/restart" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	home := t.TempDir()
	memDir := filepath.Join(home, ".claude-mem")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]string{"CLAUDE_MEM_WORKER_PORT": port})
	if err := os.WriteFile(filepath.Join(memDir, "settings.json"), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := KickWorkerRestart(context.Background(), home, srv.Client()); err != nil {
		t.Fatalf("kick: %v", err)
	}
	if hits != 1 {
		t.Fatalf("worker hits = %d, want 1", hits)
	}

	// No settings file, no port, dead worker: all silent no-ops.
	if err := KickWorkerRestart(context.Background(), t.TempDir(), nil); err != nil {
		t.Fatalf("missing settings must skip: %v", err)
	}
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	deadPort := strings.TrimPrefix(closedURL, "http://127.0.0.1:")
	deadSettings, _ := json.Marshal(map[string]string{"CLAUDE_MEM_WORKER_PORT": deadPort})
	if err := os.WriteFile(filepath.Join(memDir, "settings.json"), deadSettings, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := KickWorkerRestart(context.Background(), home, &http.Client{Timeout: time.Second}); err != nil {
		t.Fatalf("dead worker must skip silently: %v", err)
	}
}

// Read-only opens must not create a missing database: a status probe that
// conjures an empty claude-mem.db would pass for a real (empty) store.
func TestOpenSyncDBReadOnly_MissingFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	if _, err := OpenSyncDBReadOnly(path); err == nil {
		t.Fatal("read-only open of a missing DB must error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only open created the file: %v", err)
	}
}

func TestImportSessionWithUnassignedMemoryID(t *testing.T) {
	for _, incomingID := range []any{nil, "sender-memory"} {
		t.Run(fmt.Sprint(incomingID), func(t *testing.T) {
			receiver := newTestSyncDB(t)
			execIns(t, receiver, `INSERT INTO sdk_sessions (content_session_id, memory_session_id, project, platform_source, started_at, started_at_epoch) VALUES ('pending-content', NULL, 'proj', 'claude', '2026-09-20', 0)`)
			bundle := &Bundle{Sessions: []map[string]any{{"content_session_id": "pending-content", "memory_session_id": incomingID, "platform_source": "claude"}}}
			for pass := 0; pass < 2; pass++ {
				result, err := receiver.Import(context.Background(), bundle)
				if err != nil {
					t.Fatal(err)
				}
				if result.Sessions != 0 || result.RelinkedObservations != 0 || result.RelinkedSummaries != 0 {
					t.Fatalf("pending session changed: %+v", result)
				}
			}
			var count int
			if err := receiver.db.QueryRow(`SELECT COUNT(*) FROM sdk_sessions WHERE memory_session_id IS NULL`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("unassigned receiver sessions=%d, want 1", count)
			}
		})
	}
}

func TestImportRejectsConflictingBundleSessionIdentity(t *testing.T) {
	receiver := newTestSyncDB(t)
	seedSession(t, receiver, "content-a", "receiver-memory", "2026-09-20T10:00:00.000Z")
	seedObs(t, receiver, "sender-memory", "2026-09-20T11:00:00.000Z", "known-orphan")
	bundle := &Bundle{Sessions: []map[string]any{
		{"memory_session_id": "sender-memory", "platform_source": "claude", "content_session_id": "content-a"},
		{"memory_session_id": "sender-memory", "platform_source": "claude", "content_session_id": "content-b", "project": "proj", "started_at": "2026-09-20", "started_at_epoch": 0},
	}}
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			if reverse {
				bundle.Sessions[0], bundle.Sessions[1] = bundle.Sessions[1], bundle.Sessions[0]
			}
			if _, err := receiver.Import(context.Background(), bundle); err == nil {
				t.Fatal("conflicting session identities accepted")
			}
			var sessionCount int
			if err := receiver.db.QueryRow(`SELECT COUNT(*) FROM sdk_sessions`).Scan(&sessionCount); err != nil {
				t.Fatal(err)
			}
			if sessionCount != 1 {
				t.Fatalf("failed import persisted sessions: %d", sessionCount)
			}
			var memoryID string
			if err := receiver.db.QueryRow(`SELECT memory_session_id FROM observations WHERE title='known-orphan'`).Scan(&memoryID); err != nil {
				t.Fatal(err)
			}
			if memoryID != "sender-memory" {
				t.Fatalf("failed import changed observation identity: %q", memoryID)
			}
		})
	}
}

// A write tx that reads first must not fail when another connection (the
// claude-mem worker) tries to commit between its read and its write.
func TestSyncDBWriteTxSurvivesConcurrentCommit(t *testing.T) {
	d := newTestSyncDB(t)
	// The live worker runs the store in WAL mode, where readers never block a
	// writer's commit; that is what makes the deferred upgrade fail.
	if _, err := d.db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	worker, err := sql.Open("sqlite", "file:"+d.Path+"?_pragma=busy_timeout(50)")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	const insert = "INSERT INTO sdk_sessions (content_session_id, memory_session_id, project, started_at, started_at_epoch, status) VALUES (?, ?, 'p', '2026-01-01', 1, 'active')"

	tx, err := d.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM sdk_sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Deferred tx: this commit lands and the write below fails SQLITE_BUSY.
	// Immediate tx: we hold the write lock, so the worker is the one refused.
	if _, err := worker.Exec(insert, "w", "w"); err == nil {
		t.Log("worker committed between the read and the write")
	}
	if _, err := tx.Exec(insert, "s", "s"); err != nil {
		t.Fatalf("write after read in tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
