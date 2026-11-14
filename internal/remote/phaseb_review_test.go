package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

func TestVerifyRefusesRootAndMissingImageAccountBeforePush(t *testing.T) {
	for _, tc := range []struct{ identity, want string }{
		{"root", "remote.hosts.buildbox.ssh"},
		{"missing\t1234:5678", "build the image with the host UID and GID"},
		{"unknown", "no verified passwd entry"},
	} {
		t.Run(tc.identity, func(t *testing.T) {
			f, repo := verifyFixture(t)
			f.exec.preflight = preflightAbsent + "identity\t" + tc.identity + "\n"
			err := f.verifyDeps().Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Verify = %v; want %s", err, tc.want)
			}
			if f.exec.order("push") >= 0 || f.exec.order("git init -q --bare") >= 0 {
				t.Fatal("refused identity still wrote to host")
			}
			if held := f.leases(); len(held) != 0 {
				t.Fatalf("leases left: %+v", held)
			}
		})
	}
}

// Execute the identity probe shell with stubbed host/runtime commands. No
// container daemon or real host account is used.
func TestVerifyIdentityProbeChecksHostUIDInImage(t *testing.T) {
	for _, tc := range []struct{ uid, getentExit, want string }{
		{"0", "0", "identity\troot"},
		{"1234", "0", "identity\tok"},
		{"1234", "2", "identity\tmissing\t1234:5678"},
	} {
		t.Run(tc.uid+"-"+tc.getentExit, func(t *testing.T) {
			stub := `image_known=yes; id(){ if [ "$1" = -u ]; then echo ` + tc.uid + `; else echo 5678; fi; }; docker(){ printf '%s\n' "$*" >&2; return ` + tc.getentExit + `; }; `
			script := fmt.Sprintf(verifyIdentityTemplate, "docker", ShellQuote("dev:latest"))
			cmd := osexec.Command("sh", "-c", stub+script)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("probe = %s, %v; want %s", out, err, tc.want)
			}
			// Runtime output is deliberately suppressed by the probe, so assert the
			// exact UID lookup with a stub that refuses any other argv.
			if tc.uid != "0" && tc.getentExit == "0" {
				strict := `image_known=yes; id(){ if [ "$1" = -u ]; then echo 1234; else echo 5678; fi; }; docker(){ [ "$*" = 'run --rm --network none --read-only --user 0 --entrypoint getent dev:latest passwd 1234' ]; }; `
				out, err = osexec.Command("sh", "-c", strict+script).CombinedOutput()
				if err != nil || !strings.Contains(string(out), "identity\tok") {
					t.Fatalf("wrong UID probe argv: %s, %v", out, err)
				}
			}
		})
	}
}

func TestVerifyAndSrcRecoverOnlyTheirOwnRunningContainer(t *testing.T) {
	for _, kind := range []string{"verify", "src"} {
		for _, owner := range []string{"ours", "ours-removal-failed", "foreign", "missing", "different-scope"} {
			t.Run(kind+"/"+owner, func(t *testing.T) {
				f, repo := verifyFixture(t)
				d := f.verifyDeps()
				scope := PathScope(repo)
				container := f.host.VerifyContainer("vrepo")
				if kind == "src" {
					scope = srcScope(f.host, "lib")
					container = f.host.SrcContainer("lib")
				}
				label := ""
				switch owner {
				case "ours", "ours-removal-failed":
					label = d.containerOwner(scope)
				case "foreign":
					other := d
					other.Machine = "another-machine"
					label = other.containerOwner(scope)
				case "different-scope":
					label = d.containerOwner(scope + "-other")
				}
				f.exec.preflight = "container\trunning\nimage\tknown\nuser\tdev\nidentity\tok\nowner\t" + label + "\n"
				if owner == "ours-removal-failed" {
					f.exec.removeErr = errors.New("runtime unavailable")
				}
				var err error
				if kind == "verify" {
					err = d.Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})
				} else {
					err = d.Src(context.Background(), f.cfg, SrcOptions{HostName: f.host.Name, Name: "lib", Command: []string{"true"}, Cwd: f.root})
				}
				if owner == "ours-removal-failed" {
					if err == nil || !strings.Contains(err.Error(), "could not remove") {
						t.Fatalf("failed recovery = %v", err)
					}
					if f.exec.order("push") >= 0 || f.exec.order("--label net.staix.dot.remote-owner=") >= 0 {
						t.Fatal("started new job after failed recovery")
					}
					return
				}
				if owner == "ours" {
					if err != nil {
						t.Fatal(err)
					}
					if len(f.exec.removals()) != 1 || !strings.Contains(f.exec.removals()[0], container) {
						t.Fatalf("removals = %v", f.exec.removals())
					}
					if f.exec.order("--label net.staix.dot.remote-owner="+label) < 0 {
						t.Fatal("new container has no ownership label")
					}
				} else {
					var deferred *Deferred
					if !errors.As(err, &deferred) {
						t.Fatalf("foreign/unknown owner = %v; want defer", err)
					}
					if len(f.exec.removals()) != 0 {
						t.Fatal("removed foreign container")
					}
				}
			})
		}
	}
}

func TestPreflightReadsOwnershipLabel(t *testing.T) {
	script := fmt.Sprintf(preflightTemplate, "job", "docker", "dev")
	for _, owner := range []string{"ours", "foreign", ""} {
		stub := `docker(){ case "$1" in ps) echo job;; inspect) printf '%s\n' '` + owner + `';; image) echo dev;; *) return 1;; esac; }; `
		out, err := osexec.Command("sh", "-c", stub+script).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "owner\t"+owner+"\n") {
			t.Fatalf("owner %q = %s, %v", owner, out, err)
		}
	}
}

