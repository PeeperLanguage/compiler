# Context vault

What an agent needs before touching this repository, in one place. Every
fact here names the file it came from; when a source and this vault disagree,
the source wins and the vault is fixed in the same change.

| File | Holds |
| --- | --- |
| [`01-product.md`](01-product.md) | What Peeper and its compiler are for; goals in order; what works and what does not |
| [`02-pipeline.md`](02-pipeline.md) | The compile as a state machine: phases, per-declaration states, what each phase reads and writes |
| [`03-vocabulary.md`](03-vocabulary.md) | The terms and tokens of the codebase: crates, ids, diagnostic code families, MIR terms, fixture modes |
| [`04-versions.md`](04-versions.md) | Toolchain and dependency versions, targets, CI and validation commands |
| [`05-rules.md`](05-rules.md) | Digest of the six guidance files (`RULES.md`, `AGENTS.md`, `rust-style.md`, `RULEBOOK.md`, `docs/architecture.md`, `docs/ir-design.md`) |
| [`06-plan.md`](06-plan.md) | The sequential plan for the current work, with status |
| [`07-worklog.md`](07-worklog.md) | What was done, measured and decided, with commits and evidence |

## How to use it

1. Read `05-rules.md` and `06-plan.md` before any edit; the full rules stay in
   the six files it digests, and a digest line is no excuse to skip them.
2. Before claiming something exists or was done, find it in the source or in
   `07-worklog.md`. Write nothing here from memory.
3. After each step, update `06-plan.md` (checkbox) and append to
   `07-worklog.md`: the commit, what changed, the numbers, what was not done.
4. When code changes a fact recorded here (a phase, a version, a code
   family), update the vault in that commit.
