package ui

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/config/catalog"
	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
)

// ConfigureAI prompts for AI CLI/config helper setup.
func ConfigureAI(state *config.UserState, yes bool, freshDefault bool) error {
	printSection("AI")

	aiDefault := state.Modules.AI.Enabled
	if freshDefault {
		aiDefault = true
	}

	if !yes {
		fmt.Println(StyleHint.Render("  Enables shell aliases and assistant config files; app installation is managed by `dot apps`."))
		fmt.Println(StyleHint.Render("  Managed files: ~/.config/shell/30-ai.sh, ~/.config/claude/settings.json"))
	}

	var err error
	state.Modules.AI.Enabled, err = ConfirmBool("Enable AI CLI/config helpers?", aiDefault, yes)
	if err != nil {
		return err
	}
	if !state.Modules.AI.Enabled {
		state.Modules.AI.AgentsSSOT = false
		state.Modules.AI.Skills = config.AISkillsConfig{}
		return nil
	}
	agentsDefault := state.Modules.AI.AgentsSSOT
	if freshDefault {
		agentsDefault = true
	}
	state.Modules.AI.AgentsSSOT, err = ConfirmBool("Keep global agent instructions in sync?", agentsDefault, yes)
	if err != nil {
		return err
	}
	if yes {
		return nil
	}

	modeDefault := "none"
	switch state.Modules.AI.Skills.Provider {
	case "maru", "anchor": // anchor is a legacy alias for maru
		modeDefault = "maru"
	case "path":
		modeDefault = "custom path"
	}
	fmt.Println(StyleHint.Render("  Skills SSOT is only used by the read-only `dot ai skills` diagnostics; the Maru app manages runtime symlinks."))
	mode, err := Select("Skills SSOT",
		[]string{"none", "maru", "custom path"}, modeDefault, false)
	if err != nil {
		return err
	}
	if mode == "none" {
		state.Modules.AI.Skills = config.AISkillsConfig{}
		return nil
	}

	var skills config.AISkillsConfig
	switch mode {
	case "maru":
		skills.Provider = "maru"
		skills.SSOTPath = ""
	case "custom path":
		skills.Provider = "path"
		defaultPath := state.Modules.AI.Skills.SSOTPath
		if defaultPath == "" {
			defaultPath = "~/.maru/skills"
		}
		skills.SSOTPath, err = Input("Skills SSOT path", defaultPath, false)
		if err != nil {
			return err
		}
	}
	toolDefault := state.Modules.AI.Skills.Tools
	var managedDefaults []string
	for _, tool := range toolDefault {
		if tool == "claude" || tool == "codex" {
			managedDefaults = append(managedDefaults, tool)
		}
	}
	toolDefault = managedDefaults
	if len(toolDefault) == 0 {
		toolDefault = []string{"claude", "codex"}
	}
	skills.Tools, err = MultiSelectLabeled("Target tools for skills", skillToolSelectOptions(), toolDefault, false)
	if err != nil {
		return err
	}
	state.Modules.AI.Skills = skills
	return nil
}

func skillToolSelectOptions() []SelectOption {
	return []SelectOption{
		{Label: "claude - Claude Code", Value: "claude"},
		{Label: "codex - Codex CLI", Value: "codex"},
	}
}

// ConfigureTerminal prompts for prompt style and supported GUI terminal apps.
// Prompt style applies on all platforms; app installation is supported on
// macOS and Arch Linux desktops.
func ConfigureTerminal(state *config.UserState, profile string, system *config.SystemInfo, yes bool) error {
	printSection("Terminal")

	// Prompt style — useful on every platform including servers.
	promptDefault := state.Modules.PromptStyle
	if promptDefault == "" {
		if profile == "server" || profile == "minimal" {
			promptDefault = "minimal"
		} else {
			promptDefault = "rich"
		}
	}
	var err error
	state.Modules.PromptStyle, err = Select("Prompt style",
		[]string{"minimal", "rich"}, promptDefault, yes)
	if err != nil {
		return err
	}

	editorDefault := state.Modules.Editor
	if editorDefault == "" {
		if runtime.GOOS == "darwin" && profile != "server" {
			editorDefault = "zed"
		} else {
			editorDefault = "vi"
		}
	}
	if !yes {
		fmt.Println(StyleHint.Render("  Sets $EDITOR; zed/code are installed as casks on macOS via `dot apps`."))
	}
	state.Modules.Editor, err = Select("Default editor",
		[]string{"zed", "code", "vi"}, editorDefault, yes)
	if err != nil {
		return err
	}

	if profile == "server" {
		state.Modules.Warp = false
		state.Modules.TerminalApps = config.UserTerminalAppsState{}
		return nil
	}

	availableApps := config.TerminalAppOptionsForSystem(system)
	if len(availableApps) == 0 {
		return nil
	}

	supported := make(map[string]bool, len(availableApps))
	for _, app := range availableApps {
		supported[app.Token] = true
	}
	rawDefault := state.Modules.TerminalApps.Apps
	appDefault := make([]string, 0, len(rawDefault))
	for _, app := range rawDefault {
		if supported[app] {
			appDefault = append(appDefault, app)
		}
	}
	if len(appDefault) == 0 && (!state.Modules.TerminalApps.Enabled || len(rawDefault) > 0) {
		if state.Modules.Warp {
			if supported["warp"] {
				appDefault = []string{"warp"}
			}
		}
		if len(appDefault) == 0 {
			appDefault = config.DefaultTerminalApps(profile, system)
		}
	}
	selectedApps, err := MultiSelectLabeled(
		"Terminal apps to install",
		terminalAppSelectOptions(availableApps),
		appDefault,
		yes,
	)
	if err != nil {
		return err
	}
	state.Modules.TerminalApps.Enabled = true
	state.Modules.TerminalApps.Apps = selectedApps
	state.Modules.Warp = sliceutil.Contains(selectedApps, "warp")
	return nil
}

