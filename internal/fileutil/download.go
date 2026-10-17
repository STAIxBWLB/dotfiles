package fileutil

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

const refreshFile = ".dotfiles-refresh"

const (
	metadataHTTPTimeout       = 15 * time.Second
	bulkDialTimeout           = 10 * time.Second
	bulkTLSHandshakeTimeout   = 10 * time.Second
	bulkResponseHeaderTimeout = 15 * time.Second
)

// ArchiveLimits bounds untrusted archive resources before extraction. Values
// describe compressed input, individual output entries, all output entries,
// entry count, and the maximum decompression expansion ratio respectively.
type ArchiveLimits struct {
	MaxCompressedBytes     int64
	MaxEntryBytes          int64
	MaxTotalExtractedBytes int64
	MaxEntries             int
	MaxExpansionRatio      int64
}

// DefaultArchiveLimits is deliberately sized around the project's known
// release assets while leaving no unbounded extraction surface. The dot binary
// outgrew a 16 MiB entry limit at about v2.70.28 (20 MB), which stopped every
// self-update; the release build checks each binary against MaxEntryBytes
// (TestReleaseBinaryFitsArchiveLimits), so the next growth fails the release
// instead of the updater (#231).
var DefaultArchiveLimits = ArchiveLimits{
	MaxCompressedBytes:     201326592,
	MaxEntryBytes:          67108864,
	MaxTotalExtractedBytes: 402653184,
	MaxEntries:             4096,
	MaxExpansionRatio:      8,
}

// ValidateMeasured checks a known artifact's dimensions against this policy.
// It is intentionally public so source-pin callers can validate catalog data
// before they start a download.
func (l ArchiveLimits) ValidateMeasured(compressed, extracted, entries int64) error {
	if !withinArchiveLimit(compressed, l.MaxCompressedBytes) {
		return fmt.Errorf("compressed archive size %d exceeds limit %d", compressed, l.MaxCompressedBytes)
	}
	if !withinArchiveLimit(extracted, l.MaxTotalExtractedBytes) {
		return fmt.Errorf("extracted archive size %d exceeds limit %d", extracted, l.MaxTotalExtractedBytes)
	}
	if entries < 0 || entries > int64(l.MaxEntries) {
		return fmt.Errorf("archive entry count %d exceeds limit %d", entries, l.MaxEntries)
	}
	if compressed == 0 && extracted != 0 {
		return fmt.Errorf("archive has output without compressed input")
	}
	if compressed > 0 && extracted > compressed*l.MaxExpansionRatio {
		return fmt.Errorf("archive expansion ratio exceeds limit %d", l.MaxExpansionRatio)
	}
	return nil
}

func withinArchiveLimit(got, max int64) bool { return got >= 0 && got <= max }

type archiveBudget struct {
	limits  ArchiveLimits
	entries int
	total   int64
}

func (b *archiveBudget) reserve(name string, declared int64) error {
	if declared < 0 || declared > b.limits.MaxEntryBytes {
		return fmt.Errorf("archive entry %q exceeds per-entry limit", name)
	}
	if b.entries == b.limits.MaxEntries {
		return fmt.Errorf("archive has more than %d entries", b.limits.MaxEntries)
	}
	if declared > b.limits.MaxTotalExtractedBytes-b.total {
		return fmt.Errorf("archive entry %q exceeds total extracted limit", name)
	}
	b.entries++
	return nil
}

func (b *archiveBudget) add(name string, n int64) error {
	if n < 0 || n > b.limits.MaxEntryBytes || n > b.limits.MaxTotalExtractedBytes-b.total {
		return fmt.Errorf("archive entry %q exceeds extraction limit", name)
	}
	b.total += n
	return nil
}

func (b *archiveBudget) checkExpansion(compressed int64) error {
	if compressed == 0 && b.total > 0 {
		return errors.New("archive expands before compressed input is read")
	}
	if compressed > 0 && b.total > compressed*b.limits.MaxExpansionRatio {
		return errors.New("archive exceeds expansion ratio limit")
	}
	return nil
}

