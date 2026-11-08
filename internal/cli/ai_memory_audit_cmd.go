package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aimemory"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// memoryMarkersName sits next to the agents SSOT, so `dot peer` carries it
// between machines with the rest of ~/.config/dotfiles.
const memoryMarkersName = "memory-markers.yaml"

func memoryOptionsFromCmd(cmd *cobra.Command) aimemory.Options {
	home := homeFromCmd(cmd)
	over, _ := cmd.Flags().GetString("home")
	explicit := over != ""
	o := aimemory.Options{
		Home:        home,
		CodexHome:   filepath.Join(home, ".codex"),
		MarkersPath: filepath.Join(newAgentsManagerFromCmd(cmd).SSOTDirPath(), memoryMarkersName),
	}
	seen := map[string]bool{}
	addClaude := func(dir string) {
		key := dir
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			key = real
		}
		if !seen[key] {
			seen[key] = true
			o.ClaudeDirs = append(o.ClaudeDirs, dir)
		}
	}
	if !explicit {
		if v := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(v) {
			addClaude(v)
		}
		if v := os.Getenv("CODEX_HOME"); filepath.IsAbs(v) {
			o.CodexHome = v
		}
		// jcode keeps its store under the real home, so --home never reads it
		// and the audit reports it unavailable.
		if bin, err := exec.LookPath("jcode"); err == nil {
			o.JcodeStats = func() (string, error) {
				ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
				defer cancel()
				out, err := exec.CommandContext(ctx, bin, "memory", "stats").Output()
				return string(out), err
			}
		}
	}
	addClaude(filepath.Join(home, ".claude"))
	return o
}

func newAIMemoryAuditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "audit",
		Short: "Report the size and drift of each agent's native memory (read-only)",
		Long: `Read the native memory of Claude Code, Codex, Qwen, Grok and jcode and report,
per store: file count and bytes, the always-loaded index against its limit
(Claude MEMORY.md: 200 lines or 25 KB; Codex memory_summary.md: 10 KB), index
links to missing files, memory files no index links to, hits of retired terms,
and the last change. A table shows each retired term's hits per agent.

Stores read: Claude projects/*/memory under $CLAUDE_CONFIG_DIR and ~/.claude;
$CODEX_HOME/memories (else ~/.codex/memories); ~/.qwen/memories and
~/.qwen/projects/*/memory; the MEMORY.md indexes under ~/.grok/memory-v2; and
the counts of 'jcode memory stats'. A store that is missing is reported as
unavailable, never as clean.

Retired terms come from ~/.config/dotfiles/agents/memory-markers.yaml:

  markers:
    - pattern: old-plugin-name        # case-insensitive text
      retired: 2026-10-01
      replacement: new-tool
      reason: the plugin was removed
    - pattern: 'old-repo(-v1)?'
      regex: true

The audit writes nothing. Output holds counts and file names only; --show also
prints the matching lines. Exit code 1 when an index is over its limit or links
a missing file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o := memoryOptionsFromCmd(cmd)
			o.Agents, _ = cmd.Flags().GetStringSlice("agent")
			o.Show, _ = cmd.Flags().GetBool("show")
			for _, a := range o.Agents {
				if !contains(aimemory.Agents, a) {
					return fmt.Errorf("unknown agent %q (one of %s)", a, strings.Join(aimemory.Agents, ", "))
				}
			}
			r, err := aimemory.Audit(o)
			if err != nil {
				return err
			}
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				if err := printJSON(cmd, r); err != nil {
					return err
				}
			} else {
				printMemoryAudit(printerFrom(cmd), r, o.Show)
			}
			if len(r.Problems) > 0 {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("memory audit: %d problem(s), listed under Problems with the step that fixes each", len(r.Problems))}
			}
			return nil
		},
	}
	c.Flags().StringSlice("agent", nil, "Agents to audit (comma-separated: "+strings.Join(aimemory.Agents, ",")+")")
	c.Flags().Bool("json", false, "Print the report as JSON")
	c.Flags().Bool("show", false, "Also print the lines that match a retired term")
	return c
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func printMemoryAudit(p *Printer, r aimemory.Report, show bool) {
	p.Header("Agent Memory Audit")
	if r.MarkersStatus == aimemory.StatusOK {
		p.KV("Markers", fmt.Sprintf("%s (%d)", r.MarkersFile, len(r.Markers)))
	} else {
		p.KV("Markers", "unavailable: "+r.MarkersFile+" not found, no retired-term checks")
	}
	agent := ""
	empty := 0
	for _, s := range r.Stores {
		if s.Status == aimemory.StatusOK && s.Files == 0 {
			empty++
			continue
		}
		if s.Agent != agent {
			agent = s.Agent
			p.Section(agent)
		}
		if s.Status != aimemory.StatusOK {
			p.Bullet(ui.StyleHint.Render(ui.MarkAbsent), fmt.Sprintf("%s  unavailable (%s)", s.Path, s.Reason))
			continue
		}
		marker := ui.StyleSuccess.Render(ui.MarkPresent)
		if (s.Index != nil && s.Index.Over) || len(s.Broken) > 0 || len(s.Unreadable) > 0 {
			marker = ui.StyleError.Render(ui.MarkFail)
		}
		line := fmt.Sprintf("%s  %d files, %s", s.Path, s.Files, humanBytes(s.Bytes))
		if s.Modified != "" {
			line += ", changed " + s.Modified
		}
		p.Bullet(marker, line)
		if s.Index != nil && s.Index.Bytes > 0 {
			limit := ""
			if s.Index.MaxLines > 0 {
				limit = fmt.Sprintf(" of %d lines / %s", s.Index.MaxLines, humanBytes(s.Index.MaxBytes))
			} else if s.Index.MaxBytes > 0 {
				limit = " of " + humanBytes(s.Index.MaxBytes)
			}
			state := ""
			if s.Index.Over {
				state = "  OVER LIMIT"
			}
			p.Line("      index %s: %d lines, %s%s%s", filepath.Base(s.Index.Path), s.Index.Lines, humanBytes(s.Index.Bytes), limit, state)
		}
		for _, b := range s.Broken {
			p.Line("      broken link: %s", b)
		}
		for _, u := range s.Unreadable {
			p.Line("      unreadable: %s", u)
		}
		if len(s.Orphans) > 0 {
			p.Line("      not in the index (%d): %s", len(s.Orphans), strings.Join(s.Orphans, ", "))
		}
		for _, mh := range s.Markers {
			fileSet := []string{}
			for _, h := range mh.Hits {
				if !contains(fileSet, h.File) {
					fileSet = append(fileSet, h.File)
				}
			}
			p.Line("      %q: %d hit(s) in %s", mh.Marker, len(mh.Hits), strings.Join(fileSet, ", "))
			if show {
				for _, h := range mh.Hits {
					p.Line("        %s:%d: %s", h.File, h.Line, h.Text)
				}
			}
		}
	}
	if empty > 0 {
		p.Line("  %d empty store(s) not shown (listed by --json)", empty)
	}
	if len(r.CrossAgent) > 0 {
		p.Section("Retired terms per agent")
		header := fmt.Sprintf("  %-28s", "term")
		for _, a := range aimemory.Agents {
			header += fmt.Sprintf(" %7s", a)
		}
		p.Line("%s", header)
		for _, row := range r.CrossAgent {
			line := fmt.Sprintf("  %-28s", truncate(row.Marker, 28))
			for _, a := range aimemory.Agents {
				cell := "-"
				if n, ok := row.Counts[a]; ok {
					cell = fmt.Sprint(n)
				}
				line += fmt.Sprintf(" %7s", cell)
			}
			p.Line("%s", line)
		}
		p.Line("  - : no readable store, not checked")
	}
	if len(r.Problems) > 0 {
		p.Section("Problems")
		for _, prob := range r.Problems {
			p.Warn("  %s", prob)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func humanBytes(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1f KB", float64(n)/1_000)
	}
	return fmt.Sprintf("%d B", n)
}

func newAIMemoryArchiveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "archive <agent> <path>...",
		Short: "Move stale memory files into an archive (dry run unless --apply)",
		Long: `Move whole memory files out of the layer an agent loads. Content is never
