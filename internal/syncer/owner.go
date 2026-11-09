package syncer

import (
	"fmt"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
)

// Owner guards a sync profile against having two writers.
//
// Why this exists: the cloud mirror is a single-writer channel. Each machine
// keeps its own baseline manifest and the mirror profile runs with delete
// propagation on, so two machines pushing the same mirror do not merge - they
// take turns undoing each other, and `--max-delete` is the only thing standing
// between that and mass deletion. This was not theoretical: a second Mac came
// up with the same scheduled push installed and immediately contended for the
// same Dropbox folder.
//
// The guard is deliberately opt-in (empty Owner = unrestricted) so upgrading
// `dot` does not break a workspace that has not declared an owner yet.
// `dot sync setup` is different: it explicitly installs the unattended writer
// described above, so it requires an owner before that scheduler can run.
// Existing unowned workspaces keep their unrestricted push and pull behavior;
// this setup-time requirement never fires merely because an upgrade occurred.

// scutilPath is where macOS keeps scutil. Resolving it by PATH is not safe:
// see MachineNames.
const scutilPath = "/usr/sbin/scutil"

// ShortHostname returns the machine's hostname without its domain suffix.
// launchd and Bonjour hand back "foo.local" in some contexts and "foo" in
// others, so compare on the short form only.
func ShortHostname() (string, error) {
	h, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return NormalizeHostname(h), nil
}

// MachineNames returns every name that identifies this machine, normalized.
//
// os.Hostname() alone is not a safe identity on macOS. A Mac whose HostName was
// never set reports the literal default "Mac" - measured on a current machine -
// so two such Macs would both answer to the same owner string and the guard
// would grant write access to both. That is the exact failure it exists to stop.
//
// LocalHostName and ComputerName are the names an operator recognizes
// ("Youngs-MacBook-Pro"), so accepting any of them lets the config name a
// specific machine without needing sudo to fix HostName. Matching a set only
// widens acceptance for THIS machine; it never admits a different one, because
// the configured owner still has to equal one of these exact names.
func MachineNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = NormalizeHostname(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	if h, err := os.Hostname(); err == nil {
		add(h)
	}
	if runtime.GOOS == "darwin" {
		for _, key := range []string{"LocalHostName", "ComputerName"} {
			// Absolute path, not PATH lookup: launchd hands agents a minimal
			// PATH (/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin) with no
			// /usr/sbin, so a bare "scutil" resolves interactively and fails
			// under the scheduler. That asymmetry made the guard reject the
			// very machine that owned the profile, and only in the scheduled
			// run - the interactive check kept reporting success.
			if b, err := osexec.Command(scutilPath, "--get", key).Output(); err == nil {
				add(string(b))
			}
		}
	}
	return out
}

// PreferredMachineName picks the best name to record as an owner.
//
// The candidates are not equally good. os.Hostname() can be the generic default
// "Mac", which another machine could also answer to. ComputerName is the Finder
// display name and may contain spaces and a curly apostrophe ("Young's MacBook
// Pro") - legal in YAML but a poor identifier to type or diff. LocalHostName is
// the DNS-safe form ("youngs-macbook-pro"), which is what we want.
//
// So: prefer a specific, DNS-safe name; fall back to whatever exists.
func PreferredMachineName() string {
	names := MachineNames()
	if len(names) == 0 {
		return ""
	}
	generic := genericMachineNames
	best := ""
	for _, n := range names {
		if generic[n] {
			continue
		}
		if !dnsSafeName(n) {
			continue
		}
		if len(n) > len(best) {
			best = n
		}
	}
	if best != "" {
		return best
	}
	// Nothing DNS-safe and specific: take the first non-generic, else the first.
	for _, n := range names {
		if !generic[n] {
			return n
		}
	}
	return names[0]
}

func dnsSafeName(n string) bool {
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return n != ""
}

// NormalizeHostname lowercases and strips the domain part.
func NormalizeHostname(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.Index(h, "."); i > 0 {
		h = h[:i]
	}
	return h
}

// ownersMatch reports whether owner, or one of its recorded aliases, is one
// of names (compared normalized).
func ownersMatch(owner string, aliases []string, names ...string) bool {
	for _, want := range append([]string{owner}, aliases...) {
		want = NormalizeHostname(want)
		if want == "" {
			continue
		}
		for _, n := range names {
			if NormalizeHostname(n) == want {
				return true
			}
		}
	}
	return false
}

// sameOwner reports whether two profiles name the same owner: the owners are
// equal, or one side records the other's owner as an earlier name. Two
// machines migrated one after the other by `dot sync owner --rename` agree
// throughout. Aliases alone never match each other: two different current
// owners that share an old name are two coordinators, and the fence must
// refuse them.
func sameOwner(a string, aAliases []string, b string, bAliases []string) bool {
	a, b = NormalizeHostname(a), NormalizeHostname(b)
	if a == "" || b == "" {
		return false
	}
	return a == b || ownersMatch("", aAliases, b) || ownersMatch("", bAliases, a)
}

// AssignOwner sets the profile owner. A different owner is a new decision,
// not a rename, so the earlier names recorded for the old one are dropped.
func AssignOwner(local *LocalConfig, owner string) {
	if NormalizeHostname(owner) != NormalizeHostname(local.Owner) {
		local.OwnerAliases = nil
	}
	local.Owner = owner
}

// genericMachineNames are names many Macs answer to; they identify no one.
var genericMachineNames = map[string]bool{"mac": true, "macbook": true, "localhost": true, "imac": true}

// OwnerMismatchError reports that this machine may not write the profile.
type OwnerMismatchError struct {
	Profile string
	Owner   string
	Host    string
}

func (e *OwnerMismatchError) Error() string {
	return fmt.Sprintf(
		"profile %q is owned by %q but this machine answers to %q; refusing to write.\n"+
			"  Two writers on one target corrupt it: each machine keeps its own baseline and\n"+
			"  delete propagation would make the runs undo each other.\n"+
			"  To move ownership here: dot sync owner --store=%s --set-self\n"+
			"  To read instead of write: dot sync pull --store=%s",
		e.Profile, e.Owner, e.Host, e.Profile, e.Profile)
}

// CheckOwner returns an OwnerMismatchError when cfg declares an owner that is
// not this machine. A nil error means writing is allowed.
func CheckOwner(cfg *Config) error {
	if cfg == nil || strings.TrimSpace(cfg.Owner) == "" {
		return nil
	}
	names := MachineNames()
	if len(names) == 0 {
		// Cannot prove we are the owner, so do not claim to be.
		return fmt.Errorf("cannot determine this machine's name to check profile owner")
	}
	if ownersMatch(cfg.Owner, cfg.OwnerAliases, names...) {
		return nil
	}
	return &OwnerMismatchError{
		Profile: NormalizeProfile(cfg.Profile),
		Owner:   cfg.Owner,
		Host:    strings.Join(names, ", "),
	}
}
