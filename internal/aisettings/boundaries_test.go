package aisettings

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// pathClass classifies what a Path-suffixed declaration does with the path
// it produces, which determines what the boundary test asserts about it.
type pathClass int

const (
	// classHomeWrite resolves a write target under the user's home; the
	// resolved path must stay under a root docs/BOUNDARIES.md permits.
	classHomeWrite pathClass = iota
	// classCallerRooted resolves under a root the caller supplied; the
	// resolved path must stay under the root it was given.
	classCallerRooted
	// classReadOnly resolves a path dot only reads (diagnostics); no
	// containment claim is asserted.
	classReadOnly
	// classTransform takes a path or document as input and returns a
	// transformed string; it resolves no write root.
	classTransform
)

// pathBoundaryEntry is one inventory row: a path-producing declaration and
// its classification.
type pathBoundaryEntry struct {
	name  string
	class pathClass
}

// pathBoundaryTable lists every function and method in package aisettings
// whose name ends in "Path", ordered alphabetically by qualified name so the
// table reads as an inventory. Methods are qualified with their receiver
// type so the two SSOTPath methods stay distinct.
//
// ponytail: the inventory keys on the "Path" name suffix, so a resolver
// named without it escapes the table. The mitigation is the explicit
// extraPathProducers list below plus the review habit of classifying new
// resolvers, not a cleverer matcher.
var pathBoundaryTable = []pathBoundaryEntry{
	{"AIEventsPath", classHomeWrite},
	{"AgentsManager.SSOTDirPath", classCallerRooted},
	{"AgentsManager.SSOTPath", classCallerRooted},
	{"AgentsManager.StatePath", classHomeWrite},
	{"AgentsManager.TargetPath", classHomeWrite},
	{"AgentsManager.backupTargetPath", classHomeWrite},
	{"AgentsManager.legacyStatePath", classCallerRooted},
	{"AmemManager.AntigravityMCPPath", classHomeWrite},
	{"AmemManager.CopilotMCPPath", classHomeWrite},
	{"AmemManager.KimiMCPPath", classHomeWrite},
	{"AmemManager.KiroMCPPath", classHomeWrite},
	{"AmemManager.PiAgentsPath", classHomeWrite},
	{"AmemManager.QwenMCPPath", classHomeWrite},
	{"CoauthorGuardManager.SSOTPath", classHomeWrite},
	{"CoauthorGuardManager.dotManagedHooksPath", classTransform},
	{"CoauthorGuardManager.gitConfigPath", classHomeWrite},
	{"CoauthorGuardManager.hookPath", classHomeWrite},
	{"Engine.LatestPointerPath", classCallerRooted},
	{"Engine.VersionPath", classCallerRooted},
	{"Engine.copyMaterializedPath", classTransform},
	{"HUDManager.claudeScriptPath", classHomeWrite},
	{"HUDManager.codexConfigPath", classHomeWrite},
	{"InstructionPath", classHomeWrite},
	{"PreserveForeignHooksPath", classTransform},
	{"RetireOptions.archivePath", classHomeWrite},
	{"SkillsManager.DefaultMaruSSOTPath", classReadOnly},
	{"VmemManager.ClaudeConfigPath", classReadOnly}, // status and the retire dry run only read it; Claude Code owns it
	{"VmemManager.CodexConfigPath", classReadOnly},  // status and the retire dry run only read it; the person owns it
	{"VmemManager.CopilotMCPPath", classHomeWrite},
	{"VmemManager.KiroMCPPath", classHomeWrite},
	{"canonicalSkillPath", classTransform},
	{"normalizeGitPath", classTransform},
	{"validateMaterializedPath", classTransform},
}

// extraPathProducers lists known declarations that resolve a write path but
// whose names do not end in "Path", so the suffix-keyed inventory cannot see
// them. Each is driven in Part B like any other entry. Keep this list short;
// prefer naming new resolvers with the Path suffix.
var extraPathProducers = []pathBoundaryEntry{
	{"AgentsManager.ssotDir", classCallerRooted},
	{"Engine.AIConfigRoot", classCallerRooted},
	{"Engine.HostRoot", classCallerRooted},
}

