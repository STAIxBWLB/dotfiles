package remote

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
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
		fmt.Fprintf(&b, "for dir in %s/*/; do if [ -d \"$dir\" ] && [ ! -L \"${dir%%/}\" ]; then printf 'job\\t%%s\\t%%s\\n' %s \"${dir%%/}\"; fi; done 2>/dev/null\n",
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
    [ -L "$repo" ] && continue
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
%s
exit 0`

// pruneMirrorTemplate removes one mirror only while no run holds it: it takes
// the mirror's claim itself, so a run starting now defers instead of syncing
// into a directory being deleted.
const pruneMirrorTemplate = `records="$HOME"/%s/.dot-remote
lock="$records"/%s.claim
root="$HOME"/%s
rel=%s
target="$root/$rel"
walk="$root"
set -f
IFS=/
for part in $rel; do
  walk="$walk/$part"
  if [ -L "$walk" ]; then echo "refused: $walk is a symlink, so removing it could leave the mirror root; check it and remove it by hand" >&2; exit 4; fi
done
unset IFS
set +f
if ! mkdir "$lock" 2>/dev/null; then echo 'busy: a run holds the claim on this mirror; run dot remote prune again after it ends' >&2; exit 3; fi
rm -rf "$target" "$records"/%s
status=$?
rmdir "$lock"
exit $status`

// pruneRefTemplate deletes one leftover verify ref, unless the verify that
// pushed it is still running.
const pruneRefTemplate = `if ! running=$(%s ps --filter %s --format '{{.Names}}' 2>&1); then echo "cannot tell whether that verify is running ($running); run dot remote prune again once the runtime answers" >&2; exit 3; fi
if [ -n "$running" ]; then echo 'busy: that verify is still running; run dot remote prune again after it ends' >&2; exit 3; fi
git -C "$HOME"/%s update-ref -d %s`

// pruneContainerTemplate removes one stopped container, unless it was
// restarted since the listing.
const pruneContainerTemplate = `if ! running=$(%s ps --filter %s --format '{{.Names}}' 2>&1); then echo "cannot tell whether the container is running ($running); run dot remote prune again once the runtime answers" >&2; exit 3; fi
if [ -n "$running" ]; then echo 'busy: the container is running again; leave it, or stop it and run dot remote prune again' >&2; exit 3; fi
%s rm %s`

// PruneListScript is the read-only listing prune runs on a host.
func PruneListScript(h Host) string { return pruneListScriptFor(h) }

// pruneListScriptFor is the read-only listing for one host.
func pruneListScriptFor(h Host) string {
	return fmt.Sprintf(pruneListTemplate, ShellQuote(h.Root), ShellQuote(h.GitRoot), ShellQuote(h.GitRoot),
		h.Runtime, ShellQuote("name="+h.Prefix+"-"), jobListScript(h))
}

// pruneMirrorScript removes one mirror under its own claim.
func pruneMirrorScript(h Host, rel string) string {
	key := MirrorKey(rel)
	return fmt.Sprintf(pruneMirrorTemplate, ShellQuote(h.Root), key, ShellQuote(h.Root), ShellQuote(rel), key)
}

var (
	hexKey      = regexp.MustCompile(`^[0-9a-f]{12}$`)
	srcNameForm = regexp.MustCompile(`^(root|[A-Za-z0-9][A-Za-z0-9._-]*)$`)
)

// dotContainer reports whether name is one of the containers dot itself
// names on this host: <prefix>-<key> (run), <prefix>-verify-<key>,
// <prefix>-src-<name|root>, or <prefix>-job-<profile>_<name> for one of this
// host's profiles. The prefix is compared literally; the runtime's own filter
// is a regex and only narrows the listing.
func dotContainer(h Host, name string) bool {
	rest, ok := strings.CutPrefix(name, h.Prefix+"-")
	if !ok {
		return false
	}
	switch {
	case hexKey.MatchString(rest):
		return true
	case strings.HasPrefix(rest, "verify-"):
		return hexKey.MatchString(strings.TrimPrefix(rest, "verify-"))
	case strings.HasPrefix(rest, "src-"):
		return srcNameForm.MatchString(strings.TrimPrefix(rest, "src-"))
	case strings.HasPrefix(rest, "job-"):
		profile, job, ok := strings.Cut(strings.TrimPrefix(rest, "job-"), "_")
		if !ok {
			return false
		}
		_, known := h.Jobs[profile]
		return known && jobNamePattern.MatchString(job)
	}
	return false
}

// mirrorRecord is one mirror the host has a record of.
type mirrorRecord struct {
	key, rel string
	claimed  bool
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
	// The mirror paths this machine holds a lease on, as relative paths, and
	// their keys: a verify pushes its refs before its container starts, so a
	// ref of a held key may belong to a verify that is about to fetch it.
	heldRels := map[string]bool{}
	heldKeys := map[string]bool{}
	if d.Store != nil {
		if leases, lerr := d.Store.ListLeases(); lerr == nil {
			for _, lease := range leases {
				if rel, rerr := cfg.Rel(lease.Scope); rerr == nil {
					heldRels[rel] = true
					heldKeys[MirrorKey(rel)] = true
				}
			}
		}
	}
	var mirrors []mirrorRecord
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
			mirrors = append(mirrors, mirrorRecord{key: key, rel: rel, claimed: claim == "claimed"})
		case "ref":
			if len(fields) < 3 || !strings.HasPrefix(fields[2], "refs/verify/") {
				continue
			}
			refKey := strings.SplitN(strings.TrimPrefix(fields[2], "refs/verify/"), "/", 2)[0]
			if heldKeys[refKey] {
				report.Skipped = append(report.Skipped, fmt.Sprintf("ref %s: this machine is verifying that checkout now", fields[2]))
				continue
			}
			report.Items = append(report.Items, PruneItem{Kind: "verify-ref", Name: fields[1] + " " + fields[2], key: fields[1],
				Why: "left by a verify that did not finish"})
		case "container":
			if len(fields) < 3 || !dotContainer(h, fields[1]) {
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
	// A mirror is removable only when no claimed record and no held lease sits
	// at its path or below it: rm -rf of a parent takes a nested mirror with it.
	busy := map[string]bool{}
	for _, m := range mirrors {
		if m.claimed {
			busy[m.rel] = true
		}
	}
	for rel := range heldRels {
		busy[rel] = true
	}
	for _, m := range mirrors {
		local := filepath.Join(cfg.LocalRoot, filepath.FromSlash(m.rel))
		if _, err := os.Stat(local); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			report.Skipped = append(report.Skipped, fmt.Sprintf("mirror %s: cannot tell whether %s exists (%v)", m.rel, local, err))
			continue
		}
		holder := ""
		for rel := range busy {
			if rel == m.rel || strings.HasPrefix(rel, m.rel+"/") {
				holder = rel
				break
			}
		}
		if holder != "" {
			report.Skipped = append(report.Skipped, fmt.Sprintf("mirror %s: a run holds %s; run dot remote prune again after it ends", m.rel, holder))
			continue
		}
		report.Items = append(report.Items, PruneItem{Kind: "mirror", Name: m.rel, key: m.key,
			Why: fmt.Sprintf("the local checkout %s is gone", local)})
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
