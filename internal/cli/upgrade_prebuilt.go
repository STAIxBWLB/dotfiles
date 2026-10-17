package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

const (
	prebuiltMaxCompressedBytes = int64(32 << 20)
	prebuiltMaxBinaryBytes     = int64(64 << 20)
	prebuiltMaxDocBytes        = int64(1 << 20)
	prebuiltMaxExpandedBytes   = int64(67 << 20)
	prebuiltMaxMembers         = 3
	prebuiltMaxChecksumBytes   = int64(1 << 20)
)

var errPrebuiltExpansionLimit = errors.New("release archive exceeds expanded size limit")

var stableReleaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

var upgradeAcquirePrebuilt = resourceguard.AcquireVerifiedPrebuiltUpdate

type verifiedPrebuiltPlan struct {
	Version      string
	AssetName    string
	DownloadURL  string
	ChecksumsURL string
}

func planVerifiedPrebuilt(tag, goos, goarch string) (verifiedPrebuiltPlan, error) {
	if !stableReleaseTag.MatchString(tag) {
		return verifiedPrebuiltPlan{}, fmt.Errorf("release tag %q is not an exact stable version", tag)
	}
	if err := validatePrebuiltPlatform(goos); err != nil {
		return verifiedPrebuiltPlan{}, err
	}
	if goarch != "amd64" && goarch != "arm64" {
		return verifiedPrebuiltPlan{}, fmt.Errorf("verified prebuilt updates are unsupported on %s/%s", goos, goarch)
	}
	version := strings.TrimPrefix(tag, "v")
	asset := fmt.Sprintf("dot_%s_%s_%s.tar.gz", version, goos, goarch)
	base := "https://github.com/" + githubRepo + "/releases/download/" + tag + "/"
	return verifiedPrebuiltPlan{Version: version, AssetName: asset, DownloadURL: base + asset, ChecksumsURL: base + "checksums.txt"}, nil
}

func validatePrebuiltPlatform(goos string) error {
	if goos != "darwin" && goos != "linux" {
		return fmt.Errorf("verified prebuilt updates are unsupported on %s", goos)
	}
	return nil
}

func isTrustedReleaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Host == "github.com" && strings.HasPrefix(u.Path, "/"+githubRepo+"/releases/download/")
}

func prebuiltHTTPClient(timeout time.Duration) *http.Client {
	allowed := map[string]bool{
		"github.com": true, "release-assets.githubusercontent.com": true,
		"objects.githubusercontent.com": true,
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || !allowed[req.URL.Hostname()] {
				return fmt.Errorf("untrusted release redirect to %s", req.URL.Redacted())
			}
			if len(via) >= 5 {
				return fmt.Errorf("too many release redirects")
			}
			return nil
		},
	}
}

func downloadVerifiedPrebuilt(ctx context.Context, plan verifiedPrebuiltPlan, dir string) (string, []byte, error) {
	checksums, err := fetchPrebuiltChecksums(ctx, plan.ChecksumsURL)
	if err != nil {
		return "", nil, err
	}
	return downloadVerifiedPrebuiltWithChecksums(ctx, plan, dir, checksums)
}

func downloadVerifiedPrebuiltWithChecksums(ctx context.Context, plan verifiedPrebuiltPlan, dir string, checksums []byte) (string, []byte, error) {
	goos, goarch, err := supportedNativeReleaseTarget()
	if err != nil {
		return "", nil, err
	}
	expectedPlan, err := planVerifiedPrebuilt("v"+plan.Version, goos, goarch)
	if err != nil || plan != expectedPlan {
		return "", nil, fmt.Errorf("release plan does not match the exact native GitHub release")
	}
	archivePath := filepath.Join(dir, plan.AssetName)
	sum, size, err := downloadPrebuiltAsset(ctx, plan.DownloadURL, archivePath)
	if err != nil {
		return "", nil, err
	}
	if size > prebuiltMaxCompressedBytes {
		return "", nil, fmt.Errorf("release archive is %d bytes, maximum is %d", size, prebuiltMaxCompressedBytes)
	}
	expectedHash, err := parseUniqueChecksum(checksums, plan.AssetName)
	if err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(sum, expectedHash) {
		return "", nil, fmt.Errorf("checksum mismatch for %s", plan.AssetName)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return "", nil, err
	}
	defer archive.Close()
	binaryPath := filepath.Join(dir, "dot")
	if err := extractSinglePrebuilt(archive, binaryPath); err != nil {
		return "", nil, err
	}
	return binaryPath, checksums, nil
}

