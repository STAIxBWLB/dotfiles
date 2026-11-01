package aipolicy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/STAIxBWLB/dotfiles/internal/toolversion"
)

// KnowledgeApproval is a narrow native MCP tool grant, never a server definition.
// Both consumers and launch adapters validate against the fixed tool allowlist.
type KnowledgeApproval struct {
	Server       string `json:"server"`
	Plugin       string `json:"plugin,omitempty"`
	Tool         string `json:"tool"`
	ApprovalMode string `json:"approval_mode"`
}

// vaultKnowledgeTools is vmem's MCP surface as dot last verified it (@staix/vault-mem 0.2.4
// VMEM_TOOL_NAMES, plan ai-workspace#60 §3.2): the fallback when the installed vmem publishes no
// manifest (knowledgeToolList). vmem_move, vmem_delete, vmem_promote and vmem_promote_from_amem return
// a preview and a fingerprint and write only on a second call with confirm=true and that fingerprint,
// so the preview is the guard; a narrower list for unattended tasks is Maru's own policy. The names
// reach Claude Code as mcp__vmem__<tool>.
var vaultKnowledgeTools = []string{"vmem_append", "vmem_check", "vmem_create", "vmem_delete", "vmem_graph_build", "vmem_graph_neighbors", "vmem_link", "vmem_links", "vmem_lint", "vmem_list", "vmem_log_append", "vmem_log_tail", "vmem_mocs", "vmem_move", "vmem_patch", "vmem_promote", "vmem_promote_from_amem", "vmem_read", "vmem_read_many", "vmem_search", "vmem_set_frontmatter", "vmem_skills_mine", "vmem_status", "vmem_supersede"}

// vaultServer is the MCP server name vmem is registered under (`vmem setup` and the mcpServers key dot
// writes for Kiro and Copilot). The retired Obsidian MCP server ("obsidian", @bitbonsai/mcpvault) earns
// no grant: its bindings are never recognized and never mapped onto vmem.
const vaultServer = "vmem"

// memoryKnowledgeTools is amem's MCP surface as dot last verified it (agent-mem 0.17.0 src/mcp.ts
// MCP_TOOLS): the fallback when the installed amem publishes no manifest (knowledgeToolList). None
// deletes or moves a note: amem_remember and amem_correct only propose a candidate for the person to
// accept, and amem_work_state_write appends scalar work-list fields. The names reach Claude Code as
// mcp__amem__<tool>.
var memoryKnowledgeTools = []string{"amem_search", "amem_evidence", "amem_show", "amem_timeline", "amem_brief", "amem_graph", "amem_work_state_read", "amem_work_state_write", "amem_remember", "amem_correct", "amem_vault_candidates", "amem_capacity"}

// memoryServer is the MCP server name amem is registered under (the name `amem setup`
// registers for Claude Code and Codex, and the mcpServers key dot writes for Kiro and Copilot).
const memoryServer = "amem"

// knowledgeManifest returns the MCP tools the installed amem or vmem publishes in its manifest, nil
// when it publishes none. Tests replace it so they never read a real install.
var knowledgeManifest = cachedManifestTools()

func cachedManifestTools() func(server string) []toolversion.Tool {
	var mu sync.Mutex
	cache := map[string][]toolversion.Tool{}
	return func(server string) []toolversion.Tool {
		mu.Lock()
		defer mu.Unlock()
		if tools, ok := cache[server]; ok {
			return tools
		}
		var tools []toolversion.Tool
		pkg := map[string]string{memoryServer: "@staix/agent-mem", vaultServer: "@staix/vault-mem"}[server]
		if bin, err := exec.LookPath(server); err == nil && pkg != "" {
			tools = toolversion.Read(context.Background(), bin, pkg).Tools
		}
		cache[server] = tools
		return tools
	}
}

var grantableTool = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// destructiveVerbs mark a tool that can delete, forget, move, purge or remove something.
var destructiveVerbs = []string{"delete", "forget", "move", "purge", "remove"}

