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
		"`<prefix>-verify-<mirror key>`",
		"`<prefix>-src-<name>`",
		"`refs/verify/<mirror key>/<sha>-<nonce>`",
		"`<jobs root>/<name>`",
		"`<prefix>-<profile>:latest`",
		"`<prefix>-job-<profile>_<name>`",
		"`<local.root>/<name>`",
		"`~/dot-remote-jobs/<profile>`",
		"`~/.local/state/dot/remote-jobs/<profile>/<name>`",
		"`remote-job:<host>/<profile>`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/BOUNDARIES.md does not name %s", want)
		}
	}
	// The names the code builds must match the shapes the document spells.
	if got := host.VerifyContainer("a"); got != host.Prefix+"-verify-"+MirrorKey("a") {
		t.Errorf("VerifyContainer = %q", got)
	}
	if got := host.SrcContainer("lib"); got != host.Prefix+"-src-lib" {
		t.Errorf("SrcContainer = %q", got)
	}
	if got := VerifyRef("a", "abc", "n"); got != "refs/verify/"+MirrorKey("a")+"/abc-n" {
		t.Errorf("VerifyRef = %q", got)
	}
	// The sent listing is what a run writes on this machine, so the document
	// names that too, and a rename has to reach it.
	p := JobProfile{Name: "media", Dockerfile: "/x"}
	if got := p.ImageHost(host).Image(); got != host.Prefix+"-media:latest" {
		t.Errorf("profile image = %q", got)
	}
	if got := JobContainer(host, p, "j"); got != host.Prefix+"-job-media_j" {
		t.Errorf("job container = %q", got)
	}
	if got := JobScope(host, "media"); got != "remote-job:"+host.Name+"/media" {
		t.Errorf("job scope = %q", got)
	}
	if DefaultJobsRoot != "dot-remote-jobs" || DefaultLocalJobsRoot != ".local/state/dot/remote-jobs" {
		t.Error("job root defaults drifted from docs/BOUNDARIES.md")
	}
	if !strings.Contains(doc, "`~/.local/state/dot/remote-sent/<host>--<mirror key>.sent`") {
		t.Error("docs/BOUNDARIES.md does not name the sent listing a run keeps on this machine")
	}
}
