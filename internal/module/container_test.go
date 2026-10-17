package module

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/container"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// fakeHost is a PATH of shell stubs that log every call to $FAKE_LOG, the
// fake runner the module tests assert exact command sequences against.
type fakeHost struct {
	t   *testing.T
	bin string
	log string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	h := &fakeHost{t: t, bin: t.TempDir(), log: filepath.Join(t.TempDir(), "calls")}
	t.Setenv("PATH", h.bin)
	t.Setenv("FAKE_BIN", h.bin)
	t.Setenv("FAKE_LOG", h.log)
	t.Setenv("FAKE_STATE", t.TempDir())
	return h
}

// stub writes an executable that logs its argv, then runs body.
func (h *fakeHost) stub(name, body string) {
	h.t.Helper()
	writeExecutable(h.t, filepath.Join(h.bin, name),
		"#!/bin/sh\nprintf '%s\\n' \""+name+" $*\" >> \"$FAKE_LOG\"\n"+body+"\n")
}

// calls returns the logged argv lines, skipping the read-only probes so a
// test asserts only what changes the host.
func (h *fakeHost) calls(probes ...string) []string {
	h.t.Helper()
	data, err := os.ReadFile(h.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
next:
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		for _, p := range probes {
			if strings.HasPrefix(line, p) {
				continue next
			}
		}
		out = append(out, line)
	}
	return out
}

func assertCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func containerTestContext(t *testing.T, sys *config.SystemInfo, yes bool) *RunContext {
	t.Helper()
	runner := dotexec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &RunContext{
		Config: &config.Config{
			System:  sys,
			Modules: config.ModulesConfig{Container: config.ContainerConfig{Enabled: true}},
		},
		Runner:  runner,
		Brew:    dotexec.NewBrew(runner),
		HomeDir: t.TempDir(),
		Yes:     yes,
		Out:     io.Discard,
	}
}

func runContainer(t *testing.T, m *ContainerModule, rc *RunContext) error {
	t.Helper()
	if _, err := m.Check(context.Background(), rc); err != nil {
		return err
	}
	_, err := m.Apply(context.Background(), rc)
	return err
}

// --- macOS ---------------------------------------------------------------

func (h *fakeHost) mac(version string) {
	h.stub("sw_vers", "echo "+version)
	// brew: `install container` drops the container stub on PATH; `services
	// start` registers the login job.
	h.stub("brew", `case "$1 $2" in
"list --formula") [ -f "$FAKE_STATE/installed" ];;
"install container") : > "$FAKE_STATE/installed"; /bin/cp "$FAKE_STATE/container" "$FAKE_BIN/container";;
"services info") if [ -f "$FAKE_STATE/registered" ]; then echo '[{"registered":true}]'; else echo '[{"registered":false}]'; fi;;
"services start") : > "$FAKE_STATE/registered";;
*) exit 1;;
esac`)
	writeExecutable(h.t, filepath.Join(os.Getenv("FAKE_STATE"), "container"), `#!/bin/sh
printf '%s\n' "container $*" >> "$FAKE_LOG"
case "$1 $2" in
"system status") if [ -f "$FAKE_STATE/running" ]; then s=running; else s=stopped; fi
  echo "{\"status\":\"$s\",\"paths\":{\"appRoot\":\"$FAKE_STATE/app\"}}";;
"system start") : > "$FAKE_STATE/running";;
"system kernel") /bin/mkdir -p "$FAKE_STATE/app/kernels"; : > "$FAKE_STATE/app/kernels/default.kernel-arm64";;
"--version ") echo "container CLI version 1.5.0";;
esac`)
}

var macProbes = []string{"sw_vers", "brew list", "brew services info", "container system status", "container --version"}

func TestContainerMacFreshInstall(t *testing.T) {
	h := newFakeHost(t)
	h.mac("27.0")
	rc := containerTestContext(t, &config.SystemInfo{OS: "darwin", Arch: "arm64"}, true)

	if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, h.calls(macProbes...),
		"brew install container",
		"brew services start container",
		"container system start --disable-kernel-install",
		"container system kernel set --recommended",
	)
	st, err := container.LoadState(rc.HomeDir)
	if err != nil || st == nil || st.Backend != container.BackendApple || st.Binary != filepath.Join(h.bin, "container") || st.Version != "container CLI version 1.5.0" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	shim, err := os.ReadFile(container.ShimPath(rc.HomeDir))
	if err != nil || string(shim) != string(container.ShimScript(container.DotPath(rc.HomeDir), st.Binary)) {
		t.Fatalf("shim = %q, %v", shim, err)
	}
	if fi, _ := os.Stat(container.ShimPath(rc.HomeDir)); fi.Mode()&0o111 == 0 {
		t.Fatal("shim must be executable")
	}

	check, err := (&ContainerModule{Chosen: true}).Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("second check = %+v, %v; want satisfied", check, err)
	}
}

