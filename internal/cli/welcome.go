package cli

import (
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
	printWelcomeCmd(p, "dot status", "Full environment status at a glance")
	printWelcomeCmd(p, "dot apply", "Apply configuration")
	printWelcomeCmd(p, "dot check", "Show pending changes without applying")
	printWelcomeCmd(p, "dot config", "Display current config")
	printWelcomeCmd(p, "dot clean", "Remove junk directories (node_modules, caches)")
	printWelcomeCmd(p, "dot sync", "Sync binaries with remote server via rsync")
	printWelcomeCmd(p, "dot gsync", "Sync workspace artifacts with Drive mirror")
	printWelcomeCmd(p, "dot tunnel", "Manage Cloudflare Tunnel SSH access")
	printWelcomeCmd(p, "dot guard", "Claude Code safety hooks (careful + freeze)")
	printWelcomeCmd(p, "dot backup", "One-stop backup wizard (profile, apps, AI, secrets)")
	printWelcomeCmd(p, "dot restore", "One-stop restore wizard (cross-host migration)")
	printWelcomeCmd(p, "dot apps install", "Install macOS cask apps (interactive picker)")
	printWelcomeCmd(p, "dot apps backup", "Snapshot macOS app settings")
	printWelcomeCmd(p, "dot profile backup", "Version-snapshot config + app lists + secrets")
	printWelcomeCmd(p, "dot open <project>", "Launch/resume a tmux workspace")
	printWelcomeCmd(p, "dot list", "Show registered projects")

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

	section(p, "1. First-time setup on a new machine",
		"Fresh install — get from zero to productive in minutes.",
		[]usecase{
			{"curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash",
				"Download binary from GitHub Releases"},
			{"dot init",
				"Interactive setup (auto-detects git config, timezone, SSH keys)"},
			{"dot apply --dry-run",
				"Preview all changes before applying"},
			{"dot apply",
				"Apply to system (installs packages, shell, git, etc.)"},
		})

	section(p, "2. Safe apply — preserve existing environment",
		"Avoid overwriting your current config.",
		[]usecase{
			{"dot preflight --check-only",
				"Scan environment, report conflicts"},
			{"dot preflight",
				"Generate config file from detected environment"},
			{"dot check",
				"Show which modules have pending changes"},
			{"dot diff --module shell",
				"Preview changes for a specific module"},
			{"dot apply --module shell --module git",
				"Apply only selected modules"},
		})

	section(p, "3. Daily workspace — tmux + AI",
		"Launch multi-panel dev workspaces with canonical 'dot'",
		[]usecase{
			{"dot open myproject",
				"Auto-register current dir and launch tmux workspace"},
			{"dot open myproject --layout claude",
				"Launch with specific layout override"},
			{"dot register myproject ~/dev/app --layout claude",
				"Register project path with default layout"},
			{"dot list",
				"Show all registered projects and active sessions"},
			{"dot layouts",
				"Show available pane layouts (dev, claude, monitor)"},
			{"dot doctor",
				"Check which workspace tools are installed"},
			{"dot stop myproject",
				"Stop a running tmux session"},
		})

	section(p, "4. Workspace cleanup",
		"Remove junk that wastes disk and breaks Drive sync. _sys/ is always protected.",
		[]usecase{
			{"dot clean",
				"Scan and preview (node_modules, __pycache__, .venv, .cache, .DS_Store)"},
			{"dot clean --yes",
				"Actually delete safe patterns"},
			{"dot clean --all --yes",
				"Include risky patterns (dist/, build/, out/, target/)"},
			{"dot status",
				"Full environment dashboard (system, modules, secrets, sync, workspace)"},
		})

	section(p, "5. Remote server sync (rsync)",
		"Sync binary files with Ubuntu server over SSH. Text files use git only.",
		[]usecase{
			{"dot sync setup",
				"One-time setup: rsync, SSH key, remote host, binary extensions, scheduler"},
			{"dot sync",
				"Pull-then-push (default): pull newer binaries, then push local"},
			{"dot sync pull",
				"Pull only: remote → local (--update, safe)"},
			{"dot sync push",
				"Push only: local → remote (--delete-after, local is authority)"},
			{"dot sync status",
				"Show sync health, scheduler state, last result"},
		})

	section(p, "6. Local rsync mirror (gsync)",
		"Git-shared baseline tracks mirror payloads; push/pull preview first, intake stages new mirror files.",
		[]usecase{
			{"dot gsync init",
				"One-time: create <workspace>/.dotfiles/gdrive-sync/ + migrate global state"},
			{"dot gsync setup",
				"Check rsync and keep managed schedulers off by default"},
			{"dot gsync setup --push-interval=15m --push-mode=clean",
				"Opt into automatic push only when no Drive conflicts are detected"},
			{"dot gsync setup --pull-interval=15m --pull-mode=force",
				"Opt into automatic pull with overwrite backups"},
			{"dot gsync push",
				"Preview workspace → mirror changes, then confirm"},
			{"dot gsync push --propagate=create,update,delete",
				"Override policy for one run (refused if list is empty)"},
			{"dot gsync pull",
				"Preview baseline-tracked mirror payload changes, then confirm"},
			{"dot gsync intake",
				"Stage new mirror-origin files into inbox/gdrive/<ts>/"},
			{"dot gsync intake --strict",
				"Use sha256 fingerprints (catches mtime-preserved content edits)"},
			{"dot gsync inbox list",
				"Show staged run-dirs, imports manifest, tombstones"},
			{"dot gsync inbox forget <relpath>",
				"Drop one imports entry — next intake re-stages this path"},
			{"dot gsync inbox clear",
				"Empty imports.manifest and tombstones.log"},
			{"dot gsync status",
				"Paths, filter mode, propagation policy, schedulers, last-pull/push/intake"},
			{"dot gsync pause",
				"Stop managed schedulers + set the paused gate"},
			{"dot gsync resume",
				"Clear paused gate, re-arm installed schedulers"},
			{"dot gsync shared",
				"Manage manually curated shared-folder exclusions"},
		})

	section(p, "7. SSH over Cloudflare Tunnel",
		"Expose this Mac's SSH service through a dot-managed local Cloudflare Tunnel.",
		[]usecase{
			{"dot tunnel setup",
				"Configure cloudflared, Remote Login, DNS route, and LaunchDaemon"},
			{"dot tunnel setup --dry-run",
				"Preview the system commands without changing this Mac"},
			{"dot tunnel status",
				"Show cloudflared, daemon, port 22, config, and connector status"},
			{"dot tunnel log 100",
				"Tail the LaunchDaemon error log"},
			{"dot tunnel client add mac.example.com",
				"Add the SSH ProxyCommand block on a client machine"},
			{"ssh user@mac.example.com",
				"Connect through Cloudflare Access browser authentication"},
			{"dot tunnel uninstall",
				"Remove the daemon and optionally remove config/credentials/tunnel"},
		})

	section(p, "8. Secrets management (age encryption)",
		"Encrypt SSH keys and shell secrets with age.",
		[]usecase{
			{"dot secrets init --scaffold",
				"Create empty shell secrets template (0600), then encrypt SSH key + any shell secrets"},
			{"dot secrets init",
				"Encrypt existing SSH key + shell secrets"},
			{"dot secrets status",
				"Show decrypted/encrypted file status"},
			{"dot secrets backup ~/backup",
				"Copy encrypted files to backup location"},
			{"dot secrets restore ~/backup",
				"Decrypt from backup on new machine"},
		})

	section(p, "9. macOS app management (cask install + settings backup)",
		"Install apps from an embedded catalog; back up and restore per-app settings.",
		[]usecase{
			{"dot apps list",
				"Browse the cask catalog (★ defaults, ✓ installed)"},
			{"dot apps install",
				"Interactive checkbox picker → save selection → install missing casks"},
			{"dot apps install raycast obsidian",
				"Install specific casks by token"},
			{"dot apps install --defaults",
				"Install the catalog's 20-app default set"},
			{"dot apps status",
				"Show install + backup presence for each tracked app"},
			{"dot apps backup",
				"Snapshot per-app settings (plists, Application Support) to backup root"},
			{"dot apps backup moom hazel --to ~/backup",
				"Back up specific apps to a custom path"},
			{"dot apps restore",
				"Restore all backed-up settings (confirms first, flushes cfprefsd)"},
		})

	section(p, "10. Profile snapshots (versioned config backup)",
		"Snapshot config.yaml, cask lists, and optional secrets into timestamped versions.",
		[]usecase{
			{"dot profile backup",
				"Create a new version snapshot under <backup-root>/profiles/<host>/"},
			{"dot profile backup --tag pre-migration --include-secrets",
				"Tag the snapshot and include ~/.ssh/age_key*"},
			{"dot profile list",
				"List all snapshots for this host (★ marks latest)"},
			{"dot profile restore",
				"Restore the latest snapshot (config.yaml → ~/.config/dotfiles/)"},
			{"dot profile restore --version 20260416T083412Z --include-secrets",
				"Restore a specific version including age keys"},
			{"dot profile prune --keep 5",
				"Delete snapshots older than the 5 most recent"},
		})

	section(p, "11. Cross-machine migration",
		"Move your full setup to a new Mac in minutes — one wizard per side.",
		[]usecase{
			{"dot backup",
				"[old machine] One-stop wizard: profile + apps + AI/Maru + secrets"},
			{"dot restore",
				"[new machine] Pick source host, restore in safe order, optional dot apply"},
			{"dot backup --yes --scope profile,ai,secrets",
				"Unattended backup of selected domains"},
			{"dot restore --yes --host <src>",
				"Unattended cross-host restore"},
			{"dot profile backup --tag pre-migration --include-secrets",
				"Individual commands remain available for fine-grained control"},
			{"dot profile restore --include-secrets && dot apply && dot apps restore && dot ai restore",
				"[new machine] Manual sequence equivalent to the wizard"},
		})

	section(p, "12. Prompt style",
		"Switch between a minimal and a rich Starship prompt.",
		[]usecase{
			{"dot reconfigure",
				"Re-run init (Prompt style: minimal / rich)"},
			{"dot apply --module terminal",
				"Deploy the selected starship.toml immediately"},
		})

	section(p, "13. Updates and reconfiguration",
		"Keep the tool and config current.",
		[]usecase{
			{"dot update --check",
				"Check for newer version"},
			{"dot update",
				"Download and install latest release"},
			{"dot ai update",
				"Update AI CLIs, Claude plugins, marketplaces, and skills"},
			{"dot ai auth relogin <server>",
				"Re-authenticate an MCP server that lost its OAuth credentials"},
			{"dot reconfigure",
				"Re-run init prompts with current values as defaults"},
			{"dot version",
				"Show installed version and build info"},
		})

	section(p, "14. GPU server / DGX provisioning",
		"Deploy on a fresh GPU server — auto-detects NVIDIA + CUDA.",
		[]usecase{
			{"curl -fsSL .../install.sh | bash",
				"Install binary"},
			{"dot init --yes",
				"Auto-selects 'server' profile when GPU detected"},
			{"dot apply --yes",
				"Apply server profile (no workspace, fonts, gpg, secrets)"},
		})

	section(p, "15. Troubleshooting",
		"Diagnose and recover from issues.",
		[]usecase{
			{"dot doctor",
				"Check workspace tool installation"},
			{"dot status",
				"Full environment dashboard at a glance"},
			{"dot config",
				"Show loaded configuration and system info"},
			{"dot sync log 100",
				"View recent rsync sync log entries"},
		})

	section(p, "16. Claude Code safety hooks (guard)",
		"Warn before destructive shell commands and fence file edits into one directory.",
		[]usecase{
			{"dot guard enable",
				"Register careful warnings (rm -rf, DROP, force-push, ...; new sessions only)"},
			{"dot guard freeze ./src",
				"Deny Edit/Write outside ./src (takes effect immediately)"},
			{"dot guard unfreeze",
				"Clear the edit boundary"},
			{"dot guard status",
				"Show hook registration and binary health"},
			{"dot guard disable",
				"Remove guard hook entries; other hooks untouched"},
		})

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
