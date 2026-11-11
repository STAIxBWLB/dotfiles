package admission

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// UncoveredNote is the bypass-visibility disclaimer every status view
// carries: the controller only sees jobs launched through it.
const UncoveredNote = "jobs launched outside `dot admit` hold no lease; a bounded process-table scan catches common heavy work, not every process"

// NoteIgnoredPressure writes the single stderr line an ignored pressure gate
// prints, naming the source and what it bypassed. Gate paths call it from
// their own writers so a library caller never writes to stdout, where it would
// corrupt a wrapped command's payload.
func NoteIgnoredPressure(w io.Writer, d Decision) {
	if !d.PressureIgnored || w == nil {
		return
	}
	reason := "pressure defer bypassed"
	if len(d.IgnoredReasons) > 0 {
		reason = strings.Join(d.IgnoredReasons, "; ")
	}
	source := d.IgnoreSource
	if source == "" {
		source = "ignore"
	}
	fmt.Fprintf(w, "admission: pressure gate ignored (%s): %s\n", source, reason)
}

// DeferOutcome is the machine-readable record `dot admit` prints when it
// defers (exit 75, EX_TEMPFAIL). RetryAfterSeconds is advisory.
type DeferOutcome struct {
	Outcome           string `json:"outcome"` // always "deferred"
	Scope             string `json:"scope"`
	Class             string `json:"class"`
	Owner             string `json:"owner,omitempty"`
	Reason            string `json:"reason"`
	RetryAfterSeconds int64  `json:"retry_after"`
}

// Bundle is the bounded diagnostic record behind `dot admit status --json`:
// one pressure snapshot with per-probe availability flags, the gate verdict,
// the hysteresis state, the current slot owners, and the last WindowServer
// evidence. Building it never scans the disk beyond reading the slot leases
// the store already owns.
type Bundle struct {
	GeneratedAt string   `json:"generated_at"`
	Platform    string   `json:"platform"`
	Admit       bool     `json:"admit"`
	Reasons     []string `json:"reasons,omitempty"`
	// IgnoredReasons records what a one-off ignore or relax bypassed; it is
	// absent unless the gate actually admitted past a pressure defer, so a
	// default host's bundle is byte-identical to before.
	IgnoredReasons    []string         `json:"ignored_reasons,omitempty"`
	RetryAfterSeconds int64            `json:"retry_after_seconds,omitempty"`
	Snapshot          PressureSnapshot `json:"snapshot"`
	History           History          `json:"history"`
	Owners            []Lease          `json:"owners"`
	Note              string           `json:"note"`
}

// BuildBundle assembles the diagnostic record from already-gathered inputs.
func BuildBundle(snap PressureSnapshot, d Decision, hist History, owners []Lease, now time.Time) Bundle {
	b := Bundle{
		GeneratedAt:    now.Format(time.RFC3339),
		Platform:       snap.Platform,
		Admit:          d.Admit,
		Reasons:        d.Reasons,
		IgnoredReasons: d.IgnoredReasons,
		Snapshot:       snap,
		History:        hist,
		Note:           UncoveredNote,
	}
	if d.RetryAfter > 0 && !d.Admit {
		b.RetryAfterSeconds = int64(d.RetryAfter / time.Second)
	}
	if owners == nil {
		owners = []Lease{}
	}
	b.Owners = owners
	return b
}
