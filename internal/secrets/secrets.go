package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// EventKind names one observable step outcome. The engine emits kinds and
// the values they interpolate; the command layer owns every string a user
// sees, so no formatted message crosses this seam.
type EventKind int

const (
	EventVerificationSkipped EventKind = iota // Reason
	EventScaffoldPreview                      // Path (dry run)
	EventScaffolded                           // Path
	EventPlaintextMissing                     // Label, Path
	EventEncrypted                            // Path (plaintext), Dest (archive)
	EventArchiveCopied                        // Name
	EventArchiveMissing                       // Label, Path
	EventRestored                             // Path
	EventRestoreBackup                        // Path (the .bak- copy)
	EventRestoreUnchanged                     // Path
	EventRestoreSkipped                       // Path
	EventUnmatchedArchives                    // Names, Path (the source dir)
)

// Event is one step outcome. Which fields carry meaning depends on Kind; the
// comments on each kind above name them.
type Event struct {
	Kind   EventKind
	Label  string   // managed-entry label, e.g. "SSH key"
	Name   string   // bare archive file name
	Path   string   // the primary path the outcome concerns
	Dest   string   // secondary path (encrypt destination)
	Reason string   // why round-trip verification was skipped
	Names  []string // archives that matched no managed entry
}

// emit delivers ev when a Progress callback was supplied; a nil callback
// drops it, so every caller may leave Progress unset.
func emit(progress func(Event), ev Event) {
	if progress != nil {
		progress(ev)
	}
}

// ConfirmKind names a yes/no question a flow must ask. The command layer
// owns the wording and the unattended (--yes) policy for each one (D-09).
type ConfirmKind int

const (
	// ConfirmOverwriteRestore asks before replacing an existing plaintext
	// file whose content differs from the decrypted archive.
	ConfirmOverwriteRestore ConfirmKind = iota
)

// StatusOptions controls Status. StoreDir arrives resolved so a caller
// honoring a --home override reports on the store that override names.
type StatusOptions struct {
	Runner   *exec.Runner
	State    *config.UserState
	Home     string
	StoreDir string
}

// FileStatus is one presence probe. Label is what the report names the row
// by: the entry label for plaintext files, the archive name for encrypted
// ones.
type FileStatus struct {
	Label   string
	Path    string
	Present bool
}

// StatusResult reports what Status observed. Recipients and the last-backup
// record are not repeated here: they are the caller's own state.
type StatusResult struct {
	Plaintext   []FileStatus
	Encrypted   []FileStatus
	AgeIdentity string
}

// Status probes every managed secret for the presence of its plaintext file
// and its encrypted archive. Absence is a field, never an error — a missing
// file is exactly what the report exists to show.
func Status(opts StatusOptions) (*StatusResult, error) {
	entries, err := Entries(opts.State, opts.Home)
	if err != nil {
		return nil, err
	}
	res := &StatusResult{
		Plaintext:   make([]FileStatus, 0, len(entries)),
		Encrypted:   make([]FileStatus, 0, len(entries)),
		AgeIdentity: ResolveAgeIdentity(opts.State, opts.Home),
	}
	for _, entry := range entries {
		res.Plaintext = append(res.Plaintext, FileStatus{
			Label:   entry.Label,
			Path:    entry.Plain,
			Present: opts.Runner.FileExists(entry.Plain),
		})
	}
	for _, entry := range entries {
		archive := filepath.Join(opts.StoreDir, entry.AgeName)
		res.Encrypted = append(res.Encrypted, FileStatus{
			Label:   entry.AgeName,
			Path:    archive,
			Present: opts.Runner.FileExists(archive),
		})
	}
	return res, nil
}

// InitOptions controls Init. StoreDir and Home arrive resolved; the caller
// is responsible for rejecting a state with no configured recipients before
// getting here, because that check precedes every path lookup.
type InitOptions struct {
	Runner   *exec.Runner
	State    *config.UserState
	Home     string
	StoreDir string
	Scaffold bool
	Progress func(Event)
}