func TestContainerMacUnsupportedWritesNothing(t *testing.T) {
	for _, tc := range []struct{ arch, version string }{{"arm64", "15.6"}, {"amd64", "27.0"}} {
		h := newFakeHost(t)
		h.mac(tc.version)
		rc := containerTestContext(t, &config.SystemInfo{OS: "darwin", Arch: tc.arch}, true)
		m := &ContainerModule{}

		check, err := m.Check(context.Background(), rc)
		if err != nil || len(check.Changes) != 1 || !strings.Contains(check.Changes[0].Description, "unsupported platform") {
			t.Fatalf("%s %s check = %+v, %v", tc.arch, tc.version, check, err)
		}
		res, err := m.Apply(context.Background(), rc)
		if err != nil || res.Changed {
			t.Fatalf("apply = %+v, %v; want a silent skip", res, err)
		}
		assertCalls(t, h.calls("sw_vers"))
		if entries, _ := os.ReadDir(rc.HomeDir); len(entries) != 0 {
			t.Fatalf("unsupported host wrote %v", entries)
		}
	}
}

// --- Linux ---------------------------------------------------------------

var ubuntu = &config.SystemInfo{OS: "linux", DistroID: "ubuntu", DistroLike: []string{"debian"}}
var arch = &config.SystemInfo{OS: "linux", DistroID: "arch"}

// linux stubs apt-get/pacman presence and a sudo that runs nothing but
// "installs" by dropping a docker or podman stub on PATH. FAKE_FAIL names a
// package whose install fails.
func (h *fakeHost) linux() {
	h.stub("apt-get", "exit 0")
	h.stub("sudo", `[ "$1" = "-n" ] && exit 0
for a in "$@"; do
  if [ "$a" = "$FAKE_FAIL" ]; then exit 100; fi
  case "$a" in
  docker.io|docker) printf '#!/bin/sh\necho "Docker version 27.5.1"\n' > "$FAKE_BIN/docker"; /bin/chmod +x "$FAKE_BIN/docker";;
  podman) printf '#!/bin/sh\necho "podman version 4.9.3"\n' > "$FAKE_BIN/podman"; /bin/chmod +x "$FAKE_BIN/podman";;
  esac
done`)
}

func TestContainerLinuxExistingDockerIsUsedAsIs(t *testing.T) {
	h := newFakeHost(t)
	h.linux()
	h.stub("docker", `[ "$1" = "--version" ] && echo "Docker version 29.1.3"; exit 0`)
	rc := containerTestContext(t, ubuntu, true)

	if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
		t.Fatal(err)
	}
	// AC7: only the read-only probes ran; nothing installed or reconfigured.
	assertCalls(t, h.calls(), "docker info", "docker --version", "docker info", "docker --version")
	st, _ := container.LoadState(rc.HomeDir)
	if st == nil || st.Backend != container.BackendDocker || st.Version != "Docker version 29.1.3" {
		t.Fatalf("state = %+v", st)
	}
}

func TestContainerLinuxUnusableDockerStops(t *testing.T) {
	h := newFakeHost(t)
	h.linux()
	h.stub("docker", `[ "$1" = "info" ] && exit 1; exit 0`)
	rc := containerTestContext(t, ubuntu, true)

	err := runContainer(t, &ContainerModule{Chosen: true}, rc)
	if err == nil || !strings.Contains(err.Error(), "sudo usermod -aG docker $USER") {
		t.Fatalf("err = %v, want the usermod guidance", err)
	}
	assertCalls(t, h.calls("docker info"))
	if entries, _ := os.ReadDir(rc.HomeDir); len(entries) != 0 {
		t.Fatalf("a stop must write nothing, got %v", entries)
	}
}

func TestContainerLinuxDockerOfferAccepted(t *testing.T) {
	for _, tc := range []struct {
		sys     *config.SystemInfo
		install string
	}{
		{ubuntu, "sudo apt-get install -y docker.io"},
		{arch, "sudo pacman -S --needed --noconfirm docker"},
	} {
		h := newFakeHost(t)
		h.linux()
		h.stub("pacman", "exit 0")
		rc := containerTestContext(t, tc.sys, true) // --yes accepts the offer
		user := dockerGroupUser(rc)

		if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
			t.Fatal(err)
		}
		assertCalls(t, h.calls("sudo -n", "docker --version"),
			tc.install,
			"sudo systemctl enable --now docker",
			"sudo usermod -aG docker "+user,
		)
		if st, _ := container.LoadState(rc.HomeDir); st == nil || st.Backend != container.BackendDocker {
			t.Fatalf("state = %+v", st)
		}
	}
}

