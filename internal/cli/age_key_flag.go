package cli

import (
	"github.com/spf13/cobra"
)

// addAgeKeyFlag registers --include-age-key, which copies the plain
// ~/.ssh/age_key* identity. It is not the `secrets` scope, which means the
// encrypted .age files (#377).
func addAgeKeyFlag(c *cobra.Command, usage string) {
	c.Flags().Bool("include-age-key", false, usage)
}

// ageKeyFlag reports whether --include-age-key is set.
func ageKeyFlag(cmd *cobra.Command) bool {
	include, _ := cmd.Flags().GetBool("include-age-key")
	return include
}
