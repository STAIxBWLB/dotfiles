package aipolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeCodexRequiresExistingBindings(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{Agent: "codex", Home: home}
	grants, err := KnowledgeApprovals(runtime)
	if err != nil || len(grants) != 0 {
		t.Fatalf("%+v %v", grants, err)
	}
	native := []byte("[mcp_servers.vmem]\ncommand = \"/opt/bin/vmem\"\nargs = [\"mcp\"]\n[mcp_servers.amem]\ncommand = \"/opt/bin/amem\"\nargs = [\"mcp\"]\n")
	if err = os.WriteFile(filepath.Join(home, "config.toml"), native, 0600); err != nil {
		t.Fatal(err)
	}
	grants, err = KnowledgeApprovals(runtime)
	if err != nil || len(grants) != len(vaultKnowledgeTools)+len(memoryKnowledgeTools) {
		t.Fatalf("%+v %v", grants, err)
	}
	args, err := KnowledgeArgs(runtime, grants)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{"mcp_servers.vmem.tools.vmem_create.approval_mode", "mcp_servers.vmem.tools.vmem_move.approval_mode", "mcp_servers.amem.tools.amem_search.approval_mode", "mcp_servers.amem.tools.amem_work_state_write.approval_mode", "mcp_servers.amem.tools.amem_remember.approval_mode", "approval_mode=\"approve\""} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %s in %s", expected, joined)
		}
	}
	for _, excluded := range []string{"obsidian", "write_note", "delete_note", "move_file", "build_corpus", "prime_corpus", "command=", "url=", "claude-mem", "mcp-search", "plugins."} {
		if strings.Contains(joined, excluded) {
			t.Fatalf("unexpected %s", excluded)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || string(data) != string(native) {
		t.Fatal("native identity changed")
	}
}

// amem's grant set is its frozen P2 tool list and nothing a note can be deleted or moved with.
func TestKnowledgeMemoryToolsAreAmemsFrozenSurface(t *testing.T) {
	want := []string{"amem_search", "amem_show", "amem_timeline", "amem_graph", "amem_work_state_read", "amem_work_state_write", "amem_remember", "amem_vault_candidates", "amem_capacity"}
	if strings.Join(memoryKnowledgeTools, ",") != strings.Join(want, ",") {
		t.Fatalf("memory tools drifted from agent-mem src/mcp.ts: %v", memoryKnowledgeTools)
	}
	for _, name := range memoryKnowledgeTools {
		if !strings.HasPrefix(name, "amem_") || strings.Contains(name, "delete") || strings.Contains(name, "forget") || strings.Contains(name, "move") {
			t.Errorf("unsafe or foreign memory tool %q", name)
		}
	}
}

// AMEM_PACKAGE_DIR names an unpacked @staix/agent-mem tarball (`npm pack @staix/agent-mem@0.11.0`, then
// extract). When set, every allowlisted tool must be in that package's own MCP tool list, so a rename or a
// removal in a later amem release fails here and not at the cutover. A tool the package adds is not granted
// until it is added to the allowlist on purpose. Unset (CI), the frozen list above is the contract.
func TestKnowledgeMemoryToolsMatchThePublishedPackage(t *testing.T) {
	dir := os.Getenv("AMEM_PACKAGE_DIR")
	if dir == "" {
		t.Skip("AMEM_PACKAGE_DIR not set; run with an unpacked @staix/agent-mem tarball to compare")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "src", "mcp.ts"))
	if err != nil {
		t.Fatal(err)
	}
	var published []string
	for _, part := range strings.Split(string(raw), `tool("amem_`)[1:] {
		published = append(published, "amem_"+part[:strings.Index(part, `"`)])
	}
	have := map[string]bool{}
	for _, name := range published {
		have[name] = true
	}
	for _, name := range memoryKnowledgeTools {
		if !have[name] {
			t.Errorf("allowlisted %s is not in the published package (%v)", name, published)
		}
	}
}

