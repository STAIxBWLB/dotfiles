package syncer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

func nfdTestConfig(t *testing.T, root string) *Config {
	t.Helper()
	paths := ResolveLocalPathsForProfile(root, DefaultProfile)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	return &Config{
		LocalPath:    root,
		FilterMode:   FilterModeExclude,
		ExcludesFile: paths.ExcludeFile,
		IgnoreFile:   paths.IgnoreFile,
		LocalPaths:   paths,
	}
}

func writeNFDTestFile(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func hasRawNFDTestEntry(t *testing.T, dir, name string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

func TestPlanWorkspaceNameNormalization_DeepestFirstAndApply(t *testing.T) {
	root := t.TempDir()
	// U+00E9 is NFC; the decomposed spelling is its NFD target.
	nfcDir := "caf\u00e9"
	nfdDir := norm.NFD.String(nfcDir)
	nfcFile := "r\u00e9sum\u00e9.txt"
	nfdFile := norm.NFD.String(nfcFile)
	writeNFDTestFile(t, root, nfcDir+"/"+nfcFile, "payload")
	oldDir := filepath.Join(root, nfcDir)

	plan, err := PlanWorkspaceNameNormalization(nfdTestConfig(t, root))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Renames) != 2 {
		t.Fatalf("got %d renames, want file + directory: %#v", len(plan.Renames), plan.Renames)
	}
	if !strings.Contains(plan.Renames[0].OldRel, "/") {
		t.Fatalf("deepest rename first = %#v", plan.Renames)
	}
	if plan.Renames[0].NewRel != nfdDir+"/"+nfdFile {
		t.Errorf("child target = %q, want %q", plan.Renames[0].NewRel, nfdDir+"/"+nfdFile)
	}

	result, err := NormalizeWorkspaceNames(nfdTestConfig(t, root), false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Applied != 2 {
		t.Fatalf("applied = %d, want 2", result.Applied)
	}
	newFile := filepath.Join(root, nfdDir, nfdFile)
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("normalized file missing: %v", err)
	}
	if hasRawNFDTestEntry(t, filepath.Dir(oldDir), nfcDir) {
		t.Fatalf("old directory spelling still exists")
	}
	if hasRawNFDTestEntry(t, filepath.Join(root, nfdDir), nfdFile) == false {
		t.Fatalf("normalized file spelling missing")
	}
	if !NFDMigrationMarked(root) {
		t.Fatalf("migration marker missing at %s", NFDMigrationMarkerPath(root))
	}
	entries, err := os.ReadDir(filepath.Join(root, ".dotfiles"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), nfdTempPrefix) {
			t.Fatalf("temporary rename entry leaked: %s", entry.Name())
		}
	}
}

func TestNormalizeWorkspaceNames_DryRunAndMarkerGate(t *testing.T) {
	root := t.TempDir()
	nfc := "caf\u00e9.txt"
	old := writeNFDTestFile(t, root, nfc, "payload")
	cfg := nfdTestConfig(t, root)

	result, err := NormalizeWorkspaceNames(cfg, true)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !result.DryRun || len(result.Plan.Renames) != 1 {
		t.Fatalf("dry-run result = %#v", result)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("dry-run changed source: %v", err)
	}
	if NFDMigrationMarked(root) {
		t.Fatal("dry-run wrote migration marker")
	}

	// Before the explicit staged migration, a real push must fail closed rather
	// than silently sending NFC names or mutating them without operator review.
	second := writeNFDTestFile(t, root, "caf\u00e9-second.txt", "payload")
	if err := NormalizeWorkspaceNamesBeforePush(cfg); err == nil || !strings.Contains(err.Error(), "requiring NFD migration") {
		t.Fatalf("pre-push helper before marker error = %v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("pre-push helper ran before marker: %v", err)
	}

	if _, err := NormalizeWorkspaceNames(cfg, false); err != nil {
		t.Fatalf("explicit apply: %v", err)
	}
	writeNFDTestFile(t, root, "caf\u00e9-third.txt", "payload")
	if err := NormalizeWorkspaceNamesBeforePush(cfg); err != nil {
		t.Fatalf("pre-push helper after marker: %v", err)
	}
	if hasRawNFDTestEntry(t, root, "caf\u00e9-third.txt") {
		t.Fatalf("pre-push helper left NFC spelling")
	}
	if !hasRawNFDTestEntry(t, root, norm.NFD.String("caf\u00e9-third.txt")) {
		t.Fatalf("pre-push helper did not create NFD spelling")
	}
}

func TestPushAutomaticallyNormalizesNewNFCNamesAfterMigration(t *testing.T) {
	root := t.TempDir()
	mirror := t.TempDir()
	paths := ResolveLocalPaths(root)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Profile:      DefaultProfile,
		LocalPath:    root + "/",
		MirrorPath:   mirror + "/",
		Target:       Target{Kind: TargetLocal, Path: mirror + "/"},
		FilterMode:   FilterModeExclude,
		ConfigDir:    paths.StoreDir,
		ExcludesFile: paths.ExcludeFile,
		IgnoreFile:   paths.IgnoreFile,
		AllowFile:    paths.AllowFile,
		LogFile:      filepath.Join(t.TempDir(), "sync.log"),
		Propagation:  DefaultPropagationPolicy(),
		LocalPaths:   paths,
	}
	if err := MarkNFDMigration(root); err != nil {
		t.Fatal(err)
	}
	nfc := "download-caf\u00e9.txt"
	writeNFDTestFile(t, root, nfc, "payload")

	bin := t.TempDir()
	fakeRsync := filepath.Join(bin, "rsync")
	if err := os.WriteFile(fakeRsync, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := dotexec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := Push(context.Background(), runner, cfg, false); err != nil {
		t.Fatal(err)
	}
	if hasRawNFDTestEntry(t, root, nfc) {
		t.Fatal("push left newly downloaded NFC spelling in workspace")
	}
	if !hasRawNFDTestEntry(t, root, norm.NFD.String(nfc)) {
		t.Fatal("push did not rename newly downloaded name to NFD")
	}
	second := "later-r\u00e9sum\u00e9.txt"
	writeNFDTestFile(t, root, second, "payload")
	if err := Push(context.Background(), runner, cfg, false); err != nil {
		t.Fatal(err)
	}
	if hasRawNFDTestEntry(t, root, second) || !hasRawNFDTestEntry(t, root, norm.NFD.String(second)) {
		t.Fatal("a reused push config skipped normalization for a later NFC download")
	}
}

func TestPlanWorkspaceNameNormalization_PreflightCollisionIsAtomic(t *testing.T) {
	root := t.TempDir()
	nfc := "caf\u00e9.txt"
	nfd := norm.NFD.String(nfc)
	first := writeNFDTestFile(t, root, nfc, "nfc")
	second := writeNFDTestFile(t, root, nfd, "nfd")
	if !hasRawNFDTestEntry(t, root, nfc) || !hasRawNFDTestEntry(t, root, nfd) {
		t.Skip("filesystem normalizes sibling names; cannot represent a collision fixture")
	}

	_, err := PlanWorkspaceNameNormalization(nfdTestConfig(t, root))
	if err == nil {
		t.Fatal("collision plan unexpectedly succeeded")
	}
	var preflight *NameNormalizationPreflightError
	if !asNameNormalizationPreflightError(err, &preflight) {
		t.Fatalf("error = %T %v, want preflight error", err, err)
	}
	if len(preflight.Collisions) != 1 {
		t.Fatalf("collisions = %#v, want one", preflight.Collisions)
	}
	for _, path := range []string{first, second} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("preflight changed %s: %v", path, statErr)
		}
	}
}

