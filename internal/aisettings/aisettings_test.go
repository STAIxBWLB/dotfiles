package aisettings

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"gopkg.in/yaml.v3"
)

func testEngine(t *testing.T) (*Engine, string, string) {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	return &Engine{
		Runner:   exec.NewRunner(false, slog.Default()),
		HomeDir:  home,
		Root:     root,
		Hostname: "testhost",
		User:     "tester",
	}, home, root
}

func TestBackupRestoreSkipsAuthByDefault(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("# agents v1\n"))
	mustWrite(t, filepath.Join(home, ".codex", "auth.json"), []byte(`{"token":"secret"}`))
	mustWrite(t, filepath.Join(home, ".codex", "skills", "mine", "SKILL.md"), []byte("# mine"))

	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, "home", ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("auth should be excluded by default, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, "home", ".codex", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill directories should be excluded, stat err=%v", err)
	}

	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("mutated\n"))
	if _, err := eng.Restore(RestoreOptions{Version: snap.Version}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# agents v1\n" {
		t.Fatalf("restored config = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatalf("excluded auth should not be deleted by restore: %v", err)
	}
}

func TestRestoreLatestAlias(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("# agents v1\n"))

	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("mutated\n"))
	restored, err := eng.Restore(RestoreOptions{Version: "latest"})
	if err != nil {
		t.Fatalf("restore latest alias: %v", err)
	}
	if restored.Version != snap.Version {
		t.Fatalf("restored version = %q, want %q", restored.Version, snap.Version)
	}
	got, err := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# agents v1\n" {
		t.Fatalf("restored config = %q", got)
	}
}

func TestCountTreeUsesRelativeRootExclusion(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "config.json"), []byte(`{"ok":true}`))

	info, err := os.Lstat(src)
	if err != nil {
		t.Fatal(err)
	}
	files, bytes, err := countTree(src, info, ".claude/session-env")
	if err != nil {
		t.Fatalf("count tree: %v", err)
	}
	if files != 0 || bytes != 0 {
		t.Fatalf("excluded relative root counted files=%d bytes=%d", files, bytes)
	}
}

func TestIncludeAuthBackup(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "auth.json"), []byte(`{"token":"secret"}`))
	snap, err := eng.Backup(BackupOptions{IncludeAuth: true})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, "home", ".codex", "auth.json")); err != nil {
		t.Fatalf("auth should be included with IncludeAuth: %v", err)
	}
}

func TestBackupFailsClosedOnInlineSecret(t *testing.T) {
	eng, home, root := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("OBSIDIAN_API_KEY = \"live-value\"\n"))
	if _, err := eng.Backup(BackupOptions{}); err == nil {
		t.Fatal("backup should reject inline secret")
	}
	entries, err := os.ReadDir(filepath.Join(root, "ai-config"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("failed backup left snapshot entries: %v", entries)
	}
}

func TestBackupAllowsEnvironmentSecretReference(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("OBSIDIAN_API_KEY = \"${OBSIDIAN_API_KEY}\"\n"))
	if _, err := eng.Backup(BackupOptions{}); err != nil {
		t.Fatalf("environment reference should be portable: %v", err)
	}
}

func TestSecretScanUsesSemanticExactNames(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte(
		"customApiKeyResponses = \"enabled\"\nmodel_auto_compact_token_limit = 120000\n"))
	if _, err := eng.Backup(BackupOptions{}); err != nil {
		t.Fatalf("non-secret settings must not trigger scanner: %v", err)
	}
}

