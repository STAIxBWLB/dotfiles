package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootRegistersTunnel(t *testing.T) {
	root := NewRootCmd("dev", "test")
	known := knownSubcommands(root)
	if !known["tunnel"] {
		t.Fatal("knownSubcommands missing tunnel")
	}
	cmd, _, err := root.Find([]string{"tunnel"})
	if err != nil {
		t.Fatalf("Find(tunnel): %v", err)
	}
	if cmd.Name() != "tunnel" {
		t.Fatalf("Find(tunnel) = %q, want tunnel", cmd.Name())
	}
}

func TestTunnelClientAddListRemove(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte("Include ~/.ssh/config.d/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runDotForTest("--home", home, "tunnel", "client", "add", "mac.example.com")
	if err != nil {
		t.Fatalf("client add: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "added mac.example.com") {
		t.Fatalf("add output unexpected:\n%s", out)
	}

	out, errOut, err = runDotForTest("--home", home, "tunnel", "client", "add", "mac.example.com")
	if err != nil {
		t.Fatalf("client add idempotent: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "already configured") {
		t.Fatalf("duplicate add should say already configured, got:\n%s", out)
	}
	if strings.Contains(out, "added mac.example.com") {
		t.Fatalf("duplicate add must not claim it added the host:\n%s", out)
	}
	data, err := os.ReadFile(filepath.Join(sshDir, "config.d", "dot-tunnel"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "Host mac.example.com"); count != 1 {
		t.Fatalf("expected one Host block, got %d:\n%s", count, data)
	}

	out, errOut, err = runDotForTest("--home", home, "tunnel", "client", "list")
	if err != nil {
		t.Fatalf("client list: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if strings.TrimSpace(out) != "mac.example.com" {
		t.Fatalf("list output = %q", out)
	}

	out, errOut, err = runDotForTest("--home", home, "tunnel", "client", "remove", "mac.example.com")
	if err != nil {
		t.Fatalf("client remove: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "removed mac.example.com") {
		t.Fatalf("remove output unexpected:\n%s", out)
	}
	out, _, err = runDotForTest("--home", home, "tunnel", "client", "list")
	if err != nil {
		t.Fatalf("client list after remove: %v", err)
	}
	if !strings.Contains(out, "No tunnel SSH hosts configured") {
		t.Fatalf("list after remove unexpected:\n%s", out)
	}
}

func TestTunnelClientDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	out, errOut, err := runDotForTest("--home", home, "tunnel", "client", "add", "mac.example.com", "--dry-run")
	if err != nil {
		t.Fatalf("client add --dry-run: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "would ") {
		t.Fatalf("dry-run output unexpected:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "config.d", "dot-tunnel")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote drop-in, stat err=%v", err)
	}
}

func TestTunnelSetupNonDarwinAbortsBeforeWork(t *testing.T) {
	cmd := newTunnelSetupCmd()
	err := runTunnelSetupForGOOS(cmd, nil, "linux")
	if err == nil {
		t.Fatal("expected non-darwin setup to fail")
	}
	if !strings.Contains(err.Error(), "macOS-only") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Unattended first-run setup must fail on the missing hostname BEFORE any
// system mutation (no brew install, no browser login), with an error that
// names the --hostname flag. GOOS is forced to darwin so the input check —
// which precedes every platform-specific step — is exercised on any host.
func TestTunnelSetupUnattendedRequiresHostname(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	cmd := newTunnelSetupCmd()
	cmd.Flags().Bool("yes", true, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().String("home", "", "")
	err := runTunnelSetupForGOOS(cmd, nil, "darwin")
	if err == nil {
		t.Fatal("expected unattended first-run setup to fail")
	}
	if !strings.Contains(err.Error(), "--hostname") {
		t.Fatalf("error should point at --hostname, got: %v", err)
	}
}
