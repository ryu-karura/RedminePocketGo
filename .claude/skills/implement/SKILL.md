---
name: implement
description: Plan-driven staged implementation workflow for RedminePocketGo. Use BEFORE implementing any feature, endpoint, screen, module, or script in this repo — it dictates which task may be worked on next, and how progress is recorded in docs/plan.md.
---

# Staged implementation workflow

This repo is implemented incrementally against `docs/plan.md`. Never build
everything at once; never work ahead of the plan.

## The plan file

`docs/plan.md` is the single source of truth for implementation order and
progress. It contains numbered phases, each with checkbox tasks and explicit
completion criteria. Exactly one phase may be 進行中 (in progress) at a time.

## Workflow for every task

1. **Read `docs/plan.md`.** Identify the phase marked 進行中. If none is,
   promote the first 未着手 phase to 進行中 (this edit goes in the same
   commit as the first task of that phase).
2. **Read `.claude/skills/test/LESSONS.md`** and apply every rule recorded
   there. This is mandatory before writing any code.
3. **Pick the topmost unchecked task in the current phase.** Do not pick
   tasks from later phases, and do not batch several tasks into one change
   unless they are inseparable (say so in the commit body if they are).
4. **Write a failing test first** (CLAUDE.md §9-5). Consult the `test`
   skill for which suite the test belongs to.
5. **Implement the minimum** that makes the test pass, following CLAUDE.md
   and Design.md. If the task turns out to hide more work, split it: add the
   new task lines to `docs/plan.md` first, then continue with the reduced
   task.
6. **Run the test suites** the `test` skill maps to your change. All green
   before commit.
7. **Commit atomically.** The same commit contains: the implementation, its
   tests, the checked `[x]` box in `docs/plan.md`, and any doc updates
   required by CLAUDE.md §6. Conventional Commits, scopes
   `app` / `server` / `scripts` / `docs`.

## Phase transitions

- A phase is 完了 only when every task is checked AND its 完了条件
  (completion criteria) are verifiably met — run them, don't assume.
- Flip the phase status in the フェーズ一覧 table in the same commit as the
  last task, then promote the next phase when work on it starts.
- The 地図表示 (map) row stays untouched until the user explicitly asks.

## Changing the plan

The plan may be amended (new tasks, splits, reordering) but never silently:
every amendment adds one row to the 変更履歴 table (date, change, reason)
in the same commit. Removing an unchecked task requires a reason there too.

## Prohibitions

- No implementation work without a corresponding unchecked task in the
  current phase — add the task first if it is genuinely missing.
- No checking a box while any mapped test suite is red.
- No skipping the plan.md update "to be done later"; a commit that
  implements a task but doesn't check it is incomplete.
- No starting a later phase while the current one has unchecked tasks,
  unless the user explicitly reorders the plan.
