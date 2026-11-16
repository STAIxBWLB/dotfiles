package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	maxShortLen = 60
	maxLongCols = 80
)

// uppercaseInUse matches an uppercase letter in a Use line. The metadata rules
// write every placeholder as <name> or [<name>], so an uppercase letter means a
// DIR, FILE or N style placeholder.
var uppercaseInUse = regexp.MustCompile(`[A-Z]`)

// validateHelpMetadata walks every available command under root and returns one
// message per offense, each naming the command path, so a failing run lists
// every violation instead of the first. It checks the #452 metadata rules:
//
//   - Short: one line, at most 60 characters, sentence case, no trailing period
//   - flag descriptions (local, persistent, inherited and the built-in --help):
//     one line, lowercase first letter, no trailing period
//   - Long: no line over 80 columns
//   - Example: every runnable command has one
//   - Use: no uppercase placeholders
//   - every non-hidden root command belongs to a group
//
// It initializes the built-in --help flag on each command, so the generated
// text is measured too. Call it after the generated help and completion
// commands exist, as NewRootCmd leaves them.
func validateHelpMetadata(root *cobra.Command) []string {
	var problems []string
	add := func(path, format string, args ...any) {
		problems = append(problems, path+": "+fmt.Sprintf(format, args...))
	}
	seenFlags := map[string]bool{}

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		path := c.CommandPath()
		c.InitDefaultHelpFlag()

		checkShort(path, c.Short, add)

		for i, line := range strings.Split(c.Long, "\n") {
			if cols := len([]rune(line)); cols > maxLongCols {
				add(path, "Long line %d is %d columns, over %d", i+1, cols, maxLongCols)
			}
		}

		if c.Runnable() && strings.TrimSpace(c.Example) == "" {
			add(path, "runnable command has no Example")
		}

		if uppercaseInUse.MatchString(c.Use) {
			add(path, "Use %q has an uppercase placeholder; write <name>", c.Use)
		}

		for _, set := range []*pflag.FlagSet{c.LocalFlags(), c.PersistentFlags(), c.InheritedFlags()} {
			set.VisitAll(func(f *pflag.Flag) {
				// A flag shows on many commands (persistent and inherited). Report
				// it once, at the first command that reaches it.
				key := f.Name + "\x00" + f.Usage
				if seenFlags[key] {
					return
				}
				seenFlags[key] = true
				checkFlagUsage(path, f, add)
			})
		}

		if c.Parent() == root && !c.Hidden && (c.GroupID == "" || !root.ContainsGroup(c.GroupID)) {
			add(path, "root command is outside a group (GroupID %q)", c.GroupID)
		}

		for _, child := range c.Commands() {
			if !child.Hidden {
				walk(child)
			}
		}
	}
	walk(root)
	sort.Strings(problems)
	return problems
}

func checkShort(path, short string, add func(path, format string, args ...any)) {
	switch {
	case strings.TrimSpace(short) == "":
		add(path, "Short is empty")
		return
	case strings.Contains(short, "\n"):
		add(path, "Short spans lines")
	}
	if n := len([]rune(short)); n > maxShortLen {
		add(path, "Short is %d characters, over %d: %q", n, maxShortLen, short)
	}
	if first := []rune(short)[0]; unicode.IsLetter(first) && !unicode.IsUpper(first) {
		add(path, "Short is not sentence case: %q", short)
	}
	if strings.HasSuffix(strings.TrimSpace(short), ".") {
		add(path, "Short ends in a period: %q", short)
	}
}

func checkFlagUsage(path string, f *pflag.Flag, add func(path, format string, args ...any)) {
	usage := f.Usage
	label := "--" + f.Name
	switch {
	case strings.TrimSpace(usage) == "":
		add(path, "flag %s has no description", label)
		return
	case strings.Contains(usage, "\n"):
		add(path, "flag %s description spans lines", label)
	}
	if first := []rune(usage)[0]; unicode.IsUpper(first) {
		add(path, "flag %s description starts uppercase: %q", label, usage)
	}
	if strings.HasSuffix(strings.TrimSpace(usage), ".") {
		add(path, "flag %s description ends in a period: %q", label, usage)
	}
}