edited or deleted, and index files are never touched.

  claude, qwen  <memory dir>/_archive/<path>: outside the index, and outside
                the */memory/*.md files amem imports
  codex         $CODEX_HOME/memories-archive/<path>: outside the directory
                Codex consolidates. memory_summary.md, MEMORY.md,
                raw_memories.md and rollout_summaries/ are refused, because
                the next consolidation rewrites them; move their source notes
                (for example extensions/ad_hoc/notes/*) instead.

Without --apply, each move is previewed with the index lines that still name
the file. With --apply the files are moved, one 'dot ai audit' event is
written, and the command that moves them back is printed. Remove the listed
index lines yourself or ask the agent to.

Refused: directories, paths outside the agent's memory store, symlinks that
resolve outside it, the store's own index, and a destination that exists.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			apply, _ := cmd.Flags().GetBool("apply")
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				apply = false
			}
			moves, err := aimemory.PlanArchive(memoryOptionsFromCmd(cmd), args[0], args[1:])
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("Archive " + args[0] + " memory")
			for _, m := range moves {
				p.Line("%s", m.From)
				p.Line("  -> %s", m.To)
				for _, ref := range m.IndexRefs {
					p.Line("  still named by %s", ref)
				}
			}
			if !apply {
				p.Line("Dry run: nothing moved. Re-run with --apply (and without --dry-run).")
				return nil
			}
			done, err := aimemory.ApplyArchive(moves)
			if len(done) > 0 {
				payload := map[string]any{"agent": args[0], "moves": done}
				auditAIEventBestEffort(cmd, "ai.memory.archive", payload)
				p.Success("Moved %d file(s). To move them back:", len(done))
				p.Line("  %s", aimemory.ReverseCommand(done))
			}
			if err != nil {
				return errors.Join(fmt.Errorf("archive stopped after %d of %d file(s); fix the cause below and re-run with the files not yet moved, or undo the moved ones with the command above", len(done), len(moves)), err)
			}
			return nil
		},
	}
	c.Flags().Bool("apply", false, "Move the files (default: preview)")
	return c
}
