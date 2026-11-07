package workspace

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed scripts
var embeddedScripts embed.FS

// scriptFiles lists the workspace shell scripts to deploy.
var scriptFiles = []string{
	"launcher.sh",
	"tools.sh",
	"layouts.sh",
	"themes.sh",
	"ahub-pane.sh",
}

// Deploy copies workspace shell scripts to ~/.local/share/dot/workspace/.
// Only writes files that have changed (SHA256 comparison).
func Deploy() (bool, error) {
	dataDir, err := DataDir()
	if err != nil {
		return false, err
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return false, fmt.Errorf("creating data dir: %w", err)
	}

	anyChanged := false
	for _, name := range scriptFiles {
		content, err := embeddedScripts.ReadFile("scripts/" + name)
		if err != nil {
			return false, fmt.Errorf("reading embedded %s: %w", name, err)
		}

		dest := filepath.Join(dataDir, name)
		changed, err := writeIfChanged(dest, content)
		if err != nil {
			return false, fmt.Errorf("deploying %s: %w", name, err)
		}
		if changed {
			anyChanged = true
		}
	}

	return anyChanged, nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func writeIfChanged(path string, content []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && sha256Hex(existing) == sha256Hex(content) {
		return false, nil
	}

	if err := os.WriteFile(path, content, 0755); err != nil {
		return false, err
	}
	return true, nil
}

// LauncherPath returns the path to the deployed launcher script.
func LauncherPath() (string, error) {
	dataDir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "launcher.sh"), nil
}
