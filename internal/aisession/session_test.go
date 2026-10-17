package aisession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
)

func fixture(t *testing.T, agent string) (Options, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	binary := filepath.Join(t.TempDir(), agent)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/argv"
cat > "$HOME/input"
if [ -e "$HOME/fail" ]; then exit 2; fi
`
	if agent == "claude" {
		script += `printf '%s\n' '{"type":"result","subtype":"success","session_id":"native-123"}'
`
	} else {
		script += `printf '%s\n' '{"type":"thread.started","thread_id":"native-123"}' '{"type":"turn.completed"}'
`
	}
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return Options{Home: home, WorkDir: dir, Resolution: aipolicy.Resolution{Eligible: true, HomeMode: "pinned", Agent: agent, Version: "known", Executable: binary, Home: filepath.Join(home, "."+agent), PolicyRevision: "1", Model: "balanced", Effort: "medium", LaunchArgs: []string{"--model", "balanced"}}, MaxSwitches: 2, Validate: func(context.Context, aipolicy.Resolution) error { return nil }}, home
}
func turns(t *testing.T, values ...Turn) *strings.Reader {
	t.Helper()
	var b bytes.Buffer
	for _, v := range values {
		if err := json.NewEncoder(&b).Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	return strings.NewReader(b.String())
}
func cp() *Checkpoint {
	return &Checkpoint{Summary: "작업 완료, 외부 전송 없음", Verification: "checked", CompletedEffects: []string{"none"}}
}
func TestNativeResumeAndPrivateReceipts(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			o, home := fixture(t, agent)
			o.Input = turns(t, Turn{Task: "한국어 첫 작업"}, Turn{Task: "한국어 이어서", Checkpoint: cp()})
			r, err := Run(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if r.Turns != 2 || r.NativeSession != "native-123" || r.Status != "completed" {
				t.Fatalf("%+v", r)
			}
			args, _ := os.ReadFile(filepath.Join(home, "argv"))
			if !strings.Contains(string(args), "resume native-123") {
				t.Fatalf("no native resume: %s", args)
			}
			prompt, _ := os.ReadFile(filepath.Join(home, "input"))
			if string(prompt) != "한국어 이어서" {
				t.Fatalf("prompt changed: %s", prompt)
			}
			data, _ := os.ReadFile(r.Path)
			if bytes.Contains(data, []byte("한국어")) {
				t.Fatal("raw prompt in receipt")
			}
			st, _ := os.Stat(r.Path)
			if st.Mode().Perm() != 0600 {
				t.Fatal("receipt not private")
			}
		})
	}
}
func TestBoundaryStopsWithoutSecondLaunch(t *testing.T) {
	for _, kind := range []string{"pending", "denied", "missing", "invalid-path", "resume-failure"} {
		t.Run(kind, func(t *testing.T) {
			o, home := fixture(t, "claude")
			c := cp()
			switch kind {
			case "pending":
				c.Pending = []string{"send status unknown"}
			case "denied":
				c.ApprovalDenied = true
			case "missing":
				c = nil
			case "invalid-path":
				c.Files = []string{"../outside"}
			}
			o.Input = turns(t, Turn{Task: "first"}, Turn{Task: "next", Checkpoint: c, Workload: "analysis"})
			o.Resolve = func(string) (aipolicy.Resolution, error) {
				if kind == "resume-failure" {
					_ = os.WriteFile(filepath.Join(home, "fail"), nil, 0600)
				}
				return o.Resolution, nil
			}
			_, err := Run(context.Background(), o)
			if err == nil {
				t.Fatal("expected stop")
			}
			args, _ := os.ReadFile(filepath.Join(home, "argv"))
			want := 1
			if kind == "resume-failure" {
				want = 2
			}
			if strings.Count(string(args), "\n") != want {
				t.Fatalf("unexpected retries %s", args)
			}
		})
	}
}
func TestSwitchBudgetAndOverride(t *testing.T) {
	for _, freeze := range []bool{false, true} {
		t.Run(map[bool]string{true: "override", false: "budget"}[freeze], func(t *testing.T) {
			o, home := fixture(t, "codex")
			o.UserOverride = freeze
			n := 0
			o.Resolve = func(string) (aipolicy.Resolution, error) {
				n++
				r := o.Resolution
				r.Model = strings.Repeat("m", n+1)
				r.LaunchArgs = []string{"--model", r.Model}
				return r, nil
			}
			o.Input = turns(t, Turn{Task: "first"}, Turn{Task: "two", Workload: "analysis", Checkpoint: cp()}, Turn{Task: "three", Workload: "analysis", Checkpoint: cp()}, Turn{Task: "four", Workload: "analysis", Checkpoint: cp()})
			r, err := Run(context.Background(), o)
			if freeze {
				if err != nil || n != 0 || r.Switches != 0 {
					t.Fatalf("override: %+v %v", r, err)
				}
			} else {
				if err == nil || r.Switches != 2 {
					t.Fatalf("budget: %+v %v", r, err)
				}
				data, _ := os.ReadFile(filepath.Join(home, "argv"))
				if strings.Count(string(data), "\n") != 3 {
					t.Fatal("launched after budget")
				}
			}
		})
	}
}
func TestForeignHomeAndValidation(t *testing.T) {
	o, home := fixture(t, "codex")
	o.Home = t.TempDir()
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("foreign home accepted")
	}
	o.Home = home
	o.Validate = func(context.Context, aipolicy.Resolution) error { return errors.New("provider changed") }
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("validation ignored")
	}
	if _, err := os.Stat(filepath.Join(home, "argv")); !os.IsNotExist(err) {
		t.Fatal("launched")
	}
}
func TestCancellation(t *testing.T) {
	o, _ := fixture(t, "claude")
	if err := os.WriteFile(o.Resolution.Executable, []byte("#!/bin/sh\nsleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, o)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("process did not stop")
	}
}
func TestNativeFailureAndDenial(t *testing.T) {
	for _, event := range []string{`{"type":"result","subtype":"success","session_id":"s","permission_denials":[{}]}`, `{"type":"result","subtype":"error_max_turns","session_id":"s"}`, `{"type":"turn.failed"}`, `garbage`} {
		o := newObserver("claude", nil)
		_, _ = o.Write([]byte(event))
		err := o.finish()
		if !o.denied && err == nil {
			t.Fatalf("accepted %s", event)
		}
	}
}
func TestStoreRejectsSymlink(t *testing.T) {
	o, home := fixture(t, "claude")
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestAutomaticTurnResolution(t *testing.T) {
	o, home := fixture(t, "codex")
	calls := 0
	o.ResolveTurn = func(turn Turn) (aipolicy.Resolution, error) {
		calls++
		r := o.Resolution
		if strings.Contains(turn.Task, "분석") {
			r.Effort = "high"
			r.LaunchArgs = []string{"--model", r.Model, "-c", `model_reasoning_effort="high"`}
		}
		return r, nil
	}
	o.Input = turns(t, Turn{Task: "상태 확인"}, Turn{Task: "원인 분석", Checkpoint: cp()})
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || r.Switches != 1 || r.Effort != "high" {
		t.Fatalf("%+v calls %d", r, calls)
	}
	argv, _ := os.ReadFile(filepath.Join(home, "argv"))
	if !strings.Contains(string(argv), `model_reasoning_effort="high"`) {
		t.Fatalf("effort not applied: %s", argv)
	}
}
func TestCrossProviderStops(t *testing.T) {
	o, home := fixture(t, "claude")
	o.Resolve = func(string) (aipolicy.Resolution, error) { r := o.Resolution; r.Agent = "codex"; return r, nil }
	o.Input = turns(t, Turn{Task: "first"}, Turn{Task: "next", Workload: "analysis", Checkpoint: cp()})
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("cross provider silently replayed")
	}
	argv, _ := os.ReadFile(filepath.Join(home, "argv"))
	if strings.Count(string(argv), "\n") != 1 {
		t.Fatal("unexpected launch")
	}
}
func TestUnsupportedAndEmptyInput(t *testing.T) {
	o, _ := fixture(t, "claude")
	o.Input = strings.NewReader("")
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("empty input accepted")
	}
	o.Input = turns(t, Turn{Task: "task"})
	o.Resolution.Agent = "kimi"
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("unsupported managed adapter accepted")
	}
}

func TestCheckpointHandoffStartsNewSession(t *testing.T) {
	for _, source := range []string{"claude", "codex"} {
		t.Run(source, func(t *testing.T) {
			o, home := fixture(t, source)
			destination := "codex"
			if source == "codex" {
				destination = "claude"
			}
			binary := filepath.Join(t.TempDir(), destination)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/target-argv"
cat >> "$HOME/target-input"
`
			if destination == "claude" {
				script += `printf '%s\n' '{"type":"result","subtype":"success","session_id":"new-native"}'
`
			} else {
				script += `printf '%s\n' '{"type":"thread.started","thread_id":"new-native"}' '{"type":"turn.completed"}'
`
			}
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			o.Resolution.KnowledgeApprovals = testKnowledgeGrants(source)
			next := o.Resolution
			next.KnowledgeApprovals = testKnowledgeGrants(destination)
			next.Agent = destination
			next.Executable = binary
			next.Home = filepath.Join(home, "."+destination)
			o.Resolve = func(string) (aipolicy.Resolution, error) { return next, nil }
			c := cp()
			c.Scope = "한국어 보고서를 작성하고 이미 전송한 메일은 재전송하지 않는다"
			if err := os.WriteFile(filepath.Join(o.WorkDir, "result.md"), []byte("검증한 결과"), 0600); err != nil {
				t.Fatal(err)
			}
			c.Files = []string{"result.md"}
			c.CompletedEffects = []string{"Mail sent, receipt operation-123"}
			var output bytes.Buffer
			o.Stdout = &output
			o.Input = turns(t, Turn{Task: "PRIVATE_INITIAL_TASK"}, Turn{Task: "결과 문서 작성", Workload: "analysis", Checkpoint: c}, Turn{Task: "문서 검토", Checkpoint: cp()})
			r, err := Run(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if r.NativeSession != "new-native" || r.Continuity != "checkpoint-handoff" || r.Switches != 1 || len(r.Handoffs) != 1 || r.Handoffs[0].FromNativeSession != "native-123" {
				t.Fatalf("wrong continuity: %+v", r)
			}
			argv, _ := os.ReadFile(filepath.Join(home, "target-argv"))
			lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
			if len(lines) != 2 || strings.Contains(lines[0], "resume") || !strings.Contains(lines[1], "resume new-native") {
				t.Fatalf("wrong starts: %s", argv)
			}
			input, _ := os.ReadFile(filepath.Join(home, "target-input"))
			text := string(input)
			if strings.Contains(text, "PRIVATE_INITIAL_TASK") || !strings.Contains(text, "operation-123") || !strings.Contains(text, c.Scope) || !strings.Contains(text, "결과 문서 작성") || !strings.Contains(text, "sha256") {
				t.Fatalf("wrong handoff payload: %s", input)
			}
			if !strings.Contains(output.String(), "new-native") || !strings.Contains(output.String(), "native-123") {
				t.Fatal("native stdout not propagated")
			}
		})
	}
}

