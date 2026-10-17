package aisettings

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func testAgentsManager(t *testing.T) (*AgentsManager, string) {
	t.Helper()
	home := t.TempDir()
	return NewAgentsManager(exec.NewRunner(false, slog.Default()), home), home
}

const literalNonASCIIInstruction = "Emit non-ASCII text as the literal characters themselves, in every tool-call parameter,\n" +
	"file write, and shell command. Never hand-encode a codepoint (`\\uXXXX`, `\\xNN`, octal,\n" +
	"HTML entity, percent-encoding), and never repair a non-ASCII typo by codepoint\n" +
	"arithmetic; edit the literal characters instead."

func TestAgentsInitDefaultIncludesLiteralNonASCIIInstruction(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	foundAuthoringSection := false
	for _, section := range defaultAgentSections {
		if section == "Text Encoding" {
			foundAuthoringSection = true
			break
		}
	}
	if !foundAuthoringSection {
		t.Fatal("Text Encoding missing from interactive authoring sections")
	}

	if _, err := mgr.Init(InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	ssot, err := os.ReadFile(mgr.SSOTPath())
	if err != nil {
		t.Fatalf("read initialized SSOT: %v", err)
	}
	textEncoding := sectionBody(t, string(ssot), "Text Encoding", "Operating Principles")
	if textEncoding != literalNonASCIIInstruction {
		t.Fatalf("Text Encoding body = %q, want exact instruction %q", textEncoding, literalNonASCIIInstruction)
	}
	if strings.Contains(textEncoding, "Exception:") || strings.Contains(textEncoding, "Why:") || strings.Contains(textEncoding, "Keep in sync") {
		t.Fatalf("Text Encoding contains removed qualification: %q", textEncoding)
	}

	for _, toolID := range []string{"claude", "codex"} {
		rendered, err := mgr.Render(toolID)
		if err != nil {
			t.Fatalf("Render(%s): %v", toolID, err)
		}
		got := sectionBody(t, rendered, "Text Encoding", "Operating Principles")
		if got != literalNonASCIIInstruction {
			t.Fatalf("%s rendered Text Encoding = %q, want exact instruction %q", toolID, got, literalNonASCIIInstruction)
		}
	}
}

func sectionBody(t *testing.T, text, heading, nextHeading string) string {
	t.Helper()
	start := "## " + heading + "\n\n"
	end := "\n\n## " + nextHeading
	startIndex := strings.Index(text, start)
	if startIndex < 0 {
		t.Fatalf("missing %q heading", heading)
	}
	afterStart := text[startIndex+len(start):]
	body, _, found := strings.Cut(afterStart, end)
	if !found {
		t.Fatalf("missing next %q heading", nextHeading)
	}
	return body
}

func TestAgentsApplyCopiesSSOTToTargets(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared instructions\n"))

	res, err := mgr.Apply(ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Items) != len(RegisteredAgentTools()) {
		t.Fatalf("applied items = %d, want %d", len(res.Items), len(RegisteredAgentTools()))
	}
	for _, tool := range RegisteredAgentTools() {
		target, err := mgr.TargetPath(tool.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read %s: %v", target, err)
		}
		if !strings.HasPrefix(string(got), agentsManagedHeader+"\n\n") {
			t.Fatalf("%s target missing managed header: %q", tool.ID, got)
		}
		if !strings.Contains(string(got), "shared instructions\n") {
			t.Fatalf("%s target = %q", tool.ID, got)
		}
	}
}

func TestAgentsApplyAppliesOverlay(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))
	mustWrite(t, filepath.Join(mgr.SSOTDirPath(), "overlays", "claude.md"), []byte("claude only\n"))

	if _, err := mgr.Apply(ApplyOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	claudeTarget, _ := mgr.TargetPath("claude")
	claude, err := os.ReadFile(claudeTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claude), "<!-- overlay:claude -->\nclaude only\n") {
		t.Fatalf("claude overlay missing: %q", claude)
	}
	codexTarget, _ := mgr.TargetPath("codex")
	codex, err := os.ReadFile(codexTarget)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(codex), "claude only") {
		t.Fatalf("codex unexpectedly received claude overlay: %q", codex)
	}
}

