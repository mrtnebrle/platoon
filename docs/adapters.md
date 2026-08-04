# Command And State Adapter Contract

Platoon invokes every subprocess with an executable and argument array through
`exec.CommandContext`. It does not interpolate a shell, store environment
values, or include command output in durable errors. Stdout and stderr are
separate and bounded. Unexpected stderr fails closed.

## Dagr CLI Profile

An applied start generates a unique hookless workflow and invokes:

```text
dagr [configured args] --db <database> workflow load <workflow.yaml>
dagr [configured args] --db <database> stage list <workflow-name>
dagr [configured args] --db <database> run start <workflow-name>
```

Reconciliation uses:

```text
dagr [configured args] --db <database> run watch <full-run-id>
dagr [configured args] --db <database> run step-done <full-run-id> <full-stage-id>
dagr [configured args] --db <database> run step-fail <full-run-id> <full-stage-id>
```

If the Commander crashes after run-start intent is durable but before the full
ID is local, the configured `inspectExecutable` runs a bounded command equivalent
to:

```text
sqlite3 -readonly -batch -noheader <database> <fixed full-run-ID query>
```

The generated workflow name is a validated lowercase slug and is the only query
value. Zero rows proves absence and permits one start, one canonical UUID is
adopted and verified through `dagr run watch`, and multiple rows block.

Platoon requires canonical full UUIDs from load/list/start receipts. It never
uses the shortened run ID shown by list/watch as authority. Snapshot parsing
requires every expected short stage name exactly once with a recognized
icon/status pair. Terminal acknowledgements are post-verified with another
snapshot.

Generated YAML uses only `name`, stage `name`, `depends-on`, and deterministic
`position`. It contains no hook, executor, sub-DAG, brief, or shell content.

## Sergeant Dispatch Profile

For one stage, Platoon invokes the configured dispatch executable with:

```text
<project>
--td <task>
--repos <one-repository>
--branch <repository-prefix>-<stage-id>
--agent <harness>
--stage <stage-id>
--intent-file <canonical-intent>
--origin-profile <profile>
--correlation-id <run-id>-<stage-id>
```

The command must exit zero and print exactly one early `task-id: <id>` and one
final `Task ID: <id>` with equal safe IDs. Platoon then verifies durable state;
stdout alone never commits a reservation.

Before invocation, every local Platoon process for the current user acquires one
user-global restrictive transport lock. Its path is independent of state and
fleet roots, preventing credential-helper races across unrelated manifests.

## Fleet State Profile

For fleet `<fleet>` and repository `<repo>`, Platoon reads:

```text
<fleetRoot>/<fleet>/intent_revision
<fleetRoot>/<fleet>/.callbacks/origin.json
<fleetRoot>/<fleet>/<repo>/intent_revision
<fleetRoot>/<fleet>/<repo>/project
<fleetRoot>/<fleet>/<repo>/td_task
<fleetRoot>/<fleet>/<repo>/stage
<fleetRoot>/<fleet>/<repo>/branch
<fleetRoot>/<fleet>/<repo>/status
<fleetRoot>/<fleet>/<repo>/result          # required only for done
<fleetRoot>/<fleet>/<repo>/worktree
<fleetRoot>/<fleet>/<repo>/worktree_git_pointer
<fleetRoot>/<fleet>/<repo>/worktree_git_dir
<fleetRoot>/<fleet>/<repo>/initial_sha
```

Every field must be a bounded regular non-symlink and stable across two reads.
The fleet task must contain exactly one non-hidden repository directory.
Project, task, stage, derived issue branch, callback origin (for dispatched
work), repository, and both intent revisions must match the manifest/run
binding. The worktree, `.git` pointer target, and recorded Git directory are
pinned by filesystem device/inode identity on first observation and rechecked;
`initial_sha` must be a full Git hash.

Recognized nonterminal states are `dispatched`, `in_progress`, `needs_input`,
`blocked`, `waiting`, `orphaned`, and `drained`. Success is exact `done` plus a
non-empty result. Failure is `failed: <nonblank reason>`. Result content is not
persisted; Platoon records only its SHA-256 digest.

Callback origin is strict JSON:

```json
{
  "version": "sergeant.callback-origin/v1",
  "profile": "platoon-local",
  "correlation_id": "run-example-stage-example"
}
```

Recovery scans at most 1024 fleet-root entries. Malformed callback authority or
zero/multiple matches blocks recovery.

## Git Diff Profile

Platoon verifies Sergeant's recorded `.git` pointer and physical Git directory,
builds a private temporary index from the dispatch tree, filters ambient
`GIT_*` variables, and executes bounded argument-array commands equivalent to:

```text
git --no-replace-objects --git-dir=<git-dir> --work-tree=<worktree> read-tree <initial_sha>^{tree}
git --no-replace-objects --git-dir=<git-dir> --work-tree=<worktree> diff --raw -z --no-renames <initial_sha> --
git --no-replace-objects --git-dir=<git-dir> --work-tree=<worktree> ls-files --others -z --
```

Disabling rename detection causes both old deletion and new addition paths to be
checked. Ignored untracked files remain visible. Raw old/new modes identify
deleted and type-changed symlinks as well as current symlinks. NUL framing
preserves unusual names; control characters are rejected. Root Sergeant
control/transport files are excluded. Every changed symlink is out of claim.
The child index, replacement refs, global/system Git configuration, fsmonitor,
sparse checkout, and ambient Git directory variables are not authority.

## Sergeant Operational Controls

The manifest records `sgt-watch`, `sgt-wake`, and `sgt-drain` executables so the
compatibility boundary is explicit. Platoon intentionally does not call them:
watch/wake/drain can synchronize or signal child lifecycle, which remains
Sergeant-owned. Platoon drain is local admission state only.
