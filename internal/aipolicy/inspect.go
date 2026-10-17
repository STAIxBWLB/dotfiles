package aipolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

var nativeVersion = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+\b`)

// Inspect executes only version/help probes. Explicit homes deliberately ignore
// ambient profile overrides so examining a fixture cannot inspect another account.
func Inspect(ctx context.Context, home string, explicit bool) ([]Runtime, error) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	actualHome, _ := os.UserHomeDir()
	explicit = explicit || filepath.Clean(home) != filepath.Clean(actualHome)
	out := []Runtime{}
	for _, agent := range []string{"claude", "codex", "grok", "kimi", "opencode", "qwen", "kiro", "pi"} {
		r := Runtime{HomeMode: "native-default", Agent: agent, Home: filepath.Join(home, "."+agent), Capabilities: []string{}, Constraints: []string{}}
		if explicit {
			r.HomeMode = "pinned"
		}
		if agent == "kimi" {
			r.Home = filepath.Join(home, ".kimi-code")
		}
		if agent == "opencode" {
			r.Home = filepath.Join(config.ConfigHome(home, explicit), "opencode")
		}
		if !explicit {
			key := ""
			switch agent {
			case "codex":
				key = "CODEX_HOME"
			case "claude":
				key = "CLAUDE_CONFIG_DIR"
			case "kimi":
				key = "KIMI_CODE_HOME"
			case "opencode":
				key = "OPENCODE_CONFIG_DIR"
			}
			if key != "" && os.Getenv(key) != "" {
				r.Home = os.Getenv(key)
				r.HomeMode = "pinned"
			}
		}
		if !filepath.IsAbs(r.Home) {
			return nil, fmt.Errorf("%s runtime home must be absolute", agent)
		}
		inspectPreferences(&r)
		executable := agent
		if agent == "kiro" {
			executable = "kiro-cli"
		}
		binary, err := exec.LookPath(executable)
		if err != nil {
			r.Constraints = append(r.Constraints, "executable not installed")
			out = append(out, r)
			continue
		}
		r.Executable = binary
		probe := func(args ...string) (string, error) {
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			cmd := exec.CommandContext(cctx, binary, args...)
			cmd.Dir = home
			cmd.Env = probeEnvironment(home, explicit, r)
			output := &boundedProbeOutput{}
			cmd.Stdout = output
			e := cmd.Run()
			return output.String(), e
		}
		version, err := probe("--version")
		if err != nil {
			r.Constraints = append(r.Constraints, "version probe failed")
			out = append(out, r)
			continue
		}
		r.Version = nativeVersion.FindString(version)
		r.Available = r.Version != ""
		help, err := probe("--help")
		if err != nil {
			r.Constraints = append(r.Constraints, "help probe failed")
		} else {
			if agent == "codex" && r.Version == "0.157.1" && strings.Contains(help, "--approve-for-me") {
				r.AutoReview = true
			}
			if agent == "claude" && r.Version == "2.1.283" && strings.Contains(help, "--permission-mode") && strings.Contains(help, "auto") && strings.Contains(help, "--effort") {
				r.AutoReview = true
			}
		}
		if r.AutoReview {
			if agent == "claude" {
				if status, e := probe("auth", "status", "--json"); e == nil {
					var auth struct {
						LoggedIn         bool   `json:"loggedIn"`
						AuthMethod       string `json:"authMethod"`
						APIProvider      string `json:"apiProvider"`
						SubscriptionType string `json:"subscriptionType"`
					}
					if json.Unmarshal([]byte(status), &auth) == nil {
						r.SubscriptionVerified = auth.LoggedIn && auth.AuthMethod == "claude.ai" && auth.APIProvider == "firstParty" && (auth.SubscriptionType == "max" || auth.SubscriptionType == "pro" || auth.SubscriptionType == "team" || auth.SubscriptionType == "enterprise")
					}
				}
			}
			if agent == "codex" {
				r.SubscriptionVerified = codexSubscription(r.Home)
			}
			if !r.SubscriptionVerified {
				r.Constraints = append(r.Constraints, "subscription authentication not verified")
			}
			r.Capabilities = []string{"text", "code", "shell", "auto-review"}
			r.Constraints = append(r.Constraints, "account entitlement and selected model require explicit target validation")
		} else {
			r.Constraints = append(r.Constraints, "automatic review not verified for installed version")
		}
		out = append(out, r)
	}
	return out, ctx.Err()
}

// Only an allowlist of scalar preferences is exposed; no auth/config dump.
func inspectPreferences(r *Runtime) { inspectPreferencesFile(r, "") }

func inspectPreferencesFile(r *Runtime, filename string) {
	r.Preferences = map[string]string{}
	keys := []string{}
	switch r.Agent {
	case "claude":
		keys = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}
	case "codex":
		keys = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY"}
	}
	for _, key := range keys {
		if os.Getenv(key) != "" {
			r.SubscriptionConflict = true
		}
	}
	name := ""
	switch r.Agent {
	case "claude":
		name = "settings.json"
	case "codex":
		name = "config.toml"
	default:
		return
	}
	if filename != "" {
		name = filename
	}
	f, err := os.Open(filepath.Join(r.Home, name))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		r.Constraints = append(r.Constraints, "native preferences cannot be read")
		r.SubscriptionConflict = true
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		r.Constraints = append(r.Constraints, "native preferences exceed inspection limit or cannot be read")
		r.SubscriptionConflict = true
		return
	}
	if r.Agent == "claude" {
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil {
			r.Constraints = append(r.Constraints, "native preferences malformed")
			r.SubscriptionConflict = true
			return
		}
		if mode, ok := doc["permissions"].(map[string]any); ok {
			if v, ok := mode["defaultMode"].(string); ok && allowedPreference(v) {
				r.Preferences["permission_mode"] = v
			}
		}
		if v, ok := doc["effortLevel"].(string); ok && allowedPreference(v) {
			r.Preferences["effort"] = v
		}
		if env, ok := doc["env"].(map[string]any); ok {
			for _, key := range keys {
				if _, exists := env[key]; exists {
					r.SubscriptionConflict = true
				}
			}
		}
		if _, exists := doc["apiKeyHelper"]; exists {
			r.SubscriptionConflict = true
		}
	} else {
		section := ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = line
				if strings.HasPrefix(line, "[profiles.") {
					r.Constraints = append(r.Constraints, "legacy inline Codex profiles detected; migration to separate profile files remains pending; native base is unmanaged")
				}
				continue
			}
			if section != "" {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.Trim(strings.TrimSpace(key), "\"'")
			value = strings.Trim(strings.TrimSpace(strings.SplitN(value, "#", 2)[0]), "\"'")
			switch key {
			case "approval_policy", "approvals_reviewer", "sandbox_mode", "model_reasoning_effort":
				if allowedPreference(value) {
					r.Preferences[key] = value
				}
			case "openai_base_url":
				if value != "" {
					r.SubscriptionConflict = true
				}
			case "model_provider":
				if value != "openai" {
					r.SubscriptionConflict = true
				}
			case "forced_login_method":
				if value == "api" {
					r.SubscriptionConflict = true
				}
			}
		}
	}
}
func allowedPreference(value string) bool {
	switch value {
	case "auto", "default", "acceptEdits", "bypassPermissions", "plan", "dontAsk", "never", "on-request", "on-failure", "untrusted", "auto_review", "user", "workspace-write", "read-only", "danger-full-access", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

// Native CLIs may inherit project settings from ancestors. Check every possible
// project root, without reading credentials or traversing directories recursively.
func projectSubscriptionConflict(agent, cwd string) (bool, error) {
	files, err := projectSettingsFiles(agent, cwd)
	if err != nil {
		return false, err
	}
	return projectFilesSubscriptionConflict(agent, files), nil
}
func projectFilesSubscriptionConflict(agent string, files []string) bool {
	for _, file := range files {
		r := Runtime{Agent: agent, Home: filepath.Dir(file)}
		inspectPreferencesFile(&r, filepath.Base(file))
		if r.SubscriptionConflict {
			return true
		}
	}
	return false
}

func codexSubscription(home string) bool {
	f, err := os.Open(filepath.Join(home, "auth.json"))
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return false
	}
	var auth struct {
		Mode string `json:"auth_mode"`
	}
	return json.Unmarshal(data, &auth) == nil && auth.Mode == "chatgpt"
}

type boundedProbeOutput struct{ buffer bytes.Buffer }

func (b *boundedProbeOutput) String() string { return b.buffer.String() }
func (b *boundedProbeOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 65536 {
		return 0, fmt.Errorf("runtime probe exceeds output limit")
	}
	return b.buffer.Write(p)
}
func probeEnvironment(home string, explicit bool, r Runtime) []string {
	// Setting a previously absent CLAUDE_CONFIG_DIR changes its keychain namespace.
	// Native probes must preserve environment presence, not just equivalent paths.
	if !explicit {
		return os.Environ()
	}
	roots := map[string]bool{"HOME": true, "CODEX_HOME": true, "CLAUDE_CONFIG_DIR": true, "KIMI_CODE_HOME": true, "OPENCODE_CONFIG_DIR": true, "XDG_CONFIG_HOME": true}
	if explicit {
		roots["OPENCODE_CONFIG"] = true
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !roots[key] {
			env = append(env, entry)
		}
	}
	codexHome := filepath.Join(home, ".codex")
	claudeHome := filepath.Join(home, ".claude")
	kimiHome := filepath.Join(home, ".kimi-code")
	opencodeHome := filepath.Join(config.ConfigHome(home, explicit), "opencode")
	switch r.Agent {
	case "codex":
		codexHome = r.Home
	case "claude":
		claudeHome = r.Home
	case "kimi":
		kimiHome = r.Home
	case "opencode":
		opencodeHome = r.Home
	}
	return append(env, "HOME="+home, "CODEX_HOME="+codexHome, "CLAUDE_CONFIG_DIR="+claudeHome, "KIMI_CODE_HOME="+kimiHome, "OPENCODE_CONFIG_DIR="+opencodeHome, "XDG_CONFIG_HOME="+config.ConfigHome(home, explicit))
}

// KnowledgeConflicts preserves native ask/deny rules and surfaces precedence
// conflicts instead of describing shadowed grants as no-confirm permissions.
func KnowledgeConflicts(r Runtime, cwd string, grants []KnowledgeApproval) ([]string, error) {
	projectFiles, err := projectSettingsFiles(r.Agent, cwd)
	if err != nil {
		return nil, err
	}
	return knowledgeConflictsForProjectFiles(r, projectFiles, grants)
}
func knowledgeConflictsForProjectFiles(r Runtime, projectFiles []string, grants []KnowledgeApproval) ([]string, error) {
	conflicts := []string{}
	if r.Agent != "claude" || len(grants) == 0 {
		return conflicts, nil
	}
	if !filepath.IsAbs(r.Home) {
		return nil, fmt.Errorf("knowledge permission inspection requires absolute native home")
	}
	files := []string{filepath.Join(r.Home, "settings.json"), filepath.Join(r.Home, "settings.local.json")}
	switch goruntime.GOOS {
	case "darwin":
		files = append(files, "/Library/Application Support/ClaudeCode/managed-settings.json")
	case "linux":
		files = append(files, "/etc/claude-code/managed-settings.json")
	}
	return knowledgeConflictsFromFiles(r, append(files, projectFiles...), grants)
}
func knowledgeConflictsFromFiles(r Runtime, files []string, grants []KnowledgeApproval) ([]string, error) {
	conflicts := []string{}

	seen := map[string]bool{}
	for _, file := range files {
		if seen[file] {
			continue
		}
		seen[file] = true
		f, err := os.Open(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot inspect native knowledge permission rules")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		_ = f.Close()
		if readErr != nil || len(data) > 1<<20 {
			return nil, fmt.Errorf("native knowledge permission rules exceed inspection limit")
		}
		var doc struct {
			Permissions struct {
				Ask  []string `json:"ask"`
				Deny []string `json:"deny"`
			} `json:"permissions"`
		}
		if json.Unmarshal(data, &doc) != nil {
			return nil, fmt.Errorf("native knowledge permission rules malformed")
		}
		for _, entry := range []struct {
			mode  string
			rules []string
		}{{"ask", doc.Permissions.Ask}, {"deny", doc.Permissions.Deny}} {
			mode, rules := entry.mode, entry.rules
			for _, rule := range rules {
				pattern, _, _ := strings.Cut(rule, "(")
				for _, grant := range grants {
					tool := "mcp__" + grant.Server + "__" + grant.Tool
					matched, err := filepath.Match(pattern, tool)
					if err != nil {
						return nil, fmt.Errorf("native knowledge permission pattern malformed")
					}
					if matched || pattern == "mcp__"+grant.Server {
						conflicts = append(conflicts, "native "+mode+" rule overrides approved knowledge tool "+tool+"; retain rule and reconcile scoped permission explicitly")
					}
				}
			}
		}
	}
	return conflicts, nil
}
