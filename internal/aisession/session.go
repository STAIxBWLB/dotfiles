// Package aisession supervises native agent processes at explicit turn boundaries.
// Native transcripts remain owned by the agent; receipts contain no prompts/output.
package aisession

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/STAIxBWLB/dotfiles/internal/aihandoff"
	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

type Options struct {
	Resolution          aipolicy.Resolution
	Home, WorkDir, Task string
	Stdin               io.Reader
	Stdout, Stderr      io.Writer
	// Input is a trusted supervisor's JSONL turn stream, never agent output.
	Input       io.Reader
	Resolve     func(string) (aipolicy.Resolution, error)
	ResolveTurn func(Turn) (aipolicy.Resolution, error)
	// Validate must freshly check executable version, provider and entitlement.
	Validate     func(context.Context, aipolicy.Resolution) error
	UserOverride bool
	MaxSwitches  int
}
type Checkpoint struct {
	Scope            string   `json:"scope,omitempty"`
	Summary          string   `json:"summary"`
	Files            []string `json:"files,omitempty"`
	Verification     string   `json:"verification"`
	Pending          []string `json:"pending,omitempty"`
	CompletedEffects []string `json:"completed_effects,omitempty"`
	ApprovalDenied   bool     `json:"approval_denied,omitempty"`
}
type Turn struct {
	Task         string      `json:"task"`
	Workload     string      `json:"workload,omitempty"`
	Checkpoint   *Checkpoint `json:"checkpoint,omitempty"`
	UserOverride bool        `json:"user_override,omitempty"`
}
type HandoffReceipt struct {
	FromAgent         string `json:"from_agent"`
	FromNativeSession string `json:"from_native_session"`
	ToAgent           string `json:"to_agent"`
}
type Receipt struct {
	Handoffs       []HandoffReceipt `json:"handoffs,omitempty"`
	ID             string           `json:"id"`
	Agent          string           `json:"agent"`
	Model          string           `json:"model,omitempty"`
	Effort         string           `json:"effort,omitempty"`
	PolicyRevision string           `json:"policy_revision"`
	NativeSession  string           `json:"native_session,omitempty"`
	Turns          int              `json:"turns"`
	Switches       int              `json:"switches"`
	Frozen         bool             `json:"frozen"`
	Status         string           `json:"status"`
	Continuity     string           `json:"continuity"`
	Path           string           `json:"path"`
}

