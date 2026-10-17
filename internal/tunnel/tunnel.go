package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	dottemplate "github.com/STAIxBWLB/dotfiles/internal/template"
)

const (
	ConfigDir  = "/etc/cloudflared"
	ConfigPath = "/etc/cloudflared/config.yml"
	PlistPath  = "/Library/LaunchDaemons/com.dotfiles.cloudflared.plist"
	Label      = "com.dotfiles.cloudflared"
	LogOutPath = "/Library/Logs/com.dotfiles.cloudflared.out.log"
	LogErrPath = "/Library/Logs/com.dotfiles.cloudflared.err.log"
)

type TunnelRecord struct {
	ID              string
	Name            string
	CredentialsFile string
	Connections     int
}

type DaemonState string

const (
	DaemonNotInstalled DaemonState = "not installed"
	DaemonLoaded       DaemonState = "loaded"
	DaemonRunning      DaemonState = "running"
)

type RouteDNSStatus string

const (
	RouteDNSChanged   RouteDNSStatus = "changed"
	RouteDNSUnchanged RouteDNSStatus = "unchanged"
)

type configTemplateData struct {
	TunnelID string
	Hostname string
}

type plistTemplateData struct {
	CloudflaredPath string
}

type cloudflaredTunnelJSON struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	CredentialsFile string            `json:"credentials_file"`
	Connections     []json.RawMessage `json:"connections"`
}

func LookupTunnelID(ctx context.Context, runner *exec.Runner, cloudflaredPath, name string) (*TunnelRecord, bool, error) {
	result, err := runner.RunQuery(ctx, cloudflaredPath, "tunnel", "list", "--name", name, "--output", "json")
	if err != nil {
		return nil, false, err
	}
	return ParseTunnelListJSON([]byte(result.Stdout), name)
}

func CreateTunnel(ctx context.Context, runner *exec.Runner, cloudflaredPath, name string) (*TunnelRecord, error) {
	result, err := runner.Run(ctx, cloudflaredPath, "tunnel", "create", "--output", "json", name)
	if err != nil {
		return nil, err
	}
	return ParseTunnelCreateJSON([]byte(result.Stdout))
}

func RouteDNS(ctx context.Context, runner *exec.Runner, cloudflaredPath, tunnelName, hostname string, overwrite bool) (RouteDNSStatus, error) {
	args := []string{"tunnel", "route", "dns"}
	if overwrite {
		args = append(args, "--overwrite-dns")
	}
	args = append(args, tunnelName, hostname)

	result, err := runner.Run(ctx, cloudflaredPath, args...)
	if err == nil {
		_ = result
		return RouteDNSChanged, nil
	}
	if IsAlreadyConfiguredError(err) {
		return RouteDNSUnchanged, nil
	}
	return "", err
}

func WaitForConnections(ctx context.Context, runner *exec.Runner, cloudflaredPath, tunnelName string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		record, found, err := LookupTunnelID(ctx, runner, cloudflaredPath, tunnelName)
		if err == nil && found && record.Connections > 0 {
			return record.Connections, nil
		}
		if err != nil {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return 0, lastErr
			}
			return 0, fmt.Errorf("no active tunnel connections after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func ParseTunnelListJSON(data []byte, name string) (*TunnelRecord, bool, error) {
	var records []cloudflaredTunnelJSON
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, false, fmt.Errorf("parsing tunnel list JSON: %w", err)
	}
	for _, record := range records {
		if record.Name == name {
			return fromCloudflaredJSON(record), true, nil
		}
	}
	return nil, false, nil
}

func ParseTunnelCreateJSON(data []byte) (*TunnelRecord, error) {
	var record cloudflaredTunnelJSON
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parsing tunnel create JSON: %w", err)
	}
	if record.ID == "" || record.Name == "" {
		return nil, fmt.Errorf("tunnel create JSON missing id or name")
	}
	return fromCloudflaredJSON(record), nil
}

