// Package aisettings backs up and restores portable AI assistant settings.
//
// The default manifest intentionally avoids auth tokens, caches, histories,
// sessions, logs, and generated/system bundles. Use IncludeAuth only when the
// operator explicitly wants local credentials included in an archive.
package aisettings

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/snapstore"
)

const (
	archiveVersion = 1
	homePrefix     = "home"
	maxSecretScan  = 4 << 20
)

var (
	assignmentPattern     = regexp.MustCompile(`(?i)["']?([a-z0-9_.-]+)["']?\s*[:=]\s*(?:"([^"\r\n]*)"|'([^'\r\n]*)'|([^\s,#}\]\r\n]+))`)
	positionalFlagPattern = regexp.MustCompile(`(?i)["'](--?[a-z0-9_-]+)["']\s*,\s*["']([^"']+)["']`)
	shellFlagPattern      = regexp.MustCompile(`(?i)(?:^|\s)(--?[a-z0-9_-]+)(?:=|\s+)["']?([^\s"']+)`)
)

const (
	claudeStateRelPath = ".claude.json"
	codexConfigRelPath = ".codex/config.toml"
)

// Entry describes one home-relative path managed by the AI config archive.
type Entry struct {
	Tool        string `yaml:"tool"`
	Path        string `yaml:"path"`
	Description string `yaml:"description,omitempty"`
	Auth        bool   `yaml:"auth,omitempty"`
}

// EntrySummary captures the result for one manifest entry.
type EntrySummary struct {
	Tool    string `yaml:"tool"`
	Path    string `yaml:"path"`
	Auth    bool   `yaml:"auth,omitempty"`
	Copied  int    `yaml:"copied"`
	Missing int    `yaml:"missing"`
	Skipped int    `yaml:"skipped,omitempty"`
	Files   int    `yaml:"files"`
	Bytes   int64  `yaml:"bytes"`
}

// Summary aggregates a backup, restore, export, or import operation.
type Summary struct {
	Version string
	Path    string
	Entries []EntrySummary
	Files   int
	Bytes   int64

	// PreBackupPath is set by restore/import when existing live files were
	// preserved before being overwritten ("" when nothing was preserved).
	PreBackupPath string
}

// Meta records snapshot/archive provenance.
type Meta struct {
	Version     string    `yaml:"version"`
	Tag         string    `yaml:"tag,omitempty"`
	Hostname    string    `yaml:"hostname,omitempty"`
	CreatedAt   time.Time `yaml:"created_at"`
	IncludeAuth bool      `yaml:"include_auth"`
	User        string    `yaml:"user,omitempty"`
}

// ArchiveManifest records which entries were present when the snapshot was made.
type ArchiveManifest struct {
	Schema      int            `yaml:"schema"`
	IncludeAuth bool           `yaml:"include_auth"`
	Entries     []EntrySummary `yaml:"entries"`
}

// Snapshot is one versioned backup under <root>/ai-config/<host>.
type Snapshot struct {
	Version     string
	Tag         string
	CreatedAt   time.Time
	Path        string
	IsLatest    bool
	IncludeAuth bool
	Files       int
}

// Status reports live and backup presence for an entry.
type Status struct {
	Entry         Entry
	PresentLive   bool
	PresentBackup bool
}

// Engine executes AI config backup/restore/export/import operations.
type Engine struct {
	Runner   *exec.Runner
	HomeDir  string
	Root     string
	Hostname string
	User     string
}

// Entries returns the static AI config manifest.
func Entries(includeAuth bool) []Entry {
	// Skill directories are intentionally absent. The Maru app (formerly
	// Anchor) owns skill federation, runtime links, and source reconciliation;
	// dotfiles only archives environment and agent settings around that
	// boundary.
	entries := []Entry{
		{Tool: "claude", Path: ".config/claude/settings.json", Description: "dot-managed Claude settings"},
		{Tool: "claude", Path: ".claude/settings.json", Description: "Claude Code settings"},
		{Tool: "claude", Path: ".claude/CLAUDE.md", Description: "Claude Code global instructions"},
		{Tool: "claude", Path: ".claude/agents", Description: "Claude agents"},
		{Tool: "claude", Path: ".claude/commands", Description: "Claude commands"},
		{Tool: "claude", Path: ".claude/hooks", Description: "Claude hooks"},
		{Tool: "claude", Path: ".claude.json", Description: "Claude Code state and MCP config"},
		{Tool: "codex", Path: ".codex/AGENTS.md", Description: "Codex global instructions"},
		{Tool: "codex", Path: codexConfigRelPath, Description: "Codex config and MCP servers"},
		{Tool: "codex", Path: ".codex/prompts", Description: "Codex prompts"},
		{Tool: "codex", Path: ".codex/rules", Description: "Codex rules"},
		{Tool: "agents", Path: AgentsSSOTRelPath, Description: "AI agents SSOT"},
		{Tool: "kiro", Path: ".kiro/steering/AGENTS.md", Description: "Kiro global steering instructions"},
		{Tool: "kiro", Path: ".kiro/settings/mcp.json", Description: "Kiro CLI MCP servers"},
		{Tool: "kimi", Path: ".kimi-code/AGENTS.md", Description: "Kimi Code global instructions"},
		{Tool: "kimi", Path: ".kimi-code/mcp.json", Description: "Kimi Code MCP servers"},
		// Unlike .codex/config.toml this needs no restore guard: kimi stores
		// api_key = "" and points at its credentials by path
		// (storage = "file", key = "oauth/kimi-code") rather than hash-keying
		// them to the server definitions, so a whole-file restore orphans
		// nothing.
		{Tool: "kimi", Path: ".kimi-code/config.toml", Description: "Kimi Code config, providers, and models"},
		// pi has no MCP support by design; its agent home keeps global
		// instructions, settings, and the provider/model catalog. models.json
		// is what makes settings.json restorable — settings.json names a
		// provider ("dgx") that only models.json defines — so the two travel
		// together, the way kimi's config.toml does. Sessions, extensions, and
		// skills are machine state dot never restores.
		{Tool: "pi", Path: ".pi/agent/AGENTS.md", Description: "pi global instructions"},
		{Tool: "pi", Path: ".pi/agent/settings.json", Description: "pi settings"},
		{Tool: "pi", Path: ".pi/agent/models.json", Description: "pi providers and model catalog"},
		// Qwen's sessions, memories, and usage record are machine state dot
		// never restores; settings.json holds the dot-managed claude-mem MCP
		// entry alongside Qwen's own model/auth settings.
		{Tool: "qwen", Path: ".qwen/AGENTS.md", Description: "Qwen Code global instructions"},
		{Tool: "grok", Path: ".grok/AGENTS.md", Description: "Grok global instructions"},
		{Tool: "opencode", Path: ".config/opencode/AGENTS.md", Description: "OpenCode global instructions"},
		{Tool: "qwen", Path: ".qwen/settings.json", Description: "Qwen Code settings and MCP servers"},
		{Tool: "antigravity", Path: ".gemini/GEMINI.md", Description: "Antigravity/Gemini global instructions"},
		{Tool: "antigravity", Path: ".gemini/config/mcp_config.json", Description: "Antigravity shared MCP config"},
		{Tool: "antigravity", Path: ".gemini/config/hooks.json", Description: "Antigravity global hooks"},
		{Tool: "antigravity", Path: ".gemini/config/rules", Description: "Antigravity global rules"},
		{Tool: "antigravity", Path: ".gemini/config/plugins", Description: "Antigravity global plugins"},
		{Tool: "antigravity", Path: ".gemini/antigravity/mcp_config.json", Description: "Antigravity app MCP config"},
		{Tool: "antigravity", Path: ".gemini/antigravity/browserAllowlist.txt", Description: "Antigravity browser allowlist"},
		{Tool: "antigravity", Path: ".gemini/antigravity-cli/settings.json", Description: "Antigravity CLI settings"},
		{Tool: "antigravity", Path: ".gemini/antigravity-cli/keybindings.json", Description: "Antigravity CLI keybindings"},
		{Tool: "antigravity", Path: ".gemini/antigravity-cli/plugins", Description: "Antigravity CLI plugins"},
		// Copilot CLI reads user settings from settings.json; config.json is
		// machine/login state (its own header says so) and stays out.
		{Tool: "copilot", Path: ".copilot/copilot-instructions.md", Description: "GitHub Copilot global instructions"},
		{Tool: "copilot", Path: ".copilot/settings.json", Description: "Copilot CLI user settings"},
		{Tool: "copilot", Path: ".copilot/mcp-config.json", Description: "Copilot CLI MCP servers"},
		{Tool: "copilot", Path: ".copilot/permissions-config.json", Description: "Copilot CLI tool permissions"},
		{Tool: "aider", Path: ".aider.conf.md", Description: "Aider global instructions"},
		// Maru settings files only — skills/_sources/env are Maru-managed git
		// repos and venvs that Maru restores itself.
		{Tool: "maru", Path: ".maru/settings.json", Description: "Maru app settings"},
		{Tool: "maru", Path: ".maru/sites.json", Description: "Maru sites registry"},
	}
	authEntries := []Entry{
		{Tool: "claude", Path: ".claude/settings.local.json", Description: "Claude local/auth settings", Auth: true},
		{Tool: "claude", Path: ".config/claude/settings.local.json", Description: "Claude local/auth settings", Auth: true},
		{Tool: "codex", Path: ".codex/auth.json", Description: "Codex auth credentials", Auth: true},
		{Tool: "kimi", Path: ".kimi-code/credentials", Description: "Kimi Code credentials", Auth: true},
		{Tool: "pi", Path: ".pi/agent/auth.json", Description: "pi OAuth credentials", Auth: true},
		{Tool: "antigravity", Path: ".gemini/oauth_creds.json", Description: "Antigravity/Gemini OAuth credentials", Auth: true},
		{Tool: "antigravity", Path: ".gemini/google_accounts.json", Description: "Antigravity/Gemini account cache", Auth: true},
	}
	if includeAuth {
		entries = append(entries, authEntries...)
	}
	return entries
}