// allowedBoundaryRoots are the write roots docs/BOUNDARIES.md grants, as
// tilde-prefixed literals. Every literal must appear verbatim in the
// document (Part C), and every home-write resolver must stay under one of
// them (Part B).
var allowedBoundaryRoots = []string{
	"~/.claude/CLAUDE.md",
	"~/.claude/statusline-dot.py",
	"~/.config/dotfiles/agents",
	"~/.local/share/dotfiles",
	"~/.config/git/config",
	"~/.config/git/hooks/commit-msg",
	"~/.claude-mem",
	// A root ending in "-" is a name prefix: the dated archive sits beside ~/.claude-mem.
	"~/.claude-mem.archive-",
	"~/Library/LaunchAgents/com.dotfiles.claude-mem-bridge.plist",
	"~/Library/LaunchAgents/com.dotfiles.claude-mem-sync.plist",
	"~/.claude/plugins/marketplaces/thedotmack",
	"~/.kimi-code/mcp.json",
	"~/.qwen/settings.json",
	"~/.gemini/config/mcp_config.json",
	"~/.kiro/settings/mcp.json",
	"~/.copilot/mcp-config.json",
	"~/.codex/config.toml",
	// The agents fan-out targets, one root per registry entry. Listing each
	// one is what makes a newly registered tool a documented grant rather
	// than a silent one (see TestAgentTargetsResolveUnderAllowedRoots).
	"~/.codex/AGENTS.md",
	"~/.config/opencode/AGENTS.md",
	"~/.kiro/steering/AGENTS.md",
	"~/.kimi-code/AGENTS.md",
	"~/.pi/agent/AGENTS.md",
	"~/.qwen/AGENTS.md",
	"~/.gemini/GEMINI.md",
	"~/.copilot/copilot-instructions.md",
	"~/.aider.conf.md",
}

// expandBoundaryRoot expands a tilde-prefixed boundary root against home.
func expandBoundaryRoot(root, home string) string {
	if root == "~" {
		return home
	}
	if strings.HasPrefix(root, "~/") {
		return filepath.Join(home, root[2:])
	}
	return root
}

// withinBoundary reports whether path is root itself or a child of root.
// Copied from within() in internal/guard/freeze.go:59 rather than imported:
// importing guard from aisettings would invert the dependency the claudecfg
// seam exists to avoid. The separator in the prefix check is what rejects a
// sibling whose name merely extends the root (~/.claude-mem vs ~/.claude).
func withinBoundary(path, root string) bool {
	// A root ending in "-" names a prefix (the dated claude-mem archive), not a directory.
	if strings.HasSuffix(root, "-") {
		return strings.HasPrefix(path, root)
	}
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// collectPathDeclarations parses the Go sources in dir (test files excluded:
// the boundary constrains production code) and returns the qualified name of
// every function and method whose name ends in "Path", sorted. Files are
// parsed individually rather than via parser.ParseDir, which is deprecated.
func collectPathDeclarations(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasSuffix(fn.Name.Name, "Path") {
				continue
			}
			names = append(names, qualifiedDeclName(fn))
		}
	}
	sort.Strings(names)
	return names, nil
}

