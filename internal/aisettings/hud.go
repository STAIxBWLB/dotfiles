package aisettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/claudecfg"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// HUDMarker tags the statusLine command dot writes into
// ~/.claude/settings.json. Claude Code runs the statusLine command through a
// shell, so a trailing comment is inert and lets us tell our own entry from
// one another tool installed (GSD ships gsd-statusline.js, for example).
// Same discipline as guard.HookMarker.
const HUDMarker = "# dot-hud"

// claudeStatusLineCommand is the marked command dot installs.
const claudeStatusLineCommand = "~/.claude/statusline-dot.py " + HUDMarker

// isDotStatusLine reports whether an existing statusLine command belongs to
// dot. The unmarked legacy spelling counts so upgrades adopt their own entry
// instead of treating it as a foreign owner.
func isDotStatusLine(command string) bool {
	return strings.Contains(command, HUDMarker) ||
		strings.TrimSpace(command) == "~/.claude/statusline-dot.py"
}

var defaultCodexStatusLine = []string{
	"model-with-reasoning",
	"git-branch",
	"context-remaining",
	"total-input-tokens",
	"total-output-tokens",
	"five-hour-limit",
	"weekly-limit",
}

const claudeHUDScript = `#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import time


def pct(value):
    if value is None:
        return "n/a"
    try:
        return f"{int(round(float(value)))}%"
    except (TypeError, ValueError):
        return "n/a"


def duration(seconds):
    try:
        seconds = max(0, int(seconds))
    except (TypeError, ValueError):
        return "n/a"
    days, rem = divmod(seconds, 86400)
    hours, rem = divmod(rem, 3600)
    minutes = rem // 60
    if days:
        return f"{days}d{hours:02d}h"
    if hours:
        return f"{hours}h{minutes:02d}m"
    return f"{minutes}m"


def reset_in(limit):
    if not isinstance(limit, dict):
        return ""
    resets_at = limit.get("resets_at")
    if not resets_at:
        return ""
    return f"({duration(float(resets_at) - time.time())})"


def git_branch(cwd):
    if not cwd:
        return ""
    try:
        out = subprocess.check_output(
            ["git", "-C", cwd, "branch", "--show-current"],
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=0.2,
        ).strip()
        return out
    except Exception:
        return ""


try:
    data = json.load(sys.stdin)
except Exception:
    data = {}

model = data.get("model", {})
name = model.get("display_name") or model.get("id") or "claude"
effort = data.get("effort", {}).get("level") or ""
cwd = data.get("workspace", {}).get("current_dir") or data.get("cwd") or ""
branch = git_branch(cwd)
ctx = data.get("context_window", {})
rate = data.get("rate_limits", {})
five = rate.get("five_hour", {})
week = rate.get("seven_day", {})
session_ms = data.get("cost", {}).get("total_duration_ms") or 0

parts = [name]
if effort:
    parts.append(effort)
if branch:
    parts.append(branch)
parts.append(f"ctx:{pct(ctx.get('used_percentage'))}")
parts.append(f"5h:{pct(five.get('used_percentage'))}{reset_in(five)}")
parts.append(f"wk:{pct(week.get('used_percentage'))}{reset_in(week)}")
parts.append(f"session:{duration(int(session_ms) / 1000)}")

print(" | ".join(parts))
`

// HUDManager manages dot-native status lines for Claude Code and Codex.
type HUDManager struct {
	Runner  *dotexec.Runner
	HomeDir string
}

// HUDOptions controls HUD Apply.
type HUDOptions struct {
	Tools  []string
	DryRun bool
	// Force takes over a statusLine currently owned by another tool.
	// Without it a foreign statusLine is preserved and reported as drift.
	Force bool
}

// HUDResult summarizes HUD Apply.
type HUDResult struct {
	Items  []HUDItem
	DryRun bool
}

// HUDItem reports one managed HUD target.
type HUDItem struct {
	ToolID     string
	TargetPath string
	Drift      string
	Changed    bool
	Detail     string
}

// NewHUDManager returns a HUD manager rooted at homeDir.
func NewHUDManager(runner *dotexec.Runner, homeDir string) *HUDManager {
	return &HUDManager{Runner: runner, HomeDir: homeDir}
}

