// Command gendocs regenerates docs/commands/ from the live cobra tree.
//
// It is a build-time tool only: importing github.com/spf13/cobra/doc pulls the
// man-page toolchain (go-md2man, blackfriday) into this package's build graph,
// which is why it lives in its own main package instead of anywhere reachable
// from ./cmd/dot. The lint job asserts that separation on every run.
//
// Usage: go run ./tools/gendocs <output-dir>   (see the Makefile's docs target)
package main

import (
	"log"
	"os"

	"github.com/spf13/cobra/doc"

	"github.com/STAIxBWLB/dotfiles/internal/cli"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: gendocs <output-dir>")
	}
	out := os.Args[1]

	// GenMarkdownTree calls os.Create per command and never deletes, so a
	// renamed or removed command would leave an orphan page that is already
	// committed and the drift check would pass on a wrong tree. Clean first.
	if err := os.RemoveAll(out); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}

	// Fixed literals, never ldflags or VCS-derived values: ResolveVersion falls
	// back to debug.ReadBuildInfo() when commit is "none", which would make the
	// output depend on the generating checkout.
	root := cli.NewRootCmd("dev", "none")
	// The auto-generation date tag is the one real drift source; the flag
	// propagates to children through cobra's parent visiting.
	root.DisableAutoGenTag = true

	if err := doc.GenMarkdownTree(root, out); err != nil {
		log.Fatal(err)
	}
}
