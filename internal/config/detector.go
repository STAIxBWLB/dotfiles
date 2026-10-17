package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	execrun "github.com/STAIxBWLB/dotfiles/internal/exec"
)

// SystemInfo holds detected system information.
type SystemInfo struct {
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	Hostname   string   `json:"hostname"`
	DistroID   string   `json:"distro_id,omitempty"`
	DistroLike []string `json:"distro_like,omitempty"`
	HasBrew    bool     `json:"has_brew"`
	BrewPath   string   `json:"brew_path"`
	// GPU/CUDA detection
	HasNVIDIAGPU bool   `json:"has_nvidia_gpu"`
	GPUModel     string `json:"gpu_model"`
	HasCUDA      bool   `json:"has_cuda"`
	CUDAHome     string `json:"cuda_home"`
	IsDGX        bool   `json:"is_dgx"`
	// Shell/Git detection
	Shell      string `json:"shell"`
	HasGit     bool   `json:"has_git"`
	GitVersion string `json:"git_version"`
}

// DetectSystem probes the current system and returns SystemInfo.
func DetectSystem() (*SystemInfo, error) {
	info := &SystemInfo{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}

	detectHostname(info)
	detectDistribution(info)
	detectShell(info)
	detectGit(info)
	detectBrew(info)
	detectGPU(info)
	detectCUDA(info)
	detectDGX(info)

	return info, nil
}

// IsArchLinux reports whether the detected Linux distribution is Arch or an
// Arch-derived distribution.
func (s *SystemInfo) IsArchLinux() bool {
	if s == nil || s.OS != "linux" {
		return false
	}
	if s.DistroID == "arch" {
		return true
	}
	for _, id := range s.DistroLike {
		if id == "arch" {
			return true
		}
	}
	return false
}

// SuggestProfile returns a profile name based on detected system.
func (s *SystemInfo) SuggestProfile() string {
	if s.OS == "linux" && (s.HasNVIDIAGPU || s.HasCUDA || s.IsDGX) {
		return "server"
	}
	return "full"
}

func detectHostname(info *SystemInfo) {
	res, err := execrun.NewProbeRunner().RunQuery(context.Background(), "hostname")
	if err != nil {
		info.Hostname = "unknown"
		return
	}
	info.Hostname = strings.TrimSpace(res.Stdout)
}

func detectDistribution(info *SystemInfo) {
	if info.OS != "linux" {
		return
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return
	}
	values := parseOSRelease(string(data))
	info.DistroID = strings.ToLower(values["ID"])
	info.DistroLike = append(info.DistroLike, strings.Fields(strings.ToLower(values["ID_LIKE"]))...)
}

func parseOSRelease(data string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values
}

func detectBrew(info *SystemInfo) {
	path, err := exec.LookPath("brew")
	if err != nil {
		return
	}
	info.HasBrew = true
	info.BrewPath = path
}

func detectGPU(info *SystemInfo) {
	res, err := execrun.NewProbeRunner().RunQuery(context.Background(), "nvidia-smi", "--query-gpu=name", "--format=csv,noheader,nounits")
	if err != nil {
		return
	}
	info.HasNVIDIAGPU = true
	lines := strings.SplitN(strings.TrimSpace(res.Stdout), "\n", 2)
	if len(lines) > 0 {
		info.GPUModel = strings.TrimSpace(lines[0])
	}
}

func detectCUDA(info *SystemInfo) {
	for _, p := range []string{"/usr/local/cuda", "/usr/cuda"} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			info.HasCUDA = true
			info.CUDAHome = p
			return
		}
	}
}

func detectDGX(info *SystemInfo) {
	if _, err := os.Stat("/etc/dgx-release"); err == nil {
		info.IsDGX = true
	}
}

func detectShell(info *SystemInfo) {
	shell := os.Getenv("SHELL")
	if shell != "" {
		info.Shell = filepath.Base(shell)
		return
	}
	info.Shell = "unknown"
}

func detectGit(info *SystemInfo) {
	res, err := execrun.NewProbeRunner().RunQuery(context.Background(), "git", "--version")
	if err != nil {
		return
	}
	info.HasGit = true
	// parse "git version 2.43.0" -> "2.43.0"
	parts := strings.Fields(strings.TrimSpace(res.Stdout))
	if len(parts) >= 3 {
		info.GitVersion = parts[2]
	}
}