// Status reports whether selected HUD targets match dot-native defaults.
func (m *HUDManager) Status(tools []string) ([]HUDItem, error) {
	ids, err := resolveHUDToolIDs(tools)
	if err != nil {
		return nil, err
	}
	var out []HUDItem
	for _, id := range ids {
		switch id {
		case "codex":
			out = append(out, m.codexStatus())
		case "claude":
			item, err := m.claudeStatus()
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
	}
	return out, nil
}

// Apply writes selected HUD targets.
func (m *HUDManager) Apply(opts HUDOptions) (*HUDResult, error) {
	ids, err := resolveHUDToolIDs(opts.Tools)
	if err != nil {
		return nil, err
	}
	effectiveDryRun := opts.DryRun || m.runner().DryRun
	result := &HUDResult{DryRun: effectiveDryRun}
	for _, id := range ids {
		var item HUDItem
		switch id {
		case "codex":
			item, err = m.applyCodexHUD(effectiveDryRun)
		case "claude":
			item, err = m.applyClaudeHUD(effectiveDryRun, opts.Force)
		}
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (m *HUDManager) codexStatus() HUDItem {
	path := m.codexConfigPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return HUDItem{ToolID: "codex", TargetPath: path, Drift: "missing", Detail: "config missing"}
	}
	if err != nil {
		return HUDItem{ToolID: "codex", TargetPath: path, Drift: "error", Detail: err.Error()}
	}
	next := patchCodexStatusLine(string(data))
	if next == string(data) {
		return HUDItem{ToolID: "codex", TargetPath: path, Drift: "in-sync"}
	}
	return HUDItem{ToolID: "codex", TargetPath: path, Drift: "out-of-sync", Detail: "tui.status_line differs"}
}

func (m *HUDManager) claudeStatus() (HUDItem, error) {
	settingsPath := claudecfg.SettingsPath(m.homeDir())
	scriptPath := m.claudeScriptPath()
	scriptOK := false
	if data, err := os.ReadFile(scriptPath); err == nil && string(data) == claudeHUDScript {
		scriptOK = true
	}
	settingsOK, detail, err := m.claudeSettingsStatus()
	if err != nil {
		return HUDItem{}, err
	}
	item := HUDItem{ToolID: "claude", TargetPath: settingsPath, Drift: "in-sync"}
	switch {
	case scriptOK && settingsOK:
		return item, nil
	case detail != "":
		item.Drift = "out-of-sync"
		item.Detail = detail
	default:
		item.Drift = "out-of-sync"
		item.Detail = "statusline script differs"
	}
	return item, nil
}

func (m *HUDManager) applyCodexHUD(dryRun bool) (HUDItem, error) {
	path := m.codexConfigPath()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return HUDItem{}, fmt.Errorf("read %s: %w", path, err)
	}
	next := patchCodexStatusLine(string(data))
	changed := string(data) != next
	item := HUDItem{ToolID: "codex", TargetPath: path, Changed: changed, Drift: "in-sync"}
	if changed {
		item.Drift = "out-of-sync"
		item.Detail = "write tui.status_line"
	}
	if changed && !dryRun {
		// Codex rewrites config.toml continuously; an atomic rename write
		// guarantees it never reads a torn file. A write it makes between our
		// read and rename can still be lost — the next codex write self-heals.
		if _, err := fileutil.EnsureFileAtomic(m.runner(), m.homeDir(), path, []byte(next), 0o600); err != nil {
			return HUDItem{}, err
		}
	}
	return item, nil
}

// claudePreviewDetail is the both-halves detail: what a run reports when the
// settings and the script are BOTH out of date. It is shared by the preview
// and the write result, which is why the fully-stale case reads the same
// either way.
const claudePreviewDetail = "write statusLine and statusline-dot.py"

// claudeConflictItem is the item BOTH statusLine refusals report: the
// pre-lock check in mergedClaudeSettings, and the recheck the Mutate closure
// performs under the lock. One constructor with two real callers, so "the
// two refusals report identically" is a property of the code rather than a
// convention two hand-built literals happen to agree on.
func claudeConflictItem(settingsPath, reason string) HUDItem {
	return HUDItem{ToolID: "claude", TargetPath: settingsPath, Changed: false, Drift: "out-of-sync", Detail: reason}
}

// claudeHUDItem shapes the non-refusal item. A detail is only meaningful
// when something changed, which is the one rule both callers share.
func claudeHUDItem(settingsPath string, changed bool, detail string) HUDItem {
	item := HUDItem{ToolID: "claude", TargetPath: settingsPath, Changed: changed, Drift: "in-sync"}
	if changed {
		item.Drift = "out-of-sync"
		item.Detail = detail
	}
	return item
}

// claudeHUDDetail names the halves a run is reporting on: the ones a write
// actually landed, or the ones a preview would write. Reporting both when
// only one is in play is the misreport this function exists to prevent, and
// it is the same misreport on either path — which is why the name no longer
// claims a write happened.
func claudeHUDDetail(settingsHalf, scriptHalf bool) string {
	switch {
	case settingsHalf && scriptHalf:
		return claudePreviewDetail
	case settingsHalf:
		return "write statusLine"
	case scriptHalf:
		return "write statusline-dot.py"
	}
	return ""
}

// applyClaudeHUD installs dot's statusLine block and the script it points at,
// and reports what the writes actually did rather than what the pre-lock read
// predicted.
//
// Write order is script first, then the settings Mutate, and that is a
// decision rather than an accident. A failed settings write leaves an orphan
// statusline-dot.py that nothing references: inert, idempotent, and healed by
// the next successful run. The reverse order would leave a statusLine entry
// pointing at a stale or absent script, which is a visibly broken status line
// in Claude Code. The safe half is the one that already lands first.
//
// ponytail: known ceiling. The pair is still not atomic — a failed settings
// write leaves that orphan script behind. Closing it needs a rollback or a
// staged two-phase write, which is a larger change than the reporting fix
// this is. STATE.md WR-02's reporting clause lands here; its atomicity clause
// is accepted, not closed.
//
// The refusal branch's provenance: a Codex review on PR #69 found that a
// closure declining a foreign statusLine still rendered as `wrote`, and the
// thread was answered as "Phase 5 BUG-03 closes it". That answer was
// imprecise. BUG-03's retry fires on a hash MISMATCH, while a foreign
// statusLine is a REFUSAL: the closure returns ErrNoChange and Mutate
// short-circuits to (false, nil) before any retry. Threading that result into
// the item is what actually closes it.
func (m *HUDManager) applyClaudeHUD(dryRun, force bool) (HUDItem, error) {
	settingsPath := claudecfg.SettingsPath(m.homeDir())
	scriptPath := m.claudeScriptPath()
	settings, refused, err := m.mergedClaudeSettings(force)
	if err != nil {
		return HUDItem{}, err
	}
	if refused != "" {
		// Another tool owns statusLine. Leave it and report, rather than
		// silently replacing an entry dot does not own.
		return claudeConflictItem(settingsPath, refused), nil
	}
	settingsChanged := true
	if current, err := os.ReadFile(settingsPath); err == nil {
		settingsChanged = !jsonBytesEqual(current, settings)
	} else if !os.IsNotExist(err) {
		return HUDItem{}, fmt.Errorf("read %s: %w", settingsPath, err)
	}
	scriptChanged := true
	if current, err := os.ReadFile(scriptPath); err == nil {
		scriptChanged = string(current) != claudeHUDScript
	} else if !os.IsNotExist(err) {
		return HUDItem{}, fmt.Errorf("read %s: %w", scriptPath, err)
	}
	changed := settingsChanged || scriptChanged
	if !changed || dryRun {
		return claudeHUDItem(settingsPath, changed, claudeHUDDetail(settingsChanged, scriptChanged)), nil
	}

	wroteScript, err := fileutil.EnsureFile(m.runner(), m.homeDir(), scriptPath, []byte(claudeHUDScript), 0o755)
	if err != nil {
		return HUDItem{}, err
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		return HUDItem{}, fmt.Errorf("chmod %s: %w", scriptPath, err)
	}
	// The settings write goes through claudecfg.Mutate, which re-reads and
	// re-hashes under its PID lock. The closure re-checks ownership there and
	// declines via ErrNoChange if a foreign statusLine appeared since the
	// check above; refusedUnderLock carries that reason back out, because the
	// closure computes it and would otherwise throw it away. It is assigned
	// on every pass, so a retry's verdict replaces the earlier one.
	var refusedUnderLock string
	wroteSettings, err := claudecfg.Mutate(m.runner(), m.homeDir(), func(current map[string]any) error {
		refusedUnderLock = applyDotStatusLine(current, force)
		if refusedUnderLock != "" {
			return claudecfg.ErrNoChange
		}
		return nil
	})
	if err != nil {
		return HUDItem{}, err
	}
	if refusedUnderLock != "" {
		return claudeConflictItem(settingsPath, refusedUnderLock), nil
	}
	return claudeHUDItem(settingsPath, wroteScript || wroteSettings, claudeHUDDetail(wroteSettings, wroteScript)), nil
}

func (m *HUDManager) claudeSettingsStatus() (bool, string, error) {
	path := claudecfg.SettingsPath(m.homeDir())
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, "settings missing", nil
	}
	settings, err := claudecfg.Read(m.homeDir())
	if err != nil {
		// The status path is read-only: a file dot cannot parse is drift to
		// report, not a reason to abort and hide every other HUD target.
		if errors.Is(err, claudecfg.ErrInvalidJSON) {
			return false, "settings json invalid", nil
		}
		return false, "", err
	}
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return false, "statusLine missing", nil
	}
	command, _ := sl["command"].(string)
	if !isDotStatusLine(command) {
		return false, foreignStatusLineReason(command), nil
	}
	if sl["type"] != "command" || command != claudeStatusLineCommand {
		return false, "statusLine command differs", nil
	}
	return true, "", nil
}

