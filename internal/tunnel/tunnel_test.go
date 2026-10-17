package tunnel

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	dottemplate "github.com/STAIxBWLB/dotfiles/internal/template"
)

func TestParseTunnelListJSON(t *testing.T) {
	data := []byte(`[
		{"id":"11111111-1111-1111-1111-111111111111","name":"other","connections":[]},
		{"id":"123e4567-e89b-12d3-a456-426614174000","name":"dot-mac","connections":[{"id":"a"},{"id":"b"}]}
	]`)
	record, found, err := ParseTunnelListJSON(data, "dot-mac")
	if err != nil {
		t.Fatalf("ParseTunnelListJSON: %v", err)
	}
	if !found {
		t.Fatal("expected tunnel to be found")
	}
	if record.ID != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("ID = %q", record.ID)
	}
	if record.Connections != 2 {
		t.Fatalf("Connections = %d, want 2", record.Connections)
	}
}

func TestParseTunnelCreateJSON(t *testing.T) {
	record, err := ParseTunnelCreateJSON([]byte(`{
		"id":"123e4567-e89b-12d3-a456-426614174000",
		"name":"dot-mac",
		"credentials_file":"/Users/me/.cloudflared/123e4567-e89b-12d3-a456-426614174000.json"
	}`))
	if err != nil {
		t.Fatalf("ParseTunnelCreateJSON: %v", err)
	}
	if record.Name != "dot-mac" || record.CredentialsFile == "" {
		t.Fatalf("unexpected record: %#v", record)
	}
}

func TestLookupAndCreateTunnelUseJSON(t *testing.T) {
	fake := fakeCloudflared(t)
	runner := exec.NewRunner(false, slog.Default())

	record, found, err := LookupTunnelID(context.Background(), runner, fake, "dot-mac")
	if err != nil {
		t.Fatalf("LookupTunnelID: %v", err)
	}
	if !found || record.ID != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("lookup = %#v found=%v", record, found)
	}

	created, err := CreateTunnel(context.Background(), runner, fake, "dot-new")
	if err != nil {
		t.Fatalf("CreateTunnel: %v", err)
	}
	if created.Name != "dot-new" || created.ID != "223e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("created = %#v", created)
	}
}

func TestRenderConfigAndPlist(t *testing.T) {
	engine := dottemplate.NewEngine()
	cfg, err := RenderConfig(engine, "123e4567-e89b-12d3-a456-426614174000", "mac.example.com")
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	cfgText := string(cfg)
	for _, want := range []string{
		"tunnel: 123e4567-e89b-12d3-a456-426614174000",
		"credentials-file: /etc/cloudflared/123e4567-e89b-12d3-a456-426614174000.json",
		"hostname: mac.example.com",
		"service: ssh://localhost:22",
	} {
		if !strings.Contains(cfgText, want) {
			t.Fatalf("config missing %q:\n%s", want, cfgText)
		}
	}

	plist, err := RenderPlist(engine, "/opt/homebrew/bin/cloudflared")
	if err != nil {
		t.Fatalf("RenderPlist: %v", err)
	}
	plistText := string(plist)
	for _, want := range []string{
		"<string>/opt/homebrew/bin/cloudflared</string>",
		"<string>--config</string>",
		"<string>/etc/cloudflared/config.yml</string>",
		"<string>tunnel</string>",
		"<string>run</string>",
		"<key>RunAtLoad</key>",
		"<string>/Library/Logs/com.dotfiles.cloudflared.err.log</string>",
	} {
		if !strings.Contains(plistText, want) {
			t.Fatalf("plist missing %q:\n%s", want, plistText)
		}
	}
}

func TestValidation(t *testing.T) {
	validID := "123e4567-e89b-12d3-a456-426614174000"
	if err := ValidateTunnelID(validID); err != nil {
		t.Fatalf("valid UUID rejected: %v", err)
	}
	if err := ValidateHostname("mac.example.com"); err != nil {
		t.Fatalf("valid hostname rejected: %v", err)
	}
	if err := ValidateTunnelName("dot-macbook"); err != nil {
		t.Fatalf("valid tunnel name rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"bad uuid", func() error { return ValidateTunnelID("bad") }},
		{"uppercase host", func() error { return ValidateHostname("Mac.example.com") }},
		{"single label host", func() error { return ValidateHostname("mac") }},
		{"leading hyphen", func() error { return ValidateHostname("-mac.example.com") }},
		{"name whitespace", func() error { return ValidateTunnelName("dot mac") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func fakeCloudflared(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudflared")
	script := `#!/bin/sh
case "$*" in
  "tunnel list --name dot-mac --output json")
    printf '%s\n' '[{"id":"123e4567-e89b-12d3-a456-426614174000","name":"dot-mac","connections":[{}]}]'
    ;;
  "tunnel create --output json dot-new")
    printf '%s\n' '{"id":"223e4567-e89b-12d3-a456-426614174000","name":"dot-new","credentials_file":"/tmp/223e4567-e89b-12d3-a456-426614174000.json"}'
    ;;
  *)
    echo "unexpected args: $*" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
