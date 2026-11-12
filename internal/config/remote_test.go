package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The remote block is part of the state document, so `dot config export`
// carries it and an unset block never renders. Both halves matter: a block that
// did not survive export would have to be retyped on every machine.
func TestRemoteBlockRoundTripsThroughTheStateDocument(t *testing.T) {
	maxRuns := 2
	ratio := 0.7
	state := UserState{
		Name:          "u",
		SchemaVersion: 0,
		Remote: UserRemoteState{
			Default:      "buildbox",
			LocalRoot:    "~/workspace",
			ExcludeExtra: []string{"datasets"},
			Hosts: map[string]UserRemoteHost{
				"buildbox": {
					SSH:          "buildbox",
					Runtime:      "docker",
					Prefix:       "me-rdev",
					Root:         "dot-remote",
					GitRoot:      "dot-remote-git",
					SrcRoot:      "dot-remote-src",
					CPUs:         16,
					Memory:       "32g",
					MaxRuns:      &maxRuns,
					MaxLoadRatio: &ratio,
					Image:        UserRemoteImage{Dockerfile: "~/images/dev.Dockerfile"},
					Caches:       map[string]string{"cargo-registry": "/usr/local/cargo/registry"},
					Env:          map[string]string{"LANG": "C.UTF-8"},
					Jobs: map[string]UserRemoteJob{"media": {
						Root: "/data/me/jobs", Image: UserRemoteImage{Ref: "ghcr.io/org/media:1"}, MaxJobs: &maxRuns,
						Env: map[string]string{"WORKER_THREADS": "{half_cpus}"}, Fallback: "local",
						Local: UserRemoteJobLocal{Root: "~/scratch/jobs", Path: []string{"~/tools/bin"}},
					}},
				},
			},
			Projects: map[string]UserRemoteProject{
				"~/workspace/work/dev/one": {Verify: "make check", Base: "origin/main", Host: "buildbox"},
			},
		},
	}
	data, err := MarshalState(&state)
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"remote:",
		"default: buildbox",
		"local_root: ~/workspace",
		"max_runs: 2",
		"max_load_ratio: 0.7",
		"cargo-registry: /usr/local/cargo/registry",
		"origin/main",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the exported document does not carry %q:\n%s", want, text)
		}
	}

	var back UserState
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("decoding the exported document: %v", err)
	}
	if back.Remote.Default != "buildbox" || len(back.Remote.Hosts) != 1 {
		t.Fatalf("remote block did not survive: %+v", back.Remote)
	}
	host := back.Remote.Hosts["buildbox"]
	if host.MaxRuns == nil || *host.MaxRuns != 2 || host.MaxLoadRatio == nil || *host.MaxLoadRatio != 0.7 {
		t.Errorf("pointers did not survive: %+v", host)
	}
	if host.Image.Dockerfile != "~/images/dev.Dockerfile" {
		t.Errorf("dockerfile = %q", host.Image.Dockerfile)
	}
	project := back.Remote.Projects["~/workspace/work/dev/one"]
	if job := host.Jobs["media"]; job.Root != "/data/me/jobs" || job.MaxJobs == nil || *job.MaxJobs != 2 ||
		job.Env["WORKER_THREADS"] != "{half_cpus}" || job.Local.Root != "~/scratch/jobs" || len(job.Local.Path) != 1 {
		t.Errorf("job profile did not survive: %+v", job)
	}
	if project.Verify != "make check" || project.Host != "buildbox" {
		t.Errorf("project = %+v", project)
	}
}

// An unset block is omitted, so a state file that never used remote work does
// not grow a section for it.
func TestRemoteBlockIsOmittedWhenUnset(t *testing.T) {
	data, err := MarshalState(&UserState{Name: "u"})
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	if strings.Contains(string(data), "remote:") {
		t.Errorf("an unset remote block rendered:\n%s", data)
	}
	if !(UserRemoteState{}).IsZero() {
		t.Error("the zero remote block does not report itself empty")
	}
	if (UserRemoteHost{Image: UserRemoteImage{Ref: "x"}}).IsZero() {
		t.Error("a host with an image reports itself empty")
	}
}
