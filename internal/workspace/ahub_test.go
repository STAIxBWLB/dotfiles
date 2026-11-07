package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAhubPeers(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		want         []AhubPeer
	}{
		{"no config falls back to codex then claude", "", []AhubPeer{{"codex", []string{"codex"}}, {"claude", []string{"claude"}}}},
		{"start.agents order, first two", `{"start":{"agents":["claude","kimi","codex"]}}`, []AhubPeer{{"claude", []string{"claude"}}, {"kimi", []string{"kimi"}}}},
		{"declared instance starts with --as", `{"start":{"agents":["codex-review"]},"instances":{"codex-review":{"kind":"codex"}}}`, []AhubPeer{{"codex-review", []string{"codex", "--as", "codex-review"}}, {"codex", []string{"codex"}}}},
		{"unknown and unsafe ids are skipped", `{"start":{"agents":["agy","x; rm -rf /","odd"]},"instances":{"odd":{"kind":"bash"}}}`, []AhubPeer{{"codex", []string{"codex"}}, {"claude", []string{"claude"}}}},
		{"malformed file falls back", `{`, []AhubPeer{{"codex", []string{"codex"}}, {"claude", []string{"claude"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.config != "" {
				if err := os.MkdirAll(filepath.Join(root, ".agenthub"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".agenthub", "config.local.json"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := AhubPeers(root); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	env := AhubPeerEnv([]AhubPeer{{"codex-review", []string{"codex", "--as", "codex-review"}}})
	if want := []string{"DOT_AHUB_PEER_A_ID=codex-review", "DOT_AHUB_PEER_A_ARGS=codex --as codex-review"}; !reflect.DeepEqual(env, want) {
		t.Fatalf("env %v, want %v", env, want)
	}
}
