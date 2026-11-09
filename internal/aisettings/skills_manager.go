package aisettings

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SkillsProviderMaru = "maru"
	SkillsProviderPath = "path"

	DefaultMaruSkillsRoot = "~/.maru/skills"

	SkillLinkStatusInSync        = "in-sync"
	SkillLinkStatusMissing       = "missing"
	SkillLinkStatusConflict      = "conflict"
	SkillLinkStatusSourceMissing = "source-missing"
)

// SkillsManager reports drift between a configured skills SSOT and per-tool
// skill roots. It is read-only: the Maru app owns skill sources, the registry,
// runtime symlinks, and tool federation; dotfiles only diagnoses.
type SkillsManager struct {
	HomeDir       string
	Tools         []SkillTool
	SelectedTools []string
	ExplicitHome  bool
}

// SkillTool describes one tool root that can consume Markdown skills.
type SkillTool struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	RootPath    string   `json:"root_path"`
	Aliases     []string `json:"aliases,omitempty"`
}

// SkillsOptions controls status diagnostics.
type SkillsOptions struct {
	Provider string
	SSOTPath string
	Tools    []string
	Warnings []string
}

// SkillSourceItem is a single source skill directory under the SSOT root.
type SkillSourceItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// SkillTargetStatus describes one desired source->tool symlink.
type SkillTargetStatus struct {
	ToolID     string `json:"tool_id"`
	ToolRoot   string `json:"tool_root"`
	SkillName  string `json:"skill_name"`
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// SkillsStatusReport aggregates source inventory and target drift.
type SkillsStatusReport struct {
	Provider string              `json:"provider"`
	SSOTPath string              `json:"ssot_path"`
	Tools    []string            `json:"tools"`
	Sources  []SkillSourceItem   `json:"sources"`
	Items    []SkillTargetStatus `json:"items"`
	Warnings []string            `json:"warnings,omitempty"`
}

// RegisteredSkillTools returns provider-managed runtime targets. Maru
// currently federates skills to Claude Code and Codex only. Broader skill
// inventories (agents, Gemini, Antigravity) remain available through
// `dot ai skills list|validate`, but are not treated as managed sync targets.
func RegisteredSkillTools() []SkillTool {
	return []SkillTool{
		{ID: "kimi", DisplayName: "Kimi Code", RootPath: "~/.kimi-code/skills"},
		{ID: "qwen", DisplayName: "Qwen Code", RootPath: "~/.qwen/skills"},
		{ID: "grok", DisplayName: "Grok", RootPath: "~/.grok/skills"},
		{ID: "opencode", DisplayName: "OpenCode", RootPath: "~/.config/opencode/skills"},
		{
			ID:          "claude",
			DisplayName: "Claude Code",
			RootPath:    "~/.claude/skills",
		},
		{
			ID:          "codex",
			DisplayName: "Codex CLI",
			RootPath:    "~/.codex/skills",
		},
	}
}

// NewSkillsManager returns a skills manager rooted at homeDir.
func NewSkillsManager(homeDir string) *SkillsManager {
	return &SkillsManager{
		HomeDir: homeDir,
		Tools:   RegisteredSkillTools(),
	}
}

// DefaultMaruSSOTPath returns Maru's runtime skills root.
func (m *SkillsManager) DefaultMaruSSOTPath() string {
	return expandHome(DefaultMaruSkillsRoot, m.homeDir())
}

// DefaultTools returns registered tools whose own skills root directory (e.g.
// ~/.claude/skills) exists on disk. Used by the read-only path/status commands
// so they never hard-fail on a missing tool selection. Detecting the skills
// root itself — rather than its parent — keeps tools that share a parent
// directory independent (e.g. gemini at ~/.gemini/skills versus antigravity at
// ~/.gemini/antigravity/skills). Falls back to every registered tool ID when
// none is detected so informational output is never empty. It never mutates
// anything.
func (m *SkillsManager) DefaultTools() []string {
	if m.SelectedTools != nil {
		return append([]string{}, m.SelectedTools...)
	}
	var ids []string
	seen := map[string]bool{}
	for _, tool := range m.registry() {
		if seen[tool.ID] {
			continue
		}
		root := expandHome(tool.RootPath, m.homeDir())
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			ids = append(ids, tool.ID)
			seen[tool.ID] = true
		}
	}
	if len(ids) == 0 {
		for _, tool := range m.registry() {
			if seen[tool.ID] {
				continue
			}
			ids = append(ids, tool.ID)
			seen[tool.ID] = true
		}
	}
	return ids
}

