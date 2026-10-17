package fileutil

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

type archiveTestEntry struct {
	name     string
	content  string
	typeflag byte
	linkname string
	mode     int64
}

func makeTarGzEntries(t *testing.T, entries []archiveTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0755
		}
		header := &tar.Header{Name: entry.name, Mode: mode, Typeflag: typeflag, Linkname: entry.linkname}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, entries []archiveTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		mode := os.FileMode(entry.mode)
		if mode == 0 {
			mode = 0755
		}
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(mode)
		if entry.typeflag == tar.TypeSymlink {
			header.SetMode(os.ModeSymlink | 0755)
			entry.content = entry.linkname
		}
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// makeTarGz builds an in-memory .tar.gz with the given name → content
// entries, all regular files mode 0755.
func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownloadFile_ReturnsSHA256(t *testing.T) {
	content := []byte("release archive bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset.tar.gz")
	runner := exec.NewRunner(false, quietLogger())

	got, err := DownloadFile(context.Background(), runner, srv.URL, dest)
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}

	sum := sha256.Sum256(content)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("sha256 = %s, want %s", got, want)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Error("downloaded content differs from served content")
	}
}

func TestDownloadFile_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "asset.tar.gz")
	runner := exec.NewRunner(false, quietLogger())

	if _, err := DownloadFile(context.Background(), runner, srv.URL, dest); err == nil {
		t.Fatal("expected error on HTTP 404")
	}
}

func TestDownloadFile_RejectsChunkedBodyOverLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("oversize"))
	}))
	defer srv.Close()

	limits := DefaultArchiveLimits
	limits.MaxCompressedBytes = 1
	dest := filepath.Join(t.TempDir(), "asset.zip")
	runner := exec.NewRunner(false, quietLogger())
	if _, err := downloadFileWithLimits(context.Background(), runner, newBulkHTTPClient(), srv.URL, dest, limits); err == nil {
		t.Fatal("expected streamed body limit refusal")
	}
}

func TestDownloadFile_DryRun(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")
	runner := exec.NewRunner(true, quietLogger())

	sum, err := DownloadFile(context.Background(), runner, "http://invalid.invalid/x", dest)
	if err != nil {
		t.Fatalf("dry-run should not error: %v", err)
	}
	if sum != "" {
		t.Errorf("dry-run sum = %q, want empty", sum)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dry-run must not create the destination file")
	}
}

func TestMetadataTimeout(t *testing.T) {
	client := newMetadataHTTPClient()
	if client.Timeout != metadataHTTPTimeout {
		t.Fatalf("metadata client timeout = %s, want %s", client.Timeout, metadataHTTPTimeout)
	}

	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial metadata"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-started; cancel() }()
	dest := filepath.Join(t.TempDir(), "checksums.txt")
	if _, err := DownloadMetadataFile(ctx, exec.NewRunner(false, quietLogger()), srv.URL, dest); err == nil {
		t.Fatal("expected canceled metadata download to fail")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("canceled metadata download must remove the partial destination")
	}
}

func TestSlowBulkDownload(t *testing.T) {
	const total = 24 << 20
	chunk := bytes.Repeat([]byte("a"), 256<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for written := 0; written < total; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(170 * time.Millisecond)
		}
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "slow.tar.gz")
	started := time.Now()
	got, err := DownloadFile(context.Background(), exec.NewRunner(false, quietLogger()), srv.URL, dest)
	if err != nil {
		t.Fatalf("slow bulk download: %v", err)
	}
	if elapsed := time.Since(started); elapsed <= metadataHTTPTimeout {
		t.Fatalf("slow bulk body completed in %s, test must exceed metadata deadline %s", elapsed, metadataHTTPTimeout)
	}
	want := sha256.Sum256(bytes.Repeat(chunk, total/len(chunk)))
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("slow bulk sha256 = %s, want %s", got, hex.EncodeToString(want[:]))
	}
}

func TestBulkTransportTimeouts(t *testing.T) {
	client := newBulkHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("bulk client timeout = %s, want no total body deadline", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("bulk transport = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != bulkTLSHandshakeTimeout || transport.ResponseHeaderTimeout != bulkResponseHeaderTimeout {
		t.Fatalf("bulk transport timeouts = TLS %s/header %s", transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout)
	}
}

func TestDownloadCancellationCleanup(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1024))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-started; cancel() }()
	dest := filepath.Join(t.TempDir(), "cancelled.tar.gz")
	if _, err := DownloadFile(ctx, exec.NewRunner(false, quietLogger()), srv.URL, dest); err == nil {
		t.Fatal("expected cancellation to fail")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("canceled bulk download must remove partial file")
	}
}

func TestDownloadRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("retry success"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "retry.tar.gz")
	if _, err := DownloadFile(context.Background(), exec.NewRunner(false, quietLogger()), srv.URL, dest); err != nil {
		t.Fatalf("retry download: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestExtractTarGz(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"dot": "#!/bin/sh\necho dot\n"})
	dest := t.TempDir()

	if err := ExtractTarGz(bytes.NewReader(archive), dest, 0); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}

	target := filepath.Join(dest, "dot")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
	data, _ := os.ReadFile(target)
	if string(data) != "#!/bin/sh\necho dot\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestExtractTarGz_StripComponents(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"prefix-1.0/bin/tool": "binary"})
	dest := t.TempDir()

	if err := ExtractTarGz(bytes.NewReader(archive), dest, 1); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "bin", "tool")); err != nil {
		t.Errorf("stripped path missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "prefix-1.0")); !os.IsNotExist(err) {
		t.Error("unstripped prefix directory should not exist")
	}
}

func TestExtractTarGzSecurityMatrix(t *testing.T) {
	t.Run("stripped siblings stay rooted", func(t *testing.T) {
		dest := t.TempDir()
		archive := makeTarGzEntries(t, []archiveTestEntry{
			{name: "prefix/dir/file", content: "file"},
			{name: "prefix/sibling", content: "sibling"},
		})
		if err := ExtractTarGz(bytes.NewReader(archive), dest, 1); err != nil {
			t.Fatalf("ExtractTarGz: %v", err)
		}
		for _, path := range []string{"dir/file", "sibling"} {
			if _, err := os.Stat(filepath.Join(dest, path)); err != nil {
				t.Fatalf("expected rooted %s: %v", path, err)
			}
		}
	})

	for _, tc := range []struct {
		name  string
		entry archiveTestEntry
		strip int
	}{
		{name: "upward", entry: archiveTestEntry{name: "../escape", content: "no"}},
		{name: "absolute", entry: archiveTestEntry{name: "/escape", content: "no"}},
		{name: "post strip upward", entry: archiveTestEntry{name: "prefix/../escape", content: "no"}, strip: 1},
		{name: "hard link", entry: archiveTestEntry{name: "hard", typeflag: tar.TypeLink, linkname: "target"}},
		{name: "fifo", entry: archiveTestEntry{name: "fifo", typeflag: tar.TypeFifo}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			outside := filepath.Join(filepath.Dir(dest), "escape")
			if err := ExtractTarGz(bytes.NewReader(makeTarGzEntries(t, []archiveTestEntry{tc.entry})), dest, tc.strip); err == nil {
				t.Fatal("ExtractTarGz succeeded for unsafe entry")
			}
			if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("outside target was touched: %v", err)
			}
		})
	}

	t.Run("relative link is resolved from final parent", func(t *testing.T) {
		dest := t.TempDir()
		archive := makeTarGzEntries(t, []archiveTestEntry{
			{name: "prefix/lib/target", content: "ok"},
			{name: "prefix/bin/tool", typeflag: tar.TypeSymlink, linkname: "../lib/target"},
		})
		if err := ExtractTarGz(bytes.NewReader(archive), dest, 1); err != nil {
			t.Fatalf("ExtractTarGz: %v", err)
		}
		if target, err := os.Readlink(filepath.Join(dest, "bin", "tool")); err != nil || target != "../lib/target" {
			t.Fatalf("link = %q, %v", target, err)
		}
	})
}

