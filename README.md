# Platoon

Platoon is a standalone Go Commander for coordinating existing Sergeant fleets
over a dagr workflow. Dagr owns acyclic readiness. Platoon owns fenced
admission, implementation/review capacity, repository policy, path and semantic
claims, child reconciliation, and a durable merge-ready queue.

Platoon does not copy or modify Sergeant or dagr. It never changes GitHub
identity, rewrites child status, pushes, merges, or activates production.

## Install

Go 1.24 or newer, dagr, Sergeant, Git, and the `sqlite3` CLI are required.
`sqlite3` is used read-only to recover full dagr run IDs after a crash.

```sh
go install github.com/mrtnebrle/platoon/cmd/platoon@latest
```

From a source checkout:

```sh
make install
```

## Quick Start

The repository includes a complete synthetic manifest.

```sh
platoon validate --file examples/platoon.yaml
platoon plan --file examples/platoon.yaml
platoon start --file examples/platoon.yaml
platoon plan --file examples/platoon-typed.yaml
```

The third command is a preview. It does not create a state directory or invoke
dagr, Sergeant, or Git. Add `--apply` only after inspecting the plan:

```sh
platoon start --file examples/platoon.yaml --state .platoon --apply
```

## Commands

| Command | Behavior |
|---|---|
| `validate --file <manifest>` | Strictly validates YAML and domain rules without side effects. |
| `survey --file <manifest> --caller <role> [--stage <id>]` | For typed missions, applies the common read/query gate and prints a bounded `platoon.source-bundle/v1alpha1` without Platoon state or effects. Reference missions return unsupported. |
| `plan --file <manifest> [--source-bundle <file>]` | Prints deterministic JSON admission decisions and, for typed missions, an offline reproducible packet preview when a valid bundle is supplied. It never queries mutable sources. |
| `start --file <manifest> [--state <dir>] [--source-bundle <file>]` | Prints the same read-only admission and typed-mission packet preview. |
| `start --file <manifest> [--state <dir>] --apply` | For reference missions, persists and starts the existing run. Typed missions require `--source-bundle` and perform revalidation only; they publish no state or effect in this phase. |
| `reconcile --run <id> [--state <dir>]` | Prints local durable status as a read-only preview. |
| `reconcile --run <id> [--state <dir>] --apply` | Runs one bounded reconciliation cycle. |
| `reconcile --run <id> --apply --poll <duration> [--max-cycles <n>]` | Runs explicit bounded polling. The interval is 100 ms through 1 hour; cycles default to 60 and must be 1 through 10000. |
| `status --run <id> [--state <dir>]` | Reports token use, active claims, queued/running stages, child IDs, blockers, critical ready work, and merge candidates without mutation. |
| `drain --run <id> [--state <dir>] --apply` | Stops new Platoon admissions while continuing reconciliation of known children. |
| `resume --run <id> [--state <dir>] --apply` | Re-enables admission for a nonterminal drained run. |

The default state root is `.platoon`. Every mutation requires `--apply`.
Validation, planning, status, and non-applied start/reconcile never create it.

## Manifest

The supported API version is `platoon.dev/v1alpha1`.

- [JSON Schema](schema/platoon.schema.json)
- [Mission declaration schema](schema/mission.schema.json)
- [Manifest reference](docs/manifest.md)
- [Synthetic example](examples/platoon.yaml)
- [Synthetic typed preview](examples/platoon-typed.yaml)

Each stage declares one repository, one task, implementation or read-only
review mode, an agent harness, model/risk classes, dependencies, path claims,
semantic claims, and acceptance commands. `adoptFleet` explicitly binds a
pre-existing fleet to a stage.

`spec.missionFormat` is optional. Omission or `reference` preserves the original
path-only behavior: Platoon does not read or sniff the mission file, even when
its contents look typed. An explicitly empty format is invalid.
`declaration-v1alpha1` explicitly opts into a strict,
stable-read `platoon.dev/mission/v1alpha1` declaration. `validate`, `plan`, and
non-applied `start` compile the same deterministic class, output, and sufficiency
preview. Blocking unknowns and contradictions report `ready: false`; malformed,
missing, changed, symlinked, or oversized declarations fail closed. Preview
diagnostics contain only bounded mode/schema/reason identifiers, never mission
bodies or resolved private paths. Typed applied start requires
`--source-bundle`, recompiles the packet, and requeries declared sources before
any state or effect. It returns `replan_required` when schema, revision, quality,
content identity, or freshness differs. Matching content observed later is
accepted because `observedAt` is provenance rather than content. This phase
validates only; command-driven typed packet and run publication remains
disabled. An internal Mission Control fixture seam exercises the next storage
substrate without invoking dagr, Sergeant, or another effect. It accepts only a
ready in-process compiled packet with verified observations and pinned
creation-disabled rollback evidence accepted by an injected trust verifier; it
is not a public creation API. The same fixture seam can publish an effect-disabled
successor only from the full current fence.