// Tool returns a registered skill target tool by ID or alias.
func (m *SkillsManager) Tool(id string) (SkillTool, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, tool := range m.registry() {
		if tool.ID == id {
			return tool, true
		}
		for _, alias := range tool.Aliases {
			if strings.ToLower(strings.TrimSpace(alias)) == id {
				return tool, true
			}
		}
	}
	return SkillTool{}, false
}

// TargetRoot returns the absolute skills root for one tool.
func (m *SkillsManager) TargetRoot(toolID string) (string, error) {
	tool, ok := m.Tool(toolID)
	if !ok {
		return "", fmt.Errorf("unknown skills tool %q", toolID)
	}
	if err := validateInstructionOverride(m.homeDir(), tool.ID, m.ExplicitHome); err != nil {
		return "", err
	}
	fallback := expandHome(tool.RootPath, m.homeDir())
	instruction := InstructionPath(m.homeDir(), tool.ID, m.ExplicitHome, filepath.Join(filepath.Dir(fallback), "AGENTS.md"))
	return filepath.Join(filepath.Dir(instruction), "skills"), nil
}

// Status reports target symlink drift for all configured source skills.
func (m *SkillsManager) Status(opts SkillsOptions) (*SkillsStatusReport, error) {
	resolved, err := m.resolveOptions(opts)
	if err != nil {
		return nil, err
	}
	sources, warnings, err := m.listSources(resolved.SSOTPath)
	if err != nil {
		return nil, err
	}
	report := &SkillsStatusReport{
		Provider: resolved.Provider,
		SSOTPath: resolved.SSOTPath,
		Tools:    append([]string(nil), resolved.Tools...),
		Sources:  sources,
		Warnings: append(append([]string(nil), resolved.Warnings...), warnings...),
	}
	if len(sources) == 0 && len(warnings) > 0 {
		report.Items = append(report.Items, SkillTargetStatus{
			Status:  SkillLinkStatusSourceMissing,
			Message: strings.Join(warnings, "; "),
		})
		return report, nil
	}
	for _, id := range resolved.Tools {
		root, err := m.TargetRoot(id)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			target := filepath.Join(root, source.Name)
			status := SkillTargetStatus{
				ToolID:     id,
				ToolRoot:   root,
				SkillName:  source.Name,
				SourcePath: source.Path,
				TargetPath: target,
			}
			m.applyTargetStatus(&status)
			report.Items = append(report.Items, status)
		}
	}
	return report, nil
}

func (m *SkillsManager) applyTargetStatus(status *SkillTargetStatus) {
	info, err := os.Lstat(status.TargetPath)
	if os.IsNotExist(err) {
		status.Status = SkillLinkStatusMissing
		return
	}
	if err != nil {
		status.Status = SkillLinkStatusConflict
		status.Message = err.Error()
		return
	}
	if info.Mode()&os.ModeSymlink == 0 {
		status.Status = SkillLinkStatusConflict
		status.Message = "target exists and is not a symlink"
		return
	}
	if m.symlinkPointsTo(status.TargetPath, status.SourcePath) {
		status.Status = SkillLinkStatusInSync
		return
	}
	target, err := os.Readlink(status.TargetPath)
	if err != nil {
		status.Message = err.Error()
	} else {
		status.Message = fmt.Sprintf("symlink points to %s", target)
	}
	status.Status = SkillLinkStatusConflict
}