// AIConfigRoot returns the subtree containing all host snapshots.
func (e *Engine) AIConfigRoot() string {
	return filepath.Join(e.Root, "ai-config")
}

// HostRoot returns the host-specific snapshot directory.
func (e *Engine) HostRoot() string {
	return filepath.Join(e.AIConfigRoot(), e.Hostname)
}

// VersionPath returns the path for a version id.
func (e *Engine) VersionPath(version string) string {
	return filepath.Join(e.HostRoot(), version)
}

// LatestPointerPath returns <host-root>/latest.txt.
func (e *Engine) LatestPointerPath() string {
	return filepath.Join(e.HostRoot(), "latest.txt")
}

// BackupOptions controls Backup.
type BackupOptions struct {
	Tag         string
	IncludeAuth bool
}

// RestoreOptions controls Restore and Import.
type RestoreOptions struct {
	Version     string
	IncludeAuth bool
}

// Backup creates a new host-scoped versioned snapshot. A partially written
// version directory is removed when any step before the metadata files
// fails, so List/ResolveLatest never see orphan snapshots.
func (e *Engine) Backup(opts BackupOptions) (*Summary, error) {
	if err := e.validatePortableEntries(opts.IncludeAuth); err != nil {
		return nil, err
	}
	version, err := e.uniqueVersion(time.Now())
	if err != nil {
		return nil, err
	}
	dest := e.VersionPath(version)
	if err := e.Runner.MkdirAll(filepath.Join(dest, homePrefix), 0o755); err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = e.Runner.RemoveAll(dest)
		}
	}()
	sum, err := e.copyFromHome(filepath.Join(dest, homePrefix), opts.IncludeAuth)
	if err != nil {
		return nil, err
	}
	sum.Version = version
	sum.Path = dest
	if err := e.writeSnapshotMetadata(dest, version, opts.Tag, opts.IncludeAuth, sum); err != nil {
		return nil, err
	}
	committed = true
	// Pointer failure keeps the (complete) snapshot; ResolveLatest falls
	// back to the newest directory.
	if err := e.Runner.WriteFile(e.LatestPointerPath(), []byte(version+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write latest.txt: %w", err)
	}
	return sum, nil
}

// Restore restores a versioned snapshot into HomeDir.
func (e *Engine) Restore(opts RestoreOptions) (*Summary, error) {
	version := opts.Version
	if version == "" || version == "latest" {
		v, err := e.ResolveLatest()
		if err != nil {
			return nil, err
		}
		version = v
	}
	src := e.VersionPath(version)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("snapshot %s not found at %s", version, src)
	}
	sum, err := e.restoreFromSnapshotRoot(src, opts.IncludeAuth)
	if err != nil {
		return nil, err
	}
	sum.Version = version
	sum.Path = src
	return sum, nil
}

