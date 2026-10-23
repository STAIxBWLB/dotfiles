package aitooling

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrokNativeReadinessAndStableUpdateDeferral(t *testing.T) {
	cases := []struct{ name, body, status string }{
		{"trusted current", `[{"name":"ponytail","version":"2.0.0","source":"DietrichGebert/ponytail","trusted":true,"enabled":true}]`, "installed"},
		{"unknown trust", `[{"name":"ponytail","version":"2.0.0","source":"DietrichGebert/ponytail","enabled":true}]`, "partial"},
		{"untrusted", `[{"name":"ponytail","version":"2.0.0","source":"DietrichGebert/ponytail","trusted":false,"enabled":true}]`, "pending-trust"},
		{"old stable", `[{"name":"ponytail","version":"1.0.0","source":"DietrichGebert/ponytail","trusted":true,"enabled":true}]`, "deferred-stable-ref"},
		{"native pin", `[{"name":"ponytail","version":"1.0.0","source":"DietrichGebert/ponytail@v1.0.0","trusted":true,"enabled":true}]`, "pinned"},
		{"different source", `[{"name":"ponytail","version":"2.0.0","source":"other/ponytail","trusted":true,"enabled":true}]`, "deferred-provenance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t)
			fakeBinary(t, e, "grok")
			calls := 0
			e.exec = func(_ context.Context, c command) (string, error) {
				calls++
				if strings.Join(c.Args, " ") != "plugin list --json" {
					t.Fatalf("unsafe native command: %v", c.Args)
				}
				return tc.body, nil
			}
			r := e.grokPonytail(context.Background(), "", Update)
			if r.Status != tc.status || calls != 1 {
				t.Fatalf("%+v calls=%d", r, calls)
			}
			if tc.name == "old stable" && !strings.Contains(r.Detail, "DietrichGebert/ponytail@v2.0.0") {
				t.Fatalf("missing exact manual source: %+v", r)
			}
		})
	}
}
func writePluginFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestOpenCodeCachedPackageWithoutConfigDoesNotClaimInstalled(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "opencode")
	configPath := filepath.Join(e.profile("opencode"), "opencode.json")
	writePluginFixture(t, configPath, `{"plugin":["@dietrichgebert/ponytail"],"model":"preserved"}`)
	calls := 0
	e.exec = func(_ context.Context, c command) (string, error) {
		calls++
		if strings.Join(c.Args, " ") != "plugin @dietrichgebert/ponytail@2.0.0 --global --pure --force" {
			t.Fatalf("%v", c.Args)
		}
		writePluginFixture(t, filepath.Join(e.profile("opencode"), "node_modules", "@dietrichgebert", "ponytail", "package.json"), `{"name":"@dietrichgebert/ponytail","version":"2.0.0"}`)
		return "", nil
	}
	r := e.openCodePonytail(context.Background(), "", Update)
	if r.Status != "pending-runtime" || calls != 1 {
		t.Fatalf("%+v calls=%d", r, calls)
	}
	after, _ := os.ReadFile(configPath)
	if !strings.Contains(string(after), `"model":"preserved"`) {
		t.Fatal("unrelated native config changed")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(configPath), "opencode.json.dot-backup-*"))
	if len(backups) != 1 {
		t.Fatal("missing unique config backup")
	}
}
func TestOpenCodeChangedManagedCacheDefers(t *testing.T) {
	e := testEngine(t)
	fakeBinary(t, e, "opencode")
	writePluginFixture(t, filepath.Join(e.profile("opencode"), "opencode.json"), `{"plugin":["@dietrichgebert/ponytail"]}`)
	root := filepath.Join(e.profile("opencode"), "node_modules", "@dietrichgebert", "ponytail")
	writePluginFixture(t, filepath.Join(root, "package.json"), `{"name":"@dietrichgebert/ponytail","version":"1.0.0"}`)
	digest, err := pluginDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	e.receipts["ponytail/opencode"] = receipt{Version: "1.0.0", Integrity: digest}
	writePluginFixture(t, filepath.Join(root, "custom-user-file"), "do not delete")
	r := e.openCodePonytail(context.Background(), "", Update)
	if r.Status != "deferred-local-change" {
		t.Fatal(r)
	}
}
func TestOpenCodeExactNativeCacheAndReferenceReachInstalled(t *testing.T) {
	e := testEngine(t)
	writePluginFixture(t, filepath.Join(e.profile("opencode"), "opencode.json"), `{"plugin":["@dietrichgebert/ponytail@2.0.0"]}`)
	writePluginFixture(t, filepath.Join(e.profile("opencode"), "node_modules", "@dietrichgebert", "ponytail", "package.json"), `{"name":"@dietrichgebert/ponytail","version":"2.0.0"}`)
	r := e.openCodePonytail(context.Background(), "", Inspect)
	if r.Status != "installed" || r.Installed != "2.0.0" {
		t.Fatal(r)
	}
}
func npmArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func useNPMArchive(t *testing.T, e *Engine, archive []byte, badSRI bool) {
	t.Helper()
	sum := sha512.Sum512(archive)
	if badSRI {
		sum[0] ^= 0xff
	}
	metadata, _ := json.Marshal(map[string]any{"dist": map[string]string{"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:])}})
	e.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := metadata
		if strings.HasSuffix(req.URL.Path, ".tgz") {
			body = archive
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
}
func TestNPMCacheIntegrityRejectsTamperingAndTraversal(t *testing.T) {
	cases := []struct {
		name    string
		archive map[string]string
		extra   bool
		badSRI  bool
		want    bool
	}{
		{name: "clean", archive: map[string]string{"package/package.json": "fixture"}, want: true},
		{name: "bad SRI", archive: map[string]string{"package/package.json": "fixture"}, badSRI: true},
		{name: "extra local file", archive: map[string]string{"package/package.json": "fixture"}, extra: true},
		{name: "extra archive file", archive: map[string]string{"package/package.json": "fixture", "package/missing.js": "not installed"}},
		{name: "archive traversal", archive: map[string]string{"package/package.json": "fixture", "package/../../escape": "escape"}},
		{name: "absolute path", archive: map[string]string{"package/package.json": "fixture", "package//absolute": "escape"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t)
			root := t.TempDir()
			writePluginFixture(t, filepath.Join(root, "package.json"), "fixture")
			if tc.extra {
				writePluginFixture(t, filepath.Join(root, "user-extra"), "preserve")
			}
			useNPMArchive(t, e, npmArchive(t, tc.archive), tc.badSRI)
			if got := e.ponyCacheClean(context.Background(), root, "1.0.0"); got != tc.want {
				t.Fatalf("clean=%v want=%v", got, tc.want)
			}
		})
	}
}
