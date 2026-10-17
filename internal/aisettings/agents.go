package aisettings

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	dottemplate "github.com/STAIxBWLB/dotfiles/internal/template"
)

const (
	AgentsSSOTRelPath = ".config/dotfiles/agents"
	AgentsSSOTName    = "AGENTS.md"

	legacyStateName      = ".state.json"
	agentsManagedHeader  = "<!-- managed by dot ai agents - edit ~/.config/dotfiles/agents/AGENTS.md -->"
	agentsOverlayPattern = "<!-- overlay:%s -->"
)

var defaultAgentSections = []string{
	"Identity",
	"How I Work",
	"Writing Style",
	"Text Encoding",
	"Operating Principles",
	"Project Conventions",
	"System Resource Safety",
	"Tool-Specific Notes",
	"Custom",
}

// AgentsManager manages the shared AI agents instruction SSOT and deployed
// per-tool copies.
type AgentsManager struct {
	Runner         *dotexec.Runner
	HomeDir        string
	SSOTDir        string
	Tools          []AgentTool
	SelectedTools  []string
	SelectionError error
	ExplicitHome   bool
	profilePass    bool
	// Out receives progress and prompt output; nil means os.Stdout.
	Out io.Writer
}

// out returns the writer progress and prompt output goes to: m.Out when set,
// os.Stdout when m or m.Out is nil.
func (m *AgentsManager) out() io.Writer {
	if m == nil || m.Out == nil {
		return os.Stdout
	}
	return m.Out
}

// AgentStatus describes one rendered target's sync state.
type AgentStatus struct {
	Tool          AgentTool
	TargetPath    string
	TargetExists  bool
	SSOTExists    bool
	SSOTHash      string
	RenderedHash  string
	TargetHash    string
	Drift         string
	OverlayExists bool
}

// ApplyOptions controls AgentsManager.Apply.
type ApplyOptions struct {
	Tools  []string
	DryRun bool
	Force  bool
}

// ApplyResult summarizes an agents apply operation.
type ApplyResult struct {
	Items    []AgentApplyItem
	Warnings []string
	DryRun   bool
}

// AgentApplyItem captures one tool target write.
type AgentApplyItem struct {
	ToolID       string
	TargetPath   string
	Changed      bool
	BackedUp     bool
	BackupPath   string
	Conflict     bool
	ExpectedHash string
	ActualHash   string
	Diff         string
}

// ProtectedWriteConflictError reports a live target changed outside the last
// dot-managed apply state, so overwriting it requires explicit force.
type ProtectedWriteConflictError struct {
	ToolID       string
	TargetPath   string
	ExpectedHash string
	ActualHash   string
}

func (e *ProtectedWriteConflictError) Error() string {
	return fmt.Sprintf("protected write conflict for %s target %s (expected last-applied hash %s, got %s); rerun `dot ai agents apply --tool %s --force` after reviewing the diff",
		e.ToolID, e.TargetPath, shortHashForMessage(e.ExpectedHash), shortHashForMessage(e.ActualHash), e.ToolID)
}

// PullOptions controls copying a live tool target back into the SSOT.
type PullOptions struct {
	FromTool string
	Yes      bool
	Force    bool
}

// PullResult summarizes a pull operation.
type PullResult struct {
	FromTool   string
	SourcePath string
	SSOTPath   string
	BackupPath string
	Changed    bool
}

// InitOptions controls SSOT initialization.
type InitOptions struct {
	FromCurrent string
	Yes         bool
	Force       bool
}

// InitResult summarizes SSOT initialization.
type InitResult struct {
	Path       string
	Created    bool
	FromTool   string
	BackupPath string
}

// AuthorOptions controls assisted SSOT authoring.
type AuthorOptions struct {
	FromCurrent    string
	NonInteractive bool
	Section        string
	Value          string
	Yes            bool
}

// AuthorResult summarizes assisted authoring.
type AuthorResult struct {
	Path     string
	Changed  bool
	Sections []string
}

// ShowOptions controls SSOT display.
type ShowOptions struct {
	RenderedTool    string
	WithLineNumbers bool
}

type agentsState struct {
	LastApplied map[string]string `json:"lastApplied"`
}

type agentApplyPlan struct {
	id           string
	target       string
	rendered     string
	renderedHash string
	targetExists bool
	targetHash   string
	changed      bool
	item         AgentApplyItem
	conflict     bool
}

