package syncer

import (
	"context"
	"errors"
	"fmt"
	"html"
	osexec "os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// Stable identifiers used across launchd / systemd integrations.
const (
	launchdLabel         = "com.dotfiles.sync"
	launchdLabelIntake   = "com.dotfiles.sync-intake"
	systemdServiceName   = "dotfiles-sync.service"
	systemdTimerName     = "dotfiles-sync.timer"
	systemdServiceIntake = "dotfiles-sync-intake.service"
	systemdTimerIntake   = "dotfiles-sync-intake.timer"
)

// Legacy unit identifiers superseded by the unified `dot sync` names live in
// the per-platform scheduler files (scheduler_darwin.go / scheduler_other.go);
// CleanupLegacyUnits removes them when installing or uninstalling the current
// units so stale schedulers never double-fire a sync.

// SchedulerKind selects which periodic action a Scheduler call targets.
// Push and intake are both opt-in via Interval/PullInterval > 0.
type SchedulerKind int

const (
	SchedulerKindPush SchedulerKind = iota
	SchedulerKindIntake
)

// Action returns the gsync subcommand the unit should invoke.
func (k SchedulerKind) Action() string {
	if k == SchedulerKindIntake {
		return "pull"
	}
	return "push"
}

// LaunchdLabel returns the launchd Label for this kind.
func (k SchedulerKind) LaunchdLabel() string {
	if k == SchedulerKindIntake {
		return launchdLabelIntake
	}
	return launchdLabel
}

// SystemdServiceName returns the systemd service unit filename.
func (k SchedulerKind) SystemdServiceName() string {
	if k == SchedulerKindIntake {
		return systemdServiceIntake
	}
	return systemdServiceName
}

// SystemdTimerName returns the systemd timer unit filename.
func (k SchedulerKind) SystemdTimerName() string {
	if k == SchedulerKindIntake {
		return systemdTimerIntake
	}
	return systemdTimerName
}

// Description is the human-readable Description= line written into
// systemd units (and used for log/status banners).
func (k SchedulerKind) Description() string {
	if k == SchedulerKindIntake {
		return "dot sync pull (baseline-tracked payload restore)"
	}
	return "dot sync push (workspace → target)"
}

// SchedulerState is the runtime status of an auto-sync timer.
type SchedulerState int

const (
	SchedulerNotInstalled SchedulerState = iota
	SchedulerRunning
	SchedulerStopped
	SchedulerTargetUserActionRequired
	// SchedulerSpawnFailed means launchd holds the job but cannot spawn it
	// (state = spawn failed in `launchctl print`), seen after a Homebrew
	// upgrade replaced the binary under launchd's managed launch constraint
	// (#233). The job never runs again until it is reloaded, so it is its
	// own state, not "running".
	SchedulerSpawnFailed
)

// SchedulerSnapshot holds the state and last process exit reported by the
// service manager. An absent exit code means no value was reported.
type SchedulerSnapshot struct {
	State        SchedulerState
	LastExitCode *int
}

func (s SchedulerState) String() string {
	switch s {
	case SchedulerRunning:
		return "running"
	case SchedulerStopped:
		return "stopped"
	case SchedulerTargetUserActionRequired:
		return "target user action required: " + SchedulerTargetUserInstruction()
	case SchedulerSpawnFailed:
		return "spawn failed; reload the job with `dot sync pause && dot sync resume`"
	default:
		return "not installed"
	}
}

// StringForProfile keeps recovery guidance aimed at the job being inspected.
func (s SchedulerState) StringForProfile(profile string) string {
	if s != SchedulerSpawnFailed || profile == "" || profile == DefaultProfile {
		return s.String()
	}
	command := "dot sync --profile=" + shellQuote(profile)
	return "spawn failed; reload the job with `" + command + " pause && " + command + " resume`"
}

// LaunchdSpawnFailed reports whether a `launchctl print` dump shows a job
// launchd cannot spawn. The recorded output names it "state = spawn failed"
// alongside the last exit code (78: EX_CONFIG for a launch-constraint
// mismatch); the job stays loaded, so an exit-0 print alone is not "running".
func LaunchdSpawnFailed(printOutput string) bool {
	for _, line := range strings.Split(printOutput, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"state =", "job state ="} {
			if value, ok := strings.CutPrefix(line, prefix); ok && strings.TrimSpace(value) == "spawn failed" {
				return true
			}
		}
	}
	return false
}