func TestExtractZipSecurityMatrix(t *testing.T) {
	t.Run("regular content remains rooted", func(t *testing.T) {
		dest := t.TempDir()
		archive := makeZip(t, []archiveTestEntry{{name: "dir/file", content: "ok"}})
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			t.Fatal(err)
		}
		if err := ExtractZip(reader, dest); err != nil {
			t.Fatalf("ExtractZip: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(dest, "dir", "file")); err != nil || string(data) != "ok" {
			t.Fatalf("rooted zip output = %q, %v", data, err)
		}
	})

	for _, name := range []string{"../escape", "/escape", "C:/escape"} {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			archive := makeZip(t, []archiveTestEntry{{name: name, content: "no"}})
			reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
			if err != nil {
				t.Fatal(err)
			}
			if err := ExtractZip(reader, dest); err == nil {
				t.Fatal("ExtractZip succeeded for unsafe name")
			}
		})
	}

	t.Run("safe and escaping symlinks", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			link string
			want bool
		}{
			{name: "safe", link: "../lib/target", want: true},
			{name: "escape", link: "../../escape"},
			{name: "absolute", link: "/escape"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dest := t.TempDir()
				archive := makeZip(t, []archiveTestEntry{{name: "lib/target", content: "ok"}, {name: "bin/tool", typeflag: tar.TypeSymlink, linkname: tc.link}})
				reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
				if err != nil {
					t.Fatal(err)
				}
				err = ExtractZip(reader, dest)
				if tc.want && err != nil {
					t.Fatalf("ExtractZip: %v", err)
				}
				if !tc.want && err == nil {
					t.Fatal("ExtractZip accepted escaping symlink")
				}
			})
		}
	})
}

func TestExtractTarGz_ArchiveLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int64
		max  int64
		want bool
	}{
		{"entry exact", 67108864, DefaultArchiveLimits.MaxEntryBytes, true},
		{"entry boundary plus one", 67108865, DefaultArchiveLimits.MaxEntryBytes, false},
		{"total exact", 402653184, DefaultArchiveLimits.MaxTotalExtractedBytes, true},
		{"total boundary plus one", 402653185, DefaultArchiveLimits.MaxTotalExtractedBytes, false},
		{"ratio exact", 8, DefaultArchiveLimits.MaxExpansionRatio, true},
		{"ratio boundary plus one", 9, DefaultArchiveLimits.MaxExpansionRatio, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinArchiveLimit(tc.got, tc.max); got != tc.want {
				t.Fatalf("withinArchiveLimit(%d, %d) = %v, want %v", tc.got, tc.max, got, tc.want)
			}
		})
	}

	limits := DefaultArchiveLimits
	limits.MaxEntryBytes = 4
	limits.MaxTotalExtractedBytes = 6
	limits.MaxEntries = 1
	limits.MaxExpansionRatio = 8
	limits.MaxCompressedBytes = 1024
	for _, tc := range []struct {
		name    string
		entries []archiveTestEntry
	}{
		{name: "entry", entries: []archiveTestEntry{{name: "large", content: "12345"}}},
		{name: "total", entries: []archiveTestEntry{{name: "one", content: "1234"}, {name: "two", content: "1234"}}},
		{name: "count", entries: []archiveTestEntry{{name: "one", content: "1"}, {name: "two", content: "1"}}},
	} {
		t.Run("wired "+tc.name, func(t *testing.T) {
			if err := extractTarGzWithLimits(bytes.NewReader(makeTarGzEntries(t, tc.entries)), t.TempDir(), 0, limits); err == nil {
				t.Fatal("expected archive limit error")
			}
		})
	}

	t.Run("wired expansion ratio is checked after the complete tar stream", func(t *testing.T) {
		ratioLimits := DefaultArchiveLimits
		ratioLimits.MaxCompressedBytes = 1024
		ratioLimits.MaxEntryBytes = 1024
		ratioLimits.MaxTotalExtractedBytes = 1024
		ratioLimits.MaxEntries = 1
		ratioLimits.MaxExpansionRatio = 1
		archive := makeTarGzEntries(t, []archiveTestEntry{{name: "compressible", content: strings.Repeat("x", 128)}})
		if err := extractTarGzWithLimits(bytes.NewReader(archive), t.TempDir(), 0, ratioLimits); err == nil {
			t.Fatal("expected final expansion ratio error")
		}
	})
}

func TestExtractZip_ArchiveLimits(t *testing.T) {
	if DefaultArchiveLimits.MaxEntries != 4096 || DefaultArchiveLimits.MaxCompressedBytes != 201326592 || DefaultArchiveLimits.MaxEntryBytes != 67108864 {
		t.Fatalf("unexpected production limits: %+v", DefaultArchiveLimits)
	}
	limits := DefaultArchiveLimits
	limits.MaxEntryBytes = 4
	limits.MaxTotalExtractedBytes = 4
	limits.MaxEntries = 1
	limits.MaxExpansionRatio = 8
	limits.MaxCompressedBytes = 1024
	archive := makeZip(t, []archiveTestEntry{{name: "large", content: "12345"}})
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if err := extractZipWithLimits(reader, t.TempDir(), limits); err == nil {
		t.Fatal("expected zip archive limit error")
	}
}

