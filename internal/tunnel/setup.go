package tunnel

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	dottemplate "github.com/STAIxBWLB/dotfiles/internal/template"
)

// connectorWaitTimeout bounds the post-install wait for the connector to
// register with Cloudflare before setup reports it unobserved.
const connectorWaitTimeout = 20 * time.Second

// EventKind names one observable step outcome. The engine emits kinds and the
// data they interpolate; the command layer owns every string a user sees, so
// no formatted message crosses this seam.
type EventKind int

const (
	EventCloudflaredFound EventKind = iota
	EventCloudflaredWouldInstall
	EventCloudflaredInstalled
	EventCertFound
	EventCertMissing
	EventCertWouldLogin
	EventRemoteLoginReachable
	EventRemoteLoginWouldEnable
	EventRemoteLoginEnabled
	EventRemoteLoginFallback
	EventRemoteLoginEnabledViaFallback
	EventTunnelReused
	EventTunnelCreating
	EventTunnelCreated
	EventSudoPriming
	EventCredentialsInstalled
	EventCredentialsPresent
	EventConfigInstalled
	EventDNSUnchanged
	EventDNSConfigured
	EventDNSOverwritten
	EventDaemonInstalled
	EventConnectorRegistered
	EventConnectorMissing
	EventDaemonRemoved
	EventManagedFilesRemoved
)

// Event is one step outcome. Only the values the command layer cannot already
// hold travel here: the tunnel name and hostname are the caller's own inputs.
type Event struct {
	Kind        EventKind
	Path        string // cloudflared binary, cert, or installed file
	TunnelID    string
	Connections int
}

// ConfirmKind names a yes/no question the flow must ask. The command layer
// owns the wording and the unattended (--yes) policy for each one (D-09).
type ConfirmKind int

const (
	ConfirmInstallCloudflared ConfirmKind = iota
	ConfirmEnableRemoteLogin
	ConfirmOverwriteDNS
	ConfirmRemoveManagedFiles
	ConfirmDeleteRemoteTunnel
)

// SetupOptions controls Setup. TunnelName and Hostname arrive already
// resolved: prompting for them is the command layer's job.
type SetupOptions struct {
	TunnelName string
	Hostname   string
	Home       string // user home; CertPath and the credential source hang off it
	DryRun     bool
	// Confirm answers a ConfirmKind. Never called on a dry run.
	Confirm func(ConfirmKind) (bool, error)
	// Progress receives step outcomes as they happen; nil drops them.
	Progress func(Event)
}

// SetupResult reports what Setup established. TunnelID is empty on a dry run,
// which stops before any tunnel is looked up or created.
type SetupResult struct {
	CloudflaredPath string
	TunnelID        string
}

// UninstallOptions controls Uninstall. The tunnel identity comes from saved
// state; both fields may be empty when no setup ever completed.
type UninstallOptions struct {
	TunnelID   string
	TunnelName string
	Confirm    func(ConfirmKind) (bool, error)
	Progress   func(Event)
}

// UninstallResult reports which optional removals the user confirmed and
// whether the remote delete was skipped for a reason worth reporting.
type UninstallResult struct {
	RemovedConfig bool
	DeletedTunnel bool
	DeleteSkip    DeleteSkip
}

// DeleteSkip explains why a confirmed remote-tunnel delete did not run.
type DeleteSkip string

const (
	DeleteSkipNone               DeleteSkip = ""
	DeleteSkipCloudflaredMissing DeleteSkip = "cloudflared-missing"
	DeleteSkipNoTunnelName       DeleteSkip = "no-tunnel-name"
)

// LogOptions controls TailErrorLog.
type LogOptions struct {
	Lines int
}

// LogResult carries the tail of the daemon error log.
type LogResult struct {
	Text string
}

func (o SetupOptions) emit(event Event) { emit(o.Progress, event) }

func (o SetupOptions) confirm(kind ConfirmKind) (bool, error) { return confirm(o.Confirm, kind) }

func (o UninstallOptions) emit(event Event) { emit(o.Progress, event) }

func (o UninstallOptions) confirm(kind ConfirmKind) (bool, error) { return confirm(o.Confirm, kind) }

func emit(progress func(Event), event Event) {
	if progress != nil {
		progress(event)
	}
}