type countingLimitReader struct {
	r     io.Reader
	limit int64
	n     int64
}

func (r *countingLimitReader) Read(p []byte) (int, error) {
	if r.n > r.limit {
		return 0, fmt.Errorf("compressed archive exceeds limit %d", r.limit)
	}
	remaining := r.limit + 1 - r.n
	if remaining <= 0 {
		return 0, fmt.Errorf("compressed archive exceeds limit %d", r.limit)
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.r.Read(p)
	r.n += int64(n)
	if r.n > r.limit {
		return n, fmt.Errorf("compressed archive exceeds limit %d", r.limit)
	}
	return n, err
}

func newMetadataHTTPClient() *http.Client {
	return &http.Client{Timeout: metadataHTTPTimeout}
}

// MetadataHTTPClient returns the total-deadline client for small metadata
// requests that need request headers beyond DownloadMetadataFile.
func MetadataHTTPClient() *http.Client { return newMetadataHTTPClient() }

func newBulkHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: bulkDialTimeout, KeepAlive: 30 * time.Second}
	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = bulkTLSHandshakeTimeout
	transport.ResponseHeaderTimeout = bulkResponseHeaderTimeout
	transport.IdleConnTimeout = 90 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{Transport: transport}
}

// httpGetWithRetry performs an HTTP GET with exponential backoff on transient failures.
// Retries on network errors and 5xx responses. 4xx responses returned immediately.
func httpGetWithRetry(ctx context.Context, client *http.Client, url string, attempts int) (*http.Response, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode < 500 {
				return resp, nil
			}
			lastErr = errorsJoin(fmt.Errorf("HTTP %d", resp.StatusCode), resp.Body.Close())
		} else {
			lastErr = err
		}
		if i < attempts-1 {
			select {
			case <-time.After(time.Duration(1<<i) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", attempts, lastErr)
}

// DownloadAndExtractTarGz downloads a .tar.gz archive and extracts it.
func DownloadAndExtractTarGz(ctx context.Context, runner *exec.Runner, url, destDir string, stripComponents int) error {
	if runner.DryRun {
		runner.Logger.Info("dry-run: download+extract", "url", url, "dest", destDir)
		return nil
	}

	resp, err := httpGetWithRetry(ctx, newBulkHTTPClient(), url, 3)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	if err := checkContentLength(resp, DefaultArchiveLimits); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	return ExtractTarGz(&countingLimitReader{r: resp.Body, limit: DefaultArchiveLimits.MaxCompressedBytes}, destDir, stripComponents)
}

// DownloadFile downloads url to destPath (0644) and returns the hex-encoded
// SHA-256 of the response body, so callers can verify the artifact before
// using it. Respects dry-run (logs and returns "", nil).
func DownloadFile(ctx context.Context, runner *exec.Runner, url, destPath string) (string, error) {
	return downloadFileWithLimits(ctx, runner, newBulkHTTPClient(), url, destPath, DefaultArchiveLimits)
}

// DownloadMetadataFile uses a total deadline for small control-plane files
// such as checksums and release metadata. Large archive transfers must use
// DownloadFile so a steadily progressing body is not cut off by this limit.
func DownloadMetadataFile(ctx context.Context, runner *exec.Runner, url, destPath string) (string, error) {
	return downloadFileWithLimits(ctx, runner, newMetadataHTTPClient(), url, destPath, DefaultArchiveLimits)
}

func downloadFileWithLimits(ctx context.Context, runner *exec.Runner, client *http.Client, url, destPath string, limits ArchiveLimits) (sum string, err error) {
	if runner.DryRun {
		runner.Logger.Info("dry-run: download", "url", url, "dest", destPath)
		return "", nil
	}

	resp, err := httpGetWithRetry(ctx, client, url, 3)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
		if closeErr := resp.Body.Close(); closeErr != nil {
			return "", errorsJoin(statusErr, closeErr)
		}
		return "", statusErr
	}
	if err := checkContentLength(resp, limits); err != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errorsJoin(err, closeErr)
		}
		return "", fmt.Errorf("download %s: %w", url, err)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errorsJoin(err, closeErr)
		}
		return "", fmt.Errorf("creating %s: %w", destPath, err)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			if removeErr := os.Remove(destPath); removeErr != nil && !os.IsNotExist(removeErr) {
				err = errorsJoin(err, fmt.Errorf("removing partial %s: %w", destPath, removeErr))
			}
		}
	}()

	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), &countingLimitReader{r: resp.Body, limit: limits.MaxCompressedBytes})
	closeFileErr := f.Close()
	closeBodyErr := resp.Body.Close()
	if copyErr != nil || closeFileErr != nil || closeBodyErr != nil {
		return "", fmt.Errorf("saving download: %w", errorsJoin(copyErr, closeFileErr, closeBodyErr))
	}
	succeeded = true
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExtractTarGz extracts a gzipped tar stream into destDir, stripping
// stripComponents leading path elements.
func ExtractTarGz(r io.Reader, destDir string, stripComponents int) error {
	return extractTarGzWithLimits(r, destDir, stripComponents, DefaultArchiveLimits)
}