func TestAgentsApplyBlocksHandEditedTargetByDefault(t *testing.T) {
	mgr, home := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("ssot\n"))
	target, _ := mgr.TargetPath("codex")
	mustWrite(t, target, []byte("hand edit\n"))

	res, err := mgr.Apply(ApplyOptions{Tools: []string{"codex"}})
	if err == nil {
		t.Fatal("apply should fail on protected write conflict")
	}
	var conflict *ProtectedWriteConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %T %v, want ProtectedWriteConflictError", err, err)
	}
	if conflict.ToolID != "codex" {
		t.Fatalf("conflict tool = %q, want codex", conflict.ToolID)
	}
	if len(res.Items) != 1 || !res.Items[0].Conflict {
		t.Fatalf("result should include conflict item, got %+v", res)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hand edit\n" {
		t.Fatalf("target was overwritten despite conflict: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "dotfiles", "backup", "agents")); !os.IsNotExist(err) {
		t.Fatalf("conflict should not create agents backup dir, stat err=%v", err)
	}
}

func TestAgentsApplyForceBacksUpHandEditedTarget(t *testing.T) {
	mgr, home := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("ssot\n"))
	target, _ := mgr.TargetPath("codex")
	mustWrite(t, target, []byte("hand edit\n"))

	res, err := mgr.Apply(ApplyOptions{Tools: []string{"codex"}, Force: true})
	if err != nil {
		t.Fatalf("apply force: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(res.Warnings))
	}
	if !res.Items[0].BackedUp {
		t.Fatal("expected hand-edited target to be backed up")
	}
	backup, err := os.ReadFile(res.Items[0].BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != "hand edit\n" {
		t.Fatalf("backup = %q", backup)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), agentsManagedHeader+"\n\n") || !strings.Contains(string(got), "ssot\n") {
		t.Fatalf("target = %q", got)
	}
	if !strings.HasPrefix(res.Items[0].BackupPath, filepath.Join(home, ".local", "share", "dotfiles", "backup", "agents")) {
		t.Fatalf("backup path outside agents backup root: %s", res.Items[0].BackupPath)
	}
}

func TestAgentsApplyConflictPreflightPreventsPartialWrites(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("first\n"))
	if _, err := mgr.Apply(ApplyOptions{Tools: []string{"claude", "codex"}}); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	claudeTarget, _ := mgr.TargetPath("claude")
	codexTarget, _ := mgr.TargetPath("codex")
	initialClaude, err := os.ReadFile(claudeTarget)
	if err != nil {
		t.Fatal(err)
	}

	mustWrite(t, mgr.SSOTPath(), []byte("second\n"))
	mustWrite(t, codexTarget, []byte("hand edit\n"))

	res, err := mgr.Apply(ApplyOptions{Tools: []string{"claude", "codex"}})
	if err == nil {
		t.Fatal("apply should fail on protected write conflict")
	}
	var conflict *ProtectedWriteConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %T %v, want ProtectedWriteConflictError", err, err)
	}
	if conflict.ToolID != "codex" {
		t.Fatalf("conflict tool = %q, want codex", conflict.ToolID)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2 planned items", len(res.Items))
	}
	if !res.Items[0].Changed || res.Items[0].Conflict {
		t.Fatalf("claude item = %+v, want changed non-conflict", res.Items[0])
	}
	if !res.Items[1].Conflict {
		t.Fatalf("codex item = %+v, want conflict", res.Items[1])
	}
	gotClaude, err := os.ReadFile(claudeTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotClaude) != string(initialClaude) {
		t.Fatalf("pre-conflict target was partially written:\n%s", gotClaude)
	}
	state, err := mgr.readState()
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := mgr.Render("claude")
	if err != nil {
		t.Fatal(err)
	}
	if state.LastApplied["claude"] == normalizedHash([]byte(rendered)) {
		t.Fatal("last-applied state advanced despite conflict")
	}
}

