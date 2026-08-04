# Operations

## Preflight

1. Pin trusted dagr, `sqlite3`, Sergeant, Git, and acceptance executables.
2. Ensure the manifest directory contains the referenced canonical intent.
3. Ensure each task exists in the configured Sergeant project/repository.
4. Use distinct repository branch prefixes for unrelated Platoon runs.
5. Ensure the passwd-account home permits creation of Platoon's restrictive
   per-user authority directory.
6. Validate and inspect the deterministic plan before applying.

```sh
platoon validate --file platoon.yaml
platoon plan --file platoon.yaml
platoon start --file platoon.yaml
```

## Start

```sh
platoon start --file platoon.yaml --state .platoon --apply
```

Record the returned local run ID. An error after the state root appears is not a
request to rerun start. Inspect status; the durable run may require
reconciliation.

## Reconcile

Run one bounded cycle by default:

```sh
platoon reconcile --run <run-id> --state .platoon --apply
```

Use explicit bounded polling only for attended operation:

```sh
platoon reconcile --run <run-id> --state .platoon --apply --poll 5s --max-cycles 60
```

Polling intervals are 100 milliseconds through 1 hour. `--max-cycles` defaults
to 60, accepts 1 through 10000, and is invalid without `--poll`. Polling ends
early at `completed` or `failed`. An adapter error returns nonzero and leaves
durable recovery evidence. Do not rerun start or manually edit `state.json`.

## Status

```sh
platoon status --run <run-id> --state .platoon
```

Inspect:

- implementation/review used versus configured tokens;
- active repository path/semantic claims;
- queued, running, adopted, and child fleet identities;
- merge queue state and integration attempts;
- blockers and critical ready ordering.

`reconcile` without `--apply` returns the same local status in a preview envelope
and does not inspect or mutate upstream adapter stores.

## Drain And Resume

```sh
platoon drain --run <run-id> --state .platoon --apply
platoon resume --run <run-id> --state .platoon --apply
```

Drain stops later admissions but reconciliation continues for reservations and
children already known. It does not call Sergeant drain, signal workers, or
change child status. Resume is rejected for terminal runs.

## Blocker Runbook

### `reconcile_required`

- Do not dispatch the stage manually or rerun start.
- Compare the reservation correlation with callback origin evidence.
- If exactly one fleet exists, rerun applied reconciliation.
- If no or multiple fleets exist, preserve both state roots and resolve the
  upstream ambiguity before changing any authority.

### `out_of_claim`

- Treat the child fleet as unchanged and still Sergeant-owned.
- Inspect the bounded path list against the manifest claim.
- Correct work through the owning issue/fleet or revise the manifest through a
  new audited run. Never edit Platoon state to claim success.

### Lease held or ambiguous

- Check whether another Commander command is live.
- Wait through the configured TTL only when the owner process has exited.
- A different-host owner is intentionally non-recoverable in this release.
- Never delete `lease.json` or lock files to bypass fencing.

### Integration failed

- No merge or push occurred.
- Inspect the configured command directly in the child worktree.
- A failed candidate remains terminal for this run; remediation belongs in a
  successor issue-owned stage.

### Base changed

- The candidate returns to queued with the current base identity.
- The next reconciliation reruns all configured integration/acceptance commands.

## Backup And Cleanup

Back up the entire state root atomically while no Commander command is active.
Do not copy only `state.json`; lease, workflow, and dagr database references form
one recovery context. Platoon does not clean Sergeant fleets or worktrees. Use
Sergeant's own reviewed cleanup process only after run and child evidence are
terminal.
