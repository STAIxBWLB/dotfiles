package remote

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// PruneOptions is one `dot remote prune` request.
type PruneOptions struct {
	// Yes deletes what the listing shows; without it prune only lists.
	Yes bool
}

// PruneItem is one thing prune found to remove.
type PruneItem struct {
	// Kind is "mirror", "verify-ref" or "container".
	Kind string `json:"kind"`
	// Name is the mirror's relative path, the ref (bare repo and ref name), or
	// the container name.
	Name string `json:"name"`
	// Why says what makes it removable.
	Why string `json:"why"`
	// Removed is set after a --yes run that removed it; Error when it could not.
	Removed bool   `json:"removed,omitempty"`
	Error   string `json:"error,omitempty"`
	// key is the mirror key or bare repo, for the removal script.
	key string
}

// PruneReport is what prune found on one host, and what it removed.
type PruneReport struct {
	Host  string      `json:"host"`
	Items []PruneItem `json:"items"`
	// Skipped names what prune left alone and why (a claimed mirror, a record
	// that does not read as a path).
	Skipped []string `json:"skipped,omitempty"`
	// Kept are job directories `dot remote job` kept after a failure. They are
	// listed for a person to read or remove, never removed by prune: their
	// out/ may be the only copy of what the job made.
	Kept []KeptJob `json:"kept_jobs,omitempty"`
}

// KeptJob is one kept job directory on the host.
type KeptJob struct {
	Profile string `json:"profile"`
	Dir     string `json:"dir"`
}

// jobListScript lists the job directories under each profile root.
func jobListScript(h Host) string {
	var b strings.Builder
	for _, name := range sortedKeys(h.Jobs) {
		p := h.Jobs[name]
		fmt.Fprintf(&b, "for dir in %s/*/; do [ -d \"$dir\" ] && printf 'job\\t%%s\\t%%s\\n' %s \"${dir%%/}\"; done 2>/dev/null\n",
			rootExpr(p.Root), ShellQuote(name))
	}
	return b.String()
}

// pruneListTemplate lists, read-only, the mirror records with whether each is
// claimed, the leftover verify refs, and this prefix's containers that are not
// running.
const pruneListTemplate = `records="$HOME"/%s/.dot-remote
if [ -d "$records" ]; then
  for record in "$records"/*; do
    [ -f "$record" ] || continue
    key=${record##*/}
    rel=$(cat "$record" 2>/dev/null) || continue
    if [ -d "$records/$key.claim" ]; then claim=claimed; else claim=free; fi
    printf 'mirror\t%%s\t%%s\t%%s\n' "$key" "$claim" "$rel"
  done
fi
if [ -d "$HOME"/%s ]; then
  for repo in "$HOME"/%s/*.git; do
    [ -d "$repo" ] || continue
    git -C "$repo" for-each-ref --format='%%(refname)' refs/verify 2>/dev/null | while read -r ref; do
      printf 'ref\t%%s\t%%s\n' "${repo##*/}" "$ref"
    done
  done
fi
if ! listed=$(%s ps -a --filter %s --format '{{.Names}}\t{{.Status}}' 2>&1); then
  printf 'error\tcannot list containers (%%s)\n' "$listed"
else
  printf '%%s\n' "$listed" | while IFS='	' read -r name status; do
    [ -n "$name" ] || continue
    case "$status" in Up*|up*|Running*|running*) continue;; esac
    printf 'container\t%%s\t%%s\n' "$name" "$status"
  done
fi
%s`

// pruneMirrorTemplate removes one mirror only while no run holds it: it takes
// the mirror's claim itself, so a run starting now defers instead of syncing
// into a directory being deleted.
const pruneMirrorTemplate = `records="$HOME"/%s/.dot-remote
lock="$records"/%s.claim
if ! mkdir "$lock" 2>/dev/null; then echo 'busy: a run holds the claim'; exit 3; fi
rm -rf "$HOME"/%s "$records"/%s
status=$?
rmdir "$lock"
exit $status`

// pruneRefTemplate deletes one leftover verify ref, unless the verify that
// pushed it is still running.
const pruneRefTemplate = `if [ -n "$(%s ps --filter %s --format '{{.Names}}' 2>/dev/null)" ]; then echo 'busy: that verify is still running'; exit 3; fi
git -C "$HOME"/%s update-ref -d %s`

// pruneContainerTemplate removes one stopped container, unless it was
// restarted since the listing.
const pruneContainerTemplate = `if [ -n "$(%s ps --filter %s --format '{{.Names}}' 2>/dev/null)" ]; then echo 'busy: the container is running again'; exit 3; fi
%s rm %s`

// PruneListScript is the read-only listing prune runs on a host.
func PruneListScript(h Host) string { return pruneListScriptFor(h) }

