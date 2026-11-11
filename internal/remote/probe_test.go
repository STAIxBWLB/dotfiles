package remote

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

func TestParseLoad(t *testing.T) {
	load, err := ParseLoad("7.50 4.00 2.00 2/512 4242\n16\n")
	if err != nil {
		t.Fatalf("ParseLoad: %v", err)
	}
	if load.Load1 != 7.5 || load.CPUs != 16 {
		t.Errorf("load = %+v, want 7.5 on 16 CPUs", load)
	}
	if !load.Available() {
		t.Error("a read host is reported unavailable")
	}
	if got := load.Ratio(); got != 7.5/16 {
		t.Errorf("ratio = %v, want %v", got, 7.5/16)
	}
	if got := load.String(); !strings.Contains(got, "load1 7.50 on 16 CPUs (0.47)") {
		t.Errorf("String = %q", got)
	}
	// nproc sometimes answers with surrounding whitespace on one line.
	if load, err := ParseLoad("  1.00 1.00 1.00 1/10 9\n 4 \n"); err != nil || load.CPUs != 4 {
		t.Errorf("ParseLoad with padded output = %+v, %v", load, err)
	}
}

func TestParseLoadRejectsIncompleteTelemetry(t *testing.T) {
	cases := map[string]string{
		"load only": "0.50 0.40 0.30 1/100 4242\n",
		"cpus only": "\n8\n",
		"no load":   "not-a-number 0.40 0.30 1/100 4242\n8\n",
		"no cpus":   "0.50 0.40 0.30 1/100 4242\nnot-a-count\n",
		"zero cpus": "0.50 0.40 0.30 1/100 4242\n0\n",
		"nothing":   "",
	}
	for name, out := range cases {
		if _, err := ParseLoad(out); err == nil {
			t.Errorf("ParseLoad accepted %s (%q)", name, out)
		}
	}
}

func TestLoadDefer(t *testing.T) {
	host := testHost()
	host.MaxLoadRatio = 0

	if reason := host.LoadDefer(HostLoad{Load1: 100, CPUs: 4}, false); reason != "" {
		t.Errorf("a host with no ratio deferred: %q", reason)
	}

	host.MaxLoadRatio = 0.7
	if reason := host.LoadDefer(HostLoad{Load1: 2.5, CPUs: 4}, false); reason != "" {
		t.Errorf("load1 2.5 on 4 CPUs deferred under 0.7: %q", reason)
	}
	// Exactly at the limit defers: the ratio is a ceiling, not a threshold to
	// cross.
	reason := host.LoadDefer(HostLoad{Load1: 2.8, CPUs: 4}, false)
	if !strings.Contains(reason, "host load1 2.80 on 4 CPUs is at or over 0.7 x its CPUs") {
		t.Errorf("reason = %q", reason)
	}
	if !strings.Contains(reason, "remote.hosts.buildbox.max_load_ratio") {
		t.Errorf("reason = %q, want it to name the key to change", reason)
	}
	// The bypass wins over both a loaded host and an unreadable one.
	if reason := host.LoadDefer(HostLoad{Load1: 99, CPUs: 4}, true); reason != "" {
		t.Errorf("the bypass still deferred: %q", reason)
	}
	if reason := host.LoadDefer(HostLoad{Err: "ssh timed out"}, true); reason != "" {
		t.Errorf("the bypass still deferred an unreadable host: %q", reason)
	}
	missing := host.LoadDefer(HostLoad{Err: "ssh timed out after 30s"}, false)
	if !strings.Contains(missing, "missing telemetry is not health") {
		t.Errorf("unreadable host reason = %q", missing)
	}
}

func TestProbeLoadReadsBothNumbers(t *testing.T) {
	fake := &fakeExec{t: t, loads: "4.25 3.00 2.00 1/200 77\n8\n"}
	var out, errOut bytes.Buffer
	deps := Deps{Exec: fake.execute, Out: &out, Err: &errOut}
	load := deps.ProbeLoad(context.Background(), testHost())
	if !load.Available() || load.Load1 != 4.25 || load.CPUs != 8 {
		t.Fatalf("ProbeLoad = %+v", load)
	}
	if got := len(fake.calls); got != 1 {
		t.Fatalf("the probe sent %d commands, want one round trip", got)
	}
	argv := fake.calls[0]
	if argv[0] != "ssh" || argv[len(argv)-1] != loadProbeCommand {
		t.Errorf("probe argv = %v", argv)
	}
}

func TestProbeLoadReportsAnUnreachableHost(t *testing.T) {
	fake := &fakeExec{t: t, probeErr: errors.New("ssh: connect to host buildbox port 22: Connection refused")}
	deps := Deps{Exec: fake.execute}
	load := deps.ProbeLoad(context.Background(), testHost())
	if load.Available() {
		t.Fatalf("an unreachable host read as available: %+v", load)
	}
	if !strings.Contains(load.Err, "Connection refused") {
		t.Errorf("Err = %q", load.Err)
	}
	if got := load.String(); !strings.HasPrefix(got, "unavailable (") {
		t.Errorf("String = %q", got)
	}
}

// The lease class is what makes `dot admit` and `dot remote` share one store:
// the class name and the host stamp are the contract between them.
func TestRemoteClassIsTheAdmissionClassName(t *testing.T) {
	if admission.ClassRemote != "remote" {
		t.Errorf("ClassRemote = %q, want \"remote\"", admission.ClassRemote)
	}
}
