package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every `dot ...` the welcome screen and `dot usecase` teach must resolve to a
// live, non-deprecated command under its canonical name, with flags it has
// (#372). An alias such as `dot gsync` fails because the command path differs.
func TestWelcomeNamesLiveCommands(t *testing.T) {
	var all []usecase
	all = append(all, welcomeCommands...)
	for _, sec := range useSections {
		all = append(all, sec.cases...)
	}
	checked := 0
	for _, uc := range all {
		for _, part := range strings.Split(uc.cmd, " && ") {
			part = strings.TrimSpace(part)
			if !strings.HasPrefix(part, "dot ") {
				continue
			}
			fields := strings.Fields(part)[1:]
			var path, flags []string
			for _, f := range fields {
				if f == "--" { // the rest is the wrapped command
					break
				}
				switch {
				case strings.HasPrefix(f, "--"):
					flags = append(flags, strings.SplitN(strings.TrimPrefix(f, "--"), "=", 2)[0])
				case len(flags) == 0 && !strings.HasPrefix(f, "<") && !strings.HasPrefix(f, "-"):
					path = append(path, f)
				}
			}
			root := NewRootCmd("dev", "test")
			c, _, err := root.Find(path)
			if err != nil || c == root {
				t.Errorf("%q: no such command (%v)", part, err)
				continue
			}
			if c.Deprecated != "" {
				t.Errorf("%q: %s is deprecated: %s", part, c.CommandPath(), c.Deprecated)
			}
			if !strings.HasPrefix(part, c.CommandPath()) {
				t.Errorf("%q: names %s through an alias", part, c.CommandPath())
			}
			for _, name := range flags {
				if c.Flags().Lookup(name) == nil && c.InheritedFlags().Lookup(name) == nil {
					t.Errorf("%q: %s has no --%s flag", part, c.CommandPath(), name)
				}
			}
			checked++
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d commands; the welcome data was not read", checked)
	}
}

// Sibling commands never share a Short: a copied Short hides what each does (#372).
func TestSiblingShortsAreUnique(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		seen := map[string]string{}
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Deprecated != "" {
				continue
			}
			if prev, ok := seen[sub.Short]; ok {
				t.Errorf("%s and %s share the Short %q", prev, sub.CommandPath(), sub.Short)
			}
			seen[sub.Short] = sub.CommandPath()
			walk(sub)
		}
	}
	walk(NewRootCmd("dev", "test"))
}