// LaunchdLastExitCode accepts both the numeric print form and launchd's
// annotated form, for example "last exit code = 78: EX_CONFIG".
func LaunchdLastExitCode(printOutput string) *int {
	for _, line := range strings.Split(printOutput, "\n") {
		raw, ok := strings.CutPrefix(strings.TrimSpace(line), "last exit code =")
		if !ok {
			continue
		}
		number, _, _ := strings.Cut(strings.TrimSpace(raw), ":")
		if value, err := strconv.Atoi(strings.TrimSpace(number)); err == nil {
			return &value
		}
	}
	return nil
}

// SchedulerTargetUserActionRequiredError means that scheduler artifacts were
// reconciled for an explicit --home target, but its user service-manager
// domain cannot be safely controlled by the invoking process.
type SchedulerTargetUserActionRequiredError struct{}

func (e *SchedulerTargetUserActionRequiredError) Error() string {
	return SchedulerTargetUserInstruction()
}

// SchedulerTargetUserInstruction is shared by scheduler state, engine errors,
// and CLI guidance. A home path does not identify a UID, login session, or
// systemd user bus, so callers must never try to derive one from --home.
func SchedulerTargetUserInstruction() string {
	return "scheduler artifacts were reconciled, but no service-manager action ran in the caller domain; log in as the target user or rerun the same command without --home via sudo -iu <target-user>"
}

// SchedulerTemplateData feeds the launchd plist + systemd unit templates.
type SchedulerTemplateData struct {
	DotfilesPath string // absolute path to the `dot` (or `dotfiles`) binary
	LogFile      string
	Interval     int

	// Per-kind fields: the templates render the same file twice with
	// distinct labels/actions so push and intake units don't collide.
	// Home is the --home override the unit must re-supply on every scheduled
	// run; empty renders no flag. Without it a unit installed for another
	// user's home runs `dot sync` against the invoking user's workspace on
	// every tick, long after the install command exited.
	Home              string
	PlistHomeArg      string
	PlistDotfilesPath string
	PlistLogFile      string
	SystemdHomeArg    string
	Label             string // launchd Label
	Profile           string // sync profile the unit operates on ("" for the default)
	Action            string // gsync subcommand to run
	Mode              string // non-interactive run mode (clean|force)
	Description       string // systemd Description= line
	ServiceName       string // systemd Unit= reference (timer → service)
}

// Scheduler manages the platform-specific periodic gsync timers.
//
// One Scheduler instance can install, pause, resume, or query either
// kind (push or intake). The high-level entry points (Install, Pause,
// Resume, State) act on the push unit by default and on both when
// cfg.PullInterval > 0; *Kind variants target a specific kind.
//
// Methods Install*Kind / Uninstall*Kind / Pause*Kind / Resume*Kind /
// StateKind are defined per platform in scheduler_darwin.go and
// scheduler_other.go.

// profileArg is the value rendered into the unit's command line. Empty for the
// default profile so existing units render byte-identical to before.
func profileArg(profile string) string {
	if profile == "" || profile == DefaultProfile {
		return ""
	}
	return profile
}

type Scheduler struct {
	Runner *exec.Runner
	Paths  *Paths
	Config *Config
	Engine *template.Engine
}

var schedulerLookPath = osexec.LookPath

type plistXMLTextError struct {
	value  string
	offset int
	reason string
}

func (e *plistXMLTextError) Error() string {
	return fmt.Sprintf("XML text %q is not XML 1.0 representable: %s at byte offset %d", e.value, e.reason, e.offset)
}