func TestAgentsApplyDryRunDoesNotWriteAndSetsFlags(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared instructions\n"))

	targetPath, err := mgr.TargetPath("codex")
	if err != nil {
		t.Fatalf("TargetPath: %v", err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		t.Fatalf("target %s unexpectedly exists before dry-run apply", targetPath)
	}

	res, err := mgr.Apply(ApplyOptions{
		DryRun: true,
		Tools:  []string{"codex"},
	})
	if err != nil {
		t.Fatalf("Apply dry-run: %v", err)
	}
	if !res.DryRun {
		t.Fatalf("res.DryRun = %v, want true", res.DryRun)
	}
	if len(res.Items) != 1 {
		t.Fatalf("len(res.Items) = %d, want 1", len(res.Items))
	}
	item := res.Items[0]
	if !item.Changed {
		t.Fatalf("item.Changed = %v, want true for pending write in dry-run", item.Changed)
	}
	if item.BackedUp {
		t.Fatalf("item.BackedUp = %v, want false in dry-run", item.BackedUp)
	}
	if _, err := os.Stat(targetPath); err == nil {
		t.Fatalf("target %s exists after dry-run apply, expected no write", targetPath)
	}
}

func TestAgentsApplyDryRunRunnerSetsDryRunAndDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	mgr := NewAgentsManager(exec.NewRunner(true, slog.Default()), home)
	mustWrite(t, mgr.SSOTPath(), []byte("shared instructions\n"))

	targetPath, err := mgr.TargetPath("codex")
	if err != nil {
		t.Fatalf("TargetPath: %v", err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		t.Fatalf("target %s unexpectedly exists before dry-run apply with dry-run runner", targetPath)
	}

	res, err := mgr.Apply(ApplyOptions{Tools: []string{"codex"}})
	if err != nil {
		t.Fatalf("Apply dry-run with dry-run runner: %v", err)
	}
	if !res.DryRun {
		t.Fatalf("res.DryRun = %v, want true", res.DryRun)
	}
	if len(res.Items) != 1 {
		t.Fatalf("len(res.Items) = %d, want 1", len(res.Items))
	}
	item := res.Items[0]
	if !item.Changed {
		t.Fatalf("item.Changed = %v, want true for pending write in dry-run", item.Changed)
	}
	if item.BackedUp {
		t.Fatalf("item.BackedUp = %v, want false in dry-run", item.BackedUp)
	}
	if _, err := os.Stat(targetPath); err == nil {
		t.Fatalf("target %s exists after dry-run apply with dry-run runner, expected no write", targetPath)
	}
}

func TestAgentsApplyDryRunDoesNotCreateBackup(t *testing.T) {
	mgr, home := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("ssot\n"))
	targetPath, err := mgr.TargetPath("codex")
	if err != nil {
		t.Fatalf("TargetPath: %v", err)
	}
	mustWrite(t, targetPath, []byte("hand edit\n"))

	res, err := mgr.Apply(ApplyOptions{DryRun: true, Tools: []string{"codex"}})
	if err != nil {
		t.Fatalf("Apply dry-run: %v", err)
	}
	if !res.DryRun {
		t.Fatalf("res.DryRun = %v, want true", res.DryRun)
	}
	if len(res.Items) != 1 {
		t.Fatalf("len(res.Items) = %d, want 1", len(res.Items))
	}
	if !res.Items[0].Changed {
		t.Fatal("expected dry-run to report pending change")
	}
	if res.Items[0].BackedUp {
		t.Fatal("dry-run must not mark the target as backed up")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "dotfiles", "backup", "agents")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create agents backup dir, stat err=%v", err)
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hand edit\n" {
		t.Fatalf("dry-run modified target = %q", got)
	}
}

func TestAgentsPullSeedsSSOT(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	target, _ := mgr.TargetPath("claude")
	mustWrite(t, target, []byte("# Identity\nTest User\n"))

	res, err := mgr.Pull(PullOptions{FromTool: "claude"})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !res.Changed {
		t.Fatal("expected pull to write SSOT")
	}
	got, err := os.ReadFile(mgr.SSOTPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Identity\nTest User\n" {
		t.Fatalf("SSOT = %q", got)
	}
}

func TestAgentsAuthorNonInteractiveSection(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("preface\n\n## Identity\nKeep me\n\n## How I Work\nold\n\n## Custom\nTail\n"))

	res, err := mgr.Author(AuthorOptions{
		NonInteractive: true,
		Section:        "How I Work",
		Value:          "- terse",
	})
	if err != nil {
		t.Fatalf("author: %v", err)
	}
	if !res.Changed {
		t.Fatal("expected author to change the section")
	}
	gotBytes, err := os.ReadFile(mgr.SSOTPath())
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)
	if !strings.Contains(got, "## Identity\nKeep me") {
		t.Fatalf("identity section was not preserved: %q", got)
	}
	if !strings.Contains(got, "## How I Work\n- terse") {
		t.Fatalf("section was not updated: %q", got)
	}
	if !strings.Contains(got, "## Custom\nTail") {
		t.Fatalf("custom tail was not preserved: %q", got)
	}
}