// Init encrypts every managed plaintext file into the store. A plaintext
// file that does not exist is reported and skipped, not an error. Failure
// anywhere stops the run with the previous archives untouched.
func Init(ctx context.Context, opts InitOptions) error {
	if opts.Runner.DryRun {
		opts.Runner.Logger.Info("dry-run: would create secrets dir", "path", opts.StoreDir)
	} else if err := os.MkdirAll(opts.StoreDir, 0700); err != nil {
		return fmt.Errorf("creating secrets dir: %w", err)
	}

	if !opts.Runner.CommandExists("age") {
		return fmt.Errorf("age is not installed — run 'dot apply' to install it")
	}

	// Build common recipient args.
	recipientArgs := make([]string, 0, len(opts.State.Secrets.AgeRecipients)*2)
	for _, r := range opts.State.Secrets.AgeRecipients {
		recipientArgs = append(recipientArgs, "-r", r)
	}

	// Round-trip verification: a typo'd recipient produces archives that
	// encrypt fine and fail only at restore time, when the plaintext may
	// already be gone.
	verify, skipReason := verifier(ctx, opts.Runner, ResolveAgeIdentity(opts.State, opts.Home))
	if skipReason != "" {
		emit(opts.Progress, Event{Kind: EventVerificationSkipped, Reason: skipReason})
	}

	entries, err := Entries(opts.State, opts.Home)
	if err != nil {
		return err
	}

	// Optionally scaffold the shell secrets template first so the entry
	// loop below picks it up.
	shellSecrets := filepath.Join(opts.Home, ".config", "shell", "90-secrets.sh")
	if opts.Scaffold && !opts.Runner.FileExists(shellSecrets) {
		if opts.Runner.DryRun {
			emit(opts.Progress, Event{Kind: EventScaffoldPreview, Path: shellSecrets})
		} else {
			if err := os.MkdirAll(filepath.Dir(shellSecrets), 0755); err != nil {
				return fmt.Errorf("creating shell config dir: %w", err)
			}
			if err := os.WriteFile(shellSecrets, []byte(shellSecretsTemplate), 0600); err != nil {
				return fmt.Errorf("scaffolding shell secrets: %w", err)
			}
			emit(opts.Progress, Event{Kind: EventScaffolded, Path: shellSecrets})
		}
	}

	for _, entry := range entries {
		if !opts.Runner.FileExists(entry.Plain) {
			emit(opts.Progress, Event{Kind: EventPlaintextMissing, Label: entry.Label, Path: entry.Plain})
			continue
		}
		dest := filepath.Join(opts.StoreDir, entry.AgeName)
		if err := encryptFile(ctx, opts.Runner, recipientArgs, entry.Plain, dest, verify); err != nil {
			return fmt.Errorf("encrypting %s: %w", entry.Label, err)
		}
		emit(opts.Progress, Event{Kind: EventEncrypted, Path: entry.Plain, Dest: dest})
	}
	return nil
}

// BackupOptions controls Backup.
type BackupOptions struct {
	Runner   *exec.Runner
	StoreDir string
	Dest     string
	Progress func(Event)
}

// BackupResult reports how many archives reached the destination. On error
// the partial count is still returned, so a caller can say how far it got.
type BackupResult struct {
	Copied int
}

// Backup copies every *.age in the store to Dest. A store that does not
// exist yet is Copied=0 with no error.
func Backup(opts BackupOptions) (*BackupResult, error) {
	res := &BackupResult{}
	entries, err := os.ReadDir(opts.StoreDir)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("reading secrets dir: %w", err)
	}

	if opts.Runner.DryRun {
		opts.Runner.Logger.Info("dry-run: would create destination", "path", opts.Dest)
	} else if err := os.MkdirAll(opts.Dest, 0700); err != nil {
		return res, fmt.Errorf("creating destination: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".age" {
			continue
		}
		src := filepath.Join(opts.StoreDir, e.Name())
		dst := filepath.Join(opts.Dest, e.Name())
		if err := copyArchive(opts.Runner, src, dst); err != nil {
			return res, fmt.Errorf("copying %s: %w", e.Name(), err)
		}
		emit(opts.Progress, Event{Kind: EventArchiveCopied, Name: e.Name()})
		res.Copied++
	}
	return res, nil
}

