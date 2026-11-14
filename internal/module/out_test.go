package module

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// outStubModule reports a fixed set of pending changes and never applies.
type outStubModule struct {
	name    string
	changes []Change
}

func (s *outStubModule) Name() string { return s.name }

func (s *outStubModule) Check(_ context.Context, _ *RunContext) (*CheckResult, error) {
	return &CheckResult{Satisfied: len(s.changes) == 0, Changes: s.changes}, nil
}

func (s *outStubModule) Apply(_ context.Context, _ *RunContext) (*ApplyResult, error) {
	return &ApplyResult{}, nil
}

// TestRunAll_WritesToRunContextOut verifies that RunAll routes its progress
// lines through RunContext.Out when one is assigned. Without this guarantee
// the Phase 3 extractions would land on code that still writes to process
// stdout, and callers capturing output through the seam would see nothing.
func TestRunAll_WritesToRunContextOut(t *testing.T) {
	pinModuleReportMarkers(t)
	var buf bytes.Buffer
	rc := &RunContext{Out: &buf, DryRun: true}
	m := &outStubModule{name: "stub", changes: []Change{{Description: "do something"}}}

	if err := RunAll(context.Background(), []Module{m}, rc); err != nil {
		t.Fatalf("RunAll error = %v", err)
	}

	want := "  →  stub: do something\n"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("buffer = %q, want it to contain %q", buf.String(), want)
	}
}

// TestRunContextOut_NilDefaultsToStdout verifies that an unset Out falls back
// to os.Stdout, including on a nil *RunContext. Without this guarantee a
// caller that forgets to assign Out would either panic or silently discard
// the only record of what dot is about to change.
func TestRunContextOut_NilDefaultsToStdout(t *testing.T) {
	rc := &RunContext{}
	if got := rc.out(); got != os.Stdout {
		t.Errorf("zero-value out() = %v, want os.Stdout", got)
	}

	var nilRC *RunContext
	if got := nilRC.out(); got != os.Stdout {
		t.Errorf("nil receiver out() = %v, want os.Stdout", got)
	}
}

func pinModuleReportMarkers(t *testing.T) {
	t.Helper()
	restore, err := ui.SetPresentation(func(k string) string {
		if k == "LANG" {
			return "en_US.UTF-8"
		}
		return ""
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}
