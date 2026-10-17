// Package gsync implements the local→mirror rsync flow that backs
// `dot sync`. Git owns tracked source files, baseline.manifest is the
// Git-shared Drive payload index for untracked artifacts, and push propagates
// local artifact creates/updates by default while deletes are opt-in.
package syncer

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// excludesTemplatePath is the path inside the embedded templates FS.
const excludesTemplatePath = "sync/excludes.txt"

// includesTemplatePath is the path inside the embedded templates FS.
const includesTemplatePath = "sync/includes.txt"

// LoadDefaultIncludePatterns returns the parsed default include patterns from
// the embedded file.
func LoadDefaultIncludePatterns() ([]string, error) {
	engine := template.NewEngine()
	content, err := engine.ReadStatic(includesTemplatePath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded includes: %w", err)
	}
	return parsePatternLines(content)
}

func loadPatternFileOrDefault(path string, defaults func() ([]string, error)) ([]string, error) {
	if path == "" {
		return defaults()
	}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaults()
	}
	if err != nil {
		return nil, err
	}
	patterns, err := parsePatternLines(content)
	if err != nil {
		return nil, err
	}
	return patterns, nil
}

func parsePatternLines(content []byte) ([]string, error) {
	var patterns []string
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning patterns: %w", err)
	}
	return patterns, nil
}

// runtimeFilters bundles the per-run generated filter files that layer into
// the rsync argv alongside the static exclude/ignore/allow files.
type runtimeFilters struct {
	SharedDyn     string // shared-folder excludes (operator-curated)
	SubmodulesDyn string // git submodule paths — synced via Git, never rsync
	WorktreesDyn  string // linked-worktree roots (peer profile only, sticky)
	TrackedDyn    string // include layer: tracked relpaths ∪ baseline keys
	TombstonesDyn string // paths deleted locally — must not be pulled back

	// cleanup removes the temp directory a preview's per-run filter files were
	// written into. nil for a real run, which writes them into the store.
	cleanup func()
}

// secretExcludePatterns is the deny-by-default secrets layer. These paths
// never sync unless the operator explicitly re-includes them in allow.txt.
var secretExcludePatterns = []string{
	"/.secrets",
	"/.secrets/**",
	"/.maru/secrets",
	"/.maru/secrets/**",
	"/_sys/mcp.local.json",
	".env",
	".env.*",
	// Root-anchored credential stores. Directory-only entries intentionally
	// deny traversal at the root without classifying similarly named nested paths.
	"/.ssh/",
	"/.gnupg/",
	"/.aws/credentials",
	"/.config/gcloud/credentials.db",
	"/.config/gh/hosts.yml",
	"/.docker/config.json",
	"/.kube/config",
	"/.netrc",
	"/.npmrc",
	"/.pypirc",
	"/credentials.json",
	"/.terraform.d/credentials.tfrc.json",
	"/.local/share/keyrings/",
	// Private-key names and extensions are credential-bearing at any depth.
	rsyncCaseFoldPattern("id_rsa"),
	rsyncCaseFoldPattern("id_dsa"),
	rsyncCaseFoldPattern("id_ecdsa"),
	rsyncCaseFoldPattern("id_ed25519"),
	rsyncCaseFoldPattern("*.pem"),
	rsyncCaseFoldPattern("*.key"),
	rsyncCaseFoldPattern("*.p12"),
	rsyncCaseFoldPattern("*.pfx"),
	rsyncCaseFoldPattern("*.jks"),
	rsyncCaseFoldPattern("*.keystore"),
	rsyncCaseFoldPattern("*.kdbx"),
	rsyncCaseFoldPattern("*.tfstate"),
	rsyncCaseFoldPattern("*.tfstate.backup"),
	rsyncCaseFoldPattern("serviceAccountKey.json"),
}

// secretAllowBuiltins re-include harmless env templates ahead of the
// `.env.*` exclude — they are documentation, not credentials.
var secretAllowBuiltins = []string{
	".env.example",
	".env.sample",
	".env.template",
}