func downloadPrebuiltAsset(ctx context.Context, rawURL, dest string) (string, int64, error) {
	if !isTrustedReleaseURL(rawURL) {
		return "", 0, fmt.Errorf("untrusted release URL")
	}
	client := prebuiltHTTPClient(2 * time.Minute)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", 0, err
		}
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("release archive returned HTTP %d", resp.StatusCode)
				_ = resp.Body.Close()
			} else if resp.ContentLength > prebuiltMaxCompressedBytes {
				_ = resp.Body.Close()
				return "", 0, fmt.Errorf("release archive exceeds %d bytes", prebuiltMaxCompressedBytes)
			} else {
				sum, size, saveErr := saveBoundedDownload(resp.Body, dest, prebuiltMaxCompressedBytes)
				closeErr := resp.Body.Close()
				if saveErr == nil && closeErr == nil {
					return sum, size, nil
				}
				lastErr = errors.Join(saveErr, closeErr)
				_ = os.Remove(dest)
			}
		} else {
			lastErr = err
		}
		if attempt < 2 {
			select {
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			case <-ctx.Done():
				return "", 0, ctx.Err()
			}
		}
	}
	return "", 0, fmt.Errorf("downloading verified release: %w", lastErr)
}

func saveBoundedDownload(r io.Reader, dest string, limit int64) (string, int64, error) {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, limit+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return "", n, errors.Join(copyErr, closeErr)
	}
	if n > limit {
		return "", n, fmt.Errorf("download exceeded %d bytes", limit)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fetchPrebuiltChecksums(ctx context.Context, rawURL string) ([]byte, error) {
	if !isTrustedReleaseURL(rawURL) {
		return nil, fmt.Errorf("untrusted checksums URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := prebuiltHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching release checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checksums returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > prebuiltMaxChecksumBytes {
		return nil, fmt.Errorf("checksums file exceeds %d bytes", prebuiltMaxChecksumBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, prebuiltMaxChecksumBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > prebuiltMaxChecksumBytes {
		return nil, fmt.Errorf("checksums file exceeds %d bytes", prebuiltMaxChecksumBytes)
	}
	return body, nil
}

func parseUniqueChecksum(data []byte, asset string) (string, error) {
	var found string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		sum := fields[0]
		if len(sum) != sha256.Size*2 {
			return "", fmt.Errorf("invalid checksum entry for %s", asset)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", fmt.Errorf("invalid checksum entry for %s", asset)
		}
		if found != "" {
			return "", fmt.Errorf("duplicate checksum entries for %s", asset)
		}
		found = sum
	}
	if found == "" {
		return "", fmt.Errorf("asset %s not listed in checksums.txt", asset)
	}
	return found, nil
}

func extractSinglePrebuilt(r io.ReadSeeker, destination string) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("opening release archive: %w", err)
	}
	bounded := &expandedLimitReader{reader: gz, limit: prebuiltMaxExpandedBytes}
	entries, expandedSize, err := scanPrebuiltTar(bounded)
	closeErr := gz.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err = gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("reopening release archive: %w", err)
	}
	defer gz.Close()
	limited := &expandedLimitReader{reader: gz, limit: prebuiltMaxExpandedBytes}
	tr := tar.NewReader(limited)
	seen := make(map[string]bool, len(entries))
	var file *os.File
	var binarySize int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanupExtracted(file, destination)
			return fmt.Errorf("reading release archive: %w", err)
		}
		if !allowedPrebuiltMember(h.Name) || h.Typeflag != tar.TypeReg || h.Mode&07000 != 0 || seen[h.Name] {
			cleanupExtracted(file, destination)
			return fmt.Errorf("release archive contains an unexpected or duplicate member %q", h.Name)
		}
		seen[h.Name] = true
		maxSize := prebuiltMaxDocBytes
		if h.Name == "dot" {
			maxSize = prebuiltMaxBinaryBytes
		}
		if h.Size <= 0 || h.Size > maxSize {
			cleanupExtracted(file, destination)
			return fmt.Errorf("release archive member %q has invalid size %d", h.Name, h.Size)
		}
		if h.Name != "dot" {
			if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil {
				cleanupExtracted(file, destination)
				return fmt.Errorf("reading release document %q: %w", h.Name, err)
			}
			continue
		}
		binarySize = h.Size
		file, err = os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			return fmt.Errorf("creating extracted executable: %w", err)
		}
		written, copyErr := io.CopyN(file, tr, h.Size)
		if copyErr != nil {
			cleanupExtracted(file, destination)
			return fmt.Errorf("reading release executable: %w", copyErr)
		}
		if written != binarySize {
			cleanupExtracted(file, destination)
			return fmt.Errorf("release executable has invalid size: wrote %d of %d bytes", written, binarySize)
		}
	}
	if !seen["dot"] || len(seen) != len(entries) {
		cleanupExtracted(file, destination)
		return fmt.Errorf("release archive must contain one dot executable and only bounded release documents")
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		cleanupExtracted(file, destination)
		return fmt.Errorf("checking release archive trailer: %w", err)
	}
	if limited.total != expandedSize {
		cleanupExtracted(file, destination)
		return fmt.Errorf("release archive changed while extracting")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	if err := os.Chmod(destination, 0o755); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}