func extractTarGzWithLimits(r io.Reader, destDir string, stripComponents int, limits ArchiveLimits) error {
	if stripComponents < 0 {
		return fmt.Errorf("strip components must be non-negative")
	}
	compressed := &countingLimitReader{r: r, limit: limits.MaxCompressedBytes}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	root, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("opening extraction root: %w", err)
	}
	defer root.Close()

	budget := archiveBudget{limits: limits}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		name, ok := strippedArchiveName(hdr.Name, stripComponents)
		if !ok {
			continue
		}
		if err := validateArchiveTarget(name); err != nil {
			return fmt.Errorf("tar entry %q: %w", hdr.Name, err)
		}
		// Only directory entries may carry the trailing "/" that normalisation
		// strips; a file or link named "x/" is malformed and must not become "x".
		if hdr.Typeflag != tar.TypeDir && strings.HasSuffix(hdr.Name, "/") {
			return fmt.Errorf("tar entry %q: directory name on a non-directory entry", hdr.Name)
		}
		if err := budget.reserve(hdr.Name, hdr.Size); err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if hdr.Mode&07000 != 0 {
				return fmt.Errorf("tar entry %q has privileged mode", hdr.Name)
			}
			if err := root.MkdirAll(name, 0755); err != nil {
				return fmt.Errorf("creating tar directory %q: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if hdr.Mode&07000 != 0 {
				return fmt.Errorf("tar entry %q has privileged mode", hdr.Name)
			}
			if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
				return fmt.Errorf("creating tar parent %q: %w", hdr.Name, err)
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safeArchiveMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("creating tar file %q: %w", hdr.Name, err)
			}
			n, copyErr := copyArchiveEntry(f, tr, limits.MaxEntryBytes)
			closeErr := f.Close()
			if copyErr != nil {
				return fmt.Errorf("writing tar entry %q: %w", hdr.Name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("closing tar entry %q: %w", hdr.Name, closeErr)
			}
			if err := budget.add(hdr.Name, n); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if hdr.Mode&07000 != 0 {
				return fmt.Errorf("tar entry %q has privileged mode", hdr.Name)
			}
			if err := validateArchiveLink(name, hdr.Linkname); err != nil {
				return fmt.Errorf("tar link %q: %w", hdr.Name, err)
			}
			if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
				return fmt.Errorf("creating tar link parent %q: %w", hdr.Name, err)
			}
			if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing existing tar link %q: %w", hdr.Name, err)
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return fmt.Errorf("creating tar link %q: %w", hdr.Name, err)
			}
		default:
			return fmt.Errorf("tar entry %q has unsupported type %d", hdr.Name, hdr.Typeflag)
		}
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return fmt.Errorf("reading tar archive tail: %w", err)
	}
	if err := budget.checkExpansion(compressed.n); err != nil {
		return err
	}

	return nil
}