func TestDeleteMarkdownSectionRemovesHeadingAndBody(t *testing.T) {
	doc := "preface\n\n## Identity\nKeep me\n\n## How I Work\nold\n\n## Custom\nTail\n"
	got := deleteMarkdownSection(doc, "How I Work")
	if strings.Contains(got, "## How I Work") || strings.Contains(got, "old") {
		t.Fatalf("section was not removed: %q", got)
	}
	if !strings.Contains(got, "## Identity\nKeep me") {
		t.Fatalf("identity section was not preserved: %q", got)
	}
	if !strings.Contains(got, "## Custom\nTail") {
		t.Fatalf("custom section was not preserved: %q", got)
	}
}

func TestAgentsRenderIncludesOverlayMarker(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))
	mustWrite(t, filepath.Join(mgr.SSOTDirPath(), "overlays", "claude.md"), []byte("claude only\n"))

	rendered, err := mgr.Render("claude")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "<!-- overlay:claude -->") {
		t.Fatalf("overlay marker missing: %q", rendered)
	}
}

func TestAgentsRenderAntigravityFallsBackToGeminiOverlay(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))
	mustWrite(t, filepath.Join(mgr.SSOTDirPath(), "overlays", "gemini.md"), []byte("google cli only\n"))

	rendered, err := mgr.Render("antigravity")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "<!-- overlay:gemini -->\ngoogle cli only\n") {
		t.Fatalf("legacy gemini overlay missing: %q", rendered)
	}
}

