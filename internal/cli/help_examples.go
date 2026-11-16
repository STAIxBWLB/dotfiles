package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// helpExamples holds the Example text for each runnable command outside
// dot ai, dot sync, dot peer and dot remote, keyed by the path below "dot".
// The ai subtree has its own table in ai_examples.go. Several of these
// commands are built in loops, so one table keeps their examples together.
var helpExamples = map[string][]string{
	"":                      {"dot", "dot usecase"},
	"admit":                 {"dot admit -- cargo test", "dot admit --class maintenance --wait 30m -- ./reindex.sh"},
	"admit relax":           {"dot admit relax --for 1h --ignore pressure", "dot admit relax --clear"},
	"admit status":          {"dot admit status", "dot admit status --json"},
	"apply":                 {"dot apply", "dot apply --profile minimal --dry-run"},
	"apps backup":           {"dot apps backup --all", "dot apps backup raycast"},
	"apps install":          {"dot apps install --recommended", "dot apps install raycast"},
	"apps list":             {"dot apps list"},
	"apps restore":          {"dot apps restore --all", "dot apps restore raycast"},
	"apps status":           {"dot apps status"},
	"backup":                {"dot backup", "dot backup --scope profile,apps --tag before-upgrade"},
	"clean":                 {"dot clean", "dot clean ~/workspace --yes"},
	"completion bash":       {"source <(dot completion bash)"},
	"completion fish":       {"dot completion fish | source"},
	"completion powershell": {"dot completion powershell | Out-String | Invoke-Expression"},
	"completion zsh":        {"source <(dot completion zsh)"},
	"config":                {"dot config", "dot config --profile server"},
	"config export":         {"dot config export", "dot config export ~/dotfiles-config.yaml"},
	"container setup":       {"dot container setup"},
	"container status":      {"dot container status", "dot container status --probe"},
	"diff":                  {"dot diff", "dot diff --summary"},
	"guard":                 {"dot guard --help"},
	"guard disable":         {"dot guard disable"},
	"guard enable":          {"dot guard enable"},
	"guard freeze":          {"dot guard freeze ~/workspace/work/app"},
	"guard status":          {"dot guard status"},
	"guard unfreeze":        {"dot guard unfreeze"},
	"help":                  {"dot help apply", "dot help sync push"},
	"hub":                   {"dot hub myproject", "dot hub myproject status"},
	"init":                  {"dot init", "dot init --detect"},
	"layouts":               {"dot layouts"},
	"layouts doctor":        {"dot layouts doctor"},
	"list":                  {"dot list"},
	"noindex":               {"dot noindex", "dot noindex ~/workspace --verbose"},
	"noindex setup":         {"dot noindex setup"},
	"noindex uninstall":     {"dot noindex uninstall"},
	"open":                  {"dot open myproject", "dot open myproject --layout claude"},
	"profile backup":        {"dot profile backup", "dot profile backup --tag before-upgrade"},
	"profile list":          {"dot profile list"},
	"profile prune":         {"dot profile prune --keep 3"},
	"profile restore":       {"dot profile restore", "dot profile restore --version 2.83.0"},
	"profile root":          {"dot profile root", "dot profile root --detect"},
	"reconfigure":           {"dot reconfigure"},
	"register":              {"dot register myproject ~/workspace/work/myproject"},
	"restore":               {"dot restore", "dot restore --scope profile --apply"},
	"rollback":              {"dot rollback"},
	"secrets backup":        {"dot secrets backup", "dot secrets backup ~/backups/secrets"},
	"secrets init":          {"dot secrets init --scaffold"},
	"secrets list":          {"dot secrets list"},
	"secrets restore":       {"dot secrets restore ~/backups/secrets"},
	"secrets status":        {"dot secrets status"},
	"status":                {"dot status", "dot status --profile server"},
	"stop":                  {"dot stop myproject"},
	"tunnel":                {"dot tunnel --help"},
	"tunnel client":         {"dot tunnel client --help"},
	"tunnel client add":     {"dot tunnel client add mac.example.com"},
	"tunnel client list":    {"dot tunnel client list"},
	"tunnel client remove":  {"dot tunnel client remove mac.example.com"},
	"tunnel log":            {"dot tunnel log", "dot tunnel log 50"},
	"tunnel setup":          {"dot tunnel setup --hostname mac.example.com"},
	"tunnel status":         {"dot tunnel status"},
	"tunnel uninstall":      {"dot tunnel uninstall"},
	"unregister":            {"dot unregister myproject"},
	"update":                {"dot update", "dot update --check"},
	"usecase":               {"dot usecase"},
	"version":               {"dot version"},
	"watchdog":              {"dot watchdog --help"},
	"watchdog log":          {"dot watchdog log", "dot watchdog log 50"},
	"watchdog notify":       {"dot watchdog notify warn \"disk almost full\""},
	"watchdog reap":         {"dot watchdog reap"},
	"watchdog setup":        {"dot watchdog setup"},
	"watchdog status":       {"dot watchdog status"},
	"watchdog uninstall":    {"dot watchdog uninstall"},
	"watchdog warp":         {"dot watchdog warp"},
	"ws audit":              {"dot ws audit", "dot ws audit work"},
	"ws init":               {"dot ws init", "dot ws init --force"},
	"ws mkdir":              {"dot ws mkdir work/dev/newapp"},
	"ws mv":                 {"dot ws mv work/dev/old work/dev/new"},
	"ws reconcile":          {"dot ws reconcile", "dot ws reconcile work"},
	"ws rm":                 {"dot ws rm work/dev/old", "dot ws rm work/dev/old --recursive"},
}

