package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate_Profile(t *testing.T) {
	tests := []struct {
		profile string
		wantErr bool
	}{
		{"", false},
		{"minimal", false},
		{"full", false},
		{"server", false},
		{"MINIMAL", true},
		{"custom", true},
		{"unknown", true},
	}
	for _, tt := range tests {
		s := &UserState{Profile: tt.profile}
		err := s.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("Validate(profile=%q) err=%v, wantErr=%v", tt.profile, err, tt.wantErr)
		}
	}
}

func TestValidate_Email(t *testing.T) {
	tests := []struct {
		email   string
		wantErr bool
	}{
		{"", false},
		{"user@example.com", false},
		{"a@b", false},
		{"noat", true},
		{"spaces allowed@x.com", false}, // lenient
	}
	for _, tt := range tests {
		s := &UserState{Email: tt.email}
		err := s.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("Validate(email=%q) err=%v, wantErr=%v", tt.email, err, tt.wantErr)
		}
	}
}

func TestValidate_GithubUser(t *testing.T) {
	tests := []struct {
		user    string
		wantErr bool
	}{
		{"", false},
		{"entelecheia", false},
		{"user-name", false},
		{"User123", false},
		{"bad user", true},
		{"under_score", true},
		{"dot.name", true},
		{strings.Repeat("a", 40), true},
	}
	for _, tt := range tests {
		s := &UserState{GithubUser: tt.user}
		err := s.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("Validate(github_user=%q) err=%v, wantErr=%v", tt.user, err, tt.wantErr)
		}
	}
}

func TestValidate_GsyncSharedExcludes(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{"empty", nil, false},
		{"single relative", []string{"projects/koica-shared"}, false},
		{"multiple relative", []string{"projects/a", "external/b/c"}, false},
		{"absolute path rejected", []string{"/abs/path"}, true},
		{"parent escape rejected", []string{"../escape"}, true},
		{"parent escape mid-path rejected", []string{"projects/../../etc"}, true},
		{"single dot allowed", []string{"./relative"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &UserState{}
			s.Modules.Gsync.SharedExcludes = tt.entries
			err := s.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(shared_excludes=%v) err=%v, wantErr=%v", tt.entries, err, tt.wantErr)
			}
		})
	}
}

func TestValidate_TunnelState(t *testing.T) {
	validID := "123e4567-e89b-12d3-a456-426614174000"
	tests := []struct {
		name    string
		tunnel  UserTunnelState
		wantErr bool
	}{
		{
			name: "empty",
		},
		{
			name: "valid",
			tunnel: UserTunnelState{
				TunnelName: "dot-macbook",
				TunnelID:   validID,
				Hostname:   "mac.example.com",
			},
		},
		{
			name:    "uppercase hostname rejected",
			tunnel:  UserTunnelState{Hostname: "Mac.example.com"},
			wantErr: true,
		},
		{
			name:    "single label hostname rejected",
			tunnel:  UserTunnelState{Hostname: "mac"},
			wantErr: true,
		},
		{
			name:    "bad uuid rejected",
			tunnel:  UserTunnelState{TunnelID: "not-a-uuid"},
			wantErr: true,
		},
		{
			name:    "whitespace tunnel name rejected",
			tunnel:  UserTunnelState{TunnelName: "dot mac"},
			wantErr: true,
		},
		{
			name:    "long tunnel name rejected",
			tunnel:  UserTunnelState{TunnelName: strings.Repeat("a", 65)},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &UserState{}
			state.Modules.Tunnel = tt.tunnel
			err := state.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestTunnelStateSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := &UserState{}
	original.Modules.Tunnel = UserTunnelState{
		TunnelName: "dot-macbook",
		TunnelID:   "123e4567-e89b-12d3-a456-426614174000",
		Hostname:   "mac.example.com",
	}

	if err := saveStateAt(path, original); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if loaded.Modules.Tunnel != original.Modules.Tunnel {
		t.Fatalf("tunnel state = %#v, want %#v", loaded.Modules.Tunnel, original.Modules.Tunnel)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := &UserState{
		Name:       "Test User",
		Email:      "test@example.com",
		GithubUser: "testuser",
		Timezone:   "UTC",
		Profile:    "full",
	}
	original.Modules.Workspace.Path = "~/workspace"
	original.Modules.AI.Enabled = true

	if err := saveStateAt(path, original); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}

	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}

	if loaded.Name != original.Name {
		t.Errorf("Name: got %q, want %q", loaded.Name, original.Name)
	}
	if loaded.Email != original.Email {
		t.Errorf("Email: got %q, want %q", loaded.Email, original.Email)
	}
	if loaded.Profile != original.Profile {
		t.Errorf("Profile: got %q, want %q", loaded.Profile, original.Profile)
	}
	if loaded.Modules.Workspace.Path != original.Modules.Workspace.Path {
		t.Errorf("Workspace.Path: got %q, want %q", loaded.Modules.Workspace.Path, original.Modules.Workspace.Path)
	}
	if !loaded.Modules.AI.Enabled {
		t.Error("AI.Enabled: expected true")
	}
}

func TestLoadState_LegacyAIToolsMigratesToAI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("name: Test\nprofile: full\nmodules:\n  ai_tools: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if !loaded.Modules.AI.Enabled {
		t.Fatal("legacy ai_tools did not enable modules.ai.enabled")
	}
	if err := saveStateAt(path, loaded); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ai_tools") {
		t.Fatalf("legacy ai_tools key was persisted: %s", data)
	}
}

