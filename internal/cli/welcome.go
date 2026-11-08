package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// printWelcome shows a friendly getting-started guide when invoked without args.
func printWelcome(cmd *cobra.Command, version, commit string) {
	p := printerFrom(cmd)
	v, c := ResolveVersion(version, commit)
	p.Header("dot " + v + " (" + c + ")")
	p.Line("  %s", ui.StyleValue.Render("Declarative user environment & workspace manager."))

	// Detect status
	state, _ := config.LoadState()
	initialized := state != nil && state.Name != ""

	if !initialized {
		p.Section("Not configured yet")
		p.Line("  %s  %s", ui.StyleKey.Render("1."), ui.StyleValue.Render("dot init"))
		p.Line("     %s", ui.StyleHint.Render("Interactive setup — asks for name, email, profile, modules"))
		p.Line("  %s  %s", ui.StyleKey.Render("2."), ui.StyleValue.Render("dot apply"))
		p.Line("     %s", ui.StyleHint.Render("Apply configuration to the system"))
	} else {
		p.Section("Current configuration")
		p.KV("Profile", state.Profile+" ("+state.Name+")")
	}

	p.Section("Common commands")
	for _, c := range welcomeCommands {
		printWelcomeCmd(p, c.cmd, c.desc)
	}

	p.Blank()
	p.Line("  %s", ui.StyleHint.Render("Tip: 'dotfiles' remains a back-compat alias; use 'dot apply'"))
	p.Line("  %s", ui.StyleHint.Render("See 'dot usecase' for detailed workflows"))
	p.Line("  %s", ui.StyleHint.Render("See 'dot help' for all commands"))
	p.Blank()
}

func printWelcomeCmd(p *Printer, cmd, desc string) {
	p.Line("  %s  %s",
		ui.StyleValue.Bold(true).Render(cmd),
		ui.StyleHint.Render("— "+desc))
}

// newUsecaseCmd returns a command that prints detailed workflow examples.
func newUsecaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "usecase",
		Short: "Show detailed use cases and workflows",
		Long:  "Walk through common workflows with real commands: first-time setup, daily use, sync, workspace, upgrades, troubleshooting.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			printUsecases(cmd)
			return nil
		},
	}
}

func printUsecases(cmd *cobra.Command) {
	p := printerFrom(cmd)
	p.Header("dot Use Cases")
	p.Line("  %s", ui.StyleHint.Render("Note: 'dot' is the canonical command; 'dotfiles' remains a back-compat alias."))
	p.Line("  %s", ui.StyleHint.Render("Examples below use canonical 'dot'."))

	for i, sec := range useSections {
		section(p, fmt.Sprintf("%d. %s", i+1, sec.title), sec.intro, sec.cases)
	}

	p.Section("Global flags (work with most commands)")
	printFlag(p, "--yes", "Unattended mode, skip all prompts")
	printFlag(p, "--dry-run", "Preview without making changes")
	printFlag(p, "--profile", "Override profile (minimal|full|server)")
	printFlag(p, "--module", "Run specific modules only (repeatable)")
	printFlag(p, "--config", "Custom config YAML path")
	printFlag(p, "--home", "Override home directory (admin setup)")

	p.Blank()
	p.Line("  %s", ui.StyleHint.Render("Run 'dot <command> --help' for detailed options"))
	p.Blank()
}

type usecase struct {
	cmd  string
	desc string
}

type useSection struct {
	title, intro string
	cases        []usecase
}

// welcomeCommands and useSections are data so a test can resolve every
// `dot ...` they name against the live command tree (#372).
var welcomeCommands = []usecase{
	{"dot status", "Full environment status at a glance"},
	{"dot apply", "Apply configuration"},
	{"dot diff", "Show the changes apply would make"},
	{"dot config", "Display current config"},
	{"dot clean", "Remove junk directories (node_modules, caches)"},
	{"dot sync", "Sync the workspace with a local or SSH mirror"},
	{"dot peer", "Keep two machines' workspaces in step"},
	{"dot ai", "Set up and maintain the AI coding agents"},
	{"dot tunnel", "Manage Cloudflare Tunnel SSH access"},
	{"dot guard", "Claude Code safety hooks (careful + freeze)"},
	{"dot backup", "One-stop backup wizard (profile, apps, AI, secrets)"},
	{"dot restore", "One-stop restore wizard (cross-host migration)"},
	{"dot apps install", "Install macOS cask apps (interactive picker)"},
	{"dot open <project>", "Launch/resume a tmux workspace"},
	{"dot list", "Show registered projects"},
	{"dot update", "Install the latest dot release"},
}