// qualifiedDeclName returns the declaration name, qualified with the
// receiver type for methods.
func qualifiedDeclName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if ident, ok := recv.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// TestEveryPathFuncIsInTheBoundaryTable verifies that every Path-suffixed
// declaration in this package is classified in pathBoundaryTable, and that
// every table entry still exists in the source, so both a new function and a
// deleted one fail. Without this guarantee a new path resolver — for
// example one moved into the package by the Phase 3 ai_cmd.go slice —
// escapes the write-boundary contract in docs/BOUNDARIES.md.
func TestEveryPathFuncIsInTheBoundaryTable(t *testing.T) {
	collected, err := collectPathDeclarations(".")
	if err != nil {
		t.Fatalf("parse package source: %v", err)
	}
	if len(collected) == 0 {
		t.Fatal("inventory collected zero Path-suffixed declarations: a parse that silently returns nothing must not report a green boundary")
	}
	inSource := map[string]bool{}
	for _, name := range collected {
		inSource[name] = true
	}
	inTable := map[string]bool{}
	for _, entry := range pathBoundaryTable {
		inTable[entry.name] = true
	}
	var unclassified, stale []string
	for name := range inSource {
		if !inTable[name] {
			unclassified = append(unclassified, name)
		}
	}
	for _, entry := range pathBoundaryTable {
		if !inSource[entry.name] {
			stale = append(stale, entry.name)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	if len(unclassified) > 0 {
		t.Errorf("unclassified Path declarations: %s\nclassify each against a write root in docs/BOUNDARIES.md and add it to pathBoundaryTable", strings.Join(unclassified, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("table entries with no matching declaration (deleted or renamed?): %s", strings.Join(stale, ", "))
	}
}

// TestPathsResolveUnderAllowedRoots verifies that every home-write resolver,
// driven against a temp home so the invoking user's real home is never
// touched, resolves under one of the roots docs/BOUNDARIES.md permits, and
// that every caller-rooted resolver stays under the root it was given.
// Without this guarantee aisettings can grow a write target the prose
// boundary never granted.
func TestPathsResolveUnderAllowedRoots(t *testing.T) {
	home := t.TempDir()
	callerRoot := t.TempDir()
	entries := append([]pathBoundaryEntry{}, pathBoundaryTable...)
	entries = append(entries, extraPathProducers...)
	for _, entry := range entries {
		t.Run(entry.name, func(t *testing.T) {
			switch entry.class {
			case classReadOnly:
				t.Skip("read-only: resolves a root dot only reads; docs/BOUNDARIES.md forbids writing it")
			case classTransform:
				t.Skip("transform: takes a path or document as input; resolves no write root")
			}
			switch entry.class {
			case classHomeWrite:
				got := resolveHomeWrite(t, entry.name, home)
				for _, root := range allowedBoundaryRoots {
					if withinBoundary(got, expandBoundaryRoot(root, home)) {
						return
					}
				}
				t.Errorf("%s = %q resolves outside every root docs/BOUNDARIES.md permits", entry.name, got)
			case classCallerRooted:
				got := resolveCallerRooted(t, entry.name, home, callerRoot)
				if !withinBoundary(got, callerRoot) {
					t.Errorf("%s = %q resolves outside caller-supplied root %q", entry.name, got, callerRoot)
				}
			}
		})
	}
}

// resolveHomeWrite invokes the named home-write resolver against home.
// Entries taking an argument get a fixed representative value chosen so the
// result is deterministic; where a method returns an error for an unknown
// input, the error path is asserted rather than inventing a value.
func resolveHomeWrite(t *testing.T, name, home string) string {
	t.Helper()
	switch name {
	case "InstructionPath":
		return InstructionPath(home, "codex", true, filepath.Join(home, ".codex", "AGENTS.md"))
	case "AIEventsPath":
		return AIEventsPath(home)
	case "AgentsManager.TargetPath":
		m := &AgentsManager{HomeDir: home}
		got, err := m.TargetPath("claude")
		if err != nil {
			t.Fatalf("TargetPath(claude): %v", err)
		}
		if _, err := m.TargetPath("bogus-tool"); err == nil {
			t.Error("TargetPath(bogus-tool) = nil error, want an error for an unknown tool")
		}
		return got
	case "AgentsManager.StatePath":
		return (&AgentsManager{HomeDir: home}).StatePath()
	case "AgentsManager.backupTargetPath":
		return (&AgentsManager{HomeDir: home}).backupTargetPath("claude")
	case "AmemManager.CopilotMCPPath":
		return (&AmemManager{HomeDir: home}).CopilotMCPPath()
	case "AmemManager.KimiMCPPath":
		return (&AmemManager{HomeDir: home}).KimiMCPPath()
	case "AmemManager.QwenMCPPath":
		return (&AmemManager{HomeDir: home}).QwenMCPPath()
	case "AmemManager.AntigravityMCPPath":
		return (&AmemManager{HomeDir: home}).AntigravityMCPPath()
	case "AmemManager.PiAgentsPath":
		return (&AmemManager{HomeDir: home}).PiAgentsPath()
	case "AmemManager.KiroMCPPath":
		return (&AmemManager{HomeDir: home}).KiroMCPPath()
	case "VmemManager.KiroMCPPath":
		return (&VmemManager{HomeDir: home}).KiroMCPPath()
	case "VmemManager.CopilotMCPPath":
		return (&VmemManager{HomeDir: home}).CopilotMCPPath()
	case "RetireOptions.archivePath":
		// The destination of `dot ai memory retire-claude-mem --apply`: a dated sibling
		// of ~/.claude-mem, never the data directory itself and never a deletion.
		return (RetireOptions{HomeDir: home, Now: func() time.Time { return time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC) }}).archivePath()
	case "CoauthorGuardManager.SSOTPath":
		return (&CoauthorGuardManager{HomeDir: home}).SSOTPath()
	case "CoauthorGuardManager.gitConfigPath":
		return (&CoauthorGuardManager{HomeDir: home}).gitConfigPath()
	case "CoauthorGuardManager.hookPath":
		return (&CoauthorGuardManager{HomeDir: home}).hookPath()
	case "HUDManager.claudeScriptPath":
		return (&HUDManager{HomeDir: home}).claudeScriptPath()
	case "HUDManager.codexConfigPath":
		return (&HUDManager{HomeDir: home}).codexConfigPath()
	default:
		t.Fatalf("no home-write invocation for %s; extend resolveHomeWrite", name)
		return ""
	}
}

// resolveCallerRooted invokes the named caller-rooted resolver against root.
func resolveCallerRooted(t *testing.T, name, home, root string) string {
	t.Helper()
	switch name {
	case "AgentsManager.SSOTDirPath":
		return (&AgentsManager{HomeDir: home, SSOTDir: root}).SSOTDirPath()
	case "AgentsManager.SSOTPath":
		return (&AgentsManager{HomeDir: home, SSOTDir: root}).SSOTPath()
	case "AgentsManager.legacyStatePath":
		return (&AgentsManager{HomeDir: home, SSOTDir: root}).legacyStatePath()
	case "AgentsManager.ssotDir":
		return (&AgentsManager{HomeDir: home, SSOTDir: root}).ssotDir()
	case "Engine.AIConfigRoot":
		return (&Engine{Root: root, Hostname: "boundary-test"}).AIConfigRoot()
	case "Engine.HostRoot":
		return (&Engine{Root: root, Hostname: "boundary-test"}).HostRoot()
	case "Engine.LatestPointerPath":
		return (&Engine{Root: root, Hostname: "boundary-test"}).LatestPointerPath()
	case "Engine.VersionPath":
		return (&Engine{Root: root, Hostname: "boundary-test"}).VersionPath("v1")
	default:
		t.Fatalf("no caller-rooted invocation for %s; extend resolveCallerRooted", name)
		return ""
	}
}

// TestAgentTargetsResolveUnderAllowedRoots drives AgentsManager.TargetPath
// over every registered agent, not just the one representative the
// pathBoundaryTable row resolves. Registering a tool adds a home write
// target, and the boundary table cannot see it: qwen and pi each added one
// while the table kept asserting "claude". Without this the prose boundary
// grants a path nothing checks.
func TestAgentTargetsResolveUnderAllowedRoots(t *testing.T) {
	home := t.TempDir()
	tools := RegisteredAgentTools()
	if len(tools) == 0 {
		t.Fatal("registry is empty: a green boundary must not come from an empty inventory")
	}
	m := &AgentsManager{HomeDir: home}
	for _, tool := range tools {
		t.Run(tool.ID, func(t *testing.T) {
			got, err := m.TargetPath(tool.ID)
			if err != nil {
				t.Fatalf("TargetPath(%s): %v", tool.ID, err)
			}
			for _, root := range allowedBoundaryRoots {
				if withinBoundary(got, expandBoundaryRoot(root, home)) {
					return
				}
			}
			t.Errorf("agent %s target %q resolves outside every root docs/BOUNDARIES.md permits; add its path to allowedBoundaryRoots and to docs/BOUNDARIES.md", tool.ID, got)
		})
	}
}

// TestAllowedRootsMatchBoundariesDoc verifies every allowed-root literal
// appears verbatim in docs/BOUNDARIES.md. The check is a substring match,
// not a structural parse: a substring check breaks when a root is deleted
// from the document — the event worth failing on — and survives a reformat,
// which is not.
func TestAllowedRootsMatchBoundariesDoc(t *testing.T) {
	if len(allowedBoundaryRoots) == 0 {
		t.Fatal("allowedBoundaryRoots is empty: the boundary must permit at least one root")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "BOUNDARIES.md"))
	if err != nil {
		t.Fatalf("read docs/BOUNDARIES.md: %v", err)
	}
	doc := string(data)
	for _, root := range allowedBoundaryRoots {
		if !strings.Contains(doc, root) {
			t.Errorf("allowed root %q not found verbatim in docs/BOUNDARIES.md", root)
		}
	}
}

// TestWithinRejectsSiblingPrefixRoot verifies the containment predicate
// accepts the root itself and a child of the root, and rejects a sibling
// directory whose name merely extends the root — the claude-mem data
// directory against the claude root, a live pair in this package rather
// than a synthetic one. Without this guarantee a raw prefix match would
// judge ~/.claude-mem to be inside ~/.claude.
func TestWithinRejectsSiblingPrefixRoot(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	claudeMem := filepath.Join(home, ".claude-mem")
	if !withinBoundary(claude, claude) {
		t.Error("withinBoundary(root, root) = false, want true: the allowed root itself must be accepted")
	}
	if !withinBoundary(filepath.Join(claude, "settings.json"), claude) {
		t.Error("withinBoundary(child, root) = false, want true")
	}
	if withinBoundary(claudeMem, claude) {
		t.Errorf("withinBoundary(%q, %q) = true, want false: a sibling whose name extends the root must be rejected", claudeMem, claude)
	}
}