// Export writes a portable tar.gz archive. The archive file is always
// 0600: with IncludeAuth it contains OAuth/API credentials in plaintext,
// and nothing depends on it being group/world-readable otherwise.
func (e *Engine) Export(path string, opts BackupOptions) (*Summary, error) {
	if err := e.validatePortableEntries(opts.IncludeAuth); err != nil {
		return nil, err
	}
	if err := e.validatePortableArchiveLinks(opts.IncludeAuth); err != nil {
		return nil, err
	}
	if e.Runner.DryRun {
		e.Runner.Logger.Info("dry-run: export AI config", "path", path)
		sum, err := e.copyFromHome("", opts.IncludeAuth)
		if err != nil {
			return nil, err
		}
		sum.Path = path
		return sum, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create export dir: %w", err)
	}
	dir := filepath.Dir(path)
	out, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary archive: %w", err)
	}
	tmpPath := out.Name()
	committed := false
	defer func() {
		if !committed {
			_ = out.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := out.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("chmod archive: %w", err)
	}

	version := NewVersion(time.Now())
	var sum *Summary
	err = writeCompressedTar(out, func(tw *tar.Writer) error {
		var writeErr error
		sum, writeErr = e.addHomeToTar(tw, opts.IncludeAuth)
		if writeErr != nil {
			return writeErr
		}
		sum.Version = version
		sum.Path = path
		if writeErr := addYAMLToTar(tw, "meta.yaml", Meta{
			Version: version, Tag: opts.Tag, Hostname: e.Hostname,
			CreatedAt: time.Now().UTC(), IncludeAuth: opts.IncludeAuth, User: e.User,
		}); writeErr != nil {
			return writeErr
		}
		return addYAMLToTar(tw, "manifest.yaml", ArchiveManifest{
			Schema: archiveVersion, IncludeAuth: opts.IncludeAuth, Entries: sum.Entries,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("write archive: %w", err)
	}
	if err := out.Sync(); err != nil {
		return nil, fmt.Errorf("sync archive: %w", err)
	}
	if err := out.Close(); err != nil {
		return nil, fmt.Errorf("close archive: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("publish archive: %w", err)
	}
	committed = true
	if dirHandle, openErr := os.Open(dir); openErr == nil {
		syncErr := dirHandle.Sync()
		closeErr := dirHandle.Close()
		if syncErr != nil {
			return nil, fmt.Errorf("sync archive directory: %w", syncErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close archive directory: %w", closeErr)
		}
	} else {
		return nil, fmt.Errorf("open archive directory for sync: %w", openErr)
	}
	return sum, nil
}

func writeCompressedTar(out io.Writer, write func(*tar.Writer) error) error {
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)
	if err := write(tw); err != nil {
		_ = tw.Close()
		_ = gw.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gw.Close()
		return err
	}
	return gw.Close()
}

// validatePortableEntries fails before writing a snapshot/archive when a
// portable configuration file contains an inline credential. Auth entries are
// already opt-in via --include-auth; ordinary settings must reference the
// environment or keychain instead of silently leaking into a plaintext backup.
func (e *Engine) validatePortableEntries(includeAuth bool) error {
	roots, err := e.portableArchiveRoots(includeAuth)
	if err != nil {
		return err
	}
	for _, entry := range Entries(includeAuth) {
		if entry.Auth {
			continue
		}
		src := filepath.Join(e.HomeDir, entry.Path)
		if _, err := os.Lstat(src); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect %s for secrets: %w", entry.Path, err)
		}
		if err := scanMaterializedSecrets(src, entry.Path, roots, map[string]bool{}); err != nil {
			return err
		}
	}
	return nil
}

// portableArchiveRoots returns the canonical roots of every managed entry that
// exists, so nested symlinks may be materialized when they resolve into any
// managed portable root (not only their own entry's root).
func (e *Engine) portableArchiveRoots(includeAuth bool) ([]portableArchiveRoot, error) {
	var roots []portableArchiveRoot
	for _, entry := range Entries(includeAuth) {
		src := filepath.Join(e.HomeDir, entry.Path)
		info, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("portable archive entry %s: top-level managed entry may not be a symlink", entry.Path)
		}
		canonical, err := filepath.EvalSymlinks(src)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", entry.Path, err)
		}
		roots = append(roots, portableArchiveRoot{path: filepath.Clean(canonical), isDir: info.IsDir()})
	}
	return roots, nil
}

func scanMaterializedSecrets(src, logicalRel string, roots []portableArchiveRoot, stack map[string]bool) error {
	if isExcluded(logicalRel) {
		return nil
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	actual := src
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, resolvedInfo, portable, err := resolveMaterializedSymlink(src, roots)
		if err != nil {
			return err
		}
		if !portable {
			return nil
		}
		actual, info = resolved, resolvedInfo
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported special file %s", src)
		}
		var data []byte
		if logicalRel == claudeStateRelPath {
			// Only the mcpServers projection is archived, so project before
			// the size-capped scan: a large machine-local Claude state file
			// must not block backup of its small portable slice.
			data, err = readClaudeMCPProjection(actual)
			if err != nil {
				return fmt.Errorf("parse %s MCP state: %w", logicalRel, err)
			}
		} else {
			var candidate bool
			data, candidate, err = readSecretScanCandidate(logicalRel, actual, info.Size())
			if err != nil {
				return fmt.Errorf("read %s for inline credential scan: %w", logicalRel, err)
			}
			if !candidate {
				return nil
			}
		}
		field, scanned, err := inlineSecretField(logicalRel, data, int64(len(data)))
		if err != nil {
			return fmt.Errorf("scan %s for inline credentials: %w", logicalRel, err)
		}
		if scanned && field != "" {
			return fmt.Errorf("refusing portable AI backup: secret-like value in %s (%s); move it to an environment variable or keychain", logicalRel, field)
		}
		return nil
	}
	realDir, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return err
	}
	if stack[realDir] {
		return fmt.Errorf("symlink directory cycle at %s", src)
	}
	stack[realDir] = true
	defer delete(stack, realDir)
	children, err := os.ReadDir(actual)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := scanMaterializedSecrets(filepath.Join(actual, child.Name()), filepath.Join(logicalRel, child.Name()), roots, stack); err != nil {
			return err
		}
	}
	return nil
}

func inlineSecretField(path string, data []byte, size int64) (string, bool, error) {
	ext := strings.ToLower(filepath.Ext(path))
	// Extensionless executables and opaque helper files are common in hook
	// directories. All regular files are classified by content before scanning;
	// arbitrary binaries are never parsed as configuration.
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		if isStructuredTextExtension(ext) {
			return "", true, fmt.Errorf("structured text file is not valid UTF-8")
		}
		return "", false, nil
	}
	if size > maxSecretScan || len(data) > maxSecretScan {
		return "", true, fmt.Errorf("managed text file is too large to scan safely")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", true, nil
	}

	switch ext {
	case ".json":
		var value any
		dec := json.NewDecoder(bytes.NewReader(data))
		if err := dec.Decode(&value); err != nil {
			return "", true, err
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			if err == nil {
				return "", true, fmt.Errorf("multiple JSON values")
			}
			return "", true, err
		}
		if field := structuredSecretField(value); field != "" {
			return field, true, nil
		}
	case ".yaml", ".yml":
		var value any
		if err := yaml.Unmarshal(data, &value); err != nil {
			return "", true, err
		}
		if field := structuredSecretField(value); field != "" {
			return field, true, nil
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, match := range assignmentPattern.FindAllStringSubmatch(line, -1) {
			if !isSecretName(match[1]) {
				continue
			}
			value := firstNonEmpty(match[2], match[3])
			quoted := value != ""
			if !quoted {
				value = match[4]
			}
			if isSecretPlaceholder(value) {
				continue
			}
			if !quoted && isCodeExpressionValue(ext, match[1], value) {
				continue
			}
			return match[1], true, nil
		}
		for _, pattern := range []*regexp.Regexp{positionalFlagPattern, shellFlagPattern} {
			for _, match := range pattern.FindAllStringSubmatch(line, -1) {
				if isSecretName(match[1]) && !isSecretPlaceholder(match[2]) {
					return match[1], true, nil
				}
			}
		}
	}
	return "", true, nil
}

func isStructuredTextExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".json", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func readSecretScanCandidate(rel, path string, size int64) ([]byte, bool, error) {
	structured := isStructuredTextExtension(filepath.Ext(rel))
	if size > maxSecretScan {
		file, err := os.Open(path)
		if err != nil {
			return nil, true, err
		}
		defer file.Close()
		prefix, err := io.ReadAll(io.LimitReader(file, 8192))
		if err != nil {
			return nil, true, err
		}
		if bytes.IndexByte(prefix, 0) >= 0 || !utf8.Valid(prefix) {
			if structured {
				return nil, true, fmt.Errorf("structured text file is not valid UTF-8")
			}
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("managed text file is too large to scan safely")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		if structured {
			return nil, true, fmt.Errorf("structured text file is not valid UTF-8")
		}
		return nil, false, nil
	}
	return data, true, nil
}

func structuredSecretField(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if isSecretName(key) {
				if text, ok := item.(string); ok && !isSecretPlaceholder(text) {
					return key
				}
			}
			if field := structuredSecretField(item); field != "" {
				return field
			}
		}
	case map[any]any:
		for key, item := range v {
			name, ok := key.(string)
			if ok && isSecretName(name) {
				if text, ok := item.(string); ok && !isSecretPlaceholder(text) {
					return name
				}
			}
			if field := structuredSecretField(item); field != "" {
				return field
			}
		}
	case []any:
		for i, item := range v {
			if flag, ok := item.(string); ok && isSecretName(flag) && i+1 < len(v) {
				if text, ok := v[i+1].(string); ok && !isSecretPlaceholder(text) {
					return flag
				}
			}
			if field := structuredSecretField(item); field != "" {
				return field
			}
		}
	}
	return ""
}

func isSecretName(name string) bool {
	name = strings.TrimLeft(strings.TrimSpace(name), "-")
	var normalized strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			normalized.WriteRune(r)
		}
	}
	n := normalized.String()
	if n == "authorization" || n == "bearer" {
		return true
	}
	for _, suffix := range []string{"apikey", "accesstoken", "authtoken", "bearertoken", "clientsecret", "privatekey", "password"} {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return n == "token" || n == "secret"
}

// isExpressionOnlyExtension reports whether an unquoted assignment value in
// this file type is necessarily an expression rather than a string literal.
// In these languages a bare credential cannot be written without quotes, so an
// unquoted value is always source. Shell, TOML, ini, env and markdown are
// deliberately excluded: `API_KEY=abc123` is a real credential there.
func isExpressionOnlyExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".py", ".rb", ".go", ".java", ".rs":
		return true
	default:
		return false
	}
}

// isCodeExpressionValue reports whether an unquoted assignment value is a code
// expression rather than a literal credential.
//
// A value with no letters or digits cannot be a credential. This matters for
// comparisons: assignmentPattern matches the first `=` of `token === '-C'` and
// yields the value `==`, an operator fragment.
//
// In expression-only languages an unquoted value is source by construction, so
// subscripts such as `const token = tokens[j]` are exempt. The exemption is
// scoped to those extensions on purpose: applying it everywhere would let a
// real `api_key=abc[123]` in a shell or TOML file through.
func isCodeExpressionValue(ext, name, value string) bool {
	if strings.ContainsAny(value, "()'\"`{}") {
		return true
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	}) < 0 {
		return true
	}
	if isExpressionOnlyExtension(ext) {
		return true
	}
	return strings.EqualFold(strings.TrimLeft(name, "-"), value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isSecretPlaceholder(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "$") || strings.HasPrefix(strings.ToLower(value), "env:") {
		return true
	}
	normalized := strings.ToLower(strings.Trim(value, "<>[]{}"))
	switch normalized {
	case "redacted", "placeholder", "changeme", "none", "null", "***", "xxxxx":
		return true
	default:
		return false
	}
}

func readClaudeMCPProjection(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return claudeMCPProjection(data)
}

// claudeMCPProjection deliberately archives only the portable MCP portion of
// ~/.claude.json. Claude also stores project trust, session, telemetry, and
// other machine-local runtime state in this file; that state must not travel
// between machines or be replaced during restore.
func claudeMCPProjection(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return []byte("{}\n"), nil
	}
	state, err := decodeJSONObject(data)
	if err != nil {
		return nil, err
	}
	projection := map[string]json.RawMessage{}
	if servers, ok := state["mcpServers"]; ok {
		projection["mcpServers"] = servers
	}
	out, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, error) {
	var state map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&state); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return state, nil
}

func (e *Engine) restoreClaudeMCPState(src, dst, rel, preRoot string) (int, int64, bool, error) {
	projectedData, err := os.ReadFile(src)
	if err != nil {
		return 0, 0, false, fmt.Errorf("read %s MCP projection: %w", rel, err)
	}
	return e.restoreClaudeMCPProjectionData(projectedData, dst, rel, preRoot)
}

func (e *Engine) restoreLegacyClaudeMCPState(src, dst, rel, preRoot string) (int, int64, bool, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, 0, false, fmt.Errorf("read legacy Claude MCP config: %w", err)
	}
	legacy, err := decodeJSONObject(data)
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse legacy Claude MCP config: %w", err)
	}
	projection := map[string]json.RawMessage{}
	if servers, ok := legacy["mcpServers"]; ok {
		projection["mcpServers"] = servers
	} else {
		servers, marshalErr := json.Marshal(legacy)
		if marshalErr != nil {
			return 0, 0, false, marshalErr
		}
		projection["mcpServers"] = servers
	}
	projectedData, err := json.Marshal(projection)
	if err != nil {
		return 0, 0, false, err
	}
	return e.restoreClaudeMCPProjectionData(projectedData, dst, rel, preRoot)
}

func (e *Engine) restoreClaudeMCPProjectionData(projectedData []byte, dst, rel, preRoot string) (int, int64, bool, error) {
	projection, err := decodeJSONObject(projectedData)
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse %s MCP projection: %w", rel, err)
	}
	live := map[string]json.RawMessage{}
	if data, readErr := os.ReadFile(dst); readErr == nil {
		live, err = decodeJSONObject(data)
		if err != nil {
			return 0, 0, false, fmt.Errorf("parse live %s before MCP merge: %w", rel, err)
		}
	} else if !os.IsNotExist(readErr) {
		return 0, 0, false, fmt.Errorf("read live %s before MCP merge: %w", rel, readErr)
	}
	if servers, ok := projection["mcpServers"]; ok {
		live["mcpServers"] = servers
	} else {
		delete(live, "mcpServers")
	}
	merged, err := json.MarshalIndent(live, "", "  ")
	if err != nil {
		return 0, 0, false, err
	}
	merged = append(merged, '\n')
	moved, err := e.backupExisting(dst, rel, preRoot)
	if err != nil {
		return 0, 0, false, err
	}
	if err := e.Runner.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, 0, moved, err
	}
	if err := e.Runner.WriteFile(dst, merged, 0o600); err != nil {
		return 0, 0, moved, err
	}
	return 1, int64(len(merged)), moved, nil
}

// Import restores a portable tar.gz archive into HomeDir.
func (e *Engine) Import(path string, opts RestoreOptions) (*Summary, error) {
	tmp, err := os.MkdirTemp("", "dotfiles-ai-import-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := extractTarGz(path, tmp); err != nil {
		return nil, err
	}
	sum, err := e.restoreFromSnapshotRoot(tmp, opts.IncludeAuth)
	if err != nil {
		return nil, err
	}
	sum.Path = path
	if meta, _ := readMeta(filepath.Join(tmp, "meta.yaml")); meta != nil {
		sum.Version = meta.Version
	}
	return sum, nil
}

