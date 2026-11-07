package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// AhubPeer is one peer slot of the ahub layouts: the peer id shown on the pane
// and the arguments after `ahub --project <root>` that start it.
type AhubPeer struct {
	ID   string
	Args []string
}

// ahubKinds are the peer kinds ahub starts by name (`ahub <kind>`).
var ahubKinds = map[string]bool{"claude": true, "codex": true, "kimi": true, "jcode": true, "qwen": true, "pi": true, "local": true}

// AhubPeers picks the two peer slots, in order, from start.agents in the
// project's untracked .agenthub/config.local.json, then codex and claude. An id
// is used only when it is a kind above or an instance declared with one of
// those kinds (`ahub <kind> --as <id>`); anything else is skipped. Nothing read
// here is run: the file only chooses among these fixed launch forms (#331).
func AhubPeers(root string) []AhubPeer {
	var cfg struct {
		Start struct {
			Agents []string `json:"agents"`
		} `json:"start"`
		Instances map[string]struct {
			Kind string `json:"kind"`
		} `json:"instances"`
	}
	if raw, err := os.ReadFile(filepath.Join(root, ".agenthub", "config.local.json")); err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	var peers []AhubPeer
	seen := map[string]bool{}
	for _, id := range append(cfg.Start.Agents, "codex", "claude") {
		if len(peers) == 2 || seen[id] || !validSessionName.MatchString(id) {
			continue
		}
		switch kind := cfg.Instances[id].Kind; {
		case ahubKinds[id]:
			peers = append(peers, AhubPeer{ID: id, Args: []string{id}})
		case ahubKinds[kind]:
			peers = append(peers, AhubPeer{ID: id, Args: []string{kind, "--as", id}})
		default:
			continue
		}
		seen[id] = true
	}
	return peers
}

// AhubPeerEnv passes the slots to the layout scripts. Every token matches
// validSessionName, so the scripts split the args on spaces safely.
func AhubPeerEnv(peers []AhubPeer) []string {
	var env []string
	for i, p := range peers {
		slot := string(rune('A' + i))
		env = append(env, "DOT_AHUB_PEER_"+slot+"_ID="+p.ID, "DOT_AHUB_PEER_"+slot+"_ARGS="+strings.Join(p.Args, " "))
	}
	return env
}

// IsAhubLayout reports whether a layout is one of the agent-hub layouts.
func IsAhubLayout(name string) bool { return name == "ahub" || name == "ahub-compact" }