// Run never retries a native process: an unsuccessful process may already have
// completed an external effect. Managed continuation requires a curated boundary.
func Run(ctx context.Context, o Options) (receipt Receipt, err error) {
	if err = validateOptions(o); err != nil {
		return receipt, err
	}
	if err = o.Validate(ctx, o.Resolution); err != nil {
		return receipt, err
	}
	store, err := newStore(o.Home)
	if err != nil {
		return receipt, err
	}
	defer store.Close()
	receipt = Receipt{ID: store.id, Agent: o.Resolution.Agent, Model: o.Resolution.Model, Effort: o.Resolution.Effort, PolicyRevision: o.Resolution.PolicyRevision, Frozen: o.UserOverride, Status: "starting", Continuity: "native interactive", Path: store.path}
	defer func() {
		if err != nil {
			receipt.Status = "stopped"
		}
		if e := store.write("receipt.json", receipt); err == nil && e != nil {
			err = e
		}
	}()
	if err = store.write("receipt.json", receipt); err != nil {
		return receipt, err
	}
	if o.Input == nil {
		args := append([]string{}, o.Resolution.LaunchArgs...)
		if o.Task != "" {
			args = append(args, "--", o.Task)
		}
		err = runProcess(ctx, o, o.Resolution, args, o.Stdin, o.Stdout)
		if err == nil {
			receipt.Status = "completed"
		}
		return receipt, err
	}
	if o.Resolution.Agent != "claude" && o.Resolution.Agent != "codex" {
		return receipt, errors.New("managed turns currently require Claude or Codex native resume")
	}
	receipt.Continuity = "native session resume"
	scanner := bufio.NewScanner(o.Input)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	current := o.Resolution
	for scanner.Scan() {
		if receipt.Turns >= 256 {
			return receipt, errors.New("session limit is 256 turns")
		}
		var turn Turn
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&turn); err != nil {
			return receipt, fmt.Errorf("invalid turn: %w", err)
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return receipt, errors.New("turn must contain one JSON object")
		}
		if turn.Task == "" || len(turn.Task) > aihandoff.SummaryLimit || !utf8.ValidString(turn.Task) {
			return receipt, errors.New("task must be valid UTF-8 and 1..32768 bytes")
		}
		if receipt.Turns > 0 && turn.Checkpoint == nil {
			return receipt, errors.New("continuation requires a curated checkpoint")
		}
		var artifactRecord aihandoff.Record
		if turn.Checkpoint != nil {
			if artifactRecord, err = checkpoint(ctx, o, current, turn.Checkpoint); err != nil {
				return receipt, err
			}
			if err = store.write(fmt.Sprintf("checkpoint-%03d.json", receipt.Turns), turn.Checkpoint); err != nil {
				return receipt, err
			}
			if err = store.write(fmt.Sprintf("artifacts-%03d.json", receipt.Turns), artifactRecord.Artifacts); err != nil {
				return receipt, err
			}
			if turn.Checkpoint.ApprovalDenied {
				return receipt, errors.New("approval denied; routing cannot bypass rejection")
			}
			if len(turn.Checkpoint.Pending) > 0 {
				return receipt, errors.New("pending or ambiguous effects require reconciliation before continuation")
			}
		}
		var handoff *HandoffReceipt
		receipt.Frozen = receipt.Frozen || turn.UserOverride
		if !receipt.Frozen && (o.ResolveTurn != nil || (turn.Workload != "" && o.Resolve != nil)) {
			var next aipolicy.Resolution
			var e error
			if o.ResolveTurn != nil {
				next, e = o.ResolveTurn(turn)
			} else {
				next, e = o.Resolve(turn.Workload)
			}
			if e != nil {
				return receipt, e
			}
			if receipt.Turns > 0 {
				if err = RetainKnowledge(current, next); err != nil {
					return receipt, err
				}
			}
			if !sameConfiguration(current, next) {
				if receipt.Turns > 0 && receipt.Switches >= min(o.MaxSwitches, 2) {
					return receipt, errors.New("automatic switch budget exhausted")
				}
				if receipt.Turns > 0 && (next.Agent != current.Agent || next.Home != current.Home || next.HomeMode != current.HomeMode || next.Executable != current.Executable || next.Version != current.Version || next.Billing != current.Billing) {
					if err = completeHandoffCheckpoint(turn.Checkpoint); err != nil {
						return receipt, err
					}
					handoff = &HandoffReceipt{FromAgent: current.Agent, FromNativeSession: receipt.NativeSession, ToAgent: next.Agent}
				}
				if !next.Eligible || (next.Agent != "claude" && next.Agent != "codex") {
					return receipt, errors.New("replacement resolution is ineligible")
				}
				current = next
				if receipt.Turns > 0 {
					receipt.Switches++
				}
			}
		}
		if err = o.Validate(ctx, current); err != nil {
			return receipt, err
		}
		prompt := turn.Task
		if handoff != nil {
			prompt = handoffPrompt(turn, artifactRecord.Artifacts)
			receipt.NativeSession = ""
			receipt.Continuity = "checkpoint-handoff"
			receipt.Handoffs = append(receipt.Handoffs, *handoff)
		}
		args := turnArgs(current, receipt.NativeSession)
		receipt.Status = "running"
		receipt.Agent = current.Agent
		receipt.Model = current.Model
		receipt.Effort = current.Effort
		receipt.PolicyRevision = current.PolicyRevision
		if err = store.write("receipt.json", receipt); err != nil {
			return receipt, err
		}
		observer := newObserver(current.Agent, o.Stdout)
		err = runProcess(ctx, o, current, args, strings.NewReader(prompt), observer)
		parseErr := observer.finish()
		if err != nil {
			return receipt, fmt.Errorf("native turn failed; effects may be ambiguous; no retry: %w", err)
		}
		if parseErr != nil {
			return receipt, parseErr
		}
		if observer.denied {
			return receipt, errors.New("native approval denial; automatic routing stopped")
		}
		if !observer.completed || observer.session == "" {
			return receipt, errors.New("native completion/session receipt missing; no retry")
		}
		if receipt.NativeSession != "" && observer.session != receipt.NativeSession {
			return receipt, errors.New("native resume returned a different session; no retry")
		}
		receipt.NativeSession = observer.session
		receipt.Turns++
		receipt.Status = "boundary"
		if err = store.write("receipt.json", receipt); err != nil {
			return receipt, err
		}
	}
	if err = scanner.Err(); err != nil {
		return receipt, err
	}
	if receipt.Turns == 0 {
		return receipt, errors.New("managed input contains no turns")
	}
	receipt.Status = "completed"
	return receipt, nil
}
func sameConfiguration(a, b aipolicy.Resolution) bool {
	return a.Agent == b.Agent && a.Model == b.Model && a.Effort == b.Effort && a.TargetID == b.TargetID && a.Home == b.Home && a.HomeMode == b.HomeMode && a.Executable == b.Executable && a.PermissionMechanism == b.PermissionMechanism && a.Version == b.Version && a.Billing == b.Billing && a.BillingVerified == b.BillingVerified && slices.Equal(a.KnowledgeApprovals, b.KnowledgeApprovals) && slices.Equal(a.LaunchArgs, b.LaunchArgs)
}
func validateOptions(o Options) error {
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(home)
	if e != nil {
		return e
	}
	requested, e := filepath.EvalSymlinks(o.Home)
	if e != nil {
		return e
	}
	if !filepath.IsAbs(o.Home) || actual != requested {
		return errors.New("native launch rejects foreign --home")
	}
	if !filepath.IsAbs(o.WorkDir) {
		return errors.New("work directory must be absolute")
	}
	st, e := os.Stat(o.WorkDir)
	if e != nil {
		return e
	}
	if !st.IsDir() {
		return errors.New("work directory is not a directory")
	}
	if !o.Resolution.Eligible || o.Resolution.Executable == "" || o.Resolution.Version == "" {
		return errors.New("launch requires an eligible versioned resolution")
	}
	if o.Validate == nil {
		return errors.New("fresh runtime validation is required")
	}
	if o.MaxSwitches < 0 {
		return errors.New("switch budget cannot be negative")
	}
	if o.Input != nil && o.Task != "" {
		return errors.New("managed input and interactive task are mutually exclusive")
	}
	return nil
}
func checkpoint(ctx context.Context, o Options, r aipolicy.Resolution, c *Checkpoint) (aihandoff.Record, error) {
	if len(c.Pending) > 32 || len(c.CompletedEffects) > 64 || len(c.Verification) > aihandoff.SummaryLimit {
		return aihandoff.Record{}, errors.New("checkpoint exceeds limits")
	}
	for _, s := range append(append([]string{}, c.Pending...), c.CompletedEffects...) {
		if len(s) > 4096 || !utf8.ValidString(s) {
			return aihandoff.Record{}, errors.New("invalid checkpoint effect")
		}
	}
	return aihandoff.RecordNote(ctx, aihandoff.Options{Home: o.Home, Project: o.WorkDir, Agent: r.Agent, Kind: "progress", Summary: c.Summary, Artifacts: c.Files, SelectedAgents: []string{r.Agent}, DryRun: true})
}