var useSections = []useSection{
	{"First-time setup on a new machine", "Fresh install: get from zero to productive in minutes.", []usecase{
		{"curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash", "Download binary from GitHub Releases"},
		{"dot init", "Interactive setup (auto-detects git config, timezone, SSH keys)"},
		{"dot apply --dry-run", "Preview all changes before applying"},
		{"dot apply", "Apply to system (installs packages, shell, git, etc.)"},
	}},
	{"Safe apply: preserve existing environment", "Avoid overwriting your current config.", []usecase{
		{"dot init --check-only", "Scan environment, report conflicts"},
		{"dot init --detect", "Generate config file from detected environment"},
		{"dot diff --summary", "Show which modules are satisfied and which need changes"},
		{"dot diff --module shell", "Preview changes for a specific module"},
		{"dot apply --module shell --module git", "Apply only selected modules"},
	}},
	{"Daily workspace: tmux + AI", "Launch multi-panel dev workspaces.", []usecase{
		{"dot open myproject", "Auto-register current dir and launch tmux workspace"},
		{"dot open myproject --layout ahub", "Launch with a specific layout"},
		{"dot register myproject ~/dev/app --layout claude", "Register project path with default layout"},
		{"dot list", "Show all registered projects and active sessions"},
		{"dot layouts", "Show available pane layouts"},
		{"dot hub myproject", "Focus the AHUB console of an ahub workspace"},
		{"dot hub myproject git", "Focus or add the workspace's git window"},
		{"dot layouts doctor", "Check which workspace tools are installed"},
		{"dot stop myproject", "Stop a running tmux session"},
	}},
	{"Workspace cleanup", "Remove junk that wastes disk and slows sync. _sys/ is always protected.", []usecase{
		{"dot clean", "Scan and preview (node_modules, __pycache__, .venv, .cache, .DS_Store)"},
		{"dot clean --yes", "Actually delete safe patterns"},
		{"dot clean --all --yes", "Include risky patterns (dist/, build/, out/, target/)"},
		{"dot status", "Full environment dashboard (system, modules, secrets, sync, workspace)"},
	}},
	{"Workspace sync to a mirror", "rsync the workspace to a local folder (a cloud client's) or an SSH host; push and pull preview first.", []usecase{
		{"dot sync init", "One-time: create <workspace>/.dotfiles/sync/"},
		{"dot sync target local:~/Dropbox/work", "Set the mirror (local: path or ssh:user@host:path)"},
		{"dot sync setup --push-interval=15m --push-mode=clean", "Opt into a scheduled push when no conflicts are detected"},
		{"dot sync push", "Preview workspace -> mirror changes, then confirm"},
		{"dot sync pull", "Preview baseline-tracked mirror changes, then confirm"},
		{"dot sync intake", "Stage new mirror-origin files into inbox/gdrive/<ts>/"},
		{"dot sync inbox list", "Show staged run-dirs, imports manifest, tombstones"},
		{"dot sync status", "Paths, filters, propagation policy, schedulers, last runs"},
		{"dot sync pause", "Stop managed schedulers and set the paused gate"},
		{"dot sync conflicts list", "List conflict backups"},
	}},
	{"Peer sync between two machines", "Mirror the workspace and selected home files between two Macs over SSH.", []usecase{
		{"dot peer init --host user@other-mac", "One-time: write <workspace>/.dotfiles/peer/ with the other machine as target"},
		{"dot peer diff", "Show what a sync would transfer, without changing anything"},
		{"dot peer sync", "Run one sync now"},
		{"dot peer setup", "Schedule peer sync"},
		{"dot peer status", "Owner, scheduler, last run and conflicts"},
		{"dot peer git fetch", "Fetch every workspace repo so git state matches the other machine"},
		{"dot peer git realign --apply", "Move each repo's HEAD to the commit its files already match"},
		{"dot peer doctor", "Probe everything that silently breaks a peer transfer"},
	}},
	{"Workspace structure on both trees", "Change folders in the workspace and its sync mirror together, and find where they drifted.", []usecase{
		{"dot ws init", "Clone each configured workspace repo (work, vault) into the workspace"},
		{"dot ws mkdir projects/new", "Create a directory on both workspaces"},
		{"dot ws mv projects/old projects/new", "Rename or move a directory on both workspaces"},
		{"dot ws rm projects/old", "Remove a directory from both workspaces"},
		{"dot ws audit", "Report structural mismatches between the workspaces"},
		{"dot ws reconcile", "Resolve those mismatches interactively"},
	}},
	{"SSH over Cloudflare Tunnel", "Expose this Mac's SSH service through a dot-managed local Cloudflare Tunnel.", []usecase{
		{"dot tunnel setup", "Configure cloudflared, Remote Login, DNS route, and LaunchDaemon"},
		{"dot tunnel setup --dry-run", "Preview the system commands without changing this Mac"},
		{"dot tunnel status", "Show cloudflared, daemon, port 22, config, and connector status"},
		{"dot tunnel log 100", "Tail the LaunchDaemon error log"},
		{"dot tunnel client add mac.example.com", "Add the SSH ProxyCommand block on a client machine"},
		{"ssh user@mac.example.com", "Connect through Cloudflare Access browser authentication"},
		{"dot tunnel uninstall", "Remove the daemon and optionally remove config/credentials/tunnel"},
	}},
	{"Secrets management (age encryption)", "Encrypt SSH keys and shell secrets with age.", []usecase{
		{"dot secrets init --scaffold", "Create empty shell secrets template (0600), then encrypt SSH key + any shell secrets"},
		{"dot secrets init", "Encrypt existing SSH key + shell secrets"},
		{"dot secrets status", "Show decrypted/encrypted file status"},
		{"dot secrets backup ~/backup", "Copy encrypted files to backup location"},
		{"dot secrets restore ~/backup", "Decrypt from backup on new machine"},
	}},
	{"macOS app management (cask install + settings backup)", "Install apps from an embedded catalog; back up and restore per-app settings.", []usecase{
		{"dot apps list", "Browse the cask catalog (★ defaults, ✓ installed)"},
		{"dot apps install", "Interactive checkbox picker -> save selection -> install missing casks"},
		{"dot apps install raycast obsidian", "Install specific casks by token"},
		{"dot apps install --defaults", "Install the catalog's default set"},
		{"dot apps status", "Show install + backup presence for each tracked app"},
		{"dot apps backup", "Snapshot per-app settings (plists, Application Support) to backup root"},
		{"dot apps backup moom hazel --to ~/backup", "Back up specific apps to a custom path"},
		{"dot apps restore", "Restore all backed-up settings (confirms first, flushes cfprefsd)"},
	}},
	{"Profile snapshots (versioned config backup)", "Snapshot config.yaml, cask lists, and optional age keys into timestamped versions.", []usecase{
		{"dot profile backup", "Create a new version snapshot under <backup-root>/profiles/<host>/"},
		{"dot profile backup --tag pre-migration --include-age-key", "Tag the snapshot and include ~/.ssh/age_key*"},
		{"dot profile list", "List all snapshots for this host (★ marks latest)"},
		{"dot profile restore", "Restore the latest snapshot (config.yaml -> ~/.config/dotfiles/)"},
		{"dot profile restore --version 20260416T083412Z --include-age-key", "Restore a specific version including age keys"},
		{"dot profile prune --keep 5", "Delete snapshots older than the 5 most recent"},
	}},
	{"Cross-machine migration", "Move your full setup to a new Mac: one wizard per side.", []usecase{
		{"dot backup", "[old machine] One-stop wizard: profile + apps + AI/Maru + secrets"},
		{"dot restore", "[new machine] Pick source host, restore in safe order, optional dot apply"},
		{"dot backup --yes --scope profile,ai,secrets", "Unattended backup of selected domains"},
		{"dot restore --yes --host <src>", "Unattended cross-host restore"},
		{"dot profile restore --include-age-key && dot apply && dot apps restore && dot ai restore", "[new machine] Manual sequence equivalent to the wizard"},
	}},
	{"AI coding agents", "Choose the agents once; dot keeps their instructions, skills and tools in step.", []usecase{
		{"dot ai setup", "Choose the agents and add-ons this machine runs"},
		{"dot ai tools status", "Show what the saved selection installs and where it drifted"},
		{"dot ai agents apply", "Write the shared agent instructions to every selected agent"},
		{"dot ai family install --all", "Install the agent-hub family (ahub, amem, vmem, acon, ares)"},
		{"dot ai family update", "Move installed family tools to their latest release"},
		{"dot ai policy status", "Show the adaptive work policy each agent runs under"},
		{"dot admit -- make test", "Run a heavy job in this repository's resource slot"},
	}},
	{"Prompt style", "Switch between a minimal and a rich Starship prompt.", []usecase{
		{"dot reconfigure", "Re-run init (Prompt style: minimal / rich)"},
		{"dot apply --module terminal", "Deploy the selected starship.toml immediately"},
	}},
	{"Updates and reconfiguration", "Keep the tool and config current.", []usecase{
		{"dot update --check", "Check for newer version"},
		{"dot update", "Download and install latest release"},
		{"dot rollback", "Restore the previous standalone dot binary"},
		{"dot ai update", "Update the selected AI CLIs and add-ons (no blanket plugin or skill update)"},
		{"dot ai auth relogin <server>", "Re-authenticate an MCP server that lost its OAuth credentials"},
		{"dot reconfigure", "Re-run init prompts with current values as defaults"},
		{"dot version", "Show installed version and build info"},
	}},
	{"GPU server / DGX provisioning", "Deploy on a fresh GPU server: auto-detects NVIDIA + CUDA.", []usecase{
		{"curl -fsSL .../install.sh | bash", "Install binary"},
		{"dot init --yes", "Auto-selects 'server' profile when GPU detected"},
		{"dot apply --yes", "Apply server profile (no workspace, fonts, gpg, secrets)"},
	}},
	{"Troubleshooting", "Diagnose and recover from issues.", []usecase{
		{"dot layouts doctor", "Check workspace tool installation"},
		{"dot status", "Full environment dashboard at a glance"},
		{"dot config", "Show loaded configuration and system info"},
		{"dot sync log 100", "View recent workspace sync log entries"},
		{"dot peer doctor", "Probe the peer connection and transfer prerequisites"},
	}},
	{"Claude Code safety hooks (guard)", "Warn before destructive shell commands and fence file edits into one directory.", []usecase{
		{"dot guard enable", "Register careful warnings (rm -rf, DROP, force-push, ...; new sessions only)"},
		{"dot guard freeze ./src", "Deny Edit/Write outside ./src (takes effect immediately)"},
		{"dot guard unfreeze", "Clear the edit boundary"},
		{"dot guard status", "Show hook registration and binary health"},
		{"dot guard disable", "Remove guard hook entries; other hooks untouched"},
	}},
}

func section(p *Printer, title, intro string, cases []usecase) {
	p.Section(title)
	p.Line("  %s", ui.StyleHint.Render(intro))
	for _, c := range cases {
		p.Line("  $ %s", ui.StyleValue.Bold(true).Render(c.cmd))
		p.Line("    %s", ui.StyleHint.Render(c.desc))
	}
}

func printFlag(p *Printer, flag, desc string) {
	p.Line("  %s  %s",
		ui.StyleKey.Render(flag),
		ui.StyleHint.Render(desc))
}