// AC3: after retirement the old claude-mem names are refused outright, never mapped onto amem.
func TestKnowledgeRefusesTheRetiredClaudeMemServerNames(t *testing.T) {
	old := []KnowledgeApproval{
		{Server: "plugin_claude-mem_mcp-search", Tool: "search", ApprovalMode: "approve"},
		{Server: "plugin_claude-mem_mcp-search", Tool: "observation_add", ApprovalMode: "approve"},
		{Server: "mcp-search", Plugin: "claude-mem@claude-mem-local", Tool: "search", ApprovalMode: "approve"},
		{Server: "mcp-search", Plugin: "claude-mem@claude-mem-local", Tool: "observation_add", ApprovalMode: "approve"},
		{Server: "mcp-search", Tool: "search", ApprovalMode: "approve"},
	}
	for _, agent := range []string{"claude", "codex"} {
		for _, grant := range old {
			if _, err := KnowledgeArgs(Runtime{Agent: agent}, []KnowledgeApproval{grant}); err == nil {
				t.Errorf("%s accepted the retired grant %+v", agent, grant)
			}
		}
	}
	// amem's own names under a retired plugin binding are refused too: a plugin-scoped amem does not exist.
	if _, err := KnowledgeArgs(Runtime{Agent: "codex"}, []KnowledgeApproval{{Server: "amem", Plugin: "claude-mem@claude-mem-local", Tool: "amem_search", ApprovalMode: "approve"}}); err == nil {
		t.Error("accepted amem under a claude-mem plugin")
	}
	// and an old tool name on the amem server is not an amem tool.
	for _, tool := range []string{"search", "observation_add", "get_observations", "smart_search"} {
		if _, err := KnowledgeArgs(Runtime{Agent: "claude"}, []KnowledgeApproval{{Server: "amem", Tool: tool, ApprovalMode: "approve"}}); err == nil {
			t.Errorf("accepted the claude-mem tool name %q on amem", tool)
		}
	}
}

// An amem tool name asked of any other server is refused too: the allowlist is keyed on the server,
// so a retired claude-mem name can never be granted an amem tool by mapping.
func TestKnowledgeAmemToolsAreGrantedOnlyOnTheAmemServer(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, server := range []string{"mcp-search", "plugin_claude-mem_mcp-search", "claude-mem", "other", "obsidian"} {
			for _, tool := range []string{"amem_search", "amem_work_state_write", "amem_remember"} {
				grant := KnowledgeApproval{Server: server, Tool: tool, ApprovalMode: "approve"}
				if _, err := KnowledgeArgs(Runtime{Agent: agent}, []KnowledgeApproval{grant}); err == nil {
					t.Errorf("%s granted %s on server %q", agent, tool, server)
				}
			}
		}
		if _, err := KnowledgeArgs(Runtime{Agent: agent}, []KnowledgeApproval{{Server: "amem", Tool: "amem_search", ApprovalMode: "approve"}}); err != nil {
			t.Errorf("%s refused amem's own tool: %v", agent, err)
		}
	}
	// ask and deny modes grant nothing, whatever the server.
	if _, err := KnowledgeArgs(Runtime{Agent: "claude"}, []KnowledgeApproval{{Server: "amem", Tool: "amem_search", ApprovalMode: "ask"}}); err == nil {
		t.Error("a non-approve mode was granted")
	}
}

// A machine that still has only the claude-mem plugin enabled gets no memory grant: the binding is
// not recognized, so nothing is silently mapped, and nothing errors either.
func TestKnowledgeCodexWithOnlyTheOldPluginGrantsNothing(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := "[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n[plugins.\"claude-mem@claude-mem-local\".mcp_servers.mcp-search.tools.search]\napproval_mode = \"approve\"\n"
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	grants, err := KnowledgeApprovals(Runtime{Agent: "codex", Home: home})
	if err != nil || len(grants) != 0 {
		t.Fatalf("old plugin produced grants %+v err=%v", grants, err)
	}
}