func (m *SkillsManager) resolveOptions(opts SkillsOptions) (SkillsOptions, error) {
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	ssot := strings.TrimSpace(opts.SSOTPath)
	if provider == "" && ssot != "" {
		provider = SkillsProviderPath
	}
	if provider == "" {
		return SkillsOptions{}, fmt.Errorf("skills provider is required (maru or path)")
	}
	switch provider {
	case SkillsProviderMaru:
		if ssot == "" {
			ssot = DefaultMaruSkillsRoot
		}
	case SkillsProviderPath:
		if ssot == "" {
			return SkillsOptions{}, fmt.Errorf("skills ssot path is required when provider is %q", SkillsProviderPath)
		}
	default:
		if provider == "anchor" {
			return SkillsOptions{}, fmt.Errorf("skills provider %q was renamed: use %s", opts.Provider, SkillsProviderMaru)
		}
		return SkillsOptions{}, fmt.Errorf("unknown skills provider %q", opts.Provider)
	}
	tools, migrationWarnings, err := m.resolveToolIDs(opts.Tools)
	if err != nil {
		return SkillsOptions{}, err
	}
	if len(tools) == 0 {
		tools = m.DefaultTools()
	}
	return SkillsOptions{
		Provider: provider,
		SSOTPath: filepath.Clean(expandHome(ssot, m.homeDir())),
		Tools:    tools,
		Warnings: append(append([]string(nil), opts.Warnings...), migrationWarnings...),
	}, nil
}

func (m *SkillsManager) resolveToolIDs(ids []string) ([]string, []string, error) {
	seen := map[string]bool{}
	var out []string
	var warnings []string
	for _, id := range ids {
		for _, part := range strings.Split(id, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" {
				continue
			}
			tool, ok := m.Tool(part)
			if !ok {
				return nil, nil, fmt.Errorf("unknown skills tool %q (valid: %s)", part, m.toolIDList())
			}
			if seen[tool.ID] {
				continue
			}
			seen[tool.ID] = true
			out = append(out, tool.ID)
		}
	}
	return out, warnings, nil
}

func (m *SkillsManager) listSources(root string) ([]SkillSourceItem, []string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, []string{fmt.Sprintf("skills SSOT root %s does not exist", root)}, nil
		}
		return nil, nil, fmt.Errorf("read skills SSOT root %s: %w", root, err)
	}
	var out []SkillSourceItem
	var warnings []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if !skillNamePattern.MatchString(name) {
			warnings = append(warnings, fmt.Sprintf("ignored invalid skill directory name %s", name))
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Stat(filepath.Join(path, "SKILL.md"))
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("ignored %s: %v", path, err))
			}
			continue
		}
		if info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("ignored %s: SKILL.md is a directory", path))
			continue
		}
		out = append(out, SkillSourceItem{Name: name, Path: path})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, warnings, nil
}

func (m *SkillsManager) symlinkPointsTo(link, desired string) bool {
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	target = filepath.Clean(target)
	desired = filepath.Clean(desired)
	if target == desired {
		return true
	}
	targetReal, targetErr := filepath.EvalSymlinks(target)
	desiredReal, desiredErr := filepath.EvalSymlinks(desired)
	if targetErr == nil && desiredErr == nil {
		return filepath.Clean(targetReal) == filepath.Clean(desiredReal)
	}
	return false
}

func (m *SkillsManager) registry() []SkillTool {
	if len(m.Tools) > 0 {
		return m.Tools
	}
	return RegisteredSkillTools()
}

// toolIDList returns the comma-joined canonical tool IDs for error hints.
// IDs are de-duplicated (registry order preserved) so a custom-injected
// registry cannot produce an unstable or repetitive hint string.
func (m *SkillsManager) toolIDList() string {
	seen := map[string]bool{}
	ids := make([]string, 0, len(m.registry()))
	for _, tool := range m.registry() {
		if seen[tool.ID] {
			continue
		}
		seen[tool.ID] = true
		ids = append(ids, tool.ID)
	}
	return strings.Join(ids, ", ")
}

func (m *SkillsManager) homeDir() string {
	return normalizeHomeDir(m.HomeDir)
}
