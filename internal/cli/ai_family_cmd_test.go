package cli

import (
	"strings"
	"testing"
)

func TestAIFamilyCommands(t *testing.T) {
	home := t.TempDir()
	out, _, err := runDotForTest("--home", home, "ai", "family", "install", "acon")
	if err != nil || !strings.Contains(out, "acon: skipped") || !strings.Contains(out, "no published npm package") {
		t.Fatalf("install acon = %q, %v", out, err)
	}
	out, _, err = runDotForTest("--home", home, "ai", "family", "status")
	if err != nil || !strings.Contains(out, "ahub: not-installed") || !strings.Contains(out, "vmem: not-installed") || !strings.Contains(out, "ares: unpublished") {
		t.Fatalf("status = %q, %v", out, err)
	}
	if _, _, err = runDotForTest("--home", home, "ai", "rollback", "amem"); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("rollback = %v", err)
	}
	if _, _, err = runDotForTest("--home", home, "ai", "family", "install", "amem", "--version", "latest"); err == nil || !strings.Contains(err.Error(), "stable X.Y.Z") {
		t.Fatalf("bad version = %v", err)
	}
}