func TestKnowledgeCodexNoPhantomBindings(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{
		"[mcp_servers.obsidian.tools.read_note]\napproval_mode = \"ask\"\n",
		"[mcp_servers.obsidian]\ncommand = \"obsidian-mcp\"\nenabled = false\n",
		"[\"mcp_servers.obsidian\"]\ncommand = \"not-a-server\"\n",
		"[mcp_servers.amem.tools.amem_search]\napproval_mode = \"ask\"\n",
		"[mcp_servers.amem]\ncommand = \"/opt/bin/amem\"\nenabled = false\n",
		"[\"mcp_servers.amem\"]\ncommand = \"not-a-server\"\n",
	} {
		if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		grants, err := KnowledgeApprovals(Runtime{Agent: "codex", Home: home})
		if err != nil || len(grants) != 0 {
			t.Fatalf("phantom grants %+v %v", grants, err)
		}
	}
}
func TestKnowledgeClaudePreciseGrants(t *testing.T) {
	runtime := Runtime{Agent: "claude"}
	grants, err := KnowledgeApprovals(runtime)
	if err != nil {
		t.Fatal(err)
	}
	args, err := KnowledgeArgs(runtime, grants)
	if err != nil || len(args) != 2 || args[0] != "--allowedTools" {
		t.Fatalf("%+v %v", args, err)
	}
	for _, name := range []string{"mcp__vmem__vmem_create", "mcp__vmem__vmem_patch", "mcp__vmem__vmem_delete", "mcp__amem__amem_work_state_write", "mcp__amem__amem_remember", "mcp__amem__amem_search"} {
		if !strings.Contains(args[1], name) {
			t.Fatal(name)
		}
	}
	for _, tool := range []string{"delete_note", "move_note", "build_corpus", "*", "vmem_*"} {
		if _, err := KnowledgeArgs(runtime, []KnowledgeApproval{{Server: "vmem", Tool: tool, ApprovalMode: "approve"}}); err == nil {
			t.Fatal("unsafe grant accepted")
		}
	}
}
func TestKnowledgeOverlayContainsOnlyPermissionLeaves(t *testing.T) {
	prefs, policy, _ := preferenceFixture(t)
	policy.Targets[0].Agent = "codex"
	policy.Targets[0].Version = "0.157.1"
	home := filepath.Join(prefs.Home, ".codex")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.vmem]\ncommand = \"private-executable\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inventory := []Runtime{{Agent: "codex", Home: home, Version: "0.157.1", Available: true, AutoReview: true, SubscriptionVerified: true}}
	changes, err := prefs.Apply(policy, inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(changes[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"vmem_create\".approval_mode = \"approve\"") || strings.Contains(string(data), "private-executable") {
		t.Fatalf("%s", data)
	}
}

func TestKnowledgeCanonicalDeclaredHome(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.vmem]\ncommand = \"/opt/bin/vmem\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	grants, err := KnowledgeApprovals(Runtime{Agent: "codex", Home: home})
	if err != nil || len(grants) != len(vaultKnowledgeTools) {
		t.Fatalf("%+v %v", grants, err)
	}
}

func TestKnowledgeCodexCLIUsesNativeRawSegments(t *testing.T) {
	runtime := Runtime{Agent: "codex"}
	grants := []KnowledgeApproval{
		{Server: "vmem", Tool: "vmem_create", ApprovalMode: "approve"},
		{Server: "amem", Tool: "amem_work_state_write", ApprovalMode: "approve"},
	}
	args, err := KnowledgeArgs(runtime, grants)
	if err != nil {
		t.Fatal(err)
	}
	// Model Codex's actual override-key parser: split raw key by dots. Quotes
	// are literal key characters here, although native TOML files need them.
	expected := [][]string{
		{"mcp_servers", "vmem", "tools", "vmem_create", "approval_mode"},
		{"mcp_servers", "amem", "tools", "amem_work_state_write", "approval_mode"},
	}
	for i, parts := range expected {
		if args[2*i] != "-c" {
			t.Fatalf("unexpected flag %q", args[2*i])
		}
		key, value, ok := strings.Cut(args[2*i+1], "=")
		if !ok || value != "\"approve\"" {
			t.Fatalf("invalid override %q", args[2*i+1])
		}
		actual := strings.Split(key, ".")
		if len(actual) != len(parts) {
			t.Fatalf("unexpected segments %q", actual)
		}
		for j, part := range parts {
			if actual[j] != part {
				t.Fatalf("phantom native key: got %q want %q", actual[j], part)
			}
		}
	}
	if !strings.Contains(knowledgeKey(grants[1]), "mcp_servers.\"amem\".tools.\"amem_work_state_write\"") {
		t.Fatal("TOML file keys lost required quoting")
	}
}