// foreignStatusLineReason names the tool that currently owns statusLine so the
// drift report is actionable rather than a bare "differs".
func foreignStatusLineReason(command string) string {
	owner := strings.TrimSpace(command)
	if owner == "" {
		return "statusLine owned by another tool"
	}
	if fields := strings.Fields(owner); len(fields) > 0 {
		owner = filepath.Base(strings.Trim(fields[len(fields)-1], `"'`))
	}
	return fmt.Sprintf("statusLine owned by another tool (%s); rerun with --force to take it over", owner)
}

// applyDotStatusLine applies dot's statusLine block to a settings map. A
// statusLine installed by another tool is left alone unless force is set;
// the return value reports that refusal (empty when the block was applied).
func applyDotStatusLine(settings map[string]any, force bool) string {
	if existing, ok := settings["statusLine"].(map[string]any); ok && !force {
		command, _ := existing["command"].(string)
		if !isDotStatusLine(command) {
			return foreignStatusLineReason(command)
		}
	}
	settings["statusLine"] = map[string]any{
		"type":            "command",
		"command":         claudeStatusLineCommand,
		"refreshInterval": float64(5),
	}
	return ""
}

// mergedClaudeSettings returns the settings file with dot's statusLine block
// applied. A statusLine installed by another tool is left alone unless force
// is set; the second return value reports that refusal.
func (m *HUDManager) mergedClaudeSettings(force bool) ([]byte, string, error) {
	settings, err := claudecfg.Read(m.homeDir())
	if err != nil {
		return nil, "", err
	}
	refused := applyDotStatusLine(settings, force)
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, "", err
	}
	return append(data, '\n'), refused, nil
}

