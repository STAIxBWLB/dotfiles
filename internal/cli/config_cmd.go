package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show current configuration",
		Long:  "Display active profile, system info, enabled modules, and user settings.",
		RunE:  runConfig,
	}
	cmd.AddCommand(newConfigExportCmd())
	return cmd
}

func newConfigExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export [path]",
		Short: "Export configuration to a portable YAML file",
		Long: `Export the current dot configuration to stdout or a file.
The exported file can be used on another machine with 'dot init --from <file>'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runConfigExport,
	}
}

func runConfigExport(cmd *cobra.Command, args []string) error {
	homeOverride, _ := cmd.Flags().GetString("home")

	var state *config.UserState
	var err error
	if homeOverride != "" {
		state, err = config.LoadStateForHome(homeOverride)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if state.Name == "" {
		return fmt.Errorf("no configuration found — run 'dot init' first")
	}

	// Not yaml.Marshal: export is a second door onto the same file format, and
	// it must stamp the version the same way saveStateAt does. Marshaling the
	// struct directly re-emitted a forward-version label over a payload yaml.v3
	// had already stripped (found by review on PR #88).
	data, err := config.MarshalState(state)
	if err != nil {
		return err
	}

	p := printerFrom(cmd)
	if len(args) == 0 {
		p.Raw("%s", string(data))
		return nil
	}

	path := args[0]
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	p.Line("Configuration exported to %s", path)
	return nil
}

func runConfig(cmd *cobra.Command, _ []string) error {
	profileName, _ := cmd.Flags().GetString("profile")
	configPath, _ := cmd.Flags().GetString("config")
	homeOverride, _ := cmd.Flags().GetString("home")

	if profileName == "" {
		profileName = os.Getenv("DOTFILES_PROFILE")
	}
	if homeOverride == "" {
		homeOverride = os.Getenv("DOTFILES_HOME")
	}

	var state *config.UserState
	var err error
	if homeOverride != "" {
		state, err = config.LoadStateForHome(homeOverride)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if profileName == "" && state.Profile != "" {
		profileName = state.Profile
	}

	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}

	if profileName == "" && configPath == "" {
		profileName = sysInfo.SuggestProfile()
	}

	cfg, err := config.Load(profileName, configPath, sysInfo)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	config.ApplyStateToConfig(cfg, state)
	config.ApplyEnvOverrides(cfg)

	p := printerFrom(cmd)
	p.Header("dot Configuration")
	p.KV("Profile", profileName)
	if homeOverride != "" {
		p.KV("Config", config.StatePathForHome(homeOverride))
	} else {
		p.KV("Config", config.StatePath())
	}

	p.Section("System")
	p.KV("OS", sysInfo.OS+"/"+sysInfo.Arch)
	p.KV("Hostname", sysInfo.Hostname)
	p.KV("Shell", sysInfo.Shell)
	if sysInfo.HasBrew {
		p.KV("Brew", sysInfo.BrewPath)
	}
	if sysInfo.HasGit {
		p.KV("Git", sysInfo.GitVersion)
	}
	if sysInfo.HasNVIDIAGPU {
		p.KV("GPU", sysInfo.GPUModel)
	}
	if sysInfo.HasCUDA {
		p.KV("CUDA", sysInfo.CUDAHome)
	}

	p.Section("User")
	p.KV("Name", cfg.Name)
	p.KV("Email", cfg.Email)
	p.KV("GitHub", cfg.GithubUser)
	p.KV("Timezone", cfg.Timezone)

	if cfg.Modules.Workspace.Enabled {
		p.Section("Workspace")
		p.KV("Path", cfg.Modules.Workspace.Path)
		if cfg.Modules.Workspace.Gdrive != "" {
			p.KV("Cloud root", cfg.Modules.Workspace.Gdrive)
		}
		if cfg.Modules.Workspace.GdriveSymlink != "" {
			p.KV("Cloud link", cfg.Modules.Workspace.GdriveSymlink)
		}
		if cfg.Modules.Workspace.Symlink != "" {
			p.KV("Symlink", cfg.Modules.Workspace.Symlink)
		}
		for _, repo := range cfg.Modules.Workspace.Repos {
			p.KV(repo.Name+" repo", repo.Remote)
		}
	}

	p.Section("Modules")
	allModules := []struct {
		name    string
		enabled bool
		detail  string
	}{
		{"packages", cfg.Modules.Packages.Enabled, ""},
		{"shell", cfg.Modules.Shell.Enabled, ""},
		{"node", cfg.Modules.Node.Enabled, ""},
		{"git", cfg.Modules.Git.Enabled, fmtIf(cfg.Modules.Git.Signing, "signing")},
		{"ssh", cfg.Modules.SSH.Enabled, cfg.Modules.SSH.KeyName},
		{"terminal", cfg.Modules.Terminal.Enabled, terminalDetail(cfg)},
		{"tmux", cfg.Modules.Tmux.Enabled, ""},
		{"workspace", cfg.Modules.Workspace.Enabled, workspaceDetail(cfg)},
		{"ai", cfg.Modules.AI.Enabled, "CLI/config helpers"},
		{"fonts", cfg.Modules.Fonts.Enabled, cfg.Modules.Fonts.Family},
		{"macapps", cfg.Modules.MacApps.Enabled, macappsDetail(state)},
		{"conda", cfg.Modules.Conda.Enabled, ""},
		{"gpg", cfg.Modules.GPG.Enabled, ""},
		{"secrets", cfg.Modules.Secrets.Enabled, ""},
		{"container", cfg.Modules.Container.Enabled, cfg.Modules.Container.Backend},
	}

	for _, m := range allModules {
		marker := ui.StyleHint.Render(ui.MarkAbsent)
		detail := ""
		if m.enabled {
			marker = ui.StyleSuccess.Render(ui.MarkPresent)
			if m.detail != "" {
				detail = ui.StyleHint.Render("  (" + m.detail + ")")
			}
		}
		p.Bullet(marker, ui.StyleValue.Render(m.name)+detail)
	}

	pkgs := cfg.AllPackages()
	if len(pkgs) > 0 {
		p.Section(fmt.Sprintf("Packages (%d)", len(pkgs)))
		p.Line("  %s", ui.StyleHint.Render(strings.Join(pkgs, ", ")))
	}

	casks := cfg.AllCasks()
	if len(casks) > 0 {
		p.Section(fmt.Sprintf("Casks — install list (%d)", len(casks)))
		p.Line("  %s", ui.StyleHint.Render(strings.Join(casks, ", ")))
	}

	if len(state.Modules.MacApps.BackupApps) > 0 {
		p.Section(fmt.Sprintf("Casks — backup list (%d)", len(state.Modules.MacApps.BackupApps)))
		p.Line("  %s", ui.StyleHint.Render(strings.Join(state.Modules.MacApps.BackupApps, ", ")))
	}

	if state.Modules.MacApps.BackupRoot != "" {
		p.Section("Backup")
		p.KV("Root", state.Modules.MacApps.BackupRoot)
		if state.Modules.MacApps.LastBackup != nil {
			lb := state.Modules.MacApps.LastBackup
			p.KV("Last backup", fmt.Sprintf("%s (%d files)", lb.Time.Format("2006-01-02 15:04"), lb.Files))
			p.KV("Path", lb.Path)
		}
	}

	if state.Secrets.AgeIdentity != "" {
		p.Section("Secrets")
		p.KV("Age identity", state.Secrets.AgeIdentity)
		for i, r := range state.Secrets.AgeRecipients {
			p.KV(fmt.Sprintf("Recipient %d", i+1), r)
		}
		if state.Secrets.LastBackup != nil {
			lb := state.Secrets.LastBackup
			p.KV("Last backup", fmt.Sprintf("%s (%d files)", lb.Time.Format("2006-01-02 15:04"), lb.Files))
		}
	}

	if state.Modules.Rsync.RemoteHost != "" {
		p.Section("Sync")
		p.KV("rsync", state.Modules.Rsync.RemoteHost)
		if state.Modules.Rsync.RemotePath != "" {
			p.KV("  path", state.Modules.Rsync.RemotePath)
		}
		if state.Modules.Rsync.Interval > 0 {
			p.KV("  interval", formatInterval(state.Modules.Rsync.Interval))
		}
	}

	p.Blank()
	return nil
}

func workspaceDetail(cfg *config.Config) string {
	detail := cfg.Modules.Workspace.Path
	if n := len(cfg.Modules.Workspace.Repos); n > 0 {
		detail += fmt.Sprintf(" (%d repo(s))", n)
	}
	return detail
}

func terminalDetail(cfg *config.Config) string {
	parts := []string{}
	if cfg.Modules.Terminal.PromptStyle != "" {
		parts = append(parts, "prompt="+cfg.Modules.Terminal.PromptStyle)
	}
	if len(cfg.Modules.Terminal.Apps) > 0 {
		parts = append(parts, "apps="+strings.Join(cfg.Modules.Terminal.Apps, ","))
	} else if cfg.Modules.Terminal.Warp {
		parts = append(parts, "warp")
	}
	return strings.Join(parts, ", ")
}

func macappsDetail(state *config.UserState) string {
	n := len(state.Modules.MacApps.Casks) + len(state.Modules.MacApps.CasksExtra)
	if n == 0 {
		return "defaults"
	}
	return fmt.Sprintf("%d casks", n)
}

func fmtIf(cond bool, label string) string {
	if cond {
		return label
	}
	return ""
}
