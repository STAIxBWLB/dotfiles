package aipolicy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// KnowledgeApproval is a narrow native MCP tool grant, never a server definition.
// Both consumers and launch adapters validate against the fixed tool allowlist.
type KnowledgeApproval struct {
	Server       string `json:"server"`
	Plugin       string `json:"plugin,omitempty"`
	Tool         string `json:"tool"`
	ApprovalMode string `json:"approval_mode"`
}

var vaultKnowledgeTools = []string{"read_note", "read_multiple_notes", "search_notes", "list_directory", "get_notes_info", "get_frontmatter", "get_vault_stats", "list_all_tags", "write_note", "patch_note", "update_frontmatter", "manage_tags"}

// memoryKnowledgeTools is amem's frozen MCP surface (agent-mem src/mcp.ts MCP_TOOLS, #13 P2).
// None deletes or moves a note: amem_remember only proposes a candidate for the person to accept,
// and amem_work_state_write appends scalar work-list fields. The names reach Claude Code as
// mcp__amem__<tool>.
var memoryKnowledgeTools = []string{"amem_search", "amem_show", "amem_timeline", "amem_graph", "amem_work_state_read", "amem_work_state_write", "amem_remember", "amem_vault_candidates", "amem_capacity"}

// memoryServer is the MCP server name amem is registered under (`codex mcp add amem`,
// `claude mcp add amem`, and the mcpServers key dot writes for the other agents).
const memoryServer = "amem"

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
		add("obsidian", "", vaultKnowledgeTools)
		add(memoryServer, "", memoryKnowledgeTools)
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
			for _, server := range []string{"obsidian", memoryServer} {
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
		// plugin binding is not recognized, so it earns no grant and is never mapped to amem.
		if bound["obsidian"] && !disabled["obsidian"] {
			add("obsidian", "", vaultKnowledgeTools)
		}
		if bound[memoryServer] && !disabled[memoryServer] {
			add(memoryServer, "", memoryKnowledgeTools)
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
	if grant.Server == "obsidian" && grant.Plugin == "" {
		return vaultKnowledgeTools
	}
	// amem is a plain MCP server for both agents. Any other server name, the retired
	// claude-mem ones ("mcp-search", "plugin_claude-mem_mcp-search") included, gets no
	// tools, so KnowledgeArgs refuses it instead of mapping it onto amem.
	if (agent == "claude" || agent == "codex") && grant.Server == memoryServer && grant.Plugin == "" {
		return memoryKnowledgeTools
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
// It never accepts wildcard grants, delete/move operations or corpus-build tools.
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
