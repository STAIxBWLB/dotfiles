package aitooling

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

type command struct {
	Path    string
	Args    []string
	Env     []string
	Dir     string
	Input   []byte
	Timeout time.Duration
}
type executor func(context.Context, command) (string, error)

// Keep command output private: installers can print tokens or account details.
// Errors expose the executable and exit state only, never stdout/stderr.
func execute(ctx context.Context, c command) (string, error) {
	limit := c.Timeout
	if limit == 0 {
		limit = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin = bytes.NewReader(c.Input)
	cmd.WaitDelay = 2 * time.Second
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := resourceguard.RunCommand(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", filepath.Base(c.Path), err)
	}

	return out.String(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return append(out, prefix+value)
}

func (e *Engine) environment() []string {
	env := os.Environ()
	if e.opts.ExplicitHome {
		// Do not let an inherited account profile redirect an explicit-home run.
		for _, k := range []string{"CODEX_HOME", "KIMI_CODE_HOME", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "AGENTS_HOME", "CLAUDE_MEM_DATA_DIR", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GROK_DEPLOYMENT_KEY", "XAI_API_KEY", "GITHUB_TOKEN", "GH_TOKEN", "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "NPM_CONFIG_PREFIX", "npm_config_prefix", "UV_TOOL_DIR", "UV_TOOL_BIN_DIR"} {
			env = setEnv(env, k, "")
		}
	}
	env = setEnv(env, "HOME", e.opts.HomeDir)
	env = setEnv(env, "CODEX_HOME", e.profile("codex"))
	env = setEnv(env, "KIMI_CODE_HOME", e.profile("kimi"))
	env = setEnv(env, "OPENCODE_CONFIG_DIR", e.profile("opencode"))
	env = setEnv(env, "CLAUDE_CONFIG_DIR", e.profile("claude"))
	env = setEnv(env, "XDG_CONFIG_HOME", config.ConfigHome(e.opts.HomeDir, e.opts.ExplicitHome))
	env = setEnv(env, "CARGO_BUILD_JOBS", "2")
	env = setEnv(env, "GOMAXPROCS", "2")
	env = setEnv(env, "CI", "1")
	env = setEnv(env, "CLAUDE_MEM_ONLINE_OPTIN", "false")
	env = setEnv(env, "PATH", e.pathEnv())
	return env
}
func (e *Engine) profile(id string) string {
	defaults := map[string]string{"claude": ".claude", "codex": ".codex", "kimi": ".kimi-code", "qwen": ".qwen", "grok": ".grok", "opencode": filepath.Join(".config", "opencode")}
	keys := map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME", "kimi": "KIMI_CODE_HOME", "opencode": "OPENCODE_CONFIG_DIR"}
	if !e.opts.ExplicitHome {
		if v := os.Getenv(keys[id]); filepath.IsAbs(v) {
			return v
		}
	}
	if id == "opencode" {
		return filepath.Join(config.ConfigHome(e.opts.HomeDir, e.opts.ExplicitHome), "opencode")
	}

	return filepath.Join(e.opts.HomeDir, defaults[id])
}
func (e *Engine) pathEnv() string {
	h := e.opts.HomeDir
	var dirs []string
	// fnm multishell shims disappear when the session ends. Resolve their target
	// before persisting or passing PATH to noninteractive child processes.
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if e.opts.ExplicitHome {
			actual, _ := os.UserHomeDir()
			if actual != h && strings.HasPrefix(dir, actual+string(os.PathSeparator)) {
				continue
			}
		}
		if strings.Contains(dir, "fnm_multishells") {
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				dir = resolved
			} else {
				continue
			}
		}
		dirs = append(dirs, dir)
	}
	// Existing PATH is authoritative for provider adoption. Managed default
	// install locations are fallbacks, not a way to shadow the user's CLI.
	dirs = append(dirs, filepath.Join(h, ".local", "bin"), filepath.Join(h, ".kimi-code", "bin"), filepath.Join(h, ".grok", "bin"), filepath.Join(h, ".opencode", "bin"), filepath.Join(h, ".bun", "bin"))
	return strings.Join(dirs, string(os.PathListSeparator))
}
func (e *Engine) find(name string) string {
	for _, dir := range filepath.SplitList(e.pathEnv()) {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}
func (e *Engine) run(ctx context.Context, path string, args ...string) (string, error) {
	return e.exec(ctx, command{Path: path, Args: args, Env: e.environment(), Dir: e.opts.HomeDir})
}