func TestAgentsGeminiAliasDeduplicatesApply(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))

	res, err := mgr.Apply(ApplyOptions{Tools: []string{"antigravity", "gemini"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ToolID != "antigravity" {
		t.Fatalf("items = %+v, want one antigravity item", res.Items)
	}
	target, err := mgr.TargetPath("gemini")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "shared\n") {
		t.Fatalf("target = %q", got)
	}
	state, err := mgr.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastApplied["antigravity"] == "" {
		t.Fatalf("antigravity last-applied missing: %+v", state.LastApplied)
	}
	if state.LastApplied["gemini"] != "" {
		t.Fatalf("gemini alias should not keep separate state: %+v", state.LastApplied)
	}
}

func TestAgentsGeminiLastAppliedStateMigratesToAntigravity(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("first\n"))
	rendered, err := mgr.Render("gemini")
	if err != nil {
		t.Fatalf("render gemini alias: %v", err)
	}
	target, err := mgr.TargetPath("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, target, []byte(rendered))
	if err := mgr.writeState(&agentsState{LastApplied: map[string]string{
		"gemini": normalizedHash([]byte(rendered)),
	}}); err != nil {
		t.Fatal(err)
	}

	mustWrite(t, mgr.SSOTPath(), []byte("second\n"))
	res, err := mgr.Apply(ApplyOptions{Tools: []string{"gemini"}})
	if err != nil {
		t.Fatalf("apply with legacy gemini state: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Conflict {
		t.Fatalf("unexpected apply result: %+v", res.Items)
	}
	state, err := mgr.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastApplied["antigravity"] == "" || state.LastApplied["gemini"] != "" {
		t.Fatalf("state was not migrated to canonical id: %+v", state.LastApplied)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "second\n") {
		t.Fatalf("target was not updated: %q", got)
	}
}

// writeLegacyAgentsState writes the pre-move state file into the synced SSOT
// dir, as an older dot or a peer sync from another machine would.
func writeLegacyAgentsState(t *testing.T, mgr *AgentsManager, lastApplied map[string]string) string {
	t.Helper()
	path := filepath.Join(mgr.SSOTDirPath(), ".state.json")
	mustWriteJSON(t, path, agentsState{LastApplied: lastApplied})
	return path
}

// renderToTarget writes the current render of toolID to its target, as a
// previous apply on this machine would have, and returns its hash.
func renderToTarget(t *testing.T, mgr *AgentsManager, toolID string) string {
	t.Helper()
	rendered, err := mgr.Render(toolID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := mgr.TargetPath(toolID)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, target, []byte(rendered))
	return normalizedHash([]byte(rendered))
}

func TestAgentsApplyWritesStateOutsideSyncedSSOTDir(t *testing.T) {
	mgr, home := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))
	if _, err := mgr.Apply(ApplyOptions{Tools: []string{"codex"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := filepath.Join(home, ".local", "share", "dotfiles", "agents", "state.json")
	if got := mgr.StatePath(); got != want {
		t.Fatalf("StatePath = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("state not written to machine-local path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mgr.SSOTDirPath(), ".state.json")); !os.IsNotExist(err) {
		t.Fatalf("state leaked into the synced SSOT dir, stat err=%v", err)
	}
}

func TestAgentsLegacyStateKeepsOnlyEntriesMatchingLiveTargets(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("first\n"))
	claudeHash := renderToTarget(t, mgr, "claude")
	renderToTarget(t, mgr, "codex")
	legacy := writeLegacyAgentsState(t, mgr, map[string]string{
		"claude":       claudeHash,
		"codex":        normalizedHash([]byte("another machine's render\n")),
		"retired-tool": claudeHash,
	})

	state, err := mgr.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LastApplied) != 1 || state.LastApplied["claude"] != claudeHash {
		t.Fatalf("migrated state = %+v, want only the claude entry that matches its target", state.LastApplied)
	}

	// The SSOT changes on another machine and syncs here. The migrated claude
	// record still proves the target is this machine's last render.
	mustWrite(t, mgr.SSOTPath(), []byte("second\n"))
	if _, err := mgr.Apply(ApplyOptions{Tools: []string{"claude"}}); err != nil {
		t.Fatalf("apply with migrated record: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy state not removed after write, stat err=%v", err)
	}
	state, err = mgr.readState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.LastApplied["codex"]; ok {
		t.Fatalf("dropped foreign entry came back: %+v", state.LastApplied)
	}

	// The dropped codex entry is "no record": its stale target conflicts.
	_, err = mgr.Apply(ApplyOptions{Tools: []string{"codex"}})
	var conflict *ProtectedWriteConflictError
	if !errors.As(err, &conflict) || conflict.ExpectedHash != "" {
		t.Fatalf("err = %v, want a no-record conflict for codex", err)
	}
}

func TestAgentsLegacyForeignStateNeverConflictsWithCurrentRender(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("current\n"))
	want := map[string]string{
		"claude": renderToTarget(t, mgr, "claude"),
		"codex":  renderToTarget(t, mgr, "codex"),
	}
	foreign := map[string]string{
		"claude": normalizedHash([]byte("stale\n")),
		"codex":  normalizedHash([]byte("stale\n")),
	}
	legacy := writeLegacyAgentsState(t, mgr, foreign)

	// The second pass is a legacy file synced back from a machine still on
	// an older dot after this one migrated: ignored, then removed again.
	for pass := 1; pass <= 2; pass++ {
		res, err := mgr.Apply(ApplyOptions{Tools: []string{"claude", "codex"}})
		if err != nil {
			t.Fatalf("pass %d: apply: %v", pass, err)
		}
		for _, item := range res.Items {
			if item.Conflict || item.Changed {
				t.Fatalf("pass %d: item = %+v, want in-sync non-conflict", pass, item)
			}
		}
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("pass %d: legacy state not removed, stat err=%v", pass, err)
		}
		state, err := mgr.readState()
		if err != nil {
			t.Fatal(err)
		}
		for id, hash := range want {
			if state.LastApplied[id] != hash {
				t.Fatalf("pass %d: %s last-applied = %q, want %q", pass, id, state.LastApplied[id], hash)
			}
		}
		writeLegacyAgentsState(t, mgr, foreign)
	}
}

func TestAgentsApplySucceedsWhenLegacyStateCannotBeRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permission")
	}
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))
	legacy := writeLegacyAgentsState(t, mgr, map[string]string{"codex": "stale"})
	ssotDir := mgr.SSOTDirPath()
	if err := os.Chmod(ssotDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ssotDir, 0o755) })

	res, err := mgr.Apply(ApplyOptions{Tools: []string{"codex"}})
	if err != nil {
		t.Fatalf("apply failed on an unremovable legacy state: %v", err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "legacy agents state not removed") {
		t.Fatalf("warnings = %q, want one legacy-removal warning", res.Warnings)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy state should remain in the read-only dir: %v", err)
	}
	state, err := mgr.readState()
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := mgr.Render("codex")
	if err != nil {
		t.Fatal(err)
	}
	if state.LastApplied["codex"] != normalizedHash([]byte(rendered)) {
		t.Fatalf("state not written: %+v", state.LastApplied)
	}
}