func TestHandoffRequiresCompleteCheckpoint(t *testing.T) {
	for _, field := range []string{"scope", "verification", "effects"} {
		t.Run(field, func(t *testing.T) {
			o, home := fixture(t, "claude")
			c := cp()
			c.Scope = "Preserve original task instructions"
			switch field {
			case "scope":
				c.Scope = ""
			case "verification":
				c.Verification = ""
			case "effects":
				c.CompletedEffects = nil
			}
			o.Resolve = func(string) (aipolicy.Resolution, error) { r := o.Resolution; r.Agent = "codex"; return r, nil }
			o.Input = turns(t, Turn{Task: "first"}, Turn{Task: "next", Workload: "analysis", Checkpoint: c})
			if _, err := Run(context.Background(), o); err == nil {
				t.Fatal("incomplete handoff accepted")
			}
			argv, _ := os.ReadFile(filepath.Join(home, "argv"))
			if strings.Count(string(argv), "\n") != 1 {
				t.Fatal("new process launched")
			}
		})
	}
}
func TestHandoffFailureNeverRetries(t *testing.T) {
	o, home := fixture(t, "claude")
	binary := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'started\\n' >> \"$HOME/target-argv\"\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	o.Resolve = func(string) (aipolicy.Resolution, error) {
		r := o.Resolution
		r.Agent = "codex"
		r.Executable = binary
		return r, nil
	}
	c := cp()
	c.Scope = "Continue without repeating completed effects"
	o.Input = turns(t, Turn{Task: "first"}, Turn{Task: "next", Workload: "analysis", Checkpoint: c}, Turn{Task: "no retry", Checkpoint: c})
	r, err := Run(context.Background(), o)
	if err == nil || r.Status != "stopped" {
		t.Fatalf("%+v %v", r, err)
	}
	argv, _ := os.ReadFile(filepath.Join(home, "target-argv"))
	if strings.Count(string(argv), "\n") != 1 {
		t.Fatal("failed handoff retried")
	}
}

