// Package toolversion matches stable X.Y.Z versions against ranges and reads the
// manifest a family tool (amem, vmem) publishes about itself, so pins and tool
// lists are data rather than constants compiled into dot.
package toolversion

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var versionToken = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// parse reads a stable X.Y.Z (an optional leading v). A prerelease or build
// suffix, a leading zero or anything else is not a stable version.
func parse(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return out, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func compare(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Find returns the first whole stable version in command output ("amem
// 0.17.0", "codex-cli 0.166.0"). A number that is part of a longer one
// (10.11.0 is not 0.11.0, 1.0.11.0 is no version) or carries a prerelease or
// build suffix (-rc1, +build) is skipped. A sentence-ending dot is allowed.
func Find(output string) string {
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	for _, loc := range versionToken.FindAllStringIndex(output, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && (isDigit(output[start-1]) || output[start-1] == '.') {
			continue
		}
		if end < len(output) && (output[end] == '-' || output[end] == '+') {
			continue
		}
		if end+1 < len(output) && output[end] == '.' && isDigit(output[end+1]) {
			continue
		}
		if _, ok := parse(output[start:end]); ok {
			return output[start:end]
		}
	}
	return ""
}

// ValidSpec reports whether spec is a bare X.Y.Z or a range Satisfies can read.
func ValidSpec(spec string) bool {
	_, ok := comparators(spec)
	return ok
}

type comparator struct {
	op string
	v  [3]int
}

func comparators(spec string) ([]comparator, bool) {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return nil, false
	}
	out := make([]comparator, 0, len(fields))
	for _, f := range fields {
		op := ""
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(f, candidate) {
				op = candidate
				break
			}
		}
		v, ok := parse(strings.TrimPrefix(f, op))
		if !ok {
			return nil, false
		}
		if op == "" {
			op = "="
		}
		out = append(out, comparator{op, v})
	}
	return out, true
}

// Satisfies reports whether version is inside spec: a bare X.Y.Z is an exact
// match, otherwise every space-separated comparator (>=, >, <=, <, =) must
// hold, as in ">=0.14.0 <1.0.0". An unreadable spec or version never matches.
func Satisfies(spec, version string) bool {
	v, ok := parse(version)
	if !ok {
		return false
	}
	cs, ok := comparators(spec)
	if !ok {
		return false
	}
	for _, c := range cs {
		d := compare(v, c.v)
		hold := map[string]bool{">=": d >= 0, ">": d > 0, "<=": d <= 0, "<": d < 0, "=": d == 0}[c.op]
		if !hold {
			return false
		}
	}
	return true
}

// Manifest is what a tool publishes about the installed release. A zero field
// means the tool did not publish it, and the caller uses its compiled fallback.
type Manifest struct {
	// Version is the installed release.
	Version string
	// Supported is the version range the tool declares (for example the
	// releases that may share its home), empty when not published or unreadable.
	Supported string
	// Tools are the MCP tool names the release serves, nil when not published.
	Tools []string
	// Source is "manifest" (`<tool> manifest --json`), "package.json" (the
	// installed package's version only) or "" (nothing found).
	Source string
}

// manifestJSON is the reader contract for `<tool> manifest --json`. Unknown
// keys are ignored, so a tool may publish more than dot reads.
type manifestJSON struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Supported string `json:"supported"`
	MCP       struct {
		Tools []json.RawMessage `json:"tools"`
	} `json:"mcp"`
}

const manifestLimit = 1 << 20

// Read asks bin for `manifest --json`. When that fails, is not JSON, names
// another package or carries no stable version, it reads the version from the
// installed package.json named pkg, found by resolving bin's link and walking
// up. A missing manifest is never an error: the result is then partial or zero.
func Read(ctx context.Context, bin, pkg string) Manifest {
	if bin == "" {
		return Manifest{}
	}
	if m, ok := fromCommand(ctx, bin, pkg); ok {
		return m
	}
	if v := fromPackageJSON(bin, pkg); v != "" {
		return Manifest{Version: v, Source: "package.json"}
	}
	return Manifest{}
}

func fromCommand(ctx context.Context, bin, pkg string) (Manifest, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "manifest", "--json")
	var out bytes.Buffer
	cmd.Stdout = &limited{&out}
	if cmd.Run() != nil {
		return Manifest{}, false
	}
	var raw manifestJSON
	if json.Unmarshal(out.Bytes(), &raw) != nil {
		return Manifest{}, false
	}
	if (raw.Name != "" && raw.Name != pkg) || Find(raw.Version) != raw.Version || raw.Version == "" {
		return Manifest{}, false
	}
	m := Manifest{Version: raw.Version, Source: "manifest"}
	if ValidSpec(raw.Supported) {
		m.Supported = raw.Supported
	}
	for _, item := range raw.MCP.Tools {
		var name string
		if json.Unmarshal(item, &name) != nil {
			var named struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(item, &named) != nil {
				continue
			}
			name = named.Name
		}
		if name != "" {
			m.Tools = append(m.Tools, name)
		}
	}
	return m, true
}

func fromPackageJSON(bin, pkg string) string {
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(resolved)
	for i := 0; i < 6; i++ {
		raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil && len(raw) <= manifestLimit {
			var p struct{ Name, Version string }
			if json.Unmarshal(raw, &p) == nil && p.Name == pkg {
				if _, ok := parse(p.Version); ok {
					return p.Version
				}
				return ""
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

type limited struct{ b *bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := manifestLimit - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}
