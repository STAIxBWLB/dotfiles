package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
)

func familyIDs() string {
	var ids []string
	for _, t := range aitooling.FamilyTools() {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}

func newAIFamilyCmd() *cobra.Command {
	c := &cobra.Command{Use: "family", Short: "Install agent-hub family tools side by side, one directory per version", Long: `Install ` + familyIDs() + ` side by side.

Each version gets its own npm prefix under ~/.local/share/dotfiles/tools/<id>/<version>/.
A 'current' link names the active version and 'previous' the one before it;
~/.local/bin/<bin> points through 'current'. Activation is one rename: new
sessions start the new version, running processes keep the files they started
with and are listed, and nothing is stopped. Versions other than current and
previous are removed once no running process uses them. 'dot ai rollback <id>'
makes previous current again.

For ahub this is the global CLI only: 'ahub upgrade' still runs each project's
daemon from ~/.agenthub/releases. After ahub activates, run the printed
'ahub setup' to move the shared Claude plugin to the same version.`}
	install := &cobra.Command{Use: "install <id>... | --all", Short: "Install family tools and make each one current", Long: `Install each named family tool, or every one with --all, and make it current.

Tools are installed one after another; a failure on one does not stop the
others, and the command exits non-zero naming each tool that failed.`, RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetString("version")
		adopt, _ := cmd.Flags().GetBool("adopt")
		if all, _ := cmd.Flags().GetBool("all"); all == (len(args) > 0) {
			return fmt.Errorf("name one or more of %s, or pass --all", familyIDs())
		} else if all {
			for _, t := range aitooling.FamilyTools() {
				args = append(args, t.ID)
			}
		}
		if version != "" && len(args) != 1 {
			return errors.New("--version needs exactly one id")
		}
		e := familyEngine(cmd)
		return familyBatch(cmd, "ai.family.install", args, func(id string) (aitooling.FamilyResult, error) {
			return e.FamilyInstall(cmd.Context(), id, version, adopt)
		})
	}}
	install.Flags().String("version", "", "Exact X.Y.Z version to install, with one id (default: the registry's latest)")
	install.Flags().Bool("all", false, "Install every family tool")
	install.Flags().Bool("adopt", false, "Repoint an existing ~/.local/bin/<bin> symlink from another install; that install is left in place")
	update := &cobra.Command{Use: "update [id...]", Short: "Move installed family tools to the registry's latest version", Long: `Move each named family tool, or with no ids every installed one, to the
registry's latest version. A tool that is not installed is reported and
skipped. With --dry-run nothing is written: each tool is shown as already
current or with the version it would move to.`, RunE: func(cmd *cobra.Command, args []string) error {
		adopt, _ := cmd.Flags().GetBool("adopt")
		e := familyEngine(cmd)
		installed := map[string]bool{}
		var current []string
		for _, r := range e.FamilyStatus() {
			if r.Current != "" {
				installed[r.ID] = true
				current = append(current, r.ID)
			}
		}
		if len(args) == 0 {
			args = current
		}
		if len(args) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no family tool is installed; run 'dot ai family install --all' or name one")
			return nil
		}
		return familyBatch(cmd, "ai.family.update", args, func(id string) (aitooling.FamilyResult, error) {
			if !installed[id] && familyKnown(id) {
				return aitooling.FamilyResult{ID: id, Status: "not-installed", Notes: []string{"skipped; install it with: dot ai family install " + id}}, nil
			}
			return e.FamilyInstall(cmd.Context(), id, "", adopt)
		})
	}}
	update.Flags().Bool("adopt", false, "Repoint an existing ~/.local/bin/<bin> symlink from another install; that install is left in place")
	status := &cobra.Command{Use: "status", Short: "Show each family tool's current and previous versions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		for _, r := range familyEngine(cmd).FamilyStatus() {
			printFamily(cmd, r)
		}
		return nil
	}}
	c.AddCommand(install, update, status)
	return c
}

func newAIRollbackCmd() *cobra.Command {
	return &cobra.Command{Use: "rollback <id>", Short: "Make a family tool's previous version current again", Long: `Make the previous version of a family tool (` + familyIDs() + `) current again.

Running processes are listed and keep their files; new sessions start the
restored version. The version it leaves becomes 'previous'.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := familyEngine(cmd).FamilyRollback(cmd.Context(), args[0])
		return finishFamily(cmd, "ai.family.rollback", args[0], r, err)
	}}
}

func familyEngine(cmd *cobra.Command) *aitooling.Engine {
	dry, _ := cmd.Flags().GetBool("dry-run")
	over, _ := cmd.Flags().GetString("home")
	return aitooling.New(aitooling.Options{HomeDir: homeFromCmd(cmd), ExplicitHome: over != "", DryRun: dry, Out: cmd.ErrOrStderr()})
}

// familyBatch runs do for each id in turn and joins the failures, so one tool never stops the rest.
func familyBatch(cmd *cobra.Command, event string, ids []string, do func(string) (aitooling.FamilyResult, error)) error {
	var errs []error
	for _, id := range ids {
		r, err := do(id)
		if err := finishFamily(cmd, event, id, r, err); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func familyKnown(id string) bool {
	for _, t := range aitooling.FamilyTools() {
		if t.ID == id {
			return true
		}
	}
	return false
}

func finishFamily(cmd *cobra.Command, event, id string, r aitooling.FamilyResult, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	printFamily(cmd, r)
	if r.Status == "activated" || r.Status == "active" {
		auditAIEventBestEffort(cmd, event, map[string]any{"id": r.ID, "current": r.Current, "previous": r.Previous})
	}
	if strings.HasPrefix(r.Status, "deferred") {
		return fmt.Errorf("%s: %s", id, r.Status)
	}
	return nil
}

func printFamily(cmd *cobra.Command, r aitooling.FamilyResult) {
	line := r.ID + ": " + r.Status
	if r.Current != "" {
		line += " current " + r.Current
	}
	if r.Previous != "" {
		line += ", previous " + r.Previous
	}
	if len(r.Versions) > 0 {
		line += " (installed: " + strings.Join(r.Versions, ", ") + ")"
	}
	fmt.Fprintln(cmd.OutOrStdout(), line)
	for _, n := range r.Notes {
		fmt.Fprintln(cmd.OutOrStdout(), "  "+n)
	}
}
