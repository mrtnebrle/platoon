# Threat Model

## Assets

- Admission capacity and repository write exclusivity.
- Commander generation and durable run history.
- Child fleet identity, task/repository binding, and terminal evidence.
- Dagr stage identity and exactly-once logical advancement.
- Operator credentials inherited by trusted adapter executables.
- Confidential command output and child content.

## Trust Boundaries

The operator controls the manifest, executable search path, adapter binaries,
state-root parent, repository paths, and environment. Dagr's database and the
Sergeant fleet root are separate authorities. Child worktree content and command
output are untrusted evidence.

Platoon is not a sandbox for malicious executables. Operators must pin and
secure the dagr, Sergeant, Git, and acceptance executables they configure.

## Threats And Controls

| Threat | Control |
|---|---|
| Concurrent or stale Commander writes | Nonblocking OS lease lock, generation fencing, expiry, same-host check, and PID absence proof. |
| Partial or reordered state publication | Same-directory create/write/sync/rename/directory-sync with strict JSON readers. |
| Duplicate dispatch after a crash | Durable provisional reservation before dispatch; serialized dispatch; exact callback correlation; ambiguity blocks. |
| Global credential race inside Sergeant | One user-global dispatch file lock across repositories, runs, state roots, and fleet roots. Platoon never switches identity. |
| Dagr-ready work bypasses policy | Hookless generated workflows; only the admission transaction calls dispatch. |
| Token or claim overcommit | Run-local capacity plus a restrictive per-user claim registry covering every state root, including adopted fleets. |
| Case-insensitive path bypass | Case-folded overlap and coverage checks on normalized relative slash paths. |
| Path traversal or symlink escape | No absolute/traversing claims; bounded NUL-delimited Git parsing; changed symlinks fail closed; authority paths reject symlinks. |
| Forged child success | Exact one-repository project/task/stage/branch/intent/correlation binding; `done` requires non-empty result; stable bounded reads. |
| Out-of-claim child work | Diff from Sergeant's recorded initial SHA; deleted, renamed, modified, ignored/untracked, and historical symlink paths checked before and after integration commands. |
| Child-controlled Git metadata | Pinned worktree/Git-dir identity, private base index, replacement-object suppression, and filtered Git environment. |
| Secret or content leakage | No shell interpolation; bounded separate output; failed output omitted from errors; only result digest and sanitized paths persist. |
| Descendant process leak | Commands run in a new process group; deadline kills the group and bounds pipe wait. |
| Stale candidate integration | Base SHA checked before and after commands; changed bases return to queued. |
| Automatic publication | No merge or push operation exists in the Commander or command integrator. |

## Failure Windows

- Before a provisional reservation is durable, dispatch is forbidden.
- A prepared reservation is known pre-dispatch and can resume once. After the
  dispatching transition, restart requires unique durable correlation. Proven
  absence permits one bounded retry; exhaustion or multiple candidates block.
- After terminal child evidence and before local release, reconciliation repeats
  verification and releases idempotently.
- Before dagr terminal mutation, the desired terminal transition is journaled.
  It remains pending until dagr post-verification succeeds.
- An interrupted `integrating` candidate returns to queued because no success
  record exists. Acceptance commands rerun on the current base.
- A state write failure leaves either the old complete file or the new complete
  file authoritative; temporary files are never read as authority.
- Initialization publishes supporting intent/workflow files before `state.json`;
  a crash before the final write leaves no run authority.

## Residual Risks

- Human-oriented upstream receipts can change without a version signal. Strict
  parsing converts drift to an operational blocker, not silent acceptance.
- PID reuse can cause conservative stale-lease refusal. It cannot cause unsafe
  takeover.
- A host loss leaves its lease ambiguous to another host. The first release has
  no distributed consensus or cross-host recovery override.
- A dispatch that fails before publishing callback correlation may have partial
  upstream side effects. Platoon blocks for operator evidence rather than
  guessing or redispatching.
- Trusted commands can mutate their working tree or external systems. Manifests
  must use non-destructive validation commands; Platoon cannot sandbox them.