func TestNativeHomeIdentity(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			key := "CLAUDE_CONFIG_DIR"
			if agent == "codex" {
				key = "CODEX_HOME"
			}
			for _, mode := range []string{"native-default", "pinned"} {
				t.Run(mode, func(t *testing.T) {
					r := aipolicy.Resolution{Agent: agent, Home: "/validated/home", HomeMode: mode}
					env, err := sessionEnvironment([]string{"HOME=/actual/home", key + "=/ambient/foreign", key + "=/duplicate", "KEEP=yes"}, r)
					if err != nil {
						t.Fatal(err)
					}
					count := 0
					for _, entry := range env {
						if strings.HasPrefix(entry, key+"=") {
							count++
							if entry != key+"=/validated/home" {
								t.Fatalf("ambient home leaked: %s", entry)
							}
						}
					}
					want := 0
					if mode == "pinned" {
						want = 1
					}
					if count != want {
						t.Fatalf("%s count %d", mode, count)
					}
				})
			}
			if _, err := sessionEnvironment(nil, aipolicy.Resolution{Agent: agent, HomeMode: "unknown"}); err == nil {
				t.Fatal("unknown identity mode accepted")
			}
		})
	}
}

func TestCodexActualItemReceipts(t *testing.T) {
	// Wire examples follow rust-v0.157.1 exec_events.rs and the JSONL processor.
	cases := []struct {
		name, event       string
		denied, ambiguous bool
	}{
		{"declined command", `{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"deploy","aggregated_output":"","exit_code":null,"status":"declined"}}`, true, false},
		{"failed patch", `{"type":"item.completed","item":{"id":"item_0","type":"file_change","changes":[{"path":"file.go","kind":"update"}],"status":"failed"}}`, false, true},
		{"MCP denial", `{"type":"item.completed","item":{"id":"item_0","type":"mcp_tool_call","server":"mail","tool":"send","arguments":{},"result":null,"error":{"message":"permission denied"},"status":"failed"}}`, false, true},
		{"MCP uncertain effect", `{"type":"item.completed","item":{"id":"item_0","type":"mcp_tool_call","server":"mail","tool":"send","arguments":{},"result":null,"error":{"message":"connection closed"},"status":"failed"}}`, false, true},
		{"ordinary failing test", `{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"go test ./...","aggregated_output":"FAIL","exit_code":1,"status":"failed"}}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := newObserver("codex", nil)
			_, _ = observer.Write([]byte("{\"type\":\"thread.started\",\"thread_id\":\"native-123\"}\n" + tc.event + "\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"output_tokens\":1}}\n"))
			err := observer.finish()
			if observer.denied != tc.denied || (err != nil) != tc.ambiguous {
				t.Fatalf("denied=%t err=%v", observer.denied, err)
			}
		})
	}
}
func TestCodexDeclinedTurnNeverRoutes(t *testing.T) {
	o, home := fixture(t, "codex")
	script := `#!/bin/sh
printf 'started\n' >> "$HOME/argv"
cat > /dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"native-123"}' '{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"deploy","aggregated_output":"","exit_code":null,"status":"declined"}}' '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}'
`
	if err := os.WriteFile(o.Resolution.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	resolved := 0
	o.Resolve = func(string) (aipolicy.Resolution, error) {
		resolved++
		r := o.Resolution
		r.Model = "stronger"
		return r, nil
	}
	o.Input = turns(t, Turn{Task: "first"}, Turn{Task: "continue using another model", Workload: "analysis", Checkpoint: cp()})
	r, err := Run(context.Background(), o)
	if err == nil || r.Status != "stopped" || resolved != 0 {
		t.Fatalf("%+v error=%v routes=%d", r, err, resolved)
	}
	argv, _ := os.ReadFile(filepath.Join(home, "argv"))
	if strings.Count(string(argv), "\n") != 1 {
		t.Fatalf("unexpected second native launch: %s", argv)
	}
}
