package cli

import (
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
	install := &cobra.Command{Use: "install <id>", Short: "Install a version of a family tool and make it current", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetString("version")
		adopt, _ := cmd.Flags().GetBool("adopt")
		r, err := familyEngine(cmd).FamilyInstall(cmd.Context(), args[0], version, adopt)
		return finishFamily(cmd, "ai.family.install", r, err)
	}}
	install.Flags().String("version", "", "Exact X.Y.Z version to install (default: the registry's latest)")
	install.Flags().Bool("adopt", false, "Repoint an existing ~/.local/bin/<bin> symlink from another install; that install is left in place")
	status := &cobra.Command{Use: "status", Short: "Show each family tool's current and previous versions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		for _, r := range familyEngine(cmd).FamilyStatus() {
			printFamily(cmd, r)
		}
		return nil
	}}
	c.AddCommand(install, status)
	return c
}

func newAIRollbackCmd() *cobra.Command {
	return &cobra.Command{Use: "rollback <id>", Short: "Make a family tool's previous version current again", Long: `Make the previous version of a family tool (` + familyIDs() + `) current again.

Running processes are listed and keep their files; new sessions start the
restored version. The version it leaves becomes 'previous'.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := familyEngine(cmd).FamilyRollback(cmd.Context(), args[0])
		return finishFamily(cmd, "ai.family.rollback", r, err)
	}}
}

func familyEngine(cmd *cobra.Command) *aitooling.Engine {
	dry, _ := cmd.Flags().GetBool("dry-run")
	over, _ := cmd.Flags().GetString("home")
	return aitooling.New(aitooling.Options{HomeDir: homeFromCmd(cmd), ExplicitHome: over != "", DryRun: dry, Out: cmd.ErrOrStderr()})
}

func finishFamily(cmd *cobra.Command, event string, r aitooling.FamilyResult, err error) error {
	if err != nil {
		return err
	}
	printFamily(cmd, r)
	if r.Status == "activated" || r.Status == "active" {
		auditAIEventBestEffort(cmd, event, map[string]any{"id": r.ID, "current": r.Current, "previous": r.Previous})
	}
	if strings.HasPrefix(r.Status, "deferred") {
		return fmt.Errorf("%s: %s", r.ID, r.Status)
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
