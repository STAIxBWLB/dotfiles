package ui

import (
	"fmt"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// PrintStateSummary displays the current configuration summary with styled output.
func PrintStateSummary(state *config.UserState) {
	fmt.Println()
	fmt.Println(StyleHeader.Render(" Configuration Summary "))
	fmt.Println()

	printKV("Profile", state.Profile)
	printKV("Name", state.Name)
	printKV("Email", state.Email)
	printKV("GitHub", state.GithubUser)
	printKV("Timezone", state.Timezone)
	printKV("SSH key", state.SSH.KeyName)
	printKV("Editor", state.Modules.Editor)
	printKV("AI", formatBool(state.Modules.AI.Enabled))
	if state.Modules.TerminalApps.Enabled || len(state.Modules.TerminalApps.Apps) > 0 {
		printKV("Terminal apps", strings.Join(state.Modules.TerminalApps.Apps, ", "))
	} else if state.Modules.Warp {
		printKV("Warp", formatBool(state.Modules.Warp))
	}
	if state.Modules.Workspace.Path != "" {
		printKV("Workspace", state.Modules.Workspace.Path)
		if state.Modules.Workspace.Gdrive != "" {
			printKV("Cloud", state.Modules.Workspace.Gdrive)
		}
		if state.Modules.Workspace.GdriveSymlink != "" {
			printKV("Cloud link", state.Modules.Workspace.GdriveSymlink+" → "+state.Modules.Workspace.Gdrive)
		}
		if state.Modules.Workspace.Symlink != "" {
			printKV("Symlink", state.Modules.Workspace.Path+" → "+state.Modules.Workspace.Symlink)
		}
		for _, repo := range state.Modules.Workspace.Repos {
			printKV(repo.Name+" repo", repo.Remote)
		}
	}
	if state.Modules.Fonts.Family != "" {
		printKV("Font family", state.Modules.Fonts.Family)
	}
	if state.Secrets.AgeIdentity != "" {
		printKV("Age identity", state.Secrets.AgeIdentity)
		if len(state.Secrets.AgeRecipients) > 0 {
			printKV("Age pubkey", state.Secrets.AgeRecipients[0])
		}
	}
	if state.Modules.MacApps.Enabled || len(state.Modules.MacApps.Casks) > 0 {
		printKV("Install list", fmt.Sprintf("%d selected + %d extra",
			len(state.Modules.MacApps.Casks), len(state.Modules.MacApps.CasksExtra)))
		if len(state.Modules.MacApps.BackupApps) > 0 {
			printKV("Backup list", fmt.Sprintf("%d apps", len(state.Modules.MacApps.BackupApps)))
		} else {
			printKV("Backup list", "(same as install list)")
		}
		if state.Modules.MacApps.BackupRoot != "" {
			printKV("Backup root", state.Modules.MacApps.BackupRoot)
		}
	}
	fmt.Println()
}
