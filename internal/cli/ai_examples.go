package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// aiExamples holds the Example text for each runnable command under dot ai,
// keyed by the path below "dot ai". Several of these commands are built in
// loops, so one table keeps their examples in a single place.
var aiExamples = map[string][]string{
	"":                        {"dot ai --help"},
	"agents apply":            {"dot ai agents apply", "dot ai agents apply --tool claude,codex"},
	"agents author":           {"dot ai agents author", "dot ai agents author --non-interactive --section style --value 'Be brief'"},
	"agents diff":             {"dot ai agents diff", "dot ai agents diff --tool codex"},
	"agents edit":             {"dot ai agents edit"},
	"agents init":             {"dot ai agents init", "dot ai agents init --from-current claude"},
	"agents list":             {"dot ai agents list"},
	"agents path":             {"dot ai agents path"},
	"agents pull":             {"dot ai agents pull --from claude"},
	"agents show":             {"dot ai agents show", "dot ai agents show --rendered codex"},
	"agents status":           {"dot ai agents status"},
	"audit summary":           {"dot ai audit summary"},
	"audit tail":              {"dot ai audit tail", "dot ai audit tail 50"},
	"auth login":              {"dot ai auth login --all-needed", "dot ai auth login \"claude.ai Canva\""},
	"auth relogin":            {"dot ai auth relogin \"claude.ai Canva\""},
	"auth status":             {"dot ai auth status", "dot ai auth status --tool codex --json"},
	"backup":                  {"dot ai backup", "dot ai backup --tag before-upgrade"},
	"backup status":           {"dot ai backup status"},
	"coauthor-guard apply":    {"dot ai coauthor-guard apply --mode warn", "dot ai coauthor-guard apply --mode block --persist"},
	"coauthor-guard status":   {"dot ai coauthor-guard status"},
	"export":                  {"dot ai export ai-settings.tar.gz"},
	"family install":          {"dot ai family install amem", "dot ai family install --all"},
	"family rollback":         {"dot ai family rollback amem"},
	"family status":           {"dot ai family status"},
	"family update":           {"dot ai family update", "dot ai family update ahub --dry-run"},
	"handoff record":          {"dot ai handoff record --agent codex --kind review --summary-file notes.md"},
	"handoff show":            {"dot ai handoff show", "dot ai handoff show --json"},
	"hud apply":               {"dot ai hud apply", "dot ai hud apply --tool claude --persist"},
	"hud status":              {"dot ai hud status"},
	"import":                  {"dot ai import ai-settings.tar.gz"},
	"mcp":                     {"dot ai mcp --help"},
	"mcp install":             {"dot ai mcp install amem"},
	"mcp status":              {"dot ai mcp status amem"},
	"memory":                  {"dot ai memory --help"},
	"memory archive":          {"dot ai memory archive claude old-notes.md", "dot ai memory archive claude old-notes.md --apply"},
	"memory audit":            {"dot ai memory audit", "dot ai memory audit --agent claude,codex --json"},
	"policy apply":            {"dot ai policy apply", "dot ai policy apply --persist"},
	"policy diff":             {"dot ai policy diff"},
	"policy inspect":          {"dot ai policy inspect", "dot ai policy inspect --json"},
	"policy resolve":          {"dot ai policy resolve --task 'fix the flaky test'", "dot ai policy resolve --agent codex --json"},
	"policy rollback":         {"dot ai policy rollback"},
	"policy status":           {"dot ai policy status"},
	"prune":                   {"dot ai prune", "dot ai prune --keep 3"},
	"restore":                 {"dot ai restore", "dot ai restore --host laptop --version latest"},
	"session start":           {"dot ai session start --task 'review the open PR'", "dot ai session start --input turns.jsonl"},
	"setup":                   {"dot ai setup", "dot ai setup --non-interactive --agents claude,codex"},
	"skills list":             {"dot ai skills list", "dot ai skills list --tool claude --json"},
	"skills path":             {"dot ai skills path"},
	"skills status":           {"dot ai skills status", "dot ai skills status --json"},
	"skills validate":         {"dot ai skills validate", "dot ai skills validate --strict"},
	"tools apply":             {"dot ai tools apply"},
	"tools list":              {"dot ai tools list", "dot ai tools list --details"},
	"tools status":            {"dot ai tools status"},
	"update":                  {"dot ai update --check", "dot ai update --tool claude,codex"},
	"update schedule disable": {"dot ai update schedule disable"},
	"update schedule enable":  {"dot ai update schedule enable"},
	"update schedule status":  {"dot ai update schedule status"},
	"vault":                   {"dot ai vault --help"},
}

// applyAIExamples fills Example on every command under ai that has none.
func applyAIExamples(ai *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Example == "" {
			key := strings.TrimPrefix(strings.TrimPrefix(c.CommandPath(), ai.CommandPath()), " ")
			if ex, ok := aiExamples[key]; ok {
				c.Example = "  " + strings.Join(ex, "\n  ")
			}
		}
		for _, sc := range c.Commands() {
			walk(sc)
		}
	}
	walk(ai)
}
