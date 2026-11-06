package template

import (
	"strings"
	"testing"
)

func TestRender_StaticGitIgnore(t *testing.T) {
	e := NewEngine()

	out, err := e.Render("git/gitignore.global", nil)
	if err != nil {
		t.Fatalf("Render git/gitignore.global: %v", err)
	}

	content := string(out)
	// git/gitignore.global is a static file; check known entries
	for _, entry := range []string{".DS_Store", ".env*", "!.envrc", "node_modules/", ".firecrawl/"} {
		if !strings.Contains(content, entry) {
			t.Errorf("Render git/gitignore.global: expected %q in output", entry)
		}
	}
	// Regression for #142: a global *.sql rule silently hid new migration
	// files in repos that track SQL. The header documents the decision, so
	// only an active (non-comment) pattern line may fail the check.
	for line := range strings.Lines(content) {
		if strings.TrimSpace(line) == "*.sql" {
			t.Error("Render git/gitignore.global: *.sql must not be an active ignore pattern")
		}
	}
}

func TestRender_InvalidTemplate(t *testing.T) {
	e := NewEngine()

	_, err := e.Render("nonexistent/template", nil)
	if err == nil {
		t.Error("Render with nonexistent template: expected error, got nil")
	}
}

func TestReadStatic(t *testing.T) {
	e := NewEngine()

	out, err := e.ReadStatic("git/gitignore.global")
	if err != nil {
		t.Fatalf("ReadStatic git/gitignore.global: %v", err)
	}

	if len(out) == 0 {
		t.Error("ReadStatic git/gitignore.global: expected non-empty content")
	}

	if !strings.Contains(string(out), ".DS_Store") {
		t.Error("ReadStatic git/gitignore.global: expected .DS_Store in content")
	}
}

func TestReadStatic_Missing(t *testing.T) {
	e := NewEngine()

	_, err := e.ReadStatic("nonexistent/file")
	if err == nil {
		t.Error("ReadStatic missing file: expected error, got nil")
	}
}

func TestRenderString(t *testing.T) {
	e := NewEngine()

	tmpl := "Hello, {{.Name}}! Email: {{.Email}}"
	data := map[string]any{
		"Name":  "Alice",
		"Email": "alice@example.com",
	}

	out, err := e.RenderString("test", tmpl, data)
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}

	got := string(out)
	if got != "Hello, Alice! Email: alice@example.com" {
		t.Errorf("RenderString: got %q, want %q", got, "Hello, Alice! Email: alice@example.com")
	}
}

func TestRenderString_WithFuncMap(t *testing.T) {
	e := NewEngine()

	// Test the custom func map functions registered in NewEngine
	cases := []struct {
		name string
		tmpl string
		data map[string]any
		want string
	}{
		{
			name: "toLower",
			tmpl: `{{toLower .V}}`,
			data: map[string]any{"V": "HELLO"},
			want: "hello",
		},
		{
			name: "toUpper",
			tmpl: `{{toUpper .V}}`,
			data: map[string]any{"V": "hello"},
			want: "HELLO",
		},
		{
			name: "trimSpace",
			tmpl: `{{trimSpace .V}}`,
			data: map[string]any{"V": "  hi  "},
			want: "hi",
		},
		{
			name: "replace",
			tmpl: `{{replace .V "foo" "bar"}}`,
			data: map[string]any{"V": "foo baz foo"},
			want: "bar baz bar",
		},
		{
			name: "join",
			tmpl: `{{join .V ", "}}`,
			data: map[string]any{"V": []string{"a", "b", "c"}},
			want: "a, b, c",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := e.RenderString(tc.name, tc.tmpl, tc.data)
			if err != nil {
				t.Fatalf("RenderString %s: %v", tc.name, err)
			}
			if string(out) != tc.want {
				t.Errorf("RenderString %s: got %q, want %q", tc.name, string(out), tc.want)
			}
		})
	}
}

func TestRenderString_InvalidSyntax(t *testing.T) {
	e := NewEngine()

	_, err := e.RenderString("bad", "{{.Unclosed", nil)
	if err == nil {
		t.Error("RenderString with invalid syntax: expected error, got nil")
	}
}

func TestRenderString_ExpandHomeIsNotAvailable(t *testing.T) {
	e := NewEngine()

	_, err := e.RenderString("expand-home", `{{expandHome "~/workspace"}}`, nil)
	if err == nil {
		t.Fatal("RenderString expandHome: expected removed function to fail parsing")
	}
}

func TestRender_ToolsInitAIToolPaths(t *testing.T) {
	e := NewEngine()

	out, err := e.Render("shell/50-tools-init.sh.tmpl", nil)
	if err != nil {
		t.Fatalf("Render shell/50-tools-init.sh.tmpl: %v", err)
	}

	content := string(out)
	// AI CLI installers append these PATH lines to ~/.zshrc, which dot
	// regenerates; the managed fragment must provide them instead.
	for _, line := range []string{
		`[ -d "$HOME/.opencode/bin" ] && export PATH="$HOME/.opencode/bin:$PATH"`,
		`[ -d "$HOME/.kilo/bin" ] && export PATH="$HOME/.kilo/bin:$PATH"`,
		`[ -d "$HOME/.kimi-code/bin" ] && export PATH="$HOME/.kimi-code/bin:$PATH"`,
		`[ -d "$HOME/.bun/bin" ] && export PATH="$PATH:$HOME/.bun/bin"`,
	} {
		if !strings.Contains(content, line) {
			t.Errorf("Render shell/50-tools-init.sh.tmpl: expected guard line %q in output", line)
		}
	}
}