// Status reports live and latest-backup presence.
func (e *Engine) Status(includeAuth bool) []Status {
	latest, _ := e.ResolveLatest()
	var out []Status
	for _, entry := range Entries(includeAuth) {
		st := Status{Entry: entry}
		if _, err := os.Lstat(filepath.Join(e.HomeDir, entry.Path)); err == nil {
			st.PresentLive = true
		}
		if latest != "" {
			if _, err := os.Lstat(filepath.Join(e.VersionPath(latest), homePrefix, entry.Path)); err == nil {
				st.PresentBackup = true
			}
		}
		out = append(out, st)
	}
	return out
}

// List enumerates snapshots newest-first.
func (e *Engine) List() ([]Snapshot, error) {
	return e.list(false)
}

func (e *Engine) list(withoutLatest bool) ([]Snapshot, error) {
	entries, err := os.ReadDir(e.HostRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	latest := ""
	if !withoutLatest {
		latest, _ = e.readLatestPointer()
	}
	var out []Snapshot
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		version := entry.Name()
		meta, err := readMeta(filepath.Join(e.HostRoot(), version, "meta.yaml"))
		if err != nil || meta == nil {
			// Not a committed snapshot (partial dir from an old failed
			// backup, or an unrelated directory) — never list it, never let
			// the latest fallback pick it.
			continue
		}
		manifest, _ := readArchiveManifest(filepath.Join(e.HostRoot(), version, "manifest.yaml"))
		s := Snapshot{
			Version:  version,
			Path:     filepath.Join(e.HostRoot(), version),
			IsLatest: version == latest,
		}
		s.Tag = meta.Tag
		s.CreatedAt = meta.CreatedAt
		s.IncludeAuth = meta.IncludeAuth
		if manifest != nil {
			for _, e := range manifest.Entries {
				s.Files += e.Files
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// ResolveLatest returns the latest snapshot id. An empty or dangling
// pointer (the version was pruned/deleted) falls back to the newest
// existing snapshot — note the explicit empty check: VersionPath("") is
// HostRoot() itself, which exists, so a stat alone would pass.
func (e *Engine) ResolveLatest() (string, error) {
	return snapstore.ResolveLatest(e.LatestPointerPath(), e.HostRoot(), e.VersionPath, func() ([]string, error) {
		all, err := e.list(true)
		if err != nil {
			return nil, err
		}
		versions := make([]string, 0, len(all))
		for _, s := range all {
			versions = append(versions, s.Version)
		}
		return versions, nil
	})
}

// Prune removes older snapshots, keeping the newest `Keep` (including
// whatever latest.txt points at).
func (e *Engine) Prune(opts PruneOptions) ([]string, error) {
	keep := opts.Keep
	if keep < 1 {
		keep = 1
	}
	all, err := e.List()
	if err != nil {
		return nil, err
	}
	if len(all) <= keep {
		return nil, nil
	}
	latest, _ := e.ResolveLatest()
	infos := make([]snapstore.SnapshotInfo, 0, len(all))
	for _, s := range all {
		infos = append(infos, snapstore.SnapshotInfo{Version: s.Version, Path: s.Path})
	}
	return snapstore.Prune(e.Runner, keep, infos, latest)
}

func (e *Engine) readLatestPointer() (string, error) {
	return snapstore.ReadLatestPointer(e.LatestPointerPath())
}

// NewVersion returns a UTC filesystem-safe version id.
func NewVersion(t time.Time) string {
	return snapstore.NewVersion(t)
}

func (e *Engine) uniqueVersion(t time.Time) (string, error) {
	version, err := snapstore.UniqueVersion(t, e.VersionPath)
	if err != nil {
		return "", fmt.Errorf("%w under %s", err, e.HostRoot())
	}
	return version, nil
}

func (e *Engine) copyFromHome(destRoot string, includeAuth bool) (*Summary, error) {
	sum := &Summary{}
	var roots []portableArchiveRoot
	if destRoot != "" {
		var err error
		roots, err = e.portableArchiveRoots(includeAuth)
		if err != nil {
			return nil, err
		}
	}
	for _, entry := range Entries(includeAuth) {
		src := filepath.Join(e.HomeDir, entry.Path)
		es := EntrySummary{Tool: entry.Tool, Path: entry.Path, Auth: entry.Auth}
		info, err := os.Lstat(src)
		if err != nil {
			es.Missing = 1
			sum.Entries = append(sum.Entries, es)
			continue
		}
		if entry.Path == claudeStateRelPath {
			data, err := readClaudeMCPProjection(src)
			if err != nil {
				return nil, fmt.Errorf("project %s MCP state: %w", entry.Path, err)
			}
			if destRoot != "" {
				dst := filepath.Join(destRoot, entry.Path)
				if err := e.Runner.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return nil, err
				}
				if err := e.Runner.WriteFile(dst, data, 0o600); err != nil {
					return nil, err
				}
			}
			es.Copied, es.Files, es.Bytes = 1, 1, int64(len(data))
			sum.Files++
			sum.Bytes += int64(len(data))
			sum.Entries = append(sum.Entries, es)
			continue
		}
		if destRoot == "" {
			files, bytes, err := countTree(src, info, entry.Path)
			if err != nil {
				return nil, fmt.Errorf("count %s: %w", entry.Path, err)
			}
			es.Copied = 1
			es.Files = files
			es.Bytes = bytes
		} else {
			files, bytes, err := e.copyMaterializedPath(src, filepath.Join(destRoot, entry.Path), entry.Path, roots, map[string]bool{})
			if err != nil {
				return nil, fmt.Errorf("copy %s: %w", entry.Path, err)
			}
			es.Copied = 1
			es.Files = files
			es.Bytes = bytes
		}
		sum.Files += es.Files
		sum.Bytes += es.Bytes
		sum.Entries = append(sum.Entries, es)
	}
	return sum, nil
}

func (e *Engine) addHomeToTar(tw *tar.Writer, includeAuth bool) (*Summary, error) {
	sum := &Summary{}
	roots, err := e.portableArchiveRoots(includeAuth)
	if err != nil {
		return nil, err
	}
	for _, entry := range Entries(includeAuth) {
		src := filepath.Join(e.HomeDir, entry.Path)
		es := EntrySummary{Tool: entry.Tool, Path: entry.Path, Auth: entry.Auth}
		info, err := os.Lstat(src)
		if err != nil {
			es.Missing = 1
			sum.Entries = append(sum.Entries, es)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive %s: top-level managed entry may not be a symlink", entry.Path)
		}
		if entry.Path == claudeStateRelPath {
			data, err := readClaudeMCPProjection(src)
			if err != nil {
				return nil, fmt.Errorf("project %s MCP state: %w", entry.Path, err)
			}
			if err := addBytesToTar(tw, filepath.ToSlash(filepath.Join(homePrefix, entry.Path)), data, 0o600); err != nil {
				return nil, err
			}
			es.Copied, es.Files, es.Bytes = 1, 1, int64(len(data))
			sum.Files++
			sum.Bytes += int64(len(data))
			sum.Entries = append(sum.Entries, es)
			continue
		}
		files, bytes, err := addMaterializedPathToTar(tw, src, filepath.Join(homePrefix, entry.Path), entry.Path, roots, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("archive %s: %w", entry.Path, err)
		}
		es.Copied = 1
		es.Files = files
		es.Bytes = bytes
		sum.Files += files
		sum.Bytes += bytes
		sum.Entries = append(sum.Entries, es)
	}
	return sum, nil
}

func (e *Engine) restoreFromSnapshotRoot(root string, includeAuth bool) (*Summary, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if err := rejectSnapshotSymlinks(root, rootInfo); err != nil {
		return nil, err
	}
	entries, err := validatedRestoreEntries(root, includeAuth)
	if err != nil {
		return nil, err
	}
	// One pre-restore dir per restore run, so the user gets a single
	// "previous files" location instead of one fragment per entry.
	preRoot := e.preRestoreDir(time.Now())
	preUsed := false
	sum := &Summary{}
	for _, restore := range entries {
		if restore.target.Auth && !includeAuth {
			continue
		}
		src := filepath.Join(root, homePrefix, restore.sourcePath)
		dst := filepath.Join(e.HomeDir, restore.target.Path)
		es := EntrySummary{Tool: restore.target.Tool, Path: restore.target.Path, Auth: restore.target.Auth}
		info, err := os.Lstat(src)
		if err != nil {
			es.Missing = 1
			sum.Entries = append(sum.Entries, es)
			continue
		}
		if err := rejectSnapshotSymlinks(src, info); err != nil {
			return nil, fmt.Errorf("restore %s: %w", restore.sourcePath, err)
		}
		if restore.target.Path == claudeStateRelPath {
			if restore.legacyClaudeMCP {
				files, bytes, moved, err := e.restoreLegacyClaudeMCPState(src, dst, restore.target.Path, preRoot)
				if err != nil {
					return nil, err
				}
				if moved {
					preUsed = true
				}
				es.Copied, es.Files, es.Bytes = 1, files, bytes
				sum.Files += files
				sum.Bytes += bytes
				sum.Entries = append(sum.Entries, es)
				continue
			}
			files, bytes, moved, err := e.restoreClaudeMCPState(src, dst, restore.target.Path, preRoot)
			if err != nil {
				return nil, err
			}
			if moved {
				preUsed = true
			}
			es.Copied = 1
			es.Files = files
			es.Bytes = bytes
			sum.Files += files
			sum.Bytes += bytes
			sum.Entries = append(sum.Entries, es)
			continue
		}
		if restore.target.Path == codexConfigRelPath {
			if live, statErr := os.Lstat(dst); statErr == nil && live.Mode().IsRegular() {
				// A live config.toml holds Keychain-hash-keyed MCP server defs,
				// project trust levels, and plugin state that Codex rewrites
				// continuously; a whole-file replace reverts them to snapshot
				// age and orphans MCP OAuth credentials. Restore only onto a
				// machine without one (fresh bootstrap); to force, move the
				// live file aside and re-run.
				// ponytail: MCP-only TOML projection if selective restore is
				// ever needed.
				es.Skipped = 1
				sum.Entries = append(sum.Entries, es)
				continue
			}
		}
		moved, err := e.backupExisting(dst, restore.target.Path, preRoot)
		if err != nil {
			return nil, err
		}
		if moved {
			preUsed = true
		}
		if !e.Runner.DryRun {
			_ = os.RemoveAll(dst)
		}
		files, bytes, err := e.copyTree(src, dst, info, restore.target.Path)
		if err != nil {
			return nil, fmt.Errorf("restore %s: %w", restore.target.Path, err)
		}
		es.Copied = 1
		es.Files = files
		es.Bytes = bytes
		sum.Files += files
		sum.Bytes += bytes
		sum.Entries = append(sum.Entries, es)
	}
	if preUsed && !e.Runner.DryRun {
		sum.PreBackupPath = preRoot
	}
	return sum, nil
}

func rejectSnapshotSymlinks(src string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snapshot symlink is not allowed: %s", src)
	}
	if !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot symlink is not allowed: %s", path)
		}
		return nil
	})
}

