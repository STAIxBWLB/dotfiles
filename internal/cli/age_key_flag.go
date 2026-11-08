package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// addAgeKeyFlag registers --include-age-key and, for one release, the old
// --include-secrets name. The old name collided with the `secrets` scope,
// which means the encrypted .age files, while this flag copies the plain
// ~/.ssh/age_key* identity (#377).
func addAgeKeyFlag(c *cobra.Command, usage string) {
	c.Flags().Bool("include-age-key", false, usage)
	c.Flags().Bool("include-secrets", false, "Deprecated: use --include-age-key")
	_ = c.Flags().MarkHidden("include-secrets")
}

// ageKeyFlag reports whether either name is set, warning on stderr (not via
// cobra's deprecation, which writes to stdout) when the old one is used.
func ageKeyFlag(cmd *cobra.Command) bool {
	include, _ := cmd.Flags().GetBool("include-age-key")
	if cmd.Flags().Changed("include-secrets") {
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --include-secrets is deprecated; use '--include-age-key' instead. The old flag will be removed after one release.")
		old, _ := cmd.Flags().GetBool("include-secrets")
		include = include || old
	}
	return include
}
