package cli

import (
	"strings"
	"testing"
)

func TestAIFamilyCommands(t *testing.T) {
	home := t.TempDir()
	if _, _, err := runDotForTest("--home", home, "ai", "family", "install"); err == nil || !strings.Contains(err.Error(), "or pass --all") {
		t.Fatalf("install without ids = %v", err)
	}
	if _, _, err := runDotForTest("--home", home, "ai", "family", "install", "--all", "amem"); err == nil || !strings.Contains(err.Error(), "or pass --all") {
		t.Fatalf("install with ids and --all = %v", err)
	}
	if _, _, err := runDotForTest("--home", home, "ai", "family", "install", "amem", "vmem", "--version", "1.0.0"); err == nil || !strings.Contains(err.Error(), "exactly one id") {
		t.Fatalf("--version with two ids = %v", err)
	}
	out, _, err := runDotForTest("--home", home, "ai", "family", "update")
	if err != nil || !strings.Contains(out, "no family tool is installed") {
		t.Fatalf("update with nothing installed = %q, %v", out, err)
	}
	// A named tool that is not installed is skipped; an unknown one fails without stopping the rest.
	out, _, err = runDotForTest("--home", home, "ai", "family", "update", "nope", "ares")
	if err == nil || !strings.Contains(err.Error(), "nope: unknown family tool") || !strings.Contains(out, "ares: not-installed") {
		t.Fatalf("update nope ares = %q, %v", out, err)
	}
	out, _, err = runDotForTest("--home", home, "ai", "family", "status")
	if err != nil || !strings.Contains(out, "ahub: not-installed") || !strings.Contains(out, "vmem: not-installed") || !strings.Contains(out, "ares: not-installed") || !strings.Contains(out, "acon: not-installed") {
		t.Fatalf("status = %q, %v", out, err)
	}
	if _, _, err = runDotForTest("--home", home, "ai", "family", "rollback", "amem"); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("rollback = %v", err)
	}
	if _, _, err = runDotForTest("--home", home, "ai", "family", "install", "amem", "--version", "latest"); err == nil || !strings.Contains(err.Error(), "stable X.Y.Z") {
		t.Fatalf("bad version = %v", err)
	}
}
