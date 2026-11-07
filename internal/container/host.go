package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SuggestedDNS is offered when WARP holds port 53 and modules.container.dns
// is empty.
var SuggestedDNS = []string{"1.1.1.1", "1.0.0.1"}

// ShimsDir is the directory 00-exports.sh puts ahead of the brew bin dirs.
func ShimsDir(home string) string {
	return filepath.Join(home, ".local", "share", "dotfiles", "shims")
}

// ShimPath is the `container` shim.
func ShimPath(home string) string { return filepath.Join(ShimsDir(home), "container") }

// StatePath is the record of the chosen backend.
func StatePath(home string) string {
	return filepath.Join(home, ".local", "share", "dotfiles", "container", "state.json")
}

// LegacySnippetPath is the hand-written zsh workaround the shim replaces.
func LegacySnippetPath(home string) string {
	return filepath.Join(home, ".config", "shell", "45-container.sh")
}

// LegacySnippet is the workaround's known content. Setup removes the file
// only when it still matches byte for byte.
const LegacySnippet = `# apple/container: Cloudflare WARP binds 127.0.2.2/127.0.2.3:53, so the vmnet
# DNS proxy never listens on the container gateway (192.168.64.1:53) and lookups
# are refused (apple/container#402). Default to public DNS unless --dns/--no-dns
# is given. Drop this once a default-DNS property ships (apple/container#1449).
# Local to this Mac, not managed by dot apply.
CONTAINER_DNS=(--dns 1.1.1.1 --dns 1.0.0.1)

container() {
  case "$1" in
    run|create|build)
      if [[ " $* " != *" --dns "* && " $* " != *" --dns="* && " $* " != *" --no-dns "* ]]; then
        command container "$1" "${CONTAINER_DNS[@]}" "${@:2}"
        return
      fi
      ;;
    builder)
      if [[ "$2" == start && " $* " != *" --dns "* && " $* " != *" --dns="* ]]; then
        command container builder start "${CONTAINER_DNS[@]}" "${@:3}"
        return
      fi
      ;;
  esac
  command container "$@"
}
`

