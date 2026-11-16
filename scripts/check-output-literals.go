//go:build ignore

// check-output-literals fails the build when human output is hand-built from
// literals that internal/ui owns. It tokenizes every Go file, so comments never
// match and multi-line raw strings are measured whole.
//
//	go run scripts/check-output-literals.go [-root DIR]
//
// Findings, all outside internal/ui and outside _test.go files:
//   - a marker glyph embedded in a longer string literal
//   - a rune literal holding a marker glyph
//   - the bullet or info glyph in any string literal
//   - a string literal carrying a warning:, Warning:, Note:, note:, Tip: or
//     [dry-run] prefix word
//
// The bare "glyph" literal case stays in check-glyph-alphabet.sh, which also
// scans tests. Source here spells every glyph as an escape so the checker does
// not trip itself.
package main

import (
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// markerGlyphs are the glyphs of internal/ui/markers.go that are not ordinary
// punctuation: check, cross, middle dot, star, diamond, warning sign, section.
const markerGlyphs = "\u2713\u2717\u00b7\u2605\u25c6\u26a0\u25b8"

// nextGlyph is the one marker glyph left out of markerGlyphs on purpose: the
// arrow appears in ordinary prose and path-mapping literals, the same way the
// tilde does for check-glyph-alphabet.sh, so including it would keep this gate
// permanently red on correct code.
const nextGlyph = "\u2192"

// bannedGlyphs are never allowed outside internal/ui: bullet and info.
const bannedGlyphs = "\u2022\u2139"

var prefixPattern = regexp.MustCompile(`(^|[^A-Za-z])((warning|Warning|Note|note|Tip):|\[dry-run\])`)

// allowed lists the classes of text the issue (#450) names as not human report
// output. Each entry needs a reason; an entry matches a file suffix and a
// substring of the literal's value.
var allowed = []struct{ file, contains, reason string }{}

type finding struct {
	file string
	line int
	text string
	why  string
}

func main() {
	root := flag.String("root", ".", "repository root to scan")
	flag.Parse()
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fatal(err)
	}
	var files []string
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fatal(err)
	}
	sort.Strings(files)

	checkAlphabet(absRoot)
	checkAllowList()

	var findings []finding
	measured := 0
	for _, path := range files {
		rel, _ := filepath.Rel(absRoot, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/ui/") || strings.HasPrefix(rel, "scripts/") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			fatal(err)
		}
		measured++
		findings = append(findings, scan(rel, src)...)
	}
	if measured == 0 {
		fmt.Fprintln(os.Stderr, "check-output-literals: no Go files measured, so this check would report success without measuring anything")
		os.Exit(1)
	}
	fmt.Printf("check-output-literals: scanned %d Go file(s)\n", measured)
	if len(findings) > 0 {
		for _, f := range findings {
			fmt.Fprintf(os.Stderr, "check-output-literals: %s:%d: %s: %s\n", f.file, f.line, f.why, f.text)
		}
		fmt.Fprintln(os.Stderr, "check-output-literals: use the internal/ui helpers (Mark*, WriteWarning, WriteNote, WriteDryRun) instead of literals")
		fmt.Fprintf(os.Stderr, "check-output-literals: %d finding(s)\n", len(findings))
		os.Exit(1)
	}
	fmt.Printf("check-output-literals: %d Go file(s) measured; no embedded glyph or prefix literals outside internal/ui\n", measured)
}

// checkAlphabet is the precondition that keeps this gate honest: every
// non-ASCII glyph that internal/ui/markers.go defines must be covered by
// markerGlyphs, or by the one documented exclusion. A green verdict from a
// glyph set that has drifted from the alphabet would be success having
// measured nothing.
func checkAlphabet(root string) {
	path := filepath.Join(root, "internal", "ui", "markers.go")
	src, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	fset := token.NewFileSet()
	file := fset.AddFile(path, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	seen := 0
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.STRING {
			continue
		}
		value, err := strconv.Unquote(lit)
		if err != nil || len([]rune(value)) != 1 || value[0] < 0x80 {
			continue
		}
		seen++
		if !strings.Contains(markerGlyphs, value) && value != nextGlyph {
			fmt.Fprintf(os.Stderr, "check-output-literals: internal/ui/markers.go defines %q, which markerGlyphs does not cover\n", value)
			os.Exit(1)
		}
	}
	if seen == 0 {
		fmt.Fprintln(os.Stderr, "check-output-literals: found no glyph in internal/ui/markers.go; would otherwise report success having measured nothing")
		os.Exit(1)
	}
}

func checkAllowList() {
	for _, a := range allowed {
		if strings.TrimSpace(a.reason) == "" || a.file == "" || a.contains == "" {
			fmt.Fprintf(os.Stderr, "check-output-literals: allow-list entry %q needs a file, a substring and a reason\n", a.file)
			os.Exit(1)
		}
	}
}

func scan(rel string, src []byte) []finding {
	var out []finding
	fset := token.NewFileSet()
	file := fset.AddFile(rel, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return out
		}
		if tok != token.STRING && tok != token.CHAR {
			continue
		}
		value, err := strconv.Unquote(lit)
		if err != nil {
			value = lit
		}
		line := fset.Position(pos).Line
		report := func(why string) {
			if isAllowed(rel, value) {
				return
			}
			out = append(out, finding{rel, line, snippet(value), why})
		}
		switch {
		case strings.ContainsAny(value, bannedGlyphs):
			report("bullet or info glyph outside internal/ui")
		case tok == token.CHAR && strings.ContainsAny(value, markerGlyphs):
			report("marker glyph as a rune literal")
		case tok == token.STRING && strings.ContainsAny(value, markerGlyphs) && len([]rune(strings.TrimSpace(value))) > 1:
			report("marker glyph embedded in a longer string")
		}
		if tok == token.STRING && prefixPattern.MatchString(value) {
			report("hand-written warning, note, tip or dry-run prefix")
		}
	}
}

func isAllowed(rel, value string) bool {
	for _, a := range allowed {
		if strings.HasSuffix(rel, a.file) && strings.Contains(value, a.contains) {
			return true
		}
	}
	return false
}

func snippet(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > 80 {
		value = string([]rune(value)[:80]) + "..."
	}
	return strconv.Quote(value)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "check-output-literals:", err)
	os.Exit(1)
}
