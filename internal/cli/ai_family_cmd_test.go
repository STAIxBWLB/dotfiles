package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
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
	// The condition and the command that fixes it are two lines: the second is
	// the shared next-step form, so a person copies it without editing the first.
	if !strings.Contains(out, "\n  "+ui.MarkNext+" dot ai family install --all\n") {
		t.Fatalf("update with nothing installed printed no next-step line: %q", out)
	}
	// A named tool that is not installed is skipped; an unknown one fails without stopping the rest.
	out, _, err = runDotForTest("--home", home, "ai", "family", "update", "nope", "ares")
	if err == nil || !strings.Contains(err.Error(), "nope: unknown family tool") || !strings.Contains(out, "ares: not-installed") {
		t.Fatalf("update nope ares = %q, %v", out, err)
	}
	out, _, err = runDotForTest("--home", home, "ai", "family", "status")
	if err != nil || !strings.Contains(out, "ahub: not-installed") || !strings.Contains(out, "vmem: not-installed") || !strings.Contains(out, "ares: not-installed") || !strings.Contains(out, "acon: not-installed") || !strings.Contains(out, "arev: not-installed") {
		t.Fatalf("status = %q, %v", out, err)
	}
	if _, _, err = runDotForTest("--home", home, "ai", "family", "rollback", "amem"); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("rollback = %v", err)
	}
	if _, _, err = runDotForTest("--home", home, "ai", "family", "install", "amem", "--version", "latest"); err == nil || !strings.Contains(err.Error(), "stable X.Y.Z") {
		t.Fatalf("bad version = %v", err)
	}
}

type familyRegistryStub struct{}

func (familyRegistryStub) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "registry.npmjs.org" || !strings.HasSuffix(req.URL.Path, "/latest") {
		return nil, fmt.Errorf("unexpected registry request: %s", req.URL)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"version":"0.1.0"}`)),
		Request:    req,
	}, nil
}

func TestAIFamilyInstallAllIncludesArev(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = familyRegistryStub{}
	t.Cleanup(func() { http.DefaultTransport = transport })
	out, _, err := runDotForTest("--home", t.TempDir(), "--dry-run", "ai", "family", "install", "--all")
	if err != nil || !strings.Contains(out, "arev: planned") || !strings.Contains(out, "@staix/agent-review@0.1.0") {
		t.Fatalf("install --all = %q, %v", out, err)
	}
}
