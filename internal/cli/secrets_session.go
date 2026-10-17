package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
)

// secretsSession is the home and store directory one `dot secrets` invocation
// operates on. Resolving them in one place is what keeps --home from reaching
// some subcommands and not others (BUG-08).
//
// State is deliberately NOT a field: it is loaded at the point of need, so a
// malformed config.yaml cannot stop `dot secrets backup <dest>` from copying
// the archives out. That is the recovery case where getting them out matters
// most, and an explicit destination needs no state to name it.
type secretsSession struct {
	// Home is the resolved target home: the override when one was given,
	// the process home otherwise.
	Home string
	// override is the raw --home / $DOTFILES_HOME value, empty for the
	// process home. It selects the state loader and saver, so a run under
	// the flag reads and records in the target's state file.
	override string
	// StoreDir is the encrypted store, joined from the store-relative
	// constant against Home. That is the same spelling the one-stop backup
	// and restore steps use (onestop.go:162), which is why they honored the
	// flag while these subcommands did not.
	StoreDir string
}

// secretsSessionFrom resolves the session for one command run: the flag, then
// $DOTFILES_HOME, then the process home. The override outranks XDG_CONFIG_HOME,
// following config.StatePathForHome and the apply/check/diff precedent.
func secretsSessionFrom(cmd *cobra.Command) (*secretsSession, error) {
	s := &secretsSession{override: homeOverrideFrom(cmd)}
	s.Home = s.override
	if s.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		s.Home = home
	}
	s.StoreDir = filepath.Join(s.Home, secrets.StoreDirRel)
	return s, nil
}

// loadState reads the target home's state file. Backup re-reads after copying
// so a concurrent edit is not clobbered by the last-backup record.
func (s *secretsSession) loadState() (*config.UserState, error) {
	if s.override != "" {
		return config.LoadStateForHome(s.override)
	}
	return config.LoadState()
}

// requireState is loadState for the subcommands that cannot proceed without
// it, keeping the wording those four RunE bodies have always returned.
func (s *secretsSession) requireState() (*config.UserState, error) {
	state, err := s.loadState()
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}
	return state, nil
}

// saveState writes state back to the target home.
func (s *secretsSession) saveState(state *config.UserState) error {
	if s.override != "" {
		return config.SaveStateForHome(s.override, state)
	}
	return config.SaveState(state)
}
