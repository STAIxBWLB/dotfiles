package module

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// runAllStubModule fails its Check, its Apply, or neither, so one table can
// cover both failure arms of RunAll's loop as well as the success path.
type runAllStubModule struct {
	name     string
	checkErr error
	applyErr error
}

func (s *runAllStubModule) Name() string { return s.name }

func (s *runAllStubModule) Check(_ context.Context, _ *RunContext) (*CheckResult, error) {
	if s.checkErr != nil {
		return nil, s.checkErr
	}
	return &CheckResult{Changes: []Change{{Description: "do something"}}}, nil
}

func (s *runAllStubModule) Apply(_ context.Context, _ *RunContext) (*ApplyResult, error) {
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	return &ApplyResult{Changed: true, Messages: []string{"applied"}}, nil
}

// TestRunAll_AggregatesCheckErrors pins BUG-01: a module whose Check returns an
// error must count as a failed module, so dot apply exits non-zero instead of
// reporting success for a module it could not even evaluate. The table also
// carries the Apply-failure row and a row that must return nil, so it cannot
// pass against a RunAll that returns an error unconditionally, and it asserts
// the printed line so the fix cannot quietly reword what the operator sees.
func TestRunAll_AggregatesCheckErrors(t *testing.T) {
	pinModuleReportMarkers(t)
	checkBoom := errors.New("check exploded")
	applyBoom := errors.New("apply exploded")

	tests := []struct {
		name        string
		modules     []Module
		dryRun      bool
		wantErr     bool
		wantErrHas  []string
		wantOut     []string
		wantOutNone []string
	}{
		{
			name:       "check error is aggregated",
			modules:    []Module{&runAllStubModule{name: "alpha", checkErr: checkBoom}},
			wantErr:    true,
			wantErrHas: []string{"1 module(s) failed", "alpha", "check exploded"},
			wantOut:    []string{"  ⚠  alpha: check error: check exploded\n"},
		},
		{
			name:       "apply error is aggregated",
			modules:    []Module{&runAllStubModule{name: "beta", applyErr: applyBoom}},
			wantErr:    true,
			wantErrHas: []string{"1 module(s) failed", "beta", "apply exploded"},
			wantOut:    []string{"  ✗  beta: apply exploded\n"},
		},
		{
			name:    "no failure returns nil",
			modules: []Module{&runAllStubModule{name: "gamma"}},
			wantErr: false,
			wantOut: []string{"  →  gamma: do something\n", "  ✓  gamma: applied\n"},
		},
		{
			name: "every failing module is counted",
			modules: []Module{
				&runAllStubModule{name: "alpha", checkErr: checkBoom},
				&runAllStubModule{name: "beta", checkErr: checkBoom},
			},
			wantErr:    true,
			wantErrHas: []string{"2 module(s) failed", "alpha", "beta"},
		},
		{
			name:       "dry-run still aggregates a check error",
			modules:    []Module{&runAllStubModule{name: "alpha", checkErr: checkBoom}},
			dryRun:     true,
			wantErr:    true,
			wantErrHas: []string{"1 module(s) failed", "alpha"},
		},
		{
			name:        "dry-run never reaches a failing Apply",
			modules:     []Module{&runAllStubModule{name: "beta", applyErr: applyBoom}},
			dryRun:      true,
			wantErr:     false,
			wantOutNone: []string{"apply exploded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			rc := &RunContext{Out: &buf, DryRun: tt.dryRun}

			err := RunAll(context.Background(), tt.modules, rc)

			if tt.wantErr && err == nil {
				t.Fatalf("RunAll error = nil, want non-nil\nstdout=%q", buf.String())
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("RunAll error = %v, want nil\nstdout=%q", err, buf.String())
			}
			for _, want := range tt.wantErrHas {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("RunAll error = %q, want it to contain %q", err, want)
				}
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("stdout = %q, want it to contain %q", buf.String(), want)
				}
			}
			for _, unwanted := range tt.wantOutNone {
				if strings.Contains(buf.String(), unwanted) {
					t.Errorf("stdout = %q, want it NOT to contain %q", buf.String(), unwanted)
				}
			}
		})
	}
}