// NewAgentsManager returns an agents manager rooted at homeDir.
func NewAgentsManager(runner *dotexec.Runner, homeDir string, explicitHome ...bool) *AgentsManager {
	m := &AgentsManager{
		Runner:  runner,
		HomeDir: homeDir,
		Tools:   RegisteredAgentTools(),
	}
	m.ExplicitHome = len(explicitHome) > 0 && explicitHome[0]
	var state *config.UserState
	var err error
	actual, _ := os.UserHomeDir()
	if !m.ExplicitHome && filepath.Clean(homeDir) == filepath.Clean(actual) {
		state, err = config.LoadState()
	} else {
		state, err = config.LoadStateForHome(homeDir)
	}
	if err != nil {
		m.SelectionError = err
	} else if state.Modules.AI.Tooling != nil {
		m.SelectedTools = append([]string{}, state.Modules.AI.Tooling.Agents...)
	}
	return m
}

// SSOTPath returns the absolute path to the shared AGENTS.md file.
func (m *AgentsManager) SSOTPath() string {
	return filepath.Join(m.ssotDir(), AgentsSSOTName)
}

// SSOTDirPath returns the absolute path to the agents SSOT directory.
func (m *AgentsManager) SSOTDirPath() string {
	return m.ssotDir()
}

// StatePath returns the absolute path to the apply-state file. The state
// records this machine's rendered targets, so it lives in the machine-local
// data tree next to the agents backups, never in the SSOT dir that is synced
// between machines.
func (m *AgentsManager) StatePath() string {
	return filepath.Join(m.homeDir(), ".local", "share", "dotfiles", "agents", "state.json")
}

// legacyStatePath is where the apply state lived before it moved out of the
// synced SSOT dir. readState migrates it and removeLegacyState deletes it.
func (m *AgentsManager) legacyStatePath() string {
	return filepath.Join(m.ssotDir(), legacyStateName)
}

// DefaultApplyTools returns non-optional tools plus optional tools whose target
// file already exists.
func (m *AgentsManager) DefaultApplyTools() []string {
	if m.SelectedTools != nil {
		return append([]string{}, m.SelectedTools...)
	}
	var ids []string
	seen := map[string]bool{}
	for _, tool := range m.registry() {
		target, err := m.TargetPath(tool.ID)
		if err != nil {
			continue
		}
		if !tool.Optional {
			if !seen[tool.ID] {
				ids = append(ids, tool.ID)
				seen[tool.ID] = true
			}
			continue
		}
		if _, err := os.Lstat(target); err == nil {
			if !seen[tool.ID] {
				ids = append(ids, tool.ID)
				seen[tool.ID] = true
			}
		}
	}
	return ids
}

// Tool returns a registered tool by id.
func (m *AgentsManager) Tool(id string) (AgentTool, bool) {
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
	return AgentTool{}, false
}

// TargetPath returns a registered tool target path as an absolute path.
func (m *AgentsManager) TargetPath(toolID string) (string, error) {
	tool, ok := m.Tool(toolID)
	if !ok {
		return "", fmt.Errorf("unknown agents tool %q", toolID)
	}
	if err := validateInstructionOverride(m.homeDir(), tool.ID, m.ExplicitHome); err != nil {
		return "", err
	}
	return InstructionPath(m.homeDir(), tool.ID, m.ExplicitHome, m.expandHome(tool.TargetPath)), nil
}