func TestDownloadArchive_CompressedLimit(t *testing.T) {
	served := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.Header().Set("Content-Length", "201326593")
		_, _ = w.Write([]byte("too large"))
	}))
	defer srv.Close()
	if err := DownloadAndExtractTarGz(context.Background(), exec.NewRunner(false, quietLogger()), srv.URL, t.TempDir(), 0); err == nil {
		t.Fatal("expected compressed input refusal")
	}
	if !served {
		t.Fatal("server was not reached")
	}
}

func TestArchiveLimits_MeasuredSupportedArtifacts(t *testing.T) {
	artifacts := map[string]struct{ compressed, extracted, largestEntry, entries int64 }{
		"FiraCode":                  {compressed: 28602426, extracted: 164000000, entries: 500},
		"JetBrainsMono":             {compressed: 133975870, extracted: 243185440, entries: 98},
		"Hack":                      {compressed: 18694868, extracted: 145000000, entries: 700},
		"dot-darwin-amd64-v2.69.2":  {compressed: 5766495, extracted: 14817700, largestEntry: 14734288, entries: 3},
		"dot-darwin-arm64-v2.69.2":  {compressed: 5355772, extracted: 13822278, largestEntry: 13738866, entries: 3},
		"dot-linux-amd64-v2.69.2":   {compressed: 5646768, extracted: 14448268, largestEntry: 14364856, entries: 3},
		"dot-linux-arm64-v2.69.2":   {compressed: 5135535, extracted: 13452940, largestEntry: 13369528, entries: 3},
		"dot-darwin-amd64-v2.70.29": {compressed: 8517776, extracted: 21268555, largestEntry: 21228656, entries: 3},
		"dot-darwin-arm64-v2.70.29": {compressed: 8035139, extracted: 20067421, largestEntry: 20027522, entries: 3},
		"dot-linux-amd64-v2.70.29":  {compressed: 8410888, extracted: 20860051, largestEntry: 20820152, entries: 3},
		"dot-linux-arm64-v2.70.29":  {compressed: 7773002, extracted: 19569811, largestEntry: 19529912, entries: 3},
		"oh-my-zsh-146461f":         {compressed: 3340957, extracted: 7126554, entries: 1492},
	}
	for name, artifact := range artifacts {
		t.Run(name, func(t *testing.T) {
			if err := DefaultArchiveLimits.ValidateMeasured(artifact.compressed, artifact.extracted, artifact.entries); err != nil {
				t.Fatal(err)
			}
			if artifact.largestEntry > DefaultArchiveLimits.MaxEntryBytes {
				t.Fatalf("largest entry %d exceeds per-entry limit %d", artifact.largestEntry, DefaultArchiveLimits.MaxEntryBytes)
			}
		})
	}
}

// The release build runs this on every binary it built (.goreleaser.yaml post
// hook): a binary the updater could not extract fails the release before
// anything is published (#231).
func TestReleaseBinaryFitsArchiveLimits(t *testing.T) {
	path := os.Getenv("DOT_RELEASE_BINARY")
	if path == "" {
		t.Skip("DOT_RELEASE_BINARY is set by the release build")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > DefaultArchiveLimits.MaxEntryBytes {
		t.Fatalf("%s is %d bytes; dot update extracts at most %d per entry (raise DefaultArchiveLimits.MaxEntryBytes)", path, info.Size(), DefaultArchiveLimits.MaxEntryBytes)
	}
}

// The release hook names the size check by function name; a rename that left
// the hook behind would make go test run nothing and pass (#231).
func TestReleaseHookRunsTheSizeCheck(t *testing.T) {
	body, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	name := runtime.FuncForPC(reflect.ValueOf(TestReleaseBinaryFitsArchiveLimits).Pointer()).Name()
	name = name[strings.LastIndex(name, ".")+1:]
	if !strings.Contains(string(body), "-run ^"+name+"$") || !strings.Contains(string(body), "DOT_RELEASE_BINARY={{ .Path }}") {
		t.Errorf(".goreleaser.yaml does not run %s on each built binary (DOT_RELEASE_BINARY={{ .Path }})", name)
	}
}

func TestExtractRealOhMyZsh(t *testing.T) {
	const fixture = "testdata/ohmyzsh-146461f.tar.gz"
	const fixtureSHA256 = "23fd754895813e0f81293983c7600b7ac87faf69c0734514f6bc42f2f64b7e99"

	archive, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != fixtureSHA256 {
		t.Fatalf("fixture sha256 = %s, want %s", got, fixtureSHA256)
	}
	dest := t.TempDir()
	if err := ExtractTarGz(bytes.NewReader(archive), dest, 1); err != nil {
		t.Fatalf("ExtractTarGz fixture: %v", err)
	}
	for _, required := range []string{"oh-my-zsh.sh", "lib", "plugins", "themes", "custom"} {
		if _, err := os.Stat(filepath.Join(dest, required)); err != nil {
			t.Fatalf("fixture is missing %s: %v", required, err)
		}
	}
}

// Tar directory entries carry a trailing slash. os.Root.MkdirAll rejects that
// on Go 1.26 (mkdirat "a/": no such file or directory), so the stripped target
// must be cleaned before extraction (#129).
func TestExtractTarGz_DirectoryEntriesWithTrailingSlash(t *testing.T) {
	archive := makeTarGzEntries(t, []archiveTestEntry{
		{name: "top/", typeflag: tar.TypeDir, mode: 0755},
		{name: "top/dir/", typeflag: tar.TypeDir, mode: 0755},
		{name: "top/dir/sub/", typeflag: tar.TypeDir, mode: 0755},
		{name: "top/dir/sub/file", content: "x", mode: 0644},
	})
	for strip, want := range map[int][]string{
		0: {"top", "top/dir", "top/dir/sub", "top/dir/sub/file"},
		1: {"dir", "dir/sub", "dir/sub/file"},
	} {
		dest := t.TempDir()
		if err := ExtractTarGz(bytes.NewReader(archive), dest, strip); err != nil {
			t.Fatalf("strip=%d: %v", strip, err)
		}
		for _, rel := range want {
			if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
				t.Fatalf("strip=%d: missing %s: %v", strip, rel, err)
			}
		}
	}
}

