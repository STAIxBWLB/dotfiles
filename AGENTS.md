# AGENTS.md

Project instructions for every coding agent working in this repo (Claude Code,
Codex, Kiro, Kimi, Qwen, pi). This is the only instruction file; do not add a
CLAUDE.md.

## Sources of truth

- `README.md`: modules, profiles, configuration, build commands, CI and release flow.
- `docs/BOUNDARIES.md`: which live files `dot` may write. Stay inside it.
- `docs/CEILINGS.md`: accepted design limits and when to revisit each.
  `internal/syncer/ceilings_doc_test.go` checks its code markers.
- `docs/commands/`: generated from the cobra help; never edit by hand.

Facts owned by those files are updated there. The rules below that no other
file states are maintained here.

## Verify

- `make build`, `make test`, `make lint` (README, Development).
- `make docs` after any command help change; CI fails if `docs/commands` drifts.
- Sync changes: prove rsync and Go filter parity with a real-rsync test, as in
  `internal/syncer/excludes_test.go`.

## Workflow

- Branch and PR; never commit to `main` directly.
- Conventional commits, in English.
- Before merge: CI green and a read-only OCR delegation review of the PR head.
- Release: README, CI/CD.

## Release notes

- Every published tag `vX.Y.Z` needs `docs/releases/vX.Y.Z.md` in the tagged commit. Add it in the release PR; the Release workflow publishes it as the GitHub release body and fails before goreleaser when it is missing or empty.
- Readers are people who run `dot` on their Mac, Linux workstation or GPU server. Write what changes for them, not how the code changed. Cover every PR since the previous published release.
- Format:
  - First line: one sentence on what this release changes for the user. No heading before it.
  - Then only the sections that have content, in this order: `### New`, `### Improved`, `### Fixed`, `### Action needed`.
  - One bullet per change in plain language, naming the command, flag or file the user touches (`dot sync`, `dot apply`). Describe fixes by the symptom the user saw. End the bullet with the PR number, e.g. `(#123)`.
  - `### Action needed`: steps required after upgrading (config edits, renamed commands, manual migrations). Omit it when there are none.
  - Leave out tests, CI, refactors, docs, agent instructions and release plumbing. A release with nothing else is the single line `Maintenance release: no user-facing changes.` plus the changelog line.
  - No commit SHAs, conventional-commit prefixes or internal package paths.
  - Last line: `**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/<previous release>...vX.Y.Z` (`.../commits/vX.Y.Z` for the first release).
- Upgrade command to mention when users must act: `dot update` or `brew upgrade dotfiles`.

