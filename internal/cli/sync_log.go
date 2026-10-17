package cli

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

const (
	maxSyncLogTailBytes  int64 = 256 * 1024
	syncLogSchemaVersion       = 1
)

type syncLogJSON struct {
	SchemaVersion int    `json:"schemaVersion"`
	Profile       string `json:"profile"`
	Path          string `json:"path"`
	Content       string `json:"content"`
}

func newSyncLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "log",
		Short:        "Show the tail of the profile sync log",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runSyncLog,
	}
	cmd.Flags().Int("tail", 200, "maximum number of newest lines")
	cmd.Flags().Bool("json", false, "print a stable machine-readable document")
	return cmd
}

func runSyncLog(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	cfg := bs.Config
	lines, _ := cmd.Flags().GetInt("tail")
	if lines < 1 {
		lines = 1
	}
	if lines > 1000 {
		lines = 1000
	}
	content, err := tailSyncLog(cfg.LogFile, lines)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(syncLogJSON{
			SchemaVersion: syncLogSchemaVersion,
			Profile:       cfg.Profile,
			Path:          cfg.LogFile,
			Content:       content,
		})
	}
	_, err = io.WriteString(cmd.OutOrStdout(), content)
	return err
}

func tailSyncLog(path string, maxLines int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := info.Size() - maxSyncLogTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	text := string(body)
	if offset > 0 {
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = rest
		}
	}
	rows := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(rows) > maxLines {
		rows = rows[len(rows)-maxLines:]
	}
	if len(rows) == 1 && rows[0] == "" {
		return "", nil
	}
	return strings.Join(rows, "\n"), nil
}