// plistXMLText validates and prepares one complete plist string value.
func plistXMLText(value, prefix string) (string, error) {
	text := prefix + value
	if !utf8.ValidString(value) {
		return "", &plistXMLTextError{value: text, offset: invalidUTF8Offset(value) + len(prefix), reason: "invalid UTF-8 byte"}
	}
	for offset, r := range value {
		if !xml10Character(r) {
			return "", &plistXMLTextError{value: text, offset: offset + len(prefix), reason: fmt.Sprintf("XML 1.0-illegal rune U+%04X", r)}
		}
	}
	// XML processors normalize literal carriage returns. Preserve a legal CR as
	// a character reference after escaping the complete logical value.
	return strings.ReplaceAll(html.EscapeString(text), "\r", "&#13;"), nil
}

// plistHomeArgument prepares a complete --home flag for XML character data.
func plistHomeArgument(home string) (string, error) {
	if home == "" {
		return "", nil
	}
	prepared, err := plistXMLText(home, "--home=")
	if err == nil {
		return prepared, nil
	}
	var textErr *plistXMLTextError
	if errors.As(err, &textErr) {
		return "", invalidXMLHomeError(home, textErr.offset-len("--home="), textErr.reason)
	}
	return "", err
}

func invalidUTF8Offset(value string) int {
	for offset := 0; offset < len(value); {
		_, width := utf8.DecodeRuneInString(value[offset:])
		if width == 1 && value[offset] >= utf8.RuneSelf {
			return offset
		}
		offset += width
	}
	return 0
}

func xml10Character(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= utf8.MaxRune)
}

func invalidXMLHomeError(home string, offset int, reason string) error {
	return fmt.Errorf("scheduler home %q is not XML 1.0 representable: %s at byte offset %d; rename or move it to a valid UTF-8 path without XML-illegal controls, then rerun scheduler setup", home, reason, offset)
}

// validateSchedulerMutationHome is the common pre-mutation guard for all
// scheduler entry points. plistHomeArgument remains the sole XML serializer
// and representability validator; scheduler mutations only need its verdict.
func validateSchedulerMutationHome(home string) error {
	_, err := plistHomeArgument(home)
	return err
}

func systemdHomeArgument(home string) string {
	if home == "" {
		return ""
	}
	argument := strings.NewReplacer("%", "%%", "$", "$$").Replace("--home=" + home)
	return strconv.Quote(argument)
}

// NewScheduler wires a Scheduler with all the things it needs to render
// templates and execute platform commands.
func NewScheduler(runner *exec.Runner, paths *Paths, cfg *Config, engine *template.Engine) *Scheduler {
	return &Scheduler{Runner: runner, Paths: paths, Config: cfg, Engine: engine}
}

// Install installs only explicitly enabled units. Idempotent. Legacy
// gdrive-sync / workspace-sync units are always removed first so a renamed
// scheduler never leaves a stale twin firing in parallel.
func (s *Scheduler) Install(ctx context.Context) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	return s.runArtifactOperations(
		func() error { return s.CleanupLegacyUnits(ctx) },
		func() error {
			if s.Config.Interval > 0 {
				return s.InstallKind(ctx, SchedulerKindPush)
			}
			return s.UninstallKind(ctx, SchedulerKindPush)
		},
		func() error {
			if s.Config.PullInterval > 0 {
				return s.InstallKind(ctx, SchedulerKindIntake)
			}
			return s.UninstallKind(ctx, SchedulerKindIntake)
		},
	)
}

// Uninstall removes both the push and intake units. Missing units are
// silently skipped (handled by the per-kind helpers).
func (s *Scheduler) Uninstall(ctx context.Context) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	return s.runArtifactOperations(
		func() error { return s.CleanupLegacyUnits(ctx) },
		func() error { return s.UninstallKind(ctx, SchedulerKindPush) },
		func() error { return s.UninstallKind(ctx, SchedulerKindIntake) },
	)
}

