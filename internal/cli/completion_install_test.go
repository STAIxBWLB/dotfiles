package cli

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestPatchZshForAliases_AddsRootAliasesToCompdef(t *testing.T) {
	root := &cobra.Command{Use: "dot", Aliases: []string{"dotfiles", "d"}}
	root.AddCommand(&cobra.Command{Use: "apply"})

	var buf bytes.Buffer
	if err := root.GenZshCompletion(&buf); err != nil {
		t.Fatalf("GenZshCompletion: %v", err)
	}

	patched := patchZshForAliases(buf.Bytes(), root.Name(), root.Aliases)

	if !bytes.Contains(patched, []byte("compdef _dot dot dotfiles d\n")) {
		t.Errorf("patched script missing alias compdef line; got snippet:\n%s",
			snippetAround(patched, "compdef"))
	}
	if !bytes.Contains(patched, []byte("#compdef dot dotfiles d\n")) {
		t.Errorf("header missing alias; got snippet:\n%s", snippetAround(patched, "#compdef"))
	}
}

func TestPatchZshForAliases_NoOpWhenMarkerAbsent(t *testing.T) {
	// If cobra ever changes its output format, the patcher must not corrupt
	// the script — better to leave it as-is and ship slightly-degraded
	// completion than break installation entirely.
	weird := []byte("#! /bin/zsh\n# unfamiliar format\n")
	out := patchZshForAliases(weird, "dot", []string{"dotfiles"})
	if !bytes.Equal(weird, out) {
		t.Errorf("patcher mutated script lacking the expected marker; got:\n%s", out)
	}
}

func TestPatchBashForAliases_RegistersRootAliases(t *testing.T) {
	root := &cobra.Command{Use: "dot", Aliases: []string{"dotfiles", "d"}}
	root.AddCommand(&cobra.Command{Use: "apply"})

	var buf bytes.Buffer
	if err := root.GenBashCompletionV2(&buf, true); err != nil {
		t.Fatalf("GenBashCompletionV2: %v", err)
	}

	patched := patchBashForAliases(buf.Bytes(), root.Name(), root.Aliases)
	if !bytes.Contains(patched, []byte("complete -o default -F __start_dot dotfiles d")) {
		t.Errorf("patched bash completion missing alias registration; got tail:\n%s",
			tailString(patched, 400))
	}
	if !bytes.Contains(patched, []byte("complete -o default -F __start_dot dot")) {
		t.Errorf("patched bash dropped the canonical registration; got tail:\n%s",
			tailString(patched, 400))
	}
	// Mode line must remain at the very end.
	if !bytes.HasSuffix(bytes.TrimRight(patched, "\n"), []byte("filetype=sh")) {
		t.Errorf("mode line not preserved at EOF; tail:\n%s", tailString(patched, 80))
	}
}

func TestCompletionAliases_DedupesRootName(t *testing.T) {
	got := completionAliases("dot", []string{"dotfiles", "dot", "", "dotfiles", "d"})
	want := []string{"dotfiles", "d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("completionAliases = %v, want %v", got, want)
	}
}

func TestInstallCompletions_WritesFilesAndIsIdempotent(t *testing.T) {
	root := &cobra.Command{Use: "dot", Aliases: []string{"dotfiles"}}
	root.AddCommand(&cobra.Command{Use: "apply"})
	root.AddCommand(&cobra.Command{Use: "gsync"})

	home := t.TempDir()
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))

	changed, err := installCompletions(root, runner, home)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !changed {
		t.Error("first install should report changed=true on a fresh home")
	}

	dir := filepath.Join(home, ".local", "share", "dotfiles", "completions")
	for _, name := range []string{"_dot", "dot.bash"} {
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
			continue
		}
		if fi.Size() == 0 {
			t.Errorf("%s was written empty", path)
		}
	}

	// Second install with no changes should report changed=false.
	changed, err = installCompletions(root, runner, home)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Error("second install reported changed=true despite no input drift")
	}
}

func TestInstallCompletions_DelegatingScriptShape(t *testing.T) {
	// Cobra's generated completion is a thin delegating wrapper that
	// queries the live binary at tab time, so the *script content* is
	// independent of the subcommand list. The on-disk file therefore only
	// needs refreshing when the binary itself (or cobra version) changes.
	// Verify the installed scripts have the runtime-delegation shape.
	root := &cobra.Command{Use: "dot", Aliases: []string{"dotfiles"}}
	root.AddCommand(&cobra.Command{Use: "apply"})

	home := t.TempDir()
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := installCompletions(root, runner, home); err != nil {
		t.Fatalf("install: %v", err)
	}

	zshBody, err := os.ReadFile(filepath.Join(home, ".local", "share", "dotfiles", "completions", "_dot"))
	if err != nil {
		t.Fatalf("read zsh: %v", err)
	}
	if !strings.Contains(string(zshBody), "__complete") {
		t.Errorf("zsh script missing runtime delegation marker (__complete)")
	}

	bashBody, err := os.ReadFile(filepath.Join(home, ".local", "share", "dotfiles", "completions", "dot.bash"))
	if err != nil {
		t.Fatalf("read bash: %v", err)
	}
	if !strings.Contains(string(bashBody), "__complete") {
		t.Errorf("bash script missing runtime delegation marker (__complete)")
	}
}

func snippetAround(haystack []byte, needle string) string {
	i := bytes.Index(haystack, []byte(needle))
	if i < 0 {
		return "(needle not found)"
	}
	start := i - 80
	if start < 0 {
		start = 0
	}
	end := i + 200
	if end > len(haystack) {
		end = len(haystack)
	}
	return string(haystack[start:end])
}

func tailString(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}
