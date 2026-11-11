package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestIsModuleEnabled(t *testing.T) {
	cfg := &Config{
		Modules: ModulesConfig{
			Packages:  ModuleToggle{Enabled: true},
			Shell:     ShellConfig{Enabled: true},
			Node:      ModuleToggle{Enabled: true},
			Git:       GitConfig{Enabled: false},
			SSH:       SSHModConfig{Enabled: true},
			Terminal:  TermConfig{Enabled: false},
			Tmux:      ModuleToggle{Enabled: true},
			Workspace: WorkConfig{Enabled: false},
			AI:        AIConfig{Enabled: true},
			Fonts:     FontsConfig{Enabled: false},
			Conda:     ModuleToggle{Enabled: true},
			GPG:       ModuleToggle{Enabled: false},
			Secrets:   ModuleToggle{Enabled: true},
			MacApps:   MacAppsConfig{Enabled: true},
			Container: ContainerConfig{Enabled: true},
		},
	}

	cases := []struct {
		name string
		want bool
	}{
		{"packages", true},
		{"shell", true},
		{"node", true},
		{"git", false},
		{"ssh", true},
		{"terminal", false},
		{"tmux", true},
		{"workspace", false},
		{"ai", true},
		{"fonts", false},
		{"conda", true},
		{"gpg", false},
		{"secrets", true},
		{"macapps", true},
		{"container", true},
		{"unknown", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.IsModuleEnabled(tc.name)
			if got != tc.want {
				t.Errorf("IsModuleEnabled(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestAllPackages_Deduplication(t *testing.T) {
	cfg := &Config{
		Packages:      []string{"git", "curl", "fzf"},
		PackagesExtra: []string{"fzf", "btop", "curl", "lazygit"},
	}

	all := cfg.AllPackages()

	// Should deduplicate: git, curl, fzf, btop, lazygit = 5 unique
	if len(all) != 5 {
		t.Errorf("AllPackages: expected 5 unique packages, got %d: %v", len(all), all)
	}

	seen := make(map[string]int)
	for _, p := range all {
		seen[p]++
	}
	for pkg, count := range seen {
		if count > 1 {
			t.Errorf("AllPackages: duplicate package %q appears %d times", pkg, count)
		}
	}
}

func TestAllPackages_OrderPreserved(t *testing.T) {
	cfg := &Config{
		Packages:      []string{"git", "curl"},
		PackagesExtra: []string{"btop", "lazygit"},
	}

	all := cfg.AllPackages()
	expected := []string{"git", "curl", "btop", "lazygit"}
	if len(all) != len(expected) {
		t.Fatalf("AllPackages: expected %d packages, got %d", len(expected), len(all))
	}
	for i, p := range expected {
		if all[i] != p {
			t.Errorf("AllPackages[%d]: expected %q, got %q", i, p, all[i])
		}
	}
}

func TestAllPackages_EmptyExtra(t *testing.T) {
	cfg := &Config{
		Packages: []string{"git", "curl"},
	}
	all := cfg.AllPackages()
	if len(all) != 2 {
		t.Errorf("AllPackages with no extra: expected 2, got %d", len(all))
	}
}

func TestAllCasks_DeduplicatesAndPreservesOrder(t *testing.T) {
	cfg := &Config{
		Casks:      []string{"raycast", "obsidian", "arc"},
		CasksExtra: []string{"obsidian", "slack", "arc", "zed"},
	}
	all := cfg.AllCasks()
	expected := []string{"raycast", "obsidian", "arc", "slack", "zed"}
	if len(all) != len(expected) {
		t.Fatalf("AllCasks: expected %d, got %d: %v", len(expected), len(all), all)
	}
	for i, v := range expected {
		if all[i] != v {
			t.Errorf("AllCasks[%d] = %q, want %q", i, all[i], v)
		}
	}
}

func TestAllCasks_IncludesTerminalAppsWhenEnabled(t *testing.T) {
	cfg := &Config{
		Casks:      []string{"raycast", "warp"},
		CasksExtra: []string{"cmux", "raycast"},
		Modules: ModulesConfig{
			Terminal: TermConfig{
				Enabled: true,
				Apps:    []string{"warp", "wave", "cmux"},
			},
		},
	}
	all := cfg.AllCasks()
	expected := []string{"raycast", "warp", "wave", "cmux"}
	if len(all) != len(expected) {
		t.Fatalf("AllCasks: expected %d, got %d: %v", len(expected), len(all), all)
	}
	for i, v := range expected {
		if all[i] != v {
			t.Errorf("AllCasks[%d] = %q, want %q", i, all[i], v)
		}
	}
}

func TestEditorMapping(t *testing.T) {
	cases := []struct {
		editor, cmdDarwin, cmdLinux, cask string
	}{
		{"zed", "zed --wait", "zed --wait", "zed"},
		{"code", "code --wait", "code --wait", "visual-studio-code"},
		{"vi", "vi", "vi", ""},
		{"", "zed --wait", "nano", ""},
	}
	for _, c := range cases {
		if got := EditorCommand(c.editor, true); got != c.cmdDarwin {
			t.Errorf("EditorCommand(%q, darwin) = %q, want %q", c.editor, got, c.cmdDarwin)
		}
		if got := EditorCommand(c.editor, false); got != c.cmdLinux {
			t.Errorf("EditorCommand(%q, linux) = %q, want %q", c.editor, got, c.cmdLinux)
		}
		if got := EditorCask(c.editor); got != c.cask {
			t.Errorf("EditorCask(%q) = %q, want %q", c.editor, got, c.cask)
		}
	}
}

func TestAllCasks_IncludesEditorCask(t *testing.T) {
	cfg := &Config{
		Casks:   []string{"raycast"},
		Modules: ModulesConfig{Shell: ShellConfig{Editor: "code"}},
	}
	all := cfg.AllCasks()
	if all[len(all)-1] != "visual-studio-code" {
		t.Fatalf("AllCasks missing editor cask: %v", all)
	}

	// Already-listed editor cask is not duplicated.
	cfg = &Config{
		Casks:   []string{"zed"},
		Modules: ModulesConfig{Shell: ShellConfig{Editor: "zed"}},
	}
	if all := cfg.AllCasks(); len(all) != 1 {
		t.Fatalf("AllCasks duplicated editor cask: %v", all)
	}

	// vi needs no install.
	cfg = &Config{Modules: ModulesConfig{Shell: ShellConfig{Editor: "vi"}}}
	if all := cfg.AllCasks(); len(all) != 0 {
		t.Fatalf("AllCasks should be empty for vi: %v", all)
	}
}

func TestValidate_Editor(t *testing.T) {
	for _, v := range []string{"", "zed", "code", "vi"} {
		s := &UserState{Modules: UserModulesState{Editor: v}}
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(editor=%q) unexpected error: %v", v, err)
		}
	}
	s := &UserState{Modules: UserModulesState{Editor: "emacs"}}
	if err := s.Validate(); err == nil {
		t.Error("Validate(editor=emacs) expected error, got nil")
	}
}

func TestTerminalCatalogIncludesRequestedOptions(t *testing.T) {
	for _, token := range []string{"orca", "warp", "wave", "cmux", "iterm2"} {
		if !IsTerminalAppToken(token) {
			t.Fatalf("terminal app catalog missing %q", token)
		}
	}
}

func TestDefaultTerminalAppsByPlatform(t *testing.T) {
	mac := &SystemInfo{OS: "darwin"}
	arch := &SystemInfo{OS: "linux", DistroID: "arch"}
	ubuntu := &SystemInfo{OS: "linux", DistroID: "ubuntu"}

	for name, system := range map[string]*SystemInfo{"mac": mac, "arch": arch} {
		t.Run(name, func(t *testing.T) {
			got := DefaultTerminalApps("full", system)
			if len(got) != 1 || got[0] != "orca" {
				t.Fatalf("DefaultTerminalApps(full) = %v, want [orca]", got)
			}
		})
	}
	if got := DefaultTerminalApps("minimal", mac); len(got) != 0 {
		t.Fatalf("DefaultTerminalApps(minimal) = %v, want empty", got)
	}
	if got := DefaultTerminalApps("full", ubuntu); len(got) != 0 {
		t.Fatalf("DefaultTerminalApps(full, ubuntu) = %v, want empty", got)
	}
}

func TestTemplateData_Keys(t *testing.T) {
	cfg := &Config{
		Name:       "Test User",
		Email:      "test@example.com",
		GithubUser: "testuser",
		Timezone:   "UTC",
		System: &SystemInfo{
			OS:       "darwin",
			Arch:     "arm64",
			Hostname: "testhost",
		},
		Modules: ModulesConfig{
			Workspace: WorkConfig{Enabled: true, Path: "/home/test", Gdrive: "gdrive"},
			AI:        AIConfig{Enabled: true},
			Terminal:  TermConfig{Warp: true},
			SSH:       SSHModConfig{KeyName: "id_ed25519"},
			Git:       GitConfig{Signing: true},
			Fonts:     FontsConfig{Family: "FiraCode"},
		},
	}

	data := cfg.TemplateData(t.TempDir())

	requiredKeys := []string{
		"Home",
		"Name", "Email", "GithubUser", "Timezone",
		"Hostname", "IsDarwin",
		"EnableWorkspace", "EnableAI",
		"WorkspacePath", "VaultPath", "CloudSymlink",
		"SSHKeyName", "CoauthorGuard",
		"HasCUDA", "CUDAHome", "HasNVIDIAGPU",
	}
	for _, k := range requiredKeys {
		if _, ok := data[k]; !ok {
			t.Errorf("TemplateData: missing key %q", k)
		}
	}

	// Spot-check values
	if data["Name"] != "Test User" {
		t.Errorf("TemplateData[Name] = %v, want %q", data["Name"], "Test User")
	}
	if data["IsDarwin"] != true {
		t.Errorf("TemplateData[IsDarwin] = %v, want true", data["IsDarwin"])
	}
	if data["EnableWorkspace"] != true {
		t.Errorf("TemplateData[EnableWorkspace] = %v, want true", data["EnableWorkspace"])
	}
}

func TestTemplateData_NilSystem(t *testing.T) {
	cfg := &Config{}
	data := cfg.TemplateData(t.TempDir())

	if data["IsDarwin"] != false {
		t.Errorf("TemplateData[IsDarwin] with nil System = %v, want false", data["IsDarwin"])
	}
	if data["Hostname"] != "" {
		t.Errorf("TemplateData[Hostname] with nil System = %v, want empty string", data["Hostname"])
	}
}

func TestVaultPath_Explicit(t *testing.T) {
	cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{
		Path:  "~/workspace",
		Vault: "~/custom/vault",
	}}}
	if got, want := cfg.VaultPath(t.TempDir()), "~/custom/vault"; got != want {
		t.Errorf("VaultPath() = %q, want %q", got, want)
	}
}

func TestVaultPath_DetectsWorkVault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "workspace", "work", "vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{Path: "~/workspace"}}}
	if got, want := cfg.VaultPath(home), "~/workspace/work/vault"; got != want {
		t.Errorf("VaultPath() = %q, want %q", got, want)
	}
}

