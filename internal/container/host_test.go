package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var dns = []string{"1.1.1.1", "1.0.0.1"}

func TestInjectDNS(t *testing.T) {
	cases := []struct {
		in    string
		linux bool
		want  string
	}{
		{"run --rm alpine wget -qO- https://example.com", false, "run --dns 1.1.1.1 --dns 1.0.0.1 --rm alpine wget -qO- https://example.com"},
		{"create alpine", false, "create --dns 1.1.1.1 --dns 1.0.0.1 alpine"},
		{"build -t t .", false, "build --dns 1.1.1.1 --dns 1.0.0.1 -t t ."},
		{"builder start -c 2", false, "builder start --dns 1.1.1.1 --dns 1.0.0.1 -c 2"},
		// the user's own choice wins
		{"run --dns 8.8.8.8 alpine", false, "run --dns 8.8.8.8 alpine"},
		{"run --dns=8.8.8.8 alpine", false, "run --dns=8.8.8.8 alpine"},
		{"run --no-dns alpine", false, "run --no-dns alpine"},
		{"build --dns 8.8.8.8 .", false, "build --dns 8.8.8.8 ."},
		// a --dns after the image belongs to the process, not to us
		{"run alpine dig --dns", false, "run --dns 1.1.1.1 --dns 1.0.0.1 alpine dig --dns"},
		// other verbs untouched
		{"exec web nslookup x", false, "exec web nslookup x"},
		{"builder stop", false, "builder stop"},
		// Linux: run/create only, --no-dns consumed
		{"run alpine", true, "run --dns 1.1.1.1 --dns 1.0.0.1 alpine"},
		{"run --no-dns --rm alpine", true, "run --rm alpine"},
		{"build -t t .", true, "build -t t ."},
		{"builder start", true, "builder start"},
	}
	for _, tc := range cases {
		got := InjectDNS(strings.Fields(tc.in), dns, tc.linux)
		if want := strings.Fields(tc.want); !reflect.DeepEqual(got, want) {
			t.Errorf("InjectDNS(%q, linux=%v) = %q, want %q", tc.in, tc.linux, got, want)
		}
	}
	if got := InjectDNS([]string{"run", "alpine"}, nil, false); !reflect.DeepEqual(got, []string{"run", "alpine"}) {
		t.Errorf("empty dns must leave argv alone, got %q", got)
	}
}

func TestArgvAppleKeepsSyntax(t *testing.T) {
	got, err := Argv(BackendApple, strings.Fields("run -c 4 -a arm64 alpine"), dns)
	want := strings.Fields("run --dns 1.1.1.1 --dns 1.0.0.1 -c 4 -a arm64 alpine")
	if err != nil || !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("Argv apple = %q, %v; want %q", got.Args, err, want)
	}
	got, err = Argv(BackendDocker, strings.Fields("run -c 4 alpine"), dns)
	want = strings.Fields("run --dns 1.1.1.1 --dns 1.0.0.1 --cpus 4 alpine")
	if err != nil || !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("Argv docker = %q, %v; want %q", got.Args, err, want)
	}
}

// Fixed `scutil --dns` output, as macOS 27 prints it with WARP connected.
const scutilWarp = `DNS configuration

resolver #1
  nameserver[0] : 127.0.2.2
  nameserver[1] : 127.0.2.3
  if_index : 26 (utun10)
  flags    : Request A records, Request AAAA records
  reach    : 0x00000003 (Reachable,Transient Connection)

resolver #2
  domain   : local
  options  : mdns
`

const scutilPlain = `DNS configuration

resolver #1
  nameserver[0] : 192.168.0.1
  if_index : 15 (en0)
`

func TestWarpHoldsDNS(t *testing.T) {
	if !WarpHoldsDNS(scutilWarp) {
		t.Error("WARP resolvers on 127.0.2.2/127.0.2.3 must be detected")
	}
	if WarpHoldsDNS(scutilPlain) || WarpHoldsDNS("") {
		t.Error("a plain resolver list must not count as WARP")
	}
}