// RestoreOptions controls Restore. Confirm is consulted once per existing,
// differing destination; it is never called on a dry run.
type RestoreOptions struct {
	Runner   *exec.Runner
	State    *config.UserState
	Home     string
	Src      string
	Confirm  func(ConfirmKind, string) (bool, error)
	Progress func(Event)
}

// RestoreResult summarizes a Restore run. Unmatched lists *.age files in Src
// that map to no managed entry — those were backed up (backup copies every
// *.age) but cannot be restored without a matching entry, e.g. an SSH key
// from a host with a different ssh.key_name.
type RestoreResult struct {
	Restored  int
	Unchanged int
	Skipped   int // user declined overwrite
	Unmatched []string
}

// Restore decrypts every known archive in Src back to its plaintext
// location. The partial result is returned alongside an error so a caller
// can report how far the run got.
func Restore(ctx context.Context, opts RestoreOptions) (*RestoreResult, error) {
	if !opts.Runner.CommandExists("age") {
		return nil, fmt.Errorf("age is not installed — run 'dot apply' to install it")
	}
	entries, err := Entries(opts.State, opts.Home)
	if err != nil {
		return nil, err
	}
	identity := ResolveAgeIdentity(opts.State, opts.Home)

	confirm := func(dest string) (bool, error) {
		return opts.Confirm(ConfirmOverwriteRestore, dest)
	}

	result := &RestoreResult{}
	known := make(map[string]bool, len(entries))
	for _, entry := range entries {
		known[entry.AgeName] = true
		ageSrc := filepath.Join(opts.Src, entry.AgeName)
		if !opts.Runner.FileExists(ageSrc) {
			emit(opts.Progress, Event{Kind: EventArchiveMissing, Label: entry.Label, Path: ageSrc})
			continue
		}
		status, backup, err := restoreFile(ctx, opts.Runner, identity, ageSrc, entry.Plain, entry.DirPerm, confirm)
		if err != nil {
			return result, fmt.Errorf("restoring %s: %w", entry.Label, err)
		}
		switch status {
		case restoreWritten:
			result.Restored++
			emit(opts.Progress, Event{Kind: EventRestored, Path: entry.Plain})
			if backup != "" {
				emit(opts.Progress, Event{Kind: EventRestoreBackup, Path: backup})
			}
		case restoreUnchanged:
			result.Unchanged++
			emit(opts.Progress, Event{Kind: EventRestoreUnchanged, Path: entry.Plain})
		case restoreSkipped:
			result.Skipped++
			emit(opts.Progress, Event{Kind: EventRestoreSkipped, Path: entry.Plain})
		}
	}

	if dirEntries, err := os.ReadDir(opts.Src); err == nil {
		for _, e := range dirEntries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".age" || known[name] {
				continue
			}
			result.Unmatched = append(result.Unmatched, name)
		}
	}
	if len(result.Unmatched) > 0 {
		emit(opts.Progress, Event{Kind: EventUnmatchedArchives, Names: result.Unmatched, Path: opts.Src})
	}
	return result, nil
}

// ListOptions controls List.
type ListOptions struct {
	StoreDir string
}

// ListEntry is one file in the store. Entries whose metadata cannot be read
// are omitted rather than reported as an error.
type ListEntry struct {
	Name string
	Size int64
}

// ListResult reports the store inventory. Missing is true when the store
// directory does not exist yet, which is a state to report rather than fail.
type ListResult struct {
	Missing bool
	Entries []ListEntry
}

// List reports every file in the store in directory order. Directories
// inside the store are skipped.
func List(opts ListOptions) (*ListResult, error) {
	dirEntries, err := os.ReadDir(opts.StoreDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ListResult{Missing: true}, nil
		}
		return nil, fmt.Errorf("reading secrets dir: %w", err)
	}
	res := &ListResult{Entries: make([]ListEntry, 0, len(dirEntries))}
	for _, e := range dirEntries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		res.Entries = append(res.Entries, ListEntry{Name: e.Name(), Size: info.Size()})
	}
	return res, nil
}
