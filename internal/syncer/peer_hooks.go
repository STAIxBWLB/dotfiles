package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// PeerHooks are per-machine actions around a coordinator role change (#184).
// The inactive Mac must run no scheduled jobs that write the workspace (Maru
// jobs write .maru, inbox state and digests, and collide on every sync), yet
// handover, takeover and demotion only ever moved the dot peer scheduler.
//
// on_activate runs when this machine installs its peer scheduler (dot peer
// setup, the step handover runs on the new coordinator and takeover names
// next); on_deactivate runs when it gives the role up (dot peer setup --off,
// handover on the old coordinator, demotion at the fence). Actions:
//
//	launchd-bootout <label-glob>    disable and boot out the jobs of ~/Library/LaunchAgents/<glob>.plist
//	launchd-bootstrap <label-glob>  re-enable those and bootstrap the ones not loaded
//	app-quit <App>                  quit a running app
//	app-open <App>                  open an app in the background
//
// A bootout alone lasts until the next login, when launchd loads every
// plist in ~/Library/LaunchAgents again; the disable (launchd's override
// database) keeps the jobs off across a reboot. dot records which jobs it
// disabled and on_activate enables only those, so a job stopped outside dot
// stays stopped. com.dotfiles.peer is never matched: it is dot's own
// scheduler.
//
// Failures are reported and logged; they never abort a sync or a switch.
type PeerHooks struct {
	OnActivate   []string `yaml:"on_activate,omitempty"`
	OnDeactivate []string `yaml:"on_deactivate,omitempty"`
}

// peerHookTimeout bounds one hook action (a var for tests).
var peerHookTimeout = time.Minute

// Hook phases.
const (
	HookOnActivate   = "on_activate"
	HookOnDeactivate = "on_deactivate"
)

// HookResult is one hook action's outcome. Detail says what it did (or would
// do, under DryRun).
type HookResult struct {
	Phase  string
	Action string
	DryRun bool
	Detail string
	Err    error
}

// runPeerHooks runs the phase's actions in order and appends each outcome to
// the peer log. A preview lists what each action would do and changes nothing.
func runPeerHooks(ctx context.Context, runner *exec.Runner, cfg *Config, phase string, dryRun bool) []HookResult {
	actions := cfg.Hooks.OnActivate
	if phase == HookOnDeactivate {
		actions = cfg.Hooks.OnDeactivate
	}
	var results []HookResult
	for _, action := range actions {
		res := HookResult{Phase: phase, Action: action, DryRun: dryRun}
		// A hook must not hold the peer lock: an app that never answers, or
		// a consent prompt nobody sees in a scheduled run, times out.
		actx, cancel := context.WithTimeout(ctx, peerHookTimeout)
		res.Detail, res.Err = runPeerHook(actx, runner, cfg, action, dryRun)
		if res.Err != nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
			res.Err = fmt.Errorf("timed out after %s (an app that does not answer, or a consent prompt nobody saw)", peerHookTimeout)
		}
		cancel()
		results = append(results, res)
		if !dryRun && cfg.LogFile != "" {
			exit := 0
			if res.Err != nil {
				exit = 1
			}
			if err := ensureLogDir(cfg.LogFile); err == nil {
				AppendLog(cfg.LogFile, "hook "+phase+" "+action+" ("+hookOutcome(res)+")", exit)
			}
		}
	}
	return results
}

func hookOutcome(res HookResult) string {
	if res.Err != nil {
		return res.Err.Error()
	}
	return res.Detail
}

