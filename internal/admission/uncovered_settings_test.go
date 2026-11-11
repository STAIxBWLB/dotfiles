package admission

import (
	"context"
	"strings"
	"testing"
)

// AC2: admission.uncovered shapes the scan. The CPU-only rule counts at the
// configured percent, 0 turns it off, ignore_names exempts a name outright, and
// enabled false skips the scan entirely.
func TestUncoveredSettingsShapeTheScan(t *testing.T) {
	rows := []processRow{
		{pid: 10, ppid: 1, name: "make", args: "make test", cpu: 5},        // shaped
		{pid: 11, ppid: 1, name: "node", args: "node server.js", cpu: 100}, // CPU only
	}
	parents := map[int]int{10: 1, 11: 1}

	cases := []struct {
		name     string
		settings UncoveredSettings
		want     []int
	}{
		{"defaults", DefaultUncoveredSettings(), []int{10, 11}},
		{"cpu rule off", UncoveredSettings{Enabled: true, CPUPercent: 0}, []int{10}},
		{"cpu rule raised", UncoveredSettings{Enabled: true, CPUPercent: 120}, []int{10}},
		{"cpu rule lowered", UncoveredSettings{Enabled: true, CPUPercent: 50}, []int{10, 11}},
		{"ignored name", UncoveredSettings{Enabled: true, CPUPercent: 100, IgnoreNames: []string{"node"}}, []int{10}},
		{"ignored shaped name", UncoveredSettings{Enabled: true, CPUPercent: 100, IgnoreNames: []string{"make"}}, []int{11}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobs, err := unownedHeavyJobs(rows, parents, 99, nil, tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, job := range jobs {
				got = append(got, job.PID)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("jobs = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("jobs = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A disabled scan reports nothing and never touches the process table.
func TestFindUncoveredDisabledSkipsTheScan(t *testing.T) {
	previous := processTable
	processTable = func(context.Context) (string, error) {
		t.Fatal("a disabled scan read the process table")
		return "", nil
	}
	defer func() { processTable = previous }()
	jobs, err := FindUncovered(context.Background(), t.TempDir(), false, nil, UncoveredSettings{Enabled: false})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("FindUncovered = %v, %v, want no jobs and no error", jobs, err)
	}
}

// AC1: with the default settings the scan behaves exactly as it did, CPU-only
// work included.
func TestFindUncoveredDefaultsKeepCPUOnlyRule(t *testing.T) {
	repo := fixtureRepository(t)
	previous := processTable
	processTable = func(context.Context) (string, error) {
		return "  4242     1  100.0 /usr/bin/node /repo/server.js\n", nil
	}
	defer func() { processTable = previous }()
	jobs, err := FindUncovered(context.Background(), repo, false, nil, DefaultUncoveredSettings())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || !strings.Contains(jobs[0], "node") {
		t.Fatalf("jobs = %v, want the CPU-only node process counted by default", jobs)
	}
}
