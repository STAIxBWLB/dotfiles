package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ProjectConfig holds per-project workspace settings.
type ProjectConfig struct {
	Name   string `yaml:"name"`
	Path   string `yaml:"path"`
	Layout string `yaml:"layout,omitempty"`
	Theme  string `yaml:"theme,omitempty"`
}

// Config holds global workspace settings and registered projects.
type Config struct {
	DefaultLayout string          `yaml:"default_layout"`
	DefaultTheme  string          `yaml:"default_theme"`
	Projects      []ProjectConfig `yaml:"projects"`
}

// validSessionName matches tmux-safe session names: alphanumeric, underscore, hyphen.
var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ConfigDir returns the workspace config directory (~/.config/dot).
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "dot"), nil
}

// ConfigPath returns the path to workspace.yaml.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "workspace.yaml"), nil
}

// DataDir returns the workspace data directory (~/.local/share/dot/workspace).
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "dot", "workspace"), nil
}

// LoadConfig loads workspace config from disk, returning defaults if not found.
func LoadConfig() (*Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return loadConfigAt(p)
}

// LoadConfigForHome is like LoadConfig but resolves the registry against an
// explicit home directory instead of os.UserHomeDir(). Commands that honor
// --home must use it so they list the target user's projects rather than the
// invoking user's. An empty home falls back to the current user's.
func LoadConfigForHome(home string) (*Config, error) {
	if home == "" {
		return LoadConfig()
	}
	return loadConfigAt(filepath.Join(home, ".config", "dot", "workspace.yaml"))
}

func loadConfigAt(p string) (*Config, error) {
	cfg := &Config{
		DefaultLayout: "dev",
		DefaultTheme:  "default",
	}

	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}

	if cfg.DefaultLayout == "" {
		cfg.DefaultLayout = "dev"
	}
	if cfg.DefaultTheme == "" {
		cfg.DefaultTheme = "default"
	}

	return cfg, nil
}

// Save writes workspace config to disk.
func (c *Config) Save() error {
	p, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(p, data, 0644)
}

// FindProject looks up a project by name.
func (c *Config) FindProject(name string) *ProjectConfig {
	for i := range c.Projects {
		if c.Projects[i].Name == name {
			return &c.Projects[i]
		}
	}
	return nil
}

// AddProject registers a new project. Returns error if name already exists or is invalid.
func (c *Config) AddProject(name, path, layout, theme string) error {
	if err := ValidateSessionName(name); err != nil {
		return err
	}

	if c.FindProject(name) != nil {
		return fmt.Errorf("project %q already registered", name)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	// Validate path exists
	fi, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("path %q does not exist: %w", absPath, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("path %q is not a directory", absPath)
	}

	proj := ProjectConfig{
		Name: name,
		Path: absPath,
	}
	if layout != "" && layout != c.DefaultLayout {
		proj.Layout = layout
	}
	if theme != "" && theme != c.DefaultTheme {
		proj.Theme = theme
	}

	c.Projects = append(c.Projects, proj)
	return nil
}

// RemoveProject removes a project by name. Returns false if not found.
func (c *Config) RemoveProject(name string) bool {
	for i := range c.Projects {
		if c.Projects[i].Name == name {
			c.Projects = append(c.Projects[:i], c.Projects[i+1:]...)
			return true
		}
	}
	return false
}

// EffectiveLayout returns the project's layout or the global default.
func (c *Config) EffectiveLayout(proj *ProjectConfig) string {
	if proj.Layout != "" {
		return proj.Layout
	}
	return c.DefaultLayout
}

// EffectiveTheme returns the project's theme or the global default.
func (c *Config) EffectiveTheme(proj *ProjectConfig) string {
	if proj.Theme != "" {
		return proj.Theme
	}
	return c.DefaultTheme
}

// ValidateSessionName checks if a name is valid for tmux sessions.
func ValidateSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	if !validSessionName.MatchString(name) {
		return fmt.Errorf("project name %q contains invalid characters (use alphanumeric, underscore, or hyphen only)", name)
	}
	return nil
}

// ValidLayouts returns the list of supported layout names.
func ValidLayouts() []string {
	return []string{"dev", "claude", "monitor", "ahub", "ahub-compact"}
}

// ValidThemes returns the list of supported theme names.
func ValidThemes() []string {
	return []string{"default", "dracula", "nord", "catppuccin", "tokyo-night"}
}

// IsValidLayout checks if a layout name is supported.
func IsValidLayout(name string) bool {
	for _, l := range ValidLayouts() {
		if l == name {
			return true
		}
	}
	return false
}

// IsValidTheme checks if a theme name is supported.
func IsValidTheme(name string) bool {
	for _, t := range ValidThemes() {
		if t == name {
			return true
		}
	}
	return false
}