type validatedRestoreEntry struct {
	sourcePath      string
	target          Entry
	legacyClaudeMCP bool
}

func validatedRestoreEntries(root string, includeAuth bool) ([]validatedRestoreEntry, error) {
	manifestPath := filepath.Join(root, "manifest.yaml")
	manifest, err := readArchiveManifest(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read archive manifest: %w", err)
		}
		var fallback []validatedRestoreEntry
		for _, entry := range Entries(includeAuth) {
			fallback = append(fallback, validatedRestoreEntry{
				sourcePath: entry.Path,
				target:     entry,
			})
		}
		return fallback, nil
	}
	if manifest.Schema != archiveVersion {
		return nil, fmt.Errorf("unsupported AI archive manifest schema %d", manifest.Schema)
	}
	known := map[string]Entry{}
	for _, entry := range Entries(true) {
		known[entry.Path] = entry
	}
	const legacyClaudeMCPPath = ".claude/mcp.json"
	// Snapshots written before the Claude MCP move and the Anchor -> Maru
	// rename carry these manifest paths; map them onto their current targets
	// so old archives stay restorable.
	legacySources := map[string]string{
		legacyClaudeMCPPath:     claudeStateRelPath,
		".anchor/settings.json": ".maru/settings.json",
		".anchor/sites.json":    ".maru/sites.json",
		// Copilot CLI moved its instructions file out of ~/.config.
		".config/github-copilot/AGENTS.md": ".copilot/copilot-instructions.md",
	}
	// Retired inventory entries have no current target: old snapshots still
	// list them, so they are recognized and skipped rather than rejected as
	// unknown.
	retiredSources := map[string]bool{
		".cursor/AGENTS.md": true,
	}
	seenSource := map[string]bool{}
	seenTarget := map[string]bool{}
	validated := make([]validatedRestoreEntry, 0, len(manifest.Entries))
	for _, summary := range manifest.Entries {
		if !isSafeRel(summary.Path) || filepath.Clean(summary.Path) != summary.Path {
			return nil, fmt.Errorf("unsafe archive manifest path %q", summary.Path)
		}
		if seenSource[summary.Path] {
			return nil, fmt.Errorf("duplicate archive manifest entry %q", summary.Path)
		}
		seenSource[summary.Path] = true
		if retiredSources[summary.Path] {
			continue
		}
		target, ok := known[summary.Path]
		legacy := false
		if mapped, isLegacy := legacySources[summary.Path]; isLegacy {
			if mappedTarget, exists := known[mapped]; exists {
				target, ok, legacy = mappedTarget, true, true
			}
		}
		if !ok {
			return nil, fmt.Errorf("unknown archive manifest entry %q", summary.Path)
		}
		// Legacy sources keep their original tool label (e.g. anchor -> maru),
		// so only the auth flag must match for them.
		if summary.Auth != target.Auth || (!legacy && summary.Tool != target.Tool) {
			return nil, fmt.Errorf("archive manifest metadata mismatch for %q", summary.Path)
		}
		if seenTarget[target.Path] {
			return nil, fmt.Errorf("duplicate archive target %q", target.Path)
		}
		seenTarget[target.Path] = true
		validated = append(validated, validatedRestoreEntry{
			sourcePath:      summary.Path,
			target:          target,
			legacyClaudeMCP: summary.Path == legacyClaudeMCPPath,
		})
	}
	return validated, nil
}