func confirm(ask func(ConfirmKind) (bool, error), kind ConfirmKind) (bool, error) {
	if ask == nil {
		return false, fmt.Errorf("no confirmation handler was provided")
	}
	return ask(kind)
}

// Setup configures this Mac for SSH over a Cloudflare Tunnel: cloudflared and
// its login cert, Remote Login, the tunnel record and its credentials, the
// /etc/cloudflared config, the DNS route, and the LaunchDaemon. Inputs are
// validated before the first mutation so an unattended run with bad inputs
// fails immediately instead of midway through system changes.
func Setup(ctx context.Context, runner *exec.Runner, opts SetupOptions) (*SetupResult, error) {
	if err := ValidateTunnelName(opts.TunnelName); err != nil {
		return nil, err
	}
	if err := ValidateHostname(opts.Hostname); err != nil {
		return nil, err
	}

	cloudflaredPath, err := ensureCloudflared(ctx, runner, opts)
	if err != nil {
		return nil, err
	}
	result := &SetupResult{CloudflaredPath: cloudflaredPath}

	if err := ensureCloudflaredCert(ctx, runner, opts, cloudflaredPath); err != nil {
		return nil, err
	}
	if err := ensureRemoteLogin(ctx, runner, opts); err != nil {
		return nil, err
	}
	if opts.DryRun {
		return result, nil
	}

	record, credSrc, err := resolveTunnelRecord(ctx, runner, opts, cloudflaredPath)
	if err != nil {
		return nil, err
	}
	if err := ValidateTunnelID(record.ID); err != nil {
		return nil, err
	}
	result.TunnelID = record.ID

	opts.emit(Event{Kind: EventSudoPriming})
	if err := runner.RunInteractive(ctx, "sudo", "-v"); err != nil {
		return nil, err
	}

	engine := dottemplate.NewEngine()
	if err := installTunnelConfig(ctx, runner, opts, engine, record.ID, credSrc); err != nil {
		return nil, err
	}
	if err := routeTunnelDNS(ctx, runner, opts, cloudflaredPath); err != nil {
		return nil, err
	}
	if err := installTunnelDaemon(ctx, runner, opts, engine, cloudflaredPath); err != nil {
		return nil, err
	}

	if connections, err := WaitForConnections(ctx, runner, cloudflaredPath, opts.TunnelName, connectorWaitTimeout); err == nil {
		opts.emit(Event{Kind: EventConnectorRegistered, Connections: connections})
	} else {
		opts.emit(Event{Kind: EventConnectorMissing})
	}
	return result, nil
}

// Uninstall removes the dot-managed LaunchDaemon, then asks about the two
// destructive extras: the /etc/cloudflared files and the Cloudflare tunnel
// itself. Daemon removal is best-effort — an absent daemon is not an error.
func Uninstall(ctx context.Context, runner *exec.Runner, opts UninstallOptions) (*UninstallResult, error) {
	_, _ = runner.Run(ctx, "sudo", "launchctl", "bootout", "system/"+Label)
	_, _ = runner.Run(ctx, "sudo", "rm", "-f", PlistPath)
	opts.emit(Event{Kind: EventDaemonRemoved})

	result := &UninstallResult{}
	removeConfig, err := opts.confirm(ConfirmRemoveManagedFiles)
	if err != nil {
		return nil, err
	}
	if removeConfig {
		_, _ = runner.Run(ctx, "sudo", "rm", "-f", ConfigPath)
		if opts.TunnelID != "" {
			_, _ = runner.Run(ctx, "sudo", "rm", "-f", EtcCredentialPath(opts.TunnelID))
		}
		_, _ = runner.Run(ctx, "sudo", "rmdir", ConfigDir)
		result.RemovedConfig = true
		opts.emit(Event{Kind: EventManagedFilesRemoved})
	}

	deleteTunnel, err := opts.confirm(ConfirmDeleteRemoteTunnel)
	if err != nil {
		return nil, err
	}
	if deleteTunnel {
		cloudflaredPath, found := lookupCloudflared()
		switch {
		case !found:
			result.DeleteSkip = DeleteSkipCloudflaredMissing
		case opts.TunnelName == "":
			result.DeleteSkip = DeleteSkipNoTunnelName
		default:
			if err := runner.RunInteractive(ctx, cloudflaredPath, "tunnel", "delete", opts.TunnelName); err != nil {
				return nil, err
			}
			result.DeletedTunnel = true
		}
	}
	return result, nil
}

