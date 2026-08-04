# Agent Guidance

This repository owns Platoon-level orchestration only. Do not copy, fork, or
modify Sergeant or dagr source. Integrate through the documented command and
durable-state adapters.

- Use TDD with one vertical behavior at a time.
- Keep all fixtures, examples, logs, and docs synthetic and public-safe.
- Never place secrets, private paths, raw child output, or internal evidence in
  source or durable test fixtures.
- Preserve fail-closed admission, fencing, explicit apply, and child ownership.
- Never add automatic merge, push, identity switching, or child-state writes.
- Update `README.md` in the same change set as every operator-visible behavior;
  CI verifies the base-to-head README blob.
- Run focused checks during implementation and the full native validation once
  before delivery.
- Treat `.sergeant-*` files as local orchestration transport; never commit them.
