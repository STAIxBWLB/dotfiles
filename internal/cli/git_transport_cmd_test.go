package cli

import (
	"context"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/gittransport"
)

// goldenGitTransportFixture pins both probes: SSH reset, HTTPS usable, so the
// golden records a switch from no mode file to https mode without network.
func goldenGitTransportFixture(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	orig := newGitTransportProber
	newGitTransportProber = func() *gittransport.Prober {
		return &gittransport.Prober{
			SSH: func(context.Context) string {
				return "kex_exchange_identification: read: Connection reset by peer\nConnection reset by 192.0.2.1 port 22\n"
			},
			HTTPS: func(context.Context) gittransport.Result {
				return gittransport.Result{Usable: true, Detail: "reachable, credential available"}
			},
		}
	}
	t.Cleanup(func() { newGitTransportProber = orig })
	return home, t.TempDir()
}
