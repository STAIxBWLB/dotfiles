package remote

import (
	"slices"
	"testing"
)

// The built-in list is a contract: losing one of these sends something to a
// host that must never leave this machine.
func TestBuiltinExcludesKeepSecretsAndBuildTreesHome(t *testing.T) {
	got := BuiltinExcludes()
	for _, want := range []string{
		".git",
		"node_modules",
		"target",
		"dist",
		".venv",
		".next",
		".worktrees",
		".claude/worktrees",
		".qwen/worktrees",
		".DS_Store",
		".env",
		".env.*",
		".dev.vars",
		"*.pem",
		"*.key",
		".vercel",
		".agenthub",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("the built-in excludes are missing %q: %v", want, got)
		}
	}
	// The template include is what makes .env.example survive .env.*, so it is
	// added by ExcludeArgs rather than listed here.
	if slices.Contains(got, envTemplateInclude) {
		t.Error("the env template is in the exclude list")
	}
}

func TestExcludeExtraOnlyAdds(t *testing.T) {
	builtin := BuiltinExcludes()
	cfg := Config{ExcludeExtra: []string{" datasets ", "", "  ", "vendor/cache"}}
	got := cfg.Excludes()
	if !slices.Equal(got[:len(builtin)], builtin) {
		t.Errorf("the extra entries changed the built-in list:\n%v", got)
	}
	if !slices.Equal(got[len(builtin):], []string{"datasets", "vendor/cache"}) {
		t.Errorf("extras = %v, want the trimmed entries and no blanks", got[len(builtin):])
	}
	// A configured entry that names a built-in pattern cannot remove it.
	negative := Config{ExcludeExtra: []string{".git"}}
	if got := negative.Excludes(); !slices.Contains(got, ".git") {
		t.Error(".git is gone from the exclude list")
	}
}

func TestBuiltinExcludesReturnsACopy(t *testing.T) {
	first := BuiltinExcludes()
	first[0] = "changed"
	if second := BuiltinExcludes(); second[0] == "changed" {
		t.Error("BuiltinExcludes handed out its own slice")
	}
}

func TestExcludeArgsLeadWithTheTemplateInclude(t *testing.T) {
	args := ExcludeArgs([]string{".git", ".env.*"})
	want := []string{"--include=.env.example", "--exclude=.git", "--exclude=.env.*"}
	if !slices.Equal(args, want) {
		t.Errorf("ExcludeArgs = %v, want %v", args, want)
	}
}
