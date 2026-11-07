package aipolicy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/toolversion"
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
	native := []byte("[mcp_servers.vmem]\ncommand = \"/opt/bin/vmem\"\nargs = [\"mcp\"]\n[mcp_servers.amem]\ncommand = \"/opt/bin/amem\"\nargs = [\"mcp\"]\n[mcp_servers.ares]\ncommand = \"/opt/bin/ares\"\nargs = [\"mcp\"]\n")
	if err = os.WriteFile(filepath.Join(home, "config.toml"), native, 0600); err != nil {
		t.Fatal(err)
	}
	grants, err = KnowledgeApprovals(runtime)
	if err != nil || len(grants) != len(vaultKnowledgeTools)+len(memoryKnowledgeTools)+len(researchKnowledgeTools) {
		t.Fatalf("%+v %v", grants, err)
	}
	args, err := KnowledgeArgs(runtime, grants)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{"mcp_servers.ares.tools.ares_status.approval_mode", "mcp_servers.vmem.tools.vmem_create.approval_mode", "mcp_servers.vmem.tools.vmem_move.approval_mode", "mcp_servers.amem.tools.amem_search.approval_mode", "mcp_servers.amem.tools.amem_work_state_write.approval_mode", "mcp_servers.amem.tools.amem_remember.approval_mode", "approval_mode=\"approve\""} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %s in %s", expected, joined)
		}
	}
	for _, excluded := range []string{"obsidian", "write_note", "delete_note", "move_file", "build_corpus", "prime_corpus", "command=", "url=", "claude-mem", "mcp-search", "plugins.", "ares_lit_search", "ares_attempt_run", "ares_gate"} {
		if strings.Contains(joined, excluded) {
			t.Fatalf("unexpected %s", excluded)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || string(data) != string(native) {
		t.Fatal("native identity changed")
	}
}

// No test reads the machine's amem or vmem: the compiled lists are the fallback under test unless a test
// sets knowledgeManifest itself.
func TestMain(m *testing.M) {
	knowledgeManifest = func(string) []toolversion.Tool { return nil }
	os.Exit(m.Run())
}

func plain(names ...string) []toolversion.Tool {
	out := make([]toolversion.Tool, len(names))
	for i, n := range names {
		out[i] = toolversion.Tool{Name: n}
	}
	return out
}

func readOnly(names ...string) []toolversion.Tool {
	out := plain(names...)
	for i := range out {
		out[i].ReadOnly = true
	}
	return out
}

// #319: the grant list is what the installed release publishes, inside the compiled rules. The compiled
// list is the ceiling for anything that writes: a new name earns a grant only when the manifest marks
// it readOnly and its name has no destructive verb. Nothing grantable falls back to the compiled list.
func TestKnowledgeToolsComeFromTheManifestInsideTheRules(t *testing.T) {
	published := map[string][]toolversion.Tool{}
	knowledgeManifest = func(server string) []toolversion.Tool { return published[server] }
	t.Cleanup(func() { knowledgeManifest = func(string) []toolversion.Tool { return nil } })
	grants := func(server string) string { return strings.Join(knowledgeToolList(server), ",") }
	approved := func(agent, server, tool string) bool {
		_, err := KnowledgeArgs(Runtime{Agent: agent}, []KnowledgeApproval{{Server: server, Tool: tool, ApprovalMode: "approve"}})
		return err == nil
	}

	published[memoryServer] = append(plain("amem_search", "amem_new_write", "vmem_read", "amem_*", "amem_search", "AMEM_X", "amem_"+strings.Repeat("x", 64)),
		readOnly("amem_new_read", "amem_forget", "amem_purge_all", "amem_move_entry")...)
	if got := grants(memoryServer); got != "amem_search,amem_new_read" {
		t.Fatalf("amem grants %s", got)
	}
	if !approved("claude", memoryServer, "amem_new_read") || approved("claude", memoryServer, "amem_forget") || approved("claude", memoryServer, "amem_new_write") {
		t.Fatal("amem grant rule broken")
	}

	// A new plain-string mutating name is not approved; a new readOnly one is; a readOnly claim on a
	// destructive verb is not; the compiled two-step vmem_delete and vmem_move stay.
	published[vaultServer] = append(plain("vmem_read", "vmem_delete", "vmem_move", "vmem_overwrite", "vmem_rename", "vmem_reset", "vmem_prune", "vmem_clear"),
		readOnly("vmem_stats", "vmem_purge_x", "vmem_remove_note", "vmem_forget_all")...)
	if got := grants(vaultServer); got != "vmem_read,vmem_delete,vmem_move,vmem_stats" {
		t.Fatalf("vmem grants %s", got)
	}
	for _, tool := range []string{"vmem_overwrite", "vmem_rename", "vmem_reset", "vmem_prune", "vmem_clear", "vmem_purge_x", "vmem_remove_note"} {
		if approved("codex", vaultServer, tool) {
			t.Errorf("%s earned a grant from a manifest", tool)
		}
	}
	if !approved("claude", vaultServer, "vmem_stats") {
		t.Error("a readOnly vmem_stats was refused")
	}

	// Under a compiled list without vmem_delete, a manifest publishing it earns no grant, readOnly or not.
	compiled := vaultKnowledgeTools
	vaultKnowledgeTools = []string{"vmem_read", "vmem_search"}
	t.Cleanup(func() { vaultKnowledgeTools = compiled })
	published[vaultServer] = append(plain("vmem_read", "vmem_delete"), readOnly("vmem_delete", "vmem_purge_all")...)
	if got := grants(vaultServer); got != "vmem_read" {
		t.Fatalf("vmem_delete above the compiled ceiling was granted: %s", got)
	}

	published[memoryServer] = append(plain("amem_forget", "amem_new_write"), readOnly("other_tool")...)
	if got := grants(memoryServer); got != strings.Join(memoryKnowledgeTools, ",") {
		t.Fatalf("nothing grantable must fall back to the compiled list, got %s", got)
	}
}

// amem's fallback grant set is its verified tool list and nothing a note can be deleted or moved with.
func TestKnowledgeMemoryToolsAreAmemsFrozenSurface(t *testing.T) {
	want := []string{"amem_search", "amem_evidence", "amem_show", "amem_timeline", "amem_brief", "amem_graph", "amem_work_state_read", "amem_work_state_write", "amem_remember", "amem_correct", "amem_vault_candidates", "amem_capacity"}
	if strings.Join(memoryKnowledgeTools, ",") != strings.Join(want, ",") {
		t.Fatalf("memory tools drifted from agent-mem src/mcp.ts: %v", memoryKnowledgeTools)
	}
	for _, name := range memoryKnowledgeTools {
		if !strings.HasPrefix(name, "amem_") || strings.Contains(name, "delete") || strings.Contains(name, "forget") || strings.Contains(name, "move") {
			t.Errorf("unsafe or foreign memory tool %q", name)
		}
	}
}

// AMEM_PACKAGE_DIR names an unpacked @staix/agent-mem tarball (`npm pack @staix/agent-mem@0.14.0`, then
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

// aresNotReadOnly are the @staix/agent-research 0.1.0 tools its manifest marks `readOnly: false`: every
// tool that writes a study, decides a gate, reaches the network, or starts, stops or reads a container.
var aresNotReadOnly = []string{"ares_watch_set", "ares_watch_run", "ares_console_add", "ares_console_remove", "ares_setup", "ares_archive", "ares_lessons", "ares_skill_propose", "ares_skill_decide", "ares_plan_set", "ares_plan_control", "ares_scope_set", "ares_gate", "ares_idea_add", "ares_idea_decide", "ares_investigate_close", "ares_ablate_add", "ares_ablate_close", "ares_final_close", "ares_final_seeds", "ares_selection_set", "ares_protocol_set", "ares_executor_set", "ares_executor_check", "ares_attempt_run", "ares_attempt_cancel", "ares_attempt_reconcile", "ares_attempt_logs", "ares_lit_search", "ares_lit_screen", "ares_lit_card", "ares_lit_verify", "ares_lit_bib", "ares_review_add", "ares_review_resolve", "ares_paper_build"}

// #318 AC3: networked and container tools are in no auto-approve list. ares grants read tools only, from
// the compiled list or from a manifest entry marked readOnly, and a readOnly: false entry loses its grant
// even when the compiled list names it.
func TestKnowledgeResearchGrantsReadToolsOnly(t *testing.T) {
	approved := func(agent, tool string) bool {
		_, err := KnowledgeArgs(Runtime{Agent: agent}, []KnowledgeApproval{{Server: researchServer, Tool: tool, ApprovalMode: "approve"}})
		return err == nil
	}
	for _, agent := range []string{"claude", "codex"} {
		for _, tool := range aresNotReadOnly {
			if slices.Contains(researchKnowledgeTools, tool) || approved(agent, tool) {
				t.Errorf("%s auto-approves %s", agent, tool)
			}
		}
		if !approved(agent, "ares_status") || !approved(agent, "ares_playbook") {
			t.Errorf("%s refused an ares read tool", agent)
		}
	}
	grants, err := KnowledgeApprovals(Runtime{Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	research := 0
	for _, grant := range grants {
		if grant.Server == researchServer {
			research++
			if slices.Contains(aresNotReadOnly, grant.Tool) {
				t.Errorf("claude grant on %s", grant.Tool)
			}
		}
	}
	if research != len(researchKnowledgeTools) {
		t.Fatalf("claude ares grants = %d, want %d", research, len(researchKnowledgeTools))
	}

	published := map[string][]toolversion.Tool{}
	knowledgeManifest = func(server string) []toolversion.Tool { return published[server] }
	t.Cleanup(func() { knowledgeManifest = func(string) []toolversion.Tool { return nil } })
	published[researchServer] = append(plain("ares_status", "ares_lit_search", "ares_new_write"), readOnly("ares_plan_show", "ares_new_read", "ares_purge_cache", "vmem_read")...)
	if got := strings.Join(knowledgeToolList(researchServer), ","); got != "ares_plan_show,ares_new_read" {
		t.Fatalf("ares grants from a manifest = %s", got)
	}
}

// ARES_PACKAGE_DIR names an unpacked @staix/agent-research tarball (`npm pack @staix/agent-research@0.1.0`,
// then extract). When set, every allowlisted ares tool must be in that package's src/mcp-tools.ts with the
// plain `read` annotation (readOnlyHint true, openWorldHint false). Unset (CI), the list above is the contract.
func TestKnowledgeResearchToolsMatchThePublishedPackage(t *testing.T) {
	dir := os.Getenv("ARES_PACKAGE_DIR")
	if dir == "" {
		t.Skip("ARES_PACKAGE_DIR not set; run with an unpacked @staix/agent-research tarball to compare")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "src", "mcp-tools.ts"))
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), `", description:`)
		if !ok || !strings.HasPrefix(name, `{ name: "ares_`) {
			continue
		}
		read[strings.TrimPrefix(name, `{ name: "`)] = strings.HasSuffix(strings.TrimSpace(rest), "annotations: read },")
	}
	for _, name := range researchKnowledgeTools {
		if !read[name] {
			t.Errorf("allowlisted %s is not a read tool in the published package", name)
		}
	}
	for _, name := range aresNotReadOnly {
		if read[name] {
			t.Errorf("%s is read-only in the published package; review whether to grant it", name)
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
// with Claude, which `amem setup claude` does (0.14.0 registers `claude mcp add ... amem`). The server name
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

// vmem's fallback grant set is its verified 0.2.4 tool list, and only the vmem server earns it.
func TestKnowledgeVaultToolsAreVmemsFrozenSurface(t *testing.T) {
	want := []string{"vmem_append", "vmem_check", "vmem_create", "vmem_delete", "vmem_graph_build", "vmem_graph_neighbors", "vmem_link", "vmem_links", "vmem_lint", "vmem_list", "vmem_log_append", "vmem_log_tail", "vmem_mocs", "vmem_move", "vmem_patch", "vmem_promote", "vmem_promote_from_amem", "vmem_read", "vmem_read_many", "vmem_search", "vmem_set_frontmatter", "vmem_skills_mine", "vmem_status", "vmem_supersede"}
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
