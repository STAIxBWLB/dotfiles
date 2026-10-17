package cli

import (
	"bytes"
	"errors"
	"io"
	osexec "os/exec"
	"strconv"
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func TestSyncStatusSensitiveOverridesZeroOneMany(t *testing.T) {
	render := func(overrides []syncer.SensitiveOverride) string {
		t.Helper()
		var out bytes.Buffer
		printSensitiveOverrides(&Printer{Out: &out, Err: io.Discard}, overrides, true)
		return out.String()
	}

	if got := render(nil); got != "" {
		t.Fatalf("zero overrides rendered %q", got)
	}

	one := render([]syncer.SensitiveOverride{{AllowPattern: "/.secrets/app.env", DenyPattern: "/.secrets/**"}})
	for _, want := range []string{
		"Sensitive overrides",
		"1 allow rule(s) override built-in secret exclusions",
		"/.secrets/app.env re-includes /.secrets/**",
	} {
		if !strings.Contains(one, want) {
			t.Fatalf("one override output missing %q:\n%s", want, one)
		}
	}

	many := render([]syncer.SensitiveOverride{
		{AllowPattern: "/.aws/credentials", DenyPattern: "/.aws/credentials"},
		{AllowPattern: "/.secrets/app.env", DenyPattern: "/.secrets/**"},
	})
	if !strings.Contains(many, "2 allow rule(s) override built-in secret exclusions") {
		t.Fatalf("many override count missing:\n%s", many)
	}
	if strings.Index(many, "/.aws/credentials re-includes /.aws/credentials") > strings.Index(many, "/.secrets/app.env re-includes /.secrets/**") {
		t.Fatalf("many overrides lost their sorted order:\n%s", many)
	}
}

func TestSyncStatusSensitiveOverridesOrderingAndConstructionFailure(t *testing.T) {
	var out bytes.Buffer
	p := &Printer{Out: &out, Err: io.Discard}
	p.KV("Secrets", "deny-by-default (allow.txt empty)")
	printSensitiveOverrides(p, nil, true)
	p.KV("Include file", "include.txt")
	got := out.String()
	if strings.Contains(got, "Sensitive overrides") {
		t.Fatalf("empty or failed collection must not render a partial warning:\n%s", got)
	}
	if strings.Index(got, "Secrets") > strings.Index(got, "Include file") {
		t.Fatalf("status adjacency changed:\n%s", got)
	}
}

func TestSyncSensitiveOverridesLongControlEscaping(t *testing.T) {
	long := "/.secrets/" + strings.Repeat("long-pattern-", 64) + "\x1b[31m\napp.env"
	deny := "/.secrets/**\t"
	var out bytes.Buffer
	printSensitiveOverrides(&Printer{Out: &out, Err: io.Discard}, []syncer.SensitiveOverride{{
		AllowPattern: long,
		DenyPattern:  deny,
	}}, true)
	got := out.String()
	for _, want := range []string{"long-pattern-", "\\x1b[31m", "\\napp.env", "\\t"} {
		if !strings.Contains(got, want) {
			t.Fatalf("escaped output missing %q:\n%s", want, got)
		}
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	bullet := lines[len(lines)-1]
	if strings.ContainsAny(bullet, "\x1b\n\t") {
		t.Fatalf("escaped output contains a raw terminal control byte: %q", bullet)
	}
	if strings.Contains(got, "...") {
		t.Fatalf("long output was truncated: %q", got)
	}
	if len(lines) != 2 {
		t.Fatalf("one override must occupy one logical line after its status KV, got %d lines:\n%s", len(lines), got)
	}
}

func TestSyncPushSensitiveOverridesLocalDryRun(t *testing.T) {
	var out bytes.Buffer
	config := &syncer.Config{LocalPath: "/workspace/", MirrorPath: "/mirror/", Propagation: syncer.DefaultPropagationPolicy()}
	render := renderSyncEvent(&Printer{Out: &out, Err: io.Discard}, syncRender{
		cfg:                config,
		mode:               syncer.ModeManual,
		dryRun:             true,
		sensitiveOverrides: []syncer.SensitiveOverride{{AllowPattern: "/.aws/credentials", DenyPattern: "/.aws/credentials"}},
	})
	render(syncer.SyncEvent{Kind: syncer.SyncEventPushPlanStart})
	render(syncer.SyncEvent{Kind: syncer.SyncEventDryRunNotice})
	render(syncer.SyncEvent{Kind: syncer.SyncEventPushPlanReady, PushPlan: &syncer.PushPlan{}})
	got := out.String()
	for _, want := range []string{"Push plan for", "(dry-run", "No push changes.", "Sensitive overrides: 1", "/.aws/credentials re-includes /.aws/credentials"} {
		if !strings.Contains(got, want) {
			t.Fatalf("local dry-run output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "No push changes.") > strings.Index(got, "Sensitive overrides: 1") {
		t.Fatalf("sensitive section must follow the complete local plan:\n%s", got)
	}
}

func TestSyncPushSensitiveOverridesSSHDirectDryRun(t *testing.T) {
	var out bytes.Buffer
	config := &syncer.Config{
		LocalPath:   "/workspace/",
		Target:      syncer.Target{Kind: syncer.TargetSSH, Host: "peer", Path: "/mirror"},
		Propagation: syncer.DefaultPropagationPolicy(),
	}
	render := renderSyncEvent(&Printer{Out: &out, Err: io.Discard}, syncRender{
		cfg:                config,
		mode:               syncer.ModeManual,
		dryRun:             true,
		sensitiveOverrides: []syncer.SensitiveOverride{{AllowPattern: "/.secrets/app.env", DenyPattern: "/.secrets/**"}},
	})
	render(syncer.SyncEvent{Kind: syncer.SyncEventPushSSHStart})
	got := out.String()
	for _, want := range []string{"Push /workspace/", "(dry-run", "Sensitive overrides: 1", "/.secrets/app.env re-includes /.secrets/**"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SSH dry-run output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Push /workspace/") > strings.Index(got, "(dry-run") || strings.Index(got, "(dry-run") > strings.Index(got, "Sensitive overrides: 1") {
		t.Fatalf("SSH dry-run ordering drifted:\n%s", got)
	}
}

func TestParseIntervalFlag(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"0", 0, false},
		{"15m", 900, false},
		{"900", 900, false},
		{"1h", 3600, false},
		{"5s", 0, true},
		{"10abc", 0, true},
		{"900abc", 0, true},
	}
	for _, tc := range cases {
		got, err := parseIntervalFlag(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Fatalf("parseIntervalFlag(%q) err=%v wantErr=%v", tc.raw, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("parseIntervalFlag(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestParseAutomaticModeFlag(t *testing.T) {
	for _, raw := range []string{"clean", "force"} {
		if _, err := parseAutomaticModeFlag(raw); err != nil {
			t.Fatalf("parseAutomaticModeFlag(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"manual", "bogus"} {
		if _, err := parseAutomaticModeFlag(raw); err == nil {
			t.Fatalf("parseAutomaticModeFlag(%q) should fail", raw)
		}
	}
}

func TestParseFilterMode(t *testing.T) {
	for _, raw := range []string{"include", "exclude", "INCLUDE"} {
		if _, err := syncer.ParseFilterMode(raw); err != nil {
			t.Fatalf("ParseFilterMode(%q): %v", raw, err)
		}
	}
	if _, err := syncer.ParseFilterMode("legacy"); err == nil {
		t.Fatal("ParseFilterMode(legacy) should fail")
	}
}

func TestRootRegistersSyncPrimaryAndLegacyAlias(t *testing.T) {
	root := NewRootCmd("dev", "test")
	known := knownSubcommands(root)
	for _, name := range []string{"sync", "gsync", "gdrive-sync"} {
		if !known[name] {
			t.Fatalf("knownSubcommands missing %q", name)
		}
	}

	cmd, _, err := root.Find([]string{"sync"})
	if err != nil {
		t.Fatalf("Find(sync): %v", err)
	}
	if cmd.Name() != "sync" {
		t.Fatalf("Find(sync) = %q, want sync", cmd.Name())
	}

	for _, alias := range []string{"gsync", "gdrive-sync"} {
		legacy, _, err := root.Find([]string{alias})
		if err != nil {
			t.Fatalf("Find(%s): %v", alias, err)
		}
		if legacy.Name() != "sync" {
			t.Fatalf("Find(%s) = %q, want sync", alias, legacy.Name())
		}
	}
}

func TestBareSyncPrintsHelp(t *testing.T) {
	cmd := newSyncCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bare gsync execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Run without a subcommand to print this help.",
		"Deprecated aliases: 'dot gsync', 'dot gdrive-sync'.",
		"dot sync push",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bare gsync help missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestSyncConflictsRegistersListAndPrune(t *testing.T) {
	cmd := newSyncConflictsCmd()
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"list", "prune"} {
		if !names[want] {
			t.Errorf("conflicts is missing %q subcommand", want)
		}
	}
	prune, _, err := cmd.Find([]string{"prune"})
	if err != nil {
		t.Fatalf("Find(prune): %v", err)
	}
	if prune.Flags().Lookup("older-than") == nil || prune.Flags().Lookup("all") == nil {
		t.Error("prune is missing --older-than/--all flags")
	}
}

// TestReportPushPartial pins the mirror push's treatment of rsync exit 23.
// The target is a cloud folder: files the client keeps online-only cannot be
// stat'd (macOS returns EDEADLK rather than hydrate synchronously), so rsync
// skips them and exits 23. Treating that as fatal made the scheduled push exit
// 1 on every cycle for a workspace containing any archived, never-opened file.
func TestReportPushPartial(t *testing.T) {
	p := &Printer{Out: io.Discard, Err: io.Discard}

	if got := reportPushPartial(p, nil); got != nil {
		t.Errorf("nil in, %v out", got)
	}

	partial := syncer.ClassifyRsyncError(rsyncExit(t, 23))
	if !syncer.IsPartialTransfer(partial) {
		t.Fatal("precondition: exit 23 should classify as partial")
	}
	if got := reportPushPartial(p, partial); got != nil {
		t.Errorf("partial transfer must not fail the push, got %v", got)
	}

	// A genuine failure must still fail: exit 12 is a protocol error, not a
	// skipped file, and silently succeeding there would hide a broken mirror.
	fatal := syncer.ClassifyRsyncError(rsyncExit(t, 12))
	if got := reportPushPartial(p, fatal); got == nil {
		t.Error("exit 12 must still fail the push")
	}
}

func rsyncExit(t *testing.T, code int) error {
	t.Helper()
	err := osexec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("expected exit %d", code)
	}
	return err
}

// Ctrl-C at a push prompt declines the run; any other prompt error stays one.
func TestDeclinedOnAbort(t *testing.T) {
	if ok, err := declinedOnAbort(false, huh.ErrUserAborted); ok || err != nil {
		t.Errorf("Ctrl-C = %v, %v; want a decline", ok, err)
	}
	other := errors.New("no terminal")
	if _, err := declinedOnAbort(false, other); !errors.Is(err, other) {
		t.Errorf("another prompt error = %v; want it kept", err)
	}
	if ok, err := declinedOnAbort(true, nil); !ok || err != nil {
		t.Errorf("a yes = %v, %v", ok, err)
	}
}

// Ctrl-C at either push prompt is a decline, not a failed push (#224, #231).
func TestConfirmSync_CtrlCDeclinesBothPushPrompts(t *testing.T) {
	prev := syncPrompt
	t.Cleanup(func() { syncPrompt = prev })
	syncPrompt = func(string, bool) (bool, error) { return false, huh.ErrUserAborted }
	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")
	confirm := confirmSync(cmd)
	for _, kind := range []syncer.ConfirmKind{syncer.ConfirmPushSSH, syncer.ConfirmPushPlan} {
		if ok, err := confirm(syncer.ConfirmRequest{Kind: kind}); ok || err != nil {
			t.Errorf("Ctrl-C at push prompt %d = %v, %v; want a decline", kind, ok, err)
		}
	}
}

// On an SSH target the stored shared entries are listed as inactive, in the
// form and number SharedCount uses, so the list agrees with `shared clear`.
func TestPrintInactiveShared_ListsStoredEntriesOnce(t *testing.T) {
	var out bytes.Buffer
	stored := []string{"team//ops", "team/ops", "/x", "  "}
	if err := printInactiveShared(&Printer{Out: &out, Err: &out}, stored, "peer:/work/"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"inactive for peer:/work/", `"team/ops"`, `"/x"`, `""`} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %s:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n  \""); n != len(syncer.StoredSharedEntries(stored)) {
		t.Errorf("listed %d entries, want %d:\n%s", n, len(syncer.StoredSharedEntries(stored)), got)
	}
}
