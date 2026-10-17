# Adaptive AI Work Policies

Status: Implementation on `feat/adaptive-ai-policy`; native rollout and evaluation pending.
Date: 2026-09-27
Issue: https://github.com/STAIxBWLB/dotfiles/issues/153
Maru integration: https://github.com/STAIxBWLB/maru/issues/404

## Problem and evidence

Installation and weekly updates (#150) do not establish task-fit runtime settings.
Approval flag names also have different semantics across native tools and hosts.
The approved intent is automatic review, not blanket YOLO approval, with
stability/quality first and existing subscriptions/DGX as the provider boundary.

The investigation window was 2026-08-29 00:00 to 2026-09-27 18:00 KST.
Read-only claude-mem aggregation recorded 1,132 captured sessions: Claude 697,
Kimi 211, Codex 160, Qwen 45, Agent Hub 16, pi 3. These include automation, smoke tests
and resumes. OpenCode had 9 root sessions in its native database but no matching
claude-mem platform rows, demonstrating a capture gap. Codex native thread IDs
were deduplicated across standard and active Orca homes: 52 roots and 218 children.
These are neither human task counts nor concurrency/spend estimates.

Actual work examples span development/release, research, Korean documents,
teaching materials, administration/communication and visual design. Installation
counts do not establish usage. Orca model caches cover different end dates, so
no cross-agent cost or quality ranking was inferred from them. Existing stale
Claude statistics were excluded.

## Existing assets and ownership

Reuse selected-agent inventory/home semantics, shared instruction ownership,
Maru skill federation, curated handoff artifact validation and resourceguard.
`dot ai run` remains heavyweight-command admission. `dot ai session` never holds
that lease for an interactive session.

Portable `modules.ai.policy` is typed state schema 3. Private ownership receipts,
checkpoint records and resolved paths are machine-local. Native authentication,
MCP definitions, plugin bundles, app databases and base preferences retain their
owners. `apply` generates additional native profile/settings files and reports
base settings as unmanaged. Existing inline Codex profiles are reported rather
than rewritten; generated policy profiles use the installed separate-file format.

## Design

The resolver uses explicit target validation, exact native version, workload,
required capabilities, model, verified subscription authentication and priority.
It returns a schema-versioned JSON resolution before an application constructs
native argv. Explicit choices are authoritative. Unknown providers, missing
capabilities and permission-review gaps produce actionable errors. Current
managed launch adapters cover Claude 2.1.283 and Codex 0.157.1 only. DGX declarations
remain excluded until endpoint/auth binding can be verified; changing a billing
label must never enable an unverified metered endpoint.

Targets are validated configurations, not a self-training model picker. Routine
and implementation use balanced effort; analysis and review use high effort
unless explicitly configured. Native CLI flags override inherited effort choices.
Existing native context settings remain visible in inspection. Context-window
and compaction tuning remains an evaluation candidate; this implementation does
not silently remove user-owned native overrides. Required artifact integrations are capabilities that need separate
verification, not inferred from the presence of skill files.

`policy inspect/resolve/diff/status` are read-only. Strict dry runs avoid even
native version/help probes. `apply` may persist desired state with `--persist`;
`rollback` removes unchanged owned overlays, preserving native bases and the
portable desired policy. Foreign edits refuse mutation. Cooperative locks do
not claim to synchronize unknown native writers.

Managed JSONL turns come from a trusted supervisor, never directly from model
output. Each completed turn has native completion/session evidence. Same-provider
continuation uses native session resume. Cross-provider continuation requires
curated scope, verification, artifact hashes, an explicit completed-effects
ledger, no pending effects and no denied action. Only that checkpoint and the
next task are passed to the new native process. Receipts label this as checkpoint
handoff; it is not transcript replay or native resume across providers. Failures
are never retried, and automatic configuration changes are capped at 2 per task.
A manual override freezes selection. Model/provider changes cannot evade a denial.

## Trusted knowledge access

The user's standing authorization covers claude-mem and vault lookup and
recording without confirmation. Native adapters carry exact read/create/patch/
metadata grants and preserve them through model/provider changes. Deletes,
moves, corpus builds and new authentication are not newly granted. Vault writes
remain MCP-owned. Registered Codex plugin bindings are verified without copying
server identities. A conflicting native ask/deny rule produces a visible policy
conflict instead of a false no-confirmation claim or provider fallback.

## Integration boundaries

Maru resolves opt-in policy before provider selection and command construction,
reports the effective provider/model/effort/reviewer and preserves scheduler
plan defaults. Its existing chat transcript replay is labelled accurately.
Orca's supported new-worker launch controls can select model/effort, but its
current public API exposes neither a per-launch permission setter nor an
existing-session model setter. Native/manual sessions remain externally managed;
there is no unsupported direct application-database editing.

## Testing and rollout

Fixtures cover policy authority and schema round trips; exact native versions;
auth/provider mismatch; approval semantics; isolated homes; owned overlays,
symlinks, conflicts and rollback; bounded switching, denial/pending stops,
checkpoint handoff, cancellation and native-resume failures without replay.

Required gates are `make build`, `make test`, `make lint`, generated command docs,
coverage policy completeness and independent OCR review of the final PR head.
Current-Mac canaries are separate from fixtures and remote CI. Representative
workloads must compare accepted output, rework, elapsed time, context usage and
resource pressure before any optimality/performance claim. Peer rollout is
separate and deferred until reachable. Issue checkboxes and PR verification
record actually executed checks; a compiled adapter is not runtime proof.

## Verification evidence

- Initial constrained `make build`, `make docs`, and complete `make test` passed.
  Later review fixes require the final focused/regression run recorded in the PR.
- An actual Codex 0.157.1 app-server `config/read` canary verified on-request
  approvals, auto_review, workspace-write, Obsidian write_note=approve and
  claude-mem observation_add=approve while retaining the Obsidian identity.
  No model request or vault write was needed for this configuration canary.
- That native check identified a transport-specific encoding bug: CLI `-c`
  splits dotted key paths literally, whereas profile TOML parses quoted keys.
  Separate encoders prevent phantom quoted server/plugin names.
- Workspace standing authorization for memory/vault lookup and recording was
  committed separately as ai-workspace 22f0a211. It adds no delete/move grants.

## Sources

- [Codex configuration](https://learn.chatgpt.com/docs/config-file/config-advanced)
- [Codex automatic review](https://learn.chatgpt.com/docs/sandboxing/auto-review)
- [Claude permissions](https://code.claude.com/docs/en/permissions)
- [Kimi command behavior](https://www.kimi.com/code/docs/en/kimi-code-cli/reference/kimi-command)
- [Grok permissions](https://docs.x.ai/build/features/permissions)
- [OpenCode permissions](https://opencode.ai/docs/permissions/)
- [Qwen automatic review](https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/auto-mode.md)