func TestPlanWorkspaceNameNormalization_InvalidUTF8AndExcludedTrees(t *testing.T) {
	root := t.TempDir()
	if !utf8.ValidString(string([]byte{0xff})) {
		invalid := filepath.Join(root, string([]byte{0xff}))
		if err := os.WriteFile(invalid, []byte("bad"), 0o644); err != nil {
			t.Skipf("filesystem rejects invalid UTF-8 names: %v", err)
		}
	}
	nfc := "caf\u00e9.txt"
	writeNFDTestFile(t, root, ".dotfiles/"+nfc, "protected")
	writeNFDTestFile(t, root, ".sync-conflicts/2026/"+nfc, "protected")
	writeNFDTestFile(t, root, "node_modules/"+nfc, "protected")
	if err := os.Symlink(writeNFDTestFile(t, root, "outside/"+nfc, "target"), filepath.Join(root, "link-caf\u00e9.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := PlanWorkspaceNameNormalization(nfdTestConfig(t, root))
	if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	// Remove the invalid fixture and verify all excluded entries remain in
	// their original spelling while the symlink is not renamed/followed.
	if err := os.Remove(filepath.Join(root, string([]byte{0xff}))); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanWorkspaceNameNormalization(nfdTestConfig(t, root))
	if err != nil {
		t.Fatalf("excluded plan: %v", err)
	}
	for _, rename := range plan.Renames {
		if strings.HasPrefix(rename.OldRel, ".dotfiles/") ||
			strings.HasPrefix(rename.OldRel, ".sync-conflicts/") ||
			strings.HasPrefix(rename.OldRel, "node_modules/") ||
			strings.HasPrefix(rename.OldRel, "link-") {
			t.Errorf("excluded/symlink rename planned: %#v", rename)
		}
	}
}

func TestApplyNameNormalizationPlan_RollsBackCompletedMoves(t *testing.T) {
	root := t.TempDir()
	firstOld := writeNFDTestFile(t, root, "first-caf\u00e9.txt", "first")
	firstNew := filepath.Join(root, norm.NFD.String("first-caf\u00e9.txt"))
	secondOld := writeNFDTestFile(t, root, "second-caf\u00e9.txt", "second")
	secondNew := filepath.Join(root, "occupied-target")
	if err := os.WriteFile(secondNew, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasRawNFDTestEntry(t, root, filepath.Base(firstOld)) ||
		!hasRawNFDTestEntry(t, root, filepath.Base(secondOld)) {
		t.Skip("filesystem normalizes sibling names; cannot represent rollback fixture")
	}

	plan := &NameNormalizationPlan{WorkspaceRoot: root, Renames: []NameRename{
		{OldPath: firstOld, NewPath: firstNew, OldRel: filepath.Base(firstOld), NewRel: filepath.Base(firstNew)},
		{OldPath: secondOld, NewPath: secondNew, OldRel: filepath.Base(secondOld), NewRel: filepath.Base(secondNew)},
	}}
	if err := applyNameNormalizationPlan(plan); err == nil {
		t.Fatal("plan with occupied second destination unexpectedly succeeded")
	}
	if !hasRawNFDTestEntry(t, root, filepath.Base(firstOld)) {
		t.Fatal("completed first move was not rolled back")
	}
	if hasRawNFDTestEntry(t, root, filepath.Base(firstNew)) {
		t.Fatal("rolled-back first destination remains")
	}
}

// Keep the test independent of errors.As so the package's supported Go
// versions can use the same helper style as the rest of the syncer tests.
func asNameNormalizationPreflightError(err error, target **NameNormalizationPreflightError) bool {
	preflight, ok := err.(*NameNormalizationPreflightError)
	if !ok {
		return false
	}
	*target = preflight
	return true
}
