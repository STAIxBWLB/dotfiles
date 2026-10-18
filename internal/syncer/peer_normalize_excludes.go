package syncer

import (
	"encoding/json"
	"fmt"
)

// PeerNormalizeExcludesEnv carries the coordinator's filter context to the
// remote `dot sync names normalize` (#279): the run's worktree union and its
// own exclude and ignore.txt lines. An older dot ignores the variable.
const PeerNormalizeExcludesEnv = "DOT_PEER_NORMALIZE_EXCLUDES"

type peerNormalizeExcludes struct {
	Worktrees []string `json:"worktrees"`
	Excludes  []string `json:"excludes"`
}

// encodePeerNormalizeExcludes is the coordinator side: the worktree union
// already merged into cfg and the raw pattern lines of both exclude files.
func encodePeerNormalizeExcludes(cfg *Config) (string, error) {
	payload := peerNormalizeExcludes{Worktrees: cfg.WorktreeExcludes}
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

// ApplyPeerNormalizeExcludes is the remote side: it adds the coordinator's
// worktrees and patterns to this Mac's own filter. Union only: the walk never
// excludes less than this Mac's own config does.
func ApplyPeerNormalizeExcludes(cfg *Config, value string) error {
	var payload peerNormalizeExcludes
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return fmt.Errorf("%s: %w", PeerNormalizeExcludesEnv, err)
	}
	cfg.WorktreeExcludes = append(cfg.WorktreeExcludes, payload.Worktrees...)
	cfg.ExtraExcludes = append(cfg.ExtraExcludes, payload.Excludes...)
	return nil
}
