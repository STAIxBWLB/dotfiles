package aitooling

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testEngine(t *testing.T) *Engine {
	t.Helper()
	// Provider tests do not need shell/account credentials. Keep only ordinary
	// OS location/locale variables; production environment behavior is exercised
	// explicitly with synthetic overrides below.
	safe := map[string]bool{"HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "PATH": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true, "LC_ALL": true, "TZ": true, "SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !safe[key] {
			t.Setenv(key, "")
		}
	}
	home := t.TempDir()
	t.Setenv("PATH", filepath.Join(home, "bin"))
	e := New(Options{HomeDir: home, ExplicitHome: true})
	e.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"2.0.0"}`)), Header: make(http.Header)}, nil
	})}
	e.exec = func(context.Context, command) (string, error) { t.Fatal("unexpected command"); return "", nil }
	e.acquire = func(context.Context, resourceguard.Options) (func(), error) { return func() {}, nil }
	return e
}
func fakeBinary(t *testing.T, e *Engine, name string) string {
	t.Helper()
	p := filepath.Join(e.opts.HomeDir, ".local", "bin", name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestCatalogAndSelectionAllowlist(t *testing.T) {
	var agents, tools []string
	for _, e := range Catalog() {
		if e.Kind == "agent" {
			agents = append(agents, e.ID)
		} else {
			tools = append(tools, e.ID)
		}
	}
	if !reflect.DeepEqual(agents, []string{"claude", "codex", "kimi", "qwen", "grok", "opencode", "gencode", "pi", "antigravity"}) {
		t.Fatal(agents)
	}
	if !reflect.DeepEqual(tools, []string{"ripwire", "ocr", "gsd", "ponytail"}) {
		t.Fatal(tools)
	}
	for _, bad := range []string{"skills", "firecrawl", "cursor", "agent-hub"} {
		if ValidateSelection(config.AIToolingConfig{Tools: []string{bad}}) == nil {
			t.Fatal(bad)
		}
	}
	if ValidateSelection(config.AIToolingConfig{Agents: []string{"kimi", "kimi"}}) == nil {
		t.Fatal("duplicate accepted")
	}
	if ValidateSelection(config.AIToolingConfig{Agents: []string{"codex"}, Pins: map[string]string{"codex": "1.0.0-beta"}}) == nil {
		t.Fatal("prerelease pin accepted")
	}
	if ValidateSelection(config.AIToolingConfig{Agents: []string{"gencode"}, Pins: map[string]string{"gencode": "1.18.32-gencode.1"}}) != nil {
		t.Fatal("gencode suffixed pin rejected")
	}
	if ValidateSelection(config.AIToolingConfig{Agents: []string{"pi"}, Pins: map[string]string{"pi": "0.87.1-beta"}}) == nil {
		t.Fatal("pi prerelease pin accepted")
	}
}
func TestInspectAndDryRunDoNotAcquireOrSave(t *testing.T) {
	for _, op := range []Operation{Inspect, Ensure, Update} {
		t.Run(string(op), func(t *testing.T) {
			e := testEngine(t)
			e.opts.DryRun = op != Inspect
			e.acquire = func(context.Context, resourceguard.Options) (func(), error) {
				t.Fatal("acquired mutation lease")
				return nil, nil
			}
			r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"codex"}}, op)
			if err != nil {
				t.Fatal(err)
			}
			want := "missing"
			if op != Inspect {
				want = "planned"
			}
			if len(r.Items) != 1 || r.Items[0].Status != want {
				t.Fatalf("%+v", r)
			}
			if pathExists(e.statePath()) {
				t.Fatal("read-only operation wrote receipts")
			}
		})
	}
}
func TestPressureDefersBeforeNetworkOrMutation(t *testing.T) {
	e := testEngine(t)
	e.acquire = func(context.Context, resourceguard.Options) (func(), error) {
		return nil, &resourceguard.DeferredError{Reason: "memory-warning"}
	}
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("metadata queried before admission")
		return nil, nil
	})
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"codex"}}, Update)
	if err != nil || r.Deferred != 1 || r.Items[0].Status != "deferred-resource-pressure" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestUnknownMetadataNeverCurrent(t *testing.T) {
	e := testEngine(t)
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"codex"}}, Inspect)
	if err != nil || r.Items[0].Status != "unknown" || r.Items[0].Latest != "" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestLatestRejectsDraftAndPrerelease(t *testing.T) {
	for _, body := range []string{`{"tag_name":"v2.0.0","prerelease":true}`, `{"version":"2.0.0-beta"}`, `{"tag_name":"v2.0.0","draft":true}`, `{}`} {
		e := testEngine(t)
		e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		if _, err := e.latest(context.Background(), "https://example.invalid/latest"); err == nil {
			t.Fatal(body)
		}
	}
}
func TestSubsetRetainsAgentContextWithoutUpdatingAgent(t *testing.T) {
	e := testEngine(t)
	e.opts.Only = []string{"ponytail"}
	e.opts.DryRun = true
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("agent metadata queried outside selected subset")
		return nil, nil
	})
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"grok"}, Tools: []string{"ponytail"}}, Update)
	if err != nil || len(r.Items) != 1 || r.Items[0].ID != "ponytail/grok" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestSelectedInstallUsesExactVersionAndSingleLease(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "npm")
	lease := 0
	released := false
	e.acquire = func(context.Context, resourceguard.Options) (func(), error) {
		lease++
		return func() { released = true }, nil
	}
	var commands []command
	e.exec = func(_ context.Context, c command) (string, error) {
		commands = append(commands, c)
		if filepath.Base(c.Path) == "npm" {
			fakeBinary(t, e, "codex")
			return "", nil
		}
		return "codex-cli 2.0.0", nil
	}
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"codex"}}, Ensure)
	if err != nil || r.Failed != 0 || lease != 1 || !released {
		t.Fatalf("%+v %v", r, err)
	}
	if len(commands) != 2 || strings.Join(commands[0].Args, " ") != "install --global --prefix "+filepath.Join(e.opts.HomeDir, ".local")+" --registry=https://registry.npmjs.org @openai/codex@2.0.0" {
		t.Fatalf("unexpected command sequence %v", commandDiagnostics(commands))
	}
	if !pathExists(e.statePath()) {
		t.Fatal("missing receipt")
	}
	if r.Items[0].Installed != "2.0.0" {
		t.Fatal(r)
	}
}
func TestPartialFailureContinuesIndependentSelection(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "npm")
	calls := 0
	e.exec = func(_ context.Context, c command) (string, error) { calls++; return "", errors.New("fixture failure") }
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"codex", "qwen"}}, Ensure)
	if err == nil || r.Failed != 2 || calls != 2 {
		t.Fatalf("%+v %v calls=%d", r, err, calls)
	}
}
func TestExplicitHomeSanitizesProfileEnvironment(t *testing.T) {
	e := testEngine(t)
	t.Setenv("CODEX_HOME", "/outside/account")
	t.Setenv("KIMI_CODE_HOME", "/outside/kimi")
	t.Setenv("OPENCODE_CONFIG_DIR", "/outside/opencode")
	env := strings.Join(e.environment(), "\n")
	for _, outside := range []string{"/outside/account", "/outside/kimi", "/outside/opencode"} {
		if strings.Contains(env, outside) {
			t.Fatal("profile environment assertion failed; values redacted")
		}
	}
	if !strings.Contains(env, "CODEX_HOME="+filepath.Join(e.opts.HomeDir, ".codex")) {
		t.Fatal("profile environment assertion failed; values redacted")
	}
}
func TestGSDManifestRejectsLocalEditsAndTraversal(t *testing.T) {
	root := t.TempDir()
	for _, body := range []string{`{"version":"1.0.0","files":{"../outside":"abc"}}`, `{"version":"1.0.0","files":{"missing":"abc"}}`, `bad`} {
		if err := os.WriteFile(filepath.Join(root, "gsd-file-manifest.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, clean := gsdManifestAt(root, filepath.Join(root, "skills")); clean {
			t.Fatal(body)
		}
	}
}
func TestMaruCapabilitiesFailClosed(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "maru")
	e.exec = func(_ context.Context, c command) (string, error) {
		if strings.Join(c.Args, " ") != "skills capabilities --json" {
			t.Fatalf("unexpected mutation: %v", c.Args)
		}
		return `{}`, nil
	}
	r := e.skills(context.Background(), config.AIToolingConfig{Agents: []string{"kimi"}, Skills: []string{"x"}}, Ensure)
	if r.Status != "deferred-maru-capability" {
		t.Fatal(r)
	}
}
func TestMaruSyncTargetsExactSelection(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "maru")
	var got []string
	e.exec = func(_ context.Context, c command) (string, error) {
		if c.Args[1] == "capabilities" {
			return `{"schemaVersion":1,"targets":["claude","codex","kimi","qwen","grok","opencode"],"selectedSync":true,"list":true}`, nil
		}
		got = c.Args
		return `{"applied":` + fmt.Sprint(c.Args[2] == "--apply") + `,"tools":["kimi","grok"],"desiredSkills":1,"desiredInstalls":2,"actions":[]}`, nil
	}
	r := e.skills(context.Background(), config.AIToolingConfig{Agents: []string{"kimi", "grok"}, Skills: []string{"my-skill"}}, Ensure)
	if r.Status != "applied" || !reflect.DeepEqual(got, []string{"skills", "sync", "--check", "--tools", "kimi,grok", "--skills", "my-skill", "--json"}) {
		t.Fatalf("%+v %v", r, got)
	}
}
func TestOpenCodeExistingPinPreserved(t *testing.T) {
	e := testEngine(t)
	p := filepath.Join(e.profile("opencode"), "opencode.json")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	before := `{"plugin":["@dietrichgebert/ponytail@1.0.0"],"model":"custom"}`
	if err := os.WriteFile(p, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	r := e.openCodePonytail(context.Background(), "", Update)
	after, _ := os.ReadFile(p)
	if r.Status != "pinned" || string(after) != before {
		t.Fatalf("%+v %s", r, after)
	}
}

func TestExplicitHomeClearsNativeRootAndCredentialOverrides(t *testing.T) {
	e := testEngine(t)
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "AGENTS_HOME", "CLAUDE_MEM_DATA_DIR", "GROK_DEPLOYMENT_KEY"} {
		t.Setenv(key, "external-value")
	}
	if strings.Contains(strings.Join(e.environment(), "\n"), "external-value") {
		t.Fatal("external profile/data/credential override leaked")
	}
}

// claude-mem left the add-on catalog (dotfiles#255). A saved selection that still names it is not a
// supported selection any more here; config.AIToolingConfig.Clone strips it on load so apply keeps working.
func TestClaudeMemIsNoLongerACatalogAddon(t *testing.T) {
	for _, entry := range Catalog() {
		if entry.ID == "claude-mem" || entry.ID == "amem" {
			t.Fatalf("%s must not be an automatic add-on: amem is registered by `dot ai memory install`, never installed by dot", entry.ID)
		}
	}
	if err := ValidateSelection(config.AIToolingConfig{Tools: []string{"claude-mem"}}); err == nil {
		t.Fatal("a selection naming claude-mem validated")
	}
}
func TestNativePluginPinNeverUpdatesMarketplace(t *testing.T) {
	e := testEngine(t)
	p := filepath.Join(e.profile("claude"), "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"plugins":{"ponytail@ponytail":[{"scope":"user","version":"1.0.0","installPath":"/fixture"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := e.plugin(context.Background(), "ponytail", "claude", "1.0.0", Update)
	if r.Status != "pinned" || r.Installed != "1.0.0" {
		t.Fatal(r)
	}
}
func TestPluginDigestDetectsNativeCacheEdits(t *testing.T) {
	e := testEngine(t)
	p := t.TempDir()
	file := filepath.Join(p, "plugin.json")
	if err := os.WriteFile(file, []byte(`{"version":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := pluginDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	e.receipts["ponytail/codex"] = receipt{Integrity: digest}
	if !e.pluginIntegrity(context.Background(), "ponytail/codex", "codex", "ponytail@ponytail", p) {
		t.Fatal("clean recorded cache rejected")
	}
	if err = os.WriteFile(file, []byte(`{"version":"1.0.0","local":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if e.pluginIntegrity(context.Background(), "ponytail/codex", "codex", "ponytail@ponytail", p) {
		t.Fatal("same-version local cache edit not detected")
	}
}
func TestMaruValidJSONIsNotEnough(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "maru")
	e.exec = func(_ context.Context, c command) (string, error) {
		if c.Args[1] == "capabilities" {
			return `{"schemaVersion":1,"targets":["claude"],"selectedSync":true,"list":true}`, nil
		}
		return `{"applied":true,"actions":[{"action":"conflict"}]}`, nil
	}
	r := e.skills(context.Background(), config.AIToolingConfig{Agents: []string{"claude"}, Skills: []string{"x"}}, Ensure)
	if r.Status == "applied" {
		t.Fatal(r)
	}
}

func TestMaintenanceAdmissionUsesSharedToolingScope(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(fmt.Sprint(scheduled), func(t *testing.T) {
			e := testEngine(t)
			e.opts.Scheduled = scheduled
			e.opts.Only = []string{"ponytail"}
			calls := 0
			released := false
			admit := func(_ context.Context, opts resourceguard.Options) (func(), error) {
				calls++
				if opts.ScopeKey != "tooling" {
					t.Fatalf("maintenance uses %q scope instead of shared tooling", opts.ScopeKey)
				}
				if opts.HomeDir != e.opts.HomeDir {
					t.Fatalf("admission home mismatch: %+v", opts)
				}
				return func() { released = true }, nil
			}
			e.acquire = func(ctx context.Context, opts resourceguard.Options) (func(), error) {
				if scheduled {
					t.Fatal("scheduled maintenance skipped recovery wait")
				}
				return admit(ctx, opts)
			}
			e.waitAcquire = func(ctx context.Context, opts resourceguard.Options, wait time.Duration) (func(), error) {
				if !scheduled {
					t.Fatal("manual maintenance unexpectedly waited")
				}
				if wait != 6*time.Minute {
					t.Fatalf("wait=%s", wait)
				}
				return admit(ctx, opts)
			}
			_, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"grok"}, Tools: []string{"ponytail"}}, Update)
			if err != nil || calls != 1 || !released {
				t.Fatalf("err=%v calls=%d released=%v", err, calls, released)
			}
		})
	}
}

// Never format a command struct: Env and Input can contain credentials.
func commandDiagnostic(c command) string { return fmt.Sprintf("path=%q args=%q", c.Path, c.Args) }
func commandDiagnostics(commands []command) []string {
	out := make([]string, 0, len(commands))
	for _, c := range commands {
		out = append(out, commandDiagnostic(c))
	}
	return out
}
func TestCommandDiagnosticsExcludeEnvironmentAndInput(t *testing.T) {
	c := command{Path: "/fixture/tool", Args: []string{"--version"}, Env: []string{"SYNTHETIC_SECRET=must-not-log"}, Input: []byte("private-script-input")}
	out := commandDiagnostic(c)
	if strings.Contains(out, "must-not-log") || strings.Contains(out, "private-script-input") {
		t.Fatal("command diagnostics leaked environment or stdin")
	}
	if !strings.Contains(out, "/fixture/tool") || !strings.Contains(out, "--version") {
		t.Fatal("command diagnostics omitted safe path/arguments")
	}
}

func TestOpenCodeUsesCanonicalConfigHomeResolver(t *testing.T) {
	e := testEngine(t)
	e.opts.ExplicitHome = false
	t.Setenv("HOME", e.opts.HomeDir)
	xdg := filepath.Join(e.opts.HomeDir, "custom-config")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got := e.profile("opencode"); got != filepath.Join(xdg, "opencode") {
		t.Fatalf("unexpected profile %q", got)
	}
	if !slices.Contains(e.environment(), "XDG_CONFIG_HOME="+xdg) {
		t.Fatal("subprocess configuration home diverges from canonical resolver")
	}
	adopted := filepath.Join(e.opts.HomeDir, "adopted-opencode")
	t.Setenv("OPENCODE_CONFIG_DIR", adopted)
	if got := e.profile("opencode"); got != adopted {
		t.Fatalf("native override lost: %q", got)
	}
	e.opts.ExplicitHome = true
	if got := e.profile("opencode"); got != filepath.Join(e.opts.HomeDir, ".config", "opencode") {
		t.Fatalf("explicit home escaped: %q", got)
	}
}
func TestRelativeAmbientConfigHomeRejectedBeforeAdmission(t *testing.T) {
	e := testEngine(t)
	e.opts.ExplicitHome = false
	t.Setenv("HOME", e.opts.HomeDir)
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	e.acquire = func(context.Context, resourceguard.Options) (func(), error) {
		t.Fatal("admitted invalid configuration root")
		return nil, nil
	}
	_, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"codex"}}, Ensure)
	if err == nil || !strings.Contains(err.Error(), "configuration home must be absolute") {
		t.Fatalf("got %v", err)
	}
}

func TestSuffixedLatestOnlyAcceptedWhenAllowed(t *testing.T) {
	body := `{"version":"1.18.32-gencode.1"}`
	for _, allow := range []bool{false, true} {
		e := testEngine(t)
		e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		v, err := e.latestVersion(context.Background(), "https://example.invalid/latest", allow)
		if allow && (err != nil || v != "1.18.32-gencode.1") {
			t.Fatalf("allowSuffix: v=%q err=%v", v, err)
		}
		if !allow && err == nil {
			t.Fatal("strict metadata accepted a suffixed version")
		}
	}
}
func TestGencodeInstallsSuffixedVersionViaNpm(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "npm")
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"1.18.32-gencode.1"}`)), Header: make(http.Header)}, nil
	})
	var commands []command
	e.exec = func(_ context.Context, c command) (string, error) {
		commands = append(commands, c)
		if filepath.Base(c.Path) == "npm" {
			fakeBinary(t, e, "gencode")
			return "", nil
		}
		return "gencode 1.18.32-gencode.1", nil
	}
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"gencode"}}, Ensure)
	if err != nil || r.Failed != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if len(commands) != 2 || strings.Join(commands[0].Args, " ") != "install --global --prefix "+filepath.Join(e.opts.HomeDir, ".local")+" --registry=https://registry.npmjs.org @genspark/gencode@1.18.32-gencode.1" {
		t.Fatalf("unexpected command sequence %v", commandDiagnostics(commands))
	}
	if r.Items[0].Status != "installed" || r.Items[0].Installed != "1.18.32-gencode.1" {
		t.Fatal(r.Items[0])
	}
}
func TestPiInstallsViaNpmProvider(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "npm")
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"0.87.1"}`)), Header: make(http.Header)}, nil
	})
	var commands []command
	e.exec = func(_ context.Context, c command) (string, error) {
		commands = append(commands, c)
		if filepath.Base(c.Path) == "npm" {
			fakeBinary(t, e, "pi")
			return "", nil
		}
		return "pi 0.87.1", nil
	}
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"pi"}}, Ensure)
	if err != nil || r.Failed != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if len(commands) != 2 || strings.Join(commands[0].Args, " ") != "install --global --prefix "+filepath.Join(e.opts.HomeDir, ".local")+" --registry=https://registry.npmjs.org @earendil-works/pi-coding-agent@0.87.1" {
		t.Fatalf("unexpected command sequence %v", commandDiagnostics(commands))
	}
	if r.Items[0].Status != "installed" || r.Items[0].Installed != "0.87.1" {
		t.Fatal(r.Items[0])
	}
}
func TestAntigravityMetadataURLUsesRuntimePlatform(t *testing.T) {
	got := metadataURL(binarySpecs()["antigravity"])
	switch runtime.GOOS + "_" + runtime.GOARCH {
	case "darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64":
		if strings.Contains(got, "%s") || !strings.HasSuffix(got, "/manifests/"+runtime.GOOS+"_"+runtime.GOARCH+".json") {
			t.Fatal(got)
		}
	default:
		if !strings.Contains(got, "%s") {
			t.Fatal("unsupported platform must keep the unexpanded template")
		}
	}
	if got := metadataURL(binarySpecs()["codex"]); strings.Contains(got, "%s") {
		t.Fatal(got)
	}
}
func TestAntigravityDefersSelfUpdate(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "agy")
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"1.2.12"}`)), Header: make(http.Header)}, nil
	})
	e.exec = func(_ context.Context, c command) (string, error) {
		if filepath.Base(c.Path) == "agy" {
			return "agy 1.2.11", nil
		}
		t.Fatalf("unexpected mutation command: %s", commandDiagnostic(c))
		return "", nil
	}
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"antigravity"}}, Update)
	if err != nil || len(r.Items) != 1 || r.Items[0].Status != "deferred-self-update" {
		t.Fatalf("%+v %v", r, err)
	}
	if !strings.Contains(r.Items[0].Detail, "self-updates") {
		t.Fatal(r.Items[0])
	}
}
func TestAntigravityInstallsWhenMissing(t *testing.T) {
	e := testEngine(t)
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"1.2.12"}`)), Header: make(http.Header)}, nil
	})
	var commands []command
	e.exec = func(_ context.Context, c command) (string, error) {
		commands = append(commands, c)
		if c.Path == "/bin/bash" {
			fakeBinary(t, e, "agy")
			return "", nil
		}
		return "agy 1.2.12", nil
	}
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"antigravity"}}, Ensure)
	if err != nil || r.Failed != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if len(commands) != 2 || strings.Join(commands[0].Args, " ") != "-s -- --dir "+filepath.Join(e.opts.HomeDir, ".local", "bin") {
		t.Fatalf("unexpected command sequence %v", commandDiagnostics(commands))
	}
	if r.Items[0].Status != "installed" || r.Items[0].Installed != "1.2.12" {
		t.Fatal(r.Items[0])
	}
}

func TestAntigravityDefersUnavailablePin(t *testing.T) {
	e := testEngine(t)
	e.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"1.2.12"}`)), Header: make(http.Header)}, nil
	})
	e.exec = func(_ context.Context, c command) (string, error) {
		t.Fatalf("unexpected mutation command: %s", commandDiagnostic(c))
		return "", nil
	}
	r, err := e.Run(context.Background(), config.AIToolingConfig{Agents: []string{"antigravity"}, Pins: map[string]string{"antigravity": "1.0.0"}}, Ensure)
	if err != nil || len(r.Items) != 1 || r.Items[0].Status != "deferred-pin" {
		t.Fatalf("%+v %v", r, err)
	}
	if !strings.Contains(r.Items[0].Detail, "1.2.12") || !strings.Contains(r.Items[0].Detail, "1.0.0") {
		t.Fatal(r.Items[0])
	}
}
