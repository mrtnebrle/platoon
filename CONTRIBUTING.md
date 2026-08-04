# Contributing

Platoon coordinates external durable systems, so behavioral changes are treated
as state-machine changes rather than ordinary CLI refactors.

## Development

1. Start from an issue with explicit acceptance criteria.
2. Add one failing behavioral test at a public seam.
3. Implement only enough of that vertical slice to pass.
4. Run focused tests while iterating.
5. Update `README.md` in the same commit whenever a command, manifest field,
   lifecycle transition, installation step, safety invariant, limitation, or
   example changes.
6. Before review, run `make test`, `make race`, `make vet`, and `make demo`.

Tests and examples must use synthetic projects, repositories, tasks, fleets,
hosts, identities, and URLs. Never commit local paths, private issue IDs,
credentials, command output, fleet evidence, or internal product names.

## Safety Expectations

- Keep validation, planning, status, and non-applied previews side-effect-free.
- Require explicit apply for every mutation.
- Journal intent before external mutation and make recovery idempotent.
- Treat missing, stale, malformed, or ambiguous evidence as a blocker.
- Invoke commands with argument arrays and bounded output; do not add shell
  interpolation. Preserve process-group cancellation and bounded pipe waiting.
- Do not write Sergeant child state, change global identity, merge, or push.
- Preserve restrictive state permissions and symlink defenses.

## Review

Reviews prioritize correctness, regressions, crash windows, identity/binding,
concurrency, claim escape, and missing negative tests. Cosmetic-only changes are
not blockers. A pull request should explain its state transitions and include
the exact repository-native validation commands run.