func TestSnapshotDirtyExcludesMatchRunRsyncFilters(t *testing.T) {
	f := newSyncFixture(t)
	repo := newRepo(t, f.root, "snapshot")
	paths := []string{".env", "sub/.env.local", "sub/private.key", "a.pem", ".dev.vars", "sub/.agenthub/state.json", ".maru/cache", "target/out", "sub/node_modules/lib/x", ".claude/worktrees/w/x", "sub/.codex/worktrees/w/y", ".env.example", "sub/.env.example", "normal.txt", " space\nfile.txt", ".env.example/sub/.env", ".env.example/ok"}
	// The same fixture cannot use .env.example as both a file and directory.
	paths = slices.DeleteFunc(paths, func(s string) bool { return s == ".env.example" })
	for _, rel := range paths {
		write(t, filepath.Join(repo, rel), "fixture\n")
	}
	// A tracked excluded path is removed from the snapshot too.
	gitRun(t, repo, "add", ".env")
	gitRun(t, repo, "commit", "-qm", "tracked excluded fixture")
	sha, err := SnapshotCommit(context.Background(), repo, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := gitInInput(context.Background(), repo, nil, nil, "ls-tree", "-rz", "--name-only", sha)
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.Split(string(raw), "\x00")
	dest := filepath.Join(f.root, "filtered")
	args := append([]string{"-r"}, ExcludeArgs(BuiltinExcludes())...)
	args = append(args, repo+"/", dest+"/")
	if out, err := osexec.Command(f.rsync, args...).CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v: %s", err, out)
	}
	for _, rel := range paths {
		_, err := os.Stat(filepath.Join(dest, rel))
		want := err == nil
		if slices.Contains(tree, rel) != want {
			t.Errorf("snapshot %q included=%v, rsync included=%v", rel, slices.Contains(tree, rel), want)
		}
	}
	if !slices.Contains(tree, "sub/.env.example") || slices.Contains(tree, ".env") {
		t.Fatalf("template/secret filter failed: %q", tree)
	}
}

func TestVerifyInterruptedPushKeepsSignalStatusAndCleansRefs(t *testing.T) {
	f, repo := verifyFixture(t)
	f.cfg.Projects[0].Base = "main"
	d := f.verifyDeps()
	original := d.Exec
	d.Exec = func(ctx context.Context, argv []string, in io.Reader, out, errOut io.Writer) (int, syscall.Signal, error) {
		if hasArg(argv, "push") {
			return execAttached(ctx, []string{"sh", "-c", "kill -INT $$"}, in, out, errOut)
		}
		return original(ctx, argv, in, out, errOut)
	}
	err := d.Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})
	var code *ExitCode
	if !errors.As(err, &code) || code.Code != 130 {
		t.Fatalf("Verify = %v; want 130", err)
	}
	deletes := 0
	for _, argv := range f.exec.calls {
		if strings.Contains(argv[len(argv)-1], "update-ref -d") {
			deletes++
		}
	}
	if deletes != 2 {
		t.Fatalf("deleted %d refs; want target and base", deletes)
	}
	if f.exec.order("/git/repo.git:ro") >= 0 {
		t.Fatal("started container after interrupted push")
	}
	if len(f.leases()) != 0 {
		t.Fatal("interrupted push left leases")
	}
}

func TestActiveRunsUsesRecordedVerifyAndSrcContainers(t *testing.T) {
	h := testHost()
	leases := []admission.Lease{
		{Class: admission.ClassRemote, Host: h.Name, Scope: "/home/u/one", Container: h.VerifyContainer("one"), Session: "existing session"},
		{Class: admission.ClassRemote, Host: h.Name, Scope: srcScope(h, "lib"), Container: h.SrcContainer("lib")},
		{Class: admission.ClassRemote, Host: h.Name, Scope: srcScope(h, "")},
	}
	runs := activeRuns(statusConfig("/home/u"), h, leases, time.Now())
	if len(runs) != 3 || runs[0].Container != h.VerifyContainer("one") || runs[0].Session != "existing session" || runs[1].Container != h.SrcContainer("lib") || runs[2].Container != h.SrcContainer("") {
		t.Fatalf("active runs = %+v", runs)
	}
}

func TestAdmitPersistsContainerOnOwnLeaseOnly(t *testing.T) {
	f := newRunFixture(t)
	d := f.deps()
	container := f.host.VerifyContainer("one")
	held, err := d.admit(context.Background(), f.host, admitOptions{Scope: PathScope(f.checkout), Cwd: f.checkout, Container: container})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	leases, err := f.store.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 {
		t.Fatalf("leases = %+v", leases)
	}
	for _, lease := range leases {
		if lease.Scope == PathScope(f.checkout) && lease.Container != container {
			t.Fatalf("path lease lost container: %+v", lease)
		}
		if lease.Scope == HostScope(f.host.Name) && lease.Container != "" {
			t.Fatalf("host slot has container: %+v", lease)
		}
	}
	other := d
	other.Store = admission.NewStore(filepath.Join(t.TempDir(), "other-store"), d.Runner)
	if d.containerOwner(PathScope(f.checkout)) == other.containerOwner(PathScope(f.checkout)) {
		t.Fatal("independent admission stores share ownership")
	}
}
