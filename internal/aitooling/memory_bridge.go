package aitooling

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// Prepare only selected recall and bridge configuration. The native memory
// backend owns its service, database, credentials and cooldown state.
func (e *Engine) memoryBridge(ctx context.Context, s config.AIToolingConfig, op Operation) ItemResult {
	r := ItemResult{ID: "claude-mem/bridge", Kind: "integration"}
	if e.opts.ExplicitHome {
		r.Status = "deferred-home"
		r.Detail = "memory bridge preserves backend profile ownership; run in the owning native home"
		return r
	}
	dot := e.find("dot")
	if dot == "" {
		r.Status = "deferred-prerequisite"
		r.Detail = "a durable installed dot executable is required for memory integration"
		return r
	}
	m := aisettings.NewClaudeMemManager(e.opts.HomeDir, dot, "")
	m.SelectedAgents = append([]string{}, s.Agents...)
	m.KimiHome = e.profile("kimi")
	r = e.inspectMemoryBridge(ctx, m, s)
	if op == Inspect || r.Status == "installed" {
		return r
	}
	if e.opts.DryRun {
		r.Status = "planned"
		r.Detail = "prepare selected Kimi/Qwen recall and watch configuration without service changes"
		return r
	}
	if _, err := m.PrepareSelectedIntegration(); err != nil {
		r.Status = "deferred-memory-backend"
		r.Detail = "selected integration requires a ready native backend; no service started or restarted"
		return r
	}
	return e.inspectMemoryBridge(ctx, m, s)
}
func (e *Engine) inspectMemoryBridge(ctx context.Context, m *aisettings.ClaudeMemManager, s config.AIToolingConfig) ItemResult {
	r := ItemResult{ID: "claude-mem/bridge", Kind: "integration", Status: "missing"}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status := m.Status(ctx, "")
	if status.PluginRoot == "" {
		r.Status = "deferred-memory-backend"
		r.Detail = "native memory backend unavailable"
		return r
	}
	configured := (!slices.Contains(s.Agents, "kimi") || status.KimiMCP) && (!slices.Contains(s.Agents, "qwen") || status.QwenMCP)
	expected, err := m.BuildTranscriptConfig()
	if err != nil {
		r.Status = "unknown"
		r.Detail = "cannot resolve selected native transcript schema"
		return r
	}
	wantBytes, err := json.Marshal(expected)
	if err != nil {
		r.Status = "unknown"
		return r
	}
	actual, err := os.ReadFile(m.TranscriptConfigPath())
	var want, got any
	if err != nil || json.Unmarshal(wantBytes, &want) != nil || json.Unmarshal(actual, &got) != nil || !reflect.DeepEqual(want, got) {
		configured = false
	}
	if !configured {
		r.Detail = "selected recall/watch configuration missing or drifted"
		return r
	}
	r.Status = "pending-runtime"
	r.Detail = "selected MCP and watch configuration verified; native bridge service is not running"
	if status.BridgeRunning {
		r.Status = "installed"
		r.Detail = "selected MCP/watch configuration and running bridge verified; provider capture acceptance is separate"
	}
	return r
}
func needsMemoryBridge(s config.AIToolingConfig) bool {
	return slices.Contains(s.Agents, "kimi") || slices.Contains(s.Agents, "qwen")
}
