# LESSONS — test-failure prevention rules

Append-only registry of rules derived from unexpected test failures.
Maintained per the `test` skill's lesson loop. **Read this file before
starting any implementation task** (enforced by the `implement` skill).

Format — one row per lesson, newest last:

| # | Date | Context (phase/package) | What failed | Root cause | Rule (imperative) |
|---|---|---|---|---|---|

Rules for this file:

- Entries are appended in the same commit as the fix; never edited away.
  A superseded rule gets ~~strikethrough~~ and a pointer to its successor.
- The Rule column is what agents obey — write it so it can be followed
  without reading the rest of the row.
- Rules that outgrow one package are promoted to `CLAUDE.md` or
  `.claude/rules/`; the row then becomes a one-line pointer (no
  duplication, CLAUDE.md §6).

<!-- Example (not a real entry):
| 1 | 2026-07-20 | P1 / internal/store | migration test failed on re-run | migrations were not idempotent against an existing DB | Always test migrations against both an empty DB and an already-migrated DB |
-->

| # | Date | Context | What failed | Root cause | Rule |
|---|---|---|---|---|---|