func terminalAppSelectOptions(apps []config.TerminalAppOption) []SelectOption {
	options := make([]SelectOption, len(apps))
	for i, app := range apps {
		options[i] = SelectOption{
			Label: app.Token + " - " + app.Name,
			Value: app.Token,
		}
	}
	return options
}

// ConfigureFonts prompts for font family. Skipped for server/minimal profile.
func ConfigureFonts(state *config.UserState, profile string, yes bool) error {
	if profile == "server" || profile == "minimal" {
		return nil
	}

	printSection("Fonts")

	fontFamily := state.Modules.Fonts.Family
	if fontFamily == "" {
		fontFamily = "FiraCode"
	}

	var err error
	state.Modules.Fonts.Family, err = Select("Font family", []string{"FiraCode", "JetBrainsMono", "Hack"}, fontFamily, yes)
	return err
}

// ConfigureSecrets prompts for age encryption settings with auto-detection.
func ConfigureSecrets(state *config.UserState, home, profile string, yes bool) error {
	if profile == "server" || profile == "minimal" {
		return nil
	}

	printSection("Secrets (age encryption)")

	ageKeys := detectAgeKeys(home)

	if len(ageKeys) == 0 && state.Secrets.AgeIdentity == "" {
		fmt.Println(StyleHint.Render("  No age keys found. Skipping secrets configuration."))
		fmt.Println(StyleHint.Render("  Create one later with: age-keygen -o ~/.ssh/age_key"))
		return nil
	}

	enableSecrets, err := ConfirmBool("Configure age encryption for secrets?", state.Secrets.AgeIdentity != "" || len(ageKeys) > 0, yes)
	if err != nil {
		return err
	}
	if !enableSecrets {
		state.Secrets.AgeIdentity = ""
		state.Secrets.AgeRecipients = nil
		return nil
	}

	identityDefault := state.Secrets.AgeIdentity
	if identityDefault == "" && len(ageKeys) > 0 {
		identityDefault = ageKeys[0]
	}

	if len(ageKeys) > 0 && !yes {
		fmt.Println(StyleHint.Render(fmt.Sprintf("  Found %d age key(s):", len(ageKeys))))
		for _, k := range ageKeys {
			fmt.Println(StyleHint.Render("    • " + k))
		}

		options := make([]string, len(ageKeys))
		copy(options, ageKeys)
		options = append(options, "(enter custom path)")

		selectDefault := identityDefault
		if !sliceutil.Contains(options, selectDefault) {
			selectDefault = ageKeys[0]
		}

		selected, err := Select("Age identity (private key)", options, selectDefault, false)
		if err != nil {
			return err
		}

		if selected == "(enter custom path)" {
			state.Secrets.AgeIdentity, err = Input("Age identity path", identityDefault, false)
			if err != nil {
				return err
			}
		} else {
			state.Secrets.AgeIdentity = selected
		}
	} else {
		state.Secrets.AgeIdentity, err = Input("Age identity path", identityDefault, yes)
		if err != nil {
			return err
		}
	}

	recipientDefault := ""
	if len(state.Secrets.AgeRecipients) > 0 {
		recipientDefault = state.Secrets.AgeRecipients[0]
	}
	if recipientDefault == "" {
		recipientDefault = readAgePublicKey(home, state.Secrets.AgeIdentity)
	}

	if recipientDefault != "" {
		fmt.Println(StyleHint.Render(fmt.Sprintf("  Public key: %s", recipientDefault)))
		state.Secrets.AgeRecipients = []string{recipientDefault}
	} else if !yes {
		recipient, err := Input("Age recipient (public key, blank to skip)", "", false)
		if err != nil {
			return err
		}
		if recipient != "" {
			state.Secrets.AgeRecipients = []string{recipient}
		}
	}

	return nil
}

