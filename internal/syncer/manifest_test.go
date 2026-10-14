package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFingerprint_FastMode_SizeMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := FingerprintFile(path, FingerprintFast)
	if err != nil {
		t.Fatalf("FingerprintFile: %v", err)
	}
	if fp.Size != 5 {
		t.Errorf("Size = %d, want 5", fp.Size)
	}
	if fp.Sha != "" {
		t.Errorf("Sha = %q, want empty in fast mode", fp.Sha)
	}
	if fp.Mtime.IsZero() {
		t.Error("Mtime not populated")
	}
}

func TestFingerprint_StrictMode_Sha256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	body := []byte("hello world")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := FingerprintFile(path, FingerprintStrict)
	if err != nil {
		t.Fatalf("FingerprintFile: %v", err)
	}
	want := sha256.Sum256(body)
	if fp.Sha != hex.EncodeToString(want[:]) {
		t.Errorf("Sha = %q, want %x", fp.Sha, want)
	}
}

func TestFingerprint_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	in := Fingerprint{Size: 1234, Mtime: now, Sha: "abc123"}
	encoded := in.Encode()
	parts := strings.Split(encoded, "\t")
	if len(parts) != 3 {
		t.Fatalf("Encode produced %d fields, want 3", len(parts))
	}
	out, err := DecodeFingerprint(parts[0], parts[1], parts[2])
	if err != nil {
		t.Fatalf("DecodeFingerprint: %v", err)
	}
	if out.Size != in.Size || out.Sha != in.Sha {
		t.Errorf("round-trip mismatch: %+v vs %+v", in, out)
	}
	if !out.Mtime.Equal(in.Mtime) {
		t.Errorf("mtime mismatch: %s vs %s", in.Mtime, out.Mtime)
	}
}

func TestFingerprint_DashDecodesAsEmptySha(t *testing.T) {
	fp, err := DecodeFingerprint("100", time.Now().UTC().Format(time.RFC3339), "-")
	if err != nil {
		t.Fatal(err)
	}
	if fp.Sha != "" {
		t.Errorf("Sha = %q, want empty (dash means no sha)", fp.Sha)
	}
}

func TestFingerprintsCompatible_FastVsStrict_Interop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	strict, err := FingerprintFile(path, FingerprintStrict)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := FingerprintFile(path, FingerprintFast)
	if err != nil {
		t.Fatal(err)
	}

	// Strict-stored manifest entry vs fast-mode walk: must be considered
	// compatible (lazily hashes the current file).
	if !FingerprintsCompatible(strict, fast, path) {
		t.Error("strict-vs-fast same content not compatible")
	}
	// Fast-stored vs strict-walk: also compatible (size+mtime fallback).
	if !FingerprintsCompatible(fast, strict, path) {
		t.Error("fast-vs-strict same content not compatible")
	}
}

func TestFingerprintsCompatible_DetectsContentChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	stored, err := FingerprintFile(path, FingerprintStrict)
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite content, restore the original mtime — strict mode should
	// still detect the divergence.
	if err := os.WriteFile(path, []byte("v2_payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stored.Mtime, stored.Mtime); err != nil {
		t.Fatal(err)
	}
	current, err := FingerprintFile(path, FingerprintStrict)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintsCompatible(stored, current, path) {
		t.Error("strict-mode comparator missed content change with preserved mtime")
	}
}

func TestBaselineManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.manifest")
	now := time.Now().UTC().Truncate(time.Second)
	in := map[string]Fingerprint{
		"projects/foo/note.md": {Size: 100, Mtime: now, Sha: ""},
		"a/b/c.md":             {Size: 200, Mtime: now, Sha: "deadbeef"},
	}
	if err := SaveBaselineManifest(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadBaselineManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("len mismatch: %d vs %d", len(out), len(in))
	}
	for k, v := range in {
		got, ok := out[k]
		if !ok {
			t.Errorf("missing %q", k)
			continue
		}
		if got.Size != v.Size || got.Sha != v.Sha {
			t.Errorf("%q: %+v vs %+v", k, got, v)
		}
	}

	// File must be sorted (deterministic diffs).
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	dataLines := []string{}
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") && l != "" {
			dataLines = append(dataLines, l)
		}
	}
	if len(dataLines) != 2 {
		t.Fatalf("got %d data lines: %v", len(dataLines), dataLines)
	}
	if !strings.HasPrefix(dataLines[0], "a/b/c.md") {
		t.Errorf("not sorted: %v", dataLines)
	}
}

func TestBaselineManifest_MissingFileIsEmpty(t *testing.T) {
	out, err := LoadBaselineManifest(filepath.Join(t.TempDir(), "nope.manifest"))
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("got %d entries, want empty", len(out))
	}
}

func TestImportsManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "imports.manifest")
	imp := time.Now().UTC().Truncate(time.Second)
	mtime := imp.Add(-time.Hour)
	in := map[string]ImportEntry{
		"x/y.md": {FP: Fingerprint{Size: 50, Mtime: mtime}, ImportedAt: imp},
	}
	if err := SaveImportsManifest(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadImportsManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out["x/y.md"]
	if !ok {
		t.Fatal("missing entry")
	}
	if got.FP.Size != 50 {
		t.Errorf("Size = %d", got.FP.Size)
	}
	if !got.ImportedAt.Equal(imp) {
		t.Errorf("ImportedAt = %s, want %s", got.ImportedAt, imp)
	}
}

func TestTombstones_AppendOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tombstones.log")
	now := time.Now().UTC()
	t1 := []Tombstone{{
		RelPath:    "deleted/one.md",
		BaselineFP: Fingerprint{Size: 100, Mtime: now.Add(-time.Hour)},
		DetectedAt: now,
	}}
	if err := AppendTombstones(path, t1); err != nil {
		t.Fatal(err)
	}

	t2 := []Tombstone{{
		RelPath:    "deleted/two.md",
		BaselineFP: Fingerprint{Size: 200, Mtime: now.Add(-time.Hour)},
		DetectedAt: now.Add(time.Minute),
	}}
	if err := AppendTombstones(path, t2); err != nil {
		t.Fatal(err)
	}

	out, err := LoadTombstones(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d tombstones, want 2", len(out))
	}
	if out[0].RelPath != "deleted/one.md" || out[1].RelPath != "deleted/two.md" {
		t.Errorf("order/contents wrong: %+v", out)
	}

	// Header is written exactly once even after multiple appends.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	headerCount := strings.Count(string(body), "# Auto-generated")
	if headerCount != 1 {
		t.Errorf("header appears %d times, want 1", headerCount)
	}
}

func TestForgetImport_DropsEntry(t *testing.T) {
	dir := t.TempDir()
	paths := &LocalPaths{
		StoreDir:    dir,
		ImportsFile: filepath.Join(dir, "imports.manifest"),
	}
	in := map[string]ImportEntry{
		"keep.md": {FP: Fingerprint{Size: 1}, ImportedAt: time.Now().UTC()},
		"drop.md": {FP: Fingerprint{Size: 2}, ImportedAt: time.Now().UTC()},
	}
	if err := SaveImportsManifest(paths.ImportsFile, in); err != nil {
		t.Fatal(err)
	}
	dropped, err := ForgetImport(paths, "drop.md")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Error("ForgetImport returned false for known entry")
	}
	out, err := LoadImportsManifest(paths.ImportsFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["drop.md"]; ok {
		t.Error("drop.md still present after ForgetImport")
	}
	if _, ok := out["keep.md"]; !ok {
		t.Error("keep.md collateral-deleted")
	}
}

func TestForgetImport_MissingEntryNoOp(t *testing.T) {
	dir := t.TempDir()
	paths := &LocalPaths{
		StoreDir:    dir,
		ImportsFile: filepath.Join(dir, "imports.manifest"),
	}
	if err := SaveImportsManifest(paths.ImportsFile, map[string]ImportEntry{}); err != nil {
		t.Fatal(err)
	}
	dropped, err := ForgetImport(paths, "never-was.md")
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Error("ForgetImport returned true for unknown entry")
	}
}

func TestClearImportsAndTombstones(t *testing.T) {
	dir := t.TempDir()
	paths := &LocalPaths{
		StoreDir:       dir,
		ImportsFile:    filepath.Join(dir, "imports.manifest"),
		TombstonesFile: filepath.Join(dir, "tombstones.log"),
	}
	if err := SaveImportsManifest(paths.ImportsFile, map[string]ImportEntry{
		"a.md": {FP: Fingerprint{Size: 1}, ImportedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := AppendTombstones(paths.TombstonesFile, []Tombstone{{
		RelPath: "x.md", BaselineFP: Fingerprint{Size: 1}, DetectedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}

	if err := ClearImportsAndTombstones(paths); err != nil {
		t.Fatal(err)
	}

	imp, err := LoadImportsManifest(paths.ImportsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(imp) != 0 {
		t.Errorf("imports.manifest not cleared: %v", imp)
	}
	tomb, err := LoadTombstones(paths.TombstonesFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(tomb) != 0 {
		t.Errorf("tombstones.log not cleared: %v", tomb)
	}
}

func TestFingerprintsCompatibleRejectsLinkFileTypeAlias(t *testing.T) {
	link := fingerprintLinkTarget("same", time.Unix(100, 0))
	regular := Fingerprint{Size: link.Size, Mtime: link.Mtime}
	for _, pair := range [][2]Fingerprint{{link, regular}, {regular, link}} {
		if FingerprintsCompatible(pair[0], pair[1], filepath.Join(t.TempDir(), "missing")) {
			t.Fatal("equal-size same-mtime link/file fingerprints considered compatible")
		}
	}
	if !FingerprintsCompatible(link, link, "") {
		t.Fatal("identical links must remain compatible")
	}
}
