package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

const peerHomePathsSchemaVersion = 1

type peerHomePathsJSON struct {
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path"`
	ActiveCount   int    `json:"activeCount"`
	Content       string `json:"content"`
}

func newPeerHomePathsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "home-paths",
		Short:        "Read or replace the peer host-path allowlist",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	get := &cobra.Command{
		Use:          "get",
		Short:        "Read the peer host-path list",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runPeerHomePathsGet,
	}
	get.Flags().Bool("json", false, "print a stable machine-readable document")
	set := &cobra.Command{
		Use:          "set",
		Short:        "Replace the peer host-path list from stdin",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runPeerHomePathsSet,
	}
	set.Flags().Bool("json", false, "print the updated document")
	cmd.AddCommand(get, set, newPeerHomeTrackedCmd())
	return cmd
}

// newPeerHomeTrackedCmd edits the tracked subset: entries listed there get
// baseline-aware delete propagation and conflict quarantine instead of the
// additive newest-mtime-wins treatment.
func newPeerHomeTrackedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "tracked",
		Short:        "Read or replace the tracked host-path list (deletes propagate, conflicts quarantine)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	get := &cobra.Command{
		Use:          "get",
		Short:        "Read the tracked peer host-path list",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runPeerHomeTrackedGet,
	}
	get.Flags().Bool("json", false, "print a stable machine-readable document")
	set := &cobra.Command{
		Use:          "set",
		Short:        "Replace the tracked peer host-path list from stdin",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runPeerHomeTrackedSet,
	}
	set.Flags().Bool("json", false, "print the updated document")
	cmd.AddCommand(get, set)
	return cmd
}

func readPeerHomePaths(path string) (peerHomePathsJSON, error) {
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return peerHomePathsJSON{}, err
	}
	content := string(body)
	return peerHomePathsJSON{
		SchemaVersion: peerHomePathsSchemaVersion,
		Path:          path,
		ActiveCount:   activePatternCount(content),
		Content:       content,
	}, nil
}

func runPeerHomePathsGet(cmd *cobra.Command, _ []string) error {
	return runPeerHomePathsGetAt(cmd, syncer.PeerHomePathsFile)
}

func runPeerHomeTrackedGet(cmd *cobra.Command, _ []string) error {
	return runPeerHomePathsGetAt(cmd, syncer.PeerHomeTrackedFile)
}

func runPeerHomePathsGetAt(cmd *cobra.Command, pathFor func(*syncer.LocalPaths) string) error {
	bs, err := peerBootstrapReadOnly(cmd)
	if err != nil {
		return err
	}
	document, err := readPeerHomePaths(pathFor(bs.Config.LocalPaths))
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

func runPeerHomePathsSet(cmd *cobra.Command, _ []string) error {
	return runPeerHomePathsSetAt(cmd, syncer.PeerHomePathsFile,
		"peer home-path list", "peer host paths updated")
}

func runPeerHomeTrackedSet(cmd *cobra.Command, _ []string) error {
	return runPeerHomePathsSetAt(cmd, syncer.PeerHomeTrackedFile,
		"peer tracked home-path list", "peer tracked host paths updated")
}

func runPeerHomePathsSetAt(cmd *cobra.Command, pathFor func(*syncer.LocalPaths) string, label, success string) error {
	bs, err := syncer.Bootstrap(peerBootstrapOptions(cmd))
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxPatternFileBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxPatternFileBytes {
		return fmt.Errorf("%s exceeds %d bytes", label, maxPatternFileBytes)
	}
	content := strings.ReplaceAll(string(body), "\r\n", "\n")
	if strings.ContainsRune(content, '\x00') {
		return fmt.Errorf("%s contains a NUL byte", label)
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	path := pathFor(bs.Config.LocalPaths)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		if err := writePatternFileAtomic(path, []byte(content)); err != nil {
			return err
		}
	}
	document := peerHomePathsJSON{
		SchemaVersion: peerHomePathsSchemaVersion,
		Path:          path,
		ActiveCount:   activePatternCount(content),
		Content:       content,
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(document)
	}
	printerFrom(cmd).Success("%s", success)
	return nil
}
