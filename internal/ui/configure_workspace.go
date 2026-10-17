package ui

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// ConfigureWorkspace prompts for workspace settings. Skipped for server profile.
func ConfigureWorkspace(state *config.UserState, profile string, yes bool) error {
	if profile == "server" {
		state.Modules.Workspace.Path = ""
		state.Modules.Workspace.Vault = ""
		state.Modules.Workspace.Gdrive = ""
		state.Modules.Workspace.GdriveSymlink = ""
		state.Modules.Workspace.Symlink = ""
		state.Modules.Workspace.Repos = nil
		return nil
	}

	printSection("Workspace")

	enableWorkspace, err := ConfirmBool("Enable workspace module?", state.Modules.Workspace.Path != "", yes)
	if err != nil {
		return err
	}
	if !enableWorkspace {
		state.Modules.Workspace.Path = ""
		state.Modules.Workspace.Vault = ""
		state.Modules.Workspace.Gdrive = ""
		state.Modules.Workspace.GdriveSymlink = ""
		state.Modules.Workspace.Symlink = ""
		state.Modules.Workspace.Repos = nil
		return nil
	}

	// Workspace path: state → detected local → default
	wsDefault, wsDetected := state.Modules.Workspace.Path, false
	if wsDefault == "" {
		if v := detectWorkspacePath(); v != "" {
			wsDefault, wsDetected = v, true
		} else {
			wsDefault = "~/workspace"
		}
	}
	state.Modules.Workspace.Path, err = InputWithDetected("Workspace path", wsDefault, wsDetected, yes)
	if err != nil {
		return err
	}

	if err := configureVaultPath(state, yes); err != nil {
		return err
	}

	if runtime.GOOS == "darwin" {
		if err := configureCloudMirror(state, yes); err != nil {
			return err
		}
	}

	if err := configureWorkspaceSymlinkAndRepos(state, yes); err != nil {
		return err
	}
	return nil
}

// configureVaultPath selects the vault directory. Options come from the
// current state value, detected existing directories under the workspace,
// and the fresh default (<workspace>/work/vault), plus an "other" escape.
// Unattended runs take the default as-is: an existing state choice is
// preserved, a fresh machine picks the detected location.
func configureVaultPath(state *config.UserState, yes bool) error {
	const optOther = "other (enter path)"
	wsPath := state.Modules.Workspace.Path
	freshDefault := strings.TrimSuffix(wsPath, "/") + "/work/vault"

	candidates := detectVaultCandidates(wsPath)
	previous := state.Modules.Workspace.Vault
	opts := candidates
	if previous != "" && !slices.Contains(opts, previous) {
		opts = append([]string{previous}, opts...)
	}
	if !slices.Contains(opts, freshDefault) {
		opts = append(opts, freshDefault)
	}
	opts = append(opts, optOther)

	def := previous
	if def == "" && len(candidates) > 0 {
		def = candidates[0]
	}
	if def == "" {
		def = freshDefault
	}

	choice, err := Select("Vault location", opts, def, yes)
	if err != nil {
		return err
	}
	if choice == optOther {
		state.Modules.Workspace.Vault, err = Input("Vault path", previous, yes)
		if err != nil {
			return err
		}
		return nil
	}
	state.Modules.Workspace.Vault = choice
	return nil
}

// configureCloudMirror selects the cloud storage backing the dual-workspace
// mirror from detected mounts (Dropbox first, then Drive accounts), plus the
// current state value, an "other" escape, and skip.
func configureCloudMirror(state *config.UserState, yes bool) error {
	const optOther = "other (enter path)"
	const optSkip = "skip cloud mirror"
	home, _ := os.UserHomeDir()
	mounts := detectCloudMounts(home)
	previous := state.Modules.Workspace.Gdrive
	opts := mounts
	if previous != "" && !slices.Contains(opts, previous) {
		opts = append([]string{previous}, opts...)
	}
	opts = append(opts, optOther, optSkip)
	def := previous
	if def == "" {
		if len(mounts) > 0 {
			def = mounts[0]
		} else {
			def = optSkip
		}
	}
	choice, err := Select("Cloud storage for workspace mirror", opts, def, yes)
	if err != nil {
		return err
	}
	switch choice {
	case optSkip:
		state.Modules.Workspace.Gdrive = ""
	case optOther:
		state.Modules.Workspace.Gdrive, err = Input("Cloud storage path (blank to skip)", previous, yes)
		if err != nil {
			return err
		}
	default:
		state.Modules.Workspace.Gdrive = choice
	}

	// Cloud symlink: convenience symlink for the chosen cloud root
	if state.Modules.Workspace.Gdrive == "" {
		state.Modules.Workspace.GdriveSymlink = ""
		return nil
	}
	gsDefault := state.Modules.Workspace.GdriveSymlink
	if gsDefault == "" || state.Modules.Workspace.Gdrive != previous {
		gsDefault = defaultCloudSymlink(state.Modules.Workspace.Gdrive)
	}
	state.Modules.Workspace.GdriveSymlink, err = Input("Cloud symlink name (blank to skip)", gsDefault, yes)
	if err != nil {
		return err
	}
	if previous != "" && state.Modules.Workspace.Gdrive != previous {
		fmt.Println(StyleHint.Render(
			"  Cloud changed: symlinks pointing at the previous cloud (old link, <workspace>/work/.gdrive)\n" +
				"  are left in place; remove them manually so the next `dot apply` can repoint them."))
	}
	return nil
}

