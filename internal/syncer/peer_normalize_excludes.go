package syncer

import (
	"encoding/json"
	"fmt"
)

// PeerNormalizeExcludesEnv carries the coordinator's filter context to the
// remote `dot sync names normalize` (#279): the run's worktree union and its
// own allow and exclude lines. An older dot ignores the variable.
const PeerNormalizeExcludesEnv = "DOT_PEER_NORMALIZE_EXCLUDES"

type peerNormalizeExcludes struct {
	Worktrees []string `json:"worktrees"`
	Allows    []string `json:"allows"`
	Excludes  []string `json:"excludes"`
}

// encodePeerNormalizeExcludes is the coordinator side: the worktree union
// already merged into cfg, its allow.txt lines, and the raw pattern lines of
// its excludes file and ignore.txt.
func encodePeerNormalizeExcludes(cfg *Config) (string, error) {
	payload := peerNormalizeExcludes{Worktrees: cfg.WorktreeExcludes, Allows: cfg.AllowPatterns}
	for _, path := range []string{cfg.ExcludesFile, cfg.IgnoreFile} {
		patterns, err := loadExcludeFile(path, "")
		if err != nil {
			return "", err
		}
		for _, p := range patterns {
			payload.Excludes = append(payload.Excludes, p.raw)
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ApplyPeerNormalizeExcludes is the remote side. The coordinator's inventory
// decides which names must be NFD, so this walk uses the coordinator's allow
// and exclude layers in their order instead of this Mac's own (a mix would
// let one side's allow line undo the other side's exclude). Worktrees join
// the stored set: that exclude is symmetric by design (#135). The code-owned
// layers stay as newSyncFilter builds them.
func ApplyPeerNormalizeExcludes(cfg *Config, value string) error {
	var payload peerNormalizeExcludes
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return fmt.Errorf("%s: %w", PeerNormalizeExcludesEnv, err)
	}
	cfg.WorktreeExcludes = append(cfg.WorktreeExcludes, payload.Worktrees...)
	cfg.AllowPatterns = payload.Allows
	cfg.ExcludesFile, cfg.IgnoreFile = "", ""
	cfg.ExtraExcludes = payload.Excludes
	return nil
}