func TestRender_AIYoloHelpers(t *testing.T) {
	e := NewEngine()

	out, err := e.Render("shell/30-ai.sh.tmpl", map[string]any{
		"EnableAI":     true,
		"HasNVIDIAGPU": false,
	})
	if err != nil {
		t.Fatalf("Render shell/30-ai.sh.tmpl: %v", err)
	}

	content := string(out)
	for _, want := range []string{
		"codex-yolo() {",
		`command codex --dangerously-bypass-approvals-and-sandbox "$@"`,
		"claude-yolo() {",
		`command claude --dangerously-skip-permissions "$@"`,
		"kiro-yolo() {",
		`command kiro-cli chat --trust-all-tools "$@"`,
		"copilot-yolo() {",
		`command copilot --allow-all "$@"`,
		"kimi-yolo() {",
		`command kimi --auto "$@"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("Render shell/30-ai.sh.tmpl: expected %q in output, got:\n%s", want, content)
		}
	}
}

func TestRender_WorkspaceCloudVars(t *testing.T) {
	e := NewEngine()

	out, err := e.Render("shell/40-workspace.sh.tmpl", map[string]any{
		"EnableWorkspace": true,
		"WorkspacePath":   "~/workspace",
		"VaultPath":       "~/workspace/work/vault",
		"CloudSymlink":    "~/Dropbox",
	})
	if err != nil {
		t.Fatalf("Render shell/40-workspace.sh.tmpl: %v", err)
	}

	content := string(out)
	for _, want := range []string{
		`export VAULT="$HOME/workspace/work/vault"`,
		`export MARU_SCRATCHPAD="$WORK/scratchpad"`,
		`export MARU_TEMP="$MARU_SCRATCHPAD/temp"`,
		`export CLAUDE_CODE_TMPDIR="$MARU_TEMP/runtime/claude"`,
		`export CLOUD_WORKSPACE="$HOME/Dropbox"`,
		`export CLOUD_WORK="$CLOUD_WORKSPACE/work"`,
		`alias cwork=`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("Render 40-workspace: expected %q in output, got:\n%s", want, content)
		}
	}
}

func TestRender_WorkspaceScratchpadVarsWithoutCloud(t *testing.T) {
	e := NewEngine()

	out, err := e.Render("shell/40-workspace.sh.tmpl", map[string]any{
		"EnableWorkspace": true,
		"WorkspacePath":   "~/workspace",
		"VaultPath":       "~/workspace/work/vault",
	})
	if err != nil {
		t.Fatalf("Render shell/40-workspace.sh.tmpl: %v", err)
	}

	content := string(out)
	// scratchpad contract must not depend on cloud sync being configured
	for _, want := range []string{
		`export MARU_SCRATCHPAD="$WORK/scratchpad"`,
		`export MARU_TEMP="$MARU_SCRATCHPAD/temp"`,
		`export CLAUDE_CODE_TMPDIR="$MARU_TEMP/runtime/claude"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("Render 40-workspace (no cloud): expected %q in output, got:\n%s", want, content)
		}
	}
	if strings.Contains(content, "CLOUD_WORKSPACE") {
		t.Errorf("Render 40-workspace (no cloud): unexpected CLOUD_WORKSPACE in output:\n%s", content)
	}
}

func TestRender_WithTemplateData(t *testing.T) {
	e := NewEngine()

	// git/config.tmpl uses Name, Email, GithubUser etc.
	data := map[string]any{
		"Name":       "Test User",
		"Email":      "test@example.com",
		"GithubUser": "testuser",
		"GitSigning": false,
		"SSHKeyName": "id_ed25519",
	}

	out, err := e.Render("git/config.tmpl", data)
	if err != nil {
		t.Fatalf("Render git/config.tmpl: %v", err)
	}

	content := string(out)
	if !strings.Contains(content, "Test User") {
		t.Errorf("Render git/config.tmpl: expected Name in output, got:\n%s", content)
	}
	if !strings.Contains(content, "test@example.com") {
		t.Errorf("Render git/config.tmpl: expected Email in output, got:\n%s", content)
	}
}

func TestRender_ExportsPutContainerShimAheadOfBrew(t *testing.T) {
	e := NewEngine()
	const shimLine = `export PATH="$HOME/.local/share/dotfiles/shims:$PATH"`
	for _, isDarwin := range []bool{true, false} {
		brewLine := `export PATH="/opt/homebrew/bin:$PATH"`
		if !isDarwin {
			brewLine = `export PATH="/home/linuxbrew/.linuxbrew/bin:$PATH"`
		}
		out, err := e.Render("shell/00-exports.sh.tmpl", map[string]any{"IsDarwin": isDarwin, "EnableContainer": true})
		if err != nil {
			t.Fatal(err)
		}
		content := string(out)
		brew, shim := strings.Index(content, brewLine), strings.Index(content, shimLine)
		// Each line prepends, so the later line wins on PATH.
		if brew < 0 || shim < brew {
			t.Errorf("darwin=%v: shim line at %d must follow the brew line at %d", isDarwin, shim, brew)
		}

		out, err = e.Render("shell/00-exports.sh.tmpl", map[string]any{"IsDarwin": isDarwin})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "dotfiles/shims") {
			t.Errorf("darwin=%v: shim line rendered with the module disabled", isDarwin)
		}
	}
}