// State records this host's own container decision: the backend setup chose,
// so the shim and exec need no probe, or that the host declined. It lives
// outside the synced config, so each host decides for itself.
type State struct {
	Backend   string    `json:"backend"`
	Binary    string    `json:"binary"`
	Version   string    `json:"version,omitempty"`
	Declined  bool      `json:"declined,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Same reports whether two states name the same backend install.
func (s *State) Same(o *State) bool {
	return s != nil && o != nil && s.Backend == o.Backend && s.Binary == o.Binary && s.Version == o.Version
}

// LoadState reads state.json; a missing file is (nil, nil).
func LoadState(home string) (*State, error) {
	data, err := os.ReadFile(StatePath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", StatePath(home), err)
	}
	return &s, nil
}

// Marshal renders state.json.
func (s *State) Marshal() []byte {
	data, _ := json.MarshalIndent(s, "", "  ")
	return append(data, '\n')
}

// ShimScript is the POSIX sh shim. It hands every call to dot, and execs the
// backend directly, with a warning, when dot is missing so a broken dot
// never takes containers down. dot is named by absolute path: Graphviz
// installs a `dot` of its own that can come first on PATH.
func ShimScript(dotPath, backendBinary string) []byte {
	dot, backend := shellQuote(dotPath), shellQuote(backendBinary)
	return []byte(`#!/bin/sh
# Managed by dot (modules.container). Rewritten by dot container setup.
if [ -x ` + dot + ` ]; then
  exec ` + dot + ` container exec -- "$@"
fi
printf 'container: %s is missing; running %s untranslated (rerun dot container setup)\n' ` + dot + ` ` + backend + ` >&2
exec ` + backend + ` "$@"
`)
}

// DotPath is the dot binary the shim runs: ~/.local/bin/dot when it has the
// container command (the stable target the guard hook also pins), else this
// executable. A Homebrew keg path (<prefix>/Cellar/<formula>/<version>/bin/dot)
// is mapped to the formula's opt link, and a cask path
// (<prefix>/Caskroom/<cask>/<version>/dot) to the cask's prefix/bin link, so a
// brew upgrade keeps the shim valid.
func DotPath(home string) string {
	self := filepath.Join(home, ".local", "bin", "dot")
	if !hasContainerCommand(self) {
		var err error
		if self, err = os.Executable(); err != nil {
			return "dot"
		}
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return optLinkFor(self)
}

// hasContainerCommand reports whether dot has the container command; an older
// dot rejects `container` as an unknown command.
func hasContainerCommand(dot string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return osexec.CommandContext(ctx, dot, "container", "--help").Run() == nil
}

func optLinkFor(self string) string {
	p := filepath.ToSlash(self)
	if i := strings.Index(p, "/Cellar/"); i >= 0 {
		if formula, rest, ok := strings.Cut(p[i+len("/Cellar/"):], "/"); ok {
			if _, bin, ok := strings.Cut(rest, "/"); ok {
				return filepath.FromSlash(p[:i] + "/opt/" + formula + "/" + bin)
			}
		}
	}
	if i := strings.Index(p, "/Caskroom/"); i >= 0 {
		if parts := strings.Split(p[i+len("/Caskroom/"):], "/"); len(parts) == 3 {
			return filepath.FromSlash(p[:i] + "/bin/" + parts[2])
		}
	}
	return self
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// MacUnsupported returns why apple/container cannot run on this Mac, or ""
// when it can. Upstream supports Apple silicon on macOS 26 and later only.
func MacUnsupported(arch, productVersion string) string {
	if arch != "arm64" {
		return fmt.Sprintf("apple/container needs Apple silicon (this Mac is %s)", arch)
	}
	major, _ := strconv.Atoi(strings.SplitN(strings.TrimSpace(productVersion), ".", 2)[0])
	if major < 26 {
		return fmt.Sprintf("apple/container needs macOS 26 or later (this Mac runs %s)", strings.TrimSpace(productVersion))
	}
	return ""
}

// WarpHoldsDNS reports whether `scutil --dns` output lists Cloudflare WARP's
// resolvers (127.0.2.2, 127.0.2.3), which WARP serves on port 53. The vmnet
// DNS proxy then never listens on the container gateway
// (apple/container#402). netstat is no witness here: on macOS 27 its socket
// list comes back empty when dot, not the shell, runs it.
func WarpHoldsDNS(scutilDNS string) bool {
	for _, line := range strings.Split(scutilDNS, "\n") {
		name, addr, ok := strings.Cut(line, ":")
		if !ok || !strings.HasPrefix(strings.TrimSpace(name), "nameserver[") {
			continue
		}
		if a := strings.TrimSpace(addr); a == "127.0.2.2" || a == "127.0.2.3" {
			return true
		}
	}
	return false
}

// InjectDNS adds one --dns per server unless the user chose DNS with --dns or
// --no-dns. It covers run, create, build and builder start; on linux only run
// and create (docker build has no --dns, builder start is a no-op there), and
// --no-dns is dropped there because docker has no such flag.
//
// ponytail: known ceiling. See docs/CEILINGS.md (container DNS injection).
func InjectDNS(args, servers []string, linux bool) []string {
	if len(args) == 0 {
		return args
	}
	at, end := 0, len(args)
	switch {
	case args[0] == "run" || args[0] == "create":
		at, end = 1, 1+flagSpan(args[1:])
	case args[0] == "build" && !linux:
		at = 1
	case args[0] == "builder" && len(args) > 1 && args[1] == "start" && !linux:
		at = 2
	default:
		return args
	}
	chose := false
	var head []string
	for _, a := range args[at:end] {
		if a == "--dns" || strings.HasPrefix(a, "--dns=") || a == "--no-dns" {
			chose = true
		}
		if linux && a == "--no-dns" {
			continue
		}
		head = append(head, a)
	}
	out := append([]string(nil), args[:at]...)
	if !chose {
		for _, s := range servers {
			out = append(out, "--dns", s)
		}
	}
	out = append(out, head...)
	return append(out, args[end:]...)
}

// LookPath finds name on PATH, skipping skipDir (the shims dir) so a lookup
// never resolves to the shim itself.
func LookPath(name, skipDir string) string {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || filepath.Clean(dir) == filepath.Clean(skipDir) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// AppleStatus is the part of `container system status --format json` dot reads.
type AppleStatus struct {
	Status string `json:"status"`
	Paths  struct {
		AppRoot string `json:"appRoot"`
	} `json:"paths"`
}

// ParseAppleStatus decodes status output; anything unreadable is "not running".
func ParseAppleStatus(out string) AppleStatus {
	var s AppleStatus
	_ = json.Unmarshal([]byte(out), &s)
	return s
}

// Running reports whether the apiserver is up.
func (s AppleStatus) Running() bool { return s.Status == "running" }

// KernelPath is the default guest kernel apple/container boots, under its app
// root (the default root when the stopped system cannot report one).
func KernelPath(home, appRoot string) string {
	if appRoot == "" {
		appRoot = filepath.Join(home, "Library", "Application Support", "com.apple.container")
	}
	return filepath.Join(appRoot, "kernels", "default.kernel-arm64")
}

// Argv returns the backend argv for an Apple-syntax call: unchanged on the
// Mac apart from DNS, translated to the docker dialect on Linux.
func Argv(backend string, args, dns []string) (Translation, error) {
	linux := backend != BackendApple
	args = InjectDNS(args, dns, linux)
	if !linux {
		return Translation{Args: args}, nil
	}
	return Translate(args)
}

// Resolve picks the backend binary exec runs: the one state.json recorded,
// else what is installed now (apple/container on macOS; docker, then podman,
// on Linux). It never returns the shim.
func Resolve(home, goos string, st *State) (backend, binary string) {
	shims := ShimsDir(home)
	if st != nil && st.Binary != "" && filepath.Dir(st.Binary) != shims {
		if fi, err := os.Stat(st.Binary); err == nil && !fi.IsDir() {
			return st.Backend, st.Binary
		}
	}
	if goos == "darwin" {
		if p := LookPath("container", shims); p != "" {
			return BackendApple, p
		}
		return "", ""
	}
	for _, b := range []string{BackendDocker, BackendPodman} {
		if p := LookPath(b, shims); p != "" {
			return b, p
		}
	}
	return "", ""
}
