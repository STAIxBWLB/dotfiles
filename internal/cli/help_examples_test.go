package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// commandKeys returns every command path below root, without the root name.
func commandKeys(root *cobra.Command) map[string]bool {
	keys := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		keys[strings.TrimPrefix(strings.TrimPrefix(c.CommandPath(), root.CommandPath()), " ")] = true
		for _, sc := range c.Commands() {
			walk(sc)
		}
	}
	walk(root)
	return keys
}

// A key that names no command is silently ignored by the apply functions, so
// a rename would leave a dead entry behind. These tests catch it.
func TestHelpExampleKeysResolve(t *testing.T) {
	keys := commandKeys(NewRootCmd("test", "test"))
	for key := range helpExamples {
		if !keys[key] {
			t.Errorf("helpExamples key %q matches no command", key)
		}
	}
	for key := range generatedHelp {
		if !keys[key] {
			t.Errorf("generatedHelp key %q matches no command", key)
		}
	}
}

func TestAIExampleKeysResolve(t *testing.T) {
	keys := commandKeys(newAICmd())
	for key := range aiExamples {
		if !keys[key] {
			t.Errorf("aiExamples key %q matches no command under dot ai", key)
		}
	}
}