func TestStrippedArchiveNameCleansTrailingSlash(t *testing.T) {
	cases := []struct {
		name  string
		strip int
		want  string
		ok    bool
	}{
		{"a/b/", 1, "b", true},
		{"a/b/c/", 1, filepath.Join("b", "c"), true},
		{"a/", 1, "", false},
		{"a/./", 1, "", false},
		{"a", 0, "a", true},
		{"a/", 0, "a", true},
	}
	for _, tc := range cases {
		got, ok := strippedArchiveName(tc.name, tc.strip)
		if got != tc.want || ok != tc.ok {
			t.Errorf("strippedArchiveName(%q, %d) = (%q, %v), want (%q, %v)", tc.name, tc.strip, got, ok, tc.want, tc.ok)
		}
	}
}

func TestExtractZip_DirectoryEntriesWithTrailingSlash(t *testing.T) {
	archive := makeZip(t, []archiveTestEntry{
		{name: "top/", typeflag: tar.TypeDir},
		{name: "top/dir/", typeflag: tar.TypeDir},
		{name: "top/dir/file", content: "x", mode: 0644},
	})
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := ExtractZip(reader, dest); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"top", "top/dir", "top/dir/file"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
}

// rawTarGz builds a one-entry ustar archive by hand: archive/tar refuses to
// write a regular file whose name ends in "/", but a reader still parses one.
func rawTarGz(t *testing.T, name string, typeflag byte, content string) []byte {
	t.Helper()
	block := make([]byte, 512)
	copy(block[0:], name)
	copy(block[100:], fmt.Sprintf("%07o\x00", 0o644))
	copy(block[108:], "0000000\x00")
	copy(block[116:], "0000000\x00")
	copy(block[124:], fmt.Sprintf("%011o\x00", len(content)))
	copy(block[136:], "00000000000\x00")
	copy(block[148:], "        ")
	block[156] = typeflag
	copy(block[257:], "ustar\x0000")
	sum := 0
	for _, b := range block {
		sum += int(b)
	}
	copy(block[148:], fmt.Sprintf("%06o\x00 ", sum))
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(block)
	data := make([]byte, 512)
	copy(data, content)
	gz.Write(data)
	gz.Write(make([]byte, 1024))
	gz.Close()
	return buf.Bytes()
}

func TestExtractTarGz_RejectsTrailingSlashOnFileEntry(t *testing.T) {
	archive := rawTarGz(t, "x/", tar.TypeReg, "1")
	err := ExtractTarGz(bytes.NewReader(archive), t.TempDir(), 0)
	if err == nil || !strings.Contains(err.Error(), "directory name on a non-directory entry") {
		t.Fatalf("err = %v, want rejection of file entry named like a directory", err)
	}
}
