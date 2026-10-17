package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestDetectKeysUseProvidedHome(t *testing.T) {
	invoker := t.TempDir()
	target := t.TempDir()
	t.Setenv("HOME", invoker)
	for _, home := range []string{invoker, target} {
		if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct{ home, name string }{{invoker, "age_key_invoker"}, {target, "age_key_target"}} {
		if err := os.WriteFile(filepath.Join(pair.home, ".ssh", pair.name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct{ home, name string }{{invoker, "id_ed25519_invoker"}, {target, "id_ed25519_target"}} {
		if err := os.WriteFile(filepath.Join(pair.home, ".ssh", pair.name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pair.home, ".ssh", pair.name+".pub"), []byte("public"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := detectAgeKeys(target), []string{"~/.ssh/age_key_target"}; !slices.Equal(got, want) {
		t.Errorf("detectAgeKeys(target) = %v, want %v", got, want)
	}
	if got, want := detectSSHKeys(target), []string{"id_ed25519_target"}; !slices.Equal(got, want) {
		t.Errorf("detectSSHKeys(target) = %v, want %v", got, want)
	}
}

func TestReadAgePublicKeyUsesProvidedHome(t *testing.T) {
	invoker := t.TempDir()
	target := t.TempDir()
	for _, pair := range []struct{ home, recipient string }{{invoker, "age1invoker"}, {target, "age1target"}} {
		if err := os.MkdirAll(filepath.Join(pair.home, ".ssh"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pair.home, ".ssh", "age_key.pub"), []byte(pair.recipient+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := readAgePublicKey(target, "~/.ssh/age_key"), "age1target"; got != want {
		t.Errorf("readAgePublicKey(target) = %q, want %q", got, want)
	}
}

func TestConfigureMacAppsUsesProvidedHomeForCloudBackup(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS app configuration is darwin-only")
	}
	invoker := t.TempDir()
	target := t.TempDir()
	t.Setenv("HOME", invoker)
	secrets := filepath.Join(target, "Library", "CloudStorage", "Dropbox", "secrets")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}

	state := &config.UserState{}
	if err := ConfigureMacApps(state, target, "full", true); err != nil {
		t.Fatalf("ConfigureMacApps: %v", err)
	}
	if got, want := state.Modules.MacApps.BackupRoot, filepath.Join(secrets, "dotfiles-backup"); got != want {
		t.Errorf("BackupRoot = %q, want %q", got, want)
	}
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectCloudMountsDropboxBeforeDrive(t *testing.T) {
	home := t.TempDir()
	cloud := filepath.Join(home, "Library", "CloudStorage")
	mkdirs(t,
		filepath.Join(cloud, "GoogleDrive-a@x.com", "My Drive"),
		filepath.Join(cloud, "Dropbox"),
		filepath.Join(cloud, "GoogleDrive-b@y.com"),
	)

	got := detectCloudMountsIn(home, t.TempDir())
	want := []string{
		filepath.Join(cloud, "Dropbox"),
		filepath.Join(cloud, "GoogleDrive-a@x.com", "My Drive"),
		filepath.Join(cloud, "GoogleDrive-b@y.com"),
	}
	if len(got) != len(want) {
		t.Fatalf("mounts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mounts[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestDetectCloudMountsDedupsHomeSymlink(t *testing.T) {
	home := t.TempDir()
	cloud := filepath.Join(home, "Library", "CloudStorage")
	mkdirs(t, filepath.Join(cloud, "Dropbox"))
	// Dropbox app-style convenience symlink in the home dir.
	if err := os.Symlink(filepath.Join(cloud, "Dropbox"), filepath.Join(home, "Dropbox")); err != nil {
		t.Fatal(err)
	}

	got := detectCloudMountsIn(home, t.TempDir())
	if len(got) != 1 || got[0] != filepath.Join(cloud, "Dropbox") {
		t.Fatalf("expected single canonical Dropbox mount, got %v", got)
	}
}

func TestDetectCloudMountsSkipsBrokenSymlinkAndFiles(t *testing.T) {
	home := t.TempDir()
	cloud := filepath.Join(home, "Library", "CloudStorage")
	mkdirs(t, cloud)
	// Broken symlink left behind by an uninstalled Dropbox app.
	if err := os.Symlink(filepath.Join(cloud, "Dropbox"), filepath.Join(home, "Dropbox")); err != nil {
		t.Fatal(err)
	}
	// Stray metadata file that matches the Drive prefix.
	if err := os.WriteFile(filepath.Join(cloud, "GoogleDrive-a@x.com"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := detectCloudMountsIn(home, t.TempDir()); len(got) != 0 {
		t.Fatalf("broken symlink / file candidates should be skipped, got %v", got)
	}
}

func TestDetectCloudMountsVolumesDrive(t *testing.T) {
	volumes := t.TempDir()
	mkdirs(t, filepath.Join(volumes, "GoogleDrive"))

	got := detectCloudMountsIn(t.TempDir(), volumes)
	if len(got) != 1 || got[0] != filepath.Join(volumes, "GoogleDrive") {
		t.Fatalf("expected /Volumes-style Drive mount, got %v", got)
	}
}

func TestDetectCloudMountsLegacyHomeDrive(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, filepath.Join(home, "My Drive (a@x.com)"))

	got := detectCloudMountsIn(home, t.TempDir())
	if len(got) != 1 || got[0] != filepath.Join(home, "My Drive (a@x.com)") {
		t.Fatalf("expected legacy home Drive mount, got %v", got)
	}
}

func TestDetectCloudMountsEmptyHome(t *testing.T) {
	if got := detectCloudMountsIn(t.TempDir(), t.TempDir()); len(got) != 0 {
		t.Fatalf("expected no mounts, got %v", got)
	}
}

func TestDetectVaultCandidatesPrefersWorkVault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := filepath.Join(home, "workspace")
	mkdirs(t, filepath.Join(ws, "work", "vault"), filepath.Join(ws, "vault"))

	got := detectVaultCandidates("~/workspace")
	want := []string{"~/workspace/work/vault", "~/workspace/vault"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestDetectVaultCandidatesFallsBackToTopLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mkdirs(t, filepath.Join(home, "workspace", "vault"))

	got := detectVaultCandidates("~/workspace")
	if len(got) != 1 || got[0] != "~/workspace/vault" {
		t.Fatalf("candidates = %v, want [~/workspace/vault]", got)
	}
}

func TestDetectVaultCandidatesNone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mkdirs(t, filepath.Join(home, "workspace"))

	if got := detectVaultCandidates("~/workspace"); len(got) != 0 {
		t.Fatalf("expected no candidates, got %v", got)
	}
}

// Fresh unattended init picks the detected vault location (work/vault first).
func TestConfigureWorkspaceUnattendedFreshPicksDetectedVault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mkdirs(t, filepath.Join(home, "workspace", "work", "vault"))

	state := &config.UserState{}
	state.Modules.Workspace.Path = "~/workspace"
	if err := ConfigureWorkspace(state, "full", true); err != nil {
		t.Fatalf("ConfigureWorkspace unattended fresh: %v", err)
	}
	if got, want := state.Modules.Workspace.Vault, "~/workspace/work/vault"; got != want {
		t.Fatalf("vault = %q, want %q", got, want)
	}
}

// Fresh unattended init with only a top-level vault picks that one.
func TestConfigureWorkspaceUnattendedFreshPicksTopLevelVault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mkdirs(t, filepath.Join(home, "workspace", "vault"))

	state := &config.UserState{}
	state.Modules.Workspace.Path = "~/workspace"
	if err := ConfigureWorkspace(state, "full", true); err != nil {
		t.Fatalf("ConfigureWorkspace unattended fresh: %v", err)
	}
	if got, want := state.Modules.Workspace.Vault, "~/workspace/vault"; got != want {
		t.Fatalf("vault = %q, want %q", got, want)
	}
}

// Unattended reconfigure must preserve an existing vault choice exactly,
// regardless of what detection would pick.
func TestConfigureWorkspaceUnattendedPreservesVaultState(t *testing.T) {
	state := &config.UserState{}
	state.Modules.Workspace.Path = "~/workspace"
	state.Modules.Workspace.Vault = "~/elsewhere/vault"

	if err := ConfigureWorkspace(state, "full", true); err != nil {
		t.Fatalf("ConfigureWorkspace unattended: %v", err)
	}
	if state.Modules.Workspace.Vault != "~/elsewhere/vault" {
		t.Fatalf("vault changed: %q", state.Modules.Workspace.Vault)
	}
}

func TestDefaultCloudSymlink(t *testing.T) {
	for path, want := range map[string]string{
		"/Users/u/Library/CloudStorage/Dropbox":                  "~/Dropbox",
		"/Users/u/Library/CloudStorage/GoogleDrive-a@x/My Drive": "~/gdrive-workspace",
		"/Users/u/My Drive (a@x.com)":                            "~/gdrive-workspace",
	} {
		if got := defaultCloudSymlink(path); got != want {
			t.Errorf("defaultCloudSymlink(%q) = %q, want %q", path, got, want)
		}
	}
}

// Fresh unattended init on a machine with both clouds picks Dropbox first.
func TestConfigureWorkspaceUnattendedFreshPicksDropbox(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("cloud mirror prompt is darwin-only")
	}
	home := t.TempDir()
	cloud := filepath.Join(home, "Library", "CloudStorage")
	mkdirs(t,
		filepath.Join(cloud, "GoogleDrive-a@x.com", "My Drive"),
		filepath.Join(cloud, "Dropbox"),
	)
	t.Setenv("HOME", home)

	state := &config.UserState{}
	state.Modules.Workspace.Path = "~/workspace"
	if err := ConfigureWorkspace(state, "full", true); err != nil {
		t.Fatalf("ConfigureWorkspace unattended fresh: %v", err)
	}
	if got, want := state.Modules.Workspace.Gdrive, filepath.Join(cloud, "Dropbox"); got != want {
		t.Fatalf("fresh cloud root = %q, want %q", got, want)
	}
	if state.Modules.Workspace.GdriveSymlink != "~/Dropbox" {
		t.Fatalf("fresh cloud symlink = %q, want ~/Dropbox", state.Modules.Workspace.GdriveSymlink)
	}
}

// Unattended reconfigure must preserve an existing cloud choice exactly,
// regardless of what this machine's detection would pick.
func TestConfigureWorkspaceUnattendedPreservesCloudState(t *testing.T) {
	state := &config.UserState{}
	state.Modules.Workspace.Path = "~/workspace"
	state.Modules.Workspace.Gdrive = "/custom/cloud/root"
	state.Modules.Workspace.GdriveSymlink = "~/cloud-link"

	if err := ConfigureWorkspace(state, "full", true); err != nil {
		t.Fatalf("ConfigureWorkspace unattended: %v", err)
	}
	if state.Modules.Workspace.Gdrive != "/custom/cloud/root" {
		t.Fatalf("cloud root changed: %q", state.Modules.Workspace.Gdrive)
	}
	if state.Modules.Workspace.GdriveSymlink != "~/cloud-link" {
		t.Fatalf("cloud symlink changed: %q", state.Modules.Workspace.GdriveSymlink)
	}
}
