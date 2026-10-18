package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAIMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Manage shared claude-mem integration for Codex, Kimi, Kiro, Copilot, Qwen, and pi",
		Long: `Use one claude-mem store across Codex, Kimi Code, Kiro CLI, GitHub
Copilot CLI, Qwen Code, and pi.

Codex keeps the plugin's native lifecycle hooks. Kimi, Kiro, Copilot, and
Qwen receive the same MCP recall server plus a workspace-aware transcript
capture bridge. pi has no MCP support by design and joins the transcript
bridge only.`,
	}
	cmd.AddCommand(newAIMemoryInstallCmd())
	cmd.AddCommand(newAIMemoryStatusCmd())
	cmd.AddCommand(newAIMemorySyncCmd())
	cmd.AddCommand(newAIMemoryUpdateCmd())
	cmd.AddCommand(newAIMemoryMCPServerCmd())
	cmd.AddCommand(newAIMemoryBridgeCmd())
	return cmd
}

func newAIMemoryInstallCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "install",
		Short: "Prepare selected memory adapters or install the legacy integration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			forceAgents, _ := cmd.Flags().GetBool("force-agents")
			syncPeer, _ := cmd.Flags().GetString("peer")
			mgr, err := newClaudeMemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			if mgr.SelectedAgents != nil {
				if syncPeer != "" {
					return fmt.Errorf("peer bridge registration is not part of selected-agent preparation")
				}
				if dryRun {
					counts, e := mgr.BuildTranscriptConfigForDisplay()
					if e != nil {
						return e
					}
					p.Line("would prepare selected memory adapters and bridge watches only: %v (%v)", mgr.SelectedAgents, counts)
					return nil
				}
				result, e := mgr.PrepareSelectedIntegration()
				if e != nil {
					return e
				}
				p.Line("Prepared selected memory adapters: %v; transcript watches: %v", result.ConfigPaths, result.WatchCount)
				p.Line("Capture remains unverified; no memory worker was started or restarted. Native Claude/Codex hooks remain plugin-owned; Grok/OpenCode capture is unsupported.")
				return nil
			}
			if dryRun {
				config, err := mgr.BuildTranscriptConfigForDisplay()
				if err != nil {
					return err
				}
				p.Header("Claude-mem Integration")
				p.Line("would merge MCP config for Kimi, Kiro, Copilot, and Qwen")
				p.Line("would install %s", mgr.LaunchdPlistPath())
				if syncPeer != "" {
					p.Line("would install hourly peer sync agent %s (peer %s)", mgr.SyncLaunchdPlistPath(), syncPeer)
				}
				p.KV("Kimi sessions", fmt.Sprintf("%d", config["kimi"]))
				p.KV("Kiro sessions", fmt.Sprintf("%d", config["kiro"]))
				p.KV("Copilot sessions", fmt.Sprintf("%d", config["copilot"]))
				p.KV("Qwen sessions", fmt.Sprintf("%d", config["qwen"]))
				p.KV("pi sessions", fmt.Sprintf("%d", config["pi"]))
				if cache, runnable := aisettings.CodexClaudeMemCache(mgr.HomeDir); cache != "" && !runnable {
					p.Line("would run bun install --frozen-lockfile in %s (network access)", cache)
				}
				return nil
			}
			// Repair before the plugin gate: when the codex cache is the only
			// claude-mem copy, LocatePlugin only succeeds once its runtime exists.
			repairedCache, repairErr := mgr.EnsureCodexCacheRuntime(cmd.Context())
			if repairErr != nil {
				p.Warn("codex plugin cache repair failed: %v", repairErr)
			}
			if _, err := mgr.LocatePlugin(); err != nil {
				return err
			}

			agents := newAgentsManagerFromCmd(cmd)
			instructionsChanged, err := aisettings.EnsureMemoryInstructions(agents.SSOTPath())
			if err != nil {
				return err
			}
			apply, err := agents.Apply(aisettings.ApplyOptions{
				Tools: []string{"codex", "kimi", "pi", "qwen", "kiro", "copilot"}, Force: forceAgents,
			})
			if err != nil {
				return err
			}

			result, err := mgr.Install(cmd.Context())
			if err != nil {
				return err
			}
			if syncPeer != "" {
				if err := mgr.InstallSyncAgent(cmd.Context(), syncPeer); err != nil {
					return fmt.Errorf("installing peer sync agent: %w", err)
				}
			}
			auditAIEventBestEffort(cmd, "ai.memory.install", map[string]any{
				"bridge_path":          result.BridgePath,
				"config_paths":         result.ConfigPaths,
				"instructions_changed": instructionsChanged,
				"kimi_sessions":        result.WatchCount["kimi"],
				"kiro_sessions":        result.WatchCount["kiro"],
				"copilot_sessions":     result.WatchCount["copilot"],
				"qwen_sessions":        result.WatchCount["qwen"],
				"pi_sessions":          result.WatchCount["pi"],
			})

			p.Header("Claude-mem Integration")
			p.KV("Plugin", result.PluginRoot)
			p.KV("Bridge", result.BridgePath)
			if syncPeer != "" {
				p.KV("Peer sync", "hourly → "+syncPeer)
			}
			p.KV("Kimi sessions", fmt.Sprintf("%d", result.WatchCount["kimi"]))
			p.KV("Kiro sessions", fmt.Sprintf("%d", result.WatchCount["kiro"]))
			p.KV("Copilot sessions", fmt.Sprintf("%d", result.WatchCount["copilot"]))
			p.KV("Qwen sessions", fmt.Sprintf("%d", result.WatchCount["qwen"]))
			p.KV("pi sessions", fmt.Sprintf("%d", result.WatchCount["pi"]))
			if cache := firstNonEmpty(repairedCache, result.CodexCachePath); cache != "" {
				p.Line("Installed the codex plugin cache runtime at %s", cache)
			}
			if result.CodexCacheError != "" && repairErr == nil {
				p.Warn("codex plugin cache repair failed: %s", result.CodexCacheError)
			}
			if instructionsChanged {
				p.Line("Persistent-memory policy added to the agents SSOT.")
			}
			printAgentsApplyResult(p, apply)
			p.Success("Codex, Kimi, Kiro, Copilot, Qwen, and pi now share claude-mem")
			return nil
		},
	}
	c.Flags().Bool("force-agents", false, "Back up and overwrite externally edited Codex/Kimi/Kiro/Copilot/Qwen/pi instruction targets")
	c.Flags().String("peer", "", "ssh target for hourly claude-mem replication (installs the sync LaunchAgent)")
	return c
}

func newAIMemoryStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show claude-mem integration health for all six CLIs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := newClaudeMemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			ssot := newAgentsManagerFromCmd(cmd).SSOTPath()
			status := mgr.Status(cmd.Context(), ssot)
			p := printerFrom(cmd)
			p.Header("Claude-mem Status")
			plugin := status.PluginRoot
			if plugin == "" {
				plugin = "not found"
			} else if status.PluginVersion != "" {
				plugin += " (" + status.PluginVersion + ")"
			}
			p.KV("Plugin", plugin)
			if status.PluginRoot == "" && status.PluginError != "" {
				p.Warn("  %s", status.PluginError)
			}
			codexDetail := "native hooks + plugin MCP"
			if status.CodexCachePath != "" && !status.CodexCacheRunnable {
				codexDetail = "codex plugin cache runtime missing; run: " + aisettings.ClaudeMemRepairCommand
			}
			if status.CodexHome != "" {
				codexDetail += " (CODEX_HOME=" + status.CodexHome + ", not ~/.codex)"
			}
			p.Section("Tools")
			printMemoryState(p, "codex", status.CodexNativeHooks, codexDetail)
			printMemoryState(p, "kimi", status.KimiMCP, fmt.Sprintf("MCP + %d transcript(s)", status.WatchCount["kimi"]))
			printMemoryState(p, "qwen", status.QwenMCP, fmt.Sprintf("MCP + %d transcript(s)", status.WatchCount["qwen"]))
			printMemoryState(p, "agy", status.AntigravityMCP, "MCP recall; no transcript capture")
			printMemoryState(p, "kiro", status.KiroMCP, fmt.Sprintf("MCP + %d transcript(s)", status.WatchCount["kiro"]))
			printMemoryState(p, "copilot", status.CopilotMCP, fmt.Sprintf("MCP + %d transcript(s)", status.WatchCount["copilot"]))
			// pi's readiness is the instructions target, not the transcript
			// count: a machine that has pi wired but has not run a session yet
			// is installed, not broken.
			printMemoryState(p, "pi", status.PiAgents, fmt.Sprintf("instructions + %d transcript(s); no MCP recall (pi has none)", status.WatchCount["pi"]))
			p.Section("Shared runtime")
			printMemoryState(p, "instructions", status.InstructionsEnabled, "agents SSOT recall policy")
			printMemoryState(p, "bridge", status.BridgeInstalled && status.BridgeRunning, bridgeStatusDetail(status))
			printMemorySyncStatusSection(p, cmd, mgr)
			return nil
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func printMemoryState(p *Printer, label string, ok bool, detail string) {
	marker := ui.StyleHint.Render(ui.MarkAbsent)
	state := "missing"
	if ok {
		marker = ui.StyleSuccess.Render(ui.MarkPresent)
		state = "ready"
	}
	p.Bullet(marker, fmt.Sprintf("%-13s %-7s %s", ui.StyleValue.Render(label), state, detail))
}