func patchCodexStatusLine(content string) string {
	desired := codexStatusLineTOML()
	content = strings.ReplaceAll(content, "\r\n", "\n")
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return "[tui]\n" + desired + "\n"
	}
	lines := strings.Split(trimmed, "\n")
	start, end := findTOMLTable(lines, "tui")
	if start < 0 {
		return trimmed + "\n\n[tui]\n" + desired + "\n"
	}
	statusStart, statusEnd := findTOMLKey(lines, start+1, end, "status_line")
	if statusStart >= 0 {
		next := append([]string{}, lines[:statusStart]...)
		next = append(next, desired)
		next = append(next, lines[statusEnd:]...)
		return strings.Join(next, "\n") + "\n"
	}
	next := append([]string{}, lines[:end]...)
	next = append(next, desired)
	next = append(next, lines[end:]...)
	return strings.Join(next, "\n") + "\n"
}

func codexStatusLineTOML() string {
	quoted := make([]string, 0, len(defaultCodexStatusLine))
	for _, part := range defaultCodexStatusLine {
		quoted = append(quoted, fmt.Sprintf("%q", part))
	}
	return "status_line = [" + strings.Join(quoted, ", ") + "]"
}

func findTOMLTable(lines []string, name string) (int, int) {
	start := -1
	tablePattern := regexp.MustCompile(`^\s*\[` + regexp.QuoteMeta(name) + `\]\s*(#.*)?$`)
	anyTablePattern := regexp.MustCompile(`^\s*\[[^\]]+\]\s*(#.*)?$`)
	for i, line := range lines {
		if tablePattern.MatchString(line) {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if anyTablePattern.MatchString(lines[i]) {
			end = i
			break
		}
	}
	return start, end
}

func findTOMLKey(lines []string, start, end int, key string) (int, int) {
	keyPattern := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	for i := start; i < end; i++ {
		if !keyPattern.MatchString(lines[i]) {
			continue
		}
		keyEnd := i + 1
		balance := strings.Count(lines[i], "[") - strings.Count(lines[i], "]")
		for keyEnd < end && balance > 0 {
			balance += strings.Count(lines[keyEnd], "[") - strings.Count(lines[keyEnd], "]")
			keyEnd++
		}
		return i, keyEnd
	}
	return -1, -1
}

func jsonBytesEqual(a, b []byte) bool {
	var am, bm any
	if json.Unmarshal(a, &am) != nil || json.Unmarshal(b, &bm) != nil {
		return bytes.Equal(a, b)
	}
	aj, _ := json.Marshal(am)
	bj, _ := json.Marshal(bm)
	return bytes.Equal(aj, bj)
}

func resolveHUDToolIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{"claude", "codex"}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || seen[id] {
			continue
		}
		if id != "claude" && id != "codex" {
			return nil, fmt.Errorf("unknown HUD tool %q", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func (m *HUDManager) runner() *dotexec.Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return dotexec.NewRunner(false, slog.Default())
}

func (m *HUDManager) homeDir() string {
	if m.HomeDir != "" {
		return m.HomeDir
	}
	home, _ := os.UserHomeDir()
	return home
}

func (m *HUDManager) codexConfigPath() string {
	return filepath.Join(m.homeDir(), ".codex", "config.toml")
}

func (m *HUDManager) claudeScriptPath() string {
	return filepath.Join(m.homeDir(), ".claude", "statusline-dot.py")
}
