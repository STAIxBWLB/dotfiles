package module

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func gitRunContext(t *testing.T) *RunContext {
	t.Helper()
	rc, _, _ := twoHomeRunContext(t, &config.Config{})
	return rc
}

func seedLegacyIgnore(t *testing.T, rc *RunContext, content string) {
	t.Helper()
	path := legacyIgnorePath(rc)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitModule_DeploysGitignoreGlobal(t *testing.T) {
	rc := gitRunContext(t)
	m := &GitModule{}

	var found bool
	for _, f := range m.files(rc) {
		if f.templatePath == "git/gitignore.global" {
			found = true
			if f.destPath != filepath.Join(rc.HomeDir, ".config", "git", "gitignore.global") {
				t.Errorf("gitignore.global deploys to %q", f.destPath)
			}
		}
		if f.templatePath == "git/ignore" {
			t.Error("the legacy git/ignore template must no longer be deployed (#142)")
		}
	}
	if !found {
		t.Error("git/gitignore.global is not in the module file list")
	}
}

func TestGitModule_ConfigTemplatePointsAtGitignoreGlobal(t *testing.T) {
	rc := gitRunContext(t)

	out, err := rc.Template.Render("git/config.tmpl", rc.Config.TemplateData(rc.HomeDir))
	if err != nil {
		t.Fatalf("rendering git/config.tmpl: %v", err)
	}
	if !strings.Contains(string(out), "excludesFile = ~/.config/git/gitignore.global") {
		t.Errorf("config.tmpl does not point core.excludesFile at gitignore.global:\n%s", out)
	}
}

func TestGitModule_CheckReportsLegacyIgnoreRemoval(t *testing.T) {
	rc := gitRunContext(t)
	seedLegacyIgnore(t, rc, legacyGitIgnore)
	m := &GitModule{}

	check, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	var reported bool
	for _, c := range check.Changes {
		if strings.Contains(c.Description, "remove legacy") && strings.Contains(c.Description, ".config/git/ignore") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("Check does not report the legacy ignore removal: %+v", check.Changes)
	}
}

func TestGitModule_ApplyRemovesByteIdenticalLegacyIgnore(t *testing.T) {
	rc := gitRunContext(t)
	seedLegacyIgnore(t, rc, legacyGitIgnore)
	m := &GitModule{}

	result, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := os.Stat(legacyIgnorePath(rc)); !os.IsNotExist(err) {
		t.Errorf("legacy ignore still present after Apply: %v", err)
	}
	var messaged bool
	for _, msg := range result.Messages {
		if strings.Contains(msg, "removed legacy") {
			messaged = true
		}
	}
	if !messaged {
		t.Errorf("Apply does not mention the legacy removal: %+v", result.Messages)
	}
	if _, err := os.Stat(filepath.Join(rc.HomeDir, ".config", "git", "gitignore.global")); err != nil {
		t.Errorf("gitignore.global not deployed: %v", err)
	}
}

func TestGitModule_ApplyKeepsLocallyEditedLegacyIgnore(t *testing.T) {
	rc := gitRunContext(t)
	seedLegacyIgnore(t, rc, legacyGitIgnore+"*.local-only\n")
	var out bytes.Buffer
	rc.Out = &out
	m := &GitModule{}

	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	body, err := os.ReadFile(legacyIgnorePath(rc))
	if err != nil {
		t.Fatalf("a locally edited legacy ignore must survive Apply: %v", err)
	}
	if !strings.Contains(string(body), "*.local-only") {
		t.Errorf("legacy ignore content changed by Apply:\n%s", body)
	}
	// The excludesFile pointer moved, so the run must name every custom
	// pattern that just stopped applying (codex P1 on #143).
	if !strings.Contains(out.String(), "orphaned pattern: *.local-only") {
		t.Errorf("Apply does not list the orphaned custom pattern:\n%s", out.String())
	}
}

func TestGitModule_ApplyWithoutLegacyIgnore(t *testing.T) {
	rc := gitRunContext(t)
	m := &GitModule{}

	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(legacyIgnorePath(rc)); !os.IsNotExist(err) {
		t.Errorf("Apply created a legacy ignore: %v", err)
	}
}