// completeHandoffCheckpoint requires the supervisor to reconcile effects and
// restate task authority. "none" is an explicit completed-effects declaration.
func completeHandoffCheckpoint(c *Checkpoint) error {
	if c == nil || strings.TrimSpace(c.Scope) == "" || strings.TrimSpace(c.Verification) == "" || len(c.CompletedEffects) == 0 {
		return errors.New("checkpoint handoff requires scope/instructions, verification and an explicit completed-effects ledger")
	}
	if len(c.Scope) > aihandoff.SummaryLimit || !utf8.ValidString(c.Scope) || !utf8.ValidString(c.Verification) {
		return errors.New("invalid handoff scope or verification")
	}
	for _, effect := range c.CompletedEffects {
		if strings.TrimSpace(effect) == "" {
			return errors.New("completed effects must contain explicit records or none")
		}
	}
	if c.ApprovalDenied || len(c.Pending) > 0 {
		return errors.New("denied or ambiguous work cannot be handed off")
	}
	return nil
}
func handoffPrompt(turn Turn, artifacts []aihandoff.Artifact) string {
	// JSON framing keeps literal Korean and clearly separates curated context
	// from the next task. No native transcript or previous raw prompt is included.
	payload := struct {
		Checkpoint *Checkpoint          `json:"checkpoint"`
		Artifacts  []aihandoff.Artifact `json:"verified_artifacts,omitempty"`
		NextTask   string               `json:"next_task"`
	}{turn.Checkpoint, artifacts, turn.Task}
	data, _ := json.Marshal(payload)
	return "Continue from this supervisor-curated checkpoint in a NEW native session. This is a checkpoint handoff, not transcript replay. Preserve the scope and instructions. Completed effects have already happened: do not repeat them. Verification statements are supervisor claims; artifact hashes were checked locally at this boundary. Reconcile any newly discovered uncertainty before external actions.\n" + string(data)
}