// knowledgeToolList is the grant list for server: the installed release's manifest when it publishes
// one, else the compiled list. A published tool earns a grant only when it is the server's own
// (`amem_` on amem, `vmem_` on vmem), a plain identifier, and either already in the compiled list or
// marked `readOnly: true` by the manifest without a destructive verb in its name. The compiled list is
// the ceiling for anything that writes: a new plain-string name (vmem_overwrite, vmem_reset) stays
// unapproved, and so does a readOnly claim on a name that can delete, forget, move, purge or remove.
func knowledgeToolList(server string) []string {
	fallback := map[string][]string{memoryServer: memoryKnowledgeTools, vaultServer: vaultKnowledgeTools}[server]
	published := []string{}
	seen := map[string]bool{}
	for _, tool := range knowledgeManifest(server) {
		name := tool.Name
		if seen[name] || !strings.HasPrefix(name, server+"_") || !grantableTool.MatchString(name) {
			continue
		}
		if !slices.Contains(fallback, name) && (!tool.ReadOnly || destructive(name)) {
			continue
		}
		seen[name] = true
		published = append(published, name)
	}
	if len(published) == 0 {
		return fallback
	}
	return published
}

func destructive(name string) bool {
	for _, verb := range destructiveVerbs {
		if strings.Contains(name, verb) {
			return true
		}
	}
	return false
}

// KnowledgeApprovals recognizes existing Codex bindings without copying identity
// or credentials. Claude permission patterns themselves do not register servers.
// Memory recording tools are available only when the installed plugin exposes
// its server runtime; granting them does not activate or change that runtime.
func KnowledgeApprovals(runtime Runtime) ([]KnowledgeApproval, error) {
	out := []KnowledgeApproval{}
	add := func(server, plugin string, names []string) {
		for _, name := range names {
			out = append(out, KnowledgeApproval{Server: server, Plugin: plugin, Tool: name, ApprovalMode: "approve"})
		}
	}
	switch runtime.Agent {
	case "claude":
		add(vaultServer, "", knowledgeToolList(vaultServer))
		add(memoryServer, "", knowledgeToolList(memoryServer))
	case "codex":
		if runtime.Home == "" {
			return out, nil
		}
		home, homeErr := canonicalDeclaredHome(runtime.Home)
		if homeErr != nil {
			return nil, homeErr
		}
		path := filepath.Join(home, "config.toml")
		if err := safePreferencePath(path); err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if os.IsNotExist(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		if err != nil {
			return nil, err
		}
		if len(data) > 1024*1024 {
			return nil, fmt.Errorf("codex config too large for knowledge binding inspection")
		}
		// This is a conservative recognizer of known table spellings, not a TOML
		// rewriter. Multiline strings can impersonate table headers, so refuse them.
		if strings.Contains(string(data), "\"\"\"") || strings.Contains(string(data), "'''") {
			return nil, fmt.Errorf("multiline Codex config requires manual knowledge binding validation")
		}
		section := ""
		bound := map[string]bool{}    // servers with a command or url
		disabled := map[string]bool{} // servers with enabled = false
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "[") {
				end := strings.Index(line, "]")
				if end < 0 {
					return nil, fmt.Errorf("invalid Codex config table")
				}
				section = knowledgeTable(strings.TrimSpace(line[1:end]))
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
			for _, server := range []string{vaultServer, memoryServer} {
				if section != "mcp_servers."+server {
					continue
				}
				if key == "enabled" && value == "false" {
					disabled[server] = true
				}
				if key == "command" || key == "url" {
					if len(value) > 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
						bound[server] = true
					}
				}
			}
		}
		// Only a server the person registered and left enabled is granted. The old claude-mem
		// plugin binding and the retired obsidian server are not recognized, so they earn no grant
		// and are never mapped to amem or vmem.
		if bound[vaultServer] && !disabled[vaultServer] {
			add(vaultServer, "", knowledgeToolList(vaultServer))
		}
		if bound[memoryServer] && !disabled[memoryServer] {
			add(memoryServer, "", knowledgeToolList(memoryServer))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Plugin+"/"+a.Server+"/"+a.Tool < b.Plugin+"/"+b.Server+"/"+b.Tool
	})
	return out, nil
}

