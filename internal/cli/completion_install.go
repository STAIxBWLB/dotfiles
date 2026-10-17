package cli

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// completionDir is the on-disk location for installed shell completions.
// Lives under XDG data home so removal of dotfiles cleans it up too.
func completionDir(homeDir string) string {
	return filepath.Join(homeDir, ".local", "share", "dotfiles", "completions")
}

// installCompletions generates zsh + bash + fish completion scripts for
// root and writes them to <home>/.local/share/dotfiles/completions/.
//
// Cobra writes completions tied to the root command's Name (dot).
// We post-process so both `dot` (canonical) and `dotfiles` (back-compat
// alias) are registered with the same handler — otherwise tab-completion
// would only fire for the canonical name.
//
// Files written (only rewritten when content changes):
//
//	_dot           zsh autoload-style; registers `compdef _dot dot dotfiles`
//	dot.bash       bash, source-style; registers complete for both names
//	dot.fish       fish, best-effort
//
// The fpath integration that picks these up lives in 05-completion.sh.
// Returns true when any file changed (so the caller can report it).
func installCompletions(root *cobra.Command, runner *exec.Runner, homeDir string) (bool, error) {
	dir := completionDir(homeDir)
	if err := runner.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("creating %s: %w", dir, err)
	}

	var changed bool

	// ── zsh ─────────────────────────────────────────────────────────────
	var zshBuf bytes.Buffer
	if err := root.GenZshCompletion(&zshBuf); err != nil {
		return changed, fmt.Errorf("generating zsh completion: %w", err)
	}
	zshScript := patchZshForAliases(zshBuf.Bytes(), root.Name(), root.Aliases)
	zshFile := filepath.Join(dir, "_dot")
	written, err := fileutil.EnsureFile(runner, homeDir, zshFile, zshScript, 0644)
	if err != nil {
		return changed, fmt.Errorf("writing %s: %w", zshFile, err)
	}
	if written {
		changed = true
	}

	// ── bash ────────────────────────────────────────────────────────────
	var bashBuf bytes.Buffer
	if err := root.GenBashCompletionV2(&bashBuf, true); err != nil {
		return changed, fmt.Errorf("generating bash completion: %w", err)
	}
	bashScript := patchBashForAliases(bashBuf.Bytes(), root.Name(), root.Aliases)
	bashFile := filepath.Join(dir, "dot.bash")
	written, err = fileutil.EnsureFile(runner, homeDir, bashFile, bashScript, 0644)
	if err != nil {
		return changed, fmt.Errorf("writing %s: %w", bashFile, err)
	}
	if written {
		changed = true
	}

	// ── fish (best-effort, write but don't fail apply if missing) ──────
	var fishBuf bytes.Buffer
	if err := root.GenFishCompletion(&fishBuf, true); err == nil {
		fishFile := filepath.Join(dir, "dot.fish")
		w, _ := fileutil.EnsureFile(runner, homeDir, fishFile, fishBuf.Bytes(), 0644)
		if w {
			changed = true
		}
	}

	return changed, nil
}

// completionAliases returns the root command aliases that should receive
// shell completion registration. The list is derived from root.Aliases so
// command alias changes have one source of truth.
func completionAliases(command string, aliases []string) []string {
	seen := map[string]bool{command: true}
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		out = append(out, alias)
	}
	return out
}

// patchZshForAliases rewrites the `compdef _dot dot` directive so the
// same handler also fires for alias names. Cobra's standard output
// binds only to root.Name(); without this, `dotfiles <Tab>` does
// nothing while `dot <Tab>` works.
func patchZshForAliases(script []byte, command string, aliases []string) []byte {
	aliases = completionAliases(command, aliases)
	if len(aliases) == 0 {
		return script
	}
	handler := "_" + command
	old := []byte(fmt.Sprintf("\ncompdef %s %s\n", handler, command))
	if !bytes.Contains(script, old) {
		// Cobra format changed; leave the script as-is rather than corrupt it.
		return script
	}
	names := append([]string{handler, command}, aliases...)
	replacement := []byte("\ncompdef " + strings.Join(names, " ") + "\n")
	out := bytes.Replace(script, old, replacement, 1)

	// Also fix the `#compdef dot` header so all names are recognized
	// when the file is autoloaded standalone.
	headerOld := []byte("#compdef " + command + "\n")
	headerNames := append([]string{command}, aliases...)
	headerNew := []byte("#compdef " + strings.Join(headerNames, " ") + "\n")
	out = bytes.Replace(out, headerOld, headerNew, 1)
	return out
}

// patchBashForAliases appends extra `complete` registrations so alias
// names fire the same handler. Cobra v2 bash output ends with
// the registration block for the canonical name only.
func patchBashForAliases(script []byte, command string, aliases []string) []byte {
	aliases = completionAliases(command, aliases)
	if len(aliases) == 0 {
		return script
	}
	aliasList := strings.Join(aliases, " ")
	handler := "__start_" + strings.NewReplacer("-", "_", ":", "_").Replace(command)
	addition := []byte(fmt.Sprintf(`
# Register handlers for aliases `+"`%s`"+` so they tab-complete the same as %s.
if [[ $(type -t compopt) = "builtin" ]]; then
    complete -o default -F %s %s
else
    complete -o default -o nospace -F %s %s
fi
`, aliasList, command, handler, aliasList, handler, aliasList))

	// Insert before the trailing "ex:" mode-line so the file still
	// looks tidy; if the marker isn't there, just append.
	mode := []byte("# ex: ts=")
	idx := bytes.LastIndex(script, mode)
	if idx < 0 {
		return append(script, addition...)
	}
	out := make([]byte, 0, len(script)+len(addition))
	out = append(out, script[:idx]...)
	out = append(out, addition...)
	out = append(out, script[idx:]...)
	return out
}