// SensitiveOverride reports an explicit allow.txt pattern that overlaps a
// hardcoded secret deny pattern. It is raw engine data for callers to render;
// it never changes the allow-before-deny transfer decision.
type SensitiveOverride struct {
	AllowPattern string
	DenyPattern  string
}

// SensitiveOverrides returns de-duplicated, stable-sorted allow/deny overlaps.
// The existing matcher remains the policy authority so this visibility helper
// cannot alter rsync or preview filter ordering.
func SensitiveOverrides(allowPatterns []string) []SensitiveOverride {
	seen := make(map[SensitiveOverride]struct{})
	var overrides []SensitiveOverride
	for _, allow := range allowPatterns {
		allow = strings.TrimSpace(allow)
		if allow == "" || strings.HasPrefix(allow, "#") || isSecretAllowBuiltin(allow) {
			continue
		}
		for _, deny := range secretExcludePatterns {
			if !secretPatternsOverlap(allow, deny) {
				continue
			}
			override := SensitiveOverride{AllowPattern: allow, DenyPattern: deny}
			if _, ok := seen[override]; ok {
				continue
			}
			seen[override] = struct{}{}
			overrides = append(overrides, override)
		}
	}
	sort.Slice(overrides, func(i, j int) bool {
		if overrides[i].AllowPattern == overrides[j].AllowPattern {
			return overrides[i].DenyPattern < overrides[j].DenyPattern
		}
		return overrides[i].AllowPattern < overrides[j].AllowPattern
	})
	return overrides
}

func isSecretAllowBuiltin(pattern string) bool {
	return slices.Contains(secretAllowBuiltins, pattern)
}

func secretPatternsOverlap(allow, deny string) bool {
	allowPattern := excludePattern{raw: allow}
	denyPattern := excludePattern{raw: deny}
	return patternMatchesPathOrAncestor(denyPattern, allow) ||
		allowPattern.matches(deny, false) ||
		allowPattern.matches(deny, true) ||
		globPatternsMayOverlap(allow, deny)
}

// globPatternsMayOverlap detects intersections that matching one pattern
// against the other pattern's literal text cannot see (for example foo.* and
// *.[pP][eE][mM]). The parser is intentionally ASCII-only: an unfamiliar
// pattern is treated as an overlap so visibility is conservative.
func globPatternsMayOverlap(left, right string) bool {
	leftHasPath := strings.Contains(strings.Trim(strings.TrimPrefix(left, "/"), "/"), "/")
	rightHasPath := strings.Contains(strings.Trim(strings.TrimPrefix(right, "/"), "/"), "/")
	if leftHasPath && rightHasPath {
		return false
	}
	leftTokens, leftKnown := parseGlobTokens(globBasename(left))
	rightTokens, rightKnown := parseGlobTokens(globBasename(right))
	if !leftKnown || !rightKnown {
		return true
	}
	type state struct{ left, right int }
	queue := []state{{}}
	seen := make(map[state]bool)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}
		seen[current] = true
		if current.left == len(leftTokens) && current.right == len(rightTokens) {
			return true
		}
		if current.left < len(leftTokens) && leftTokens[current.left].star {
			queue = append(queue, state{left: current.left + 1, right: current.right})
		}
		if current.right < len(rightTokens) && rightTokens[current.right].star {
			queue = append(queue, state{left: current.left, right: current.right + 1})
		}
		if current.left == len(leftTokens) || current.right == len(rightTokens) {
			continue
		}
		leftSet, nextLeft := leftTokens[current.left].consume(current.left)
		rightSet, nextRight := rightTokens[current.right].consume(current.right)
		if globSetsIntersect(leftSet, rightSet) {
			queue = append(queue, state{left: nextLeft, right: nextRight})
		}
	}
	return false
}

func globBasename(pattern string) string {
	pattern = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/"))
	if slash := strings.LastIndexByte(pattern, '/'); slash >= 0 {
		return pattern[slash+1:]
	}
	return pattern
}

type globToken struct {
	star bool
	set  [128]bool
}

