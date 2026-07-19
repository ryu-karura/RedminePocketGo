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
- **Quality gate**: after the last task of a phase is checked, run the
  `code-review` skill (medium effort) over the phase's diff and fix every
  CONFIRMED finding before flipping the phase status to 完了.
  `code-review` is provided by the Claude Code harness (a generally
  available skill, CLAUDE.md §7), not defined under `.claude/skills/`.
  If it is unavailable in the running environment, do an equivalent
  self-review pass over the phase diff and note that substitution in the
  自動実行ログ row.
- Flip the phase status in the フェーズ一覧 table in the same commit as the
  last task, then promote the next phase when work on it starts.
- The 地図表示 (map) row stays untouched until the user explicitly asks.

## Unattended runs（無人実行）

Scheduled-trigger runs (no human watching) follow every rule above, plus
the rules in this section.

### PR discovery comes first

1. **Find the open tracking PR before anything else**: list PRs in
   `ryu-karura/RedminePocketGo` and look for an open PR whose head branch
   matches `claude/*` (base `main`); if several, take the most recently
   updated. If one exists, work on its head branch, and process its review
   comments and CI failures BEFORE picking up new plan tasks.
2. **If the tracking PR is closed or merged, never reuse it or its
   branch**: start a fresh branch `claude/plan-phase-<N>` (N = current
   phase number) from the latest `origin/main`, and create a NEW pull
   request after the first push.
3. Keep the session subscribed to the tracking PR
   (`subscribe_pr_activity`) so review comments and CI failures are
   handled between scheduled runs, not only when the trigger fires.

### Safety rules

- **Run start**: `git fetch origin`, then fast-forward the working branch
  to its origin counterpart. If the branch has diverged, stop and report
  instead of working. Force-pushes and history rewrites are always
  forbidden.
- **Run end**: leave the worktree clean. Never commit a half-done task —
  discard it and record the reason as a row in plan.md's 変更履歴.
  Never push while any mapped test suite is red; CI
  (`.github/workflows/ci.yml`) is the independent check, not a substitute
  for running the suites locally first.
- One run implements at most up to the current phase's completion; do not
  roll into the next phase in the same run.

### Run log and stall detection

- Append one row to the 自動実行ログ table at the end of `docs/plan.md`
  after every scheduled run (UTC time / phase / tasks completed / commits
  / result), committed with the run's last change (or on its own if the
  run produced no implementation commits — a no-progress row is still
  recorded via a plan.md-only commit).
- **Stall detection**: if the log shows two consecutive runs stuck on the
  same task with zero new implementation commits, disable the scheduled
  trigger (`update_trigger`, enabled=false) and report the cause and how
  to resume.

### Visibility

- Rewrite the tracking PR's body so it carries the current phase-progress
  table (`update_pull_request`); do not add PR comments for routine
  progress.
- Finish every run with a PushNotification summary: tasks completed,
  phase status, next scheduled run, and any failures.

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