Ready declarations explicitly allow the required Dagr, Sergeant, and validation
effects within their class ceiling and provide exact actor/source authority
coverage. Survey additionally requires declared `read-source` or
`query-authority` permission for the exact caller and stage. Entry stops,
mutable sources without an observation bundle, missing orchestration effects,
and unattended requests are not ready. Source kinds bind
to closed source schemas; stop fields, typed predicate values, effect-specific
authority sources, and disposition owners must resolve without ambiguity.
String and boolean YAML types are explicit, Git authority uses full object IDs,
and read-only review stages cannot receive source-write, receiving-system, or
Sergeant lifecycle effects.

Source bundles contain only schema-validated typed observations, sorted source
identities, quality/revision/freshness metadata, and domain-separated canonical
SHA-256 identities. `contentDigest` excludes collection time;
`envelopeDigest` and bundle identity include it; `contentSetDigest` binds the
ordered content identities. Nulls, duplicate or undeclared sources, unknown
schemas or fields, future pre-start Dagr run/stage identities, secret-like or
raw-body fields, absolute paths, stale evidence, and observations over 1 MiB are
rejected. td and Sergeant observations use only the negotiated
`SergeantMissionSource` seam and currently report `unsupported`; Platoon never
traverses td or fleet state to emulate it. Missing or incompatible Dagr
executables produce a valid `unavailable` observation rather than an inferred
capability or a malformed bundle.

The [Atlas-inspired Mission Control PRD](docs/atlas-mission-control-prd.md)
defines the later sourced cross-repository
interpretation, handoffs, drift verdicts, trajectory, Mission Records, and a
bounded whole-observation Sergeant status-query seam behind one interface for
survey, start, reconcile, drain, resume, and status. It preserves Dagr
readiness, Sergeant worker lifecycle, Git/td authority, explicit apply, and
current adapter compatibility.
It also specifies closed source/class/output/non-production-effect/stop
contracts, content-stable source surveys, target-bound environment proofs,
crash-safe effect attempts with pre-invocation cancellation, deny-by-default
sandboxed validation/replay policies, complete authority coverage, terminal
child convergence, and a tested
compatibility artifact that keeps typed runs operable across rollback. The
document remains a design proposal beyond the declaration-preview slice above.

Implementation and review pools default to six and two tokens. A repository
defaults to one writer. Raising `maxWriters` allows same-repository concurrency
only when every active path and semantic write claim is disjoint. Repository
`branch` is an issue-branch prefix; dispatch derives `<branch>-<stage-id>` so
concurrent stages never reuse one Git branch.

Path claims are normalized literal relative subtree prefixes; glob characters
are rejected. Conflict detection is case-folded for portable safety, while
changed-path coverage requires exact case so a distinct Linux path cannot be
authorized accidentally. Exact and ancestor/descendant overlap conflicts.
Changed current or historical symlinks always fail the claim check. Root-level
Sergeant transport files are excluded because Sergeant, not a Specialist, owns
them.

Opaque project/task/fleet/model/risk/correlation IDs are 1-128 ASCII characters
from `[A-Za-z0-9._-]`; exact `.` and every `..` substring are rejected at the
manifest, command, fleet, and durable-state seams. The published schema is
compiled in tests and shares negative lexical fixtures with runtime validation.

Semantic claims are normalized to lowercase hyphenated names. Migration, state
machine, authorization, identity, recovery, purge, release, destructive, and
repository-integration claims are repository-exclusive, including qualified
names such as `authorization-policy`.

Commands are executable-plus-argument arrays, never interpolated shell strings.
Direct shells, control characters, and obvious inline secrets are rejected.
Credentials remain in the operator-controlled environment and are never copied
into Platoon state.

## Lifecycle

An applied start reads and validates one immutable manifest byte snapshot,
publishes the canonical intent and generated workflow into the restrictive run
directory, then publishes `state.json` last. A crash before state publication
leaves no authoritative run. Dagr workflows contain no hooks: readiness can
enqueue work, but only the fenced admission transaction can dispatch Sergeant.
Workflow and stage IDs are persisted before run start. If start output is lost,
read-only dagr inspection recovers exactly one full run ID, starts only after
proven absence, and blocks on multiple matches.

Admission writes a generation-bound `prepared` reservation, then durably moves
it to `dispatching` immediately before the command. Recovery may dispatch a
still-prepared reservation exactly once. A dispatching reservation requires
correlation evidence; proven absence permits one bounded retry, while exhaustion
or multiple matches blocks. One restrictive user-global lock independent of
state and fleet roots serializes every local Sergeant dispatch sharing the
user's credential helper. The stable per-UID authority is derived from the
operating-system account database, never `HOME`, `TMPDIR`, or XDG variables;
all lock waits honor the applied command context. A verified receipt commits
only matching one-repository fleet evidence with the exact callback correlation.

Reconciliation reads Sergeant-owned durable state without modifying it. A token
is released only after verified `done` plus a non-empty result, or a verified
`failed: <reason>`. Successful child work is diffed from Sergeant's recorded
dispatch base. Out-of-claim work becomes `out_of_claim`, emits only bounded
path diagnostics, and never advances dagr.