func (token globToken) consume(position int) ([128]bool, int) {
	if token.star {
		var all [128]bool
		for index := range all {
			all[index] = true
		}
		return all, position
	}
	return token.set, position + 1
}

func parseGlobTokens(pattern string) ([]globToken, bool) {
	var tokens []globToken
	for index := 0; index < len(pattern); index++ {
		character := pattern[index]
		if character >= 128 {
			return nil, false
		}
		switch character {
		case '*':
			tokens = append(tokens, globToken{star: true})
		case '?':
			var set [128]bool
			for char := range set {
				set[char] = true
			}
			tokens = append(tokens, globToken{set: set})
		case '[':
			end := index + 1
			for end < len(pattern) && pattern[end] != ']' {
				end++
			}
			if end == len(pattern) || end == index+1 {
				return nil, false
			}
			set, known := parseGlobClass(pattern[index+1 : end])
			if !known {
				return nil, false
			}
			tokens = append(tokens, globToken{set: set})
			index = end
		case '\\':
			index++
			if index == len(pattern) || pattern[index] >= 128 {
				return nil, false
			}
			fallthrough
		default:
			var set [128]bool
			set[pattern[index]] = true
			tokens = append(tokens, globToken{set: set})
		}
	}
	return tokens, true
}

func parseGlobClass(class string) ([128]bool, bool) {
	var set [128]bool
	invert := strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^")
	if invert {
		class = class[1:]
	}
	if class == "" {
		return set, false
	}
	for index := 0; index < len(class); index++ {
		if class[index] >= 128 {
			return set, false
		}
		start := class[index]
		if index+2 < len(class) && class[index+1] == '-' {
			end := class[index+2]
			if end >= 128 || start > end {
				return set, false
			}
			for character := start; character <= end; character++ {
				set[character] = true
			}
			index += 2
			continue
		}
		set[start] = true
	}
	if invert {
		for index := range set {
			set[index] = !set[index]
		}
	}
	return set, true
}

func globSetsIntersect(left, right [128]bool) bool {
	for index := range left {
		if left[index] && right[index] {
			return true
		}
	}
	return false
}