// preRestoreDir picks an unused timestamped directory under the documented
// pre-restore location. Created lazily by backupExisting.
func (e *Engine) preRestoreDir(t time.Time) string {
	return snapstore.PreRestoreDir(e.HomeDir, []string{"ai"}, t)
}

func (e *Engine) writeSnapshotMetadata(dest, version, tag string, includeAuth bool, sum *Summary) error {
	if err := writeYAML(e.Runner, filepath.Join(dest, "meta.yaml"), Meta{
		Version:     version,
		Tag:         tag,
		Hostname:    e.Hostname,
		CreatedAt:   time.Now().UTC(),
		IncludeAuth: includeAuth,
		User:        e.User,
	}); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	if err := writeYAML(e.Runner, filepath.Join(dest, "manifest.yaml"), ArchiveManifest{
		Schema:      archiveVersion,
		IncludeAuth: includeAuth,
		Entries:     sum.Entries,
	}); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// backupExisting preserves the live path into preRoot before restore
// removes and overwrites it. The whole tree is kept verbatim — no
// isExcluded filter: restore deletes the live tree entirely, so anything
// skipped here (session *.jsonl, logs, caches inside a managed dir) would
// be unrecoverable; the snapshot excluded them at backup time too. Rename
// is tried first; on cross-device failure it falls back to an unfiltered
// copy plus removal. Returns whether anything was preserved.
func (e *Engine) backupExisting(path, rel, preRoot string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if e.Runner.DryRun {
		e.Runner.Logger.Info("dry-run: would preserve existing", "path", path)
		return false, nil
	}
	dst := filepath.Join(preRoot, rel)
	// 0700: preRoot may hold auth credentials (settings.local.json,
	// auth.json) when restoring with --include-auth.
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err
	}
	if err := os.Rename(path, dst); err == nil {
		return true, nil
	}
	if err := e.copyTreeUnfiltered(path, dst); err != nil {
		return false, fmt.Errorf("backup existing %s: %w", path, err)
	}
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return true, nil
}

// copyTreeUnfiltered copies a file/dir/symlink tree without the managed-
// path exclusion rules. Only used by backupExisting's cross-device
// fallback, which already guarded against dry-run.
func (e *Engine) copyTreeUnfiltered(src, dst string) error {
	copyOne := func(p, target string, info os.FileInfo) error {
		if info.Mode()&os.ModeSymlink != 0 {
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(t, target)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode()&0o777)
		}
		_, err := copyFile(e.Runner, p, target, info.Mode())
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return copyOne(src, dst, info)
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return copyOne(p, filepath.Join(dst, rel), fi)
	})
}

type managedTreeItem struct {
	src  string
	sub  string
	info os.FileInfo
}

func walkManagedTree(src string, info os.FileInfo, relRoot string, visit func(managedTreeItem) error) error {
	if isExcluded(relRoot) {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return visit(managedTreeItem{src: src, sub: ".", info: info})
	}

	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		sub, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		entryRel := relRoot
		if sub != "." {
			entryRel = filepath.Join(relRoot, sub)
		}
		if isExcluded(entryRel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		di := info
		if sub != "." {
			di, err = d.Info()
			if err != nil {
				return err
			}
		}
		return visit(managedTreeItem{
			src:  path,
			sub:  sub,
			info: di,
		})
	})
}

func (e *Engine) copyTree(src, dst string, info os.FileInfo, relRoot string) (int, int64, error) {
	var files int
	var bytes int64
	err := walkManagedTree(src, info, relRoot, func(item managedTreeItem) error {
		target := dst
		if item.sub != "." {
			target = filepath.Join(dst, item.sub)
		}
		if item.info.Mode()&os.ModeSymlink != 0 {
			n, err := e.copySymlink(item.src, target)
			files += n
			return err
		}
		if item.info.IsDir() {
			return e.Runner.MkdirAll(target, item.info.Mode()&0o777)
		}
		n, err := copyFile(e.Runner, item.src, target, item.info.Mode())
		if err != nil {
			return err
		}
		files++
		bytes += n
		return nil
	})
	return files, bytes, err
}

// copyMaterializedPath follows a portable nested symlink only when its
// canonical target remains inside a managed portable root. Snapshots contain
// regular files/directories only, so restore never needs to trust a
// filesystem link supplied by snapshot data.
func (e *Engine) copyMaterializedPath(src, dst, logicalRel string, roots []portableArchiveRoot, stack map[string]bool) (int, int64, error) {
	if isExcluded(logicalRel) {
		return 0, 0, nil
	}
	info, err := os.Lstat(src)
	if err != nil {
		return 0, 0, err
	}
	actual := src
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, resolvedInfo, portable, err := resolveMaterializedSymlink(src, roots)
		if err != nil {
			return 0, 0, err
		}
		if !portable {
			return 0, 0, nil
		}
		actual, info = resolved, resolvedInfo
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return 0, 0, fmt.Errorf("unsupported special file %s", src)
		}
		n, err := copyFile(e.Runner, actual, dst, info.Mode())
		return 1, n, err
	}
	realDir, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return 0, 0, err
	}
	if stack[realDir] {
		return 0, 0, fmt.Errorf("symlink directory cycle at %s", src)
	}
	stack[realDir] = true
	defer delete(stack, realDir)
	if err := e.Runner.MkdirAll(dst, info.Mode()&0o777); err != nil {
		return 0, 0, err
	}
	children, err := os.ReadDir(actual)
	if err != nil {
		return 0, 0, err
	}
	var files int
	var size int64
	for _, child := range children {
		f, b, err := e.copyMaterializedPath(filepath.Join(actual, child.Name()), filepath.Join(dst, child.Name()), filepath.Join(logicalRel, child.Name()), roots, stack)
		files += f
		size += b
		if err != nil {
			return files, size, err
		}
	}
	return files, size, nil
}

func (e *Engine) copySymlink(src, dst string) (int, error) {
	target, err := os.Readlink(src)
	if err != nil {
		return 0, err
	}
	if err := e.Runner.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	if e.Runner.DryRun {
		return 1, nil
	}
	_ = os.Remove(dst)
	return 1, e.Runner.Symlink(target, dst)
}

func copyFile(runner *exec.Runner, src, dst string, mode os.FileMode) (int64, error) {
	return snapstore.CopyFile(runner, src, dst, mode)
}

func countTree(src string, info os.FileInfo, relRoot string) (int, int64, error) {
	var files int
	var bytes int64
	err := walkManagedTree(src, info, relRoot, func(item managedTreeItem) error {
		if item.info.IsDir() {
			return nil
		}
		files++
		if item.info.Mode()&os.ModeSymlink == 0 {
			bytes += item.info.Size()
		}
		return nil
	})
	return files, bytes, err
}

type portableArchiveRoot struct {
	path  string
	isDir bool
}

