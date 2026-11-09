package syncer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TargetKind discriminates the two supported sync destinations.
type TargetKind string

const (
	// TargetLocal is a directory on this machine, typically a cloud
	// client's folder (e.g. ~/Dropbox/work) that the client uploads.
	TargetLocal TargetKind = "local"
	// TargetSSH is a remote rsync-over-SSH destination (user@host:path).
	TargetSSH TargetKind = "ssh"
)

// Target is the parsed sync destination.
type Target struct {
	Kind TargetKind
	Path string // local directory, or remote path for ssh
	Host string // user@host — ssh only
}

// ParseTarget parses a target spec:
//
//	local:~/Dropbox/work        → local directory
//	ssh:user@host:~/work        → rsync over SSH
//	~/Dropbox/work              → bare path, treated as local (back-compat)
//
// An empty spec is an error; callers decide their own default.
func ParseTarget(spec string) (Target, error) {
	if err := validateTargetField("spec", spec, false); err != nil {
		return Target{}, err
	}
	switch {
	case strings.HasPrefix(spec, "local:"):
		path := strings.TrimPrefix(spec, "local:")
		if err := validateTargetField("local path", path, true); err != nil {
			return Target{}, err
		}
		return Target{Kind: TargetLocal, Path: path}, nil
	case strings.HasPrefix(spec, "ssh:"):
		rest := strings.TrimPrefix(spec, "ssh:")
		host, path, ok := strings.Cut(rest, ":")
		if !ok {
			return Target{}, fmt.Errorf("invalid target spec %q: expected ssh:user@host:path", spec)
		}
		if err := validateTargetField("ssh host", host, true); err != nil {
			return Target{}, err
		}
		if err := validateTargetField("ssh path", path, false); err != nil {
			return Target{}, err
		}
		return Target{Kind: TargetSSH, Host: host, Path: path}, nil
	default:
		// A bare path is the shorthand `dot sync target ~/Dropbox/work` takes;
		// it is not a read of any retired config key.
		if err := validateTargetField("local path", spec, true); err != nil {
			return Target{}, err
		}
		return Target{Kind: TargetLocal, Path: spec}, nil
	}
}

func validateTargetField(name, value string, rejectLeadingOption bool) error {
	if value == "" || strings.TrimSpace(value) == "" {
		return fmt.Errorf("invalid target %s %q: empty", name, value)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("invalid target %s %q: invalid UTF-8", name, value)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid target %s %q: contains control character", name, value)
		}
		if unicode.IsSpace(r) {
			return fmt.Errorf("invalid target %s %q: contains Unicode whitespace", name, value)
		}
	}
	if rejectLeadingOption && strings.HasPrefix(value, "-") {
		return fmt.Errorf("invalid target %s %q: leading option marker", name, value)
	}
	return nil
}

// String renders the canonical spec form.
func (t Target) String() string {
	if t.Kind == TargetSSH {
		return "ssh:" + t.Host + ":" + t.Path
	}
	return "local:" + t.Path
}

// IsSSH reports whether the target is an SSH remote.
func (t Target) IsSSH() bool { return t.Kind == TargetSSH }

// RsyncDest returns the rsync destination argument: the local directory
// (trailing slash) or host:path for SSH.
func (t Target) RsyncDest() string {
	if t.Kind == TargetSSH {
		path := t.Path
		if !strings.HasSuffix(path, "/") {
			path += "/"
		}
		return t.Host + ":" + path
	}
	path := t.Path
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}