func allowedPrebuiltMember(name string) bool {
	switch name {
	case "dot", "LICENSE", "README.md":
		return true
	default:
		return false
	}
}

func cleanupExtracted(file *os.File, destination string) {
	if file != nil {
		_ = file.Close()
		_ = os.Remove(destination)
	}
}

type expandedLimitReader struct {
	reader io.Reader
	limit  int64
	total  int64
}

func (r *expandedLimitReader) Read(p []byte) (int, error) {
	if r.total > r.limit {
		return 0, errPrebuiltExpansionLimit
	}
	remaining := r.limit - r.total
	if int64(len(p)) > remaining+1 {
		p = p[:remaining+1]
	}
	n, err := r.reader.Read(p)
	r.total += int64(n)
	if r.total > r.limit {
		return n, errPrebuiltExpansionLimit
	}
	return n, err
}

func scanPrebuiltTar(r io.Reader) (map[string]bool, int64, error) {
	entries := make(map[string]bool, prebuiltMaxMembers)
	var expanded *expandedLimitReader
	if bounded, ok := r.(*expandedLimitReader); ok {
		expanded = bounded
	}
	var header [512]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return nil, 0, fmt.Errorf("reading tar header: %w", err)
		}
		if allZeroBytes(header[:]) {
			var second [512]byte
			if _, err := io.ReadFull(r, second[:]); err != nil || !allZeroBytes(second[:]) {
				return nil, 0, fmt.Errorf("release archive has an incomplete tar trailer")
			}
			if err := drainTarPadding(r); err != nil {
				return nil, 0, err
			}
			break
		}
		if err := validateTarChecksum(header[:]); err != nil {
			return nil, 0, err
		}
		typeFlag := header[156]
		if typeFlag != 0 && typeFlag != '0' {
			return nil, 0, fmt.Errorf("release archive contains unsupported tar type %q", typeFlag)
		}
		name := tarFieldString(header[0:100])
		prefix := tarFieldString(header[345:500])
		if prefix != "" {
			name = prefix + "/" + name
		}
		if !allowedPrebuiltMember(name) || entries[name] {
			return nil, 0, fmt.Errorf("release archive contains an unexpected or duplicate member %q", name)
		}
		entries[name] = true
		if len(entries) > prebuiltMaxMembers {
			return nil, 0, fmt.Errorf("release archive exceeds %d members", prebuiltMaxMembers)
		}
		size, err := parseTarOctal(header[124:136])
		if err != nil {
			return nil, 0, err
		}
		maxSize := prebuiltMaxDocBytes
		if name == "dot" {
			maxSize = prebuiltMaxBinaryBytes
		}
		if size <= 0 || size > maxSize {
			return nil, 0, fmt.Errorf("release archive member %q has invalid size %d", name, size)
		}
		padded := size + (512-size%512)%512
		if _, err := io.CopyN(io.Discard, r, padded); err != nil {
			return nil, 0, fmt.Errorf("reading tar member %q: %w", name, err)
		}
	}
	if !entries["dot"] {
		return nil, 0, fmt.Errorf("release archive has no root dot executable")
	}
	if expanded == nil {
		return nil, 0, fmt.Errorf("release archive scanner has no expanded-byte counter")
	}
	return entries, expanded.total, nil
}