// DownloadAndExtractZip downloads a .zip archive and extracts it.
func DownloadAndExtractZip(ctx context.Context, runner *exec.Runner, url, destDir string) error {
	if runner.DryRun {
		runner.Logger.Info("dry-run: download+extract zip", "url", url, "dest", destDir)
		return nil
	}

	// Download to temp file
	resp, err := httpGetWithRetry(ctx, newBulkHTTPClient(), url, 3)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	if err := checkContentLength(resp, DefaultArchiveLimits); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	tmpFile, err := os.CreateTemp("", "dotfiles-*.zip")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, &countingLimitReader{r: resp.Body, limit: DefaultArchiveLimits.MaxCompressedBytes}); err != nil {
		return fmt.Errorf("saving download: %w", err)
	}
	tmpFile.Close()

	// Extract
	r, err := zip.OpenReader(tmpFile.Name())
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer r.Close()

	return ExtractZip(&r.Reader, destDir)
}

// ExtractZip extracts a validated zip reader into destDir using the same
// rooted containment policy as tar extraction.
func ExtractZip(r *zip.Reader, destDir string) error {
	return extractZipWithLimits(r, destDir, DefaultArchiveLimits)
}

func extractZipWithLimits(r *zip.Reader, destDir string, limits ArchiveLimits) error {
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("opening extraction root: %w", err)
	}
	defer root.Close()

	budget := archiveBudget{limits: limits}
	var compressed int64
	for _, f := range r.File {
		name, ok := normalizeArchiveTarget(f.Name)
		if !ok {
			continue
		}
		if err := validateArchiveTarget(name); err != nil {
			return fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
		if err := budget.reserve(f.Name, int64(f.UncompressedSize64)); err != nil {
			return err
		}
		if f.UncompressedSize64 > uint64(limits.MaxEntryBytes) || f.CompressedSize64 > uint64(limits.MaxCompressedBytes) {
			return fmt.Errorf("zip entry %q exceeds archive limit", f.Name)
		}
		if compressed > limits.MaxCompressedBytes-int64(f.CompressedSize64) {
			return fmt.Errorf("zip archive exceeds compressed limit")
		}
		compressed += int64(f.CompressedSize64)
		mode := f.Mode()
		if mode&os.ModeSetuid != 0 || mode&os.ModeSetgid != 0 || mode&os.ModeSticky != 0 {
			return fmt.Errorf("zip entry %q has privileged mode", f.Name)
		}

		switch {
		case f.FileInfo().IsDir():
			if err := root.MkdirAll(name, 0755); err != nil {
				return fmt.Errorf("creating zip directory %q: %w", f.Name, err)
			}
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("opening zip link %q: %w", f.Name, err)
			}
			linkBytes, readErr := io.ReadAll(io.LimitReader(rc, limits.MaxEntryBytes+1))
			closeErr := rc.Close()
			if readErr != nil || closeErr != nil || int64(len(linkBytes)) > limits.MaxEntryBytes {
				return fmt.Errorf("reading zip link %q: %w", f.Name, errorsJoin(readErr, closeErr))
			}
			linkname := string(linkBytes)
			if err := validateArchiveLink(name, linkname); err != nil {
				return fmt.Errorf("zip link %q: %w", f.Name, err)
			}
			if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
				return fmt.Errorf("creating zip link parent %q: %w", f.Name, err)
			}
			if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing existing zip link %q: %w", f.Name, err)
			}
			if err := root.Symlink(linkname, name); err != nil {
				return fmt.Errorf("creating zip link %q: %w", f.Name, err)
			}
			if err := budget.add(f.Name, int64(len(linkBytes))); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
				return fmt.Errorf("creating zip parent %q: %w", f.Name, err)
			}
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("opening zip entry %q: %w", f.Name, err)
			}
			out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safeArchiveMode(int64(mode.Perm())))
			if err != nil {
				rc.Close()
				return fmt.Errorf("creating zip file %q: %w", f.Name, err)
			}
			n, copyErr := copyArchiveEntry(out, rc, limits.MaxEntryBytes)
			closeErr := errorsJoin(rc.Close(), out.Close())
			if copyErr != nil || closeErr != nil {
				return fmt.Errorf("writing zip entry %q: %w", f.Name, errorsJoin(copyErr, closeErr))
			}
			if err := budget.add(f.Name, n); err != nil {
				return err
			}
		default:
			return fmt.Errorf("zip entry %q has unsupported type", f.Name)
		}
	}
	if err := budget.checkExpansion(compressed); err != nil {
		return err
	}
	return nil
}