func fromCloudflaredJSON(record cloudflaredTunnelJSON) *TunnelRecord {
	return &TunnelRecord{
		ID:              record.ID,
		Name:            record.Name,
		CredentialsFile: record.CredentialsFile,
		Connections:     len(record.Connections),
	}
}

// DaemonStateFor reports the tunnel daemon's state without requiring root.
// `launchctl print system/<label>` is denied to non-root users (it fails even
// for running system daemons), so the running probe is a pgrep for a process
// whose argv contains our config path — unique to the dot-managed daemon.
func DaemonStateFor(ctx context.Context, runner *exec.Runner) DaemonState {
	if !runner.FileExists(PlistPath) {
		return DaemonNotInstalled
	}
	result, err := runner.RunQuery(ctx, "pgrep", "-f", ConfigPath)
	if err == nil && result != nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return DaemonRunning
	}
	return DaemonLoaded
}

func Port22Open(timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", "localhost:22", timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func RenderConfig(engine *dottemplate.Engine, tunnelID, hostname string) ([]byte, error) {
	return engine.Render("tunnel/config.yml.tmpl", configTemplateData{TunnelID: tunnelID, Hostname: hostname})
}

func RenderPlist(engine *dottemplate.Engine, cloudflaredPath string) ([]byte, error) {
	return engine.Render("tunnel/com.dotfiles.cloudflared.plist.tmpl", plistTemplateData{CloudflaredPath: cloudflaredPath})
}

func SudoInstallFile(ctx context.Context, runner *exec.Runner, src, dest string, mode os.FileMode) error {
	if _, err := runner.Run(ctx, "sudo", "install", "-m", modeString(mode), "-o", "root", "-g", "wheel", src, dest); err != nil {
		return fmt.Errorf("installing %s: %w", dest, err)
	}
	return nil
}

func SudoInstallContent(ctx context.Context, runner *exec.Runner, content []byte, dest string, mode os.FileMode) error {
	tmp, err := os.CreateTemp("", "dot-tunnel-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	return SudoInstallFile(ctx, runner, tmpPath, dest, mode)
}

func HomeCredentialPath(home, tunnelID string) string {
	return filepath.Join(home, ".cloudflared", tunnelID+".json")
}

func EtcCredentialPath(tunnelID string) string {
	return filepath.Join(ConfigDir, tunnelID+".json")
}

func CertPath(home string) string {
	return filepath.Join(home, ".cloudflared", "cert.pem")
}

func ValidateTunnelName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("tunnel name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("tunnel name must be 64 characters or fewer")
	}
	for _, r := range name {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return fmt.Errorf("tunnel name may not contain whitespace")
		}
	}
	return nil
}

func ValidateTunnelID(id string) error {
	if len(id) != 36 {
		return fmt.Errorf("tunnel id must be a UUID")
	}
	for i, r := range id {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return fmt.Errorf("tunnel id must be a UUID")
			}
		default:
			if !isHexDigit(r) {
				return fmt.Errorf("tunnel id must be a UUID")
			}
		}
	}
	return nil
}

func ValidateHostname(host string) error {
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") {
		return fmt.Errorf("hostname must be a lowercase hostname with at least one dot")
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("hostname labels must be 1..63 characters")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("hostname labels may not start or end with '-'")
		}
		for _, r := range label {
			if !isHostnameRune(r) {
				return fmt.Errorf("hostname must use lowercase letters, digits, dots, and hyphens")
			}
		}
	}
	return nil
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func isHostnameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
}

func IsAlreadyConfiguredError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already configured")
}

func IsDNSConflictError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "record already exists") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "conflict") ||
		strings.Contains(msg, "cname")
}

func IsZoneMismatchError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "zone") && (strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") || strings.Contains(msg, "failed to find"))
}

func modeString(mode os.FileMode) string {
	return fmt.Sprintf("%04o", mode.Perm())
}
