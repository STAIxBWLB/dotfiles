package aisession

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
)

func testKnowledgeGrants(agent string) []aipolicy.KnowledgeApproval {
	server, plugin := "amem", ""
	return []aipolicy.KnowledgeApproval{
		{Server: "obsidian", Tool: "read_note", ApprovalMode: "approve"},
		{Server: "obsidian", Tool: "write_note", ApprovalMode: "approve"},
		{Server: server, Plugin: plugin, Tool: "amem_search", ApprovalMode: "approve"},
		{Server: server, Plugin: plugin, Tool: "amem_work_state_write", ApprovalMode: "approve"},
	}
}
func TestKnowledgeSemanticRetention(t *testing.T) {
	claude := aipolicy.Resolution{Agent: "claude", KnowledgeApprovals: testKnowledgeGrants("claude")}
	codex := aipolicy.Resolution{Agent: "codex", KnowledgeApprovals: testKnowledgeGrants("codex")}
	if err := RetainKnowledge(claude, codex); err != nil {
		t.Fatal(err)
	}
	if err := RetainKnowledge(codex, claude); err != nil {
		t.Fatal(err)
	}
	if err := RetainKnowledge(aipolicy.Resolution{Agent: "claude"}, codex); err != nil {
		t.Fatalf("no blanket grant requirement: %v", err)
	}
	if err := RetainKnowledge(codex, aipolicy.Resolution{Agent: "codex"}); err == nil {
		t.Fatal("empty replacement accepted")
	}
}
func TestReducedKnowledgeStopsBeforeBudgetAndLaunch(t *testing.T) {
	for _, removed := range []string{"write_note", "amem_work_state_write", "all"} {
		t.Run(removed, func(t *testing.T) {
			o, home := fixture(t, "claude")
			o.Resolution.KnowledgeApprovals = testKnowledgeGrants("claude")
			o.MaxSwitches = 0
			o.Resolve = func(string) (aipolicy.Resolution, error) {
				next := o.Resolution
				next.Agent = "codex"
				next.KnowledgeApprovals = nil
				if removed != "all" {
					for _, grant := range testKnowledgeGrants("codex") {
						if grant.Tool != removed {
							next.KnowledgeApprovals = append(next.KnowledgeApprovals, grant)
						}
					}
				}
				return next, nil
			}
			c := cp()
			c.Scope = "Preserve task authority and knowledge permissions"
			o.Input = turns(t, Turn{Task: "first"}, Turn{Task: "continue", Workload: "analysis", Checkpoint: c})
			r, err := Run(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), "reconnect") || strings.Contains(err.Error(), "budget") || r.Switches != 0 {
				t.Fatalf("wrong failure: %+v %v", r, err)
			}
			argv, _ := os.ReadFile(filepath.Join(home, "argv"))
			if strings.Count(string(argv), "\n") != 1 {
				t.Fatalf("launched replacement: %s", argv)
			}
		})
	}
}
func TestKnowledgeNormalizationRejectsUntrustedBindings(t *testing.T) {
	for _, grant := range []aipolicy.KnowledgeApproval{
		{Server: "other-memory", Tool: "amem_search", ApprovalMode: "approve"},
		{Server: "obsidian", Tool: "delete_note", ApprovalMode: "approve"},
		{Server: "obsidian", Tool: "read_note", ApprovalMode: "ask"},
	} {
		if err := RetainKnowledge(aipolicy.Resolution{Agent: "claude"}, aipolicy.Resolution{Agent: "claude", KnowledgeApprovals: []aipolicy.KnowledgeApproval{grant}}); err == nil {
			t.Fatalf("invalid grant accepted: %+v", grant)
		}
	}
}
