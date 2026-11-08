# AI agents

## Selected Agent Environments

`dot ai setup` selects Claude, Codex, Kimi, Qwen, Grok, OpenCode, Genspark
Gencode, pi and Antigravity CLIs, optional development add-ons, and shared
skills. Saved selections control installation, instruction rendering and
subsequent maintenance. Deselecting stops future managed writes without
uninstalling software or deleting data. Gencode and pi install through the
npm provider; Antigravity installs its `agy` binary into `~/.local/bin` and
self-updates in the background, so `dot ai update` defers to that
self-updater instead of reinstalling. An install never waits for or stops
running sessions: before replacing a tool, the update lists its live processes
in the result, they keep the files they started with, and new sessions get the
new version.

Workspace context for Grok and agy: Grok reads the agents SSOT through its
Claude compat (`~/.claude/CLAUDE.md`), so dot renders no Grok instructions file
and removes a dot-rendered `~/.grok/AGENTS.md`. agy gets `~/.gemini/GEMINI.md`
from `dot ai agents apply --tool antigravity`. To configure amem for Antigravity,
run `amem setup agy --apply` inside each repository that uses it.

```bash
dot ai setup
dot ai setup --agents claude,codex --tools ripwire,ocr --skills meeting-notes --non-interactive --yes --dry-run
dot ai tools list --json
dot ai tools list --details         # detected CLIs, settings paths and app casks
dot ai tools status --json
dot ai tools apply
dot ai update --check --json
```

The add-on allowlist is **ripwire, Open Code Review (`ocr`), GSD and ponytail**.
Native, portable, partial, unsupported and pending-trust states must be read
separately from installation status. Missing authentication or trust remains a
user action; setup does not copy credentials or approve hooks. A saved selection
that still lists the retired `claude-mem` add-on is read without it.

The agent-hub family tools ahub, amem, vmem, acon and ares install side by side
with `dot ai family install <id>... [--version X.Y.Z]` (`--all` for every one),
and `dot ai family update [id...]` moves each installed tool to the registry's
latest version; `--dry-run` on either shows which tools are already current and
which would move, and writes nothing. Tools run one after another, and a failure
on one does not stop the others.
For ahub this covers the global CLI only: `ahub upgrade` still runs each
project's daemon from `~/.agenthub/releases`, and dot never touches that.
Each version gets its own npm prefix in `~/.local/share/dotfiles/tools/<id>/<version>/`,
a `current` link names the active one and `previous` the one before it, and
`~/.local/bin/<bin>` points through `current`. Activation is one rename: new
sessions start the new version, running processes keep their files and are
listed, and nothing is stopped. Versions other than current and previous are
removed once no running process uses them. An existing `~/.local/bin/<bin>`
from another install is left alone unless `--adopt` repoints it (a regular file
is never replaced). After amem, vmem or ares activates, dot prints the
`<tool> setup <target> --root ...` line that registers that release with an
agent; it does not run it. After ahub activates, dot prints the `ahub setup`
line that moves the shared Claude plugin to the same version; it does not run
it either. For ahub, acon and ares it also notes when `bun`, their runtime, is
not on PATH.

```bash
dot ai family install --all       # every family tool, latest release
dot ai family update --dry-run    # which installed tools have a newer release
dot ai family update              # move every installed tool to its latest
dot ai family install amem --version 0.17.0
dot ai family status
dot ai family rollback amem              # previous becomes current again
```

