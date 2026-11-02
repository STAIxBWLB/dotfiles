# Review instructions

Review the changed code against its linked issue and `docs/BOUNDARIES.md`.
Classify reproducible findings as bugs, security, or contract compliance; style
issues are secondary to incorrect behavior and loss of user configuration.

- Verify explicit-home and active-profile isolation, configuration precedence,
  unknown-version handling and no hidden provider or approval-mode fallback.
- Approval review, unconditional approval, sandboxing and account entitlement
  are separate capabilities. Do not accept flag names as evidence of equivalence.
- Native credentials, MCP identities and third-party settings are preserved.
  Owned writes require conflict detection and safe, targeted rollback.
- Session transitions must preserve scope and verification, reconcile pending
  effects, distinguish native resume from handoff and never replay side effects
  after an ambiguous failure or evade a review denial.
- Read-only and dry-run operations must not initialize services or write state.
- Trace verification claims to actual commands/results and distinguish fixture,
  native runtime, CI, current-Mac and peer evidence.
- Error messages: a new or changed error, refusal or failure message a person or agent sees names the cause (what failed and why) and the next step (the command, setting or target that fixes it). When secrets, ids or paths must stay out, it names the cause class and how to check it. A message missing either is a finding; Important when it is the only signal of a blocked action (a CLI exit, a refused tool call), otherwise a nit.

Record the reviewed head/base and account for all changed files. A review does
not authorize merge or substitute for required CI and runtime validation.
