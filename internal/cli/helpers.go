package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func absPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Clean(filepath.Join(cwd, path))
	}
	return filepath.Clean(path)
}

func defaultWorkspaceWorkRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "workspace", "work")
}

func formatInterval(seconds int) string {
	if seconds <= 0 {
		return "(off)"
	}
	if seconds%3600 == 0 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}

// containsString reports whether needle appears in haystack.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