func pathWithinPortableRoots(path string, roots []portableArchiveRoot) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		if path == root.path || root.isDir && strings.HasPrefix(path, root.path+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// resolveMaterializedSymlink resolves a nested symlink for materialization.
// Dangling links and links resolving outside every managed portable root are
// machine-local wiring (plugin caches and the like); they carry no portable
// content, so traversal skips them instead of aborting the whole backup.
func resolveMaterializedSymlink(src string, roots []portableArchiveRoot) (string, os.FileInfo, bool, error) {
	actual, err := filepath.EvalSymlinks(src)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}
	if !pathWithinPortableRoots(actual, roots) {
		return "", nil, false, nil
	}
	info, err := os.Stat(actual)
	if err != nil {
		return "", nil, false, err
	}
	return actual, info, true, nil
}

func (e *Engine) validatePortableArchiveLinks(includeAuth bool) error {
	roots, err := e.portableArchiveRoots(includeAuth)
	if err != nil {
		return err
	}
	for _, entry := range Entries(includeAuth) {
		src := filepath.Join(e.HomeDir, entry.Path)
		if _, err := os.Lstat(src); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := validateMaterializedPath(src, entry.Path, roots, map[string]bool{}); err != nil {
			return fmt.Errorf("portable archive entry %s: %w", entry.Path, err)
		}
	}
	return nil
}

func validateMaterializedPath(src, logicalRel string, roots []portableArchiveRoot, stack map[string]bool) error {
	if isExcluded(logicalRel) {
		return nil
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	actual := src
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, resolvedInfo, portable, err := resolveMaterializedSymlink(src, roots)
		if err != nil {
			return fmt.Errorf("resolve symlink %s: %w", src, err)
		}
		if !portable {
			return nil
		}
		actual, info = resolved, resolvedInfo
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported special file %s", src)
		}
		return nil
	}
	realDir, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return err
	}
	if stack[realDir] {
		return fmt.Errorf("symlink directory cycle at %s", src)
	}
	stack[realDir] = true
	defer delete(stack, realDir)
	children, err := os.ReadDir(actual)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := validateMaterializedPath(filepath.Join(actual, child.Name()), filepath.Join(logicalRel, child.Name()), roots, stack); err != nil {
			return err
		}
	}
	return nil
}

func addMaterializedPathToTar(tw *tar.Writer, src, archiveName, logicalRel string, roots []portableArchiveRoot, stack map[string]bool) (int, int64, error) {
	if isExcluded(logicalRel) {
		return 0, 0, nil
	}
	info, err := os.Lstat(src)
	if err != nil {
		return 0, 0, err
	}
	actual := src
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, resolvedInfo, portable, err := resolveMaterializedSymlink(src, roots)
		if err != nil {
			return 0, 0, err
		}
		if !portable {
			return 0, 0, nil
		}
		actual, info = resolved, resolvedInfo
	}
	files, size, err := addOneToTar(tw, actual, filepath.ToSlash(archiveName), info)
	if err != nil || !info.IsDir() {
		return files, size, err
	}
	realDir, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return files, size, err
	}
	if stack[realDir] {
		return files, size, fmt.Errorf("symlink directory cycle at %s", src)
	}
	stack[realDir] = true
	defer delete(stack, realDir)
	children, err := os.ReadDir(actual)
	if err != nil {
		return files, size, err
	}
	for _, child := range children {
		f, b, err := addMaterializedPathToTar(tw, filepath.Join(actual, child.Name()), filepath.Join(archiveName, child.Name()), filepath.Join(logicalRel, child.Name()), roots, stack)
		files += f
		size += b
		if err != nil {
			return files, size, err
		}
	}
	return files, size, nil
}

func addOneToTar(tw *tar.Writer, src, name string, info os.FileInfo) (int, int64, error) {
	link := ""
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return 0, 0, err
		}
		link = target
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return 0, 0, err
	}
	header.Name = strings.TrimPrefix(name, "./")
	if err := tw.WriteHeader(header); err != nil {
		return 0, 0, err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if info.IsDir() {
			return 0, 0, nil
		}
		return 1, 0, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, 0, err
	}
	defer in.Close()
	n, err := io.Copy(tw, in)
	return 1, n, err
}

func addYAMLToTar(tw *tar.Writer, name string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	header := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}

func addBytesToTar(tw *tar.Writer, name string, data []byte, mode int64) error {
	header := &tar.Header{Name: name, Mode: mode, Size: int64(len(data))}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func extractTarGz(path, dest string) error {
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer in.Close()
	gr, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("read gzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureRealArchiveParents(dest, target); err != nil {
				return err
			}
			if info, err := os.Lstat(target); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("unsafe archive directory %q: target is not a real directory", header.Name)
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, byte(0): // byte(0) is the legacy regular-file encoding.
			if err := ensureRealArchiveParents(dest, target); err != nil {
				return err
			}
			if info, err := os.Lstat(target); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("unsafe archive file %q: target is not a regular file", header.Name)
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsafe archive entry %q: type %d is not allowed", header.Name, header.Typeflag)
		}
	}
}

// ensureRealArchiveParents prevents an archive entry from pivoting through a
// symlink or non-directory that already exists below the extraction root.
func ensureRealArchiveParents(root, target string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Dir(filepath.Clean(target)))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("unsafe archive target %q", target)
	}
	current := filepath.Clean(root)
	if info, err := os.Lstat(current); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe archive root %q: not a real directory", root)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe archive parent %q: not a real directory", current)
		}
	}
	return nil
}

func safeJoin(root, name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(root, clean)
	rootClean := filepath.Clean(root)
	if target != rootClean && !strings.HasPrefix(target, rootClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

func writeYAML(runner *exec.Runner, path string, v any) error {
	return snapstore.WriteYAML(runner, path, v)
}

func readMeta(path string) (*Meta, error) {
	return snapstore.ReadYAML[Meta](path)
}

func readArchiveManifest(path string) (*ArchiveManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest ArchiveManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func isSafeRel(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	clean := filepath.Clean(path)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(os.PathSeparator))
}

// isExcluded reports whether a home-relative managed path should be skipped.
// Callers must pass paths relative to HomeDir, never absolute filesystem paths.
func isExcluded(rel string) bool {
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		lower := strings.ToLower(part)
		switch lower {
		case ".ds_store", ".system", ".tmp", "tmp", "cache", "caches", "logs", "log",
			"sessions", "session-env", "projects", "file-history", "telemetry", "statsig",
			// Skill bundles are Maru/vendor-managed (plugin packages ship
			// their own); they never travel in the portable settings archive.
			"skills":
			return true
		}
		if strings.Contains(lower, "cache") {
			return true
		}
		if strings.HasPrefix(part, "Singleton") {
			return true
		}
	}
	switch {
	case strings.HasSuffix(rel, ".log"),
		strings.HasSuffix(rel, ".lock"),
		strings.HasSuffix(rel, ".sqlite"),
		strings.HasSuffix(rel, ".sqlite-shm"),
		strings.HasSuffix(rel, ".sqlite-wal"),
		strings.HasSuffix(rel, ".jsonl"):
		return true
	}
	return false
}

// ListHosts enumerates the hostnames that have AI config snapshots under
// root, sorted. Returns (nil, nil) when the tree doesn't exist yet.
func ListHosts(root string) ([]string, error) {
	return snapstore.ListHosts(root, "ai-config")
}