func bridgeStatusDetail(status aisettings.ClaudeMemStatus) string {
	if !status.BridgeInstalled {
		return "LaunchAgent not installed"
	}
	if !status.BridgeRunning {
		return "LaunchAgent installed but stopped"
	}
	return "LaunchAgent running"
}

func newAIMemoryMCPServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "mcp-server",
		Short:  "Run the claude-mem stdio MCP server",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := newClaudeMemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			return mgr.RunMCPServer(cmd.Context())
		},
	}
}

func newAIMemoryBridgeCmd() *cobra.Command {
	return newAIMemoryBridgeCmdWithManager(newClaudeMemManagerFromCmd)
}

func newAIMemoryBridgeCmdWithManager(manager func(*cobra.Command) (*aisettings.ClaudeMemManager, error)) *cobra.Command {
	return &cobra.Command{
		Use:    "bridge",
		Short:  "Run the Kimi/Kiro/Copilot/Qwen/pi transcript bridge",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := manager(cmd)
			if err != nil {
				return err
			}
			return mgr.RunBridge(cmd.Context())
		},
	}
}

func newAIMemoryUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update claude-mem to the latest marketplace version",
		Long: `Refresh the thedotmack marketplace checkout, compare the installed
claude-mem version, and update the Claude Code plugin when behind. The
codex plugin cache is reinstalled and its bun runtime re-materialized, so
both CLIs converge on the same version. Idempotent when already current;
the scheduled peer sync runs this check before every sync.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr, err := newClaudeMemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			result, err := mgr.UpdateClaudeMemPlugin(cmd.Context(), memorySyncRunner(dryRun), false)
			if err != nil {
				return err
			}
			auditAIEventBestEffort(cmd, "ai.memory.update", map[string]any{
				"before": result.Before, "after": result.After, "updated": result.Updated,
			})
			p.Header("claude-mem update")
			p.KV("Marketplace", result.MarketplaceVersion)
			if result.Updated {
				if dryRun {
					p.Line("  dry-run: would update %s → %s", result.Before, result.MarketplaceVersion)
				} else {
					p.Success("✓ updated %s → %s", result.Before, result.After)
				}
			} else {
				p.KV("Installed", result.After+" (current)")
			}
			if result.CodexCacheRefresh != "" {
				p.KV("Codex cache", result.CodexCacheRefresh)
			}
			return nil
		},
	}
}

func newClaudeMemManagerFromCmd(cmd *cobra.Command) (*aisettings.ClaudeMemManager, error) {
	home := homeFromCmd(cmd)
	dotPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve dot executable: %w", err)
	}
	dotPath, err = filepath.Abs(dotPath)
	if err != nil {
		return nil, err
	}
	mgr := aisettings.NewClaudeMemManager(home, dotPath, "")
	selection := func() ([]string, error) {
		selected, e := effectiveToolingForCmd(cmd)
		if e != nil {
			return nil, e
		}
		if selected == nil {
			if path, _ := cmd.Flags().GetString("config"); path != "" {
				return []string{}, nil
			}
			return nil, nil
		}
		agents := make([]string, len(selected.Agents))
		copy(agents, selected.Agents)
		return agents, nil
	}
	mgr.SelectedAgents, err = selection()
	if err != nil {
		return nil, err
	}
	mgr.AgentSelection = selection
	override, _ := cmd.Flags().GetString("home")
	mgr.ExplicitHome = override != ""
	if override == "" {
		if adopted := os.Getenv("KIMI_CODE_HOME"); filepath.IsAbs(adopted) {
			mgr.KimiHome = adopted
		}
	}
	if nodePath, lookupErr := exec.LookPath("node"); lookupErr == nil {
		if nodePath, absErr := filepath.Abs(nodePath); absErr == nil {
			mgr.NodePath = nodePath
		}
	}
	if bunPath, lookupErr := exec.LookPath("bun"); lookupErr == nil {
		if bunPath, absErr := filepath.Abs(bunPath); absErr == nil {
			mgr.BunPath = bunPath
		}
	}
	return mgr, nil
}
