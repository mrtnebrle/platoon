# Atlas-Inspired Mission Control PRD

Status: proposed  
PRD version: 1.0  
Source fixed point: Platoon `1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4`  
Last source survey: 2026-08-07

## Summary

Platoon should evolve from a stage dispatcher with a required but inert mission
reference into a mission compiler and interpreter. A versioned mission
declaration will state objective, class, effects, stops, authority assumptions,
unknowns, contradictions, and required outputs. Platoon will compile it with the
existing manifest into an immutable packet and run identity, maintain a
revisioned projection of sourced references, interpret exact Sergeant
observations without rewriting them, record an append-only sanitized
trajectory, and derive a terminal Mission Record.

The public product boundary is one deep Platoon mission interface with compile,
apply, reconcile, status, drain, resurvey, handoff, route, resume, and record
operations. Existing commands remain compatibility entry points into that
interface. Runtime facts enter through one version-negotiated Sergeant
status-query adapter. That adapter
depends on the generic project/td/fleet/coordinator resolution proposed by
[Sergeant issue #207][sergeant-207]; Platoon will not traverse td, infer worker
liveness, discover coordinators, manage callback queues, or route harness
sessions itself.

The Garrison Field Atlas is an unimplemented teaching design, not operational
evidence. This PRD adopts selected ideas as **Atlas design inspiration**:
immutable declarations, sourced projections, explicit unknowns, stage handoffs,
trajectory, mission records, drift verdicts, and unattended qualification. It
does not adopt the Atlas Work Filesystem or create a new authority system.

## Problem Statement

Platoon currently coordinates work that has already been decomposed into
manifest stages. It can validate admission policy, preserve an immutable
manifest snapshot, dispatch or adopt one Sergeant fleet per repository/task
stage, reconcile child evidence, enforce claims, and report local durable run
state. It cannot answer the higher-level questions an operator needs:

- What mission is this run intended to accomplish, and what counts as done?
- Which assertions came from Git, td, Sergeant, Dagr, an author, or Platoon's
  own interpretation?
- Which unknowns block entry, and which may proceed only if routed as findings?
- What must one stage produce before another may consume it?
- Did the observed worker state change, or did Platoon's interpretation change?
- Has source drift made the mission unsafe, merely stale, or still valid?
- What safe state and exact route allow another actor to continue?
- What did the mission change in the product and in the surrounding world?

The required `spec.mission` field appears in the manifest model and is checked
only as a safe relative path. Applied start snapshots `spec.intent`, but neither
resolves nor reads `spec.mission`. The resulting run stores the manifest and
intent provenance but no mission declaration, source projection, handoff
contract, trajectory, or Mission Record. Status reports local stage state and
fleet IDs but does not preserve an exact generic Sergeant observation beside a
separate Platoon interpretation.

External assistants cannot safely fill this gap. Sergeant's current low-level
commands do not resolve a project or td epic to coordinator-aware fleet
evidence. Reimplementing that traversal in Platoon would create conflicting
namespace and liveness rules. Treating `busy:null` as idle, Git metadata as a
coordinator response, or a transcript identifier as live ownership would be an
unsafe false conclusion. [Sergeant #207][sergeant-207] defines the required
bounded query at the owning layer.

## Solution

An operator supplies the existing `platoon.dev/v1alpha1` manifest, explicitly
sets `spec.missionFormat: declaration-v1alpha1`, and points `spec.mission` to a
`platoon.dev/mission/v1alpha1` declaration. Omitting the new format preserves
current reference-only behavior.

1. `validate` checks both documents, references, authority assumptions, source
   labels, handoff compatibility, and lifecycle completeness without side
   effects.
2. `plan` compiles a deterministic preview containing readiness, blocking
   unknowns, contradictions, admission decisions, and the source revisions it
   would bind.
3. Applied `start` snapshots the declaration and existing intent, publishes an
   immutable packet and run identity, then uses the existing fenced admission
   path.
4. `status` returns one bounded document containing exact observed state from
   Dagr, Sergeant, Git, and td references alongside explicitly labeled Platoon
   interpretation, projection revision, continuation, and recent meaningful
   events.
5. `resurvey` compares source revisions and emits a conservative drift verdict.
   It never mutates the packet or child state.
6. `route` publishes a mission-level finding reference through the authority
   that owns disposition. It does not create or close repository review
   findings itself.
7. Applied `resume` requires the expected run generation, projection revision,
   satisfied required inputs, and authoritative evidence for the selected
   continuation.
8. A terminal run derives and validates one Mission Record before the run is
   considered complete.

Every mutating operation remains explicit with `--apply`. No internal daemon is
added. An external scheduler may invoke bounded operations after inspecting
machine-readable status.

## User Stories

### Operators

1. As an operator, I want to preview mission compilation without creating local
   state or querying mutable runtime systems, so that inspection remains safe.
2. As an operator, I want to see why entry is ready, not ready, or indeterminate
   before applying a run, so that I do not begin from an unsafe world.
3. As an operator, I want to distinguish an exact Sergeant observation from
   Platoon's interpretation, so that I can audit every conclusion.
4. As an operator, I want one bounded view of blocking unknowns, contradictions,
   required inputs, continuation, and escalation route, so that I can act
   without reconstructing state across tools.
5. As an operator, I want to drain Platoon admission without signaling Sergeant
   workers, so that ownership boundaries survive an operational pause.
6. As an operator, I want resume to require matching generation and projection
   evidence, so that stale instructions cannot restart work.
7. As an operator, I want the declaration, intent, packet, projection, and
   adapter versions in every result, so that it is reproducible.
8. As an operator, I want terminal outcomes to distinguish completion, safe
   failure, supersession, and recovery need, so that each gets the right action.

### Mission Authors

9. As a mission author, I want a mission class with validated completion
   semantics, so that prose does not silently define lifecycle behavior.
10. As a mission author, I want to name allowed and prohibited effects, stops,
    authority assumptions, and required outputs, so that actors receive an
    explicit boundary.
11. As a mission author, I want to declare a sourced unknown as blocking or
    non-blocking, so that I never have to invent a missing value.
12. As a mission author, I want to declare contradictory source claims and their
    disposition authority, so that compilation cannot choose a convenient
    winner.
13. As a mission author, I want a versioned producer/consumer handoff with a
    schema, evidence, and acceptance rule, so that cross-stage dependencies are
    enforceable.
14. As a mission author, I want to request unattended qualification separately
    from receiving it, so that assertion cannot bypass safety evidence.

### Implementation And Review Actors

15. As an implementation actor, I want a task and purpose derived from one
    immutable packet, so that I cannot accidentally reinterpret parent intent.
16. As an implementation actor, I want the exact fixed point, claims, effects,
    stops, outputs, and handoffs for my stage, so that my working boundary is
    inspectable.
17. As a producer, I want handoff publication blocked until contract and evidence
    validate at the pinned revision, so that consumers never receive an
    unverified offer.
18. As a consumer, I want admission blocked on absent, superseded, incompatible,
    or unverified handoffs, so that I do not work from stale inputs.
19. As a reviewer, I want read-only evidence and finding output, so that review
    does not silently absorb remediation ownership.
20. As a validation actor, I want to classify failure cause without exposing raw
    child output, so that recovery can target actor/compliance,
    specification/procedure, or environment/mission state.

### External Assistants

21. As an external assistant, I want bounded JSON by project and optional td
    scope, so that I do not need fleet directory names.
22. As an external assistant, I want unresolved scope, `busy:null`, and missing
    coordinator evidence preserved exactly, so that I cannot report false idle
    or successful contact.
23. As an external assistant, I want source labels, revisions, observation
    times, and interpretation rules, so that my answer can cite its basis.
24. As an external assistant, I want status to exclude prompts, secrets, raw
    transcripts, raw child output, and private paths, so that a query is safe to
    expose.

### Recovery Operators

25. As a recovery operator, I want to replay the immutable packet and append-only
    trajectory, so that I can identify the last safe continuation point.
26. As a recovery operator, I want one explicit drift verdict, so that I know
    whether to continue, revalidate the projection, reassemble, exclude, or
    supersede.
27. As a recovery operator, I want stale generation, superseded run, and changed
    required-input resumes rejected, so that recovery cannot replay old intent.
28. As a recovery operator, I want partial projection and event publication to
    preserve the last authoritative pointer, so that orphan files cannot become
    state.

### Authoritative Stewards

29. As a Git steward, I want Platoon to retain only pinned references and verified
    summaries, so that Git remains authority over commits, branches, diffs, and
    merges.
30. As a td steward, I want Platoon to retain only work references and mission
    interpretation, so that td remains authority over graph and disposition.
31. As a Sergeant steward, I want Platoon to query through one adapter without
    duplicating lifecycle logic, so that Sergeant retains project/fleet
    resolution, workers, coordinators, callbacks, wake/resume/drain, reviews,
    recovery, and cleanup.
32. As a Dagr steward, I want Platoon to consume readiness through the supported
    adapter, so that Dagr remains authority over acyclic readiness and terminal
    transitions.
33. As a receiving-system steward, I want every effect independently authorized,
    so that a Platoon declaration cannot grant permission.
34. As a finding steward, I want to own acceptance, rejection, supersession, and
    disposition, so that a Platoon observation never becomes canonical truth by
    itself.

## Implementation Decisions

Except for the explicitly cited **Current Behavior And Gaps** subsection, every
product mechanism proposed in these implementation decisions is **Atlas design
inspiration adapted for Platoon**, not implemented or proven Atlas behavior.
Platoon's existing public source and Sergeant's owning contracts remain the
evidence for current behavior and authority.

### Goals

1. Make the mission declaration an enforced, immutable input to applied work.
2. Compile cross-repository purpose and contracts without taking authority from
   Git, td, Dagr, Sergeant, or receiving systems.
3. Preserve exact source observations and provenance separately from Platoon
   interpretations.
4. Make stops, unknowns, contradictions, handoffs, drift, continuation, and
   findings deterministic enough for tracer-bullet implementation.
5. Give operators and external assistants one bounded, privacy-safe status
   surface.
6. Preserve current fail-closed admission, fencing, claims, explicit apply, and
   child ownership.
7. Qualify only demonstrably bounded missions for unattended invocation.
8. Land the evolution in independently green, rollback-safe vertical slices.

### Terminology

| Term | Definition |
|---|---|
| Mission declaration | Versioned authored statement of purpose, boundaries, unknowns, contradictions, contracts, and outputs. |
| Packet | Immutable compiled snapshot of declaration, intent revision, manifest snapshot, selected contracts, and source references. Inspired by the Atlas WorkPacket; it is not a new authority. |
| Run | Immutable identity binding one packet digest to an execution history. Run state evolves through events; packet identity does not. |
| Projection | Reconstructable, revisioned mapping from packet roles to source references and verified summaries. It references evidence rather than copying it. |
| Observation | Exact bounded data returned by an owning source adapter, retained without semantic rewriting. |
| Interpretation | Platoon's labeled mission-level conclusion derived from observations and declaration rules. |
| Source label | Enum describing provenance: `declaration`, `intent`, `platoon`, `dagr`, `sergeant`, `git`, `td`, or `receiving_system`. |
| Unknown | Precisely named missing fact with source attempts, blocking flag, owner route, and disposition state. |
| Contradiction | Two or more sourced claims that cannot all govern the same decision. |
| Stop | Testable condition that prevents a named effect or stage transition. |
| Handoff | Versioned producer output and evidence accepted as a consumer's required input. |
| Meaningful event | Sanitized state transition or evidence change that can alter action, interpretation, continuation, or outcome. |
| Continuation | Safe state, next action, route, required inputs, and resume mechanism for nonterminal work. |
| Trajectory | Append-only sanitized sequence of meaningful events. |
| Mission Record | Derived terminal account of outcome, product delta, world delta, actors, evidence, continuation when required, and finding references. |
| Drift | A relevant source revision or authoritative fact no longer matching the projection that referenced it. |
| Resurvey | Bounded comparison of recorded source references against current source evidence. |
| Exclusion | Admission hold that prevents affected work from proceeding while preserving unrelated work and existing child ownership. |
| Finding | Mission-level unresolved observation routed to an authority for disposition. |

### Authority And Ownership

| Concern | Authority / owner | Platoon responsibility | Prohibited Platoon behavior |
|---|---|---|---|
| Mission purpose and boundaries | Reviewed mission declaration and canonical intent | Compile, validate, snapshot, interpret | Invent missing purpose or silently alter packet |
| Cross-repository interpretation | Platoon | Evaluate mission sufficiency, handoffs, drift, outcomes | Claim source facts it did not observe |
| Acyclic readiness | Dagr | Read and acknowledge through documented adapter | Reimplement readiness or bypass Dagr |
| Admission, capacity, claims, fencing | Platoon | Preserve current deterministic policies and leases | Dispatch without explicit apply and reservation |
| Project and td scope resolution | Sergeant plus td | Query through versioned Sergeant adapter | Traverse td or infer fleet mapping |
| Worker and coordinator activity | Sergeant | Preserve exact observation and interpret conservatively | Infer liveness from process, pane, transcript, or Git metadata |
| Callbacks and wake/resume/drain | Sergeant | Reference state; request only through supported adapter operation | Manage callback queues or write child wake/status state |
| Worker recovery, reviews, cleanup | Sergeant | Surface references and mission implications | Recover, review, or clean a child directly |
| Repository source and merge state | Git and repository steward | Pin revisions, inspect bounded diffs, enforce claims | Merge, push, rewrite, or claim repository authorization |
| Work lifecycle and finding disposition | td / configured tracker steward | Reference work and route a proposed mission finding | Create competing disposition or close findings unilaterally |
| Effect authorization | Receiving system | Declare requested effect and record response evidence | Treat declaration or exposure as authorization |
| Mission trajectory and record | Platoon local durable state | Append sanitized events and derive record | Persist raw child output, prompts, credentials, or private paths |
| Scheduling | External scheduler / operator | Expose bounded idempotent operations and wake hints | Run an internal worker daemon |

### Current Behavior And Gaps

The following statements are implemented evidence at the pinned Platoon fixed
point, not Atlas proposals.

1. The published manifest schema requires `spec.mission`
   ([schema][platoon-manifest-schema]) and runtime validates it as a safe
   relative reference
   ([validation][platoon-manifest-validation]).
2. Applied start accepts a manifest snapshot and `IntentPath`, reads and
   snapshots only the intent, and initializes run state without reading the
   mission reference ([Commander start][platoon-start]). The mission reference
   is therefore currently inert after syntax validation.
3. A run persists manifest and intent digests, Dagr identity, stage state,
   reservations, merge queues, and bounded blockers ([state model][platoon-state]).
4. Dagr owns readiness while Sergeant owns child worktree and lifecycle state;
   Platoon stores references and verified summaries
   ([architecture ownership][platoon-architecture]).
5. The Sergeant adapter dispatches one project/task/repository/stage and verifies
   a bounded receipt ([dispatch adapter][platoon-dispatch]).
6. Fleet observation uses documented durable files, recognizes distinct
   nonterminal states, and retains only a result digest
   ([adapter contract][platoon-adapters]).
7. Status is read-only and reports local tokens, claims, queue, stages, fleet
   IDs, blockers, and merge candidates ([status projection][platoon-status]).
8. Platoon intentionally does not invoke Sergeant watch, wake, or drain
   automatically ([current limitations][platoon-limitations]).

Gaps:

- No declaration schema, mission classes, sufficiency gate, source catalog, or
  authority assumptions.
- No mission snapshot or packet digest at start.
- No distinction between source observation and Platoon interpretation.
- No generic project/td/fleet/coordinator query.
- No handoff contract beyond Dagr dependency and terminal stage state.
- No source revision projection, context expansion, or drift verdict.
- No append-only trajectory, continuation contract, or Mission Record.
- No mission-level finding route.
- No unattended qualification or external scheduler contract.
- No version negotiation beyond strict manifest and local state versions.

### Product Contract

#### One Deep Platoon Mission Interface

The implementation SHALL expose one internal application boundary, here named
`MissionControl`, and derive CLI JSON from its result objects. The name is
illustrative, not a required Go identifier.

| Operation | Mutates | Input | Result |
|---|---:|---|---|
| `Compile` | no | manifest bytes, declaration bytes, intent bytes, source descriptors | deterministic packet preview, readiness, diagnostics |
| `Apply` | yes | compiled digests, expected source revisions | immutable initial packet/run publication and start admission result |
| `Reconcile` | yes | run ID, expected generation, bounded refresh policy | one existing bounded reconciliation cycle plus mission transitions/handoff offers |
| `Status` | no | run ID, optional refresh policy | exact observations plus interpretation and continuation |
| `Drain` | yes | run ID, expected generation | Platoon-local admission drain without signaling Dagr or Sergeant |
| `Resurvey` | append-only | run ID, expected generation and projection revision | drift evidence and one verdict |
| `Handoff` | append-only | contract ID, action, expected run/projection generation | verified offer, withdrawal, acceptance, or consumption result |
| `Route` | append-only/external request | finding draft, owning route, expected generation | external finding reference or explicit refusal |
| `Resume` | yes | continuation ID, required input references, expected generations | accepted transition or fail-closed reason |
| `Record` | append-only | `record_required` run ID, pending outcome, expected event head | validated Mission Record and terminal transition |

Existing `validate`, `plan`, `start`, `reconcile`, `status`, `drain`, and `resume`
commands SHALL become compatibility entry points over this boundary. The PRD
does not require renaming them. Every mutating CLI path remains `--apply` only.

The deep boundary owns mission compilation, cross-repository interpretation,
admission, claims, handoffs, drift verdicts, trajectory, and Mission Records.
It delegates all lower-level source resolution and effects to adapters.

#### One Sergeant Adapter Seam

Mission Control SHALL consume one version-negotiated `SergeantMissionSource`
adapter. Existing dispatch and file readers may implement this seam internally;
the product contract does not expose fleet-root traversal to Mission Control.
The seam has a read-only `Query` operation returning one Sergeant observation
and a bounded `Request` operation returning one Sergeant receipt.

`Query` is read-only. It accepts a registered project and optional td scope. Its
result includes resolution namespace, matched td/fleet references, worker state
totals, activity evidence quality, coordinator ownership evidence, optional
correlated response, source version, observation time, and truncation metadata.

`Request` is limited to operations Sergeant explicitly supports, such as a
correlated coordinator request or finding handoff. Every request names the
receiving authority source, requested effect, expected external revision,
correlation, and idempotency key. Its receipt is exactly `accepted`, `refused`,
or `unknown` and carries the authority decision reference/revision and observed
external revision when available. Refusal is not a transport failure; timeout,
lost receipt, stale authority, or revision mismatch is `unknown` and blocks
retry until correlated reconciliation. The adapter does not expose arbitrary
commands, session IDs, prompt text, or child-state writes. Wake, resume, drain,
recovery, reviews, and cleanup remain Sergeant-owned even if a future adapter
supports requesting them.

Until [Sergeant #207][sergeant-207] ships a supported machine-readable contract,
the new adapter reports `unsupported`. Existing dispatch and current durable
file adapters continue unchanged. Platoon MUST NOT emulate #207 by traversing td
or coordinator state.

#### Effect Authorization Gate

One gate applies to every typed-run effecting adapter invocation, whether
invoked manually, by initial apply, by reconciliation, or from an unattended
scheduler. This includes Dagr workflow load/start and terminal acknowledgment,
Sergeant dispatch and `Request`, validation/integration command execution, and
any receiving-system operation. Read-only Dagr/Sergeant/Git queries do not
require effect authorization but remain bounded and source-labeled. Legacy
reference-mode runs preserve their current adapter behavior.

Under the held run generation fence and immediately before each effecting
adapter invocation, Mission Control maps the operation to its declared effect
and must prove all of the following from the current packet/projection and exact
source evidence:

1. The requested effect is explicitly allowed and not prohibited.
2. No applicable stop, unresolved blocking unknown, contradiction, exclusion,
   or drift verdict prevents the effect.
3. The operation belongs to the current stage contract or unconsumed
   continuation and its expected packet, projection, and run generations match.
4. The receiving authority source and revision match the request, and current
   authority evidence permits this caller to attempt the operation.
5. Correlation and idempotency identities are new or reconcile to the exact same
   request.
6. An unattended request additionally carries current `qualified` evidence.

Failure returns a bounded refusal before that adapter invocation and appends no
effect event. A typed run whose required Dagr or Sergeant orchestration effect
is absent from the allowed list is not ready to start. Passing this gate does
not grant authorization: the receiving system independently accepts or refuses,
and Platoon recognizes acceptance only from its revisioned authority decision
receipt. If authority changes after the gate but before receipt, the revision
mismatch is `unknown` and enters operation-specific reconciliation rather than
accepted state.

#### Effect Attempt Journal

Every typed external effecting invocation uses one generalized durable attempt
contract after authorization and before the adapter call. Existing Sergeant
dispatch reservations and Dagr transition journals may implement this contract;
Mission Control must not create a second competing journal.

The held generation first publishes and syncs `prepared` with attempt ID,
effect/adapter operation, canonical request digest, prior run/stage state,
packet/projection/run generations, authority source/revision, correlation,
idempotency key, and preparation time. No invocation has occurred while
`prepared`. Immediately before the call, the same fence publishes and syncs
`invoking`; only then may the adapter run. A bounded receipt is validated and
the joint transition atomically stores `committed` or `refused` with its digest
and operation result.

A crash in `prepared` may invoke once only after all recorded generations and
authorization evidence still match. A crash or transport loss in `invoking` is
always uncertain: recovery queries the owning adapter by correlation and
idempotency key before retry. Exact accepted/refused/absence evidence converges
the attempt; missing, stale, multiple, or conflicting evidence remains
`reconcile_required`. An adapter operation that cannot provide stable
correlation, durable deduplication or bounded absence proof is unsupported for a
typed external effect. A `replay-safe` command policy supplies its local
process/repository proof before rerun; a `single-attempt` command never takes a
retry transition.

V1alpha1 permits exactly two invocation ordinals for a `replay-safe` command and
one for `single-attempt`. After ordinal one is uncertain and the replay proof
passes, the same root attempt ID, idempotency key, canonical request digest, and
authority binding append `retry_prepared(ordinal=2)`, re-run the authorization
gate, then sync `retry_invoking(ordinal=2)` immediately before the call. The
ordinal is journal metadata and is excluded from the canonical request digest;
creating a new root attempt is forbidden. A second uncertain result has no retry
transition and remains `reconcile_required`. Attempt records contain no
credentials, environment, or raw output.

#### Mission Declaration

The declaration is strict YAML with unknown fields rejected. Its identity is
`platoon.dev/mission/v1alpha1`, kind `Mission`, and a safe mission name. Its
required specification fields are objective, class, allowed/prohibited effects,
stops, authority assumptions, unknowns, contradictions, required outputs,
unattended request, and a source catalog. Source entries require stable ID,
closed source kind, opaque public-safe locator, revision or observation policy,
mission role, and reason.

Required mission classes and completion rules are closed in v1alpha1. Effect
ceilings list what a class may explicitly request; they grant nothing by
default. Every typed declaration still names its exact allowed subset.

| Class | Required outputs for `completed` | Effect ceiling beyond read/query/orchestration/route | Permitted terminal outcomes |
|---|---|---|---|
| `discover` | `sourced-answers`, `unknown-dispositions`, `evidence-references`, `finding-references` | none | completed, failed_safely, superseded |
| `decide` | `decision`, `authority-decision-evidence`, `decision-rationale`, `downstream-effects` | none | completed, failed_safely, superseded |
| `change-substrate` | `product-delta`, `compatibility-evidence`, `migration-evidence`, `rollback-evidence`, `dependency-evidence`, `invalidation-evidence` | write-claimed-source | completed, failed_safely, superseded |
| `deliver` | `product-delta`, `acceptance-evidence` | write-claimed-source | completed, failed_safely, superseded |
| `validate` | `independent-validation`, `finding-references`; no remediation patch | none | completed, failed_safely, superseded |
| `operate` | `operation-receipt`, `resulting-state`, `audit-evidence` | receiving-system-operation, request-sergeant-lifecycle | completed, failed_safely, superseded |
| `recover` | `safe-state`, `cause-classification`, `recovery-evidence`, `recovery-disposition` | write-claimed-source, receiving-system-operation, request-sergeant-lifecycle | completed, failed_safely, superseded |
| `learn` | `world-delta`, `finding-references`, `capability-candidates`, `workflow-candidates` | none | completed, failed_safely, superseded |

Read/query effects are `read-source` and `query-authority`. Orchestration effects
available to every class, but only when explicitly allowed, are
`dagr-load-workflow`, `dagr-start-run`, `dagr-ack-stage`,
`sergeant-dispatch`, `sergeant-coordinator-request`, `publish-handoff`,
`route-finding`, and `run-validation`. The complete additional v1alpha1 effect
registry is `write-claimed-source`, `receiving-system-operation`, and
`request-sergeant-lifecycle`. Unknown effects fail validation. Merge, push,
identity switch, credential creation, production activation outside a named
receiving-system operation, child-state write, and direct session injection are
globally forbidden and cannot appear in `allowed`.

`effects.allowed` and `effects.prohibited` are unique effect-ID sets and must be
disjoint. `effects.stages` maps every manifest stage ID to a subset of top-level
allowed effects; an undeclared stage/effect pair is denied. `effects.callers`
maps each allowed effect to a nonempty set from `operator`, `platoon`,
`external-scheduler`, and `stage`; stage callers are further restricted by
`effects.stages`. An undeclared caller/effect pair is denied. The exhaustive
effecting operation map is:

| Adapter operation | Required effect | Authority evidence and accepted receipt |
|---|---|---|
| Dagr workflow load | `dagr-load-workflow` | Configured Dagr source/version; exact workflow receipt and post-query |
| Dagr run start | `dagr-start-run` | Configured Dagr source/version; one full run identity and post-query |
| Dagr step done/fail | `dagr-ack-stage` | Bound Dagr run/stage and expected state; post-query confirms transition |
| Sergeant dispatch | `sergeant-dispatch` plus stage work effects | Registered project/adapter version and stage/task binding; correlated fleet receipt/evidence |
| Sergeant coordinator request | `sergeant-coordinator-request` | Verified current coordinator owner/revision; correlated accepted/refused/unknown receipt |
| Sergeant lifecycle request | `request-sergeant-lifecycle` | Verified Sergeant owner/revision and supported action; correlated receipt; Sergeant decides |
| Mission finding route | `route-finding` | Configured disposition authority/revision; external finding reference/receipt |
| Handoff offer/withdraw/accept/consume | `publish-handoff` | Current packet/stage/source evidence and local joint transition commit; withdrawal additionally requires current unaccepted offer plus drift/invalidation evidence |
| Acceptance/integration command | `run-validation` | Pinned repository/worktree/head and command contract; bounded exit/evidence digest |
| Worker source change | `write-claimed-source` on its stage | Sergeant dispatch binding plus Platoon path/semantic claims; Platoon never performs the write |
| Receiving-system action | `receiving-system-operation` | Named source, action, owner and revision; revisioned authority decision receipt |

Typed mode also requires `spec.commandPolicies` to cover every repository
integration and stage acceptance command by deterministic reference
`repository:<id>:integration:<zero-based-index>` or
`stage:<id>:acceptance:<zero-based-index>`. Each policy has exactly one mode:
`replay-safe` or `single-attempt`. There is no default. `replay-safe` asserts the
command has no external effect and may rerun only after the recorded process
group is absent and pinned repository/worktree/head identity plus claimed diff
have been re-observed. `single-attempt` is never retried automatically after
`invoking`; uncertainty remains `reconcile_required` for explicit recovery or
safe failure. The canonical command request digest includes executable, args,
working identity, policy mode, and attempt ID. Legacy reference mode retains
current command behavior.

Each stop has stable ID; one typed predicate; scope; blocked transitions/effects;
and route. A predicate names one source ID, schema field path, operator from
`equals`, `not_equals`, `in`, `exists`, or `quality_is`, and a bounded typed
scalar/list value when required. Scope contains `entry: true` and/or explicit
stage/effect ID sets. Fields and value types must exist in the source adapter's
negotiated schema. Missing source, unknown field, type mismatch, stale value,
or inconclusive/unavailable quality evaluates conservatively as stop active.
Stops cannot execute code or contain expressions.

Each authority assumption has stable ID, authority source ID, governed effect
IDs, claim from `source-is-authoritative`, `actor-may-attempt`, or
`owner-may-disposition`, revision policy `exact` or `current-at-invocation`,
optional expected revision required by `exact`, and a route. An
`actor-may-attempt` claim additionally requires `actorRole` from `operator`,
`platoon`, `external-scheduler`, or `stage`; `stage` requires one manifest stage
ID. The invocation entry path supplies the actual role and must match exactly.
The source must be `verified` at compilation and again at invocation. An
assumption documents the expected boundary; it never grants authorization or
substitutes for the receiving authority's receipt.

Compilation forms every allowed invocation tuple `(mission-or-stage, effect,
callerRole)` from `effects.allowed`, `effects.stages`, and `effects.callers`.
Each tuple MUST match exactly one `actor-may-attempt` assumption; zero or
overlapping matches are invalid before packet/run publication. Every effecting
operation must also resolve exactly one `source-is-authoritative` assumption for
its receiving adapter, and each disposition route exactly one
`owner-may-disposition` assumption. Extra assumptions that match no tuple are
invalid. Invocation repeats the exact tuple/source/revision match under the run
fence.

Every required output has stable ID, `category`, producing stage, versioned
schema, evidence role IDs, and the terminal outcome it gates. The closed output
category registry is the union of the exact IDs in the class table:
`sourced-answers`, `unknown-dispositions`, `evidence-references`,
`finding-references`, `decision`, `authority-decision-evidence`,
`decision-rationale`, `downstream-effects`, `product-delta`,
`compatibility-evidence`, `migration-evidence`, `rollback-evidence`,
`dependency-evidence`, `invalidation-evidence`, `acceptance-evidence`,
`independent-validation`, `operation-receipt`, `resulting-state`,
`audit-evidence`, `safe-state`, `cause-classification`, `recovery-evidence`,
`recovery-disposition`, `world-delta`, `capability-candidates`, and
`workflow-candidates`. `recovery-disposition` must resolve to
`no-residual-work` or one continuation ID. Each category required by the
selected class must be covered exactly once. Extra outputs may use only registry
categories and require unique ID/schema; they do not satisfy another required
category.

Every unknown SHALL include an ID, exact question, blocking flag, attempted
source references, and route. Every contradiction SHALL identify two or more
source IDs, the decision it blocks, and its disposition authority. Compilation
must not select a winner. Missing required declaration fields become validation
errors, not inferred values. Missing required source facts become declared
unknowns only when the author supplied the exact question and route.

#### Packet, Run, And Projection Semantics

**Atlas design inspiration:** immutable WorkPacket and revisioned projection.
Platoon uses these concepts without materializing a Garrison filesystem.

Packet identity uses a versioned byte contract. Stable-read source bytes produce
`manifestSourceDigest`, `declarationSourceDigest`, and `intentRevision` over the
exact bytes. Strictly decoded manifest and declaration values are defaulted and
normalized by their schema versions, schema-declared sets sort by normalized
stable ID, and ordered arrays retain declaration order. The resulting envelope
is encoded with [RFC 8785 JSON Canonicalization Scheme][rfc-8785]; values outside
its JSON domain, nulls where the schema has no null, and unknown fields are
rejected. The intent body is not normalized; only its exact-byte digest and
media type enter the envelope.

The canonical `platoon.packet/v1alpha1` envelope contains its schema, normalized
manifest and declaration values, all three exact source digests, intent media
type, selected handoff contracts sorted by ID, source descriptors sorted by ID
including source-native revisions, and every nested schema version. Packet ID is
lowercase SHA-256 over the ASCII domain separator
`platoon.packet/v1alpha1` followed by one NUL byte and the canonical envelope
bytes. Golden fixtures SHALL prove that map-key input order and schema-set
order do not change identity, while any semantic value, exact intent byte,
source revision, handoff, or schema change does. Packet bytes are written before
authoritative run publication with the same restrictive, atomic durability
rules as current run files. A packet never changes after publication.

A run ID binds exactly one packet digest. Reassembly creates a new packet and a
new run linked by `supersedes`; it never swaps a packet under an existing run.
The run declaration, identity, and packet binding are immutable; derived current
state evolves only from validated append-only events. An amended projection does
not change run or packet identity.

Projection revision zero records the source references used at compilation.
Later revisions may add or update references only through a trajectory event.
Each projection entry contains:

- stable role and source label;
- opaque public-safe locator or owning adapter reference;
- source-native revision or observation time;
- reason the source is relevant;
- exposure mode (`bound`, `referenced`, or `reachable`);
- integrity digest for Platoon-owned snapshots only;
- previous projection revision and event ID.

The projection MUST reference every authority required by packet source roles,
stages, effects, handoffs, required outputs, or continuations. Sergeant, Git,
td, and receiving-system references are therefore conditional on packet scope,
not ceremonial universal fields. A required authority that is missing,
unsupported, or inconclusive blocks the operation it governs; an irrelevant
authority is omitted rather than invented. The projection MUST NOT copy raw
fleet files, work-item bodies, source trees, transcripts, or credentials into
Platoon state. A verified bounded summary may be stored only when its schema and
source reference are retained.

#### Handoff Contracts

Every cross-stage dependency that transfers more than terminal readiness SHALL
name a handoff contract.

The `platoon.dev/handoff/v1alpha1` contract requires a stable ID, producer,
consumer, output schema, required evidence, compatibility and freshness rules,
and explicit missing/incompatible behavior. The initial behaviors are
`block-consumer` for missing evidence and `reassemble` for incompatibility.
Contracts are authored in the typed declaration's `spec.handoffs` collection;
producer and consumer must name existing, distinct manifest stage IDs.
Same-repository handoffs are valid; a stage handing to itself is not. The output
locator must be a source role declared in the packet rather than a child path.

The initial ingress requires no child write to Platoon state. During an applied
reconciliation, Platoon observes a producer's verified merge-ready evidence,
resolves the contract's output role through Git or another owning source
adapter, and creates an immutable offer containing reference, schema version,
source revision, evidence references, and digest. The operator-authorized
reconciliation is the publication actor; Sergeant remains owner of child output
and lifecycle. The idempotency key is the digest of packet ID, contract ID,
producer stage/fleet binding, producer head, output digest, and evidence digests.
Repeated observation returns the same offer.

`Handoff` exposes the lifecycle inside the single Mission Control interface.
`offer` is accepted only from the verified reconciliation path; `withdraw`
requires drift or invalidation evidence and is forbidden after acceptance;
`accept` validates schema, packet, producer identity, evidence, compatibility,
freshness, and current projection; `consume` occurs compare-before-set inside
the consumer's existing fenced admission transaction. The consumer's accepted
offer revision is persisted before dispatch. Platoon validates shape and
binding but does not certify payload domain truth. Producer and consumer remain
separately owned; the contract belongs to the packet.

Authorized pre-acceptance withdrawal generation-fences an immutable
`withdrawn` state for that offer revision and publishes a trajectory event with
the drift/invalidation evidence. A withdrawn revision can never be accepted,
consumed, or restored. Fresh verified producer evidence may create the next
monotonic offer revision for the same contract; it references the withdrawn
revision and receives a new evidence-derived idempotency key. Re-offer never
edits or reuses the withdrawn object.

Contract revisions follow semantic compatibility rules. A compatible minor
revision may receive `revalidate_projection`; a changed required field,
completion rule, authority, or effect boundary requires `reassemble`.

#### Evidence Provenance

All status facts and Mission Record claims SHALL carry:

- `sourceLabel` from the closed enum;
- `sourceVersion` and source-native revision/observation time;
- `observedAt` for runtime evidence;
- `collectedBy` adapter name and version;
- `scope` and stable correlation where applicable;
- `quality`: `verified`, `inconclusive`, `unavailable`, or `unsupported`;
- bounded finding/evidence references, never raw private content.

Interpretations additionally carry `rule`, `ruleVersion`, and the observation
IDs used. `inconclusive` never becomes false, idle, absent, complete, or safe.

#### Exact Sergeant Observation And Platoon Interpretation

Status SHALL place unmodified typed Sergeant data under `observed.sergeant` and
Platoon conclusions under `interpreted.platoon`. For example, an exact Sergeant
observation with resolved td scope, `busy:null`, reason
`no_verified_active_witness`, and `coordinator.queried:false` remains intact.
The adjacent Platoon state is `indeterminate`, cites its interpretation rule and
observation ID, and supplies a continuation for authoritative worker evidence.

Platoon MUST preserve unknown/wrong namespace, `busy:null`, inconclusive
activity, missing coordinator, and no correlated response as distinct states.
Project configuration, Git state, transcript existence, and session identifiers
MUST NOT be labeled as coordinator contact.

#### Meaningful Events And Trajectory

The trajectory uses immutable, individually framed event objects with a
versioned envelope, monotonic sequence, previous-event digest, run generation,
projection revision, timestamp, event type, sanitized subject, and evidence
references. This is the append-only equivalent of JSON Lines while permitting a
crash-consistent pointer transaction.

One publication protocol jointly advances observations, trajectory, and
projection. A source query intended only for a read-only status response is
ephemeral and is not durable authority. Applied reconciliation that persists an
observation first creates and syncs an immutable bounded observation object with
its source schema, revision/time, quality, and digest. The writer then creates
and syncs any immutable projection object and event object; the event references
observation IDs rather than embedding their bodies.

The writer next creates and syncs an immutable transition commit naming every
observation digest, projection digest, event digest, previous event head,
previous projection revision, resulting state, previous authoritative transition
digest, previous run generation, and previous resulting-state digest. Genesis
uses explicit null predecessor fields. Each commit therefore forms one immutable
generation chain.

It finally atomically replaces and directory-syncs the authoritative run-state
pointer. The pointer carries current transition digest/generation and the exact
previous transition digest/generation; the commit supplies observation set,
event head, projection revision, and resulting state. There is no independently
writable observation, event-head, or projection pointer. Recovery ignores
unreferenced objects/commits and orphan forks. If current transition validation
fails, recovery may use only the pointer's named previous commit after verifying
its full chain/state digest, reports `reconcile_required`, and publishes a new
successor before any effect. It never chooses among orphan descendants or skips
an intermediate generation.

Meaningful event types include:

- packet/run published;
- entry admitted;
- observation changed quality or value;
- stage admitted, handed off, excluded, resumed, or terminal;
- blocking unknown or contradiction disposition changed;
- projection expanded or revalidated;
- drift verdict issued;
- finding routed or disposition reference updated;
- continuation created or consumed;
- outcome derived and Mission Record published.

Polls that produce no semantic change do not append events. Heartbeats,
transcript growth, process existence, or repeated identical snapshots are not
meaningful progress by themselves.

Each event is capped in size, excludes source bodies and raw command output, and
uses bounded diagnostics. Events cannot be edited or removed. A correction is a
new event that references the superseded event.

#### Continuation, Required Input, Route, And Resume

Every nonterminal outcome contains one continuation object:

- stable continuation ID and expected run generation;
- current safe state and prohibited next effects;
- exact next action;
- route owner expressed as an adapter-owned reference;
- required input IDs, schemas, and authoritative sources;
- resume operation and preconditions;
- optional external scheduler hint, deadline, and attempt bound;
- evidence IDs that caused the stop.

Required inputs are references, not response bodies. Platoon verifies source,
schema, revision, and binding before accepting them. It does not decide on
behalf of the route owner.

Resume is compare-before-set. It fails if generation, packet, projection,
continuation, required input, source authority, or drift verdict changed. Resume
never writes a Sergeant worker status. If worker action is needed, Platoon
requests it through Sergeant and records the receipt; Sergeant decides whether
the action is accepted.

The nonterminal mission-status outcomes are `waiting`, `needs_input`,
`approval_required`, `blocked`, `paused`, and `escalated`. They are current
interpretations recorded in trajectory events, not terminal Mission Records.
Each requires a continuation and maps to `active`, `drained`, `excluded`, or
`reconcile_required` according to its guard. A later meaningful event may change
the nonterminal outcome without rewriting history.

#### Mission Record

The Mission Record is derived from the immutable packet, validated trajectory,
terminal source evidence, and finding references. It is not a second mutable
log. Required fields are:

- schema, record ID, run ID, packet digest, projection revision, event head;
- declaration and intent revisions;
- actors and source-owned stage/fleet references;
- one terminal outcome: `completed`, `failed_safely`, or `superseded`;
- acceptance results with evidence references;
- product delta, present even when empty;
- world delta, present even when empty;
- cause classification when harmful or safely failed;
- continuation for `failed_safely` and successor/cancellation authority
  reference when superseded;
- finding references and their externally owned disposition state;
- start, last meaningful event, and record publication times.

Product delta describes changes to the requested product at pinned Git or
receiving-system revisions. World delta describes sourced knowledge about
topology, authority, procedure, capability, policy, or substrate that should
affect future mission compilation. Neither field may contain an unsupported
claim.

No typed mission is terminal until its Mission Record validates and is
published. Completion evidence, safe failure, or an authorized supersession
first enters `record_required` with one pending terminal outcome. Record
publication failure leaves that nonterminal state unchanged; it does not change
exact child, Sergeant, or Dagr state.

#### Drift And Resurvey Verdicts

Resurvey compares only declared references and configured bounded source
queries. It emits exactly one verdict per affected scope:

| Verdict | Meaning | Allowed action |
|---|---|---|
| `continue` | Relevant source is unchanged or change is proven compatible | Continue current packet |
| `revalidate_projection` | Referenced evidence changed without changing purpose, authority, effects, or completion | Publish projection revision after validation |
| `reassemble` | Context, handoff semantics, authority, effects, or completion materially changed | Exclude affected work and compile a successor packet/run |
| `exclude` | Safe decision cannot yet be made or required replacement evidence is unavailable | Hold affected admission; preserve children and unrelated work |
| `supersede` | Intent or mission is replaced, canceled, or no longer authoritative | Terminally supersede run; never resume it |

"Amend projection" is the application of `revalidate_projection`; packet bytes
remain unchanged. A run needing reassembly does not mutate into the successor.
Unresolved, stale, unsupported, or conflicting evidence defaults to `exclude`.

#### Unattended Qualification And Scheduler Seam

`unattended.requested: true` is eligible only when all checks pass:

1. Inputs and source queries are bounded.
2. Outputs and acceptance are machine-verifiable.
3. The happy-path conditions are stable across cited prior trajectory evidence.
4. Allowed effects are deterministic, idempotent or safely fenced, and low
   residual judgment.
5. Every exception maps to a stop, continuation, route, deadline, and bounded
   attempt policy.
6. No unresolved blocking unknown, contradiction, approval, or ambiguous
   authority exists.
7. Receiving systems still enforce authorization independently.
8. Recovery and rollback have been exercised at public seams.

Qualification records the rule version and evidence references. It expires when
a relevant declaration, workflow, source contract, authority, or effect changes.
Immediately before an unattended applied operation, Mission Control compares the
qualification ID, rule version, evidence revisions, packet, projection, run
generation, and scheduler hint in the same fenced transaction that admits the
effect. Concurrent drift expires qualification and excludes the operation; it
never races through on previously qualified evidence.

The external scheduler seam consists of read-only status plus idempotent,
bounded applied commands. Status may expose `schedulerHint` with operation,
not-before time, deadline, and generation. It contains no shell command, token,
prompt, or private path. Platoon does not run a background worker or own time.

#### Finding Routing

Platoon findings are mission-level observations: cross-repository contradiction,
handoff gap, mission declaration defect, drift impact, or missing authority.
They carry packet/run/stage references, source revisions, bounded evidence, a
suggested owning route, and desired disposition class.

Repository code-review findings continue through Sergeant's repository finding
router. td remains authority for task creation and disposition. Platoon submits
a finding request through the configured owning adapter and stores only the
returned external reference and observed disposition. It never creates a shadow
finding database or changes an external disposition.

Duplicate routing uses a stable digest over mission, kind, affected source, and
authority route. Ambiguous authority blocks routing and creates a continuation;
it does not broadcast to multiple systems.

### State Transitions

#### Mission Run

`compiled_not_ready` and `compiled_ready` are preview results, not durable run
states. Admission readiness is evaluated before packet/run publication or any
adapter effect. A not-ready applied request returns the same bounded non-durable
refusal as compile and creates no run, event, packet file, or cleanup obligation.
A ready applied request publishes `initializing`, then reaches `active` only
after current Dagr start recovery succeeds.

| From | Trigger | Guard / Dagr relationship | To | Terminal |
|---|---|---|---|---:|
| `initializing` | Verified Dagr run start | Exact workflow/run/stage identities | `active` | no |
| `initializing` | Dagr outcome uncertain | Current Dagr recovery evidence incomplete | `reconcile_required` | no |
| `active` or `reconcile_required` | Operator drains admission | Expected generation; retain exact prior state/blockers; do not signal Dagr/Sergeant | `drained` | no |
| `drained` | Operator repeats drain | Same run; no state change | `drained` | no |
| `active` | Drift cannot safely resolve | Affected admission stops; running child is not rewritten | `excluded` | no |
| any nonterminal allowed to request an external effect | External outcome uncertain | Owning adapter cannot prove accepted/refused/absent | `reconcile_required` with operation and prior state | no |
| `reconcile_required` | Correlation proves no effect | Exact source-specific absence proof; expected generation and prior state match | prior safe nonterminal state | no |
| `reconcile_required` | Correlation proves refused effect | Exact revisioned refusal receipt matches request | prior safe nonterminal state plus refusal event | no |
| `reconcile_required` | Correlation proves accepted effect | Exact authority revision and receipt/fleet binding determine one operation result | operation-specific success state | no |
| `reconcile_required` | Evidence proves unsafe/incompatible result | Exact correlation and current projection identify affected scope | `excluded` | no |
| `drained(prior=reconcile_required)` | Correlation proves no effect/refusal | Same recovery guards; admission hold remains | `drained(prior=<recovered prior state>)` | no |
| `drained(prior=reconcile_required)` | Correlation proves accepted/unsafe effect | Operation reducer selects one result | `drained(prior=<nonterminal result>)` or matching `record_required` | no |
| `drained(prior=reconcile_required)` | Recovery remains missing/stale/ambiguous | No valid destination | same drained state/blockers | no |
| `drained` | Operator resumes | Drain continuation and generations match | prior nonterminal state | no |
| `excluded` | Resurvey permits resume | Required inputs and projection generation match | `active` | no |
| `active` or `drained` | Mission acceptance verified | All required Dagr stages have compatible terminal evidence and handoffs | `record_required(completed)` | no |
| `active`, `drained`, or `excluded` | Mission cannot safely complete | Required Dagr/Sergeant failure evidence and cause are verified | `record_required(failed_safely)` | no |
| `initializing`, `active`, `drained`, `excluded`, or `reconcile_required` | Owning authority replaces/cancels intent | Superseding authority evidence is verified; children are not rewritten | `record_required(superseded)` | no |
| `record_required(outcome)` | Same terminal evidence repeats | Outcome and evidence digest exactly match | same `record_required(outcome)` | no |
| `record_required` | Record validates and publishes | Pending outcome, event head, acceptance/cause/continuation evidence match | matching terminal state | yes |

`record_required` is the only route to `completed`, `failed_safely`, or
`superseded`. Dagr stage terminality is source evidence, not mission terminality.
A direct terminal write, terminal resume, or pending-outcome change is invalid.
Drain stores the exact pre-drain state and blocker set. Guarded resume restores
`reconcile_required` and its evidence unchanged when that was the prior state;
drain never clears a reconciliation obligation. Reconcile continues while
admission is drained by applying recovery to the retained prior state. A
nonterminal recovery destination stays wrapped by the drain hold; a terminal
pending outcome leaves drained and enters `record_required`.
Recovery from a publication crash remains `record_required` until the exact
record digest is proven and the atomic terminal pointer is published.
Every entry to `reconcile_required` stores its source-specific reason,
correlation, expected authority revision, prior safe nonterminal state, and
generation. Reconciliation reads only the owning adapter's authoritative
evidence and is itself generation-fenced. Missing, stale, wrong-correlation, or
multiple/conflicting results remain `reconcile_required`; they never choose a
destination. Recovery does not signal, rewrite, or clean up a Sergeant child.

The accepted destination is prior-state and operation specific. `route` and
`handoff` append their verified external reference and return to the recorded
prior state. Dagr start moves initializing to active. `resume` moves to the
continuation's already validated target. Sergeant dispatch/coordinator actions
apply the same reducer as synchronous success and may return to the prior state,
active work, exclusion, or the matching `record_required` outcome only when
exact worker/authority evidence requires it. Local-only `record` publication
never enters external-request reconciliation. An accepted receipt that cannot
select exactly one destination allowed by the original operation remains
`reconcile_required`.

#### Projection

Projection starts at `revision_0`. A current revision may enter `revalidating`
and publish the next current revision, become `excluded`, or require a successor
packet.

Only the joint transition commit and atomic run-state generation publish an
observation set or advance event head and projection revision. An unreferenced
observation, projection, event, or transition commit is not authoritative.

#### Handoff

Handoff moves from `declared` through `awaiting_producer`, `offered`,
`validating`, `accepted`, and `consumed`. Before acceptance, `offered` may move
to immutable `withdrawn`; validation may instead produce `incompatible` or
`stale`. A withdrawn offer has no outgoing transition. Fresh evidence creates a
new monotonic offer revision from the contract's `awaiting_producer` lifecycle,
referencing but not changing the withdrawn revision.

Accepted handoffs are immutable references. Correction or new evidence creates
a new offer revision. A consumed handoff cannot be withdrawn retroactively.

#### Continuation

Continuation moves from `open` to `inputs_ready`, `resuming`, and `consumed`.
A no-effect retryable failure returns to open. Changed evidence invalidates the
continuation; replacement intent supersedes it, and deadline policy may expire
it.

#### Finding Reference

Finding reference moves from `draft` through `routing` and `routed` to
`disposition_observed`. Routing may instead fail or report ambiguous authority.

External authority owns all transitions after `routed`; Platoon observes them.

#### Unattended Qualification

Qualification starts as `not_requested` or `requested`. Requested evidence moves
to `evaluating`, then exactly one of `qualified` or `denied`. A qualified record
becomes `expired` when its time bound or any relevant evidence revision changes,
and `revoked` when an owning authority withdraws permission or the qualification
policy is superseded. Denied, expired, and revoked records are immutable history;
a later attempt creates a new qualification ID. Only current `qualified`
evidence can pass an unattended admission transaction.

#### Effect Attempt

An external effect attempt moves from `prepared` to `invoking`, then to
`committed` or `refused`. Lost/invalid receipt moves `invoking` to
`reconcile_required`; exact correlated recovery then reaches `committed`,
`refused`, or `absent`. Only `prepared` is proven pre-invocation. Terminal
attempt phases are immutable, and a repeated idempotency key must return the
same request/receipt digests. For a replay-safe command only,
`reconcile_required(ordinal=1)` may move to `retry_prepared(ordinal=2)` after the
declared proof, then `retry_invoking(ordinal=2)`, and finally `committed`,
`refused`, or terminal `reconcile_required`. The root attempt ID/request digest
never changes and no ordinal greater than two is valid.

### Failure Windows

| Window | Risk | Required behavior |
|---|---|---|
| Declaration read before packet publication | Source changes during compile | Stable read and digest verification; no run publication |
| Packet files published before run pointer | Orphan immutable files | Ignore as non-authoritative; bounded cleanup may remove after proof |
| Effect attempt prepared before invoking | Crash before phase change | Proven no invocation; revalidate generations/authorization before one call |
| Effect attempt marked invoking before adapter call | Crash before or during call | Outcome uncertain; require correlated absence/receipt evidence before retry |
| Replay proof published before retry invocation | Crash before ordinal-two call | `retry_prepared` proves no second call; revalidate gate, then invoke ordinal two once |
| Replay-safe ordinal-two invocation is uncertain | Accidental third call | Remain reconcile required; journal rejects ordinal greater than two/new root attempt |
| Adapter effect succeeds before receipt is durable | Duplicate effect after restart | Keep reconcile required; query idempotency/correlation, never blind retry |
| Receipt validated before committed transition | Lost local result | Re-query exact receipt and request digest; publish one operation result |
| Run pointer published before Dagr start receipt | Existing uncertain start | Preserve current Dagr recovery and `reconcile_required` behavior |
| Sergeant query returns while source changes | Mixed observation | Adapter provides one versioned bounded observation or marks inconclusive |
| Observation object synced before event/transition | Uninterpreted evidence appears authoritative | Ignore as unreferenced; prior published observation set remains current |
| Projection file written before revision pointer | Partial amendment | Ignore unreferenced file; retain prior revision |
| Event body written without complete framing | Torn trajectory | Reject final event and retain last verified head; never skip sequence |
| Observation/event/projection objects synced before transition commit | Orphan objects | Ignore as unreferenced; prior run state remains authoritative |
| Transition commit synced before run-state replacement | Orphan commit | Ignore as unreferenced; retry may reuse only exact digests |
| Run state names observation/event/projection not jointly bound by commit | Split-brain pointers | Invalid generation enters reconcile required; never expose any as current |
| Current pointer commit is invalid after replacement | Lost authoritative predecessor | Verify only pointer-named previous commit/chain, expose reconcile required, publish successor before effects |
| Multiple orphan commits share one predecessor | Ambiguous recovery fork | Ignore all unreferenced forks; pointer alone selects current/previous authority |
| Handoff offer published before evidence | Consumer sees incomplete output | Offer is non-acceptable until all references validate |
| Handoff accepted while source drifts | Consumer begins stale work | Compare revisions at admission; exclude on mismatch |
| Required input arrives during resume | Wrong decision consumed | Compare source revision and continuation generation immediately before effect |
| Resume request reaches Sergeant but receipt is lost | Duplicate child action | Sergeant correlation owns reconciliation; Platoon does not retry without proof |
| Reconciliation evidence changes before state commit | Wrong recovery destination | Re-read correlation/authority revision under generation fence; mismatch stays reconcile required |
| Receiving authority decides while transport receipt is lost | Authorization confused with transport | Record `unknown`; reconcile correlation and authority revision before any retry |
| Finding created externally before receipt persists | Duplicate route | Reconcile by stable digest/correlation; ambiguity blocks |
| Child terminal before Mission Record | False mission completion | Enter `record_required`; preserve exact child state separately |
| Mission Record written before terminal pointer | Orphan record | Validate digest and publish pointer atomically; orphan is non-authoritative |
| Drift found while child runs | Unsafe automatic stop or continuation | Exclude new admission; do not kill or rewrite child; request Sergeant action only if policy authorizes it |
| Scheduler invokes stale hint | Old continuation resumed | Generation and projection compare fails without effect |
| Qualification expires or is revoked during scheduler admission | Stale unattended permission used | Same fenced compare excludes operation before effect |
| Process crashes after external effect | Unattributed outcome | Reconcile through owning source evidence; unresolved state fails closed |

### Negative Test Matrix

Tests use synthetic public-safe projects, repositories, tasks, sources, and
adapter output only.

| Lifecycle | Negative case | Expected public result |
|---|---|---|
| Compile | Missing mission file in explicit declaration mode | Validation fails; no state created |
| Compile | Missing mission file in default reference mode | Preserve current path-only validation; no file read |
| Compile | Typed-looking content without explicit `missionFormat` | Remain reference mode; content sniffing is forbidden |
| Compile | Symlink, oversized, changed-during-read declaration | Fail closed with bounded diagnostic |
| Compile | Unknown schema version or field | Explicit unsupported/invalid result |
| Compile | Missing objective/effect boundary/output | Invalid, never inferred |
| Compile | Class-required output category is missing or duplicated | Invalid; completion rules are not inferred |
| Compile | Output category is unknown or extra output reuses required category | Invalid declaration |
| Compile | Effect is unknown, globally forbidden, or outside class ceiling | Invalid declaration |
| Compile | Stage effect is absent from top-level allowed set | Invalid declaration |
| Compile | Stop field/operator/type is not in negotiated source schema | Invalid declaration |
| Compile | Stop source is missing/stale/inconclusive at evaluation | Treat stop as active and block its scope |
| Compile | Authority assumption source/revision is unverified | Readiness blocked; assumption grants nothing |
| Compile | `actor-may-attempt` omits role or stage binding | Invalid declaration |
| Compile | Allowed invocation tuple has zero or multiple actor-authority matches | Invalid before packet/run publication |
| Compile | Effect has zero/multiple authoritative sources or route owners | Invalid before packet/run publication |
| Compile | Typed command lacks policy or uses unknown command reference/mode | Invalid declaration; no default replay safety |
| Compile | Blocking unknown unresolved | Packet preview not ready; apply refuses before effects |
| Compile | Contradiction has no authority route | Invalid declaration |
| Apply | Declaration digest differs from preview | No run or adapter invocation |
| Apply | Partial packet publication | No authoritative run; retry is safe |
| Apply | Readiness is not satisfied | Return bounded non-durable not-ready result; create no packet/run/event files |
| Run | Transition directly from active to completed | Reject; Mission Record remains required |
| Run | Resume drained/excluded without its matching guard | No effect; run stays non-active |
| Run | Resume superseded, completed, or failed-safely run | Terminal refusal without adapter call |
| Run | Refusal occurs after an external effect | Reconcile required, never reported as refused |
| Run | Safe failure or supersession writes a terminal state before record | Reject; retain matching `record_required` pending outcome |
| Run | Record pending outcome differs from transition evidence | Reject record and retain nonterminal state |
| Drain | Reconcile-required run is drained then resumed | Preserve/restore exact blocker, correlation, and prior reconcile state |
| Drain | Correlation resolves while reconcile-required run is drained | Update retained prior state but keep admission drained |
| Drain | Drained reconciliation resolves to terminal pending outcome | Enter matching record-required state; never reopen admission |
| Drain | Drained reconciliation evidence stays ambiguous | Preserve drained wrapper, reconcile blocker, and child ownership |
| Drain | Drain repeats on an already drained run | Idempotent success with unchanged generation semantics |
| Drain | Stale generation or terminal run is drained | Reject without state or child effect |
| Query | Project or td scope unresolved/wrong namespace | Explicit unresolved; never idle |
| Query | Sergeant `busy:null` | Platoon interpretation is indeterminate |
| Query | Git metadata but no coordinator | `queried:false`; never coordinator response |
| Query | Transcript/session exists but owner stale | No live coordinator conclusion |
| Query | Unsupported Sergeant query version | Existing adapters continue; mission observation unsupported |
| Query | Oversized or unknown-field response | Reject observation; bounded error |
| Observation | Object is unreferenced by authoritative transition commit | Ignore it; prior observation set remains current |
| Observation | Event/commit names missing or wrong-digest observation | Reconcile required; do not expose partial interpretation |
| Projection | Source locator is private path or secret-like | Reject before durable write |
| Projection | Revision skips predecessor | Keep prior authoritative revision |
| Projection | Crash before pointer update | Ignore orphan revision |
| Projection | Revalidation changes purpose, authority, effect, or completion | Reject amendment and require successor packet |
| Projection | Run state names mismatched event/projection commit | Reconcile required; prior verified generation remains displayable |
| Projection | Current commit is invalid but pointer-named predecessor verifies | Use only predecessor in reconcile-required mode; no effect until successor publishes |
| Projection | Two orphan successor commits share a predecessor | Ignore both unless authoritative pointer names one; never choose by timestamp |
| Projection | Mission uses only a proper subset of source kinds | Compile exactly the packet-required authorities; invent no empty references |
| Projection | Packet-required authority is absent or inconclusive | Block its governed operation; do not substitute an irrelevant source |
| Handoff | Producer and consumer name the same stage | Reject self-handoff; same-repository distinct stages remain valid |
| Handoff | Child or unverified caller attempts to publish an offer | Reject; only applied verified reconciliation is ingress |
| Handoff | Evidence missing, stale, wrong packet, or wrong stage | Consumer remains blocked |
| Handoff | Unknown major schema | Incompatible and reassembly required |
| Handoff | Offer is withdrawn after acceptance | Reject withdrawal; resurvey may exclude and require a new offer |
| Handoff | Withdrawal lacks current drift/invalidation evidence | Authorization gate refuses before transition commit |
| Handoff | Authorized offered revision is withdrawn | Publish immutable withdrawn state/event; acceptance is thereafter impossible |
| Handoff | Fresh evidence follows withdrawal | Publish next offer revision with new key and predecessor reference |
| Handoff | Offer withdrawn after consumption | Existing consumption preserved; correction is new offer |
| Handoff | Same idempotency key has different evidence | Reconcile required; neither offer is accepted |
| Effect attempt | Crash after prepared and before invoking | Revalidate and invoke at most once; stale authorization refuses |
| Effect attempt | Crash after invoking and before/after external effect | Reconcile by correlation; never assume absence or retry blindly |
| Effect attempt | Receipt arrives before committed state publication | Re-query exact receipt and converge one committed/refused result |
| Effect attempt | Adapter lacks correlation, deduplication, or absence proof | Typed effect is unsupported before invocation |
| Effect attempt | Reused idempotency key carries different request digest | Fail closed in reconcile required |
| Command attempt | Replay-safe command loses receipt | Same root attempt may publish ordinal two only after proof and renewed authorization |
| Command attempt | Replay-safe ordinal two is uncertain or ordinal three is requested | No further invocation; remain reconcile required |
| Command attempt | Single-attempt command loses receipt | Never retry automatically; remain reconcile required for explicit disposition |
| Trajectory | Duplicate sequence or broken previous digest | Reject tail and report reconcile required |
| Trajectory | Raw child output, prompt, token, or private path | Redaction is not enough; event rejected |
| Continuation | Missing safe state, action, route, or required input | Invalid nonterminal outcome |
| Continuation | Consumed continuation is replayed | No effect; explicit replay refusal |
| Continuation | Expired input becomes ready concurrently | Expiration or newer generation wins; no resume |
| Resume | Stale run generation or projection revision | No effect; explicit stale request |
| Resume | Required input from wrong authority | No effect; continuation stays open |
| Resume | Superseded or expired continuation | No effect; terminal refusal |
| Reconcile | Exact absence proof matches correlation and generation | Return to recorded prior safe nonterminal state |
| Reconcile | Exact refusal matches request and authority revision | Return to recorded prior state with refusal event and no retry |
| Reconcile | Exact accepted receipt determines one result | Apply original operation reducer once; destination must be allowed from recorded prior state |
| Reconcile | Lost route/handoff receipt began in drained, excluded, or record-required | Recovery preserves that prior state after appending exact reference |
| Reconcile | Lost resume/Sergeant receipt proves accepted | Move only to continuation/worker-evidence destination defined by original request |
| Reconcile | Evidence is missing, stale, wrong-correlation, or ambiguous | Stay reconcile required; no adapter effect |
| Reconcile | Recovery evidence races a newer generation | Compare fails; newer state remains authoritative |
| Resurvey | Source unavailable or contradictory | `exclude`, never `continue` |
| Resurvey | Intent/effect/completion changed | `reassemble` or `supersede`, never projection-only amendment |
| Resurvey | Compatible revision with failed evidence validation | `exclude`; prior projection remains current |
| Finding | No unambiguous owning route | No broadcast; continuation records authority gap |
| Finding | Lost receipt after external creation | Correlation reconciliation; no blind duplicate |
| Finding | External disposition changes | Observation updates; Platoon does not overwrite it |
| Finding | Disposition is observed before routed reference is durable | Reconcile by correlation; no fabricated local transition |
| Record | Acceptance claim lacks evidence | `record_required`; cannot complete |
| Record | Product or world delta omitted | Invalid record even if empty would be valid |
| Record | Safe failure lacks cause/continuation | Invalid record |
| Record | Event head changes during derivation | Retry from new head; do not publish stale record |
| Unattended | Requested without prior evidence | Qualification refused |
| Unattended | Blocking unknown, mutable authority, or unbounded input | Qualification refused |
| Unattended | Qualified evidence revision changes | Qualification expires before invocation |
| Unattended | Owning authority revokes permission | Qualification is revoked and cannot be reused |
| Unattended | Drift races scheduler invocation | Fenced compare expires qualification; no effect |
| Unattended | Expired/denied qualification ID is replayed | No effect; a new evaluation ID is required |
| Scheduler | Replayed or premature hint | Compare fails; no effect |
| External request | Effect is absent from allowed list or appears in prohibited list | Pre-request gate refuses without adapter invocation |
| External request | Applicable stop, block, contradiction, exclusion, or drift is active | Pre-request gate refuses without adapter invocation |
| External request | Continuation, packet, projection, or run generation is stale | Fenced compare refuses without adapter invocation |
| External request | Receiving authority/revision is missing or stale | Pre-request gate refuses without adapter invocation |
| External request | Authorization changes after pre-request gate | Receipt revision mismatch is unknown and enters reconciliation |
| External request | Receiving authority refuses | Record exact refusal; no retry or accepted interpretation |
| External request | Receipt is lost or authorization revision mismatches | Record unknown and reconcile by correlation; no blind retry |
| External request | Receipt omits authority decision reference | Reject receipt as inconclusive and fail closed |
| Dagr effect | Typed packet omits or prohibits workflow start/terminal acknowledgment effect | Authorization gate refuses before Dagr invocation |
| Sergeant dispatch | Typed packet omits/prohibits dispatch or has an active stop | Authorization gate refuses before dispatch invocation |
| Effecting command | Validation/integration effect or authority revision is not current | Authorization gate refuses before command invocation |
| Compatibility | Existing v1alpha1 manifest with old Sergeant files | Existing behavior unchanged; mission features report compatibility mode |
| Compatibility | Nonterminal pre-mission run is older than immediately prior schema | Original commands remain operable; no forced in-place upgrade |
| Rollback | Typed run has an active Sergeant child | Rolled-back release still reconciles/drains/resumes that run; child is untouched |
| Rollback | Typed run is excluded, reconcile-required, or record-required | Rolled-back release retains all guarded operations needed to reach terminal |
| Rollback | Binary cannot operate an existing typed state version | Rollback is refused before replacement; current compatible binary remains |
| Rollback | Pinned compatibility artifact is absent or fails a state fixture | New typed state publication remains disabled; no run files are created |
| Rollback | Compatibility artifact attempts to create its disabled state version | Explicit refusal before publication; existing typed runs remain operable |
| Privacy | Source contains secret or absolute private path | Output omits it and records bounded rejection |
| Bounds | More sources/events/findings than limits | Deterministic truncation metadata or fail closed where completeness is required |
| Bounds | Whole exact observations exceed aggregate status cap | Return deterministic whole-object page/count/cursor; omitted means unknown |
| Bounds | Cursor is stale, malformed, or belongs to another run/generation | Reject cursor; do not return a mismatched page |

### Compatibility, Migration, And Rollback

#### Version Negotiation

- Existing manifests remain `platoon.dev/v1alpha1`.
- An optional `spec.missionFormat` field is added to that manifest version with
  values `reference` and `declaration-v1alpha1`; omission defaults to
  `reference`, preserving every existing manifest. Content or filename sniffing
  is forbidden. The currently required `spec.mission` path is not repurposed.
- `reference` is legacy mode: Platoon validates the path syntax exactly as it
  does now and does not require, read, or snapshot the referenced file.
  `declaration-v1alpha1` is explicit opt-in: the referenced file must exist and
  strictly decode as `platoon.dev/mission/v1alpha1`, or validation fails.
- A later manifest API version may require the typed declaration. That change is
  outside the first implementation and requires explicit migration tooling.
- Durable state adds a new major/minor version. Every released pre-mission state
  schema remains readable and operable by its original command behavior while
  it is nonterminal; terminal legacy state remains read-only inspectable.
  Writers never upgrade or downgrade a run in place. Retirement requires an
  explicit major release, backup/export path, inventory proving no nonterminal
  run uses the version, and an operator-visible migration notice.
- Sergeant negotiation starts with a capability/version query. Unsupported
  generic status leaves existing dispatch/file adapters operational and marks
  only the new observation unavailable.
- Existing callback origin and fleet file contracts remain supported until a
  separately announced adapter removal. The new query does not silently replace
  dispatch correlation recovery.

Command compatibility is explicit:

| Command | `reference` (default/legacy) | `declaration-v1alpha1` |
|---|---|---|
| `validate` | Current manifest/path checks; missing mission file remains permitted | Stable-read and strictly validate declaration; missing/invalid file fails |
| `plan` | Current deterministic admission plan | Compile packet/readiness preview without mutation |
| non-applied `start` | Current preview | Same typed compile preview |
| applied `start` | Current intent snapshot, Dagr, and Sergeant behavior | Publish packet/run, then enter existing fenced path |
| `reconcile`, `drain`, `resume` | Current run semantics only | Mission guards wrap existing semantics without child writes |
| `status` | Current status plus `missionMode: reference` | Deep observed/interpreted mission status |
| `resurvey`, `route`, `record`, unattended scheduling | Explicit `unsupported_for_reference`; no effect | Available only at the phase/version that implements the contract |

Current state mappings are likewise explicit and do not convert a legacy run:

| `platoon.state/v1alpha1` run | Compatibility interpretation | Allowed handling |
|---|---|---|
| `initialized` | `legacy.initialized` | Existing start/recovery behavior |
| `active` | `legacy.active` | Existing reconcile/drain behavior |
| `drained` | `legacy.drained` | Existing reconcile/resume behavior |
| `reconcile_required` | `legacy.reconcile_required` | Existing recovery behavior |
| `completed` | `legacy.completed` | Terminal read-only inspection |
| `failed` | `legacy.failed` | Terminal read-only inspection |

Legacy terminal runs are not backfilled with Mission Records and are never
reported as satisfying typed-record guarantees. Typed run states use only the
transition table in this PRD.

#### Migration

1. Add declaration validation and deterministic compile preview, with no durable
   changes.
2. Add packet/run snapshot fields behind an opt-in typed declaration; preserve
   current state loading.
3. Add read-only mission status and source-labeled interpretation, initially
   from existing local/Dagr/fleet evidence.
4. Integrate Sergeant #207 when its versioned contract is available; retain
   current file adapter for existing dispatch ownership.
5. Add projection and trajectory publication, then handoffs and resurvey.
6. Add continuation/resume, finding route, Mission Record, and unattended
   qualification only after their negative matrices are green.

No migration scans arbitrary fleet or td state. Existing active runs continue
under their original state schema and command behavior; they are not upgraded in
place. Operators may finish them or start a new typed mission run.

#### Rollback

Delivery is compatibility-first. Before any release enables creation of a new
typed state version, it SHALL build and identify a rollback artifact that has
creation of that version disabled but retains every reader and guarded operation
for `initializing`, `active`, `drained`, `excluded`, `reconcile_required`, and
`record_required`, including runs with active Sergeant children. Release
metadata pins the rollback artifact version and SHA-256. CI exercises that
artifact against synthetic durable fixtures for every state and operation. If
the artifact is unavailable or any fixture fails, typed state publication stays
disabled: explicit typed applied start refuses without creating files, while
legacy manifests preserve existing behavior.

Each phase is feature-gated by document/state version rather than an environment
toggle hidden from status. Rollback means stop creating new typed runs while
retaining both read and guarded mutating operations for every already published
typed nonterminal run, including reconcile, drain, resume, resurvey, handoff,
route, and record. The rolled-back release carries compatibility handlers until
those runs are terminal. A binary lacking handlers for any published typed
state version is not a valid rollback target and MUST NOT replace the compatible
binary. Never rewrite new durable state into an old layout.

If Sergeant query integration must roll back, mission status marks the source
unsupported and uses only previously supported exact local evidence. It does not
infer equivalent answers. Existing workers continue under Sergeant ownership.

### Observability, Privacy, And Bounds

Machine-readable output and human rendering derive from one result object.
Minimum observability includes operation, run/packet/projection versions,
generation, source quality counts, last meaningful event time, drift verdict,
continuation ID, truncation flags, adapter durations, and bounded error codes.

Metrics SHALL count states and durations without project names, task IDs, paths,
source bodies, or user identities. Logs use opaque run/event IDs and error codes.
No raw child stdout/stderr, prompt, transcript, raw or unvalidated adapter
response body, environment, credential, private path, hostname, or source
document is persisted in trajectory or returned by status. Status may return
the exact fields of a schema-validated, bounded typed observation; that object is
the explicit observation contract, not a raw transport body.

Initial hard bounds, configurable only within published safe ranges:

- declaration and packet: 4 MiB each;
- one source observation: 1 MiB;
- status output: 2 MiB with explicit truncation sections;
- event: 64 KiB; 10,000 events per status scan;
- sources: 1,024; handoffs: 1,024; findings: 1,024 per run;
- diagnostics: 4 KiB each, at most 100 in one response;
- adapter calls: current command timeout and output limits unless a lower
  operation-specific bound applies.

Status orders observation objects by source label, stable source ID, observation
time, then digest. It emits only whole schema-validated objects that fit after
the fixed report sections; it never truncates fields inside an observation. If
the next whole object would exceed 2 MiB, status sets
`observationsTruncated:true`, reports total/returned counts, and returns a
versioned opaque cursor for the next ordering key. Callers may page or select
source IDs, subject to the same aggregate cap. A cursor contains no locator or
private value. Omitted observations remain unknown/not-returned, never absent,
and interpretations cite observation IDs even when their full object is on a
later page.

Completeness-sensitive operations, compilation, handoff acceptance, drift
verdict, resume, and record derivation, fail closed rather than truncate inputs.
Read-only status may truncate display lists only when totals, ordering, cursor,
and `truncated:true` remain explicit.

## Testing Decisions

Testing occurs at the highest public seams and follows one vertical behavior at
a time.

1. CLI tests prove `validate`, `plan`, non-applied `start`, and `status` create no
   state or adapter effects.
2. Schema/runtime shared fixtures prove strict declaration, packet, handoff,
   effect-attempt, event, continuation, and record validation. Golden packet
   fixtures prove the canonical byte/digest contract across equivalent ordering
   and meaningful source changes.
3. A synthetic fake Dagr/Sergeant lifecycle extends the existing end-to-end
   prior art: compile, apply, dispatch, observed waiting/needs-input, handoff,
   drift, reconcile-required drain/resume, terminal evidence, record.
4. Fake Sergeant #207 responses cover td epic resolution, wrong namespace,
   `busy:null`, stale coordinator, no correlated response, incompatible version,
   and cross-harness isolation without importing Sergeant source.
5. Crash tests inject failure at every publication pointer and external request
   window in [Failure Windows](#failure-windows), including each observation,
   projection, event, effect-attempt phase, receipt, commit, and state-pointer
   boundary in the joint protocol.
6. A table-driven authorization test crosses every operation/effect mapping with
   absent permission, prohibition, active/unknown stop, stale generation,
   invalid stage scope, stale authority, and receipt revision mismatch; no
   failing case may reach its adapter.
7. Golden JSON tests verify exact observation versus interpretation labels,
   stable ordering, bounds, and privacy rejection.
8. Property tests verify append-only event chains, generation fencing,
   idempotent replay, and no unsafe drift downgrade.
9. Compatibility tests load current `platoon.state/v1alpha1`, existing
   `platoon.dev/v1alpha1` manifests, callback origin v1, and fleet files.
10. Race tests cover concurrent status/resurvey/resume and cross-root claims.
11. The repository's full `make test`, `make race`, `make vet`, and `make demo`
    remain release gates for every vertical slice.

Mocks stop at public adapters. Tests do not duplicate internal algorithms or
assert private helper calls. All fixtures are synthetic and public-safe.

## Out of Scope

1. A full clone of the Garrison Work Filesystem.
2. A new Bugle CLI.
3. A new Librarian system or organization-wide knowledge service.
4. Distributed consensus, cross-host leases, or a new workflow engine.
5. Direct OpenCode or other harness session injection.
6. An internal worker, polling, scheduling, callback, or wake daemon.
7. td graph traversal, project registry resolution, worker liveness inference,
   coordinator discovery, callback queue management, or harness session routing
   inside Platoon.
8. Absorbing Sergeant worker lifecycle, review, recovery, notification, drain,
   wake, or cleanup ownership.
9. Replacing Dagr readiness or writing Dagr state without its supported adapter.
10. Replacing Git history, td disposition, repository authorization, deployment
    authorization, or any receiving system's policy.
11. Automatic merge, push, identity switching, production activation, or child
    state writes.
12. Treating an Atlas proposal as implemented or proven behavior.

## Further Notes

### Phased Delivery

Each phase is independently deployable, documented in README, and green under
the full native suite.

#### Phase 0: Declaration Preview

Add strict declaration parsing and compile readiness to `validate` and `plan`.
No state or runtime query. Demonstrates that blocking unknowns and contradictions
refuse entry.

#### Phase 1: Immutable Packet

Snapshot typed declaration and packet during applied start. Bind packet digest
to a new run-state version and publish projection revision zero, genesis event,
immutable observations required by start/effect receipts, immutable transition
chain/two-generation pointer, and generalized attempt journal. Demonstrates
crash-safe publication and legacy-run operational compatibility. Publish and
validate the creation-disabled rollback artifact before enabling the first typed
state write. The first typed external effect also includes the closed
authorization mapping. Until all of these minimum contracts are green, typed
behavior remains preview-only.

#### Phase 2: Deep Read-Only Status

Return source-labeled local/Dagr/current fleet observations and separate Platoon
interpretation. Demonstrates bounded privacy-safe projection without #207.

#### Phase 3: Sergeant Generic Query

Add the one versioned adapter after #207 ships. Demonstrates project/td scope,
inconclusive activity, and coordinator-aware evidence without local traversal.

#### Phase 4: Projection Expansion And Trajectory Enrichment

Add post-start source expansion, additional observation adapters, and the full
meaningful-event vocabulary on the Phase 1 projection/event substrate.
Demonstrates context expansion and richer resurvey evidence without packet
mutation.

#### Phase 5: Handoffs And Drift

Enforce producer/consumer contracts and resurvey verdicts before consumer
admission. Demonstrates compatible continuation and fail-closed exclusion.

#### Phase 6: Continuation And Routing

Add required-input verification, compare-before-set resume, and mission finding
routing through owning adapters. Demonstrates no competing td/Sergeant router.

#### Phase 7: Mission Record

Derive and validate records before mission completion. Demonstrates product and
world delta plus safe-failure continuation.

#### Phase 8: External Scheduling Qualification

Expose scheduler hints and unattended qualification. Demonstrates stale hint
refusal and bounded operation; adds no daemon.

### Success Measures

The first implementation is successful when:

- 100% of typed applied runs bind an immutable declaration and packet digest;
- no status field conflates observation with interpretation;
- all inconclusive Sergeant evidence remains inconclusive;
- every accepted handoff cites producer revision and evidence;
- every drift event receives exactly one conservative verdict;
- every nonterminal interpretation and failed-safely record has a valid
  continuation;
- every typed terminal mission has a validated Mission Record;
- no new path traverses td/fleet/coordinator state outside the Sergeant adapter;
- privacy and bound tests show no raw child output or private source content;
- existing manifests and adapters retain their documented compatibility path.

These are contract measures, not proof of the Atlas's broader organizational
claims. Later pilots may measure open search, wrong-repository starts, unstated
assumptions, handoff failures, and residual judgment, but this PRD does not claim
those improvements before evidence exists.

### Open Questions And Recommendations

1. **What exact schema name will Sergeant #207 publish?** Recommendation: keep
   the adapter capability-negotiated and vendor schema opaque until #207 lands;
   do not freeze a guessed Sergeant response into Platoon state.
2. **Where should a portable Mission Record be published in addition to local
   state?** Recommendation: start with local validated JSON plus references in
   operator output. Add Git/td publication only through separately authorized
   adapters after idempotency and privacy contracts exist.
3. **How long should trajectories be retained?** Recommendation: retain with the
   run by default and add explicit operator-owned archival/purge policy later;
   never silently compact an event chain needed by a live or reviewable run.
4. **Which prior-run evidence is sufficient for unattended qualification?**
   Recommendation: require a separately reviewed policy before Phase 8. Until
   then, implement the data model and always return not qualified.

### Source Notes

#### Implemented Evidence

- Platoon source is pinned to commit
  [`1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4`][platoon-tree]. Citations above
  use immutable blob URLs and line ranges.
- Sergeant source context is pinned to commit
  [`efcc96639ab83caf908e651bbedc0790487620a0`][sergeant-tree]. The integration
  requirement itself is [public issue #207][sergeant-207], including its
  explicit namespace, activity, coordinator, privacy, and bounded-output
  acceptance criteria. Issue #207 is proposed work, not implemented behavior.
- At that Sergeant fixed point, `sgt-status` reports registered project Git
  state rather than coordinator contact ([source][sergeant-status]); the
  read-only watch regression contract permits only `busy:true` or `busy:null`
  and requires a verified active witness ([source][sergeant-watch-test]); and
  callbacks are bounded, correlated, durable, and Sergeant-owned
  ([contract][sergeant-callbacks]).
- The Atlas itself identifies Sergeant as running evidence for fixed-point
  provenance and lifecycle practices in its [Appendix][atlas-appendix]. This PRD
  still relies on Platoon and Sergeant public source for implemented claims.

#### Atlas Design Inspiration, Not Implemented Evidence

- [Chapter 0][atlas-0] and the [Field Reference WorkPacket][atlas-workpacket]: immutable
  declaration, sourced context tiers, explicit effects/stops, projection, and
  Mission Record.
- [Chapter 1][atlas-1]: blocking versus non-blocking unknowns,
  contradictions, sufficiency, and fail-closed entry.
- [Chapter 2][atlas-2]: one mission across repository assignments, immutable
  parent intent, handoffs, and fixed points per repository.
- [Chapter 3][atlas-3]: datum mismatch, resurvey, compatible amendment,
  reassembly, invalidation, and exclusion windows.
- [Chapter 4][atlas-4]: unattended qualification, meaningful progress rather
  than process liveness, healthy non-execution, cause classification, and
  continuation.
- [Chapter 5][atlas-5]: product versus world delta, local maturity, and routing
  decisions back to judgment when deterministic assumptions stop holding.
- [Field Reference][atlas-reference]: implementation-neutral contracts for
  packet, projection, workflow, finding, and Mission Record.

[atlas-0]: https://garrison-field-atlas.netlify.app/
[atlas-1]: https://garrison-field-atlas.netlify.app/1/
[atlas-2]: https://garrison-field-atlas.netlify.app/2/
[atlas-3]: https://garrison-field-atlas.netlify.app/3/
[atlas-4]: https://garrison-field-atlas.netlify.app/4/
[atlas-5]: https://garrison-field-atlas.netlify.app/5/
[atlas-appendix]: https://garrison-field-atlas.netlify.app/appendix/
[atlas-reference]: https://garrison-field-atlas.netlify.app/reference/
[atlas-workpacket]: https://garrison-field-atlas.netlify.app/reference/#workpacket
[platoon-adapters]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/docs/adapters.md#L71-L115
[platoon-architecture]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/docs/architecture.md#L3-L13
[platoon-dispatch]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/internal/adapter/sergeant.go#L13-L87
[platoon-limitations]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/README.md#L190-L208
[platoon-manifest-schema]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/schema/platoon.schema.json#L17-L25
[platoon-manifest-validation]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/internal/manifest/manifest.go#L229-L246
[platoon-start]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/internal/commander/commander.go#L69-L182
[platoon-state]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/internal/state/types.go#L16-L151
[platoon-status]: https://github.com/mrtnebrle/platoon/blob/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4/internal/cli/status.go#L12-L123
[platoon-tree]: https://github.com/mrtnebrle/platoon/tree/1b39c499ef25eaa2e3d256f2d4abae9d65cf40b4
[sergeant-207]: https://github.com/callmeradical/sergeant/issues/207
[sergeant-callbacks]: https://github.com/callmeradical/sergeant/blob/efcc96639ab83caf908e651bbedc0790487620a0/docs/callbacks.md
[sergeant-status]: https://github.com/callmeradical/sergeant/blob/efcc96639ab83caf908e651bbedc0790487620a0/bin/sgt-status
[sergeant-tree]: https://github.com/callmeradical/sergeant/tree/efcc96639ab83caf908e651bbedc0790487620a0
[sergeant-watch-test]: https://github.com/callmeradical/sergeant/blob/efcc96639ab83caf908e651bbedc0790487620a0/tests/sgt-watch-snapshot-test.sh
[rfc-8785]: https://www.rfc-editor.org/rfc/rfc8785