func TestAgentsBackupRestoreRoundTrip(t *testing.T) {
	eng, home, root := testEngine(t)
	mgr := NewAgentsManager(eng.Runner, home)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))
	if _, err := mgr.Apply(ApplyOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	newHome := t.TempDir()
	restorer := &Engine{
		Runner:   exec.NewRunner(false, slog.Default()),
		HomeDir:  newHome,
		Root:     root,
		Hostname: "testhost",
		User:     "tester",
	}
	if _, err := restorer.Restore(RestoreOptions{Version: snap.Version}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restoredMgr := NewAgentsManager(restorer.Runner, newHome)
	got, err := os.ReadFile(restoredMgr.SSOTPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "shared\n" {
		t.Fatalf("restored SSOT = %q", got)
	}
	res, err := restoredMgr.Apply(ApplyOptions{})
	if err != nil {
		t.Fatalf("post-restore apply: %v", err)
	}
	for _, item := range res.Items {
		if item.Changed {
			t.Fatalf("post-restore apply should be no-op, changed %+v", item)
		}
	}
}

func TestAgentsRegistryFiltersOptional(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	mustWrite(t, mgr.SSOTPath(), []byte("shared\n"))

	statuses, err := mgr.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	foundAntigravity := false
	for _, st := range statuses {
		if st.Tool.ID == "antigravity" {
			foundAntigravity = true
			if st.Drift != "target-missing" {
				t.Fatalf("antigravity drift = %q, want target-missing", st.Drift)
			}
		}
	}
	if !foundAntigravity {
		t.Fatal("optional antigravity tool missing from status")
	}
	if target, err := mgr.TargetPath("gemini"); err != nil || !strings.HasSuffix(target, filepath.Join(".gemini", "GEMINI.md")) {
		t.Fatalf("gemini alias target = %q, err=%v", target, err)
	}
}

func TestAgentsRegistryIncludesKiroAndKimi(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	kiro, ok := mgr.Tool("kiro")
	if !ok || kiro.Optional {
		t.Fatalf("kiro tool = %+v, ok=%v; want non-optional entry", kiro, ok)
	}
	if target, err := mgr.TargetPath("kiro"); err != nil || !strings.HasSuffix(target, filepath.Join(".kiro", "steering", "AGENTS.md")) {
		t.Fatalf("kiro target = %q, err=%v", target, err)
	}
	kimi, ok := mgr.Tool("kimi")
	if !ok || !kimi.Optional {
		t.Fatalf("kimi tool = %+v, ok=%v; want optional entry", kimi, ok)
	}
	if target, err := mgr.TargetPath("kimi"); err != nil || !strings.HasSuffix(target, filepath.Join(".kimi-code", "AGENTS.md")) {
		t.Fatalf("kimi target = %q, err=%v", target, err)
	}
}

func TestAgentsRegistryCopilotIsFirstClass(t *testing.T) {
	mgr, _ := testAgentsManager(t)
	copilot, ok := mgr.Tool("copilot")
	if !ok || copilot.Optional {
		t.Fatalf("copilot tool = %+v, ok=%v; want non-optional entry", copilot, ok)
	}
	// Copilot CLI 1.x reads ~/.copilot/copilot-instructions.md, not the old
	// ~/.config/github-copilot/AGENTS.md.
	if target, err := mgr.TargetPath("copilot"); err != nil || !strings.HasSuffix(target, filepath.Join(".copilot", "copilot-instructions.md")) {
		t.Fatalf("copilot target = %q, err=%v", target, err)
	}
}

func TestIsManagedAgentsFile(t *testing.T) {
	dir := t.TempDir()

	managed := filepath.Join(dir, "managed.md")
	if err := os.WriteFile(managed, []byte(agentsManagedHeader+"\n\n# body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsManagedAgentsFile(managed) {
		t.Error("managed file not recognized")
	}

	user := filepath.Join(dir, "user.md")
	if err := os.WriteFile(user, []byte("# my own instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsManagedAgentsFile(user) {
		t.Error("user-authored file misdetected as managed")
	}

	if IsManagedAgentsFile(filepath.Join(dir, "missing.md")) {
		t.Error("missing file misdetected as managed")
	}
}