// TailErrorLog returns the tail of the daemon's error log. A missing log file
// surfaces as an error the command layer renders as guidance.
func TailErrorLog(opts LogOptions) (*LogResult, error) {
	text, err := fileutil.TailLog(LogErrPath, opts.Lines)
	if err != nil {
		return nil, err
	}
	return &LogResult{Text: text}, nil
}

func ensureCloudflared(ctx context.Context, runner *exec.Runner, opts SetupOptions) (string, error) {
	if path, found := lookupCloudflared(); found {
		opts.emit(Event{Kind: EventCloudflaredFound, Path: path})
		return path, nil
	}
	if opts.DryRun {
		opts.emit(Event{Kind: EventCloudflaredWouldInstall})
		return "cloudflared", nil
	}
	confirmed, err := opts.confirm(ConfirmInstallCloudflared)
	if err != nil {
		return "", err
	}
	if !confirmed {
		return "", fmt.Errorf("cloudflared is required")
	}
	if _, found := lookupBrew(); !found {
		return "", fmt.Errorf("brew not found; install Homebrew first or install cloudflared manually")
	}
	if err := runner.RunAttached(ctx, "brew", "install", "cloudflared"); err != nil {
		return "", err
	}
	if path, found := lookupCloudflared(); found {
		opts.emit(Event{Kind: EventCloudflaredInstalled, Path: path})
		return path, nil
	}
	return "", fmt.Errorf("cloudflared not found in PATH after install")
}

func ensureCloudflaredCert(ctx context.Context, runner *exec.Runner, opts SetupOptions, cloudflaredPath string) error {
	cert := CertPath(opts.Home)
	if _, err := os.Stat(cert); err == nil {
		opts.emit(Event{Kind: EventCertFound, Path: cert})
		return nil
	}
	opts.emit(Event{Kind: EventCertMissing})
	if opts.DryRun {
		opts.emit(Event{Kind: EventCertWouldLogin, Path: cloudflaredPath})
		return nil
	}
	if err := runner.RunInteractive(ctx, cloudflaredPath, "tunnel", "login"); err != nil {
		return err
	}
	if _, err := os.Stat(cert); err != nil {
		return fmt.Errorf("cloudflare cert still missing at %s", cert)
	}
	return nil
}

func ensureRemoteLogin(ctx context.Context, runner *exec.Runner, opts SetupOptions) error {
	if Port22Open(time.Second) {
		opts.emit(Event{Kind: EventRemoteLoginReachable})
		return nil
	}
	if opts.DryRun {
		opts.emit(Event{Kind: EventRemoteLoginWouldEnable})
		return nil
	}
	confirmed, err := opts.confirm(ConfirmEnableRemoteLogin)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("port 22 is closed; tunnel setup aborted")
	}
	if err := runner.RunAttached(ctx, "sudo", "systemsetup", "-setremotelogin", "on"); err != nil {
		return err
	}
	if Port22Open(time.Second) {
		opts.emit(Event{Kind: EventRemoteLoginEnabled})
		return nil
	}
	opts.emit(Event{Kind: EventRemoteLoginFallback})
	if err := runner.RunAttached(ctx, "sudo", "launchctl", "load", "-w", "/System/Library/LaunchDaemons/ssh.plist"); err != nil {
		return err
	}
	if Port22Open(time.Second) {
		opts.emit(Event{Kind: EventRemoteLoginEnabledViaFallback})
		return nil
	}
	return fmt.Errorf("port 22 is still closed. Open System Settings > General > Sharing > Remote Login, then rerun dot tunnel setup")
}

