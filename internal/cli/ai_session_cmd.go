package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"

	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
	"github.com/STAIxBWLB/dotfiles/internal/aisession"
	"github.com/spf13/cobra"
)

func newAISessionCmd() *cobra.Command {
	c := &cobra.Command{Use: "session", Short: "Launch controlled agents with adaptive policy and native continuation"}
	start := &cobra.Command{Use: "start", Short: "Launch an eligible agent or supervise checkpointed JSONL turns", Long: `Start a native agent with the resolved model, effort and automatic-review flags.
Use --task for an interactive session, or --input FILE (or -) for a trusted
supervisor's JSONL turns. Managed turns re-evaluate workload at safe boundaries;
continuations require a curated checkpoint. Same-provider turns use native resume;
Claude/Codex handoffs start a new session from scope and completed-effect evidence.
Manual native/Orca sessions are not taken over. Heavy commands still use dot ai run.`, Args: cobra.NoArgs, RunE: runAISessionStart}
	addPolicyRequestFlags(start)
	start.Flags().String("input", "", "Trusted JSONL turn stream file, or - for stdin")
	start.Flags().Bool("fixed", false, "Freeze the initial configuration for this session")
	c.AddCommand(start)
	return c
}

func runAISessionStart(cmd *cobra.Command, _ []string) error {
	policy, err := effectivePolicyForCmd(cmd)
	if err != nil {
		return err
	}
	if policy == nil || !policy.Enabled {
		return fmt.Errorf("configure an enabled modules.ai.policy before starting a session")
	}
	req, err := policyRequest(cmd)
	if err != nil {
		return err
	}
	dry, _ := cmd.Flags().GetBool("dry-run")
	if dry {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Would resolve validated policy and launch at native turn boundaries (no probes, files, locks or agent calls).")
		return err
	}
	inputPath, _ := cmd.Flags().GetString("input")
	var input io.Reader
	if inputPath != "" {
		if req.Task != "" {
			return fmt.Errorf("--task and --input are mutually exclusive")
		}
		input = cmd.InOrStdin()
		if inputPath != "-" {
			f, openErr := os.Open(inputPath)
			if openErr != nil {
				return openErr
			}
			defer f.Close()
			input = f
		}
		reader := bufio.NewReaderSize(input, 256<<10)
		line, readErr := reader.ReadSlice('\n')
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("initial turn: %w", readErr)
		}
		var first aisession.Turn
		if err = json.Unmarshal(line, &first); err != nil {
			return fmt.Errorf("initial turn: %w", err)
		}
		req.Task = first.Task
		if first.Workload != "" {
			req.Workload = first.Workload
		}
		input = io.MultiReader(strings.NewReader(string(line)), reader)
		req.Unattended = true
	} else if strings.TrimSpace(req.Task) == "" {
		return fmt.Errorf("provide --task or --input")
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	prefs := policyPreferences(cmd)
	resolve := func(ctx context.Context, request aipolicy.Request) (aipolicy.Resolution, error) {
		current, loadErr := effectivePolicyForCmd(cmd)
		if loadErr != nil {
			return aipolicy.Resolution{}, loadErr
		}
		if !reflect.DeepEqual(current, policy) {
			return aipolicy.Resolution{}, fmt.Errorf("policy changed during session; restart after reviewing the new policy")
		}
		over, _ := cmd.Flags().GetString("home")
		inventory, inspectErr := aipolicy.Inspect(ctx, homeFromCmd(cmd), over != "")
		if inspectErr != nil {
			return aipolicy.Resolution{}, inspectErr
		}
		r, resolveErr := aipolicy.Resolve(current, request, inventory)
		if resolveErr != nil {
			return r, resolveErr
		}
		overlay, overlayErr := prefs.OverlayArgs(current, inventory, r.TargetID)
		if overlayErr != nil {
			return r, overlayErr
		}
		r.LaunchArgs = append(overlay, r.LaunchArgs...)
		return r, nil
	}
	initial, err := resolve(ctx, req)
	if err != nil {
		return err
	}
	fixed, _ := cmd.Flags().GetBool("fixed")
	fixed = fixed || req.Agent != ""
	options := aisession.Options{Resolution: initial, Home: homeFromCmd(cmd), WorkDir: req.CWD, Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(), Input: input, UserOverride: fixed, MaxSwitches: policy.SwitchLimit()}
	if input == nil {
		options.Task = req.Task
	}
	options.ResolveTurn = func(turn aisession.Turn) (aipolicy.Resolution, error) {
		r := req
		r.Task = turn.Task
		r.Workload = turn.Workload
		if r.Workload == "" {
			r.Workload, _ = cmd.Flags().GetString("workload")
		}
		return resolve(ctx, r)
	}
	options.Validate = func(ctx context.Context, expected aipolicy.Resolution) error {
		r := req
		r.Workload, r.Agent = expected.Workload, expected.Agent
		fresh, freshErr := resolve(ctx, r)
		if freshErr != nil {
			return freshErr
		}
		if err := aisession.RetainKnowledge(expected, fresh); err != nil {
			return err
		}
		// Constraints describe other candidates and can vary with the explicit
		// agent filter. Compare only the actual executable launch contract.
		if fresh.PolicyRevision != expected.PolicyRevision || fresh.TargetID != expected.TargetID || fresh.Agent != expected.Agent || fresh.Version != expected.Version || fresh.Home != expected.Home || fresh.HomeMode != expected.HomeMode || fresh.Executable != expected.Executable || fresh.Model != expected.Model || fresh.Effort != expected.Effort || fresh.Billing != expected.Billing || fresh.BillingVerified != expected.BillingVerified || fresh.PermissionMechanism != expected.PermissionMechanism || !reflect.DeepEqual(fresh.KnowledgeApprovals, expected.KnowledgeApprovals) || !reflect.DeepEqual(fresh.LaunchArgs, expected.LaunchArgs) {
			return fmt.Errorf("effective runtime changed before launch; resolve again")
		}
		return nil
	}
	receipt, err := aisession.Run(ctx, options)
	if receipt.ID != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Session %s: %s, %d turns, %d changes; receipt %s\n", receipt.ID, receipt.Status, receipt.Turns, receipt.Switches, receipt.Path)
	}
	return err
}