// The Claude grant names mcp__amem__<tool>. That only reaches a tool when a server called "amem" is registered
// with Claude, which `amem setup claude` does (0.11.0 registers `claude mcp add ... amem`). The server name
// `amem setup` registers and the one the grant is keyed on must agree.
func TestClaudeGrantNamesTheServerThatInstallRegisters(t *testing.T) {
	if memoryServer != "amem" {
		t.Fatalf("memoryServer = %q: `amem setup` registers amem, and the mcp__amem__ pattern must agree", memoryServer)
	}
	args, err := KnowledgeArgs(Runtime{Agent: "claude"}, []KnowledgeApproval{{Server: memoryServer, Tool: "amem_search", ApprovalMode: "approve"}})
	if err != nil || len(args) != 2 || args[1] != "mcp__amem__amem_search" {
		t.Fatalf("claude pattern %v err=%v, want mcp__amem__amem_search", args, err)
	}
}

// vmem's grant set is its frozen 0.1.4 tool list, and only the vmem server earns it.
func TestKnowledgeVaultToolsAreVmemsFrozenSurface(t *testing.T) {
	want := []string{"vmem_append", "vmem_check", "vmem_create", "vmem_delete", "vmem_graph_build", "vmem_graph_neighbors", "vmem_link", "vmem_links", "vmem_lint", "vmem_list", "vmem_log_append", "vmem_log_tail", "vmem_mocs", "vmem_move", "vmem_patch", "vmem_promote", "vmem_promote_from_amem", "vmem_read", "vmem_read_many", "vmem_search", "vmem_set_frontmatter", "vmem_status", "vmem_supersede"}
	if strings.Join(vaultKnowledgeTools, ",") != strings.Join(want, ",") {
		t.Fatalf("vault tools drifted from vault-mem VMEM_TOOL_NAMES: %v", vaultKnowledgeTools)
	}
	for _, agent := range []string{"claude", "codex"} {
		if _, err := KnowledgeArgs(Runtime{Agent: agent}, []KnowledgeApproval{{Server: "vmem", Tool: "vmem_search", ApprovalMode: "approve"}}); err != nil {
			t.Errorf("%s refused vmem's own tool: %v", agent, err)
		}
		for _, grant := range []KnowledgeApproval{
			{Server: "obsidian", Tool: "read_note", ApprovalMode: "approve"},
			{Server: "obsidian", Tool: "write_note", ApprovalMode: "approve"},
			{Server: "obsidian", Tool: "vmem_search", ApprovalMode: "approve"},
			{Server: "vmem", Tool: "read_note", ApprovalMode: "approve"},
			{Server: "amem", Tool: "vmem_search", ApprovalMode: "approve"},
		} {
			if _, err := KnowledgeArgs(Runtime{Agent: agent}, []KnowledgeApproval{grant}); err == nil {
				t.Errorf("%s granted %+v", agent, grant)
			}
		}
	}
}

// A Codex config that still registers the retired obsidian server earns no vault grant.
func TestKnowledgeCodexObsidianBindingEarnsNoGrant(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.obsidian]\ncommand = \"obsidian-mcp\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	grants, err := KnowledgeApprovals(Runtime{Agent: "codex", Home: home})
	if err != nil || len(grants) != 0 {
		t.Fatalf("obsidian binding granted: %+v %v", grants, err)
	}
	grants, err = KnowledgeApprovals(Runtime{Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.Server == "obsidian" {
			t.Fatalf("claude grant on obsidian: %+v", grant)
		}
	}
}
