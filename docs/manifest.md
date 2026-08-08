# Manifest Reference

Platoon accepts one strict YAML document matching
`schema/platoon.schema.json`. Unknown fields, aliases, merge keys, duplicate
mapping keys, extra documents, unsupported versions, missing dependencies, and
cycles fail validation.

The published Draft 2020-12 schema is compiled in repository tests. Shared
negative fixtures keep its opaque-ID and path lexing aligned with runtime
validation.

Relative runtime paths resolve from the manifest directory when an applied run
starts. Bare executable names resolve through the operator's process `PATH`.

## Top Level

| Field | Required | Meaning |
|---|---:|---|
| `apiVersion` | yes | Exactly `platoon.dev/v1alpha1`. |
| `kind` | yes | Exactly `Platoon`. |
| `metadata.name` | yes | Lowercase run-name slug. |
| `spec.project` | yes | Synthetic/portable Sergeant project slug. |
| `spec.mission` | yes | Safe relative mission reference. |
| `spec.missionFormat` | no | `reference` (the default) or `declaration-v1alpha1`. |
| `spec.intent` | yes | Safe relative canonical intent reference; its SHA-256 binds child evidence. |
| `spec.limits` | yes | Capacity and process bounds. Empty `{}` selects defaults. |
| `spec.adapters` | yes | Dagr command/database and Sergeant command/fleet-state adapters. |
| `spec.routing` | yes | Allowed model/risk/harness combinations. |
| `spec.repositories` | yes | Repository-local write and integration policy. |
| `spec.stages` | yes | One issue-owned implementation or review unit each. |

## Mission Format

Reference mode validates only the relative `spec.mission` path and never reads
the referenced file. This includes explicit `missionFormat: reference`; typed
content is not inferred from filenames or file bodies. An explicitly present
empty `missionFormat` is invalid.

`missionFormat: declaration-v1alpha1` requires a strict, regular, non-symlink
mission file no larger than 1 MiB. The declaration schema is
`schema/mission.schema.json`; `examples/platoon-typed.yaml` demonstrates it.
Compilation validates closed mission classes, effects, stops, authority tuples,
unknowns, contradictions, output categories, handoffs, and source kinds. Each
manifest stage must have an effects entry, and each class must declare its exact
required completion outputs. Validation prints a concise human preview while
plan and non-applied start return the matching machine-readable preview.

Each source kind selects one closed schema identifier. Stop fields, operators,
and bounded values must match that schema; scoped stages must exist; every
disposition route must have exactly one `owner-may-disposition` assumption; and
effect authority must use the owning adapter source kind. Every schema-required
field must be explicitly present. Ready entry additionally requires the declared
orchestration effects and complete source/actor authority.
String fields reject YAML scalar coercion, Git source revisions are full object
IDs. `write-claimed-source`, `receiving-system-operation`, and
`request-sergeant-lifecycle` are invalid for review stages.
Every allowed effect must have an effective caller tuple. Disposition-owner
assumptions govern routes only and therefore require an empty effects list.
Mutable observation-policy sources and entry stops remain not ready until a
future source bundle can supply evidence. Failures expose only fixed
`mode`, `schema`, and `reason` diagnostics.

This phase is preview-only. Typed `start --apply` refuses before state creation
or adapter invocation.

## Limits

| Field | Default | Range |
|---|---:|---:|
| `implementation` | 6 | 1 through 128 |
| `review` | 2 | 1 through 128 |
| `leaseTTL` | `5m` | 1 second through 24 hours |
| `commandTimeout` | `2m` | 1 second through 24 hours |
| `maxOutputBytes` | 65536 | 1024 through 1048576 per stdout/stderr stream |

Defaults apply only when a field is omitted. Explicit zero or empty values do
not select a default and fail range validation.

## Adapters

`spec.adapters.dagr` has `executable`, optional `args`, `database`, and
`inspectExecutable`. The inspector is the `sqlite3` CLI (or a compatible
argument-array executable) used with `-readonly` only for full-ID crash recovery.

`spec.adapters.sergeant` has `fleetRoot`, required `originProfile`, and command
objects for `dispatch`, `watch`, `wake`, and `drain`. A command object has an
`executable` and optional string `args`. No shell command string or environment
map is accepted.

The callback origin profile is mandatory because a durable opaque correlation is
the only automatic recovery key after a lost dispatch receipt.

## Routing

Every route has `model`, `risk`, and `harness`; the harness is `opencode`,
`goose`, or `claude`. Every stage must match exactly one route. Platoon
dispatches the selected harness; the Sergeant project/harness configuration maps
the policy model class to a concrete provider model.

## Repositories

| Field | Meaning |
|---|---|
| `id` | Lowercase repository slug used by Sergeant. |
| `path` | Existing integration repository path. |
| `branch` | Child issue-branch prefix. Dispatch appends `-<stage-id>`. |
| `maxWriters` | Concurrent implementation writers; default 1. Claims must still be disjoint. |
| `integration` | Ordered non-shell commands required before merge-ready. |

No integration command pushes or merges implicitly. Operators should configure
read-only build/test/lint commands.

## Stages

| Field | Meaning |
|---|---|
| `id` | Lowercase slug, at most 24 characters. |
| `repository` | Exactly one declared repository. |
| `task` | Exactly one existing task identity. |
| `mode` | `implementation` or `review`. |
| `harness`, `model`, `risk` | Must match a route. |
| `dependsOn` | Stage IDs; duplicates, missing IDs, self-edges, and cycles fail. |
| `claims.paths` | Normalized relative subtree prefixes. Implementations require at least one. |
| `claims.semantic` | Normalized named write domains. Implementations require at least one. |
| `acceptance` | Ordered command objects required for merge-ready. |
| `adoptFleet` | Optional explicit pre-existing Sergeant fleet ID. |

Review stages are read-only: both claim arrays must be present and empty. Their
acceptance commands may inspect the worktree, but any resulting changed path
fails closed.

Adoption does not bypass policy. Verified adopted fleets consume tokens and
claims. Mismatched, malformed, over-capacity, or conflicting adoption evidence
blocks new admission without changing the fleet.

Opaque IDs use 1-128 ASCII characters from `[A-Za-z0-9._-]`. Exact `.` and any
`..` substring are invalid, as are separators, whitespace, controls, and
Unicode. An explicitly present empty `adoptFleet` is invalid.

## Claims

Path claims use slash separators, no glob metacharacter, no absolute path, no
`.`/`..`, no backslash, and no control character. A claim covers itself and
descendants only with exact case. Conflict detection additionally case-folds
paths to preserve safety on case-insensitive filesystems.

Semantic claims normalize case, spaces, underscores, and slash separators to
hyphens. Exact normalized overlap conflicts. Protected domains are exclusive:

- migration and migrations;
- state-machine and state-machines;
- authorization;
- identity;
- recovery;
- purge;
- release;
- destructive and destructive-behavior;
- repository-integration.

Qualified protected names remain protected. Claim list ordering and case do not
change policy.
