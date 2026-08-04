# Architecture

## Ownership

Platoon has one durable authority: its configured local state root. Dagr remains
the authority for acyclic stage readiness. Sergeant remains the authority for a
child fleet's worktree, process, task lifecycle, notifications, review, result,
and cleanup. Platoon stores references and verified summaries; it never writes a
Sergeant status or child worktree.

One manifest stage maps to one task, one repository, one dagr stage, and at most
one Sergeant fleet. This prevents a multi-repository child from satisfying one
dagr stage before all of its work is terminal.

## Components

- `internal/manifest` strictly decodes, defaults, and validates the versioned
  manifest.
- `internal/planner` computes longest downstream path and direct unlock counts,
  then atomically evaluates token, writer, path, semantic, and protected-claim
  policy.
- `internal/state` owns restrictive atomic JSON, the generation-fenced lease,
  and the stable per-user claim/dispatch/integration authority.
- `internal/adapter` invokes commands as argument arrays, parses dagr receipts,
  parses Sergeant dispatch correlation, verifies fleet state, and inspects Git
  changed paths.
- `internal/commander` implements start, admission, adoption, reconciliation,
  drain/resume, terminal convergence, and repository candidate queues.
- `internal/cli` keeps previews separate from explicit applied mutations and
  projects durable status for operators.

## Admission Transaction

For every ready stage, the held Commander generation:

1. Recomputes active implementation/review use and active repository claims.
2. Evaluates deterministic priority and all limits under the Commander lock.
3. Registers repository claims in the per-user authority across all state roots.
4. Atomically writes a `prepared` reservation with correlation identity.
5. Acquires the user-global Sergeant dispatch lock while state remains prepared.
6. Atomically moves it to `dispatching` immediately before invocation.
7. Invokes one single-repository dispatch.
8. Requires matching early/final receipt IDs, callback correlation, and exactly
   one matching durable fleet repository.
9. Atomically commits the fleet ID or retains `reconcile_required`.

A crash before step 3 has no dispatch. A crash while `prepared` is proven to be
pre-invocation and may dispatch once. A crash in `dispatching` never causes an
unbounded retry. A later cycle scans bounded callback evidence, commits one
exact match, blocks on multiple matches, or permits one bounded retry after
proven absence before requiring operator reconciliation.

## Reconciliation

One applied reconciliation cycle is bounded:

1. Acquire a new lease generation or fail closed.
2. Retry any journaled dagr terminal transition idempotently.
3. Recover provisional reservations and configured adoptions.
4. Verify each known fleet's immutable binding and current durable status.
5. Release tokens only from verified terminal evidence.
6. Diff successful work from the dispatch base and enforce path claims.
7. Acquire the user-global integration lock, then process in-claim candidates.
8. Verify dagr's resulting state, read newly ready stages, and admit if active.
9. Atomically persist the resulting run state.

The local desired dagr terminal state is journaled before the dagr command. If
either durable store is ahead after a crash, retry plus post-verification
converges without another child or another integration command.

## Durable State

```text
<state-root>/
  .commander.lock
  lease.json
  runs/<run-id>/
    state.json
    intent.md
    workflow.yaml
```

All state and fleet roots for one local user coordinate through a restrictive
per-UID authority below the passwd-account home state directory. It contains a
claim registry plus dispatch/integration kernel locks, but never child state or
command output. Lock acquisition uses nonblocking retries bounded by context.

Run initialization creates the run directory, `intent.md`, and `workflow.yaml`
first. `state.json` is the final publication point, so partial initialization is
not authoritative.

Directories are `0700`; files are `0600`. JSON readers reject unknown fields,
truncation, symlinks, devices, oversized input, and group/world-readable
authority files. Writes create and sync a unique same-directory temporary file,
rename it, then sync the directory.

## State Machines

Run:

```text
initialized -> active -> completed|failed
                    |-> drained -> active
                    |-> reconcile_required -> active
```

Stage:

```text
pending -> ready -> queued -> reserved -> dispatched -> in_progress
                                  |             |-> waiting|needs_input|blocked
                                  |             |-> reconcile_required
                                  |             |-> out_of_claim
                                  |             |-> candidate -> integrating
                                  |                                |-> candidate (base change/recovery)
                                  |                                |-> merge_ready -> done
                                  |                                |-> failed
```

Reservation:

```text
prepared -> dispatching -> committed -> released
                 |-> reconcile_required -> committed
                                         |-> absent (bounded proof, no child)
```

Merge candidate:

```text
queued -> integrating -> merge_ready
             |-> queued (base change or interrupted attempt)
             |-> failed
```
