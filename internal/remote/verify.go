package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

// VerifyOptions is one `dot remote verify` request.
type VerifyOptions struct {
	// Path is the checkout whose commit is verified; empty means the checkout
	// the current directory belongs to.
	Path     string
	HostName string
	// Command is CMD. Empty means the matching remote.projects entry's verify.
	Command []string
	// Dirty verifies a throwaway commit of the working tree instead of HEAD.
	Dirty     bool
	Wait      time.Duration
	Ignore    admission.IgnoreSet
	IgnoreSrc string
	DryRun    bool
	Home      string
}

// verifyAuthor names the throwaway commit --dirty writes, so it needs no git
// identity of its own and never looks like the person's work.
var verifyAuthor = []string{
	"GIT_AUTHOR_NAME=dot remote verify", "GIT_AUTHOR_EMAIL=dot-remote@localhost",
	"GIT_COMMITTER_NAME=dot remote verify", "GIT_COMMITTER_EMAIL=dot-remote@localhost",
}

// SnapshotCommit returns the commit verify sends: HEAD, or with dirty a commit
// object of the working tree as it is (tracked plus untracked-not-ignored
// files), with built-in excludes applied, whose parent is HEAD. The tree is
// staged in a throwaway index, so the checkout's own index is untouched, and
// commit-tree moves no ref.
func SnapshotCommit(ctx context.Context, checkout string, dirty bool) (string, error) {
	head, err := gitIn(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s has no commit to verify: %w", checkout, err)
	}
	if !dirty {
		return head, nil
	}
	tmp, err := os.MkdirTemp("", "dot-remote-verify-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := append([]string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}, verifyAuthor...)
	if _, err := gitIn(ctx, checkout, env, "read-tree", head); err != nil {
		return "", err
	}
	if _, err := gitIn(ctx, checkout, env, "add", "-A"); err != nil {
		return "", err
	}
	// Filter the throwaway index, including tracked paths. Excluded blobs staged
	// above are unreachable from this commit and are never pushed with it.
	listing, err := gitInInput(ctx, checkout, env, nil, "ls-files", "-z")
	if err != nil {
		return "", err
	}
	var excluded []byte
	for _, rel := range strings.Split(string(listing), "\x00") {
		if snapshotExcluded(rel) {
			excluded = append(excluded, rel...)
			excluded = append(excluded, 0)
		}
	}
	if len(excluded) > 0 {
		if _, err := gitInInput(ctx, checkout, env, excluded, "update-index", "--force-remove", "-z", "--stdin"); err != nil {
			return "", err
		}
	}
	tree, err := gitIn(ctx, checkout, env, "write-tree")
	if err != nil {
		return "", err
	}
	return gitIn(ctx, checkout, env, "commit-tree", tree, "-p", head, "-m", "dot remote verify: working tree")
}

// gitIn runs one git command in dir with extra environment and returns its
// trimmed stdout.
func gitIn(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := gitInInput(ctx, dir, env, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// gitInInput preserves NUL-separated filenames byte for byte.
func gitInInput(ctx context.Context, dir string, env []string, input []byte, args ...string) ([]byte, error) {
	cmd := osexec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "LC_ALL=C", "LANG=C"), env...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// snapshotExcluded applies run's built-in patterns to every path component,
// including excluded parent directories and the env template exception.
func snapshotExcluded(rel string) bool {
	parts := strings.Split(rel, "/")
	for start := range parts {
		for end := start + 1; end <= len(parts); end++ {
			candidate := strings.Join(parts[start:end], "/")
			if candidate == envTemplateInclude {
				continue
			}
			for _, pattern := range builtinExcludes {
				if matched, _ := path.Match(pattern, candidate); matched {
					return true
				}
			}
		}
	}
	return false
}

var bareUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// BareName is the bare repository one checkout's commits go to: one per
// repository, shared by all its worktrees, named by the common dir's
// repository name plus a short hash of its absolute path.
func BareName(commonDir string) string {
	name := filepath.Base(commonDir)
	if name == ".git" {
		name = filepath.Base(filepath.Dir(commonDir))
	}
	sum := sha1.Sum([]byte(commonDir))
	return bareUnsafe.ReplaceAllString(name, "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

// VerifyRef is the ref one verify run pushes its commit to, and deletes after
// the job. It is unique per run: two machines verifying the same path share a
// mirror key, and a shared ref could be replaced between this run's push and
// its container's fetch, so the container would test the other commit.
func VerifyRef(rel, sha, nonce string) string {
	return "refs/verify/" + MirrorKey(rel) + "/" + shortSHA(sha) + "-" + nonce
}

// verifyNonce is the random part of one run's ref.
func verifyNonce() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// VerifyContainer is the container a verify of one checkout runs in.
func (h Host) VerifyContainer(rel string) string { return h.Prefix + "-verify-" + MirrorKey(rel) }

// baseRef is the base sent beside the commit: the local ref it reads and the
// branch it becomes in the bare repo. Empty when the checkout has none.
type baseRef struct {
	Local  string // a full local ref, e.g. refs/remotes/origin/main
	Branch string // e.g. main
}

// resolveBase turns the configured base (or the remote default branch) into a
// ref the push can send. A configured base that does not resolve is an error;
// a missing default is not, since a repo without a remote has none.
func resolveBase(ctx context.Context, checkout, configured string) (baseRef, error) {
	name := configured
	if name == "" {
		head, err := gitIn(ctx, checkout, nil, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
		if err == nil && head != "" {
			name = head
		} else if _, err := gitIn(ctx, checkout, nil, "rev-parse", "-q", "--verify", "refs/remotes/origin/main"); err == nil {
			name = "refs/remotes/origin/main"
		} else {
			return baseRef{}, nil
		}
	}
	full, err := gitIn(ctx, checkout, nil, "rev-parse", "--symbolic-full-name", name)
	if err != nil || full == "" {
		return baseRef{}, fmt.Errorf("the verify base %q does not name a branch in %s (remote.projects.<path>.base)", name, checkout)
	}
	var branch string
	switch {
	case strings.HasPrefix(full, "refs/remotes/"):
		rest := strings.TrimPrefix(full, "refs/remotes/")
		if _, b, ok := strings.Cut(rest, "/"); ok {
			branch = b
		}
	case strings.HasPrefix(full, "refs/heads/"):
		branch = strings.TrimPrefix(full, "refs/heads/")
	}
	if branch == "" || branch == "HEAD" {
		return baseRef{}, fmt.Errorf("the verify base %q is not a branch (%s)", name, full)
	}
	return baseRef{Local: full, Branch: branch}, nil
}

// verifyCheckout is the script the container runs as root: it makes a fresh
// checkout owned by the host user's UID and GID ($U/$G, set from the host), with
// the base branch fetched beside the target, then drops to that user with no
// capabilities and runs CMD there.
func verifyCheckout(ref string, base baseRef, command []string) string {
	return `mkdir -p /v && chown "$U:$G" /v && home=$(getent passwd "$U" | cut -d: -f6); ` +
		`exec setpriv --reuid="$U" --regid="$G" --init-groups --inh-caps=-all ` +
		`env HOME="${home:-/tmp}" PATH="$PATH" bash -c ` + ShellQuote(verifyInner(ref, base, command))
}

// verifyInner is the unprivileged part: the fresh checkout, then CMD.
func verifyInner(ref string, base baseRef, command []string) string {
	// Every refspec is one quoted word: a branch name may hold ';' or '$(',
	// which git accepts, and this script is handed to a shell.
	fetch := "git fetch -q origin " + ShellQuote("+"+ref+":refs/verify/target")
	if base.Branch != "" {
		fetch = "git fetch -q origin " + ShellQuote("+"+VerifyBaseRef(ref)+":refs/remotes/origin/"+base.Branch) +
			" " + ShellQuote("+"+ref+":refs/verify/target")
	}
	return "git init -q /v/r && cd /v/r && git remote add origin /git/repo.git && " + fetch +
		" && git checkout -q --detach refs/verify/target && " + CommandScript(command)
}

// VerifyCommand is the remote shell command a verify sends: the host user's
// ids, then the container with the bare repo mounted read-only.
func VerifyCommand(h Host, container, bare, ref string, base baseRef, command []string) string {
	return verifyCommand(h, container, bare, ref, base, command, "")
}

func verifyCommand(h Host, container, bare, ref string, base baseRef, command []string, owner string) string {
	argv := []string{"--init", "--user", "0", "-i", "--name", container}
	if owner != "" {
		argv = append(argv, "--label", "net.staix.dot.remote-owner="+owner)
	}
	if h.CPUs > 0 {
		argv = append(argv, "--cpus", fmt.Sprint(h.CPUs))
	}
	argv = append(argv, "--memory", h.Memory)
	for _, cache := range h.Caches {
		argv = append(argv, "-v", h.Volume(cache.Name)+":"+cache.Target)
	}
	for _, pair := range h.EnvPairs() {
		argv = append(argv, "-e", pair.Key+"="+pair.Value)
	}
	argv = append(argv, h.Image(), "bash", "-c", verifyCheckout(ref, base, command))
	mount := `-v "$HOME"/` + ShellQuote(path.Join(h.GitRoot, bare)) + ":/git/repo.git:ro"
	return `U=$(id -u) G=$(id -g); ` + h.Runtime + ` run --rm -e U="$U" -e G="$G" ` + mount + " " + ShellJoin(argv)
}

// bareInitScript creates the owner-only bare repo tree and the repo, once.
func bareInitScript(h Host, bare string) string {
	root, repo := ShellQuote(h.GitRoot), ShellQuote(path.Join(h.GitRoot, bare))
	return `cd "$HOME" && mkdir -p ` + root + ` && chmod 700 ` + root +
		` && { test -d ` + repo + ` || git init -q --bare ` + repo + `; }`
}

// Verify pushes the checkout's exact commit to an owner-only bare repo on the
// host, checks it out fresh in a container beside the base branch and runs CMD
// there. The ref is deleted afterwards on every path that pushed it.
func (d Deps) Verify(ctx context.Context, cfg Config, opts VerifyOptions) error {
	checkout, err := d.checkoutPath(ctx, opts.Path)
	if err != nil {
		return err
	}
	h, err := cfg.HostFor(opts.HostName, checkout)
	if err != nil {
		return err
	}
	if err := cfg.RefuseRoots(checkout, resolveExisting(opts.Home)); err != nil {
		return err
	}
	rel, err := cfg.Rel(checkout)
	if err != nil {
		return err
	}
	project, _ := cfg.Match(checkout)
	command := opts.Command
	if len(command) == 0 {
		if project.Verify == "" {
			return fmt.Errorf("no verify command for %s: pass -- COMMAND, or set remote.projects.<path>.verify", checkout)
		}
		command = []string{project.Verify}
	}
	container := h.VerifyContainer(rel)
	// The repository is read before the leases only for a dry run: a real job
	// takes its leases and reads the host's load first, as a run does, so a
	// busy path or a loaded host defers without touching git.
	if opts.DryRun {
		bare, base, err := verifyTarget(ctx, checkout, project.Base)
		if err != nil {
			return err
		}
		ref := VerifyRef(rel, "<sha>", "<nonce>")
		fmt.Fprintf(d.out(), "would push %s (%s) to %s:%s as %s\n", checkout, map[bool]string{true: "working tree", false: "HEAD"}[opts.Dirty], h.SSH, path.Join(h.GitRoot, bare), ref)
		if base.Branch != "" {
			fmt.Fprintf(d.out(), "would send the base %s as %s\n", base.Local, base.Branch)
		}
		fmt.Fprintf(d.out(), "would run in %s on %s:\n  %s\n", container, h.Name, strings.Join(SSHArgv(h.SSH, VerifyCommand(h, container, bare, ref, base, command)), " "))
		return nil
	}

	leases, err := d.admit(ctx, h, admitOptions{
		Scope: PathScope(checkout), Container: container, Cwd: checkout, Wait: opts.Wait, Ignore: opts.Ignore, IgnoreSrc: opts.IgnoreSrc,
		Busy: func(holder *admission.Lease) string { return pathBusyReason(checkout, holder) },
	})
	if err != nil {
		return err
	}
	defer leases.Release()
	bare, base, err := verifyTarget(ctx, checkout, project.Base)
	if err != nil {
		return err
	}
	if err := d.prepareContainer(ctx, h, container, PathScope(checkout), true); err != nil {
		return err
	}

	sha, err := SnapshotCommit(ctx, checkout, opts.Dirty)
	if err != nil {
		return err
	}
	ref := VerifyRef(rel, sha, d.nonce())
	remoteCommand := verifyCommand(h, container, bare, ref, base, command, d.containerOwner(PathScope(checkout)))
	if _, err := d.capture(ctx, SSHArgv(h.SSH, bareInitScript(h, bare)), nil); err != nil {
		return fmt.Errorf("creating the bare repo %s on %s: %w", path.Join(h.GitRoot, bare), h.Name, err)
	}
	refspecs := []string{"+" + sha + ":" + ref}
	if base.Branch != "" {
		refspecs = append(refspecs, "+"+base.Local+":"+VerifyBaseRef(ref))
	}
	push := append([]string{"env", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "git", "-C", checkout, "push", "-q", "--no-verify",
		h.SSH + ":" + path.Join(h.GitRoot, bare)}, refspecs...)
	// From here the ref may be on the host, so every path deletes it.
	defer d.deleteVerifyRefs(ctx, h, bare, ref, base.Branch != "")
	var pushOut, pushErr bytes.Buffer
	exit, sig, err := d.ex()(ctx, push, nil, &pushOut, &pushErr)
	if sig != 0 {
		return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("pushing %s to %s interrupted by signal %d", shortSHA(sha), h.Name, sig)}
	}
	if err != nil {
		return fmt.Errorf("pushing %s to %s: %w", shortSHA(sha), h.Name, err)
	}
	if exit != 0 {
		return fmt.Errorf("pushing %s to %s exited %d: %s; check ssh access and remote repository permissions, then retry `dot remote verify`", shortSHA(sha), h.Name, exit, tail(pushErr.String()))
	}
	what := ""
	if opts.Dirty {
		what = ", working tree"
	}
	fmt.Fprintf(d.errOut(), "dot remote: verifying %s at %s%s in %s on %s\n", rel, shortSHA(sha), what, container, h.Name)
	started := d.now()
	runErr := d.runContainer(ctx, h, container, remoteCommand)
	fmt.Fprintf(d.errOut(), "dot remote: verify %s %s finished in %s\n", rel, shortSHA(sha), d.now().Sub(started).Round(time.Second))
	return runErr
}

// verifyTarget reads the bare repo name and the base a checkout's verify uses.
func verifyTarget(ctx context.Context, checkout, configuredBase string) (string, baseRef, error) {
	common, err := gitIn(ctx, checkout, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", baseRef{}, fmt.Errorf("%s is not a git checkout, and verify sends commits: %w", checkout, err)
	}
	base, err := resolveBase(ctx, checkout, configuredBase)
	if err != nil {
		return "", baseRef{}, err
	}
	return BareName(common) + ".git", base, nil
}

// VerifyBaseRef is the per-run ref the base branch is pushed to, beside the
// target: a shared refs/heads/<branch> could be replaced by another machine's
// verify between this run's push and its fetch.
func VerifyBaseRef(ref string) string { return ref + "-base" }

// deleteVerifyRefs removes this run's target ref and, when one was pushed, its
// base ref, reporting rather than hiding a failure. The context is detached: an
// interrupted verify still cleans up.
func (d Deps) deleteVerifyRefs(ctx context.Context, h Host, bare, ref string, withBase bool) {
	refs := []string{ref}
	if withBase {
		refs = append(refs, VerifyBaseRef(ref))
	}
	for _, r := range refs {
		d.deleteVerifyRef(ctx, h, bare, r)
	}
}

// deleteVerifyRef removes one pushed ref.
func (d Deps) deleteVerifyRef(ctx context.Context, h Host, bare, ref string) {
	script := `cd "$HOME" && git -C ` + ShellQuote(path.Join(h.GitRoot, bare)) + ` update-ref -d ` + ShellQuote(ref)
	if _, err := d.capture(context.WithoutCancel(ctx), SSHArgv(h.SSH, script), nil); err != nil {
		fmt.Fprintf(d.errOut(), "dot remote: could not delete %s in %s on %s (%v); delete it with `%s`\n",
			ref, path.Join(h.GitRoot, bare), h.Name, err,
			sshCommand(h, `git -C "$HOME"/`+quotedWord(path.Join(h.GitRoot, bare))+" update-ref -d "+quotedWord(ref)))
	}
}

// nonce is this run's random ref suffix; Deps.Nonce fixes it in tests.
func (d Deps) nonce() string {
	if d.Nonce != nil {
		return d.Nonce()
	}
	return verifyNonce()
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
