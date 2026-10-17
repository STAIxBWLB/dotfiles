package config

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	execrun "github.com/STAIxBWLB/dotfiles/internal/exec"
)

// CheckStatus represents the result status of a single preflight check.
type CheckStatus int

const (
	CheckPass CheckStatus = iota
	CheckWarn
	CheckFail
)

// String returns a display label for the status.
func (s CheckStatus) String() string {
	switch s {
	case CheckPass:
		return "OK"
	case CheckWarn:
		return "WARN"
	case CheckFail:
		return "FAIL"
	default:
		return "?"
	}
}

// PreflightCheck holds the result of a single environment check.
type PreflightCheck struct {
	Name    string
	Status  CheckStatus
	Value   string
	Message string
}

// PreflightReport holds all check results.
type PreflightReport struct {
	Checks []PreflightCheck
	System *SystemInfo
}

// Counts returns pass, warn, fail counts.
func (r *PreflightReport) Counts() (pass, warn, fail int) {
	for _, c := range r.Checks {
		switch c.Status {
		case CheckPass:
			pass++
		case CheckWarn:
			warn++
		case CheckFail:
			fail++
		}
	}
	return
}

// RunPreflightChecks runs all environment checks and returns a report.
func RunPreflightChecks(sys *SystemInfo, homeDir string) *PreflightReport {
	report := &PreflightReport{System: sys}

	// OS & Architecture
	report.Checks = append(report.Checks, PreflightCheck{
		Name:   "OS",
		Status: CheckPass,
		Value:  sys.OS + " (" + sys.Arch + ")",
	})

	// Hostname
	report.Checks = append(report.Checks, PreflightCheck{
		Name:   "Hostname",
		Status: CheckPass,
		Value:  sys.Hostname,
	})

	// Shell
	report.Checks = append(report.Checks, checkShell(sys))

	// Git
	report.Checks = append(report.Checks, checkGit(sys))

	// Homebrew
	report.Checks = append(report.Checks, checkBrew(sys))

	// GPU / CUDA / DGX (informational)
	report.Checks = append(report.Checks, checkGPU(sys)...)

	// Directories
	report.Checks = append(report.Checks, checkDirectories(homeDir)...)

	// Network
	report.Checks = append(report.Checks, checkNetworkGitHub())

	return report
}

func checkShell(sys *SystemInfo) PreflightCheck {
	if sys.Shell == "unknown" || sys.Shell == "" {
		return PreflightCheck{
			Name:    "Shell",
			Status:  CheckFail,
			Value:   "not detected",
			Message: "$SHELL is not set",
		}
	}
	status := CheckPass
	msg := ""
	if sys.Shell != "zsh" {
		status = CheckWarn
		msg = "zsh is recommended"
	}
	return PreflightCheck{
		Name:    "Shell",
		Status:  status,
		Value:   sys.Shell,
		Message: msg,
	}
}

func checkGit(sys *SystemInfo) PreflightCheck {
	if !sys.HasGit {
		return PreflightCheck{
			Name:    "Git",
			Status:  CheckFail,
			Value:   "not found",
			Message: "git is required",
		}
	}
	return PreflightCheck{
		Name:   "Git",
		Status: CheckPass,
		Value:  sys.GitVersion,
	}
}

func checkBrew(sys *SystemInfo) PreflightCheck {
	if !sys.HasBrew {
		return PreflightCheck{
			Name:    "Homebrew",
			Status:  CheckWarn,
			Value:   "not found",
			Message: "package installation requires Homebrew",
		}
	}
	return PreflightCheck{
		Name:   "Homebrew",
		Status: CheckPass,
		Value:  sys.BrewPath,
	}
}

func checkGPU(sys *SystemInfo) []PreflightCheck {
	var checks []PreflightCheck

	if sys.HasNVIDIAGPU {
		checks = append(checks, PreflightCheck{
			Name:   "NVIDIA GPU",
			Status: CheckPass,
			Value:  sys.GPUModel,
		})
	}

	if sys.HasCUDA {
		checks = append(checks, PreflightCheck{
			Name:   "CUDA",
			Status: CheckPass,
			Value:  sys.CUDAHome,
		})
	}

	if sys.IsDGX {
		checks = append(checks, PreflightCheck{
			Name:   "DGX",
			Status: CheckPass,
			Value:  "detected",
		})
	}

	return checks
}

func checkDirectories(homeDir string) []PreflightCheck {
	dirs := []struct {
		name string
		path string
	}{
		{"Directory ~/.config", filepath.Join(homeDir, ".config")},
		{"Directory ~/.local/bin", filepath.Join(homeDir, ".local", "bin")},
	}

	var checks []PreflightCheck
	for _, d := range dirs {
		if fi, err := os.Stat(d.path); err == nil && fi.IsDir() {
			checks = append(checks, PreflightCheck{
				Name:   d.name,
				Status: CheckPass,
				Value:  "exists",
			})
		} else {
			checks = append(checks, PreflightCheck{
				Name:    d.name,
				Status:  CheckWarn,
				Value:   "missing",
				Message: "will be created during apply",
			})
		}
	}
	return checks
}

func checkNetworkGitHub() PreflightCheck {
	conn, err := net.DialTimeout("tcp", "github.com:443", 5*time.Second)
	if err != nil {
		return PreflightCheck{
			Name:    "GitHub connectivity",
			Status:  CheckWarn,
			Value:   "unreachable",
			Message: "some features require network access",
		}
	}
	conn.Close()
	return PreflightCheck{
		Name:   "GitHub connectivity",
		Status: CheckPass,
		Value:  "reachable",
	}
}

// GeneratePreflightConfig builds a UserState from detected system info.
func GeneratePreflightConfig(sys *SystemInfo) *UserState {
	state := &UserState{}

	// Profile
	state.Profile = sys.SuggestProfile()

	// Name: git config → $USER → ""
	state.Name = gitConfigValue("user.name")
	if state.Name == "" {
		state.Name = os.Getenv("USER")
	}

	// Email: git config → ""
	state.Email = gitConfigValue("user.email")

	// GitHub user: $GITHUB_USER → ""
	state.GithubUser = os.Getenv("GITHUB_USER")

	// Timezone: $TZ → default
	state.Timezone = os.Getenv("TZ")
	if state.Timezone == "" {
		state.Timezone = "Asia/Seoul"
	}

	// SSH key name
	if state.GithubUser != "" {
		state.SSH.KeyName = "id_ed25519_" + state.GithubUser
	} else {
		state.SSH.KeyName = "id_ed25519"
	}

	// AI CLI/config helpers enabled by default
	state.Modules.AI.Enabled = true

	// Server profile: no workspace/fonts/warp
	if state.Profile != "server" {
		state.Modules.Fonts.Family = "FiraCode"
	}

	return state
}

func gitConfigValue(key string) string {
	res, err := execrun.NewProbeRunner().RunQuery(context.Background(), "git", "config", "--global", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}
