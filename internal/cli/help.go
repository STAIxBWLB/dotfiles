package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func configureHelp(root *cobra.Command) {
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		if err := withPresentation(cmd, func() error { return renderHelp(cmd) }); err != nil {
			PrintError(cmd.ErrOrStderr(), err)
		}
	})
	root.SetUsageFunc(func(cmd *cobra.Command) error {
		return withPresentation(cmd, func() error { return renderHelp(cmd) })
	})
}

func configureRootGroups(root *cobra.Command) {
	groups := []struct{ id, title, commands string }{
		{"setup", "Setup", "init reconfigure apply diff status config update rollback version"},
		{"workspace", "Workspace", "open stop list register unregister layouts hub ws clean noindex"},
		{"sync", "Sync", "sync peer remote"},
		{"ai", "AI", "ai guard"},
		{"backup", "Backup and secrets", "backup restore profile secrets apps"},
		{"host", "Host", "admit container tunnel watchdog"},
		{"help", "Help", "usecase help completion"},
	}
	for _, group := range groups {
		root.AddGroup(&cobra.Group{ID: group.id, Title: group.title})
		for _, name := range strings.Fields(group.commands) {
			for _, child := range root.Commands() {
				if child.Name() == name {
					child.GroupID = group.id
				}
			}
		}
	}
	root.SetHelpCommandGroupID("help")
	root.SetCompletionCommandGroupID("help")
}

type helpRow struct{ label, description string }

func renderHelp(cmd *cobra.Command) error {
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()
	p := printerFrom(cmd)
	p.Header(cmd.CommandPath())
	if cmd.Short != "" {
		ui.WriteHanging(p.Out, "  ", ui.StyleHint.Render(cmd.Short))
	}
	if cmd.Long != "" && cmd.Long != cmd.Short {
		p.Blank()
		ui.WriteHanging(p.Out, "  ", strings.TrimSpace(cmd.Long))
	}
	p.Section("Usage")
	ui.WriteHanging(p.Out, "  ", cmd.UseLine())

	children := availableHelpCommands(cmd)
	if len(children) > 0 {
		if !cmd.HasParent() && len(cmd.Groups()) > 0 {
			for _, group := range cmd.Groups() {
				var rows []helpRow
				for _, child := range children {
					if child.GroupID == group.ID {
						rows = append(rows, helpRow{child.Name(), child.Short})
					}
				}
				if len(rows) > 0 {
					p.Section(group.Title)
					writeHelpRows(p.Out, rows)
				}
			}
		} else {
			p.Section("Commands")
			var rows []helpRow
			for _, child := range children {
				rows = append(rows, helpRow{child.Name(), child.Short})
			}
			writeHelpRows(p.Out, rows)
		}
	}
	local := cmd.LocalFlags()
	global := cmd.InheritedFlags()
	if !cmd.HasParent() {
		local, global = cmd.LocalNonPersistentFlags(), cmd.PersistentFlags()
	}
	for _, section := range []struct {
		name  string
		flags *pflag.FlagSet
	}{{"Flags", local}, {"Global flags", global}} {
		if rows := helpFlagRows(section.flags); len(rows) > 0 {
			p.Section(section.name)
			writeHelpRows(p.Out, rows)
		}
	}
	if cmd.Example != "" {
		p.Section("Examples")
		ui.WriteHanging(p.Out, "  ", strings.TrimSpace(cmd.Example))
	}
	if len(cmd.Aliases) > 0 {
		p.Section("Aliases")
		ui.WriteHanging(p.Out, "  ", strings.Join(cmd.Aliases, ", "))
	}
	return nil
}

func availableHelpCommands(cmd *cobra.Command) []*cobra.Command {
	var commands []*cobra.Command
	for _, child := range cmd.Commands() {
		if !child.Hidden && (child.IsAvailableCommand() || child.Name() == "help") {
			commands = append(commands, child)
		}
	}
	return commands
}

func helpFlagRows(flags *pflag.FlagSet) []helpRow {
	var rows []helpRow
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			return
		}
		// A single-flag rendering preserves pflag's value/default/NoOptDefVal
		// conventions. With one label it separates the description by 3 spaces.
		one := pflag.NewFlagSet("help", pflag.ContinueOnError)
		one.AddFlag(flag)
		label, description, _ := strings.Cut(strings.TrimSpace(one.FlagUsages()), "   ")
		rows = append(rows, helpRow{label, description})
	})
	return rows
}

func writeHelpRows(w io.Writer, rows []helpRow) {
	labelWidth := 0
	for _, row := range rows {
		labelWidth = max(labelWidth, min(30, ansi.StringWidth(row.label)))
	}
	for _, row := range rows {
		width := ansi.StringWidth(row.label)
		label := ui.StyleKey.Width(0).Render(row.label)
		if width > labelWidth {
			ui.WriteHanging(w, "  ", label)
			ui.WriteHanging(w, strings.Repeat(" ", labelWidth+4), row.description)
			continue
		}
		prefix := fmt.Sprintf("  %s%s  ", label, strings.Repeat(" ", labelWidth-width))
		ui.WriteHanging(w, prefix, row.description)
	}
}
