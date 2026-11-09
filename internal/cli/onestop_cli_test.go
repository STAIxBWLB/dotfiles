package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// onestopFixture sandboxes HOME/XDG and seeds the state file, an age key,
// AI/Maru settings, and a secrets store so every wizard domain has
// something to work on.
type onestopFixture struct {
	home string
	root string
	host string
}

func newOnestopFixture(t *testing.T) *onestopFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"), "name: original\n")
	writeCLITestFile(t, filepath.Join(home, ".ssh", "age_key"), "AGE-SECRET-KEY-TEST")
	if err := os.Chmod(filepath.Join(home, ".ssh", "age_key"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCLITestFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"opus"}`)
	writeCLITestFile(t, filepath.Join(home, ".maru", "settings.json"), `{"theme":"dark"}`)
	writeCLITestFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "identity-material")
	writeCLITestFile(t, filepath.Join(home, ".local", "share", "dotfiles-secrets", "90-secrets.sh.age"), "export SECRET=1\n")

	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if idx := strings.Index(host, "."); idx > 0 {
		host = host[:idx]
	}
	return &onestopFixture{home: home, root: t.TempDir(), host: host}
}

// readTag extracts the `tag:` value from a meta.yaml file.
func readTag(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "tag:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "tag:"))
		}
	}
	return ""
}

// latestVersion reads <hostDir>/latest.txt.
func latestVersion(t *testing.T, hostDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(hostDir, "latest.txt"))
	if err != nil {
		t.Fatalf("latest.txt: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func TestOnestopBackupAllDomains(t *testing.T) {
	f := newOnestopFixture(t)

	// apps is excluded: it is darwin-only AND depends on brew + real app
	// settings; the other three domains cover the wizard plumbing.
	out, errOut, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile,ai,secrets", "--include-age-key")
	if err != nil {
		t.Fatalf("backup: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "one-stop backup complete") {
		t.Errorf("missing completion line:\n%s", out)
	}

	profileHost := filepath.Join(f.root, "profiles", f.host)
	profVer := latestVersion(t, profileHost)
	for _, rel := range []string{"config.yaml", "meta.yaml", "secrets/age_key"} {
		if _, err := os.Stat(filepath.Join(profileHost, profVer, rel)); err != nil {
			t.Errorf("profile snapshot missing %s: %v", rel, err)
		}
	}

	aiHost := filepath.Join(f.root, "ai-config", f.host)
	aiVer := latestVersion(t, aiHost)
	for _, rel := range []string{"home/.claude/settings.json", "home/.maru/settings.json"} {
		if _, err := os.Stat(filepath.Join(aiHost, aiVer, rel)); err != nil {
			t.Errorf("ai snapshot missing %s: %v", rel, err)
		}
	}

	if _, err := os.Stat(filepath.Join(f.root, "secrets-age", f.host, "90-secrets.sh.age")); err != nil {
		t.Errorf("secrets archive missing: %v", err)
	}

	// Profile and AI snapshots share one auto-generated onestop tag.
	profTag := readTag(t, filepath.Join(profileHost, profVer, "meta.yaml"))
	aiTag := readTag(t, filepath.Join(aiHost, aiVer, "meta.yaml"))
	if profTag == "" || profTag != aiTag {
		t.Errorf("shared tag mismatch: profile=%q ai=%q", profTag, aiTag)
	}
	if !strings.HasPrefix(profTag, "onestop-") {
		t.Errorf("auto tag should be onestop-<ts>: %q", profTag)
	}
}

func TestOnestopBackupDryRunWritesNothing(t *testing.T) {
	f := newOnestopFixture(t)
	cfgPath := filepath.Join(f.home, ".config", "dotfiles", "config.yaml")
	cfgBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runDotForTest("backup", "--yes", "--dry-run", "--to", f.root, "--scope", "profile,ai,secrets", "--include-age-key")
	if err != nil {
		t.Fatalf("dry-run backup: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	var found []string
	_ = filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != f.root {
			found = append(found, p)
		}
		return nil
	})
	if len(found) > 0 {
		t.Errorf("dry-run created files under the backup root: %v", found)
	}
	// No state write either (catches any unguarded persistUserState).
	if cfgAfter, _ := os.ReadFile(cfgPath); string(cfgAfter) != string(cfgBefore) {
		t.Errorf("dry-run mutated config.yaml:\nbefore=%q\nafter=%q", cfgBefore, cfgAfter)
	}
}

func TestOnestopBackupRejectsUnknownScope(t *testing.T) {
	f := newOnestopFixture(t)
	_, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile,bogus")
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("expected unknown-scope error, got %v", err)
	}
}

func TestOnestopBackupCustomTagPropagates(t *testing.T) {
	f := newOnestopFixture(t)
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile,ai", "--tag", "migrate-2026"); err != nil {
		t.Fatal(err)
	}
	profileHost := filepath.Join(f.root, "profiles", f.host)
	aiHost := filepath.Join(f.root, "ai-config", f.host)
	if tag := readTag(t, filepath.Join(profileHost, latestVersion(t, profileHost), "meta.yaml")); tag != "migrate-2026" {
		t.Errorf("profile tag = %q", tag)
	}
	if tag := readTag(t, filepath.Join(aiHost, latestVersion(t, aiHost), "meta.yaml")); tag != "migrate-2026" {
		t.Errorf("ai tag = %q", tag)
	}
}

func TestOnestopRestoreCrossHost(t *testing.T) {
	f := newOnestopFixture(t)

	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile,ai,secrets", "--include-age-key"); err != nil {
		t.Fatal(err)
	}
	// Move every per-host tree to a fake other host.
	for _, tree := range []string{"profiles", "ai-config", "secrets-age"} {
		if err := os.Rename(filepath.Join(f.root, tree, f.host), filepath.Join(f.root, tree, "otherhost")); err != nil {
			t.Fatal(err)
		}
	}

	statePath := filepath.Join(f.home, ".config", "dotfiles", "config.yaml")
	writeCLITestFile(t, statePath, "name: mutated\n")
	writeCLITestFile(t, filepath.Join(f.home, ".claude", "settings.json"), `{"model":"mutated"}`)

	// stubAge so the secrets step "decrypts" by copying.
	stubAge(t, false)

	out, errOut, err := runDotForTest("restore", "--yes", "--from", f.root, "--host", "otherhost",
		"--scope", "profile,ai,secrets", "--include-age-key")
	if err != nil {
		t.Fatalf("restore: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "one-stop restore complete") {
		t.Errorf("missing completion line:\n%s", out)
	}
	got, _ := os.ReadFile(statePath)
	if string(got) != "name: original\n" {
		t.Errorf("state not restored: %q", got)
	}
	claude, _ := os.ReadFile(filepath.Join(f.home, ".claude", "settings.json"))
	if string(claude) != `{"model":"opus"}` {
		t.Errorf("claude settings not restored: %q", claude)
	}
	shell, err := os.ReadFile(filepath.Join(f.home, ".config", "shell", "90-secrets.sh"))
	if err != nil || string(shell) != "export SECRET=1\n" {
		t.Errorf("shell secrets not restored: %q err=%v", shell, err)
	}
}

func TestOnestopRestoreProfileFailureAborts(t *testing.T) {
	f := newOnestopFixture(t)
	// Only an AI snapshot exists — no profile tree.
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "ai"); err != nil {
		t.Fatal(err)
	}

	// profile is unavailable, so explicitly requesting it must fail fast.
	_, _, err := runDotForTest("restore", "--yes", "--from", f.root, "--scope", "profile,ai")
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("expected unavailable-profile error, got %v", err)
	}

	// ai-only restore succeeds.
	writeCLITestFile(t, filepath.Join(f.home, ".claude", "settings.json"), `{"model":"mutated"}`)
	out, errOut, err := runDotForTest("restore", "--yes", "--from", f.root, "--scope", "ai")
	if err != nil {
		t.Fatalf("ai-only restore: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	claude, _ := os.ReadFile(filepath.Join(f.home, ".claude", "settings.json"))
	if string(claude) != `{"model":"opus"}` {
		t.Errorf("claude settings not restored: %q", claude)
	}
}

func TestOnestopRestorePinsSessionRoot(t *testing.T) {
	f := newOnestopFixture(t)

	// The snapshot's config.yaml carries another machine's backup_root.
	writeCLITestFile(t, filepath.Join(f.home, ".config", "dotfiles", "config.yaml"),
		"name: original\nmodules:\n  macapps:\n    backup_root: /nonexistent/other-machine-root\n")
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile,ai"); err != nil {
		t.Fatal(err)
	}

	// Local state has no backup_root; restore from --from must keep using
	// the session root for the AI step even after profile restore rewrote
	// config.yaml with the foreign backup_root.
	writeCLITestFile(t, filepath.Join(f.home, ".config", "dotfiles", "config.yaml"), "name: local\n")
	writeCLITestFile(t, filepath.Join(f.home, ".claude", "settings.json"), `{"model":"mutated"}`)

	out, errOut, err := runDotForTest("restore", "--yes", "--from", f.root, "--scope", "profile,ai")
	if err != nil {
		t.Fatalf("restore: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	got, _ := os.ReadFile(filepath.Join(f.home, ".config", "dotfiles", "config.yaml"))
	if !strings.Contains(string(got), "/nonexistent/other-machine-root") {
		t.Fatalf("profile restore should have brought back the foreign config: %q", got)
	}
	claude, _ := os.ReadFile(filepath.Join(f.home, ".claude", "settings.json"))
	if string(claude) != `{"model":"opus"}` {
		t.Errorf("ai step did not use the pinned session root: %q", claude)
	}
}

func TestOnestopRestoreNoBackupsAbortsBeforeChanges(t *testing.T) {
	f := newOnestopFixture(t)
	empty := t.TempDir()
	_, _, err := runDotForTest("restore", "--yes", "--from", empty)
	if err == nil || !strings.Contains(err.Error(), "no backups found") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	// Preflight must abort before touching any local file.
	if got, _ := os.ReadFile(filepath.Join(f.home, ".config", "dotfiles", "config.yaml")); string(got) != "name: original\n" {
		t.Errorf("config.yaml changed: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(f.home, ".claude", "settings.json")); string(got) != `{"model":"opus"}` {
		t.Errorf("claude settings changed: %q", got)
	}
	// No pre-restore backup trees (profilesnap or aisettings) were created.
	if _, err := os.Stat(filepath.Join(f.home, ".local", "share", "dotfiles", "backup")); !os.IsNotExist(err) {
		t.Errorf("pre-restore backup tree created despite preflight abort: %v", err)
	}
}

func TestOnestopRestoreUnmatchedSecretArchiveIsNonFatal(t *testing.T) {
	f := newOnestopFixture(t)
	// An archive that maps to no secretEntries name (e.g. an SSH key from a
	// host with a different ssh.key_name, or an obsolete leftover). The
	// matching archives still restore, so this must NOT fail the run — only
	// surface a warning + a summary note.
	writeCLITestFile(t, filepath.Join(f.home, ".local", "share", "dotfiles-secrets", "id_rsa.age"), "foreign-key")
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "secrets"); err != nil {
		t.Fatal(err)
	}
	stubAge(t, false)
	out, errOut, err := runDotForTest("restore", "--yes", "--from", f.root, "--scope", "secrets")
	if err != nil {
		t.Fatalf("unmatched archive must be non-fatal: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "one-stop restore complete") {
		t.Errorf("run should complete:\n%s", out)
	}
	if !strings.Contains(out, "unmatched") && !strings.Contains(out, "id_rsa.age") {
		t.Errorf("summary should note the unmatched archive:\n%s", out)
	}
	// The warning with remediation hint is printed by secretsRestoreFiles.
	if !strings.Contains(errOut, "id_rsa.age") {
		t.Errorf("expected prominent unmatched-archive warning on stderr:\n%s", errOut)
	}
}

func TestOnestopRestoreDryRunSyncsSnapshotState(t *testing.T) {
	f := newOnestopFixture(t)
	idPath := filepath.Join(f.home, ".ssh", "id_ed25519") // created by the fixture

	// Back up profile+secrets with the snapshot config pointing age_identity
	// at the existing key.
	writeCLITestFile(t, filepath.Join(f.home, ".config", "dotfiles", "config.yaml"),
		"name: original\nsecrets:\n  age_identity: "+idPath+"\n")
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile,secrets"); err != nil {
		t.Fatal(err)
	}

	// Locally point age_identity at a path that does NOT exist. Without the
	// dry-run state sync, the secrets preview would resolve this missing
	// local identity and falsely fail; with it, the snapshot's (existing)
	// identity is used and the preview succeeds.
	writeCLITestFile(t, filepath.Join(f.home, ".config", "dotfiles", "config.yaml"),
		"name: local\nsecrets:\n  age_identity: "+filepath.Join(f.home, ".ssh", "ghost-missing")+"\n")

	stubAge(t, false) // secretsRestoreFiles requires age on PATH even in dry-run

	out, errOut, err := runDotForTest("restore", "--yes", "--dry-run", "--from", f.root, "--scope", "profile,secrets")
	if err != nil {
		t.Fatalf("dry-run should preview against the snapshot identity: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "one-stop restore complete") {
		t.Errorf("dry-run did not complete:\n%s", out)
	}
}

func TestOnestopRestoreApplyStepDryRun(t *testing.T) {
	f := newOnestopFixture(t)
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile"); err != nil {
		t.Fatal(err)
	}

	// --dry-run --apply: applyStep must short-circuit (no runApply, no
	// config.yaml write) and still appear in the summary.
	out, errOut, err := runDotForTest("restore", "--yes", "--dry-run", "--apply", "--from", f.root, "--scope", "profile")
	if err != nil {
		t.Fatalf("dry-run restore --apply: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "apply") || !strings.Contains(out, "would run dot apply") {
		t.Errorf("apply step missing or not short-circuited in dry-run:\n%s", out)
	}
	if !strings.Contains(out, "one-stop restore complete") {
		t.Errorf("restore did not complete:\n%s", out)
	}
}

func TestOnestopRestoreStateReloadFailureAborts(t *testing.T) {
	f := newOnestopFixture(t)
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile,ai"); err != nil {
		t.Fatal(err)
	}

	// Corrupt the snapshot's config.yaml with invalid YAML so the
	// post-profile state reload fails (an empty/missing file would load as
	// a zero value or fail the profile step instead).
	profileHost := filepath.Join(f.root, "profiles", f.host)
	snapCfg := filepath.Join(profileHost, latestVersion(t, profileHost), "config.yaml")
	writeCLITestFile(t, snapCfg, "{invalid yaml")

	// Mutate the live AI target so we can prove the AI step never ran.
	writeCLITestFile(t, filepath.Join(f.home, ".claude", "settings.json"), `{"model":"mutated"}`)

	_, _, err := runDotForTest("restore", "--yes", "--from", f.root, "--scope", "profile,ai")
	if err == nil || !strings.Contains(err.Error(), "state reload failed") {
		t.Fatalf("expected state-reload abort, got %v", err)
	}
	// AI step must not have run — abort happens right after the reload.
	got, _ := os.ReadFile(filepath.Join(f.home, ".claude", "settings.json"))
	if string(got) != `{"model":"mutated"}` {
		t.Errorf("AI step ran despite reload abort: %q", got)
	}
}

func TestHostFlagRejectsTraversal(t *testing.T) {
	_, root := newProfileCLIFixture(t)
	// Seed a real snapshot so the command would otherwise proceed.
	if _, _, err := runDotForTest("profile", "backup", "--to", root); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{
		{"profile", "list", "--from", root, "--host", "../../etc"},
		{"profile", "restore", "--from", root, "--host", "..", "--yes"},
		{"profile", "prune", "--from", root, "--host", "a/b", "--keep", "0", "--yes"},
		{"ai", "backup", "status", "--from", root, "--host", "../evil"},
	} {
		_, _, err := runDotForTest(cmd...)
		if err == nil || !strings.Contains(err.Error(), "invalid --host") {
			t.Errorf("%v: expected invalid --host rejection, got %v", cmd, err)
		}
	}
}

// TestPlanAgeIdentity pins the coupling that keeps a secrets backup
// recoverable: the .age archives are useless on another machine without the
// age identity, which only the profile snapshot carries.
func TestPlanAgeIdentity(t *testing.T) {
	cases := []struct {
		name           string
		scopes         []string
		canBackup      []string
		includeSecrets bool
		unattended     bool
		wantScopes     []string
		wantInclude    bool
		wantAdded      bool
		wantForced     bool
		wantOrphaned   bool
		wantAsk        bool
	}{{
		// The regression this exists for: `--yes --scope secrets` used to
		// archive .age files with no key anywhere in the backup.
		name:       "secrets alone pulls in profile and forces age keys",
		scopes:     []string{"secrets"},
		canBackup:  []string{"profile", "apps", "ai", "secrets"},
		unattended: true,
		wantScopes: []string{"profile", "secrets"},
		// scope order feeds only the plan display; steps run profile-first.
		wantInclude: true, wantAdded: true, wantForced: true,
	}, {
		name:        "secrets with profile already selected still forces age keys",
		scopes:      []string{"profile", "secrets"},
		canBackup:   []string{"profile", "secrets"},
		unattended:  true,
		wantScopes:  []string{"profile", "secrets"},
		wantInclude: true, wantForced: true,
	}, {
		name:           "explicit --include-secrets needs no forcing",
		scopes:         []string{"profile", "secrets"},
		canBackup:      []string{"profile", "secrets"},
		includeSecrets: true,
		unattended:     true,
		wantScopes:     []string{"profile", "secrets"},
		wantInclude:    true,
	}, {
		name:         "no profile snapshot to carry the identity is flagged",
		scopes:       []string{"secrets"},
		canBackup:    []string{"secrets"},
		unattended:   true,
		wantScopes:   []string{"secrets"},
		wantOrphaned: true,
	}, {
		name:       "profile alone is still asked interactively",
		scopes:     []string{"profile"},
		canBackup:  []string{"profile", "secrets"},
		wantScopes: []string{"profile"},
		wantAsk:    true,
	}, {
		name:       "profile alone unattended keeps the flag default",
		scopes:     []string{"profile"},
		canBackup:  []string{"profile", "secrets"},
		unattended: true,
		wantScopes: []string{"profile"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			selected := map[string]bool{}
			for _, s := range c.scopes {
				selected[s] = true
			}
			canBackup := map[string]bool{}
			for _, s := range c.canBackup {
				canBackup[s] = true
			}

			got := planAgeIdentity(c.scopes, selected, canBackup, c.includeSecrets, c.unattended)

			if strings.Join(got.Scopes, ",") != strings.Join(c.wantScopes, ",") {
				t.Errorf("Scopes = %v, want %v", got.Scopes, c.wantScopes)
			}
			if got.IncludeSecrets != c.wantInclude {
				t.Errorf("IncludeSecrets = %v, want %v", got.IncludeSecrets, c.wantInclude)
			}
			if got.AddedProfile != c.wantAdded {
				t.Errorf("AddedProfile = %v, want %v", got.AddedProfile, c.wantAdded)
			}
			if got.ForcedAgeKeys != c.wantForced {
				t.Errorf("ForcedAgeKeys = %v, want %v", got.ForcedAgeKeys, c.wantForced)
			}
			if got.Orphaned != c.wantOrphaned {
				t.Errorf("Orphaned = %v, want %v", got.Orphaned, c.wantOrphaned)
			}
			if got.AskAgeKeys != c.wantAsk {
				t.Errorf("AskAgeKeys = %v, want %v", got.AskAgeKeys, c.wantAsk)
			}
			// The step gating reads `selected`, not Scopes — a profile
			// pulled in for its age keys must show up there too.
			if got.AddedProfile && !selected["profile"] {
				t.Error("AddedProfile set but selected[profile] is false")
			}
		})
	}
}
