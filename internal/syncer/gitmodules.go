package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// urlMove is one submodule whose URL a committed .gitmodules changed.
type urlMove struct{ old, new string }

// checkGitmodules compares the worktree .gitmodules with the one in rev (the
// commit the repo sits on, or moves to in a preview). Peer sync never carries
// .gitmodules, so after a realign the worktree keeps the old copy, or none
// when the peer added the first submodule, and git shows a local edit that
// reverts the peer's commit (#179). A copy equal to an older committed
// version, or a missing one, is stale: --apply restores it from the index
// and syncs the moved submodule URLs. Anything else is a local edit and left
// alone.
func (r *gitStateRun) checkGitmodules(ctx context.Context, abs, rev string, apply bool, rep *GitRepoReport) {
	// at-tip: HEAD is already its upstream (an earlier realign, or a reset
	// by hand) and only local edits differ; the stale copy is one of them.
	switch {
	case rep.Status == GitRepoAligned, rep.Status == GitRepoRealignable, rep.Status == GitRepoRealigned:
	case rep.Class == GitClassAtTip:
	default:
		return
	}
	want, err := r.runOutput(ctx, abs, nil, true, "show", rev+":.gitmodules")
	if err != nil {
		return // rev has no .gitmodules: nothing to restore
	}
	path := filepath.Join(abs, ".gitmodules")
	worktree, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return
	}
	if !missing && string(worktree) == want {
		return
	}
	if !missing && !r.staleGitmodules(ctx, abs, rev, string(worktree)) {
		rep.Gitmodules = "modified"
		return
	}
	var paths []string
	if missing {
		rep.Gitmodules = "missing"
	} else {
		rep.Gitmodules = "stale"
		oldURLs := r.gitmodulesURLs(ctx, abs, "-f", path)
		for p, url := range r.gitmodulesURLs(ctx, abs, "--blob", rev+":.gitmodules") {
			if old, ok := oldURLs[p]; ok && old != url {
				paths = append(paths, p)
				rep.URLMoves = append(rep.URLMoves, p+": "+old+" -> "+url)
			}
		}
		sort.Strings(paths)
		sort.Strings(rep.URLMoves)
	}
	if !apply {
		return
	}
	// After a realign the index holds rev's .gitmodules; checkout writes
	// that one file and nothing else in the worktree.
	if _, err := r.runOutput(ctx, abs, nil, false, "checkout", "-q", "--", ".gitmodules"); err != nil {
		rep.Gitmodules += " (restore failed: " + shortErr(err) + ")"
		return
	}
	rep.Gitmodules = "restored"
	// submodule sync rewrites each moved child's origin, so it follows the
	// child's own rules: only children this run may touch (named, not
	// locked or mid-operation), and each rewrite gets an undo command.
	var syncPaths, skipped []string
	before := map[string]map[string]string{}
	for _, p := range paths {
		childAbs := filepath.Join(abs, filepath.FromSlash(p))
		if !r.included(filepath.ToSlash(filepath.Join(rep.Path, p))) {
			skipped = append(skipped, p+" (not named)")
			continue
		}
		if gitdir, err := r.gitDir(ctx, childAbs); err == nil {
			// A linked worktree shares its config with the main clone.
			if _, err := os.Stat(filepath.Join(gitdir, "commondir")); err == nil {
				skipped = append(skipped, p+" (linked worktree)")
				continue
			}
			if reason := r.blockReason(ctx, childAbs, gitdir); reason != "" {
				skipped = append(skipped, p+" ("+reason+")")
				continue
			}
			before[p] = r.remoteURLs(ctx, childAbs)
		}
		syncPaths = append(syncPaths, p)
	}
	if len(syncPaths) > 0 {
		args := append([]string{"submodule", "sync", "-q", "--"}, syncPaths...)
		if _, err := r.runOutput(ctx, abs, nil, false, args...); err != nil {
			rep.Gitmodules = "restored (submodule sync failed: " + shortErr(err) + ")"
		}
	}
	// sync rewrites the child's default remote (its branch's remote, else
	// origin): every remote URL it changed gets an undo.
	var undo []string
	for _, p := range syncPaths {
		childAbs := filepath.Join(abs, filepath.FromSlash(p))
		old, ok := before[p]
		if !ok {
			continue
		}
		now := r.remoteURLs(ctx, childAbs)
		names := make([]string, 0, len(old))
		for name := range old {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if now[name] != old[name] {
				undo = append(undo, "git -C "+shellWord(childAbs)+" remote set-url "+shellWord(name)+" "+shellWord(old[name]))
			}
		}
	}
	if len(undo) > 0 {
		if rep.URLUndo != "" {
			undo = append([]string{rep.URLUndo}, undo...)
		}
		rep.URLUndo = strings.Join(undo, " && ")
	}
	if len(skipped) > 0 {
		rep.Gitmodules += "; submodule sync skipped for " + strings.Join(skipped, ", ")
	}
}

