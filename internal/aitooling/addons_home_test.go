package aitooling

import (
	"context"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGSDPiExplicitHomePreservesForeignNPMPrefix(t *testing.T) {
	e := testEngine(t)
	foreign := t.TempDir()
	target := filepath.Join(foreign, "lib", "node_modules", "@opengsd", "gsd-pi", "bin", "gsd.js")
	original := []byte("foreign GSD-pi installation")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, original, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.opts.HomeDir, ".local", "bin", "gsd")
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fakeBinary(t, e, "npm")
	probes := 0
	e.exec = func(_ context.Context, c command) (string, error) {
		if c.Path != link || !reflect.DeepEqual(c.Args, []string{"--version"}) {
			t.Fatalf("foreign npm mutation attempted: %s", commandDiagnostic(c))
		}
		probes++
		return "1.0.0", nil
	}
	result := e.gsdPi(context.Background(), "", ItemResult{ID: "gsd/pi", Latest: "2.0.0"}, Update)
	if result.Status != "deferred-home" {
		t.Fatalf("unexpected status %+v", result)
	}
	if probes != 1 {
		t.Fatalf("expected one read-only version probe, got %d", probes)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != string(original) {
		t.Fatal("foreign installation modified")
	}
	if len(e.receipts) != 0 {
		t.Fatal("foreign installation adopted")
	}
}

func TestGSDPiEnsureHonorsExplicitPin(t *testing.T) {
	for _, pin := range []string{"", "1.5.0"} {
		t.Run("pin="+pin, func(t *testing.T) {
			e := testEngine(t)
			target := filepath.Join(e.opts.HomeDir, ".local", "lib", "node_modules", "@opengsd", "gsd-pi", "bin", "gsd.js")
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(e.opts.HomeDir, ".local", "bin", "gsd")
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			npm := fakeBinary(t, e, "npm")
			installs := 0
			e.exec = func(_ context.Context, c command) (string, error) {
				if c.Path == link && reflect.DeepEqual(c.Args, []string{"--version"}) {
					if installs > 0 {
						return pin, nil
					}
					return "1.0.0", nil
				}
				if c.Path == npm && pin != "" && len(c.Args) > 0 && c.Args[0] == "install" {
					if c.Args[len(c.Args)-1] != "@opengsd/gsd-pi@"+pin {
						t.Fatalf("wrong pin: %s", commandDiagnostic(c))
					}
					installs++
					return "", nil
				}
				t.Fatalf("unexpected command: %s", commandDiagnostic(c))
				return "", nil
			}
			result := e.gsdPi(context.Background(), pin, ItemResult{ID: "gsd/pi", Latest: "2.0.0"}, Ensure)
			if result.Status != "installed" {
				t.Fatalf("%+v", result)
			}
			if pin != "" && (installs != 1 || result.Installed != pin) {
				t.Fatalf("pin ignored: %+v installs=%d", result, installs)
			}
			if pin == "" && installs != 0 {
				t.Fatal("unpinned Ensure upgraded installation")
			}
		})
	}
}
func TestGSDCoreEnsureHonorsExplicitPin(t *testing.T) {
	for _, pin := range []string{"", "1.5.0"} {
		t.Run("pin="+pin, func(t *testing.T) {
			e := testEngine(t)
			root := e.profile("claude")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(root, "gsd-file-manifest.json")
			if err := os.WriteFile(manifest, []byte("{\"version\":\"1.0.0\",\"files\":{}}"), 0600); err != nil {
				t.Fatal(err)
			}
			npx := fakeBinary(t, e, "npx")
			gsd := fakeBinary(t, e, "gsd")
			installs := 0
			e.exec = func(_ context.Context, c command) (string, error) {
				if c.Path == gsd && reflect.DeepEqual(c.Args, []string{"--version"}) {
					return "2.0.0", nil
				}
				if c.Path == npx && pin != "" {
					if !strings.Contains(strings.Join(c.Args, " "), "@opengsd/gsd-core@"+pin) {
						t.Fatalf("wrong pin: %s", commandDiagnostic(c))
					}
					installs++
					return "", os.WriteFile(manifest, []byte("{\"version\":\""+pin+"\",\"files\":{}}"), 0600)
				}
				t.Fatalf("unexpected command: %s", commandDiagnostic(c))
				return "", nil
			}
			results := e.gsd(context.Background(), config.AIToolingConfig{Agents: []string{"claude"}, Pins: map[string]string{"gsd": pin}}, Ensure)
			if len(results) != 2 || results[0].Status != "installed" {
				t.Fatalf("%+v", results)
			}
			if pin != "" && (installs != 1 || results[0].Installed != pin) {
				t.Fatalf("pin ignored: %+v installs=%d", results, installs)
			}
			if pin == "" && installs != 0 {
				t.Fatal("unpinned Ensure upgraded installation")
			}
		})
	}
}
