package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// --list prints the list and --json the document; stdout never carries both.
func TestPrintPeerPlanOutputPicksOneFormat(t *testing.T) {
	plan := &syncer.PeerRunPlan{Items: []syncer.PeerPlanItem{{Path: "a.txt", Scope: syncer.PlanScopeWorkspace, Action: "create", Direction: "push"}}}
	for _, jsonOut := range []bool{false, true} {
		var out bytes.Buffer
		c := &cobra.Command{}
		c.SetOut(&out)
		if err := printPeerPlanOutput(c, printerFrom(c), plan, jsonOut); err != nil {
			t.Fatal(err)
		}
		text := out.String()
		isJSON := json.Valid(out.Bytes())
		if isJSON != jsonOut || (!jsonOut && (strings.HasPrefix(strings.TrimSpace(text), "{") || !strings.Contains(text, "a.txt"))) {
			t.Fatalf("jsonOut=%v wrote:\n%s", jsonOut, text)
		}
	}
}