// remoteURLs maps each remote of the repo at abs to its configured URL.
func (r *gitStateRun) remoteURLs(ctx context.Context, abs string) map[string]string {
	urls := map[string]string{}
	out, err := r.read(ctx, abs, "config", "-z", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return urls
	}
	for _, entry := range strings.Split(out, "\x00") {
		key, value, ok := strings.Cut(entry, "\n")
		if name, found := strings.CutSuffix(strings.TrimPrefix(key, "remote."), ".url"); ok && found {
			urls[name] = value
		}
	}
	return urls
}

// staleGitmodules reports whether content equals a version of .gitmodules
// that rev's history replaced: the file before each of the last ten commits
// that touched it.
func (r *gitStateRun) staleGitmodules(ctx context.Context, abs, rev, content string) bool {
	out, err := r.read(ctx, abs, "rev-list", "-n", "10", rev, "--", ".gitmodules")
	if err != nil {
		return false
	}
	for _, commit := range strings.Fields(out) {
		if old, err := r.runOutput(ctx, abs, nil, true, "show", commit+"^:.gitmodules"); err == nil && old == content {
			return true
		}
	}
	return false
}

// gitmodulesURLs maps submodule path to URL from one .gitmodules source:
// "-f <file>" or "--blob <rev>:.gitmodules".
func (r *gitStateRun) gitmodulesURLs(ctx context.Context, abs, flag, source string) map[string]string {
	out, err := r.runOutput(ctx, abs, nil, true, "config", "-z", flag, source, "--get-regexp", `^submodule\..*\.(path|url)$`)
	if err != nil {
		return nil
	}
	paths, urls := map[string]string{}, map[string]string{}
	// -z: "key\nvalue\x00" records, so a submodule name may hold spaces.
	for _, record := range strings.Split(out, "\x00") {
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			continue
		}
		name, ok := strings.CutPrefix(key, "submodule.")
		if !ok {
			continue
		}
		if n, ok := strings.CutSuffix(name, ".path"); ok {
			paths[n] = value
		} else if n, ok := strings.CutSuffix(name, ".url"); ok {
			urls[n] = value
		}
	}
	byPath := map[string]string{}
	for name, path := range paths {
		if url, ok := urls[name]; ok {
			byPath[path] = url
		}
	}
	return byPath
}

// fetchOrigin fetches a child's origin for its gitlinks. Its URL can come
// from a committed .gitmodules, so the fetch runs with the protocol
// restrictions git submodule itself applies to such URLs; like the rescue
// push, no prompt anyone can answer, and a time bound. Only this child:
// its nested submodules get their own pass.
func (r *gitStateRun) fetchOrigin(ctx context.Context, abs string) error {
	return r.fetchRemote(ctx, abs, "origin", true)
}

// fetchRemote fetches remote into abs. A submodule fetches under git
// submodule's protocol rules (GIT_PROTOCOL_FROM_USER=0), since its URL came
// from a .gitmodules; the workspace root's remote is the user's own config.
func (r *gitStateRun) fetchRemote(ctx context.Context, abs, remote string, submodule bool) error {
	fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if submodule {
		env = append(env, "GIT_PROTOCOL_FROM_USER=0")
	}
	_, err := r.runOutput(fctx, abs, env, false, "fetch", "-q", "--no-recurse-submodules", remote)
	r.forgetFetched(abs)
	return err
}

