package syncer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FingerprintMode picks how Intake compares mirror files to manifest
// entries. Fast mode uses (size, mtime); strict mode adds sha256.
type FingerprintMode int

const (
	// FingerprintFast records (size, mtime) for regular files, matching
	// rsync's natural change-detection. Links always carry a target hash.
	FingerprintFast FingerprintMode = iota
	// FingerprintStrict additionally records sha256, catching the
	// content-changed-but-mtime-preserved case (rare in practice but
	// worth opting into for paranoid intake).
	FingerprintStrict
)

// fingerprintTimeFormat is the on-disk timestamp shape. Nanosecond
// resolution preserves the full mtime; reads accept truncated-RFC3339
// for forward compatibility.
const fingerprintTimeFormat = time.RFC3339Nano
const fingerprintNoSha = "-"

// Fingerprint identifies a file's content at a point in time.
type Fingerprint struct {
	Size  int64
	Mtime time.Time
	Sha   string // regular: empty in fast mode or hex sha256; link: "link:" + hex sha256
}

// Encode renders the trailing 3 columns of a manifest line:
// "size<TAB>mtime<TAB>sha-or-dash".
func (f Fingerprint) Encode() string {
	sha := f.Sha
	if sha == "" {
		sha = fingerprintNoSha
	}
	return fmt.Sprintf("%d\t%s\t%s",
		f.Size,
		f.Mtime.UTC().Format(fingerprintTimeFormat),
		sha,
	)
}

// CompactEncode renders the fingerprint as "size|mtime|sha-or-dash" —
// the pipe-separated form used by tombstones.log so the line stays
// fielded into 3 tab columns (relpath, fingerprint, detected-at).
func (f Fingerprint) CompactEncode() string {
	sha := f.Sha
	if sha == "" {
		sha = fingerprintNoSha
	}
	return fmt.Sprintf("%d|%s|%s",
		f.Size,
		f.Mtime.UTC().Format(fingerprintTimeFormat),
		sha,
	)
}

// DecodeFingerprint parses size, mtime, sha-or-dash strings (the 3
// trailing manifest fields) into a Fingerprint.
func DecodeFingerprint(size, mtime, sha string) (Fingerprint, error) {
	s, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return Fingerprint{}, fmt.Errorf("size %q: %w", size, err)
	}
	t, err := parseFingerprintTime(mtime)
	if err != nil {
		return Fingerprint{}, fmt.Errorf("mtime %q: %w", mtime, err)
	}
	if sha == fingerprintNoSha {
		sha = ""
	}
	return Fingerprint{Size: s, Mtime: t, Sha: sha}, nil
}

// DecodeCompactFingerprint parses the pipe-separated form.
func DecodeCompactFingerprint(s string) (Fingerprint, error) {
	parts := strings.Split(s, "|")
	if len(parts) != 3 {
		return Fingerprint{}, fmt.Errorf("compact fingerprint %q: want 3 fields", s)
	}
	return DecodeFingerprint(parts[0], parts[1], parts[2])
}

func parseFingerprintTime(s string) (time.Time, error) {
	if t, err := time.Parse(fingerprintTimeFormat, s); err == nil {
		return t, nil
	}
	// Older entries written before nano-resolution lands on disk.
	return time.Parse(time.RFC3339, s)
}

// FingerprintFile lstats path and (in strict mode) hashes regular content.
// Links always hash their target string, including dangling links.
func FingerprintFile(path string, mode FingerprintMode) (Fingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Fingerprint{}, err
	}
	if info.IsDir() {
		return Fingerprint{}, fmt.Errorf("fingerprint: %s is a directory", path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return Fingerprint{}, err
		}
		return fingerprintLinkTarget(target, info.ModTime()), nil
	}
	fp := Fingerprint{Size: info.Size(), Mtime: info.ModTime().UTC()}
	if mode == FingerprintStrict {
		sha, err := hashFile(path)
		if err != nil {
			return Fingerprint{}, err
		}
		fp.Sha = sha
	}
	return fp, nil
}