func resolveTunnelRecord(ctx context.Context, runner *exec.Runner, opts SetupOptions, cloudflaredPath string) (*TunnelRecord, string, error) {
	record, found, err := LookupTunnelID(ctx, runner, cloudflaredPath, opts.TunnelName)
	if err != nil {
		return nil, "", err
	}
	if found {
		opts.emit(Event{Kind: EventTunnelReused, TunnelID: record.ID})
		credSrc := firstExistingPath(HomeCredentialPath(opts.Home, record.ID), EtcCredentialPath(record.ID))
		if credSrc == "" {
			return nil, "", fmt.Errorf("credentials JSON missing for tunnel %s. Run: cloudflared tunnel token --cred-file %s %s", record.ID, HomeCredentialPath(opts.Home, record.ID), record.ID)
		}
		return record, credSrc, nil
	}
	opts.emit(Event{Kind: EventTunnelCreating})
	record, err = CreateTunnel(ctx, runner, cloudflaredPath, opts.TunnelName)
	if err != nil {
		return nil, "", err
	}
	credSrc := record.CredentialsFile
	if credSrc == "" {
		credSrc = HomeCredentialPath(opts.Home, record.ID)
	}
	if _, err := os.Stat(credSrc); err != nil {
		return nil, "", fmt.Errorf("credentials JSON missing after tunnel create: %s", credSrc)
	}
	opts.emit(Event{Kind: EventTunnelCreated, TunnelID: record.ID})
	return record, credSrc, nil
}

func installTunnelConfig(ctx context.Context, runner *exec.Runner, opts SetupOptions, engine *dottemplate.Engine, tunnelID, credSrc string) error {
	if _, err := runner.Run(ctx, "sudo", "install", "-d", "-m", "0755", "-o", "root", "-g", "wheel", ConfigDir); err != nil {
		return fmt.Errorf("creating %s: %w", ConfigDir, err)
	}
	credDest := EtcCredentialPath(tunnelID)
	if filepath.Clean(credSrc) != filepath.Clean(credDest) {
		if err := SudoInstallFile(ctx, runner, credSrc, credDest, 0o600); err != nil {
			return err
		}
		opts.emit(Event{Kind: EventCredentialsInstalled, Path: credDest})
	} else {
		opts.emit(Event{Kind: EventCredentialsPresent, Path: credDest})
	}

	cfg, err := RenderConfig(engine, tunnelID, opts.Hostname)
	if err != nil {
		return err
	}
	if err := SudoInstallContent(ctx, runner, cfg, ConfigPath, 0o644); err != nil {
		return err
	}
	opts.emit(Event{Kind: EventConfigInstalled, Path: ConfigPath})
	return nil
}

func routeTunnelDNS(ctx context.Context, runner *exec.Runner, opts SetupOptions, cloudflaredPath string) error {
	status, err := RouteDNS(ctx, runner, cloudflaredPath, opts.TunnelName, opts.Hostname, false)
	if err == nil {
		if status == RouteDNSUnchanged {
			opts.emit(Event{Kind: EventDNSUnchanged})
		} else {
			opts.emit(Event{Kind: EventDNSConfigured})
		}
		return nil
	}
	if IsZoneMismatchError(err) {
		return fmt.Errorf("DNS route failed: cert.pem is zone-scoped. Re-run 'cloudflared tunnel login' for the zone that owns %s", opts.Hostname)
	}
	if !IsDNSConflictError(err) {
		return err
	}
	confirmed, confirmErr := opts.confirm(ConfirmOverwriteDNS)
	if confirmErr != nil {
		return confirmErr
	}
	if !confirmed {
		return fmt.Errorf("DNS route conflict for %s", opts.Hostname)
	}
	if _, err := RouteDNS(ctx, runner, cloudflaredPath, opts.TunnelName, opts.Hostname, true); err != nil {
		return err
	}
	opts.emit(Event{Kind: EventDNSOverwritten})
	return nil
}

func installTunnelDaemon(ctx context.Context, runner *exec.Runner, opts SetupOptions, engine *dottemplate.Engine, cloudflaredPath string) error {
	plist, err := RenderPlist(engine, cloudflaredPath)
	if err != nil {
		return err
	}
	if err := SudoInstallContent(ctx, runner, plist, PlistPath, 0o644); err != nil {
		return err
	}
	_, _ = runner.Run(ctx, "sudo", "launchctl", "bootout", "system/"+Label)
	if _, err := runner.Run(ctx, "sudo", "launchctl", "bootstrap", "system", PlistPath); err != nil {
		return err
	}
	opts.emit(Event{Kind: EventDaemonInstalled})
	return nil
}

func lookupBrew() (string, bool) {
	path, err := osexec.LookPath("brew")
	return path, err == nil
}

func firstExistingPath(paths ...string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