func TestVaultPath_DetectsTopLevelVault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "workspace", "vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{Path: "~/workspace"}}}
	if got, want := cfg.VaultPath(home), "~/workspace/vault"; got != want {
		t.Errorf("VaultPath() = %q, want %q", got, want)
	}
}

func TestVaultPath_FreshDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{Path: "~/workspace"}}}
	if got, want := cfg.VaultPath(home), "~/workspace/work/vault"; got != want {
		t.Errorf("VaultPath() = %q, want %q", got, want)
	}
}

func TestVaultPath_EmptyWorkspacePath(t *testing.T) {
	cfg := &Config{}
	if got := cfg.VaultPath(t.TempDir()); got != "" {
		t.Errorf("VaultPath() with empty workspace path = %q, want empty", got)
	}
}

func TestVaultPath_RelativeAnchoredUnderWorkspace(t *testing.T) {
	for vault, want := range map[string]string{
		"work/vault": "~/workspace/work/vault",
		"vault":      "~/workspace/vault",
	} {
		cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{Path: "~/workspace", Vault: vault}}}
		if got := cfg.VaultPath(t.TempDir()); got != want {
			t.Errorf("VaultPath() with vault=%q = %q, want %q", vault, got, want)
		}
	}
}

func TestVaultCloneTarget_Explicit(t *testing.T) {
	cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{Path: "~/workspace", Vault: "work/vault"}}}
	if got, want := cfg.VaultCloneTarget(t.TempDir()), "~/workspace/work/vault"; got != want {
		t.Errorf("VaultCloneTarget() = %q, want %q", got, want)
	}
}

