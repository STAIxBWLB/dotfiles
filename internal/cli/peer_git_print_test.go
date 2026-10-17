package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// The preview prints each no-match class with its suggestion, and a rescue
// move with its branch (#178).
func TestPrintPeerGitRepos_ClassesAndRescue(t *testing.T) {
	res := &syncer.GitStateResult{Root: "/w", Repos: []*syncer.GitRepoReport{
		{Path: "vault", Status: syncer.GitRepoNoMatch, Class: syncer.GitClassAtTip, Suggestion: "at origin/main; only uncommitted changes differ"},
		{Path: "sites/x", Status: syncer.GitRepoRealignable, Head: "aaaa", Target: "bbbb", Rescue: "rescue/260928-main"},
	}}
	var out bytes.Buffer
	printPeerGitRepos(&Printer{Out: &out}, res, true)
	for _, want := range []string{"at-tip: at origin/main; only uncommitted changes differ", "rescue: rescue/260928-main keeps aaaa"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// `peer git status` prints a no-match repo's tie line, which its reason
// points at (#189 round 15); an aligned repo's stays in realign and --json.
// A target a --candidate-refs pattern offered names its ref; others do
// not (#217).
func TestPrintPeerGitRepos_NamesACandidateRef(t *testing.T) {
	res := &syncer.GitStateResult{Root: "/w", Repos: []*syncer.GitRepoReport{
		{Path: "dev", Status: syncer.GitRepoRealignable, Head: "aaaa", Target: "bbbb", TargetRef: "refs/peer/m3/heads/main"},
		{Path: "vault", Status: syncer.GitRepoRealignable, Head: "cccc", Target: "dddd"},
	}}
	var out bytes.Buffer
	printPeerGitRepos(&Printer{Out: &out}, res, true)
	if !strings.Contains(out.String(), "aaaa -> bbbb (from refs/peer/m3/heads/main)") || strings.Count(out.String(), "(from ") != 1 {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestPrintPeerGitRepos_StatusShowsANoMatchTie(t *testing.T) {
	res := &syncer.GitStateResult{Root: "/w", Repos: []*syncer.GitRepoReport{
		{Path: "dev", Status: syncer.GitRepoNoMatch, Reason: "HEAD wins the tie with its descendants (see tie)", TieBreak: "HEAD and 1 candidate(s) tie on content; ..."},
		{Path: "vault", Status: syncer.GitRepoAligned, TieBreak: "aligned tie"},
	}}
	var out bytes.Buffer
	printPeerGitRepos(&Printer{Out: &out}, res, false)
	if !strings.Contains(out.String(), "tie: HEAD and 1 candidate(s) tie on content") || strings.Contains(out.String(), "aligned tie") {
		t.Errorf("output:\n%s", out.String())
	}
}

// The closing hint repeats --no-push, so following it does not push the
// rescue the preview showed as staying local (#204).
func TestRealignNextKeepsNoPush(t *testing.T) {
	for _, tc := range []struct {
		realigned, realignable int
		dryRun, rescue, noPush bool
		want                   string
	}{
		{0, 1, false, true, true, "Run with --rescue --no-push --apply to realign."},
		{0, 1, false, true, false, "Run with --rescue --apply to realign."},
		{0, 1, false, false, false, "Run with --apply to realign."},
		{0, 1, true, true, true, "--dry-run: nothing changed. Re-run without it to apply."},
		{1, 0, false, true, true, ""},
		{0, 0, false, true, true, ""},
	} {
		if got := realignNext(tc.realigned, tc.realignable, tc.dryRun, tc.rescue, tc.noPush, nil); got != tc.want {
			t.Errorf("realignNext(%+v) = %q, want %q", tc, got, tc.want)
		}
	}
}

// The hint keeps --candidate-refs, whose candidates the preview may have
// moved to (#217).
func TestRealignNextKeepsCandidateRefs(t *testing.T) {
	got := realignNext(0, 1, false, false, false, []string{"refs/peer/m3/", "refs/peer/*/heads/x y"})
	if want := "Run with --candidate-refs refs/peer/m3/ --candidate-refs 'refs/peer/*/heads/x y' --apply to realign."; got != want {
		t.Errorf("realignNext = %q, want %q", got, want)
	}
}
