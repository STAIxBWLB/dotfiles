package aisettings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Keeping claude-mem at the latest marketplace version on every synced
// machine: refresh the marketplace checkout, compare the recorded install,
// update the Claude plugin when behind, and re-materialize the codex cache.

// ClaudeMemMarketplace is the marketplace name and checkout directory the
// thedotmack claude-mem plugin ships from.
const ClaudeMemMarketplace = "thedotmack"

// ClaudeMemUpdateResult reports one update pass.
type ClaudeMemUpdateResult struct {
	Before             string
	After              string
	MarketplaceVersion string
	Updated            bool
	CodexCacheRefresh  string // what happened to the codex cache, if anything
}

// MarketplaceCheckoutPath is the marketplace checkout the plugin.json
// version is read from.
func (m *ClaudeMemManager) MarketplaceCheckoutPath() string {
	return filepath.Join(m.HomeDir, ".claude", "plugins", "marketplaces", ClaudeMemMarketplace)
}

// MarketplacePluginVersion reads the marketplace checkout's plugin version.
func (m *ClaudeMemManager) MarketplacePluginVersion() (string, error) {
	var manifest struct {
		Version string `json:"version"`
	}
	path := filepath.Join(m.MarketplaceCheckoutPath(), "plugin", ".claude-plugin", "plugin.json")
	if !readJSONFile(path, &manifest) || manifest.Version == "" {
		return "", fmt.Errorf("cannot read claude-mem marketplace version from %s", path)
	}
	return manifest.Version, nil
}

// InstalledClaudeMemVersion is the version Claude Code records for the
// active claude-mem install from the thedotmack marketplace, preferring the
// user-scoped install the updater owns ("" when not recorded). Map
// iteration order must not pick a project-scoped or foreign-marketplace
// record: the update path always targets claude-mem@thedotmack.
func (m *ClaudeMemManager) InstalledClaudeMemVersion() string {
	raw, err := os.ReadFile(filepath.Join(m.HomeDir, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return ""
	}
	var doc struct {
		Plugins map[string][]struct {
			Scope   string `json:"scope"`
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	installs := doc.Plugins["claude-mem@"+ClaudeMemMarketplace]
	for _, in := range installs {
		if in.Scope == "user" {
			return in.Version
		}
	}
	if len(installs) > 0 {
		return installs[0].Version
	}
	return ""
}

// RefreshClaudeMemMarketplace pulls the marketplace checkout current,
// preferring the claude CLI and falling back to git when it is unavailable.
func RefreshClaudeMemMarketplace(ctx context.Context, runner *exec.Runner, checkout string) error {
	if runner.CommandExists("claude") {
		if _, err := runner.Run(ctx, "claude", "plugin", "marketplace", "update", ClaudeMemMarketplace); err != nil {
			return fmt.Errorf("claude plugin marketplace update %s: %w", ClaudeMemMarketplace, err)
		}
		return nil
	}
	if _, err := runner.Run(ctx, "git", "-C", checkout, "pull", "--ff-only"); err != nil {
		return fmt.Errorf("git pull in %s (claude CLI unavailable): %w", checkout, err)
	}
	return nil
}

// UpdateClaudeMemPlugin runs one keep-latest pass. Idempotent: an install
// already at the marketplace version only refreshes the checkout and the
// codex cache runtime. runner.DryRun probes but changes nothing.
// nonInteractive passes -y to `claude plugin update`, which refuses to run
// otherwise when stdin/stdout is not a TTY (the scheduled sync run).
func (m *ClaudeMemManager) UpdateClaudeMemPlugin(ctx context.Context, runner *exec.Runner, nonInteractive bool) (*ClaudeMemUpdateResult, error) {
	result := &ClaudeMemUpdateResult{Before: m.InstalledClaudeMemVersion()}
	if err := RefreshClaudeMemMarketplace(ctx, runner, m.MarketplaceCheckoutPath()); err != nil {
		return nil, err
	}
	market, err := m.MarketplacePluginVersion()
	if err != nil {
		return nil, err
	}
	result.MarketplaceVersion = market

	if result.Before == "" {
		return nil, fmt.Errorf("claude-mem is not installed for Claude Code; install it first (marketplace has %s)", market)
	}
	if compareVersions(result.Before, market) < 0 {
		if runner.DryRun {
			result.After = result.Before
		} else {
			args := []string{"plugin", "update", "claude-mem@" + ClaudeMemMarketplace, "-s", "user"}
			if nonInteractive {
				args = append(args, "-y")
			}
			if _, err := runner.Run(ctx, "claude", args...); err != nil {
				return nil, fmt.Errorf("claude plugin update claude-mem@%s: %w", ClaudeMemMarketplace, err)
			}
			result.After = m.InstalledClaudeMemVersion()
			if result.After == "" {
				result.After = market
			}
		}
		result.Updated = true
	} else {
		result.After = result.Before
	}

	// The codex cache snapshots the marketplace checkout, so a plugin bump
	// leaves it behind; reinstall it and re-materialize its bun runtime.
	if cache, _ := CodexClaudeMemCache(m.HomeDir); cache != "" {
		switch {
		case runner.DryRun:
			result.CodexCacheRefresh = "dry-run: would reinstall and bun-install " + cache
		case result.Updated && !runner.CommandExists("codex"):
			// Visibility, not silence: the cache now runs the OLD plugin code
			// while Claude Code runs the new one, and the same-version rule
			// the peer sync relies on is broken until codex is back.
			result.CodexCacheRefresh = "STALE: plugin updated but codex CLI not in PATH; refresh later with: " + claudeMemReinstallCommand
		case result.Updated:
			if _, err := runner.Run(ctx, "codex", "plugin", "remove", "claude-mem", "--marketplace", "claude-mem-local"); err != nil {
				return result, fmt.Errorf("codex plugin remove claude-mem: %w", err)
			}
			if _, err := runner.Run(ctx, "codex", "plugin", "add", "claude-mem@claude-mem-local"); err != nil {
				return result, fmt.Errorf("codex plugin add claude-mem@claude-mem-local: %w", err)
			}
			if _, err := m.EnsureCodexCacheRuntime(ctx); err != nil {
				return result, fmt.Errorf("codex cache runtime: %w", err)
			}
			result.CodexCacheRefresh = "reinstalled " + cache
		default:
			if _, err := m.EnsureCodexCacheRuntime(ctx); err != nil {
				result.CodexCacheRefresh = "runtime repair failed: " + err.Error()
			} else {
				result.CodexCacheRefresh = "runtime ok"
			}
		}
	}
	return result, nil
}

// compareVersions orders dotted numeric versions: -1 a<b, 0 equal, 1 a>b.
// String equality would call 13.9.0 newer than 13.28.0.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		na, nb := 0, 0
		if i < len(pa) {
			na, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		}
	}
	return 0
}