func patternMatchesPathOrAncestor(pattern excludePattern, path string) bool {
	path = normalizeRel(path)
	if pattern.matches(path, false) {
		return true
	}
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		if pattern.matches(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return false
}

// commonArgs returns the rsync flags shared between pull and push.
// Empty paths inside rf are skipped.
//
// Filter order is first-match-wins, safety first:
//  0. tombstones (paths deleted locally) — first so the include layer cannot
//     re-admit them and the pull cannot restore what was just deleted
//  1. always-on state paths (/.dotfiles/, /inbox/gdrive/)
//  2. submodule excludes (submodules sync through Git)
//     2b. linked-worktree excludes (peer profile only — branch checkouts whose
//     content travels through Git)
//  3. allow.txt re-includes — the only way secrets sync — plus the
//     env-template builtins
//  4. hardcoded secrets excludes
//  5. exclude.txt (junk), 6. ignore.txt (user), 7. shared-folder excludes
//  8. --include=*/ (traversal), 9. tracked ∪ baseline includes,
//  10. binary-extension allowlist, 11. --exclude=* catch-all
//
// .gitignore is intentionally not a sync filter because gitignored binaries
// are a primary sync payload. Exclude mode stops after layer 7 (everything
// not excluded syncs).
func commonArgs(cfg *Config, rf runtimeFilters) []string {
	args := []string{
		"-a",
		"--human-readable",
		"--stats",
		"--no-links",
		// A source directory without owner-write is recreated with that mode on
		// the receiver, and rsync then cannot mkstemp inside it: "Permission
		// denied (13)" and exit 23. Measured: one dr-xr-xr-x directory in an 88 GB
		// tree held the only two files that failed a full transfer. Adding
		// owner-write to received directories costs one mode bit and removes the
		// whole failure class; file modes are untouched.
		"--chmod=Du+w",
	}
	if rf.TombstonesDyn != "" {
		args = append(args, "--exclude-from="+rf.TombstonesDyn)
	}
	args = append(args, alwaysExcludeArgs()...)
	if rf.SubmodulesDyn != "" {
		args = append(args, "--exclude-from="+rf.SubmodulesDyn)
	}
	if rf.WorktreesDyn != "" {
		args = append(args, "--exclude-from="+rf.WorktreesDyn)
	}
	args = append(args, secretsFilterArgs(cfg.AllowPatterns)...)
	excludeFiles := []string{cfg.ExcludesFile, cfg.IgnoreFile, rf.SharedDyn}
	for _, f := range excludeFiles {
		if f == "" {
			continue
		}
		args = append(args, "--exclude-from="+f)
	}
	if normalizeFilterMode(cfg.FilterMode) == FilterModeInclude {
		args = append(args, "--include=*/")
		if rf.TrackedDyn != "" {
			args = append(args, "--include-from="+rf.TrackedDyn)
		}
		for _, p := range cfg.IncludePatterns {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			args = append(args, "--include="+rsyncCaseFoldPattern(p))
		}
		args = append(args, "--exclude=*")
	}
	if cfg.Verbose {
		args = append(args, "--progress")
	}
	return args
}

// worktreeRoots are the directories where coding agents park linked Git
// worktrees (Claude Code, Qwen Code, the common .worktrees convention). A
// worktree is a branch checkout whose .git gitlink never travels, so a copy on
// the target is a dead husk, and its bulk create/remove would trip max_delete
// on every agent cleanup. Its content reaches the other machine through Git.
var worktreeRoots = []string{".claude/worktrees", ".qwen/worktrees", ".worktrees"}

func alwaysExcludeArgs() []string {
	args := []string{
		"--exclude=/.dotfiles/",
		"--exclude=/inbox/gdrive/",
		// .git (dir or gitlink file, any depth) is enforced in code, not just
		// the editable exclude.txt — an operator edit must never leak VCS
		// internals to the target.
		"--exclude=.git",
	}
	// No trailing slash, like .git: isAlwaysExcluded matches files too, and a
	// rule only one side applies turns into a propagated delete.
	for _, root := range worktreeRoots {
		args = append(args, "--exclude="+root)
	}
	return args
}

// secretsFilterArgs renders the allow.txt re-includes (with parent-dir
// includes so rsync can descend into otherwise-excluded directories),
// the env-template builtins, then the hardcoded secrets excludes.
func secretsFilterArgs(allowPatterns []string) []string {
	var args []string
	for _, p := range allowPatterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		for _, dir := range allowParentDirs(p) {
			args = append(args, "--include="+dir)
		}
		args = append(args, "--include="+p)
	}
	for _, p := range secretAllowBuiltins {
		args = append(args, "--include="+p)
	}
	for _, p := range secretExcludePatterns {
		args = append(args, "--exclude="+p)
	}
	return args
}

// allowParentDirs returns anchored directory includes for every literal
// parent of an anchored allow pattern, so `/​.maru/secrets/app.token` also
// re-includes `/.maru/` and `/.maru/secrets/`. Parents containing wildcards
// are skipped (rsync would treat them literally).
func allowParentDirs(pattern string) []string {
	if !strings.HasPrefix(pattern, "/") {
		return nil
	}
	trimmed := strings.TrimPrefix(strings.TrimSuffix(pattern, "/"), "/")
	segs := strings.Split(trimmed, "/")
	var dirs []string
	prefix := ""
	for _, seg := range segs[:len(segs)-1] {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		prefix += "/" + seg
		dirs = append(dirs, prefix+"/")
	}
	return dirs
}

func rsyncCaseFoldPattern(pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteRune(r - 'a' + 'A')
			b.WriteByte(']')
		case r >= 'A' && r <= 'Z':
			b.WriteByte('[')
			b.WriteRune(r + 'a' - 'A')
			b.WriteRune(r)
			b.WriteByte(']')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Cleanup removes anything prepareRuntimeFilters created outside the workspace
// for this run. Safe on the zero value and safe to call more than once.
func (rf runtimeFilters) Cleanup() {
	if rf.cleanup != nil {
		rf.cleanup()
	}
}