// The real command tree, including the generated help and completion commands,
// must satisfy every rule.
func TestHelpMetadata(t *testing.T) {
	root := NewRootCmd("dev", "test")
	problems := validateHelpMetadata(root)
	if len(problems) > 0 {
		t.Fatalf("%d help metadata violation(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	// A walk that visited nothing would pass; require a realistic tree.
	count := 0
	var visit func(c *cobra.Command)
	visit = func(c *cobra.Command) {
		count++
		for _, child := range c.Commands() {
			if !child.Hidden {
				visit(child)
			}
		}
	}
	visit(root)
	if count < 200 {
		t.Fatalf("the walk saw %d commands, expected the full tree of 200 or more", count)
	}
}

// plantedTree builds a small tree that satisfies every rule. Each case below
// breaks exactly one thing, so a validator that stopped checking a rule fails
// its case, and the clean tree proves the validator does not reject everything.
func plantedTree() (root, child *cobra.Command) {
	root = &cobra.Command{Use: "tool", Short: "A tool", Example: "  tool run"}
	root.AddGroup(&cobra.Group{ID: "main", Title: "Main"})
	root.PersistentFlags().Bool("verbose", false, "show more output")
	child = &cobra.Command{
		Use:     "run [<path>]",
		Short:   "Run a thing",
		Long:    "Run a thing.\n\nIt does what it says.",
		Example: "  tool run",
		GroupID: "main",
		Run:     func(*cobra.Command, []string) {},
	}
	child.Flags().String("mode", "", "execution mode")
	root.AddCommand(child)
	return root, child
}

func TestHelpMetadataRejectsPlantedViolations(t *testing.T) {
	if problems := validateHelpMetadata(func() *cobra.Command { r, _ := plantedTree(); return r }()); len(problems) > 0 {
		t.Fatalf("the clean planted tree was rejected: %v", problems)
	}

	long := strings.Repeat("x", maxLongCols+1)
	cases := []struct {
		name   string
		mutate func(root, child *cobra.Command)
		want   string
	}{
		{"short too long", func(_, c *cobra.Command) { c.Short = strings.Repeat("A", maxShortLen+1) }, "over 60"},
		{"short empty", func(_, c *cobra.Command) { c.Short = "" }, "Short is empty"},
		{"short lowercase", func(_, c *cobra.Command) { c.Short = "run a thing" }, "not sentence case"},
		{"short period", func(_, c *cobra.Command) { c.Short = "Run a thing." }, "ends in a period"},
		{"short spans lines", func(_, c *cobra.Command) { c.Short = "Run\na thing" }, "Short spans lines"},
		{"local flag uppercase", func(_, c *cobra.Command) { c.Flags().String("bad", "", "Bad description") }, "starts uppercase"},
		{"local flag period", func(_, c *cobra.Command) { c.Flags().String("bad", "", "bad description.") }, "ends in a period"},
		{"local flag newline", func(_, c *cobra.Command) { c.Flags().String("bad", "", "bad\ndescription") }, "spans lines"},
		{"local flag empty", func(_, c *cobra.Command) { c.Flags().String("bad", "", "") }, "no description"},
		{"persistent flag uppercase", func(r, _ *cobra.Command) { r.PersistentFlags().Bool("loud", false, "Loud output") }, "starts uppercase"},
		{"inherited flag period", func(r, _ *cobra.Command) { r.PersistentFlags().Bool("quiet", false, "quiet output.") }, "ends in a period"},
		{"built-in help flag", func(_, c *cobra.Command) {
			c.Flags().BoolP("help", "h", false, "Help for this command")
		}, "starts uppercase"},
		{"long line too wide", func(_, c *cobra.Command) { c.Long = "ok\n" + long }, "Long line 2"},
		{"runnable without example", func(_, c *cobra.Command) { c.Example = "" }, "no Example"},
		{"uppercase placeholder", func(_, c *cobra.Command) { c.Use = "run DIR" }, "uppercase placeholder"},
		{"root command without group", func(_, c *cobra.Command) { c.GroupID = "" }, "outside a group"},
		{"root command in unknown group", func(_, c *cobra.Command) { c.GroupID = "missing" }, "outside a group"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, child := plantedTree()
			tc.mutate(root, child)
			problems := validateHelpMetadata(root)
			if len(problems) == 0 {
				t.Fatalf("planted violation %q was not rejected", tc.name)
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("no violation mentions %q:\n%s", tc.want, joined)
			}
			// The offender must be named by command path. The root-level flag
			// cases name the first command that reaches the flag.
			if !strings.Contains(joined, "tool") {
				t.Fatalf("violations do not name a command path:\n%s", joined)
			}
		})
	}
}

// Group commands with no Run may omit an Example; a hidden command is not part
// of the help surface.
func TestHelpMetadataExemptions(t *testing.T) {
	root, _ := plantedTree()
	group := &cobra.Command{Use: "family", Short: "Manage a family", GroupID: "main"}
	group.AddCommand(&cobra.Command{Use: "list", Short: "List the family", Example: "  tool family list", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(group)
	hidden := &cobra.Command{Use: "__debug", Short: "no sentence case.", Hidden: true, Run: func(*cobra.Command, []string) {}}
	root.AddCommand(hidden)
	if problems := validateHelpMetadata(root); len(problems) > 0 {
		t.Fatalf("an exempt command was rejected: %v", problems)
	}
}
