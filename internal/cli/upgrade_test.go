package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestParseSemver(t *testing.T) {
	tests := []struct {
		in        string
		wantMajor int
		wantMinor int
		wantPatch int
		wantOK    bool
	}{
		{"1.2.3", 1, 2, 3, true},
		{"0.1.0", 0, 1, 0, true},
		{"10.20.30", 10, 20, 30, true},
		{"1.2.3-beta.1", 1, 2, 3, true},
		{"1.2.3+build", 1, 2, 3, true},
		{"1.2", 0, 0, 0, false},
		{"1.2.3.4", 0, 0, 0, false},
		{"dev", 0, 0, 0, false},
		{"", 0, 0, 0, false},
		{"abc.def.ghi", 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			major, minor, patch, ok := parseSemver(tt.in)
			if ok != tt.wantOK {
				t.Errorf("ok=%v, want %v", ok, tt.wantOK)
			}
			if ok {
				if major != tt.wantMajor || minor != tt.wantMinor || patch != tt.wantPatch {
					t.Errorf("got %d.%d.%d, want %d.%d.%d",
						major, minor, patch, tt.wantMajor, tt.wantMinor, tt.wantPatch)
				}
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.2.0", "1.10.0", -1}, // numeric compare, not string
		{"1.10.0", "1.9.0", 1},  // critical: string compare would fail
		{"2.0.0", "1.99.99", 1},
		{"0.14.0", "0.13.0", 1},
		{"dev", "1.0.0", -1}, // dev is older than any release
		{"1.0.0", "dev", 1},
		{"dev", "dev", 0},
		{"1.0.0-beta", "1.0.0", 0}, // pre-release stripped, equal
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := compareSemver(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestParseChecksums(t *testing.T) {
	const sum = "a3f5c2e8d9b1470a6c3e58d2f4b09a1c7e6d5f4a3b2c1d0e9f8a7b6c5d4e3f2a"
	tests := []struct {
		name    string
		data    string
		asset   string
		want    string
		wantErr bool
	}{
		{
			name:  "goreleaser two-space format",
			data:  sum + "  dot_1.2.3_darwin_arm64.tar.gz\n",
			asset: "dot_1.2.3_darwin_arm64.tar.gz",
			want:  sum,
		},
		{
			name:  "binary-mode asterisk prefix",
			data:  sum + " *dot_1.2.3_darwin_arm64.tar.gz\n",
			asset: "dot_1.2.3_darwin_arm64.tar.gz",
			want:  sum,
		},
		{
			name: "picks the matching line among several",
			data: strings.Repeat("0", 64) + "  dot_1.2.3_linux_amd64.tar.gz\n" +
				sum + "  dot_1.2.3_darwin_arm64.tar.gz\n",
			asset: "dot_1.2.3_darwin_arm64.tar.gz",
			want:  sum,
		},
		{
			name:    "asset missing",
			data:    sum + "  dot_1.2.3_linux_amd64.tar.gz\n",
			asset:   "dot_1.2.3_darwin_arm64.tar.gz",
			wantErr: true,
		},
		{
			name:    "garbage and blank lines skipped",
			data:    "\nnot a checksum line\nshort  dot_1.2.3_darwin_arm64.tar.gz\n",
			asset:   "dot_1.2.3_darwin_arm64.tar.gz",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChecksums([]byte(tt.data), tt.asset)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("sum = %q, want %q", got, tt.want)
			}
		})
	}
}

// makeReleaseTarGz builds a .tar.gz holding a single "dot" file.
func makeReleaseTarGz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := "#!/bin/sh\necho dot\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "dot", Mode: 0755, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseServer serves /release.tar.gz and /checksums.txt for
// downloadVerifiedArchive tests.
func releaseServer(t *testing.T, archive []byte, checksums string, checksumsStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/release.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		if checksumsStatus != http.StatusOK {
			w.WriteHeader(checksumsStatus)
			return
		}
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func upgradeTestRunner() *exec.Runner {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return exec.NewRunner(false, logger)
}

func TestDownloadVerifiedArchive_OK(t *testing.T) {
	archive := makeReleaseTarGz(t)
	sum := sha256.Sum256(archive)
	asset := "dot_1.2.3_test.tar.gz"
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	srv := releaseServer(t, archive, checksums, http.StatusOK)

	dest := t.TempDir()
	err := downloadVerifiedArchive(context.Background(), upgradeTestRunner(),
		srv.URL+"/release.tar.gz", srv.URL+"/checksums.txt", asset, dest)
	if err != nil {
		t.Fatalf("downloadVerifiedArchive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "dot")); err != nil {
		t.Errorf("extracted binary missing: %v", err)
	}
}

func TestDownloadVerifiedArchive_Mismatch(t *testing.T) {
	archive := makeReleaseTarGz(t)
	asset := "dot_1.2.3_test.tar.gz"
	checksums := strings.Repeat("0", 64) + "  " + asset + "\n"
	srv := releaseServer(t, archive, checksums, http.StatusOK)

	dest := t.TempDir()
	err := downloadVerifiedArchive(context.Background(), upgradeTestRunner(),
		srv.URL+"/release.tar.gz", srv.URL+"/checksums.txt", asset, dest)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "dot")); !os.IsNotExist(statErr) {
		t.Error("binary must not be extracted on checksum mismatch")
	}
}

func TestDownloadVerifiedArchive_ChecksumsMissing(t *testing.T) {
	archive := makeReleaseTarGz(t)
	asset := "dot_1.2.3_test.tar.gz"
	srv := releaseServer(t, archive, "", http.StatusNotFound)

	dest := t.TempDir()
	err := downloadVerifiedArchive(context.Background(), upgradeTestRunner(),
		srv.URL+"/release.tar.gz", srv.URL+"/checksums.txt", asset, dest)
	if err == nil || !strings.Contains(err.Error(), "refusing to install unverified binary") {
		t.Fatalf("err = %v, want hard failure on missing checksums.txt", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "dot")); !os.IsNotExist(statErr) {
		t.Error("binary must not be extracted without verification")
	}
}

func TestDownloadVerifiedArchive_MetadataTimeout(t *testing.T) {
	archive := makeReleaseTarGz(t)
	asset := "dot_1.2.3_test.tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("/release.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := downloadVerifiedArchive(ctx, upgradeTestRunner(), srv.URL+"/release.tar.gz", srv.URL+"/checksums.txt", asset, t.TempDir())
	if err == nil {
		t.Fatal("expected delayed checksums metadata to fail")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("metadata cancellation took %s", elapsed)
	}
}

// A Homebrew install resolves through the Cellar on every prefix layout, and
// only there (#233); dot update hands it to that prefix's brew (#235).
func TestHomebrewDotFor(t *testing.T) {
	for _, tc := range []struct {
		path   string
		ok     bool
		prefix string
	}{
		{"/opt/homebrew/Cellar/dotfiles/2.70.30/bin/dot", true, "/opt/homebrew"},
		{"/usr/local/Cellar/dotfiles/2.70.30/bin/dot", true, "/usr/local"},
		{"/home/linuxbrew/.linuxbrew/Cellar/dotfiles/2.70.30/bin/dot", true, "/home/linuxbrew/.linuxbrew"},
		{"/home/user/.local/bin/dot", false, ""},
		{"/usr/local/bin/dot", false, ""},
		{"/opt/homebrew/bin/dot", false, ""}, // an unresolved copy is dot's to replace; installs symlink into the Cellar
	} {
		h, ok := homebrewDotFor(tc.path)
		if ok != tc.ok || h.prefix != tc.prefix || (ok && h.formula != "dotfiles") {
			t.Errorf("homebrewDotFor(%q) = %+v, %v; want prefix %q, %v", tc.path, h, ok, tc.prefix, tc.ok)
		}
	}
}