// The prefix separates link targets from regular-file content hashes while
// retaining the existing manifest columns and tombstone encoding.
func fingerprintLinkTarget(target string, mtime time.Time) Fingerprint {
	sum := sha256.Sum256([]byte(target))
	return Fingerprint{Size: int64(len(target)), Mtime: mtime.UTC(), Sha: "link:" + hex.EncodeToString(sum[:])}
}

// FingerprintsCompatible compares a manifest entry to a freshly-stat'd
// fingerprint. When one side has a sha and the other doesn't, the
// missing side is hashed lazily off the supplied path so fast and
// strict manifests interoperate.
func FingerprintsCompatible(manifest, current Fingerprint, currentPath string) bool {
	if strings.HasPrefix(manifest.Sha, "link:") != strings.HasPrefix(current.Sha, "link:") {
		return false
	}
	if manifest.Sha != "" && current.Sha != "" {
		return manifest.Sha == current.Sha
	}
	if manifest.Sha != "" && current.Sha == "" {
		// Lazy upgrade: hash the current file once and compare.
		if sha, err := hashFile(currentPath); err == nil {
			return sha == manifest.Sha
		}
		return false
	}
	// Neither side has a sha, OR only current has one — both fall back
	// to size+mtime.
	return manifest.Size == current.Size && sameMtime(manifest.Mtime, current.Mtime)
}

// sameMtime compares two timestamps after normalizing to UTC and
// truncating to second precision (rsync's wire-level resolution).
func sameMtime(a, b time.Time) bool {
	return a.UTC().Truncate(time.Second).Equal(b.UTC().Truncate(time.Second))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── manifest IO ─────────────────────────────────────────────────────────

const (
	baselineHeader = "# Auto-generated by `dot sync push` — do not edit.\n" +
		"# relpath\tsize\tmtime-rfc3339\tsha256-or-dash\n"
	importsHeader = "# Auto-generated by `dot sync intake` — clear via `dot sync inbox clear`.\n" +
		"# relpath\tsize\tmtime-rfc3339\tsha256-or-dash\timported-rfc3339\n"
	tombstonesHeader = "# Auto-generated by `dot sync intake` — clear via `dot sync inbox clear`.\n" +
		"# relpath\tbaseline-fingerprint\tdetected-rfc3339\n"
)

// ponytail: known ceiling. See docs/CEILINGS.md (manifest serialization).
// manifestMu serializes manifest writes within a process. Cross-process
// coordination is the lock-dir's job (AcquireLock); within-process,
// this guards the read-modify-write nature of rewrite-style saves.
var manifestMu sync.Mutex

// LoadBaselineManifest reads <path> as the baseline manifest. A missing
// file is not an error — the first push hasn't happened yet.
func LoadBaselineManifest(path string) (map[string]Fingerprint, error) {
	return loadFingerprintMap(path)
}

// SaveBaselineManifest writes the full manifest atomically. Entries are
// sorted by relpath so re-runs produce stable diffs.
func SaveBaselineManifest(path string, entries map[string]Fingerprint) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()

	keys := sortedKeys(entries)
	var b strings.Builder
	b.WriteString(baselineHeader)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\t')
		b.WriteString(entries[k].Encode())
		b.WriteByte('\n')
	}
	return atomicWrite(path, []byte(b.String()))
}

// ImportEntry is one row of imports.manifest.
type ImportEntry struct {
	FP         Fingerprint
	ImportedAt time.Time
}

// LoadImportsManifest reads <path>; missing → empty map.
func LoadImportsManifest(path string) (map[string]ImportEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]ImportEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]ImportEntry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if isMergeConflictMarker(line) {
			return nil, fmt.Errorf("imports manifest %s contains git merge conflict marker %q", path, line)
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) != 5 {
			return nil, fmt.Errorf("imports manifest %s: bad line %q", path, line)
		}
		fp, err := DecodeFingerprint(parts[1], parts[2], parts[3])
		if err != nil {
			return nil, fmt.Errorf("imports manifest %s: %w", path, err)
		}
		importedAt, err := parseFingerprintTime(parts[4])
		if err != nil {
			return nil, fmt.Errorf("imports manifest %s: imported-at %q: %w", path, parts[4], err)
		}
		out[parts[0]] = ImportEntry{FP: fp, ImportedAt: importedAt}
	}
	return out, sc.Err()
}