func TestLoadState_LegacyWarpMigratesToTerminalApps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("name: Test\nprofile: full\nmodules:\n  warp: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if !loaded.Modules.TerminalApps.Enabled || len(loaded.Modules.TerminalApps.Apps) != 1 || loaded.Modules.TerminalApps.Apps[0] != "warp" {
		t.Fatalf("legacy warp did not migrate to terminal_apps: %#v", loaded.Modules.TerminalApps)
	}
	cfg := &Config{Modules: ModulesConfig{Terminal: TermConfig{Enabled: true}}}
	ApplyStateToConfig(cfg, loaded)
	if !cfg.Modules.Terminal.Warp {
		t.Fatal("legacy warp should keep the Warp terminal flag enabled")
	}
	if len(cfg.Modules.Terminal.Apps) != 1 || cfg.Modules.Terminal.Apps[0] != "warp" {
		t.Fatalf("legacy warp should apply terminal app selection, got %v", cfg.Modules.Terminal.Apps)
	}
	if err := saveStateAt(path, loaded); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "warp:") {
		t.Fatalf("legacy warp key was persisted: %s", data)
	}
	if !strings.Contains(string(data), "terminal_apps:") {
		t.Fatalf("terminal_apps key missing after migration: %s", data)
	}
}

func TestTerminalAppsStateAppliesAndValidates(t *testing.T) {
	state := &UserState{}
	state.Modules.TerminalApps.Enabled = true
	state.Modules.TerminalApps.Apps = []string{"wave", "cmux"}
	cfg := &Config{Modules: ModulesConfig{Terminal: TermConfig{Enabled: true, Warp: true, Apps: []string{"warp"}}}}

	if err := state.Validate(); err != nil {
		t.Fatalf("Validate terminal apps: %v", err)
	}
	ApplyStateToConfig(cfg, state)
	if cfg.Modules.Terminal.Warp {
		t.Fatal("explicit terminal app selection without warp should disable Warp theme")
	}
	if got := strings.Join(cfg.Modules.Terminal.Apps, ","); got != "wave,cmux" {
		t.Fatalf("Terminal.Apps = %q, want wave,cmux", got)
	}

	state.Modules.TerminalApps.Apps = []string{"not-a-terminal"}
	if err := state.Validate(); err == nil {
		t.Fatal("expected invalid terminal app token to fail validation")
	}
}

func TestLoadState_LegacyTerminalCasksMigratesToApps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	input := "name: Test\nprofile: full\nmodules:\n  terminal_apps:\n    enabled: true\n    casks: [warp, wave]\n"
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if got := strings.Join(loaded.Modules.TerminalApps.Apps, ","); got != "warp,wave" {
		t.Fatalf("terminal apps = %q, want warp,wave", got)
	}

	if err := saveStateAt(path, loaded); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "casks:") {
		t.Fatalf("legacy terminal casks key was persisted: %s", data)
	}
	if !strings.Contains(string(data), "apps:") {
		t.Fatalf("canonical terminal apps key missing: %s", data)
	}
}

func TestLoadState_TerminalAppsWinsOverLegacyCasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	input := "name: Test\nprofile: full\nmodules:\n  terminal_apps:\n    enabled: true\n    apps: [orca]\n    casks: [warp]\n"
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if got := strings.Join(loaded.Modules.TerminalApps.Apps, ","); got != "orca" {
		t.Fatalf("terminal apps = %q, want orca", got)
	}
}

func TestLoadState_AIAgentsSSOTPersistsAndEnablesAI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("name: Test\nprofile: full\nmodules:\n  ai:\n    agents_ssot: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	if !loaded.Modules.AI.AgentsSSOT {
		t.Fatal("modules.ai.agents_ssot was not loaded")
	}

	cfg := &Config{}
	ApplyStateToConfig(cfg, loaded)
	if !cfg.Modules.AI.Enabled {
		t.Fatal("agents_ssot should opt in the ai module")
	}
	if !cfg.Modules.AI.AgentsSSOT {
		t.Fatal("agents_ssot was not applied to config")
	}
}

func TestLoadState_AIHUDPersistEnablesAI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("name: Test\nprofile: full\nmodules:\n  ai:\n    hud: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	cfg := &Config{}
	ApplyStateToConfig(cfg, loaded)
	if !cfg.Modules.AI.Enabled || !cfg.Modules.AI.HUD {
		t.Fatalf("AI HUD should enable AI module, got enabled=%v hud=%v", cfg.Modules.AI.Enabled, cfg.Modules.AI.HUD)
	}
}