func TestContainerLinuxDockerDeclinedInstallsPodman(t *testing.T) {
	h := newFakeHost(t)
	h.linux()
	rc := containerTestContext(t, ubuntu, false)
	asked := ""
	m := &ContainerModule{Chosen: true, confirm: func(p string) (bool, error) { asked = p; return false, nil }}

	if err := runContainer(t, m, rc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "root-equivalent") {
		t.Fatalf("prompt %q must say the docker group is root-equivalent", asked)
	}
	assertCalls(t, h.calls("podman --version"), "sudo apt-get install -y podman")
	if st, _ := container.LoadState(rc.HomeDir); st == nil || st.Backend != container.BackendPodman {
		t.Fatalf("state = %+v", st)
	}
}

func TestContainerLinuxDockerInstallFailureFallsBackToPodman(t *testing.T) {
	h := newFakeHost(t)
	h.linux()
	t.Setenv("FAKE_FAIL", "docker.io")
	rc := containerTestContext(t, ubuntu, true)

	if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, h.calls("sudo -n", "podman --version"),
		"sudo apt-get install -y docker.io",
		"sudo apt-get install -y podman",
	)
	if st, _ := container.LoadState(rc.HomeDir); st == nil || st.Backend != container.BackendPodman {
		t.Fatalf("state = %+v", st)
	}
}

func TestContainerLinuxBackendPodmanSkipsDockerOffer(t *testing.T) {
	h := newFakeHost(t)
	h.linux()
	h.stub("docker", "exit 0") // present and usable, still not used
	rc := containerTestContext(t, ubuntu, true)
	rc.Config.Modules.Container.Backend = container.BackendPodman

	if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, h.calls("sudo -n", "podman --version"), "sudo apt-get install -y podman")
}

func TestContainerLinuxWithoutSudoPrintsCommands(t *testing.T) {
	h := newFakeHost(t)
	h.stub("apt-get", "exit 0") // no sudo on PATH
	rc := containerTestContext(t, ubuntu, true)

	err := runContainer(t, &ContainerModule{Chosen: true}, rc)
	for _, want := range []string{"sudo apt-get install -y docker.io", "sudo systemctl enable --now docker", "sudo apt-get install -y podman"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to list %q", err, want)
		}
	}
	assertCalls(t, h.calls())
	if entries, _ := os.ReadDir(rc.HomeDir); len(entries) != 0 {
		t.Fatalf("no-sudo stop wrote %v", entries)
	}
}

// --- legacy snippet ------------------------------------------------------

func TestContainerLegacySnippetCleanup(t *testing.T) {
	for _, tc := range []struct {
		content string
		removed bool
	}{
		{container.LegacySnippet, true},
		{container.LegacySnippet + "# my edit\n", false},
	} {
		h := newFakeHost(t)
		h.linux()
		h.stub("docker", "exit 0")
		rc := containerTestContext(t, ubuntu, true)
		path := container.LegacySnippetPath(rc.HomeDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); os.IsNotExist(err) != tc.removed {
			t.Fatalf("snippet removed = %v, want %v", os.IsNotExist(err), tc.removed)
		}
	}
}