In-claim success pins the child worktree, dispatch base, result digest, fleet ID,
exact `.git` pointer, and physical Git directory in a durable repository queue.
Diff commands use that pinned directory, a private temporary index, no
replacement objects, and a filtered `GIT_*` environment, so child index flags or
redirected metadata cannot hide changes. A user-global registry blocks
overlapping claims across arbitrary state roots, and a global integration lock
serializes candidate commands across those roots. At most one candidate is
`integrating`. The child must contain the current repository base; repository
and stage acceptance commands then run in its worktree. Terminal identity and
all claims are checked both before and after those commands. Evidence drift or a
violation blocks the candidate. A base change requeues it. Passing candidates
become merge-ready and advance dagr with compare-before-set recovery. Platoon
does not merge or push.

Run states are `initialized`, `active`, `drained`, `reconcile_required`,
`completed`, and `failed`. Terminal runs cannot resume. See
[architecture](docs/architecture.md) and [operations](docs/operations.md) for
the complete stage, reservation, lease, and recovery model.

Typed fixture state is separate from legacy `platoon.state/v1alpha1`. Packet,
observation, projection-revision-zero, event, and transition objects are
canonical, digest-addressed, restrictive files under one typed run directory;
semantically equivalent but byte-different JSON is rejected. They are
non-authoritative until `current.json` atomically names a fully verified
transition. The pointer retains current and previous transition references and
fences mutation by repair epoch, generation, and transition digest. Partial
objects, exact unreferenced forks, and commits left before a pointer replacement
are ignored; exact-byte retries re-sync and converge.
Projection entries are derived from packet source descriptors, and loading
recomputes the exact source-bundle identity from persisted observation
envelopes before accepting packet, projection, or event bindings. Each entry
keeps its declaration ID separately from its closed provenance label: receiving
and environment-classification sources map to `receiving_system`, while
validation and Platoon-policy sources map to `platoon`. Events are capped at 64
KiB and complete observation objects at 1 MiB. Generated event times must be
canonical, representable RFC 3339 timestamps before any object write.

If a pointer names an invalid current transition but a verified previous
transition, recovery first publishes a no-effect quarantine at `N+1` and
`E+1`, then a no-effect `reconcile_required` successor at `N+2`. It never adopts
the invalid state, skips a generation, or chooses an orphan descendant. Retrying
the original recovery fence resumes its own quarantine or returns its completed
repair. Counter overflow, nonadjacent recovery, and an invalid generated
candidate are rejected before object or pointer publication. Publication is
refused before creating run files unless a trusted verifier recognizes rollback
metadata that pins a creation-disabled artifact and its synthetic
compatibility-fixture digest for the typed schema.

## Safety

- One local Commander lease holds a generation and an OS file lock. Recovery
  requires expiry, the same host, and proof that the recorded PID is absent.
- Every authoritative write uses same-directory atomic replacement, file and
  directory sync, `0600` files, and `0700` directories.
- Prior generations fail closed. Live, foreign-host, or ambiguous leases cannot
  be stolen.
- Adapter execution has timeouts and separate bounded stdout/stderr. Failed
  command output is not returned in errors or persisted. Commands run in their
  own process group; timeout kills descendants and bounds pipe waiting.
- Fleet files are bounded, stable-read, regular non-symlinks with strict binding
  to exactly one repository plus project, task, stage, issue branch, callback
  correlation, and intent revision.
- Adoption consumes the same tokens and claims as new work. Conflicting or
  over-capacity adopted fleets block further admission without being stopped.
- Drain is Platoon-local; it does not signal, terminate, or rewrite child fleets.

See the [threat model](docs/threat-model.md) and
[adapter contract](docs/adapters.md).

## Validation

```sh
make test
make race
make vet
make demo
```

`make demo` runs a deterministic fake dagr/Sergeant lifecycle through start,
dispatch, terminal evidence, claim verification, merge queue, dependency unlock,
and completion. GitHub Actions runs tests on macOS and Linux, the race detector,
vet, and the fake demonstration.

CI compares each push or pull request with its proper Git base, then checks every
commit in that range. Operator-visible source, schema, examples, documentation,
installation, or lifecycle changes fail unless that same commit changes the
`README.md` blob; a later documentation commit cannot satisfy an earlier change.

## Limitations

- The first release provides one active Commander per local state root, not
  distributed consensus across hosts.
- The per-user cross-root claim registry is conservative after ambiguous owner
  loss: stale active claims continue blocking rather than being discarded.
- Dagr and Sergeant expose human-oriented command receipts and file-state
  contracts rather than versioned JSON APIs. Platoon parses only the documented
  compatibility profile. Dagr crash recovery additionally performs one bounded
  read-only query through configured `sqlite3`; schema drift blocks.
- `sgt-watch`, `sgt-wake`, and `sgt-drain` remain Sergeant-owned operational
  controls. Their executables are declared for compatibility visibility, but
  Platoon does not invoke them automatically or assume ownership of wake/drain
  lifecycle.
- A `dispatching` reservation with no callback evidence receives at most one
  absence-proven retry; exhaustion or multiple matches requires operator
  reconciliation. A still-`prepared` reservation is safe to dispatch.
- YAML aliases, merge keys, shell strings, glob claims, Windows, auto-merge, and
  auto-push are unsupported.