func turnArgs(r aipolicy.Resolution, session string) []string {
	args := append([]string{}, r.LaunchArgs...)
	if r.Agent == "claude" {
		args = append(args, "--print", "--output-format", "json")
		if session != "" {
			args = append(args, "--resume", session)
		}
		return args
	}
	// Global permission/model/config flags precede the subcommand, so resume
	// inherits exec-level approval policy instead of silently reverting to defaults.
	args = append(args, "exec", "--json")
	if session != "" {
		args = append(args, "resume", session)
	}
	return append(args, "-")
}
func runProcess(ctx context.Context, o Options, r aipolicy.Resolution, args []string, in io.Reader, out io.Writer) error {
	cmd := exec.Command(r.Executable, args...)
	cmd.Dir = o.WorkDir
	var err error
	cmd.Env, err = sessionEnvironment(os.Environ(), r)
	if err != nil {
		return err
	}
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = o.Stderr
	// Reuse argv-preserving process-group supervision without acquiring a heavy
	// work lease. The supervisor retains its group leader until cleanup completes.
	return resourceguard.RunCommand(ctx, cmd)
}

// Native default is an identity choice, not shorthand for explicitly pinning
// ~/.claude: setting CLAUDE_CONFIG_DIR can select a different keychain namespace.
func sessionEnvironment(env []string, r aipolicy.Resolution) ([]string, error) {
	key := ""
	switch r.Agent {
	case "claude":
		key = "CLAUDE_CONFIG_DIR"
	case "codex":
		key = "CODEX_HOME"
	default:
		return nil, errors.New("unsupported native session agent")
	}
	if r.HomeMode != "native-default" && r.HomeMode != "pinned" {
		return nil, errors.New("unknown native home mode")
	}
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			result = append(result, entry)
		}
	}
	if r.HomeMode == "pinned" {
		if !filepath.IsAbs(r.Home) {
			return nil, errors.New("pinned native home must be absolute")
		}
		result = append(result, key+"="+r.Home)
	}
	return result, nil
}

type localStore struct {
	root               *os.Root
	id, path, relative string
}

func newStore(home string) (*localStore, error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = root.Close()
		}
	}()
	relative := ".local/share/dotfiles/ai/sessions"
	part := ""
	for _, p := range strings.Split(relative, "/") {
		part = filepath.Join(part, p)
		st, e := root.Lstat(part)
		if os.IsNotExist(e) {
			if e = root.Mkdir(part, 0700); e != nil {
				return nil, e
			}
		} else if e != nil {
			return nil, e
		} else if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("session store contains non-directory or symlink")
		}
	}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(token)
	relative = filepath.Join(relative, id)
	if err = root.Mkdir(relative, 0700); err != nil {
		return nil, err
	}
	failed = false
	return &localStore{root: root, id: id, path: filepath.Join(home, relative, "receipt.json"), relative: relative}, nil
}
func (s *localStore) Close() { _ = s.root.Close() }
func (s *localStore) write(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	temp := filepath.Join(s.relative, name+".tmp")
	f, err := s.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = s.root.Remove(temp) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return s.root.Rename(temp, filepath.Join(s.relative, name))
}
