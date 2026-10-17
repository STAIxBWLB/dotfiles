package syncer

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// BootstrapOptions carries everything the sync and peer command trees used to
// read off a *cobra.Command before doing any work. Flag translation stays in
// cli; loading the user state, resolving the profile and constructing the
// runner are the engine's.
//
// The fields are deliberately the flags themselves rather than resolved
// objects: a caller with no cobra command (a scheduled run, a test) can fill
// the same struct without inventing a flag set.
type BootstrapOptions struct {
	// Profile names the sync store to resolve. Empty means the default
	// profile, matching the `--profile` flag's own default.
	Profile string

	// ReadOnly resolves the profile without creating the local store,
	// migrating global config, or healing .gitignore. It is a separate field
	// from DryRun on purpose: `dot peer status` is read-only with a live
	// runner, and `dot sync status --dry-run` is both.
	ReadOnly bool

	// DryRun reaches BOTH the runner and config resolution: it selects the
	// read-only resolver alongside ReadOnly, because a preview that creates the
	// per-workspace store has already written before the runner sees anything.
	// The fields stay separate because their meanings do: `dot peer status` is
	// read-only with a live runner.
	DryRun bool

	// Verbose becomes Config.Verbose, which turns rsync's output on.
	Verbose bool

	// FilterMode overrides the resolved profile's filter mode for this run,
	// but only when FilterModeSet is true. It is the raw flag string rather
	// than a parsed FilterMode so the parse error keeps its `--filter-mode:`
	// wording on this side of the seam.
	FilterMode    string
	FilterModeSet bool

	// Home is the raw --home override, empty for the process home. It reaches
	// state loading and profile resolution together: a run pointed at another
	// user's home must read that user's state file and resolve that user's
	// workspace paths, never the invoking user's (BUG-07).
	Home string
}

// BootstrapResult is what every sync and peer subcommand needs before it can
// do anything: the loaded user state, the resolved profile config, and the
// runner external commands go through.
//
// Runner covers command execution only, and its DryRun flag suppresses only
// that. It is NOT a blanket dry-run guarantee: engine entries that mutate files
// directly — PeerInit via SaveLocalConfig, PeerSchedule via os.MkdirAll and
// os.WriteFile — bypass it entirely, so each of those carries its own dry-run
// arm and returns a flag the caller renders. A caller must not read a dry-run
// runner as "nothing was written".
//
// Profile resolution is no longer among the things that slip past it: Bootstrap
// takes the read-only resolver under DryRun.
type BootstrapResult struct {
	State  *config.UserState
	Config *Config
	Runner *exec.Runner
}

// Bootstrap loads state, resolves the requested profile and builds the runner
// the caller's side effects go through.
func Bootstrap(opts BootstrapOptions) (*BootstrapResult, error) {
	var state *config.UserState
	var err error
	if opts.Home != "" {
		state, err = config.LoadStateForHome(opts.Home)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}
	resolve := ResolveConfigForHomeProfile
	if opts.ReadOnly || opts.DryRun {
		resolve = ResolveConfigReadOnlyForHomeProfile
	}
	cfg, err := resolve(state, opts.Home, opts.Profile)
	if err != nil {
		return nil, err
	}
	if opts.FilterModeSet {
		mode, err := ParseFilterMode(opts.FilterMode)
		if err != nil {
			return nil, fmt.Errorf("--filter-mode: %w", err)
		}
		cfg.FilterMode = mode
	}
	cfg.Verbose = opts.Verbose
	// exec.Runner dereferences its logger, so nil panics on the first Info call.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return &BootstrapResult{State: state, Config: cfg, Runner: exec.NewRunner(opts.DryRun, logger)}, nil
}