func runPeerHook(ctx context.Context, runner *exec.Runner, cfg *Config, action string, dryRun bool) (string, error) {
	verb, arg, _ := strings.Cut(strings.TrimSpace(action), " ")
	arg = strings.TrimSpace(arg)
	switch verb {
	case "launchd-bootout", "launchd-bootstrap", "app-quit", "app-open":
	default:
		return "", fmt.Errorf("unknown hook action %q (want launchd-bootout, launchd-bootstrap, app-quit or app-open)", verb)
	}
	if arg == "" || strings.ContainsAny(arg, "\"\\\n") {
		return "", fmt.Errorf("hook action %q needs one argument without quotes or backslashes", action)
	}
	if strings.HasPrefix(verb, "launchd-") {
		// A malformed glob would otherwise match nothing and report success
		// while the jobs keep running; a label never holds a slash, and the
		// bootstrap plists come from ~/Library/LaunchAgents only.
		if _, err := path.Match(arg, ""); err != nil {
			return "", fmt.Errorf("bad label glob %q: %w", arg, err)
		}
		if strings.Contains(arg, "/") {
			return "", fmt.Errorf("label glob %q holds a slash", arg)
		}
		if strings.IndexAny(arg, "*?[") == 0 {
			// A bare * would match every gui agent, com.apple.* included.
			return "", fmt.Errorf("label glob %q must start with a literal prefix, e.g. com.maru.job.*", arg)
		}
	}
	if runtime.GOOS != "darwin" {
		return "skipped: needs macOS", nil
	}
	if schedulerRequiresTargetUserServiceDomain(cfg) {
		// launchctl and the app actions would act in the caller's session,
		// not the --home user's.
		return "skipped: --home targets another user's session", nil
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	switch verb {
	case "launchd-bootout", "launchd-bootstrap":
		if cfg.LocalPaths == nil {
			return "", fmt.Errorf("peer store unresolved: the disabled-job record lives there")
		}
		// Only jobs with a plist here: launchd-bootstrap can restore those,
		// and a glob never reaches an app's bundled or system agents.
		plists, err := agentPlists(cfg, arg)
		if err != nil {
			return "", err
		}
		if len(plists) == 0 {
			// Most likely a typo: the jobs it meant keep running.
			return "", fmt.Errorf("no ~/Library/LaunchAgents/%s.plist matches", arg)
		}
		loaded, err := loadedLaunchdLabels(ctx, runner, domain)
		if err != nil {
			return "", err
		}
		disabled, err := disabledLaunchdLabels(ctx, runner, domain)
		if err != nil {
			return "", err
		}
		if verb == "launchd-bootout" {
			return launchdBootout(ctx, runner, cfg, domain, plists, loaded, disabled, dryRun)
		}
		return launchdBootstrap(ctx, runner, cfg, domain, plists, loaded, disabled, dryRun)
	case "app-quit":
		if dryRun {
			return "would quit " + arg + " if running", nil
		}
		// An Apple Event: dot needs Automation consent for the app once
		// (README). The timeout bounds an app that never answers.
		script := `if application "` + arg + `" is running then
with timeout of 20 seconds
tell application "` + arg + `" to quit
end timeout
end if`
		if _, err := runner.Run(ctx, "osascript", "-e", script); err != nil {
			return "", errors.New(hookErr(err))
		}
		return "quit " + arg, nil
	default: // app-open
		if dryRun {
			return "would open " + arg, nil
		}
		if _, err := runner.Run(ctx, "open", "-g", "-a", arg); err != nil {
			return "", errors.New(hookErr(err))
		}
		return "opened " + arg, nil
	}
}

// hookDisabledFile records the jobs on_deactivate disabled. on_activate
// enables exactly those, so a job stopped outside dot (Maru's Stop is a
// launchd disable) stays stopped. The peer store is per machine.
func hookDisabledFile(cfg *Config) string {
	return filepath.Join(cfg.LocalPaths.StoreDir, "hooks-disabled.txt")
}

func readHookDisabled(cfg *Config) (map[string]bool, error) {
	data, err := os.ReadFile(hookDisabledFile(cfg))
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, label := range strings.Fields(string(data)) {
		set[label] = true
	}
	return set, nil
}

func writeHookDisabled(cfg *Config, set map[string]bool) error {
	labels := make([]string, 0, len(set))
	for label := range set {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	body := strings.Join(labels, "\n")
	if body != "" {
		body += "\n"
	}
	return atomicWrite(hookDisabledFile(cfg), []byte(body))
}

// launchdBootout disables the plist-backed jobs that are not disabled yet,
// records them, and boots out the loaded ones. A disable outlasts a reboot,
// which a bootout alone does not.
func launchdBootout(ctx context.Context, runner *exec.Runner, cfg *Config, domain string, plists []string, loaded []string, disabled map[string]bool, dryRun bool) (string, error) {
	var disable, bootout []string
	for _, label := range baseNames(plists) {
		if !disabled[label] {
			disable = append(disable, label)
		}
		if slices.Contains(loaded, label) {
			bootout = append(bootout, label)
		}
	}
	summary := plural(len(disable), "job") + " disabled, " + plural(len(bootout), "loaded job") + " booted out" + listSuffix(bootout)
	if dryRun {
		return "would: " + summary, nil
	}
	recorded, err := readHookDisabled(cfg)
	if err != nil {
		return "", err
	}
	// Recorded before any disable: a job disabled but not recorded would
	// later read as stopped outside dot and never come back. A recorded
	// label whose disable fails is dropped again by on_activate.
	for _, label := range disable {
		recorded[label] = true
	}
	if err := writeHookDisabled(cfg, recorded); err != nil {
		return "", err
	}
	var failed []string
	for _, label := range disable {
		if _, err := runner.Run(ctx, "launchctl", "disable", domain+"/"+label); err != nil {
			failed = append(failed, label+" ("+hookErr(err)+")")
		}
	}
	for _, label := range bootout {
		if _, err := runner.Run(ctx, "launchctl", "bootout", domain+"/"+label); err != nil {
			failed = append(failed, label+" ("+hookErr(err)+")")
		}
	}
	if len(failed) > 0 {
		return "", fmt.Errorf("disabling or booting out %s failed", strings.Join(failed, ", "))
	}
	return summary, nil
}

// launchdBootstrap enables the jobs on_deactivate recorded and bootstraps
// the plists that are neither loaded nor disabled; a job disabled outside
// dot is left alone.
func launchdBootstrap(ctx context.Context, runner *exec.Runner, cfg *Config, domain string, plists []string, loaded []string, disabled map[string]bool, dryRun bool) (string, error) {
	recorded, err := readHookDisabled(cfg)
	if err != nil {
		return "", err
	}
	var enable, todo, kept []string
	for _, plist := range plists {
		label := strings.TrimSuffix(filepath.Base(plist), ".plist")
		off := disabled[label]
		switch {
		case off && recorded[label]:
			enable = append(enable, label)
		case off:
			kept = append(kept, label)
			continue
		}
		if !slices.Contains(loaded, label) {
			todo = append(todo, plist)
		}
	}
	summary := plural(len(enable), "job") + " enabled, " + plural(len(todo), "job") + " bootstrapped" + listSuffix(baseNames(todo))
	if len(kept) > 0 {
		summary += "; left disabled (stopped outside dot)" + listSuffix(kept)
	}
	if dryRun {
		return "would: " + summary, nil
	}
	var failed []string
	stillOff := map[string]bool{}
	for _, label := range enable {
		if _, err := runner.Run(ctx, "launchctl", "enable", domain+"/"+label); err != nil {
			failed = append(failed, label+" ("+hookErr(err)+")")
			stillOff[label] = true // a disabled job would fail its bootstrap too
			continue
		}
		delete(recorded, label)
	}
	// A recorded job enabled by hand since is no longer dot's to restore.
	for _, label := range baseNames(plists) {
		if recorded[label] && !disabled[label] {
			delete(recorded, label)
		}
	}
	if err := writeHookDisabled(cfg, recorded); err != nil {
		return "", err
	}
	for _, plist := range todo {
		if stillOff[strings.TrimSuffix(filepath.Base(plist), ".plist")] {
			continue
		}
		if _, err := runner.Run(ctx, "launchctl", "bootstrap", domain, plist); err != nil {
			failed = append(failed, filepath.Base(plist)+" ("+hookErr(err)+")")
		}
	}
	if len(failed) > 0 {
		return "", fmt.Errorf("enabling or bootstrapping %s failed", strings.Join(failed, ", "))
	}
	return summary, nil
}

// disabledLaunchdLabels lists the domain's disabled services from launchd's
// override database ("label" => disabled, or => true on older macOS).
func disabledLaunchdLabels(ctx context.Context, runner *exec.Runner, domain string) (map[string]bool, error) {
	res, err := runner.RunQuery(ctx, "launchctl", "print-disabled", domain)
	if err != nil {
		return nil, fmt.Errorf("launchctl print-disabled %s: %s", domain, hookErr(err))
	}
	set := map[string]bool{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		label, state, ok := strings.Cut(strings.TrimSpace(line), "=>")
		if !ok {
			continue
		}
		if st := strings.TrimSpace(state); st == "disabled" || st == "true" {
			set[strings.Trim(strings.TrimSpace(label), `"`)] = true
		}
	}
	return set, nil
}

// hookErr is a one-line reason: the command's own stderr when it has one.
func hookErr(err error) string {
	var cmdErr *exec.CmdError
	if errors.As(err, &cmdErr) && cmdErr.Details() != "" {
		return firstLine(cmdErr.Details())
	}
	return firstLine(err.Error())
}

// loadedLaunchdLabels lists the services loaded in the gui domain. `launchctl
// list` answers for the caller's own domain, which over ssh is not the gui
// session the Maru agents run in; `print gui/<uid>` names it explicitly.
func loadedLaunchdLabels(ctx context.Context, runner *exec.Runner, domain string) ([]string, error) {
	res, err := runner.RunQuery(ctx, "launchctl", "print", domain)
	if err != nil {
		return nil, fmt.Errorf("launchctl print %s: %s", domain, hookErr(err))
	}
	var labels []string
	in := false
	for _, line := range strings.Split(res.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !in && trimmed == "services = {":
			in = true
		case in && trimmed == "}":
			in = false
		case in:
			// "<pid> <last exit> <label>"
			if fields := strings.Fields(trimmed); len(fields) >= 3 {
				labels = append(labels, fields[len(fields)-1])
			}
		}
	}
	sort.Strings(labels)
	return labels, nil
}

// agentPlists lists ~/Library/LaunchAgents/<glob>.plist, dot's own
// scheduler excluded.
func agentPlists(cfg *Config, glob string) ([]string, error) {
	plists, err := filepath.Glob(filepath.Join(cfg.HomeDir(), "Library", "LaunchAgents", glob+".plist"))
	return slices.DeleteFunc(plists, func(p string) bool {
		return filepath.Base(p) == peerSchedulerLabel+".plist"
	}), err
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func listSuffix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return ": " + strings.Join(items, ", ")
}

func baseNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = strings.TrimSuffix(filepath.Base(p), ".plist")
	}
	return out
}