// Status reports SSOT drift for every registered tool.
func (m *AgentsManager) Status() ([]AgentStatus, error) {
	ssotPath := m.SSOTPath()
	ssotBytes, ssotErr := os.ReadFile(ssotPath)
	ssotExists := ssotErr == nil
	if ssotErr != nil && !os.IsNotExist(ssotErr) {
		return nil, fmt.Errorf("read SSOT: %w", ssotErr)
	}

	var out []AgentStatus
	for _, tool := range m.registry() {
		if m.SelectedTools != nil && !containsAgentID(m.SelectedTools, tool.ID) {
			continue
		}
		target, err := m.TargetPath(tool.ID)
		if err != nil {
			return nil, err
		}
		st := AgentStatus{
			Tool:       tool,
			TargetPath: target,
			SSOTExists: ssotExists,
		}
		if ssotExists {
			st.SSOTHash = normalizedHash(ssotBytes)
			rendered, overlayExists, err := m.renderTool(tool, ssotBytes)
			if err != nil {
				return nil, err
			}
			st.RenderedHash = normalizedHash([]byte(rendered))
			st.OverlayExists = overlayExists
		}
		targetBytes, err := os.ReadFile(target)
		if err == nil {
			st.TargetExists = true
			st.TargetHash = normalizedHash(targetBytes)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read target %s: %w", target, err)
		}

		switch {
		case !ssotExists:
			st.Drift = "ssot-missing"
		case !st.TargetExists:
			st.Drift = "target-missing"
		case st.TargetHash == st.RenderedHash:
			st.Drift = "in-sync"
		default:
			st.Drift = "out-of-sync"
		}
		out = append(out, st)
	}
	if !m.profilePass && (m.SelectedTools == nil || containsAgentID(m.SelectedTools, "codex")) {
		if extra := m.codexStandardProfile(); extra != nil {
			extra.Tools = []AgentTool{{ID: "codex", DisplayName: "Codex standard profile", TargetPath: "~/.codex/AGENTS.md", OverlayFile: "codex.md"}}
			statuses, err := extra.Status()
			if err != nil {
				return nil, err
			}
			out = append(out, statuses...)
		}
	}
	for i := range out {
		if out[i].Tool.ID == "qwen" && out[i].Drift == "in-sync" {
			ready, err := m.qwenContextReady()
			if err != nil {
				return nil, err
			}
			if !ready {
				out[i].Drift = "context-missing"
			}
		}
	}
	return out, nil
}

// Render returns the SSOT rendered for one tool, including its optional overlay.
func (m *AgentsManager) Render(toolID string) (string, error) {
	tool, ok := m.Tool(toolID)
	if !ok {
		return "", fmt.Errorf("unknown agents tool %q", toolID)
	}
	ssotBytes, err := os.ReadFile(m.SSOTPath())
	if err != nil {
		return "", fmt.Errorf("read SSOT: %w", err)
	}
	rendered, _, err := m.renderTool(tool, ssotBytes)
	return rendered, err
}

