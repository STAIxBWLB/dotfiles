package ui

import (
	"fmt"
	"os"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
)

// ConfigureIdentity prompts for identity fields with system detection.
func ConfigureIdentity(state *config.UserState, yes bool) error {
	printSection("Identity")

	// Name: state → git config → system user
	nameDefault, nameDetected := state.Name, false
	if nameDefault == "" {
		if v := detectGitConfig("user.name"); v != "" {
			nameDefault, nameDetected = v, true
		} else if v := os.Getenv("USER"); v != "" {
			nameDefault, nameDetected = v, true
		}
	}
	var err error
	state.Name, err = InputWithDetected("Full name", nameDefault, nameDetected, yes)
	if err != nil {
		return err
	}

	// Email: state → git config
	emailDefault, emailDetected := state.Email, false
	if emailDefault == "" {
		if v := detectGitConfig("user.email"); v != "" {
			emailDefault, emailDetected = v, true
		}
	}
	state.Email, err = InputWithDetected("Email address", emailDefault, emailDetected, yes)
	if err != nil {
		return err
	}

	// GitHub user: state → gh CLI → guess from email
	ghDefault, ghDetected := state.GithubUser, false
	if ghDefault == "" {
		if v := detectGithubUser(); v != "" {
			ghDefault, ghDetected = v, true
		}
	}
	state.GithubUser, err = InputWithDetected("GitHub username", ghDefault, ghDetected, yes)
	if err != nil {
		return err
	}

	// Timezone: state → /etc/localtime → $TZ → Asia/Seoul
	tzDefault, tzDetected := state.Timezone, false
	if tzDefault == "" {
		if v := detectTimezone(); v != "" {
			tzDefault, tzDetected = v, true
		} else {
			tzDefault = "Asia/Seoul"
		}
	}
	state.Timezone, err = InputWithDetected("Timezone", tzDefault, tzDetected, yes)
	return err
}

// ConfigureProfile prompts for profile selection. Returns updated state.
func ConfigureProfile(state *config.UserState, suggested string, yes bool) error {
	profileDefault := state.Profile
	if profileDefault == "" {
		profileDefault = suggested
	}

	var err error
	state.Profile, err = Select("Profile", config.AvailableProfiles(), profileDefault, yes)
	return err
}

// ConfigureSSH prompts for SSH key name with auto-detection in home.
func ConfigureSSH(state *config.UserState, home string, yes bool) error {
	printSection("SSH")

	keys := detectSSHKeys(home)

	sshKeyDefault := state.SSH.KeyName
	if sshKeyDefault == "" {
		if state.GithubUser != "" {
			sshKeyDefault = "id_ed25519_" + state.GithubUser
		} else {
			sshKeyDefault = "id_ed25519"
		}
	}

	if len(keys) > 0 && !yes {
		fmt.Println(StyleHint.Render(fmt.Sprintf("  Found %d SSH key(s):", len(keys))))
		for _, k := range keys {
			fmt.Println(StyleHint.Render("    • " + k))
		}

		options := make([]string, len(keys))
		copy(options, keys)
		options = append(options, "(enter custom name)")

		selectDefault := sshKeyDefault
		if !sliceutil.Contains(options, selectDefault) {
			selectDefault = keys[0]
		}

		selected, err := Select("SSH key", options, selectDefault, false)
		if err != nil {
			return err
		}

		if selected == "(enter custom name)" {
			state.SSH.KeyName, err = Input("SSH key name", sshKeyDefault, false)
			return err
		}
		state.SSH.KeyName = selected
		return nil
	}

	var err error
	state.SSH.KeyName, err = Input("SSH key name", sshKeyDefault, yes)
	return err
}