func TestVaultCloneTarget_Detected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "workspace", "work", "vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{Path: "~/workspace"}}}
	if got, want := cfg.VaultCloneTarget(home), "~/workspace/work/vault"; got != want {
		t.Errorf("VaultCloneTarget() = %q, want %q", got, want)
	}
}

// Legacy setups (separate vault repo, no workspace.vault key) on a fresh
// machine must keep the legacy <ws>/vault target: no fresh-default redirect.
func TestVaultCloneTarget_LegacyFallthrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{Modules: ModulesConfig{Workspace: WorkConfig{Path: "~/workspace"}}}
	if got := cfg.VaultCloneTarget(home); got != "" {
		t.Errorf("VaultCloneTarget() with nothing on disk = %q, want empty (legacy <ws>/vault)", got)
	}
}

// An integer count of seconds is multiplied by time.Second, and one past the
// int64 boundary wraps: -18446744073 becomes a positive 709.551616ms, which
// then passes every "not negative" check downstream. Both duration types
// decode their integer form through the same bound and refuse it.
func TestDurationRejectsSecondCountsThatWouldOverflow(t *testing.T) {
	for _, text := range []string{"-18446744073", "18446744074"} {
		var block struct {
			Value OptionalDuration `yaml:"value"`
		}
		err := yaml.Unmarshal([]byte("value: "+text+"\n"), &block)
		if err == nil {
			t.Errorf("yaml %s decoded to %s, want a rejection naming the value", text, block.Value.Std())
		} else if !strings.Contains(err.Error(), text) || !strings.Contains(err.Error(), "seconds") {
			t.Errorf("yaml %s error = %q, want the value and the accepted form", text, err)
		}
		var plain Duration
		if err := yaml.Unmarshal([]byte(text), &plain); err == nil {
			t.Errorf("Duration %s decoded to %s, want a rejection", text, plain.Std())
		}
	}

	// The boundary itself still decodes, in both spellings of the largest count.
	for _, text := range []string{"9223372036", "-9223372036", "300"} {
		var value Duration
		if err := yaml.Unmarshal([]byte(text), &value); err != nil {
			t.Errorf("yaml %s = %v, want it accepted", text, err)
			continue
		}
		want, err := time.ParseDuration(text + "s")
		if err != nil {
			t.Fatalf("reference duration for %s: %v", text, err)
		}
		if value.Std() != want {
			t.Errorf("yaml %s = %s, want %s", text, value.Std(), want)
		}
	}

	// A duration string keeps its own path and its own error wording.
	var value Duration
	if err := yaml.Unmarshal([]byte("1m"), &value); err != nil || value.Std() != time.Minute {
		t.Errorf("yaml 1m = %s, %v; want 1m0s", value.Std(), err)
	}
}

// The admission block is the state file's consumer of the same decode: a state
// carrying the wrapped value must fail to load instead of arriving as 709ms.
func TestLoadStateRefusesAnOverflowingAdmissionDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "schema_version: 6\nadmission:\n  thresholds:\n    recover_sustain: -18446744073\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := loadStateAt(path)
	if err == nil {
		t.Fatalf("loadStateAt accepted the value: recover_sustain = %v", state.Admission.Thresholds.RecoverSustain)
	}
	if !strings.Contains(err.Error(), "-18446744073") {
		t.Errorf("load error = %q, want the value named", err)
	}
}