// A docker install that failed and fell back to podman leaves the docker
// package behind; the next run keeps podman instead of stopping on it.
func TestContainerLinuxPodmanFallbackSticksOverBrokenDocker(t *testing.T) {
	h := newFakeHost(t)
	h.linux()
	h.stub("docker", `[ "$1" = "info" ] && exit 1; exit 0`)
	h.stub("podman", `[ "$1" = "--version" ] && echo "podman version 4.9.3"; exit 0`)
	rc := containerTestContext(t, ubuntu, true)
	st := &container.State{Backend: container.BackendPodman, Binary: filepath.Join(h.bin, "podman")}
	if err := os.MkdirAll(filepath.Dir(container.StatePath(rc.HomeDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(container.StatePath(rc.HomeDir), st.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
		t.Fatalf("podman fallback must not stop on the leftover docker: %v", err)
	}
	assertCalls(t, h.calls("docker info", "podman --version"))
	if got, _ := container.LoadState(rc.HomeDir); got == nil || got.Backend != container.BackendPodman {
		t.Fatalf("state = %+v", got)
	}
}

// --- host choice ---------------------------------------------------------

// The opt-in syncs between machines; a host that has not chosen never gets an
// install from an unattended run.
func TestContainerUndecidedHostNeverInstallsUnattended(t *testing.T) {
	h := newFakeHost(t)
	h.mac("27.0")
	rc := containerTestContext(t, &config.SystemInfo{OS: "darwin", Arch: "arm64"}, true) // --yes

	check, err := (&ContainerModule{}).Check(context.Background(), rc)
	if err != nil || check.Satisfied || len(check.Changes) != 1 || !strings.Contains(check.Changes[0].Description, "not set up on this host") {
		t.Fatalf("check = %+v, %v; want only the undecided note", check, err)
	}
	// --yes skips the question even where a prompt could run.
	noPrompt := &ContainerModule{confirm: func(p string) (bool, error) {
		t.Fatalf("--yes must not ask %q", p)
		return true, nil
	}}
	if err := runContainer(t, noPrompt, rc); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, h.calls(macProbes...))
	if entries, _ := os.ReadDir(rc.HomeDir); len(entries) != 0 {
		t.Fatalf("an unattended run on an undecided host wrote %v", entries)
	}
}

// Without a terminal and without --yes, an undecided host is not asked.
func TestContainerUndecidedHostWithoutTerminalSkips(t *testing.T) {
	h := newFakeHost(t)
	h.mac("27.0")
	rc := containerTestContext(t, &config.SystemInfo{OS: "darwin", Arch: "arm64"}, false)
	saved := ui.TerminalAttached
	ui.TerminalAttached = func() bool { return false }
	t.Cleanup(func() { ui.TerminalAttached = saved })

	if err := runContainer(t, &ContainerModule{}, rc); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, h.calls(macProbes...))
	if entries, _ := os.ReadDir(rc.HomeDir); len(entries) != 0 {
		t.Fatalf("a run without a terminal wrote %v", entries)
	}
}

func TestContainerUndecidedHostDeclinesOnce(t *testing.T) {
	h := newFakeHost(t)
	h.mac("27.0")
	rc := containerTestContext(t, &config.SystemInfo{OS: "darwin", Arch: "arm64"}, false)
	asked := 0
	m := &ContainerModule{confirm: func(p string) (bool, error) {
		if !strings.Contains(p, "this host has not chosen") {
			t.Fatalf("unexpected prompt %q", p)
		}
		asked++
		return false, nil
	}}

	if err := runContainer(t, m, rc); err != nil {
		t.Fatal(err)
	}
	st, _ := container.LoadState(rc.HomeDir)
	if st == nil || !st.Declined || st.Backend != "" {
		t.Fatalf("state = %+v, want a recorded decline", st)
	}
	check, err := m.Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("declined host check = %+v, %v; want satisfied", check, err)
	}
	if res, err := m.Apply(context.Background(), rc); err != nil || res.Changed {
		t.Fatalf("declined host apply = %+v, %v; want a no-op", res, err)
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want once", asked)
	}
	assertCalls(t, h.calls(macProbes...))
}

func TestContainerUndecidedHostAccepts(t *testing.T) {
	h := newFakeHost(t)
	h.mac("27.0")
	rc := containerTestContext(t, &config.SystemInfo{OS: "darwin", Arch: "arm64"}, false)
	m := &ContainerModule{confirm: func(string) (bool, error) { return true, nil }}

	if err := runContainer(t, m, rc); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, h.calls(macProbes...),
		"brew install container",
		"brew services start container",
		"container system start --disable-kernel-install",
		"container system kernel set --recommended",
	)
	if st, _ := container.LoadState(rc.HomeDir); st == nil || st.Backend != container.BackendApple {
		t.Fatalf("state = %+v", st)
	}
}

func TestContainerSetupOverridesDecline(t *testing.T) {
	h := newFakeHost(t)
	h.mac("27.0")
	rc := containerTestContext(t, &config.SystemInfo{OS: "darwin", Arch: "arm64"}, true)
	declined := &container.State{Declined: true}
	if err := os.MkdirAll(filepath.Dir(container.StatePath(rc.HomeDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(container.StatePath(rc.HomeDir), declined.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runContainer(t, &ContainerModule{Chosen: true}, rc); err != nil {
		t.Fatal(err)
	}
	st, _ := container.LoadState(rc.HomeDir)
	if st == nil || st.Declined || st.Backend != container.BackendApple {
		t.Fatalf("state = %+v, want setup to replace the decline", st)
	}
	if calls := h.calls(macProbes...); len(calls) == 0 || calls[0] != "brew install container" {
		t.Fatalf("setup after a decline did not install: %q", calls)
	}
}