// pruneListScriptFor is the read-only listing for one host.
func pruneListScriptFor(h Host) string {
	return fmt.Sprintf(pruneListTemplate, ShellQuote(h.Root), ShellQuote(h.GitRoot), ShellQuote(h.GitRoot),
		h.Runtime, ShellQuote("name=^"+h.Prefix+"-"), jobListScript(h))
}

// pruneMirrorScript removes one mirror under its own claim.
func pruneMirrorScript(h Host, rel string) string {
	key := MirrorKey(rel)
	return fmt.Sprintf(pruneMirrorTemplate, ShellQuote(h.Root), key, ShellQuote(h.MirrorDir(rel)), key)
}

// safeRel reports whether a record's path can name a mirror below the root.
func safeRel(rel string) bool {
	if rel == "" || rel == "." || path.IsAbs(rel) || strings.ContainsAny(rel, "\n\x00") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." || part == "" {
			return false
		}
	}
	return true
}

// Prune lists, and with Yes removes, what runs left behind on the host:
// mirrors whose local checkout (local_root/<path>) is gone, verify refs a
// killed verify did not delete, and this prefix's containers that are not
// running. A mirror another run holds, or one this machine holds a lease on,
// is skipped.
func (d Deps) Prune(ctx context.Context, cfg Config, h Host, opts PruneOptions) (PruneReport, error) {
	report := PruneReport{Host: h.Name, Items: []PruneItem{}}
	out, err := d.capture(ctx, SSHArgv(h.SSH, pruneListScriptFor(h)), nil)
	if err != nil {
		return report, fmt.Errorf("listing %s: %w", h.Name, err)
	}
	held := map[string]bool{}
	if d.Store != nil {
		if leases, lerr := d.Store.ListLeases(); lerr == nil {
			for _, lease := range leases {
				held[lease.Scope] = true
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch fields[0] {
		case "mirror":
			if len(fields) < 4 {
				continue
			}
			key, claim, rel := fields[1], fields[2], strings.Join(fields[3:], "\t")
			if !safeRel(rel) || MirrorKey(rel) != key {
				report.Skipped = append(report.Skipped, fmt.Sprintf("record %s does not name a mirror path (%q)", key, rel))
				continue
			}
			local := filepath.Join(cfg.LocalRoot, filepath.FromSlash(rel))
			if _, err := os.Stat(local); err == nil {
				continue
			}
			if claim == "claimed" || held[PathScope(local)] {
				report.Skipped = append(report.Skipped, fmt.Sprintf("mirror %s: a run holds it", rel))
				continue
			}
			report.Items = append(report.Items, PruneItem{Kind: "mirror", Name: rel, key: key,
				Why: fmt.Sprintf("the local checkout %s is gone", local)})
		case "ref":
			if len(fields) < 3 || !strings.HasPrefix(fields[2], "refs/verify/") {
				continue
			}
			report.Items = append(report.Items, PruneItem{Kind: "verify-ref", Name: fields[1] + " " + fields[2], key: fields[1],
				Why: "left by a verify that did not finish"})
		case "container":
			if len(fields) < 3 {
				continue
			}
			report.Items = append(report.Items, PruneItem{Kind: "container", Name: fields[1],
				Why: "not running (" + fields[2] + ")"})
		case "job":
			if len(fields) < 3 {
				continue
			}
			report.Kept = append(report.Kept, KeptJob{Profile: fields[1], Dir: fields[2]})
		case "error":
			if len(fields) > 1 {
				return report, fmt.Errorf("%s: %s", h.Name, fields[1])
			}
		}
	}
	sort.SliceStable(report.Items, func(i, j int) bool {
		if report.Items[i].Kind != report.Items[j].Kind {
			return report.Items[i].Kind < report.Items[j].Kind
		}
		return report.Items[i].Name < report.Items[j].Name
	})
	if !opts.Yes {
		return report, nil
	}
	for i := range report.Items {
		item := &report.Items[i]
		var script string
		switch item.Kind {
		case "mirror":
			script = pruneMirrorScript(h, item.Name)
		case "verify-ref":
			ref := strings.TrimPrefix(item.Name, item.key+" ")
			key := strings.SplitN(strings.TrimPrefix(ref, "refs/verify/"), "/", 2)[0]
			script = fmt.Sprintf(pruneRefTemplate, h.Runtime, ShellQuote("name=^"+h.Prefix+"-verify-"+key+"$"),
				ShellQuote(path.Join(h.GitRoot, item.key)), ShellQuote(ref))
		case "container":
			script = fmt.Sprintf(pruneContainerTemplate, h.Runtime, ShellQuote("name=^"+item.Name+"$"), h.Runtime, ShellQuote(item.Name))
		}
		if _, err := d.capture(ctx, SSHArgv(h.SSH, script), nil); err != nil {
			item.Error = err.Error()
			continue
		}
		item.Removed = true
		if item.Kind == "mirror" {
			if sent := d.sentPath(h, item.Name); sent != "" {
				_ = os.Remove(sent)
			}
		}
	}
	return report, nil
}