// generatedHelp restates the text of the commands Cobra generates (help and
// the completion scripts) so it follows the same metadata rules as ours.
var generatedHelp = map[string]struct{ short, long string }{
	"help": {
		short: "Show help for a command",
		long:  "Show help for any command. Without arguments it shows the help for dot itself.",
	},
	"completion": {
		short: "Generate a shell completion script",
		long: "Generate the autocompletion script for dot for a supported shell. See each\n" +
			"subcommand's help for how to load the generated script.",
	},
	"completion bash": {
		short: "Generate the bash completion script",
		long: "Generate the autocompletion script for bash. It needs the bash-completion\n" +
			"package.\n\n" +
			"Load it in the current shell session with the first example. To load it in\n" +
			"every new session, write the script to /etc/bash_completion.d/dot on Linux or\n" +
			"to $(brew --prefix)/etc/bash_completion.d/dot on macOS, then start a new\n" +
			"shell.",
	},
	"completion zsh": {
		short: "Generate the zsh completion script",
		long: "Generate the autocompletion script for zsh. If completion is not enabled in\n" +
			"your environment yet, add 'autoload -U compinit; compinit' to ~/.zshrc first.\n\n" +
			"Load it in the current shell session with the first example. To load it in\n" +
			"every new session, write the script to \"${fpath[1]}/_dot\" on Linux or to\n" +
			"$(brew --prefix)/share/zsh/site-functions/_dot on macOS, then start a new\n" +
			"shell.",
	},
	"completion fish": {
		short: "Generate the fish completion script",
		long: "Generate the autocompletion script for fish.\n\n" +
			"Load it in the current shell session with the first example. To load it in\n" +
			"every new session, write the script to ~/.config/fish/completions/dot.fish,\n" +
			"then start a new shell.",
	},
	"completion powershell": {
		short: "Generate the powershell completion script",
		long: "Generate the autocompletion script for powershell.\n\n" +
			"Load it in the current shell session with the first example. To load it in\n" +
			"every new session, add the output of that command to your powershell profile.",
	},
}

// applyHelpMetadata fills Example on every command outside dot ai, dot sync,
// dot peer and dot remote that has none, and restates the generated help and
// completion text. Call it after Cobra's generated commands exist.
func applyHelpMetadata(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		key := strings.TrimPrefix(strings.TrimPrefix(c.CommandPath(), root.CommandPath()), " ")
		if g, ok := generatedHelp[key]; ok {
			c.Short, c.Long = g.short, g.long
		}
		if c.Example == "" {
			if ex, ok := helpExamples[key]; ok {
				c.Example = "  " + strings.Join(ex, "\n  ")
			}
		}
		for _, sc := range c.Commands() {
			walk(sc)
		}
	}
	walk(root)
}