// configureWorkspaceSymlinkAndRepos handles the generic workspace-path symlink
// prompt and the optional workspace git repo configuration.
func configureWorkspaceSymlinkAndRepos(state *config.UserState, yes bool) error {
	var err error
	expandedPath := fileutil.ExpandHome(state.Modules.Workspace.Path)
	if !yes {
		keepCurrent := false
		currentTarget := readSymlinkTarget(expandedPath)
		if currentTarget != "" {
			fmt.Println(StyleHint.Render(fmt.Sprintf("  Current symlink: %s → %s", state.Modules.Workspace.Path, currentTarget)))

			keepCurrent, err = ConfirmBool("Keep existing symlink?", true, false)
			if err != nil {
				return err
			}
		}

		if keepCurrent {
			state.Modules.Workspace.Symlink = ""
		} else {
			symlinkDefault := state.Modules.Workspace.Symlink
			if symlinkDefault == "" && state.Modules.Workspace.Gdrive != "" && state.Modules.Workspace.GdriveSymlink == "" {
				// Only default to Gdrive when no separate GdriveSymlink is configured
				symlinkDefault = state.Modules.Workspace.Gdrive
			}
			fmt.Println(StyleHint.Render(fmt.Sprintf(
				"  Optional: make %s itself a symlink pointing elsewhere\n"+
					"  (e.g. into cloud-storage). Leave blank to keep %s as a real directory.",
				state.Modules.Workspace.Path, state.Modules.Workspace.Path)))
			prompt := fmt.Sprintf("Symlink %s → (blank to skip)", state.Modules.Workspace.Path)
			state.Modules.Workspace.Symlink, err = Input(prompt, symlinkDefault, false)
			if err != nil {
				return err
			}
		}
	}

	// --- Workspace git repos (optional) ---
	if !yes {
		configureRepos, err := ConfirmBool("Configure workspace git repos?", true, false)
		if err != nil {
			return err
		}
		if configureRepos {
			oldRepos := state.Modules.Workspace.Repos
			state.Modules.Workspace.Repos = nil
			topLevelVault := strings.TrimSuffix(state.Modules.Workspace.Path, "/") + "/vault"
			// The wizard configures the machine it runs on, so vault
			// detection is meant to stat the invoking user's tree.
			uiHome, _ := os.UserHomeDir()
			for _, name := range []string{"work", "vault"} {
				if name == "vault" && config.ResolveVaultPath(state.Modules.Workspace.Vault, state.Modules.Workspace.Path, uiHome) != topLevelVault {
					fmt.Println(StyleHint.Render(fmt.Sprintf(
						"  Vault location is %s — skipping the separate vault repo\n"+
							"  (the vault is expected to arrive with work, e.g. as a submodule).",
						state.Modules.Workspace.Vault)))
					continue
				}
				existingRemote := ""
				for _, r := range oldRepos {
					if r.Name == name {
						existingRemote = r.Remote
						break
					}
				}
				remote, err := Input(
					fmt.Sprintf("Git remote for %s/%s (blank to skip)", state.Modules.Workspace.Path, name),
					existingRemote, false)
				if err != nil {
					return err
				}
				if remote != "" {
					state.Modules.Workspace.Repos = append(state.Modules.Workspace.Repos, config.RepoConfig{
						Name:   name,
						Remote: remote,
					})
				}
			}
		}
	}
	return nil
}
