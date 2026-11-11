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

// NoteIgnoredUncovered writes the line an uncovered-work bypass prints instead
// of deferring: the scan still ran, so what it found is named and the person
// can judge it.
func NoteIgnoredUncovered(w io.Writer, jobs []string, source string) {
	if len(jobs) == 0 || w == nil {
		return
	}
	if source == "" {
		source = "ignore"
	}
	fmt.Fprintf(w, "admission: uncovered heavy work ignored (%s): %s\n", source, strings.Join(jobs, ", "))
}

// NoteOverflowSlot writes the line a slot bypass prints when it holds a lease
// past the configured capacity: the job is not leaseless, and the person should
// know it is running next to the work the limit was protecting.
func NoteOverflowSlot(w io.Writer, lease Lease, capacity int, source string) {
	if lease.Index < capacity || w == nil {
		return
	}
	if source == "" {
		source = "ignore"
	}
	fmt.Fprintf(w, "admission: slot capacity %d exceeded (%s): holding overflow slot %d\n", capacity, source, lease.Index)
}

// SlotBusyReason names the limit a slot acquirer hit and who holds it. When the
// caller had the slot bypass in play the limit is the capacity plus the overflow
// allowance, because deferring with the bypass active means that headroom ran
// out too: the person needs to see both numbers to judge what to do next.
func SlotBusyReason(class string, holder *Lease, capacity int, overflow bool) string {
	reason := fmt.Sprintf("slot busy: %s slot limit %d reached", class, capacity)
	if overflow {
		reason = fmt.Sprintf("slot busy: %s slot limit %d reached (capacity %d + %d overflow)",
			class, capacity+MaxOverflowSlots, capacity, MaxOverflowSlots)
	}
	if holder == nil {
		return reason
	}
	return fmt.Sprintf("%s, %s holds pid %d (since %s)",
		reason, holder.Owner, holder.PID, holder.AcquiredAt.Format(time.RFC3339))
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
	// Ignored names the rules this run stepped past, when a bypass was active
	// but did not turn this defer into an admission (a busy slot under
	// --ignore pressure, for example). Absent otherwise.
	Ignored []string `json:"ignored,omitempty"`
	// IgnoredReasons is what a bypassed rule would have held against this run:
	// the pressure defer it stepped past before the slot or the uncovered-work
	// scan deferred it instead. Absent unless something was bypassed.
	IgnoredReasons []string `json:"ignored_reasons,omitempty"`
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
	// Ignored lists the rules a one-off bypass or the relax override stepped
	// past, and IgnoredReasons what a bypassed pressure defer would have said.
	// Both are absent unless something was actually bypassed, so a default
	// host's bundle is byte-identical to before.
	Ignored           []string         `json:"ignored,omitempty"`
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
		Ignored:        d.Ignored,
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
