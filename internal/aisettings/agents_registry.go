package aisettings

// AgentTool describes one AI coding tool target that can receive the shared
// global AGENTS.md content.
type AgentTool struct {
	ID          string
	DisplayName string
	TargetPath  string
	OverlayFile string
	Aliases     []string
	Optional    bool
}

// RegisteredAgentTools returns the built-in AI agents registry.
func RegisteredAgentTools() []AgentTool {
	// Keep this registry to instruction/settings targets only. Skill target
	// symlinks are handled by SkillsManager when modules.ai.skills is enabled.
	// Grok has no entry: it reads ~/.claude/CLAUDE.md through its Claude
	// compat, and never reads ~/.grok/AGENTS.md.
	return []AgentTool{
		{ID: "opencode", DisplayName: "OpenCode", TargetPath: "~/.config/opencode/AGENTS.md", OverlayFile: "opencode.md", Optional: true},
		{
			ID:          "claude",
			DisplayName: "Claude Code",
			// Stays CLAUDE.md: Claude Code reads AGENTS.md only as project
			// instructions; user-level memory is ~/.claude/CLAUDE.md alone.
			TargetPath:  "~/.claude/CLAUDE.md",
			OverlayFile: "claude.md",
		},
		{
			ID:          "codex",
			DisplayName: "Codex CLI",
			TargetPath:  "~/.codex/AGENTS.md",
			OverlayFile: "codex.md",
		},
		{
			ID:          "kiro",
			DisplayName: "Kiro CLI",
			TargetPath:  "~/.kiro/steering/AGENTS.md",
			OverlayFile: "kiro.md",
		},
		{
			ID:          "kimi",
			DisplayName: "Kimi Code CLI",
			TargetPath:  "~/.kimi-code/AGENTS.md",
			OverlayFile: "kimi.md",
			Optional:    true,
		},
		{
			ID:          "pi",
			DisplayName: "pi",
			TargetPath:  "~/.pi/agent/AGENTS.md",
			OverlayFile: "pi.md",
			Optional:    true,
		},
		{
			ID:          "qwen",
			DisplayName: "Qwen Code",
			TargetPath:  "~/.qwen/AGENTS.md",
			OverlayFile: "qwen.md",
			Optional:    true,
		},
		{
			ID:          "antigravity",
			DisplayName: "Antigravity CLI",
			TargetPath:  "~/.gemini/GEMINI.md",
			OverlayFile: "antigravity.md",
			Aliases:     []string{"gemini"},
			Optional:    true,
		},
		{
			ID:          "copilot",
			DisplayName: "GitHub Copilot",
			TargetPath:  "~/.copilot/copilot-instructions.md",
			OverlayFile: "copilot.md",
		},
		{
			ID:          "aider",
			DisplayName: "Aider",
			TargetPath:  "~/.aider.conf.md",
			OverlayFile: "aider.md",
			Optional:    true,
		},
	}
}