// SaveImportsManifest writes the full manifest atomically, sorted.
func SaveImportsManifest(path string, entries map[string]ImportEntry) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()

	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(importsHeader)
	for _, k := range keys {
		e := entries[k]
		b.WriteString(k)
		b.WriteByte('\t')
		b.WriteString(e.FP.Encode())
		b.WriteByte('\t')
		b.WriteString(e.ImportedAt.UTC().Format(fingerprintTimeFormat))
		b.WriteByte('\n')
	}
	return atomicWrite(path, []byte(b.String()))
}

// Tombstone is one row of tombstones.log.
type Tombstone struct {
	RelPath    string
	BaselineFP Fingerprint
	DetectedAt time.Time
}

// AppendTombstones appends to <path> in append-only fashion. Header is
// written if the file is new or empty.
func AppendTombstones(path string, entries []Tombstone) error {
	if len(entries) == 0 {
		return nil
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	needHeader := false
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		needHeader = true
	case err != nil:
		return err
	case info.Size() == 0:
		needHeader = true
	}

	w, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer w.Close()

	if needHeader {
		if _, err := w.WriteString(tombstonesHeader); err != nil {
			return err
		}
	}
	for _, t := range entries {
		line := fmt.Sprintf("%s\t%s\t%s\n",
			t.RelPath,
			t.BaselineFP.CompactEncode(),
			t.DetectedAt.UTC().Format(fingerprintTimeFormat),
		)
		if _, err := w.WriteString(line); err != nil {
			return err
		}
	}
	return w.Sync()
}

// LoadTombstones reads <path>; missing → nil.
func LoadTombstones(path string) ([]Tombstone, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Tombstone
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if isMergeConflictMarker(line) {
			return nil, fmt.Errorf("tombstones %s contains git merge conflict marker %q", path, line)
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("tombstones %s: bad line %q", path, line)
		}
		fp, err := DecodeCompactFingerprint(parts[1])
		if err != nil {
			return nil, err
		}
		detectedAt, err := parseFingerprintTime(parts[2])
		if err != nil {
			return nil, err
		}
		out = append(out, Tombstone{
			RelPath: parts[0], BaselineFP: fp, DetectedAt: detectedAt,
		})
	}
	return out, sc.Err()
}

// ClearImportsAndTombstones empties both manifests. Used by
// `dot sync inbox clear`.
func ClearImportsAndTombstones(paths *LocalPaths) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	if err := atomicWrite(paths.ImportsFile, []byte(importsHeader)); err != nil {
		return err
	}
	if err := atomicWrite(paths.TombstonesFile, []byte(tombstonesHeader)); err != nil {
		return err
	}
	return nil
}

// ForgetImport drops a single relpath from imports.manifest, forcing
// the next intake to re-stage the file regardless of fingerprint match.
// Returns true if the entry existed.
func ForgetImport(paths *LocalPaths, relPath string) (bool, error) {
	imports, err := LoadImportsManifest(paths.ImportsFile)
	if err != nil {
		return false, err
	}
	if _, ok := imports[relPath]; !ok {
		return false, nil
	}
	delete(imports, relPath)
	return true, SaveImportsManifest(paths.ImportsFile, imports)
}

// loadFingerprintMap is the 4-field reader used by baseline.manifest.
func loadFingerprintMap(path string) (map[string]Fingerprint, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Fingerprint{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]Fingerprint{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if isMergeConflictMarker(line) {
			return nil, fmt.Errorf("manifest %s contains git merge conflict marker %q", path, line)
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			return nil, fmt.Errorf("manifest %s: bad line %q", path, line)
		}
		fp, err := DecodeFingerprint(parts[1], parts[2], parts[3])
		if err != nil {
			return nil, fmt.Errorf("manifest %s: %w", path, err)
		}
		out[parts[0]] = fp
	}
	return out, sc.Err()
}

func isMergeConflictMarker(line string) bool {
	return strings.HasPrefix(line, "<<<<<<<") ||
		strings.HasPrefix(line, "=======") ||
		strings.HasPrefix(line, ">>>>>>>")
}

func sortedKeys(m map[string]Fingerprint) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