func drainTarPadding(r io.Reader) error {
	var block [32 * 1024]byte
	for {
		n, err := r.Read(block[:])
		for _, b := range block[:n] {
			if b != 0 {
				return fmt.Errorf("release archive contains nonzero data after tar trailer")
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar padding: %w", err)
		}
	}
}

func allZeroBytes(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func tarFieldString(field []byte) string {
	if i := strings.IndexByte(string(field), 0); i >= 0 {
		field = field[:i]
	}
	return string(field)
}

func parseTarOctal(field []byte) (int64, error) {
	text := strings.Trim(string(field), "\x00 ")
	if text == "" {
		return 0, nil
	}
	for _, digit := range text {
		if digit < '0' || digit > '7' {
			return 0, fmt.Errorf("release archive has invalid octal tar size %q", text)
		}
	}
	size, err := strconv.ParseInt(text, 8, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("release archive has invalid octal tar size %q", text)
	}
	return size, nil
}

func validateTarChecksum(header []byte) error {
	stored, err := parseTarOctal(header[148:156])
	if err != nil {
		return fmt.Errorf("release archive has invalid tar checksum: %w", err)
	}
	var sum int64
	for i, b := range header {
		if i >= 148 && i < 156 {
			sum += int64(' ')
		} else {
			sum += int64(b)
		}
	}
	if sum != stored {
		return fmt.Errorf("release archive tar checksum mismatch")
	}
	return nil
}

func verifyExactPrebuiltBinary(ctx context.Context, path, version, goos, goarch string) error {
	if err := verifyNativeBinaryTarget(path, goos, goarch); err != nil {
		return fmt.Errorf("downloaded executable is not the selected native target: %w", err)
	}
	got, err := readDotBinaryVersion(ctx, path)
	if err != nil {
		return fmt.Errorf("running verified release --version: %w", err)
	}
	if got != version {
		return fmt.Errorf("verified executable reports %q, expected exact dot version %s", got, version)
	}
	return nil
}

func installStandalonePrebuilt(ctx context.Context, p *Printer, source, destination, version, goos, goarch string) error {
	if err := verifyExactPrebuiltBinary(ctx, source, version, goos, goarch); err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if int64(len(data)) == 0 || int64(len(data)) > prebuiltMaxBinaryBytes {
		return fmt.Errorf("verified executable size %d is outside the prebuilt limit", len(data))
	}
	release, err := upgradeAcquirePrebuilt(ctx)
	if err != nil {
		return fmt.Errorf("the verified prebuilt update needs the maintenance slot: %w; retry later", err)
	}
	defer release()
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".dot.*.new")
	if err != nil {
		return fmt.Errorf("creating staging file in %s: %w", filepath.Dir(destination), err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing staging executable: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		cleanup()
		return fmt.Errorf("replacing binary %s: %w", destination, err)
	}
	p.Line("Upgraded to %s", version)
	p.Line("Binary: %s", destination)
	return nil
}
