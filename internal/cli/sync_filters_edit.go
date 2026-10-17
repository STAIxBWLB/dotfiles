package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

const (
	maxPatternFileBytes     = 1024 * 1024
	syncFilterSchemaVersion = 1
)

type syncFilterFileJSON struct {
	SchemaVersion int    `json:"schemaVersion"`
	Profile       string `json:"profile"`
	Kind          string `json:"kind"`
	Path          string `json:"path"`
	ActiveCount   int    `json:"activeCount"`
	Content       string `json:"content"`
}

func newSyncFiltersGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "get <include|exclude|ignore|allow>",
		Short:        "Read one editable filter file",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE:         runSyncFiltersGet,
	}
	cmd.Flags().Bool("json", false, "print a stable machine-readable document")
	return cmd
}

func newSyncFiltersSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "set <include|exclude|ignore|allow>",
		Short:        "Replace one editable filter file from stdin",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE:         runSyncFiltersSet,
	}
	cmd.Flags().Bool("json", false, "print the updated file as JSON")
	cmd.Flags().Bool("ack-secret-exposure", false, "acknowledge that added allow patterns send matching secrets")
	return cmd
}

func filterPath(cfg *syncer.Config, kind string) (string, error) {
	if cfg == nil || cfg.LocalPaths == nil {
		return "", fmt.Errorf("sync profile store unresolved")
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "include":
		return cfg.LocalPaths.IncludeFile, nil
	case "exclude":
		return cfg.LocalPaths.ExcludeFile, nil
	case "ignore":
		return cfg.LocalPaths.IgnoreFile, nil
	case "allow":
		return cfg.LocalPaths.AllowFile, nil
	default:
		return "", fmt.Errorf("unknown filter kind %q", kind)
	}
}

func activePatternCount(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			count++
		}
	}
	return count
}

func activePatternSet(content string) map[string]struct{} {
	patterns := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns[line] = struct{}{}
		}
	}
	return patterns
}

func addsActivePattern(current, replacement string) bool {
	existing := activePatternSet(current)
	for pattern := range activePatternSet(replacement) {
		if _, ok := existing[pattern]; !ok {
			return true
		}
	}
	return false
}

func readFilterDocument(cfg *syncer.Config, kind string) (syncFilterFileJSON, error) {
	path, err := filterPath(cfg, kind)
	if err != nil {
		return syncFilterFileJSON{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return syncFilterFileJSON{}, err
	}
	text := string(content)
	return syncFilterFileJSON{
		SchemaVersion: syncFilterSchemaVersion,
		Profile:       cfg.Profile,
		Kind:          strings.ToLower(kind),
		Path:          path,
		ActiveCount:   activePatternCount(text),
		Content:       text,
	}, nil
}

func runSyncFiltersGet(cmd *cobra.Command, args []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	document, err := readFilterDocument(bs.Config, args[0])
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(document)
	}
	_, err = io.WriteString(cmd.OutOrStdout(), document.Content)
	return err
}

func runSyncFiltersSet(cmd *cobra.Command, args []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	limited := io.LimitReader(cmd.InOrStdin(), maxPatternFileBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(body) > maxPatternFileBytes {
		return fmt.Errorf("filter file exceeds %d bytes", maxPatternFileBytes)
	}
	if strings.ContainsRune(string(body), '\x00') {
		return fmt.Errorf("filter file contains a NUL byte")
	}
	content := strings.ReplaceAll(string(body), "\r\n", "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	kind := strings.ToLower(args[0])
	current, err := readFilterDocument(cfg, kind)
	if err != nil {
		return err
	}
	if kind == "allow" && addsActivePattern(current.Content, content) {
		ack, _ := cmd.Flags().GetBool("ack-secret-exposure")
		if !ack {
			return fmt.Errorf("adding allow patterns requires --ack-secret-exposure")
		}
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		if err := writePatternFileAtomic(current.Path, []byte(content)); err != nil {
			return err
		}
	}
	updated := current
	updated.Content = content
	updated.ActiveCount = activePatternCount(content)
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(updated)
	}
	printerFrom(cmd).Success("updated %s filter for profile %s", kind, cfg.Profile)
	return nil
}

func writePatternFileAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".filter-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func newSyncFiltersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "filters",
		Short: "Show the effective filter layers or reset them from templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newSyncFiltersGetCmd(),
		newSyncFiltersSetCmd(),
		&cobra.Command{
			Use:          "show",
			Short:        "Print the effective ordered filter rule chain",
			Args:         cobra.NoArgs,
			RunE:         runSyncFiltersShow,
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:   "reset",
			Short: "Regenerate exclude.txt/include.txt from embedded templates (with backups)",
			Long: `Backs up the current exclude.txt and include.txt to *.bak-<timestamp>
and rewrites them from the embedded templates. Use after upgrading dot to
pick up refreshed junk rules.

ignore.txt and allow.txt are operator-owned and are never touched. Re-add
any workspace-specific patterns from the backups to ignore.txt afterwards.`,
			Args:         cobra.NoArgs,
			RunE:         runSyncFiltersReset,
			SilenceUsage: true,
		},
	)
	return cmd
}

func runSyncFiltersShow(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	layers, err := syncer.FilterReport(bs.Config)
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	p.Header("Sync Filter Chain (first match wins)")
	for _, layer := range layers {
		p.Section(layer.Name)
		for _, d := range layer.Detail {
			if strings.HasPrefix(d, "WARNING") {
				p.Line("  %s", ui.StyleWarning.Render(d))
				continue
			}
			p.Line("  %s", d)
		}
	}
	p.Blank()
	return nil
}

func runSyncFiltersReset(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	if cfg.LocalPaths == nil {
		return fmt.Errorf("local paths unresolved — bug in ResolveConfig")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	p := printerFrom(cmd)
	if dryRun {
		p.Line("[dry-run] would reset %s and %s from embedded templates (backups kept)", cfg.ExcludesFile, cfg.IncludeFile)
		return nil
	}
	backups, err := syncer.ResetFilterFiles(cfg.LocalPaths)
	if err != nil {
		return err
	}
	p.Line("%s", ui.StyleSuccess.Render("✓ filter files reset from templates"))
	for _, b := range backups {
		p.KV("Backup", b)
	}
	p.Line("Re-add workspace-specific patterns to %s if the backups carried any.", cfg.IgnoreFile)
	return nil
}