func TestSecretScanCoversManagedTextAndPositionalArgs(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		data string
	}{
		{name: "markdown assignment", rel: ".claude/commands/deploy.md", data: "api_key: literal-value\n"},
		{name: "shell flag", rel: ".claude/hooks/start.sh", data: "tool --api-key literal-value\n"},
		{name: "json positional flag", rel: ".maru/settings.json", data: `{"args":["--api-key","literal-value"]}`},
		{name: "extensionless hook", rel: ".claude/hooks/preflight", data: "#!/bin/sh\ntool --token literal-value\n"},
		{name: "javascript", rel: ".claude/hooks/start.mjs", data: `const api_key = "literal-value";`},
		{name: "typescript", rel: ".claude/hooks/start.tsx", data: `const token = "literal-value";`},
		{name: "python", rel: ".claude/hooks/start.py", data: `password = "literal-value"`},
		{name: "zsh", rel: ".claude/hooks/start.zsh", data: `tool --api-key literal-value`},
		{name: "ruby", rel: ".claude/hooks/start.rb", data: `secret = "literal-value"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, home, _ := testEngine(t)
			mustWrite(t, filepath.Join(home, tt.rel), []byte(tt.data))
			if _, err := eng.Backup(BackupOptions{}); err == nil {
				t.Fatal("backup should reject inline secret")
			}
		})
	}
}

func TestSecretScanAllowsCodeSubscriptValues(t *testing.T) {
	// A hook that tokenizes shell input assigns `token` from a subscript.
	// That is source, not a credential, and must not block backup.
	tests := []struct {
		name string
		rel  string
		data string
	}{
		{name: "js subscript", rel: ".claude/hooks/guard.js", data: "const tokens = split(cmd);\nconst token = tokens[j];\n"},
		{name: "python subscript", rel: ".claude/hooks/guard.py", data: "token = parts[0]\n"},
		{name: "js strict equality", rel: ".claude/hooks/eq.js", data: "if (token === '-C' && next) {\n  return 1;\n}\n"},
		{name: "shell equality", rel: ".claude/hooks/eq.sh", data: "if [ \"$token\" == \"-C\" ]; then :; fi\n"},
		{name: "js bare identifier", rel: ".claude/hooks/id.js", data: "const token = incoming;\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, home, _ := testEngine(t)
			mustWrite(t, filepath.Join(home, tt.rel), []byte(tt.data))
			if _, err := eng.Backup(BackupOptions{}); err != nil {
				t.Fatalf("code expression must not trigger scanner: %v", err)
			}
		})
	}
}

func TestSecretScanCatchesBracketedCredentialInConfig(t *testing.T) {
	// Brackets only mean "code" in expression-only languages. A shell or TOML
	// file can hold a real unquoted credential that happens to contain one.
	tests := []struct {
		name string
		rel  string
		data string
	}{
		{name: "shell bracket password", rel: ".claude/hooks/env.sh", data: "password=p@ss[word]\n"},
		{name: "toml bracket api key", rel: ".codex/config.toml", data: "api_key=abc[123]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, home, _ := testEngine(t)
			mustWrite(t, filepath.Join(home, tt.rel), []byte(tt.data))
			if _, err := eng.Backup(BackupOptions{}); err == nil {
				t.Fatal("bracketed credential in a config file must still be rejected")
			}
		})
	}
}

func TestSecretScanSkipsOpaqueExtensionlessBinary(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".claude", "hooks", "helper"), []byte{0, 1, 2, 3, 0xff})
	if _, err := eng.Backup(BackupOptions{}); err != nil {
		t.Fatalf("opaque binary should not be parsed as config: %v", err)
	}
}

func TestSecretScanFailsClosedOnMalformedManagedJSON(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".maru", "settings.json"), []byte(`{"broken":`))
	if _, err := eng.Backup(BackupOptions{}); err == nil {
		t.Fatal("malformed managed JSON should fail closed")
	}
}

func TestSecretScanAllowsEmptyOptionalJSON(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), nil)
	if _, err := eng.Backup(BackupOptions{}); err != nil {
		t.Fatalf("empty optional JSON should be treated as absent: %v", err)
	}
}

func TestClaudeStateBackupProjectsOnlyMCPAndRestoreMerges(t *testing.T) {
	eng, home, _ := testEngine(t)
	statePath := filepath.Join(home, ".claude.json")
	mustWrite(t, statePath, []byte(`{"mcpServers":{"vault":{"command":"mcpvault"}},"projects":{"/old":{"trusted":true}},"telemetry":{"count":4}}`))
	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := os.ReadFile(filepath.Join(snap.Path, "home", ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projected), "projects") || strings.Contains(string(projected), "telemetry") || !strings.Contains(string(projected), "mcpServers") {
		t.Fatalf("snapshot must contain MCP-only projection: %s", projected)
	}
	mustWrite(t, statePath, []byte(`{"mcpServers":{"new":{"command":"other"}},"projects":{"/new":{"trusted":true}},"session":{"id":"keep"},"telemetry":{"count":9}}`))
	restored, err := eng.Restore(RestoreOptions{Version: snap.Version})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{"vault", "/new", "session", "telemetry"} {
		if !strings.Contains(text, want) {
			t.Fatalf("merged Claude state missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, `"new"`) || strings.Contains(text, "/old") {
		t.Fatalf("restore did not replace only MCP state: %s", text)
	}
	if restored.PreBackupPath == "" {
		t.Fatal("restore should preserve complete pre-merge Claude state")
	}
	pre, err := os.ReadFile(filepath.Join(restored.PreBackupPath, ".claude.json"))
	if err != nil || !strings.Contains(string(pre), `"session"`) {
		t.Fatalf("pre-restore backup did not preserve full live state: %v %s", err, pre)
	}
}

func TestEntriesIncludeAntigravityAndKeepAuthOptional(t *testing.T) {
	withoutAuth := Entries(false)
	if !hasEntry(withoutAuth, "claude", ".claude.json") || hasEntryPath(withoutAuth, ".claude/mcp.json") {
		t.Fatalf("Claude MCP SSOT should be ~/.claude.json: %+v", withoutAuth)
	}
	if !hasEntry(withoutAuth, "maru", ".maru/settings.json") {
		t.Fatalf("Maru settings entry missing: %+v", withoutAuth)
	}
	if !hasEntry(withoutAuth, "antigravity", ".gemini/antigravity-cli/settings.json") {
		t.Fatalf("antigravity CLI settings entry missing: %+v", withoutAuth)
	}
	if !hasEntry(withoutAuth, "antigravity", ".gemini/GEMINI.md") {
		t.Fatalf("antigravity global instructions entry missing: %+v", withoutAuth)
	}
	if hasEntry(withoutAuth, "antigravity", ".gemini/oauth_creds.json") {
		t.Fatalf("antigravity OAuth credentials should be auth-only: %+v", withoutAuth)
	}

	withAuth := Entries(true)
	if !hasEntry(withAuth, "antigravity", ".gemini/oauth_creds.json") {
		t.Fatalf("antigravity OAuth credentials missing with IncludeAuth: %+v", withAuth)
	}
	if !hasEntry(withAuth, "antigravity", ".gemini/google_accounts.json") {
		t.Fatalf("antigravity account cache missing with IncludeAuth: %+v", withAuth)
	}
}

func TestEntriesCoverCopilotCLI(t *testing.T) {
	entries := Entries(false)
	for _, path := range []string{
		".copilot/copilot-instructions.md",
		".copilot/settings.json",
		".copilot/mcp-config.json",
		".copilot/permissions-config.json",
	} {
		if !hasEntry(entries, "copilot", path) {
			t.Errorf("copilot entry %s missing: %+v", path, entries)
		}
	}
	// config.json is machine/login state; the legacy path predates Copilot CLI.
	if hasEntryPath(entries, ".copilot/config.json") {
		t.Error("copilot config.json is machine state and must not be backed up")
	}
	if hasEntryPath(entries, ".config/github-copilot/AGENTS.md") {
		t.Error("legacy github-copilot AGENTS.md entry should be gone")
	}
}

func TestEntriesCoverKimiCode(t *testing.T) {
	entries := Entries(false)
	for _, path := range []string{
		".kimi-code/AGENTS.md",
		".kimi-code/mcp.json",
		".kimi-code/config.toml",
	} {
		if !hasEntry(entries, "kimi", path) {
			t.Errorf("kimi entry %s missing: %+v", path, entries)
		}
	}
	if hasEntryPath(entries, ".kimi-code/credentials") {
		t.Error("kimi credentials are auth-only and must not be backed up by default")
	}
	if !hasEntry(Entries(true), "kimi", ".kimi-code/credentials") {
		t.Error("kimi credentials missing with IncludeAuth")
	}
}

func TestEntriesCoverPiAgentHome(t *testing.T) {
	entries := Entries(false)
	for _, path := range []string{
		".pi/agent/AGENTS.md",
		".pi/agent/settings.json",
		// settings.json names a provider models.json defines; restoring one
		// without the other leaves pi pointing at a provider it cannot resolve.
		".pi/agent/models.json",
	} {
		if !hasEntry(entries, "pi", path) {
			t.Errorf("pi entry %s missing: %+v", path, entries)
		}
	}
	if hasEntryPath(entries, ".pi/agent/auth.json") {
		t.Error("pi auth.json is auth-only and must not be backed up by default")
	}
	if hasEntryPath(entries, ".pi/agent/sessions") {
		t.Error("pi sessions are machine state and must not be backed up")
	}
	if !hasEntry(Entries(true), "pi", ".pi/agent/auth.json") {
		t.Error("pi auth.json missing with IncludeAuth")
	}
}

func TestEntriesExcludeSkillRuntimeDirectories(t *testing.T) {
	entries := Entries(true)
	excluded := []string{
		".claude/skills",
		".codex/skills",
		".agents/.skill-lock.json",
		".agents/skills",
		".gemini/skills",
		".gemini/antigravity/skills",
	}
	for _, path := range excluded {
		if hasEntryPath(entries, path) {
			t.Fatalf("skill runtime path %s should not be archived: %+v", path, entries)
		}
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".claude", "commands", "writer.md"), []byte("# writer"))
	archive := filepath.Join(t.TempDir(), "ai.tar.gz")
	if _, err := eng.Export(archive, BackupOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive missing: %v", err)
	}

	newHome := t.TempDir()
	importer := &Engine{Runner: exec.NewRunner(false, slog.Default()), HomeDir: newHome, Hostname: "h", Root: t.TempDir()}
	if _, err := importer.Import(archive, RestoreOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(newHome, ".claude", "commands", "writer.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# writer" {
		t.Fatalf("imported content = %q", got)
	}
}

func TestExportMaterializesInScopeSymlinkForSafeImport(t *testing.T) {
	eng, home, _ := testEngine(t)
	hooks := filepath.Join(home, ".claude", "hooks")
	mustWrite(t, filepath.Join(hooks, "target.sh"), []byte("#!/bin/sh\necho safe\n"))
	if err := os.Symlink("target.sh", filepath.Join(hooks, "live-hook.sh")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "portable.tar.gz")
	if _, err := eng.Export(archive, BackupOptions{}); err != nil {
		t.Fatalf("export materialized symlink: %v", err)
	}
	importer, importedHome, _ := testEngine(t)
	if _, err := importer.Import(archive, RestoreOptions{}); err != nil {
		t.Fatalf("import materialized archive: %v", err)
	}
	info, err := os.Lstat(filepath.Join(importedHome, ".claude", "hooks", "live-hook.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("portable symlink was not materialized as regular file: %v", info.Mode())
	}
}

func TestBackupMaterializesInScopeSymlinkForSafeRestore(t *testing.T) {
	eng, home, _ := testEngine(t)
	hooks := filepath.Join(home, ".claude", "hooks")
	mustWrite(t, filepath.Join(hooks, "target.sh"), []byte("#!/bin/sh\necho safe\n"))
	if err := os.Symlink("target.sh", filepath.Join(hooks, "live-hook.sh")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatalf("backup materialized symlink: %v", err)
	}
	materialized := filepath.Join(snapshot.Path, homePrefix, ".claude", "hooks", "live-hook.sh")
	info, err := os.Lstat(materialized)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("snapshot link was not materialized: mode=%v", info.Mode())
	}
	if _, err := eng.Restore(RestoreOptions{Version: snapshot.Version}); err != nil {
		t.Fatalf("restore materialized snapshot: %v", err)
	}
}

// Plugin managers wire hook files through symlinks into machine-local caches
// outside every managed root; those links (and dangling leftovers) carry no
// portable content, so backup/export skip them instead of aborting.
func TestExportSkipsSymlinkOutsideManagedRoots(t *testing.T) {
	eng, home, _ := testEngine(t)
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), []byte("api_key = \"not-portable\"\n"))
	hooks := filepath.Join(home, ".claude", "hooks")
	mustWrite(t, filepath.Join(hooks, "keep.sh"), []byte("#!/bin/sh\necho keep\n"))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(hooks, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "portable.tar.gz")
	if _, err := eng.Export(archive, BackupOptions{}); err != nil {
		t.Fatalf("export with out-of-root symlink: %v", err)
	}
	names := tarEntryNames(t, archive)
	if names[homePrefix+"/.claude/hooks/escape.txt"] {
		t.Fatal("out-of-root symlink content must not be archived")
	}
	if !names[homePrefix+"/.claude/hooks/keep.sh"] {
		t.Fatal("regular sibling file should be archived")
	}
}

func TestBackupSkipsDanglingSymlink(t *testing.T) {
	eng, home, _ := testEngine(t)
	hooks := filepath.Join(home, ".claude", "hooks")
	mustWrite(t, filepath.Join(hooks, "keep.sh"), []byte("#!/bin/sh\necho keep\n"))
	if err := os.Symlink(filepath.Join(home, ".claude", "plugins", "cache", "gone.mjs"), filepath.Join(hooks, "gone.mjs")); err != nil {
		t.Fatal(err)
	}
	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatalf("backup with dangling symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, homePrefix, ".claude", "hooks", "gone.mjs")); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink should be skipped, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, homePrefix, ".claude", "hooks", "keep.sh")); err != nil {
		t.Fatalf("regular sibling file should be snapshotted: %v", err)
	}
}

func TestFailedExportPreservesPreviousArchive(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("OBSIDIAN_API_KEY = \"live-value\"\n"))
	archive := filepath.Join(t.TempDir(), "portable.tar.gz")
	mustWrite(t, archive, []byte("previous-archive"))
	if _, err := eng.Export(archive, BackupOptions{}); err == nil {
		t.Fatal("inline secret should reject export")
	}
	got, err := os.ReadFile(archive)
	if err != nil || string(got) != "previous-archive" {
		t.Fatalf("failed export replaced previous archive: %q err=%v", got, err)
	}
}

func tarEntryNames(t *testing.T, archive string) map[string]bool {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	names := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names[hdr.Name] = true
	}
	return names
}

func TestPortableEntryRejectsTopLevelSymlink(t *testing.T) {
	eng, home, _ := testEngine(t)
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "settings.json"), []byte(`{"safe":true}`))
	if err := os.Symlink(filepath.Join(outside, "settings.json"), filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Export(filepath.Join(t.TempDir(), "archive.tar.gz"), BackupOptions{}); err == nil {
		t.Fatal("top-level managed symlink should be rejected")
	}
}

func TestSecretScanFollowsSafeNestedSymlink(t *testing.T) {
	eng, home, _ := testEngine(t)
	hooks := filepath.Join(home, ".claude", "hooks")
	mustWrite(t, filepath.Join(hooks, "cache", "target.sh"), []byte("tool --api-key literal-value\n"))
	if err := os.Symlink(filepath.Join("cache", "target.sh"), filepath.Join(hooks, "hook.sh")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Export(filepath.Join(t.TempDir(), "archive.tar.gz"), BackupOptions{}); err == nil {
		t.Fatal("secret scanner should inspect followed safe symlink target")
	}
}

func TestWriteCompressedTarReportsWriterFailure(t *testing.T) {
	w := &failingWriter{remaining: 8}
	err := writeCompressedTar(w, func(tw *tar.Writer) error {
		if err := tw.WriteHeader(&tar.Header{Name: "file", Mode: 0o600, Size: 64}); err != nil {
			return err
		}
		_, err := tw.Write(make([]byte, 64))
		return err
	})
	if err == nil {
		t.Fatal("compressed archive writer failure was not propagated")
	}
}

type failingWriter struct{ remaining int }

func (w *failingWriter) Write(data []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, errors.New("injected write failure")
	}
	if len(data) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, errors.New("injected write failure")
	}
	w.remaining -= len(data)
	return len(data), nil
}

func TestImportRejectsPathTraversal(t *testing.T) {
	eng, _, _ := testEngine(t)
	archive := filepath.Join(t.TempDir(), "bad.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Import(archive, RestoreOptions{}); err == nil {
		t.Fatal("expected path traversal error")
	}
}

func TestImportManifestCannotWriteArbitraryHomePath(t *testing.T) {
	eng, home, _ := testEngine(t)
	manifest, err := yaml.Marshal(ArchiveManifest{Schema: archiveVersion, Entries: []EntrySummary{{Tool: "ssh", Path: ".ssh/authorized_keys"}}})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "unknown-entry.tar.gz")
	writeTestTarGz(t, archive, []testTarEntry{
		{name: "manifest.yaml", typeflag: tar.TypeReg, data: manifest},
		{name: "home/.ssh/authorized_keys", typeflag: tar.TypeReg, data: []byte("attacker-key")},
	})
	if _, err := eng.Import(archive, RestoreOptions{IncludeAuth: true}); err == nil {
		t.Fatal("unknown manifest entry should be rejected")
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "authorized_keys")); !os.IsNotExist(err) {
		t.Fatalf("arbitrary HOME path was restored: %v", err)
	}
}

func TestRestoreManifestRejectsDuplicateAndMetadataMismatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []EntrySummary
	}{
		{name: "duplicate", entries: []EntrySummary{{Tool: "codex", Path: ".codex/config.toml"}, {Tool: "codex", Path: ".codex/config.toml"}}},
		{name: "tool mismatch", entries: []EntrySummary{{Tool: "claude", Path: ".codex/config.toml"}}},
		{name: "auth mismatch", entries: []EntrySummary{{Tool: "codex", Path: ".codex/auth.json"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data, err := yaml.Marshal(ArchiveManifest{Schema: archiveVersion, Entries: tc.entries})
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(root, "manifest.yaml"), data)
			if _, err := validatedRestoreEntries(root, true); err == nil {
				t.Fatal("untrusted manifest metadata should be rejected")
			}
		})
	}
}

func TestRestoreMigratesLegacyClaudeMCPPath(t *testing.T) {
	eng, home, _ := testEngine(t)
	version := "legacy-v1"
	root := eng.VersionPath(version)
	mustWrite(t, filepath.Join(root, homePrefix, ".claude", "mcp.json"), []byte(`{"mcpServers":{"legacy-vault":{"command":"mcpvault"}}}`))
	manifest, err := yaml.Marshal(ArchiveManifest{Schema: 1, Entries: []EntrySummary{{Tool: "claude", Path: ".claude/mcp.json"}}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "manifest.yaml"), manifest)
	mustWrite(t, filepath.Join(home, ".claude.json"), []byte(`{"projects":{"keep":{"trusted":true}},"mcpServers":{"replace":{}}}`))
	if _, err := eng.Restore(RestoreOptions{Version: version}); err != nil {
		t.Fatalf("restore legacy MCP snapshot: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "legacy-vault") || !strings.Contains(string(got), "projects") || strings.Contains(string(got), "replace") {
		t.Fatalf("legacy MCP config was not merged into canonical Claude state: %s", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy MCP path should not be restored: %v", err)
	}
}

// Snapshots written before the Anchor -> Maru rename carry .anchor/* manifest
// entries; they must restore into the current .maru/* targets.
func TestRestoreMigratesLegacyAnchorPaths(t *testing.T) {
	eng, home, _ := testEngine(t)
	version := "legacy-anchor"
	root := eng.VersionPath(version)
	mustWrite(t, filepath.Join(root, homePrefix, ".anchor", "settings.json"), []byte(`{"theme":"legacy"}`))
	manifest, err := yaml.Marshal(ArchiveManifest{Schema: archiveVersion, Entries: []EntrySummary{
		{Tool: "anchor", Path: ".anchor/settings.json"},
		{Tool: "anchor", Path: ".anchor/sites.json", Missing: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "manifest.yaml"), manifest)
	if _, err := eng.Restore(RestoreOptions{Version: version}); err != nil {
		t.Fatalf("restore legacy anchor snapshot: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".maru", "settings.json"))
	if err != nil {
		t.Fatalf("legacy anchor settings were not restored to maru path: %v", err)
	}
	if string(got) != `{"theme":"legacy"}` {
		t.Fatalf("restored maru settings = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".anchor", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy anchor path should not be restored: %v", err)
	}
}

// Snapshots written before the Copilot CLI path move carry the old
// .config/github-copilot manifest entry; they must restore into the current
// ~/.copilot/copilot-instructions.md target.
func TestRestoreMigratesLegacyCopilotPath(t *testing.T) {
	eng, home, _ := testEngine(t)
	version := "legacy-copilot"
	root := eng.VersionPath(version)
	mustWrite(t, filepath.Join(root, homePrefix, ".config", "github-copilot", "AGENTS.md"), []byte("# instructions\n"))
	manifest, err := yaml.Marshal(ArchiveManifest{Schema: archiveVersion, Entries: []EntrySummary{
		{Tool: "copilot", Path: ".config/github-copilot/AGENTS.md"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "manifest.yaml"), manifest)
	if _, err := eng.Restore(RestoreOptions{Version: version}); err != nil {
		t.Fatalf("restore legacy copilot snapshot: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".copilot", "copilot-instructions.md"))
	if err != nil {
		t.Fatalf("legacy copilot instructions were not restored to the new path: %v", err)
	}
	if string(got) != "# instructions\n" {
		t.Fatalf("restored copilot instructions = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "github-copilot", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy copilot path should not be restored: %v", err)
	}
}

// Snapshots written while Cursor was in the agents registry carry a
// .cursor/AGENTS.md manifest entry — recorded even when the file was absent.
// The retired entry must not fail the restore; it is skipped, not restored.
func TestRestoreSkipsRetiredCursorEntry(t *testing.T) {
	eng, home, _ := testEngine(t)
	version := "retired-cursor"
	root := eng.VersionPath(version)
	mustWrite(t, filepath.Join(root, homePrefix, ".cursor", "AGENTS.md"), []byte("# instructions\n"))
	mustWrite(t, filepath.Join(root, homePrefix, ".grok", "AGENTS.md"), []byte("# instructions\n"))
	mustWrite(t, filepath.Join(root, homePrefix, ".codex", "config.toml"), []byte("model = \"gpt\"\n"))
	manifest, err := yaml.Marshal(ArchiveManifest{Schema: archiveVersion, Entries: []EntrySummary{
		{Tool: "cursor", Path: ".cursor/AGENTS.md"},
		{Tool: "grok", Path: ".grok/AGENTS.md"},
		{Tool: "codex", Path: ".codex/config.toml"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "manifest.yaml"), manifest)
	if _, err := eng.Restore(RestoreOptions{Version: version}); err != nil {
		t.Fatalf("restore snapshot with retired cursor entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("retired cursor target should not be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".grok", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("retired grok target should not be restored: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("codex config was not restored: %v", err)
	}
	if string(got) != "model = \"gpt\"\n" {
		t.Fatalf("restored codex config = %q", got)
	}
}

// Claude keeps large machine-local state in ~/.claude.json; only the small
// mcpServers projection is archived, so state size must not block backup.
func TestBackupAllowsLargeClaudeState(t *testing.T) {
	eng, home, _ := testEngine(t)
	padding := strings.Repeat("a", maxSecretScan+1)
	state := `{"mcpServers":{"vault":{"command":"mcpvault"}},"history":"` + padding + `"}`
	mustWrite(t, filepath.Join(home, ".claude.json"), []byte(state))
	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatalf("backup with large Claude state: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(snap.Path, homePrefix, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "mcpvault") || strings.Contains(string(got), "history") {
		t.Fatalf("snapshot should hold only the MCP projection: %d bytes", len(got))
	}
}

// A nested symlink resolving into a different managed portable root is still
// portable: its content travels in the archive either way.
func TestBackupMaterializesCrossRootSymlink(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "prompts", "shared.md"), []byte("# shared prompt\n"))
	hooks := filepath.Join(home, ".claude", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".codex", "prompts", "shared.md"), filepath.Join(hooks, "shared.md")); err != nil {
		t.Fatal(err)
	}
	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatalf("backup cross-root symlink: %v", err)
	}
	materialized := filepath.Join(snap.Path, homePrefix, ".claude", "hooks", "shared.md")
	info, err := os.Lstat(materialized)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("cross-root link was not materialized: mode=%v", info.Mode())
	}
}

func TestSnapshotRestoreRejectsAllSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(t *testing.T, snapshotDir string) string
	}{
		{name: "in-tree", target: func(t *testing.T, snapshotDir string) string {
			mustWrite(t, filepath.Join(snapshotDir, "target.sh"), []byte("safe"))
			return "target.sh"
		}},
		{name: "escaping", target: func(t *testing.T, _ string) string {
			outside := filepath.Join(t.TempDir(), "outside.sh")
			mustWrite(t, outside, []byte("outside"))
			return outside
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, home, _ := testEngine(t)
			version := "symlink-snapshot"
			root := eng.VersionPath(version)
			snapshotDir := filepath.Join(root, homePrefix, ".claude", "hooks")
			if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target(t, snapshotDir), filepath.Join(snapshotDir, "hook.sh")); err != nil {
				t.Fatal(err)
			}
			manifest, err := yaml.Marshal(ArchiveManifest{Schema: archiveVersion, Entries: []EntrySummary{{Tool: "claude", Path: ".claude/hooks"}}})
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(root, "manifest.yaml"), manifest)
			if _, err := eng.Restore(RestoreOptions{Version: version}); err == nil {
				t.Fatal("snapshot symlink should be rejected")
			}
			if _, err := os.Stat(filepath.Join(home, ".claude", "hooks", "hook.sh")); !os.IsNotExist(err) {
				t.Fatalf("snapshot symlink reached HOME: %v", err)
			}
		})
	}
}

func TestSnapshotRestoreRejectsSymlinkRoot(t *testing.T) {
	eng, home, _ := testEngine(t)
	version := "linked-root"
	realRoot := t.TempDir()
	mustWrite(t, filepath.Join(realRoot, homePrefix, ".claude", "hooks", "hook.sh"), []byte("unsafe"))
	if err := os.MkdirAll(filepath.Dir(eng.VersionPath(version)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, eng.VersionPath(version)); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Restore(RestoreOptions{Version: version}); err == nil {
		t.Fatal("symlink snapshot root should be rejected")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "hooks", "hook.sh")); !os.IsNotExist(err) {
		t.Fatalf("symlink snapshot root reached HOME: %v", err)
	}
}

func TestExtractRejectsArchiveLinksAndPivots(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typeflag byte
		link     string
	}{
		{name: "absolute symlink", typeflag: tar.TypeSymlink, link: "/private/tmp"},
		{name: "parent symlink", typeflag: tar.TypeSymlink, link: "../outside"},
		{name: "hardlink", typeflag: tar.TypeLink, link: "../outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "pivot.tar.gz")
			writeTestTarGz(t, archive, []testTarEntry{
				{name: "pivot", typeflag: tc.typeflag, link: tc.link},
				{name: "pivot/follow-on.txt", typeflag: tar.TypeReg, data: []byte("owned")},
			})
			dest := t.TempDir()
			if err := extractTarGz(archive, dest); err == nil {
				t.Fatal("archive link should be rejected")
			}
			if _, err := os.Lstat(filepath.Join(dest, "pivot")); !os.IsNotExist(err) {
				t.Fatalf("rejected link was materialized: %v", err)
			}
		})
	}
}

func TestExtractRejectsFollowOnFileThroughExistingSymlinkParent(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "follow-on.tar.gz")
	writeTestTarGz(t, archive, []testTarEntry{{name: "pivot/follow-on.txt", typeflag: tar.TypeReg, data: []byte("owned")}})
	dest := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "pivot")); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(archive, dest); err == nil {
		t.Fatal("follow-on file through symlink parent should be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "follow-on.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped through existing symlink: %v", err)
	}
}

type testTarEntry struct {
	name     string
	typeflag byte
	link     string
	data     []byte
}

func writeTestTarGz(t *testing.T, path string, entries []testTarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Linkname: entry.link, Mode: 0o644, Size: int64(len(entry.data))}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := tw.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasEntry(entries []Entry, tool, path string) bool {
	for _, entry := range entries {
		if entry.Tool == tool && entry.Path == path {
			return true
		}
	}
	return false
}

func hasEntryPath(entries []Entry, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func TestRestorePreservesExcludedLiveFiles(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "prompts", "draft.md"), []byte("v1"))

	sum, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// A session log inside the managed dir: excluded from snapshots, but it
	// must survive restore via the pre-restore backup.
	logPath := filepath.Join(home, ".codex", "prompts", "session.jsonl")
	mustWrite(t, logPath, []byte("precious-session-data"))
	mustWrite(t, filepath.Join(home, ".codex", "prompts", "draft.md"), []byte("v2"))

	rsum, err := eng.Restore(RestoreOptions{Version: sum.Version})
	if err != nil {
		t.Fatal(err)
	}
	if rsum.PreBackupPath == "" {
		t.Fatal("PreBackupPath not set")
	}
	preserved, err := os.ReadFile(filepath.Join(rsum.PreBackupPath, ".codex", "prompts", "session.jsonl"))
	if err != nil || string(preserved) != "precious-session-data" {
		t.Errorf("excluded live file not preserved: %q err=%v", preserved, err)
	}
	got, _ := os.ReadFile(filepath.Join(home, ".codex", "prompts", "draft.md"))
	if string(got) != "v1" {
		t.Errorf("restore content: %q", got)
	}
}

func TestRestoreUsesSinglePreBackupDir(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), []byte("a"))
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("b"))

	sum, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), []byte("a2"))
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("b2"))

	rsum, err := eng.Restore(RestoreOptions{Version: sum.Version})
	if err != nil {
		t.Fatal(err)
	}
	if rsum.PreBackupPath == "" {
		t.Fatal("PreBackupPath not set")
	}
	for _, rel := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(rsum.PreBackupPath, rel)); err != nil {
			t.Errorf("entry %s missing from the single pre-backup dir: %v", rel, err)
		}
	}
	// Exactly one dated dir under backup/ai.
	aiBackup := filepath.Join(home, ".local", "share", "dotfiles", "backup", "ai")
	entries, err := os.ReadDir(aiBackup)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("pre-restore backups fragmented across %d dirs", len(entries))
	}
}

func TestResolveLatestFallsBackOnDanglingPointer(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("x"))

	s1, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Point latest.txt at a deleted version.
	mustWrite(t, eng.LatestPointerPath(), []byte("19990101T000000Z\n"))
	got, err := eng.ResolveLatest()
	if err != nil {
		t.Fatal(err)
	}
	if got != s1.Version {
		t.Errorf("dangling pointer: got %q want %q", got, s1.Version)
	}

	// Empty pointer must not resolve to the host root.
	mustWrite(t, eng.LatestPointerPath(), []byte("\n"))
	got, err = eng.ResolveLatest()
	if err != nil {
		t.Fatal(err)
	}
	if got != s1.Version {
		t.Errorf("empty pointer: got %q want %q", got, s1.Version)
	}
}

func TestAIListSkipsDirsWithoutMeta(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("x"))
	s1, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(eng.VersionPath("99999999T999999Z"), 0o755); err != nil {
		t.Fatal(err)
	}
	snaps, err := eng.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Version != s1.Version {
		t.Errorf("List must skip meta-less dirs: %+v", snaps)
	}
}

func TestAIPruneKeepsNewest(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("x"))

	s1, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Synthesize two older snapshots with valid meta.
	for _, v := range []string{"20200101T000000Z", "20210101T000000Z"} {
		dir := eng.VersionPath(v)
		mustWrite(t, filepath.Join(dir, "meta.yaml"), []byte("version: "+v+"\nhostname: testhost\n"))
	}
	removed, err := eng.Prune(PruneOptions{Keep: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v", removed)
	}
	snaps, err := eng.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Version != s1.Version {
		t.Errorf("prune kept wrong snapshot: %+v", snaps)
	}
}

func TestExportArchiveIsOwnerOnly(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("x"))
	mustWrite(t, filepath.Join(home, ".codex", "auth.json"), []byte("secret"))

	archive := filepath.Join(home, "out", "ai.tar.gz")
	// Pre-create world-readable to verify the heal path.
	mustWrite(t, archive, []byte("old"))
	if err := os.Chmod(archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Export(archive, BackupOptions{IncludeAuth: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("archive mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestAIBackupFailureLeavesNoOrphanDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is ineffective as root")
	}
	eng, home, _ := testEngine(t)
	cfg := filepath.Join(home, ".codex", "config.toml")
	mustWrite(t, cfg, []byte("x"))
	if err := os.Chmod(cfg, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })

	if _, err := eng.Backup(BackupOptions{}); err == nil {
		t.Fatal("expected backup to fail on unreadable entry")
	}
	entries, err := os.ReadDir(eng.HostRoot())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, en := range entries {
		if en.IsDir() {
			t.Errorf("orphan version dir left behind: %s", en.Name())
		}
	}
}

func TestEntriesIncludeMaruSettingsOnly(t *testing.T) {
	var maruPaths []string
	for _, e := range Entries(false) {
		if e.Tool == "maru" {
			maruPaths = append(maruPaths, e.Path)
			if isExcluded(e.Path) {
				t.Errorf("maru entry %q is filtered by isExcluded", e.Path)
			}
		}
	}
	want := []string{".maru/settings.json", ".maru/sites.json"}
	if len(maruPaths) != len(want) {
		t.Fatalf("maru entries = %v, want %v", maruPaths, want)
	}
	for i, p := range want {
		if maruPaths[i] != p {
			t.Errorf("maru entry %d = %q, want %q", i, maruPaths[i], p)
		}
	}
}

func TestMaruSettingsBackupRestoreRoundtrip(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".maru", "settings.json"), []byte(`{"theme":"dark"}`))
	mustWrite(t, filepath.Join(home, ".maru", "sites.json"), []byte(`[{"name":"halla-ai"}]`))

	sum, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".maru/settings.json", ".maru/sites.json"} {
		if _, err := os.Stat(filepath.Join(sum.Path, "home", rel)); err != nil {
			t.Errorf("snapshot missing %s: %v", rel, err)
		}
	}

	mustWrite(t, filepath.Join(home, ".maru", "settings.json"), []byte(`{"theme":"mutated"}`))
	if _, err := eng.Restore(RestoreOptions{Version: sum.Version}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(home, ".maru", "settings.json"))
	if string(got) != `{"theme":"dark"}` {
		t.Errorf("maru settings not restored: %q", got)
	}
}

func TestAIListHosts(t *testing.T) {
	root := t.TempDir()
	if hosts, err := ListHosts(root); err != nil || hosts != nil {
		t.Fatalf("missing tree should be (nil, nil): %v %v", hosts, err)
	}
	for _, h := range []string{"b-host", "a-host"} {
		if err := os.MkdirAll(filepath.Join(root, "ai-config", h), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hosts, err := ListHosts(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0] != "a-host" || hosts[1] != "b-host" {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestCopyTreeUnfilteredKeepsExcludedFiles(t *testing.T) {
	eng, home, _ := testEngine(t)
	src := filepath.Join(home, "src")
	mustWrite(t, filepath.Join(src, "config.toml"), []byte("cfg"))
	mustWrite(t, filepath.Join(src, "prompts", "session.jsonl"), []byte("session-data"))
	// A relative symlink inside the tree.
	if err := os.Symlink("config.toml", filepath.Join(src, "link.toml")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(home, "dst")
	if err := eng.copyTreeUnfiltered(src, dst); err != nil {
		t.Fatal(err)
	}
	// The whole point of the unfiltered copy: isExcluded patterns (*.jsonl)
	// are NOT skipped, unlike the managed copyTree.
	got, err := os.ReadFile(filepath.Join(dst, "prompts", "session.jsonl"))
	if err != nil || string(got) != "session-data" {
		t.Errorf("excluded file not copied: %q err=%v", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "config.toml")); string(got) != "cfg" {
		t.Errorf("regular file content: %q", got)
	}
	target, err := os.Readlink(filepath.Join(dst, "link.toml"))
	if err != nil || target != "config.toml" {
		t.Errorf("symlink not preserved: %q err=%v", target, err)
	}
}

func TestBackupExistingCrossDeviceFallback(t *testing.T) {
	eng, home, _ := testEngine(t)
	rel := ".codex/prompts"
	live := filepath.Join(home, rel)
	mustWrite(t, filepath.Join(live, "draft.md"), []byte("v1"))
	mustWrite(t, filepath.Join(live, "session.jsonl"), []byte("session"))

	// Force os.Rename to fail (simulating EXDEV) by pre-creating the
	// destination as a non-empty directory: rename onto a non-empty dir
	// errors, exercising the copyTreeUnfiltered + RemoveAll fallback.
	preRoot := filepath.Join(home, "prebak")
	dst := filepath.Join(preRoot, rel)
	mustWrite(t, filepath.Join(dst, "occupied"), []byte("x"))

	moved, err := eng.backupExisting(live, rel, preRoot)
	if err != nil {
		t.Fatalf("backupExisting: %v", err)
	}
	if !moved {
		t.Fatal("expected moved=true")
	}
	// Fallback copied the whole live tree (including the excluded .jsonl)...
	if got, err := os.ReadFile(filepath.Join(dst, "draft.md")); err != nil || string(got) != "v1" {
		t.Errorf("draft not preserved via fallback: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "session.jsonl")); err != nil || string(got) != "session" {
		t.Errorf("excluded session not preserved via fallback: %q err=%v", got, err)
	}
	// ...and removed the live tree afterward.
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Errorf("live tree not removed after fallback: %v", err)
	}
}

// TestRestoreKeepsLiveCodexConfig pins the skip-if-live guard: a live
// ~/.codex/config.toml holds Keychain-hash-keyed MCP server defs and project
// trust that a whole-file restore would revert to snapshot age (orphaning MCP
// OAuth credentials), so restore keeps the live copy and only bootstraps a
// machine without one.
func TestRestoreKeepsLiveCodexConfig(t *testing.T) {
	eng, home, _ := testEngine(t)
	cfg := filepath.Join(home, ".codex", "config.toml")
	mustWrite(t, cfg, []byte("snapshot-era\n"))

	sum, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}

	mustWrite(t, cfg, []byte("live-edits\n"))
	rsum, err := eng.Restore(RestoreOptions{Version: sum.Version})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg)
	if string(got) != "live-edits\n" {
		t.Fatalf("live config.toml was clobbered: %q", got)
	}
	found := false
	for _, entry := range rsum.Entries {
		if entry.Path != ".codex/config.toml" {
			continue
		}
		found = true
		if entry.Skipped != 1 || entry.Copied != 0 {
			t.Fatalf("entry = %+v, want Skipped=1 Copied=0", entry)
		}
	}
	if !found {
		t.Fatal("no summary entry for .codex/config.toml")
	}
	if rsum.PreBackupPath != "" {
		if _, err := os.Stat(filepath.Join(rsum.PreBackupPath, ".codex", "config.toml")); err == nil {
			t.Fatal("skipped entry must not be moved into the pre-restore backup")
		}
	}

	// Fresh machine: no live file, bootstrap restore still works.
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	rsum, err = eng.Restore(RestoreOptions{Version: sum.Version})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(cfg)
	if string(got) != "snapshot-era\n" {
		t.Fatalf("fresh-machine restore = %q, want snapshot content", got)
	}
	for _, entry := range rsum.Entries {
		if entry.Path == ".codex/config.toml" && (entry.Copied != 1 || entry.Skipped != 0) {
			t.Fatalf("fresh-machine entry = %+v, want Copied=1 Skipped=0", entry)
		}
	}
}

func TestRestorePreBackupDirIsOwnerOnly(t *testing.T) {
	eng, home, _ := testEngine(t)
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("v1"))
	sum, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), []byte("v2"))

	rsum, err := eng.Restore(RestoreOptions{Version: sum.Version})
	if err != nil {
		t.Fatal(err)
	}
	if rsum.PreBackupPath == "" {
		t.Fatal("PreBackupPath not set")
	}
	info, err := os.Stat(rsum.PreBackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("pre-restore dir mode = %v, want 0700 (may hold auth creds)", info.Mode().Perm())
	}
}