// Legacy configs carrying the deprecated modules.ai.skills.enabled flag and
// the legacy anchor provider must still load without error; anchor is
// rewritten to maru at load (#373) and nothing acts on Enabled.
func TestLoadState_AISkillsPersistsAndEnablesAI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("name: Test\nprofile: full\nmodules:\n  ai:\n    skills:\n      enabled: true\n      provider: anchor\n      tools: [claude, codex]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	cfg := &Config{}
	ApplyStateToConfig(cfg, loaded)
	if !cfg.Modules.AI.Enabled {
		t.Fatalf("AI skills should enable AI module, got enabled=%v skills=%#v", cfg.Modules.AI.Enabled, cfg.Modules.AI.Skills)
	}
	if cfg.Modules.AI.Skills.Provider != "maru" || strings.Join(cfg.Modules.AI.Skills.Tools, ",") != "claude,codex" {
		t.Fatalf("skills config not applied: %#v", cfg.Modules.AI.Skills)
	}
}

func TestValidate_AISkillsConfig(t *testing.T) {
	tests := []struct {
		name   string
		skills AISkillsConfig
		errSub string
	}{
		{name: "empty"},
		{name: "maru ok", skills: AISkillsConfig{Provider: "maru", Tools: []string{"claude"}}},
		{name: "legacy anchor ok", skills: AISkillsConfig{Enabled: true, Provider: "anchor", Tools: []string{"claude"}}},
		{name: "path ok", skills: AISkillsConfig{Provider: "path", SSOTPath: "~/skills", Tools: []string{"codex"}}},
		{name: "legacy inventory-only tool", skills: AISkillsConfig{Provider: "maru", Tools: []string{"agents", "gemini", "antigravity"}}},
		{name: "legacy enabled without tools ok", skills: AISkillsConfig{Enabled: true, Provider: "maru"}},
		{name: "unknown provider", skills: AISkillsConfig{Provider: "bogus", Tools: []string{"claude"}}, errSub: "provider"},
		{name: "path missing ssot", skills: AISkillsConfig{Provider: "path", Tools: []string{"claude"}}, errSub: "ssot_path"},
		{name: "bad tool", skills: AISkillsConfig{Provider: "maru", Tools: []string{"cursor"}}, errSub: "tools entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &UserState{}
			state.Modules.AI.Skills = tt.skills
			err := state.Validate()
			if tt.errSub == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("Validate err = %v, want substring %q", err, tt.errSub)
			}
		})
	}
}

func TestLoadState_CoauthoredGuardPersistsAndEnablesGit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("name: Test\nprofile: full\nmodules:\n  git:\n    coauthor_guard: warn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt: %v", err)
	}
	cfg := &Config{}
	ApplyStateToConfig(cfg, loaded)
	if !cfg.Modules.Git.Enabled || cfg.Modules.Git.CoauthorGuard != "warn" {
		t.Fatalf("coauthor guard should enable git module, got enabled=%v mode=%q", cfg.Modules.Git.Enabled, cfg.Modules.Git.CoauthorGuard)
	}
}

func TestSave_AtomicNoTempLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	state := &UserState{Profile: "minimal"}
	if err := saveStateAt(path, state); err != nil {
		t.Fatalf("saveStateAt: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") || strings.Contains(name, ".config.yaml.") {
			t.Errorf("temp file not cleaned up: %s", name)
		}
	}
}

func TestSave_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	invalid := &UserState{Profile: "bogus"}
	err := saveStateAt(path, invalid)
	if err == nil {
		t.Fatal("expected error for invalid profile, got nil")
	}
	if !strings.Contains(err.Error(), "invalid profile") {
		t.Errorf("unexpected error: %v", err)
	}

	// File should NOT exist
	if _, err := os.Stat(path); err == nil {
		t.Error("config file written despite validation failure")
	}
}

func TestLoad_MissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")

	state, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("loadStateAt missing: %v", err)
	}
	if state == nil {
		t.Fatal("expected empty state, got nil")
		return
	}
	if state.Name != "" || state.Profile != "" {
		t.Errorf("expected zero value, got %+v", state)
	}
}

func TestLoad_CorruptYAMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("this: is: not: valid: yaml: [\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := loadStateAt(path)
	if err == nil {
		t.Fatal("expected error for corrupt YAML, got nil")
	}
	if !strings.Contains(err.Error(), "parsing state") {
		t.Errorf("unexpected error: %v", err)
	}
}

// modules.workspace.ahub_keys in the user config survives a save and reaches
// the template data that renders the tmux menu (#331).
func TestUserState_AhubKeysReachesTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("modules:\n  workspace:\n    path: ~/workspace\n    ahub_keys: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveStateAt(path, loaded); err != nil {
		t.Fatal(err)
	}
	if loaded, err = loadStateAt(path); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	ApplyStateToConfig(cfg, loaded)
	if got := cfg.TemplateData(t.TempDir())["AhubKeys"]; got != true {
		t.Fatalf("AhubKeys = %v, want true after a save and reload", got)
	}
}
