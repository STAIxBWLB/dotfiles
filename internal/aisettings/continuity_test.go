package aisettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	dottemplate "github.com/STAIxBWLB/dotfiles/internal/template"
)

func TestContinuityPreservesCustomContentAndBacksUp(t *testing.T) {
	m, home := testAgentsManager(t)
	original := "# Custom\n\n사용자 정책 보존\n"
	if err := os.MkdirAll(filepath.Dir(m.SSOTPath()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.SSOTPath(), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := m.EnsureContinuityPolicy()
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	data, err := os.ReadFile(m.SSOTPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), original) || !strings.Contains(string(data), continuityPolicy) {
		t.Fatal("custom content or policy lost")
	}
	backups, err := filepath.Glob(filepath.Join(home, ".local/share/dotfiles/backup/agents-ssot/*/AGENTS.md"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	saved, err := os.ReadFile(backups[0])
	if err != nil || string(saved) != original {
		t.Fatal("original backup missing")
	}
	changed, err = m.EnsureContinuityPolicy()
	if err != nil || changed {
		t.Fatalf("repeat changed=%t err=%v", changed, err)
	}
	if _, err = os.Stat(filepath.Join(home, ".claude/CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("helper must not render targets")
	}
}
func TestContinuityDryRunAndFreshTemplate(t *testing.T) {
	m, _ := testAgentsManager(t)
	m.Runner.DryRun = true
	changed, err := m.EnsureContinuityPolicy()
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	if _, err = os.Stat(m.SSOTPath()); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote SSOT")
	}
	m.Runner.DryRun = false
	changed, err = m.EnsureContinuityPolicy()
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	data, _ := os.ReadFile(m.SSOTPath())
	if strings.Count(string(data), continuityStart) != 1 {
		t.Fatal("duplicate/missing policy")
	}
	tmpl, err := dottemplate.NewEngine().ReadStatic("agents/AGENTS.md.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tmpl), continuityPolicy) {
		t.Fatal("fresh template and upgrade policy differ")
	}
}
func TestContinuityRejectsMalformedMarkers(t *testing.T) {
	for _, s := range []string{continuityStart, continuityEnd, continuityEnd + continuityStart, continuityPolicy + continuityPolicy} {
		if _, err := patchContinuityPolicy(s); err == nil {
			t.Fatalf("accepted malformed policy %q", s)
		}
	}
	original := "user before\n" + continuityStart + "\nold policy\n" + continuityEnd + "\nuser after\n"
	got, err := patchContinuityPolicy(original)
	if err != nil || got != "user before\n"+continuityPolicy+"\nuser after\n" {
		t.Fatalf("patch=%q err=%v", got, err)
	}
}

func TestContinuityNeededIsReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, content         string
		exists, want, wantErr bool
	}{
		{name: "missing", want: true},
		{name: "custom", content: "# Custom\n", exists: true, want: true},
		{name: "current", content: continuityPolicy + "\n", exists: true},
		{name: "malformed", content: continuityStart, exists: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, home := testAgentsManager(t)
			if tc.exists {
				if err := os.MkdirAll(filepath.Dir(m.SSOTPath()), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(m.SSOTPath(), []byte(tc.content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			needed, err := m.ContinuityPolicyNeeded()
			if needed != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("needed=%t err=%v", needed, err)
			}
			if tc.exists {
				got, _ := os.ReadFile(m.SSOTPath())
				if string(got) != tc.content {
					t.Fatal("read mutated SSOT")
				}
			} else if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
				t.Fatal("read created config")
			}
			if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
				t.Fatal("read created backups or state")
			}
		})
	}
}

func TestContinuityBackupsAreDistinct(t *testing.T) {
	m, home := testAgentsManager(t)
	if err := os.MkdirAll(filepath.Dir(m.SSOTPath()), 0755); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first user state\n", "second user state\n"} {
		if err := os.WriteFile(m.SSOTPath(), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := m.EnsureContinuityPolicy(); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := filepath.Glob(filepath.Join(home, ".local/share/dotfiles/backup/agents-ssot/*/AGENTS.md"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("backups=%v err=%v", paths, err)
	}
	got := map[string]bool{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got[string(b)] = true
	}
	if !got["first user state\n"] || !got["second user state\n"] {
		t.Fatalf("backup overwritten: %v", got)
	}
}