// missingGitlink handles a child whose parent gitlink commit is not in its
// object store. A moved submodule URL is the usual cause after a peer
// switch: the new commit exists only at the new remote. The child compares
// its origin URL with the one the parent's commit records (wantURL; a
// relative URL is not compared). With --apply --fetch it points origin at a
// moved URL, fetches, and is classified again; otherwise the report names
// the exact commands.
func (r *gitStateRun) missingGitlink(ctx context.Context, abs, gitdir, gitlink, wantURL string, apply bool, rep *GitRepoReport) {
	// Both sides are compared as git resolves them: `remote get-url` and
	// `ls-remote --get-url` apply url.<base>.insteadOf, so an https URL in
	// .gitmodules and an ssh origin rewritten to each other do not count as
	// a move. The raw origin URL is kept to restore it if the fetch fails.
	var move *urlMove
	rawOrigin := ""
	if wantURL != "" && !strings.HasPrefix(wantURL, "./") && !strings.HasPrefix(wantURL, "../") {
		have, herr := r.read(ctx, abs, "remote", "get-url", "origin")
		want, werr := r.read(ctx, abs, "ls-remote", "--get-url", wantURL)
		if herr == nil && werr == nil && have != want {
			move = &urlMove{old: have, new: wantURL}
			rawOrigin, _ = r.read(ctx, abs, "config", "--get", "remote.origin.url")
		}
	}
	commands := "git -C " + shellWord(abs) + " fetch origin"
	if move != nil {
		commands = "git -C " + shellWord(abs) + " remote set-url origin " + shellWord(move.new) + " && " + commands
	}
	if !apply || !r.opts.Fetch {
		if move != nil {
			rep.Class = GitClassURLMoved
			rep.Suggestion = "the submodule URL moved from " + move.old + " to " + move.new + "; run " + commands + " and realign again, or realign with --apply --fetch"
		} else {
			rep.Class = GitClassGitlinkMissing
			rep.Suggestion = "fetch it: " + commands + ", or realign with --apply --fetch"
		}
		return
	}
	// Fetched from this same origin URL before its parent was judged (#201):
	// a second fetch brings nothing new, so say what the first did. A URL
	// the parent's .gitmodules restore synced since, or a first fetch that
	// failed, is fetched again.
	if r.fetched[abs] == fetchedToJudge && move == nil {
		if now, err := r.read(ctx, abs, "remote", "get-url", "origin"); err == nil && now == r.fetchedFrom[abs] {
			rep.Class = GitClassGitlinkMissing
			rep.Suggestion = "origin was " + fetchedToJudge + " but " + shortRev(gitlink) + " is still missing; check that the parent's commit was pushed with its submodule"
			return
		}
	}
	if move != nil {
		if _, err := r.runOutput(ctx, abs, nil, false, "remote", "set-url", "--", "origin", move.new); err != nil {
			rep.Reason = gitlinkMissing + "; setting the moved URL failed: " + shortErr(err)
			return
		}
	}
	err := r.fetchOrigin(ctx, abs)
	if err != nil {
		rep.Reason = gitlinkMissing + "; fetch failed: " + shortErr(err)
		if move != nil && rawOrigin != "" {
			if _, rerr := r.runOutput(ctx, abs, nil, false, "remote", "set-url", "--", "origin", rawOrigin); rerr == nil {
				rep.Reason += "; origin restored to " + rawOrigin
			}
		}
		return
	}
	fresh := &GitRepoReport{Path: rep.Path}
	r.judge(ctx, abs, gitdir, gitlink, fresh)
	if fresh.Reason == gitlinkMissing {
		fresh.Class = GitClassGitlinkMissing
		fresh.Suggestion = "origin was fetched but " + shortRev(gitlink) + " is still missing; check that the parent's commit was pushed with its submodule"
	}
	if move != nil && rawOrigin != "" {
		// The re-pointed origin is part of what this run changed.
		fresh.URLMoves = []string{"origin: " + rawOrigin + " -> " + move.new}
		fresh.URLUndo = "git -C " + shellWord(abs) + " remote set-url origin " + shellWord(rawOrigin)
	}
	*rep = *fresh
}
