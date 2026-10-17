package module

import (
	"slices"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// loadProfile builds the config through the real config package, so
// IsModuleEnabled decides enablement exactly as it does in production instead
// of against a literal that happens to satisfy today's implementation. "full"
// enables every name in defaultOrder; "minimal" enables only packages, shell,
// node, git, ssh and terminal, which is what makes it the enablement-gate
// fixture below.
func loadProfile(t *testing.T, name string) *config.Config {
	t.Helper()
	cfg, err := config.Load(name, "", nil)
	if err != nil {
		t.Fatalf("config.Load(%q): %v", name, err)
	}
	return cfg
}

// stubRegistry registers a stub module under each given name, in the given
// order. Registration order is a deliberate input: Resolve must ignore it.
func stubRegistry(names []string) *Registry {
	r := &Registry{modules: make(map[string]Module, len(names))}
	for _, n := range names {
		r.Register(&runAllStubModule{name: n})
	}
	return r
}

// reversedOrder returns defaultOrder back to front, so a registry populated
// from it disagrees with execution order on every entry.
func reversedOrder() []string {
	names := slices.Clone(defaultOrder)
	slices.Reverse(names)
	return names
}

func resolvedNames(t *testing.T, mods []Module) []string {
	t.Helper()
	names := make([]string, 0, len(mods))
	for i, m := range mods {
		if m == nil {
			t.Fatalf("Resolve returned a nil module at index %d; an unregistered name must be skipped, not yielded", i)
		}
		names = append(names, m.Name())
	}
	return names
}

// TestResolve pins the sequence `dot apply` mutates the machine in, and the
// three gates that decide which modules are in it. The order row is the
// load-bearing one and the easiest to write vacuously, so it compares the FULL
// returned sequence against a literal list — not membership — from a registry
// populated in reverse. A Resolve that ranged over r.modules would return a Go
// map's order and fail that row on nearly every run.
func TestResolve(t *testing.T) {
	// The execution order, spelled out rather than read back from defaultOrder,
	// so a reordering of the var is a test failure rather than a silent rename
	// of what "execution order" means.
	fullOrder := []string{
		"packages", "shell", "node", "git", "ssh", "terminal", "tmux",
		"workspace", "ai", "fonts", "macapps", "conda", "gpg", "secrets",
	}

	tests := []struct {
		name     string
		profile  string
		register []string // nil means every defaultOrder name, reversed
		filter   []string
		want     []string
	}{
		{
			name:    "every module enabled and no filter returns defaultOrder's sequence",
			profile: "full",
			want:    fullOrder,
		},
		{
			name:    "a nil filter is not a deny-all",
			profile: "full",
			filter:  nil,
			want:    fullOrder,
		},
		{
			name:    "an empty filter is not a deny-all either",
			profile: "full",
			filter:  []string{},
			want:    fullOrder,
		},
		{
			name:    "a module the config disables is absent with no filter",
			profile: "minimal",
			want:    []string{"packages", "shell", "node", "git", "ssh", "terminal"},
		},
		{
			name:    "a filter narrows without reordering",
			profile: "full",
			filter:  []string{"ssh", "git", "packages"},
			want:    []string{"packages", "git", "ssh"},
		},
		{
			name:    "a filtered module that config disables stays absent",
			profile: "minimal",
			filter:  []string{"tmux", "git"},
			want:    []string{"git"},
		},
		{
			name:    "an unknown filter entry yields nothing and suppresses nothing",
			profile: "full",
			filter:  []string{"not-a-module", "git"},
			want:    []string{"git"},
		},
		{
			name:     "a defaultOrder name with no registered module is skipped",
			profile:  "full",
			register: []string{"gpg", "shell", "git"},
			want:     []string{"shell", "git", "gpg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			register := tt.register
			if register == nil {
				register = reversedOrder()
			}
			r := stubRegistry(register)

			got := resolvedNames(t, r.Resolve(loadProfile(t, tt.profile), tt.filter))

			if !slices.Equal(got, tt.want) {
				t.Errorf("Resolve = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNormalizeFilter pins the one legacy name the CLI still has to reject by
// hand: `ai-tools` was renamed, and a silent pass would resolve to nothing and
// look like a module that simply did no work.
func TestNormalizeFilter(t *testing.T) {
	tests := []struct {
		name       string
		filter     []string
		want       []string
		wantErrHas []string
	}{
		{name: "nil filter is returned unchanged", filter: nil, want: nil},
		{name: "empty filter is returned unchanged", filter: []string{}, want: []string{}},
		{name: "current names pass through unchanged", filter: []string{"ai", "git"}, want: []string{"ai", "git"}},
		{
			name:       "the renamed name is rejected",
			filter:     []string{"ai-tools"},
			wantErrHas: []string{"ai-tools", "ai"},
		},
		{
			name:       "the renamed name is rejected from anywhere in the filter",
			filter:     []string{"git", "ai-tools"},
			wantErrHas: []string{"ai-tools", "ai"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeFilter(tt.filter)

			if len(tt.wantErrHas) > 0 {
				if err == nil {
					t.Fatalf("NormalizeFilter error = nil, want an error; filter = %v", got)
				}
				for _, want := range tt.wantErrHas {
					if !namesModule(err.Error(), want) {
						t.Errorf("NormalizeFilter error = %q, want it to name %q", err, want)
					}
				}
				if got != nil {
					t.Errorf("NormalizeFilter = %v alongside an error, want nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeFilter error = %v, want nil", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("NormalizeFilter = %v, want %v", got, tt.want)
			}
		})
	}
}

// namesModule checks for a quoted module name, so asserting that the error
// names "ai" is not satisfied by the "ai-tools" occurrence.
func namesModule(errText, name string) bool {
	return strings.Contains(errText, `"`+name+`"`)
}
