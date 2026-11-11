package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every tree and name a remote host carries must be named in
// docs/BOUNDARIES.md, and nothing may land outside the remote $HOME or the
// host's prefix.
func TestRemoteWritesAreInBoundaries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "BOUNDARIES.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	host := testHost()
	host.Root = DefaultRoot
	host.GitRoot = DefaultGitRoot
	host.SrcRoot = DefaultSrcRoot
	// A built image is the prefixed name; a pulled one is the operator's own
	// ref, which the document names as image.ref.
	host.Ref = ""
	host.Dockerfile = "/home/u/images/dev.Dockerfile"

	writes := RemoteWrites(host)
	if len(writes) != 7 {
		t.Fatalf("RemoteWrites = %v, want the three trees, the record directory, the image, a volume and a container", writes)
	}
	for _, write := range writes {
		underHome := strings.HasPrefix(write, "$HOME/")
		underPrefix := strings.HasPrefix(write, host.Prefix+"-") || strings.HasPrefix(write, host.Prefix+":")
		if !underHome && !underPrefix {
			t.Errorf("%q is neither under the remote $HOME nor under the prefix", write)
		}
		// The trees are named with the ~ form the document uses.
		if underHome {
			tilde := "~/" + strings.TrimPrefix(write, "$HOME/")
			if !strings.Contains(doc, "`"+tilde+"`") {
				t.Errorf("docs/BOUNDARIES.md does not name %s", tilde)
			}
		}
	}
	for _, want := range []string{
		"`<prefix>:latest`",
		"`image.ref`",
		"`<prefix>-<cache>`",
		"`<prefix>-<mirror key>`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/BOUNDARIES.md does not name %s", want)
		}
	}
}