// ConfigureMacApps prompts for macOS cask selection and backup destination.
// Skipped on non-darwin. Mutates state.Modules.MacApps in place.
func ConfigureMacApps(state *config.UserState, home, profile string, yes bool) error {
	if runtime.GOOS != "darwin" {
		state.Modules.MacApps = config.UserMacAppsState{}
		return nil
	}
	if profile == "server" {
		state.Modules.MacApps = config.UserMacAppsState{}
		return nil
	}

	printSection("macOS Apps")

	cat, err := catalog.LoadMacApps()
	if err != nil {
		fmt.Println(StyleWarning.Render("  catalog load failed: " + err.Error()))
		return nil
	}

	enableDefault := state.Modules.MacApps.Enabled || len(state.Modules.MacApps.Casks) > 0
	if state.Name == "" {
		// Fresh install defaults to enabled on darwin non-server profiles.
		enableDefault = true
	}
	enable, err := ConfirmBool("Manage macOS cask apps?", enableDefault, yes)
	if err != nil {
		return err
	}
	if !enable {
		state.Modules.MacApps = config.UserMacAppsState{}
		return nil
	}
	state.Modules.MacApps.Enabled = true

	// MultiSelect presented in grouped sections. Build a single flattened list
	// so huh renders group separators as disabled-looking labels.
	tokens := cat.AllTokens()
	// Preselect: existing state → catalog recommended (curated set).
	preselect := state.Modules.MacApps.Casks
	if len(preselect) == 0 {
		preselect = cat.Recommended
	}
	// Filter preselect to tokens that actually exist in the catalog.
	seen := make(map[string]bool)
	for _, t := range tokens {
		seen[t] = true
	}
	var valid []string
	for _, t := range preselect {
		if seen[t] {
			valid = append(valid, t)
		}
	}

	if !yes {
		fmt.Println(StyleHint.Render(fmt.Sprintf("  Catalog: %d apps across %d groups", len(tokens), len(cat.Groups))))
	}
	selected, err := MultiSelect("Select apps to install", tokens, valid, yes)
	if err != nil {
		return err
	}
	state.Modules.MacApps.Casks = selected

	// Free-form additions
	extraDefault := strings.Join(state.Modules.MacApps.CasksExtra, " ")
	extraStr, err := Input("Additional casks (space-separated, optional)", extraDefault, yes)
	if err != nil {
		return err
	}
	state.Modules.MacApps.CasksExtra = splitCaskList(extraStr)

	// Backup list: default to the install list, but let the user trim it down.
	backupDefault := state.Modules.MacApps.BackupApps
	if len(backupDefault) == 0 {
		backupDefault = selected
	}
	sameAsInstall, err := ConfirmBool("Use the same list for settings backup?",
		len(state.Modules.MacApps.BackupApps) == 0 || sliceutil.Equal(state.Modules.MacApps.BackupApps, selected),
		yes)
	if err != nil {
		return err
	}
	if sameAsInstall {
		state.Modules.MacApps.BackupApps = nil // defer to install list at runtime
	} else {
		backupSel, err := MultiSelect("Select apps whose settings to back up", selected, backupDefault, yes)
		if err != nil {
			return err
		}
		state.Modules.MacApps.BackupApps = backupSel
	}

	// Backup root (shared by app-settings/ + profiles/)
	rootDefault := state.Modules.MacApps.BackupRoot
	detected := false
	if rootDefault == "" {
		if cloud := appsettings.DetectCloudCandidate(home); cloud != "" {
			rootDefault = cloud
			detected = true
		} else {
			rootDefault = appsettings.DefaultBackupRoot(home)
		}
	}
	choice, err := Select("Backup root",
		[]string{"cloud (auto-detected)", "local", "custom"},
		pickBackupChoice(rootDefault, detected),
		yes)
	if err != nil {
		return err
	}
	switch choice {
	case "cloud (auto-detected)":
		if detected {
			state.Modules.MacApps.BackupRoot = rootDefault
		} else {
			path, inputErr := Input("Cloud backup root", rootDefault, yes)
			if inputErr != nil {
				return inputErr
			}
			state.Modules.MacApps.BackupRoot = path
		}
	case "local":
		state.Modules.MacApps.BackupRoot = appsettings.DefaultBackupRoot(home)
	case "custom":
		path, inputErr := Input("Backup root path", rootDefault, yes)
		if inputErr != nil {
			return inputErr
		}
		state.Modules.MacApps.BackupRoot = path
	}

	return nil
}