// Apply renders the SSOT to the selected tool targets. An empty Tools list
// applies to every registered tool; command callers can pass DefaultApplyTools
// when they want a narrower CLI default.
func (m *AgentsManager) Apply(opts ApplyOptions) (*ApplyResult, error) {
	if m.SelectionError != nil {
		return nil, fmt.Errorf("read tooling selection: %w", m.SelectionError)
	}
	ids, err := m.resolveToolIDs(opts.Tools)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return &ApplyResult{DryRun: opts.DryRun || m.runner().DryRun}, nil
	}
	state, err := m.readState()
	if err != nil {
		return nil, err
	}
	effectiveDryRun := opts.DryRun || m.runner().DryRun
	result := &ApplyResult{DryRun: effectiveDryRun}
	plans := make([]agentApplyPlan, 0, len(ids))
	var firstConflict *ProtectedWriteConflictError
	for _, id := range ids {
		target, err := m.TargetPath(id)
		if err != nil {
			return nil, err
		}
		rendered, err := m.Render(id)
		if err != nil {
			return nil, err
		}
		renderedHash := normalizedHash([]byte(rendered))
		targetBytes, targetErr := os.ReadFile(target)
		targetExists := targetErr == nil
		if targetErr != nil && !os.IsNotExist(targetErr) {
			return nil, fmt.Errorf("read target %s: %w", target, targetErr)
		}
		targetHash := normalizedHash(targetBytes)
		changed := !targetExists || targetHash != renderedHash

		item := AgentApplyItem{
			ToolID:     id,
			TargetPath: target,
			Changed:    changed,
		}
		if changed {
			item.Diff = unifiedTextDiff("target/"+id, "rendered/"+id, string(targetBytes), rendered)
		}

		plan := agentApplyPlan{
			id:           id,
			target:       target,
			rendered:     rendered,
			renderedHash: renderedHash,
			targetExists: targetExists,
			targetHash:   targetHash,
			changed:      changed,
			item:         item,
		}
		expectedHash := m.lastAppliedHash(state, id)
		if changed && targetExists && targetHash != "" && targetHash != expectedHash {
			item.Conflict = true
			item.ExpectedHash = expectedHash
			item.ActualHash = targetHash
			plan.item = item
			plan.conflict = true
			if result.DryRun {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s target changed outside agents SSOT; apply would stop unless --force is used", id))
			} else if !opts.Force {
				if firstConflict == nil {
					firstConflict = &ProtectedWriteConflictError{
						ToolID:       id,
						TargetPath:   target,
						ExpectedHash: expectedHash,
						ActualHash:   targetHash,
					}
				}
			}
		}

		plans = append(plans, plan)
		result.Items = append(result.Items, plan.item)
	}
	if firstConflict != nil {
		return result, firstConflict
	}
	if !m.profilePass && containsAgentID(ids, "codex") {
		if extra := m.codexStandardProfile(); extra != nil {
			preflight, err := extra.Apply(ApplyOptions{Tools: []string{"codex"}, DryRun: true, Force: opts.Force})
			if err != nil {
				return result, err
			}
			if !opts.Force && !result.DryRun {
				for _, item := range preflight.Items {
					if item.Conflict {
						return result, &ProtectedWriteConflictError{ToolID: item.ToolID, TargetPath: item.TargetPath, ExpectedHash: item.ExpectedHash, ActualHash: item.ActualHash}
					}
				}
			}
		}
	}

	if containsAgentID(ids, "qwen") {
		if err := m.ensureQwenContext(result.DryRun); err != nil {
			return result, err
		}
	}

	if opts.Force && !result.DryRun {
		for i := range plans {
			plan := plans[i]
			if !plan.conflict {
				continue
			}
			backupPath, err := m.backupTarget(plan.id, plan.target)
			if err != nil {
				return nil, err
			}
			result.Items[i].BackedUp = true
			result.Items[i].BackupPath = backupPath
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s target was changed outside agents SSOT; backed up to %s", plan.id, backupPath))
		}
	}

	for i := range plans {
		plan := plans[i]
		if plan.changed && !result.DryRun {
			if err := m.runner().MkdirAll(filepath.Dir(plan.target), 0o755); err != nil {
				return nil, fmt.Errorf("create target dir: %w", err)
			}
			if err := m.runner().WriteFile(plan.target, []byte(plan.rendered), 0o644); err != nil {
				return nil, fmt.Errorf("write target %s: %w", plan.target, err)
			}
		}
		if !result.DryRun && (!plan.changed || plan.targetExists || plan.renderedHash != "") {
			state.LastApplied[m.stateKey(plan.id)] = plan.renderedHash
			for _, alias := range m.toolAliases(plan.id) {
				delete(state.LastApplied, alias)
			}
		}
	}
	if !result.DryRun {
		if err := m.writeState(state); err != nil {
			return nil, err
		}
		if err := m.removeLegacyState(); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("legacy agents state not removed (ignored while %s exists): %v", m.StatePath(), err))
		}
	}
	if !m.profilePass && containsAgentID(ids, "codex") {
		if extra := m.codexStandardProfile(); extra != nil {
			other, err := extra.Apply(ApplyOptions{Tools: []string{"codex"}, DryRun: opts.DryRun, Force: opts.Force})
			if other != nil {
				result.Items = append(result.Items, other.Items...)
				result.Warnings = append(result.Warnings, other.Warnings...)
			}
			if err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

// Pull copies one live tool target back into the shared SSOT.
func (m *AgentsManager) Pull(opts PullOptions) (*PullResult, error) {
	if opts.FromTool == "" {
		return nil, fmt.Errorf("--from is required")
	}
	source, err := m.TargetPath(opts.FromTool)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", source, err)
	}
	data = stripManagedHeader(data)
	ssot := m.SSOTPath()
	result := &PullResult{
		FromTool:   opts.FromTool,
		SourcePath: source,
		SSOTPath:   ssot,
	}
	if old, err := os.ReadFile(ssot); err == nil {
		if normalizedHash(old) == normalizedHash(data) {
			return result, nil
		}
		if !opts.Yes && !opts.Force {
			return nil, fmt.Errorf("%s already exists; rerun with --yes to overwrite after backing it up", ssot)
		}
		backup, err := m.backupSSOT()
		if err != nil {
			return nil, err
		}
		result.BackupPath = backup
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read SSOT: %w", err)
	}
	if err := m.runner().MkdirAll(filepath.Dir(ssot), 0o755); err != nil {
		return nil, err
	}
	if err := m.runner().WriteFile(ssot, data, 0o644); err != nil {
		return nil, err
	}
	result.Changed = true
	return result, nil
}

// Init creates the shared SSOT from a live target or the embedded template.
func (m *AgentsManager) Init(opts InitOptions) (*InitResult, error) {
	if opts.FromCurrent != "" {
		pulled, err := m.Pull(PullOptions{FromTool: opts.FromCurrent, Yes: opts.Yes, Force: opts.Force})
		if err != nil {
			return nil, err
		}
		return &InitResult{
			Path:       pulled.SSOTPath,
			Created:    pulled.Changed,
			FromTool:   pulled.FromTool,
			BackupPath: pulled.BackupPath,
		}, nil
	}
	path := m.SSOTPath()
	if _, err := os.Stat(path); err == nil && !opts.Force {
		return &InitResult{Path: path}, nil
	}
	result := &InitResult{Path: path, Created: true}
	if _, err := os.Stat(path); err == nil && opts.Force {
		backup, err := m.backupSSOT()
		if err != nil {
			return nil, err
		}
		result.BackupPath = backup
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	tmpl := dottemplate.NewEngine()
	data, err := tmpl.ReadStatic("agents/AGENTS.md.tmpl")
	if err != nil {
		return nil, err
	}
	if err := m.runner().MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := m.runner().WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	return result, nil
}

// Author updates the SSOT through either a non-interactive section set or a
// simple terminal prompt.
func (m *AgentsManager) Author(opts AuthorOptions) (*AuthorResult, error) {
	if opts.FromCurrent != "" {
		if _, err := m.Pull(PullOptions{FromTool: opts.FromCurrent, Yes: opts.Yes, Force: opts.Yes}); err != nil {
			return nil, err
		}
	}
	if opts.NonInteractive {
		if strings.TrimSpace(opts.Section) == "" {
			return nil, fmt.Errorf("--section is required with --non-interactive")
		}
		return m.authorSection(opts.Section, opts.Value)
	}
	if !stdinIsTerminal() {
		return nil, fmt.Errorf("agents author requires a TTY; use `dot ai agents init` plus `dot ai agents edit`, or pass --non-interactive --section --value")
	}
	if _, err := os.Stat(m.SSOTPath()); os.IsNotExist(err) {
		if _, err := m.Init(InitOptions{}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return m.authorInteractive()
}

// Show returns raw or rendered SSOT text.
func (m *AgentsManager) Show(opts ShowOptions) (string, error) {
	var data string
	var err error
	if opts.RenderedTool != "" {
		data, err = m.Render(opts.RenderedTool)
	} else {
		b, readErr := os.ReadFile(m.SSOTPath())
		err = readErr
		data = string(b)
	}
	if err != nil {
		return "", err
	}
	if opts.WithLineNumbers {
		return numberLines(data), nil
	}
	return data, nil
}

// Edit opens the shared SSOT in the configured editor.
func (m *AgentsManager) Edit(ctx context.Context, editor string) error {
	if _, err := os.Stat(m.SSOTPath()); os.IsNotExist(err) {
		if _, err := m.Init(InitOptions{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if editor == "" {
		editor = "vi"
	}
	return runEditor(ctx, editor, m.SSOTPath())
}

// Diff returns a unified text diff between rendered desired content and the
// current live target.
func (m *AgentsManager) Diff(toolID string) (string, error) {
	rendered, err := m.Render(toolID)
	if err != nil {
		return "", err
	}
	target, err := m.TargetPath(toolID)
	if err != nil {
		return "", err
	}
	live, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return unifiedTextDiff("target/"+toolID, "rendered/"+toolID, string(live), rendered), nil
}

func (m *AgentsManager) authorSection(section, value string) (*AuthorResult, error) {
	path := m.SSOTPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, err := m.Init(InitOptions{}); err != nil {
			return nil, err
		}
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	next := setMarkdownSection(string(data), section, value)
	changed := normalizedHash(data) != normalizedHash([]byte(next))
	if changed {
		if err := m.runner().WriteFile(path, []byte(next), 0o644); err != nil {
			return nil, err
		}
	}
	return &AuthorResult{Path: path, Changed: changed, Sections: []string{section}}, nil
}

func (m *AgentsManager) authorInteractive() (*AuthorResult, error) {
	path := m.SSOTPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := string(data)
	reader := bufio.NewReader(os.Stdin)
	var changed []string
	for _, section := range defaultAgentSections {
		current := strings.TrimSpace(markdownSection(doc, section))
		fmt.Fprintf(m.out(), "\n## %s\n", section)
		if current == "" {
			fmt.Fprintln(m.out(), "(empty)")
		} else {
			fmt.Fprintln(m.out(), current)
		}
		fmt.Fprint(m.out(), "[k]eep, [r]eplace line, [e]dit in $EDITOR, [d]elete: ")
		answer, _ := reader.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "k", "keep":
			continue
		case "r", "replace":
			fmt.Fprint(m.out(), "New value: ")
			value, _ := reader.ReadString('\n')
			doc = setMarkdownSection(doc, section, strings.TrimRight(value, "\r\n"))
			changed = append(changed, section)
		case "e", "edit":
			value, err := editTextInEditor(current)
			if err != nil {
				return nil, err
			}
			doc = setMarkdownSection(doc, section, value)
			changed = append(changed, section)
		case "d", "delete":
			doc = deleteMarkdownSection(doc, section)
			changed = append(changed, section)
		default:
			fmt.Fprintln(m.out(), "unrecognized action; keeping section")
		}
	}
	didChange := normalizedHash(data) != normalizedHash([]byte(doc))
	if didChange {
		if err := m.runner().WriteFile(path, []byte(doc), 0o644); err != nil {
			return nil, err
		}
	}
	return &AuthorResult{Path: path, Changed: didChange, Sections: changed}, nil
}

func (m *AgentsManager) renderTool(tool AgentTool, ssotBytes []byte) (string, bool, error) {
	body := string(stripManagedHeader(ssotBytes))
	overlay, overlayName, err := m.readOverlay(tool)
	overlayExists := false
	if err != nil {
		if !os.IsNotExist(err) {
			return "", false, err
		}
	} else {
		overlayExists = true
		body = strings.TrimRight(body, "\n") + "\n\n" + fmt.Sprintf(agentsOverlayPattern, overlayName) + "\n" + string(overlay)
	}
	return agentsManagedHeader + "\n\n" + strings.TrimLeft(body, "\n"), overlayExists, nil
}

func (m *AgentsManager) resolveToolIDs(ids []string) ([]string, error) {
	if len(ids) == 0 && m.SelectedTools != nil {
		ids = append([]string{}, m.SelectedTools...)
		if len(ids) == 0 {
			return ids, nil
		}
	}
	if len(ids) == 0 {
		seen := map[string]bool{}
		for _, tool := range m.registry() {
			if seen[tool.ID] {
				continue
			}
			ids = append(ids, tool.ID)
			seen[tool.ID] = true
		}
		return ids, nil
	}
	seen := make(map[string]bool, len(ids))
	var out []string
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if m.SelectedTools != nil && !containsAgentID(m.SelectedTools, id) {
			return nil, fmt.Errorf("agent %q is not selected; run dot ai setup --agents to change the selection", id)
		}
		tool, ok := m.Tool(id)
		if !ok {
			return nil, fmt.Errorf("unknown agents tool %q", id)
		}
		if seen[tool.ID] {
			continue
		}
		seen[tool.ID] = true
		out = append(out, tool.ID)
	}
	return out, nil
}

func (m *AgentsManager) readOverlay(tool AgentTool) ([]byte, string, error) {
	for _, candidate := range m.overlayCandidates(tool) {
		overlayPath := filepath.Join(m.ssotDir(), "overlays", candidate.file)
		overlay, err := os.ReadFile(overlayPath)
		if err == nil {
			return overlay, candidate.id, nil
		}
		if !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("read overlay %s: %w", overlayPath, err)
		}
	}
	return nil, "", os.ErrNotExist
}

func (m *AgentsManager) overlayCandidates(tool AgentTool) []struct {
	id   string
	file string
} {
	out := []struct {
		id   string
		file string
	}{{id: tool.ID, file: tool.OverlayFile}}
	for _, alias := range tool.Aliases {
		out = append(out, struct {
			id   string
			file string
		}{id: alias, file: alias + ".md"})
	}
	return out
}

func (m *AgentsManager) lastAppliedHash(st *agentsState, toolID string) string {
	if st == nil {
		return ""
	}
	if hash := st.LastApplied[m.stateKey(toolID)]; hash != "" {
		return hash
	}
	for _, alias := range m.toolAliases(toolID) {
		if hash := st.LastApplied[alias]; hash != "" {
			return hash
		}
	}
	return ""
}

func (m *AgentsManager) toolAliases(toolID string) []string {
	tool, ok := m.Tool(toolID)
	if !ok {
		return nil
	}
	return append([]string(nil), tool.Aliases...)
}

func (m *AgentsManager) readState() (*agentsState, error) {
	st := &agentsState{LastApplied: map[string]string{}}
	data, err := os.ReadFile(m.StatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return m.readLegacyState()
		}
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.LastApplied == nil {
		st.LastApplied = map[string]string{}
	}
	return st, nil
}

// readLegacyState imports the pre-move state file from the SSOT dir. That dir
// is synced between machines, so its entries may record another machine's
// render: only an entry whose hash equals this machine's live target, hashed
// header-stripped as Apply compares, is kept. The rest, and an unparsable
// file, are dropped, which Apply reads as "no record". Dropping never adds a
// conflict: Apply consults the record only for a target that differs from the
// current render, where a mismatched record conflicts exactly as a missing
// one does, and a target equal to the current render never conflicts.
func (m *AgentsManager) readLegacyState() (*agentsState, error) {
	st := &agentsState{LastApplied: map[string]string{}}
	data, err := os.ReadFile(m.legacyStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	var legacy agentsState
	if json.Unmarshal(data, &legacy) != nil {
		return st, nil
	}
	for id, hash := range legacy.LastApplied {
		target, err := m.TargetPath(id)
		if err != nil {
			continue
		}
		if body, err := os.ReadFile(target); err == nil && normalizedHash(body) == hash {
			st.LastApplied[id] = hash
		}
	}
	return st, nil
}

func (m *AgentsManager) writeState(st *agentsState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := m.StatePath()
	if err := m.runner().MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return m.runner().WriteFileAtomic(path, data, 0o644)
}

// removeLegacyState deletes the legacy state file once the machine-local one
// is written, so a stale copy stops syncing to other machines. A copy that
// reappears later (synced back from a machine still on an older dot) is
// ignored by readState and removed again here. The removal is best-effort:
// the new state already holds the truth, so a failure such as an unwritable
// SSOT dir is only reported, never allowed to fail the apply.
func (m *AgentsManager) removeLegacyState() error {
	legacy := m.legacyStatePath()
	if _, err := os.Lstat(legacy); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := m.runner().Remove(legacy); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *AgentsManager) backupTarget(toolID, path string) (string, error) {
	dst := m.backupTargetPath(toolID + "-" + normalizedHash([]byte(path))[:12])
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	eng := &Engine{Runner: m.runner(), HomeDir: m.homeDir()}
	_, _, err = eng.copyTree(path, dst, info, filepath.Join("agents", toolID))
	if err != nil {
		return "", err
	}
	return dst, nil
}

func (m *AgentsManager) backupTargetPath(toolID string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	return filepath.Join(m.homeDir(), ".local", "share", "dotfiles", "backup", "agents", ts, toolID)
}

func (m *AgentsManager) backupSSOT() (string, error) {
	path := m.SSOTPath()
	ts := time.Now().UTC().Format("20060102T150405Z")
	dst := filepath.Join(m.homeDir(), ".local", "share", "dotfiles", "backup", "agents-ssot", ts, AgentsSSOTName)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	eng := &Engine{Runner: m.runner(), HomeDir: m.homeDir()}
	_, _, err = eng.copyTree(path, dst, info, filepath.Join("agents-ssot", AgentsSSOTName))
	if err != nil {
		return "", err
	}
	return dst, nil
}

func (m *AgentsManager) registry() []AgentTool {
	if len(m.Tools) > 0 {
		return m.Tools
	}
	return RegisteredAgentTools()
}

func (m *AgentsManager) runner() *dotexec.Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return dotexec.NewRunner(false, slog.Default())
}

func (m *AgentsManager) homeDir() string {
	if m.HomeDir != "" {
		return m.HomeDir
	}
	home, _ := os.UserHomeDir()
	return home
}

func (m *AgentsManager) ssotDir() string {
	if m.SSOTDir != "" {
		return m.SSOTDir
	}
	return filepath.Join(m.homeDir(), AgentsSSOTRelPath)
}

func (m *AgentsManager) expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(m.homeDir(), path[2:])
	}
	if path == "~" {
		return m.homeDir()
	}
	return path
}

func normalizedHash(data []byte) string {
	sum := sha256.Sum256(stripManagedHeader(data))
	return hex.EncodeToString(sum[:])
}

func shortHashForMessage(hash string) string {
	if hash == "" {
		return "<none>"
	}
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// IsManagedAgentsFile reports whether the file at path starts with the managed
// agents header, i.e. it was rendered by dot and is safe to remove or replace.
// Missing or unreadable files report false.
func IsManagedAgentsFile(path string) bool {
	// The header is the first line; a small prefix read is plenty.
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, len(agentsManagedHeader))
	n, _ := io.ReadFull(f, buf)
	return strings.HasPrefix(string(buf[:n]), agentsManagedHeader)
}

func stripManagedHeader(data []byte) []byte {
	s := string(data)
	if strings.HasPrefix(s, agentsManagedHeader) {
		s = strings.TrimPrefix(s, agentsManagedHeader)
		s = strings.TrimLeft(s, "\r\n")
	}
	return []byte(s)
}

func setMarkdownSection(doc, section, value string) string {
	section = strings.TrimSpace(section)
	value = strings.TrimRight(value, "\r\n")
	lines := splitMarkdownLines(strings.TrimRight(doc, "\r\n"))
	start, end := findMarkdownSection(lines, section)
	replacement := []string{"## " + section}
	if value != "" {
		replacement = append(replacement, strings.Split(value, "\n")...)
	}
	if start >= 0 {
		next := append([]string{}, lines[:start]...)
		next = append(next, replacement...)
		next = append(next, lines[end:]...)
		return strings.TrimRight(strings.Join(next, "\n"), "\n") + "\n"
	}
	if len(lines) == 1 && lines[0] == "" {
		return strings.Join(replacement, "\n") + "\n"
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n\n" + strings.Join(replacement, "\n") + "\n"
}

func deleteMarkdownSection(doc, section string) string {
	lines := splitMarkdownLines(strings.TrimRight(doc, "\r\n"))
	start, end := findMarkdownSection(lines, section)
	if start < 0 {
		return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	}
	next := append([]string{}, lines[:start]...)
	next = append(next, lines[end:]...)
	return strings.TrimRight(strings.Join(next, "\n"), "\n") + "\n"
}

func markdownSection(doc, section string) string {
	lines := splitMarkdownLines(strings.TrimRight(doc, "\r\n"))
	start, end := findMarkdownSection(lines, section)
	if start < 0 || start+1 >= end {
		return ""
	}
	return strings.Join(lines[start+1:end], "\n")
}

func findMarkdownSection(lines []string, section string) (int, int) {
	needle := strings.ToLower(strings.TrimSpace(section))
	start := -1
	for i, line := range lines {
		heading, ok := h2HeadingName(line)
		if !ok {
			continue
		}
		if strings.ToLower(heading) == needle {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if _, ok := h2HeadingName(lines[i]); ok {
			end = i
			break
		}
	}
	return start, end
}

func h2HeadingName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")), true
}

func splitMarkdownLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

func numberLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
	}
	return b.String()
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func editTextInEditor(initial string) (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return "", fmt.Errorf("$EDITOR is not set")
	}
	tmp, err := os.CreateTemp("", "dotfiles-agents-section-*.md")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.WriteString(initial); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := runEditor(context.Background(), editor, path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func runEditor(ctx context.Context, editor, path string) error {
	name, args, err := resolveEditorInvocation(editor)
	if err != nil {
		return err
	}
	args = append(args, path)
	// The editor is intentionally user-controlled via $EDITOR, but it is
	// resolved to an executable path and run without shell expansion.
	// #nosec G204
	cmd := osexec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func resolveEditorInvocation(editor string) (string, []string, error) {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("$EDITOR is not set")
	}
	for _, part := range parts {
		if strings.ContainsAny(part, "\x00\r\n") {
			return "", nil, fmt.Errorf("editor command contains an invalid control character")
		}
	}
	name := parts[0]
	if strings.ContainsRune(name, os.PathSeparator) {
		if !filepath.IsAbs(name) {
			return "", nil, fmt.Errorf("editor path %q must be absolute", name)
		}
		return filepath.Clean(name), parts[1:], nil
	}
	resolved, err := osexec.LookPath(name)
	if err != nil {
		return "", nil, fmt.Errorf("editor %q not found in PATH", name)
	}
	return resolved, parts[1:], nil
}

func unifiedTextDiff(from, to, a, b string) string {
	if a == b {
		return ""
	}
	aLines := diffSplitLines(a)
	bLines := diffSplitLines(b)
	ops := diffOps(aLines, bLines)
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@\n", from, to)
	for _, op := range ops {
		switch op.kind {
		case ' ':
			fmt.Fprintf(&out, " %s\n", op.text)
		case '-':
			fmt.Fprintf(&out, "-%s\n", op.text)
		case '+':
			fmt.Fprintf(&out, "+%s\n", op.text)
		}
	}
	return out.String()
}

type diffOp struct {
	kind byte
	text string
}

func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: ' ', text: a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{kind: '-', text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: '-', text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: '+', text: b[j]})
	}
	return ops
}

func diffSplitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