func knowledgeTools(agent string, grant KnowledgeApproval) []string {
	if grant.ApprovalMode != "approve" {
		return nil
	}
	if (agent == "claude" || agent == "codex") && grant.Server == vaultServer && grant.Plugin == "" {
		return knowledgeToolList(vaultServer)
	}
	// amem is a plain MCP server for both agents. Any other server name, the retired
	// claude-mem ones ("mcp-search", "plugin_claude-mem_mcp-search") included, gets no
	// tools, so KnowledgeArgs refuses it instead of mapping it onto amem.
	if (agent == "claude" || agent == "codex") && grant.Server == memoryServer && grant.Plugin == "" {
		return knowledgeToolList(memoryServer)
	}
	return nil
}
func knowledgeKey(grant KnowledgeApproval) string {
	prefix := "mcp_servers."
	if grant.Plugin != "" {
		prefix = "plugins." + strconv.Quote(grant.Plugin) + ".mcp_servers."
	}
	return prefix + strconv.Quote(grant.Server) + ".tools." + strconv.Quote(grant.Tool) + ".approval_mode"
}

// Codex -c splits raw dotted key segments and does not decode TOML key quotes.
// Callers validate all segments against the fixed allowlist before using this.
func knowledgeCLIKey(grant KnowledgeApproval) string {
	prefix := "mcp_servers."
	if grant.Plugin != "" {
		prefix = "plugins." + grant.Plugin + ".mcp_servers."
	}
	return prefix + grant.Server + ".tools." + grant.Tool + ".approval_mode"
}

// KnowledgeArgs emits only permission leaves, preserving native MCP identity.
// It never accepts wildcard grants, unlisted tools or corpus-build tools; vmem's move and delete
// are listed because they are two-step (preview + fingerprint, then confirm).
func KnowledgeArgs(runtime Runtime, grants []KnowledgeApproval) ([]string, error) {
	args := []string{}
	patterns := []string{}
	if runtime.Agent != "claude" && runtime.Agent != "codex" {
		if len(grants) > 0 {
			return nil, fmt.Errorf("knowledge grants unsupported for %s", runtime.Agent)
		}
		return args, nil
	}
	for _, grant := range grants {
		allowed := false
		for _, name := range knowledgeTools(runtime.Agent, grant) {
			if name == grant.Tool {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("unsupported knowledge tool grant")
		}
		if runtime.Agent == "claude" {
			patterns = append(patterns, "mcp__"+grant.Server+"__"+grant.Tool)
		} else {
			args = append(args, "-c", knowledgeCLIKey(grant)+"=\"approve\"")
		}
	}
	if len(patterns) > 0 {
		args = append(args, "--allowedTools", strings.Join(patterns, ","))
	}
	return args, nil
}

// knowledgeTable accepts conventional dotted keys while keeping quoted dotted
// names distinct from nested tables. Unsupported escaping never grants a tool.
func knowledgeTable(header string) string {
	parts := []string{}
	for len(strings.TrimSpace(header)) > 0 {
		header = strings.TrimSpace(header)
		token := ""
		if header[0] == '"' || header[0] == '\'' {
			quote := header[0]
			end := strings.IndexByte(header[1:], quote)
			if end < 0 {
				return ""
			}
			end++
			token = header[1:end]
			header = header[end+1:]
			if strings.ContainsAny(token, ".\\") {
				return ""
			}
		} else {
			end := strings.IndexByte(header, '.')
			if end < 0 {
				token = header
				header = ""
			} else {
				token = header[:end]
				header = header[end:]
			}
			token = strings.TrimSpace(token)
			for _, ch := range token {
				if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' && ch != '-' {
					return ""
				}
			}
		}
		if token == "" {
			return ""
		}
		parts = append(parts, token)
		header = strings.TrimSpace(header)
		if header == "" {
			break
		}
		if header[0] != '.' {
			return ""
		}
		header = header[1:]
		if strings.TrimSpace(header) == "" {
			return ""
		}
	}
	return strings.Join(parts, ".")
}