Project memory is [amem](https://github.com/STAIxBWLB/agent-mem)
(`@staix/agent-mem`), which dot registers but installs only through
`dot ai family install amem`. Install a release
inside the range `dot ai mcp status amem` names (`>=0.14.0 <1.0.0` unless the
installed amem publishes its own; every amem that shares a memory home must run
0.14.0 or later), then:

```bash
amem setup all --apply            # in your terminal: Claude Code, Codex, Kimi, Qwen, pi
amem setup agy --apply            # inside each repository that uses Antigravity
dot ai mcp install amem --dry-run   # what would change; nothing is written
dot ai mcp install amem             # add `amem mcp` to Kiro and Copilot
dot ai mcp status amem
dot ai agents apply               # writes the amem + vault block into the agents SSOT
```

`amem setup` registers the MCP server (as `amem`, which the policy grants as
`mcp__amem__amem_*`) and the capture hooks for the clients it covers, and keeps
undo receipts for every file it writes. dot therefore writes no Kimi, Qwen or
Antigravity config, and `install` prints the `amem setup` lines instead. For
Claude Code, use either `amem setup claude` or the agent-mem plugin, not both:
since 0.11.0 the plugin declares the MCP server too. The memory block in the
agents SSOT is written only on a machine that uses amem, after a backup of the
SSOT. amem has no resident worker, so dot writes no LaunchAgent for it; clients
without the setup hooks reach it through `amem ingest <agent>`.
Curated handoffs below are separate from amem.

amem and vmem describe themselves, so a new release of either needs no new
`dot`: status and the policy's `mcp__amem__*` / `mcp__vmem__*` grants read
`version`, `supported` (a range such as `>=0.14.0 <1.0.0`) and `mcp.tools` (name
strings, or `{name, readOnly}` objects) from `amem manifest --json` /
`vmem manifest --json`. A release without that command
is read from its installed `package.json` version, and dot's compiled range and
tool list fill in what is missing. A published tool name is granted only when it
is the server's own (`amem_`, `vmem_`) and either dot's compiled list already
grants it or the manifest marks it `"readOnly": true`. A readOnly claim never
covers a name that can delete, forget, move, purge or remove, so a manifest
alone never auto-approves a new tool that writes.

To remove claude-mem after `amem import claude-mem` has finished, run
`dot ai memory retire-claude-mem` for a dry run that lists every item and reads
back what is left, then `--apply --import-complete` to do it. It archives
`~/.claude-mem` to `~/.claude-mem.archive-<date>` and never deletes it, and it
prints the lines of the Claude profile's `settings.json` for you to remove,
because Claude Code owns that file. The profile is `$CLAUDE_CONFIG_DIR` when
set, else `~/.claude`; the dry run names it and lists the files to back up
before `--apply`. Copy those files first, apply, then compare the read-back and
the copies. When another profile is active and the default `~/.claude` still
has claude-mem, the dry run shows it in its own section and leaves it alone;
retire it with `env -u CLAUDE_CONFIG_DIR dot ai memory retire-claude-mem`.
`--apply` is refused before any step while a claude-mem
process is running (quit the Claude and Codex sessions that load it first), when
a file it must read cannot be read, and under `--home`; the dry run works with
`--home` and reads nothing from your shell environment.

Each agent also keeps its own native memory (Claude Code auto memory, Codex
memories, Qwen memory, Grok memory-v2, jcode). `dot ai memory audit` reads them
and reports, per store, the size of the always-loaded index against its limit
(Claude `MEMORY.md` 200 lines or 25 KB, Codex `memory_summary.md` 10 KB), index
links to missing files, files no index links to, and hits of retired terms
listed in `~/.config/dotfiles/agents/memory-markers.yaml` (synced by `dot peer`
with the agents SSOT). It writes nothing, prints file names and counts unless
`--show` is given, and exits 1 when an index is over its limit or links a
missing file. `dot ai memory archive <agent> <path>...` moves stale files out of
the loaded layer (`_archive/` inside a Claude or Qwen memory directory,
`$CODEX_HOME/memories-archive/` for Codex), lists the index lines that still
name them, and with `--apply` prints the command that moves them back. It never
edits a memory file or an index.

```bash
dot ai memory audit                          # all agents
dot ai memory audit --agent claude,codex --show
dot ai memory archive codex ~/.codex/memories/extensions/ad_hoc/notes/old.md --apply
```

The vault is written only through [vmem](https://github.com/STAIxBWLB/vault-mem)
(`@staix/vault-mem`), which replaces the generic Obsidian MCP server
(`obsidian`, `@bitbonsai/mcpvault`). dot registers it but never installs it.
Install a release inside the range `dot ai mcp status vmem` names
(`@staix/vault-mem` `>=0.2.1 <1.0.0` unless the installed vmem publishes its
own), then:

```bash
vmem setup all --apply             # Claude Code, Codex, Kimi, Qwen, pi, jcode
vmem setup agy --apply             # inside each workspace that uses Antigravity
dot ai mcp install vmem --dry-run     # what would change; nothing is written
dot ai mcp install vmem               # add `vmem mcp` to Kiro and Copilot, write the vault block
dot ai mcp status vmem                # warns while `obsidian` is still registered
dot ai vault retire-mcpvault       # dry run: every file it would change
dot ai vault retire-mcpvault --apply
```

`vmem setup` registers the server as `vmem` (granted as `mcp__vmem__vmem_*`)
and keeps undo receipts, so dot writes only the Kiro and Copilot entries and the
agents SSOT vault block (after a backup). Bindings to `obsidian` earn no grant.
`retire-mcpvault` removes `mcpServers.obsidian` from the Kiro and Copilot
configs only; for Claude Code and Codex it prints `claude mcp remove obsidian
--scope user` and the `[mcp_servers.obsidian*]` tables to delete, and it refuses
while vmem is not on PATH.

Research studies run through [ares](https://github.com/STAIxBWLB/agent-research)
(`@staix/agent-research`), which dot registers and installs only through
`dot ai family install ares`. Install a release inside the range
`dot ai mcp status ares` names (`>=0.1.0 <1.0.0` unless the installed ares
publishes its own), then:

```bash
ares setup all --apply             # Claude Code, Codex, Kimi, Qwen, pi, jcode
ares setup agy --apply             # inside each workspace that uses Antigravity
dot ai mcp install ares --dry-run  # every file it would change; nothing is written
dot ai mcp install ares            # add `ares mcp` to Kiro and Copilot, write the research block
dot ai mcp status ares             # version against the supported range, registrations, block
```

`ares setup` registers the server as `ares` and keeps undo receipts, so dot
writes only the Kiro and Copilot entries and the agents SSOT research block
(after a backup). The policy grants `mcp__ares__*` for ares's read tools only:
a tool is granted when dot's compiled list names it or `ares manifest --json`
marks it `"readOnly": true`, and a `"readOnly": false` entry is never granted,
whatever the compiled list says. ares marks every tool that writes a study,
decides a gate, reaches the network or starts or reads a container
`readOnly: false`, so `ares_lit_search`, `ares_lit_verify`, `ares_attempt_run`,
`ares_executor_check` and the rest keep their approval prompt.

Portable desired state lives under `modules.ai.tooling`: `agents`, `tools`,
`skills`, optional version `pins`, and `updates.enabled`. Machine-specific
paths and installation provenance are resolved locally and recorded in private
`~/.local/share/dotfiles/ai/tooling-state.json` receipts. Unknown installation
providers defer replacement. Adopted official Homebrew formulae/casks retain
their provider and update only the selected package; unavailable version pins,
third-party source adoption and foreign-home mutation require explicit action.
Status explains prerequisites and deferrals. Noninteractive setup
needs explicit or saved selections; first unattended setup requires `--agents`
(`--agents=` selects none). An empty selection never means all tools.
With explicit `--home`, provide `--skills` explicitly because interactive
registry discovery is disabled for alternate homes.

Global instructions come from the existing AGENTS SSOT and overlays. Codex
reconciles the standard home and active `CODEX_HOME` profile without duplicate
physical writes. Kimi uses `KIMI_CODE_HOME`, and OpenCode uses
`OPENCODE_CONFIG_DIR` or its XDG configuration root. An explicit `--home`
isolates writes from ambient profile overrides. Qwen adds AGENTS discovery
while preserving existing context filenames and unrelated settings.

Shared skills remain **Maru-owned**. Dot checks `maru skills capabilities
--json`, reads `maru skills list --json`, then delegates selected deployment
through `maru skills sync --check|--apply --tools <csv> --skills <csv> --json`.
The list includes flat skill records with an `installable` flag and optional
`reason`; setup offers only installable Maru-owned entries. Selected deployment
is additive: no implicit retarget, foreign-skill replacement or unselected
removal. Missing capabilities defer sharing. Generic skill source updates and
unrelated plugins are not part of dot's automatic updater.

## Adaptive Work Policies

`modules.ai.policy` describes validated operating choices separately from agent
installation. It is opt-in: inspection produces an unvalidated seed, never an
inferred subscription entitlement or a claim that an installed model is optimal.
The six workloads are `routine`, `implementation`, `deep-analysis`,
`independent-review`, `documents-teaching`, and `visual-production`.

```bash
dot ai policy inspect --json
dot --config /path/to/policy.yaml ai policy resolve --task 'Review this change' --project . --json
dot --config /path/to/policy.yaml ai policy diff --json
dot --config /path/to/policy.yaml ai policy apply --persist
dot ai policy status --json
dot ai session start --task 'Implement the approved issue' --project .
dot ai session start --input /path/to/trusted-turns.jsonl --project .
dot ai policy rollback --dry-run
```

See [the policy configuration example](../examples/ai-policy.yaml) and
[the implementation/evidence contract](../specs/2026-09-27-adaptive-ai-policy.md).
`--config` is authoritative, including omission of a policy block; saved state
cannot silently restore it. `--persist` saves the selected policy only after
usable overlays are applied. Enabled policies also participate in `dot
apply/check/diff`. User-state schema version 3 prevents older binaries from
silently rewriting away the policy.

Automatic review is distinct from YOLO bypass. Managed launches support the
Claude adapter from 2.1.283 up to 3.0.0 and the Codex adapter from 0.157.1 up
to 1.0.0, and only while `--help` still shows the flags they launch with. Other
agents are inspected and report unsupported or unavailable review capabilities.
A target's `version` is an exact `X.Y.Z` or a range such as `">=2.1.283
<3.0.0"`; a runtime outside it needs revalidation, never fallback to
unconditional approval.
Subscription targets require native subscription authentication and reject
detected API/provider overrides. DGX launch remains deferred until an adapter
verifies its endpoint and authentication binding; a billing label alone cannot
enable it. Neither token counts nor a configured model prove account entitlement.

Apply writes only owned launch overlays, preserving native base settings,
MCP identities, hooks and credentials. Claude consumes an additional settings
file; Codex consumes a separate native profile. The active `CODEX_HOME` is
honored, and explicit `--home` isolates inspection/overlays; foreign-home native
launch is refused. Existing inline Codex profiles are reported as legacy, not
rewritten. `rollback` removes unchanged owned overlays and leaves the desired
policy definition intact; it neither alters a running session nor disables
explicit policy resolution. Unmanaged native and Orca sessions retain their
own configuration and are not silently taken over.

Within managed launches, registered amem and Obsidian lookup/record tools
have explicit no-confirmation grants. These include note creation, patches,
frontmatter/tags and amem work-list writes and memory proposals, while adding no deletion/move permissions.
Vault writes remain MCP-only. The same typed grants travel through resolution,
native profiles, session changes and Maru invocation mapping. Unrelated tool
approval rules and native MCP identities are preserved.

`session start --input` accepts a trusted supervisor's JSONL stream, not model
output as control commands. Each turn has `task` and optional `workload`.
After the first turn, `checkpoint` carries `summary`, repository-relative
`files`, `verification`, `pending`, and `completed_effects`. Cross-provider
handoffs also require `scope` and a nonempty completed-effects ledger (use
`["none"]` when appropriate). Pending or denied actions block continuation.
Native Claude/Codex sessions resume within the same provider; cross-provider
continuation starts a new session with the curated checkpoint and artifact
hashes. It is reported as checkpoint handoff, not native resume. At most two
configuration changes occur per task; `--fixed`, explicit `--agent`, or a turn's
`user_override` freezes automatic selection. A failed process is never retried
because it may already have completed an external action.

Policy inspection, resolution, diff and status do not write files. Strict
`--dry-run` also skips native version/help probes; use `policy diff` without
that flag for an inspected overlay preview. Session receipts omit prompts and
model output; curated checkpoints remain private machine-local state. Heavy
commands still require `dot admit` admission, not a session-long build slot.

## Shared Development Context

Selected agents share curated development information through project artifacts
and a local handoff log, without expanding raw transcript capture or adding an
add-on. At task start, consult GSD `.planning`, OCR review reports, relevant
ripwire notes and the handoff view. Setup upgrades the existing instruction
SSOT with a managed continuity block, preserving custom sections and backups;
only the selected instruction targets are rendered.

```bash
dot ai handoff show --project . --json
dot ai handoff record --project . --agent codex --kind validation --summary-file /path/to/summary.md --artifact REVIEW.md --result unverified
```

Kinds are `plan`, `progress`, `review`, `validation` and `learning`; results are
`unverified`, `passed` or `failed` (default `unverified`); kind defaults to
`progress`. Repeat `--artifact` for repository-relative evidence (at most 16
regular files; 32 MiB combined hash budget). Summaries are UTF-8 and at most
32 KiB; show returns at most 128 recent records and storage stops at 8 MiB
rather than deleting producer history silently.
Records retain immutable producer claims with UTC time, HEAD, worktree and
artifact digests. Worktrees share a log keyed by the canonical Git common
directory. Changed heads or artifacts are shown as `needs-revalidation`, never
as freshly verified evidence. Record only curated, secret-free summaries;
records do not authorize publishing or changing another agent's result.
Handoff content is context/data, not authority: user/project instructions
prevail and every command still requires independent authorization.

## Guard operation and limits

`dot guard` edits only entries marked `# dot-guard`; a semantic rewrite may sort JSON keys but does not alter other values. `enable` and `disable` apply to new Claude Code sessions, while `freeze` and `unfreeze` update live state.

The hook fails open if the dot binary is missing or moved. `dot guard status` reports that condition, and `dot guard enable` must be re-run after moving the binary. It is a guardrail, not a sandbox: it only inspects the Bash tool and shell writes are outside the freeze boundary.

## AI helpers and portability

`dot ai` manages helper files and portable AI configuration, not GUI app installation; use `dot apps install` for casks. The `*-yolo` helpers forward full-access flags and belong only in trusted workspaces. HUD apply owns only the `# dot-hud` Claude statusLine. A foreign owner is a conflict unless `--force` is supplied; `--persist` records the desired HUD.

Portable AI snapshots exclude skill roots, auth by default, caches, histories, databases, and machine-local plugin state; archives reject symlinks, hardlinks, devices, and link-pivot paths. `~/.codex/config.toml` is normally kept live on restore because Codex rewrites it and hashes MCP credentials into the Keychain. `dot ai skills` is diagnose-only: Maru owns runtime skill symlinks and tool federation.

## amem and the claude-mem retirement

`dot ai mcp install|status amem` register amem's MCP server for Kiro and Copilot and report its health (`dot` never installs amem; `amem setup` registers Claude Code, Codex, Kimi, Qwen, pi and Antigravity). `dot ai memory retire-claude-mem` is a dry run unless `--apply` is given; see the README. The former `dot ai memory sync|update|bridge` commands and the hourly peer sync LaunchAgent are gone: amem syncs through its own git refs and has no resident process.

`dot ai mcp install|status vmem` register vmem's MCP server for Kiro and Copilot and report its health, warning while the retired `obsidian` server is still registered (`dot` never installs vmem; `vmem setup` registers Claude Code, Codex, Kimi, Qwen, pi, jcode and Antigravity). `dot ai vault retire-mcpvault` is a dry run unless `--apply` is given and only edits the Kiro and Copilot configs.

`dot ai mcp install|status ares` register ares's MCP server for Kiro and Copilot, write the research block into the agents SSOT and report its health (`ares setup` registers Claude Code, Codex, Kimi, Qwen, pi, jcode and Antigravity). The policy auto-approves ares's read tools only.