func checkContentLength(resp *http.Response, limits ArchiveLimits) error {
	if resp.ContentLength > limits.MaxCompressedBytes {
		return fmt.Errorf("compressed archive size %d exceeds limit %d", resp.ContentLength, limits.MaxCompressedBytes)
	}
	if value := resp.Header.Get("Content-Length"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid Content-Length %q", value)
		}
		if n > limits.MaxCompressedBytes {
			return fmt.Errorf("compressed archive size %d exceeds limit %d", n, limits.MaxCompressedBytes)
		}
	}
	return nil
}

func strippedArchiveName(name string, stripComponents int) (string, bool) {
	parts := strings.SplitN(name, "/", stripComponents+1)
	if len(parts) <= stripComponents {
		return "", false
	}
	return normalizeArchiveTarget(parts[stripComponents])
}

// normalizeArchiveTarget converts an archive member name into the extraction
// target. Directory entries end in "/" in both tar and zip, and os.Root.MkdirAll
// rejects a trailing separator on Go 1.26 (mkdirat "a/": no such file or
// directory), so the name is cleaned here for both extractors. An empty name or
// one that cleans to "." is the extraction root itself: nothing to create.
func normalizeArchiveTarget(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." {
		return "", false
	}
	return name, true
}

func validateArchiveTarget(name string) error {
	if strings.IndexByte(name, 0) >= 0 || hasDriveQualifiedPrefix(name) || !filepath.IsLocal(name) {
		return fmt.Errorf("unsafe extraction target %q", name)
	}
	return nil
}

func validateArchiveLink(finalTarget, linkname string) error {
	if linkname == "" || strings.IndexByte(linkname, 0) >= 0 || hasDriveQualifiedPrefix(linkname) || filepath.IsAbs(linkname) {
		return fmt.Errorf("unsafe link target %q", linkname)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(finalTarget), filepath.FromSlash(linkname)))
	if !filepath.IsLocal(resolved) {
		return fmt.Errorf("link target %q escapes extraction root", linkname)
	}
	return nil
}

func hasDriveQualifiedPrefix(name string) bool {
	return len(name) >= 2 && name[1] == ':' && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z'))
}

func safeArchiveMode(mode int64) os.FileMode {
	if mode&0111 != 0 {
		return 0755
	}
	return 0644
}

func copyArchiveEntry(dst io.Writer, src io.Reader, max int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, max+1))
	if err != nil {
		return n, err
	}
	if n > max {
		return n, fmt.Errorf("entry exceeds per-entry limit %d", max)
	}
	return n, nil
}

func errorsJoin(errs ...error) error { return errors.Join(errs...) }

// NeedsRefresh checks if a resource needs refreshing based on a timestamp file.
func NeedsRefresh(dir string, period time.Duration) bool {
	path := filepath.Join(dir, refreshFile)
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > period
}

// MarkRefreshed updates the refresh timestamp.
func MarkRefreshed(runner *exec.Runner, dir string) error {
	path := filepath.Join(dir, refreshFile)
	return runner.WriteFile(path, []byte(time.Now().Format(time.RFC3339)), 0644)
}