func TestMacUnsupported(t *testing.T) {
	for _, tc := range []struct {
		arch, version string
		ok            bool
	}{
		{"arm64", "27.0\n", true},
		{"arm64", "26.0.1", true},
		{"arm64", "15.6", false},
		{"amd64", "27.0", false},
	} {
		if got := MacUnsupported(tc.arch, tc.version); (got == "") != tc.ok {
			t.Errorf("MacUnsupported(%s, %q) = %q, want ok=%v", tc.arch, tc.version, got, tc.ok)
		}
	}
}

func TestStateRoundTripAndShim(t *testing.T) {
	home := t.TempDir()
	if st, err := LoadState(home); st != nil || err != nil {
		t.Fatalf("missing state = %v, %v; want nil, nil", st, err)
	}
	want := &State{Backend: BackendDocker, Binary: "/usr/bin/docker", Version: "Docker version 29.1.3"}
	if err := os.MkdirAll(filepath.Dir(StatePath(home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StatePath(home), want.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(home)
	if err != nil || !got.Same(want) {
		t.Fatalf("LoadState = %+v, %v; want %+v", got, err, want)
	}

	shim := string(ShimScript("/opt/homebrew/opt/dotfiles/bin/dot", "/opt/it's/container"))
	for _, s := range []string{"#!/bin/sh\n", `if [ -x '/opt/homebrew/opt/dotfiles/bin/dot' ]; then`,
		`exec '/opt/homebrew/opt/dotfiles/bin/dot' container exec -- "$@"`, `exec '/opt/it'\''s/container' "$@"`} {
		if !strings.Contains(shim, s) {
			t.Errorf("shim lacks %q:\n%s", s, shim)
		}
	}
}

func TestResolveSkipsShimAndPrefersState(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	for _, dir := range []string{ShimsDir(home), bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{ShimPath(home), filepath.Join(bin, "docker"), filepath.Join(bin, "podman")} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", ShimsDir(home)+string(os.PathListSeparator)+bin)

	if b, p := Resolve(home, "linux", nil); b != BackendDocker || p != filepath.Join(bin, "docker") {
		t.Fatalf("Resolve without state = %s %s, want docker from %s", b, p, bin)
	}
	st := &State{Backend: BackendPodman, Binary: filepath.Join(bin, "podman")}
	if b, p := Resolve(home, "linux", st); b != BackendPodman || p != st.Binary {
		t.Fatalf("Resolve with state = %s %s, want the recorded podman", b, p)
	}
	if b, _ := Resolve(home, "darwin", &State{Backend: BackendApple, Binary: ShimPath(home)}); b != "" {
		t.Fatalf("a state naming the shim must not resolve, got backend %q", b)
	}
}

func TestDotPathPrefersLocalBin(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, ".local", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(local)
	if got := DotPath(home); got != want {
		t.Fatalf("DotPath = %s, want %s", got, want)
	}
	if got := DotPath(t.TempDir()); got == want || got == "" {
		t.Fatalf("without ~/.local/bin/dot, DotPath = %q, want this executable", got)
	}
	// A dot that predates the container command is not pinned.
	if err := os.WriteFile(local, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DotPath(home); got == want {
		t.Fatalf("DotPath pinned a dot without the container command: %s", got)
	}
}

func TestShimFallsBackLoudly(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, "backend")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\necho \"backend $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "container")
	if err := os.WriteFile(shim, ShimScript(filepath.Join(dir, "gone", "dot"), backend), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(shim, "ls", "-a").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "is missing; running") || !strings.Contains(string(out), "backend ls -a") {
		t.Fatalf("fallback output = %q, %v", out, err)
	}
}

func TestOptLinkFor(t *testing.T) {
	for in, want := range map[string]string{
		"/opt/homebrew/Cellar/dotfiles/2.70.33/bin/dot":              "/opt/homebrew/opt/dotfiles/bin/dot",
		"/home/linuxbrew/.linuxbrew/Cellar/dotfiles/2.70.33/bin/dot": "/home/linuxbrew/.linuxbrew/opt/dotfiles/bin/dot",
		"/opt/homebrew/Caskroom/dotfiles/2.76.0/dot":                 "/opt/homebrew/bin/dot",
		"/Users/u/.local/bin/dot":                                    "/Users/u/.local/bin/dot",
	} {
		if got := optLinkFor(in); got != want {
			t.Errorf("optLinkFor(%s) = %s, want %s", in, got, want)
		}
	}
}