// Pause stops both units (intake only if installed).
func (s *Scheduler) Pause(ctx context.Context) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	return s.runArtifactOperations(
		func() error { return s.PauseKind(ctx, SchedulerKindPush) },
		func() error { return s.PauseKind(ctx, SchedulerKindIntake) },
	)
}

// Resume restarts both units (intake only if installed).
func (s *Scheduler) Resume(ctx context.Context) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	return s.runArtifactOperations(
		func() error { return s.ResumeKind(ctx, SchedulerKindPush) },
		func() error { return s.ResumeKind(ctx, SchedulerKindIntake) },
	)
}

func (s *Scheduler) requiresTargetUserServiceDomain() bool {
	return s != nil && schedulerRequiresTargetUserServiceDomain(s.Config)
}

// schedulerRequiresTargetUserServiceDomain reports whether scheduler artifacts
// target a home supplied explicitly by the operator. A home path is not enough
// evidence to address that user's launchd or systemd domain, so callers may
// stage or retire files but must not invoke the current user's service manager.
func schedulerRequiresTargetUserServiceDomain(cfg *Config) bool {
	return cfg != nil && cfg.Home != ""
}

func (s *Scheduler) runArtifactOperations(operations ...func() error) error {
	targetUserActionRequired := false
	for _, operation := range operations {
		if err := operation(); err != nil {
			var targetUserErr *SchedulerTargetUserActionRequiredError
			if errors.As(err, &targetUserErr) {
				targetUserActionRequired = true
				continue
			}
			return err
		}
	}
	if targetUserActionRequired {
		return &SchedulerTargetUserActionRequiredError{}
	}
	return nil
}

// templateDataFor resolves the binary path (preferring `dot` over the
// legacy `dotfiles` symlink) and bundles the per-kind template inputs.
func (s *Scheduler) templateDataFor(kind SchedulerKind) SchedulerTemplateData {
	dotfilesPath, _ := schedulerLookPath("dot")
	if dotfilesPath == "" {
		dotfilesPath, _ = schedulerLookPath("dotfiles")
	}
	interval := s.Config.Interval
	mode := s.Config.PushMode
	if kind == SchedulerKindIntake {
		interval = s.Config.PullInterval
		mode = s.Config.PullMode
	}
	if mode == "" {
		mode = ModeClean
	}
	// schedulerProfile, LaunchdLabelFor and SystemdServiceNameFor are all
	// nil-receiver safe, so a nil s.Paths yields the default-profile names.
	profile := s.Paths.schedulerProfile()
	return SchedulerTemplateData{
		DotfilesPath:   dotfilesPath,
		Home:           s.Config.Home,
		SystemdHomeArg: systemdHomeArgument(s.Config.Home),
		LogFile:        s.Config.LogFile,
		Interval:       interval,
		Label:          s.Paths.LaunchdLabelFor(kind),
		Profile:        profileArg(profile),
		Action:         kind.Action(),
		Mode:           mode.String(),
		Description:    kind.Description(),
		ServiceName:    s.Paths.SystemdServiceNameFor(kind),
	}
}

// preparePlistTemplateData derives XML-ready fields from raw scheduler data.
// Raw DotfilesPath and LogFile remain for systemd; launchd consumes only these
// prepared fields.
func preparePlistTemplateData(data *SchedulerTemplateData) error {
	homeArg, err := plistHomeArgument(data.Home)
	if err != nil {
		return fmt.Errorf("home %q: %w", data.Home, err)
	}
	dotfilesPath, err := plistXMLText(data.DotfilesPath, "")
	if err != nil {
		return fmt.Errorf("executable %q: %w", data.DotfilesPath, err)
	}
	logFile, err := plistXMLText(data.LogFile, "")
	if err != nil {
		return fmt.Errorf("log file %q: %w", data.LogFile, err)
	}
	data.PlistHomeArg = homeArg
	data.PlistDotfilesPath = dotfilesPath
	data.PlistLogFile = logFile
	return nil
}
