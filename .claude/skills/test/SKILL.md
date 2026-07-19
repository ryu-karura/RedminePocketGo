---
name: test
description: Which test suites to run for each kind of change in RedminePocketGo, and the failure-to-lesson loop that turns every unexpected test failure into a recorded prevention rule in LESSONS.md. Use when writing or running tests, and immediately after any test fails unexpectedly.
---

# Testing rules

## Which suite for which change

| Change touches | Run | Notes |
|---|---|---|
| `server/internal/httpapi` (handlers, middleware) | `make test-api` + `make test-unit` | Table-driven: success / unauthenticated / malformed / upstream failure (CLAUDE.md §4.7) |
| Any other `server/` package | `make test-unit` | Redmine client only against `httptest.Server` |
| `app/js/common/tree.js`, `utils.js`, other pure JS modules | `node --test app/js/tests/` | Node's built-in runner only — no npm packages, ever |
| `app/` screens, CSS, HTML | `node --test` if logic changed, plus manual 4-state check (loading / empty / error / populated) served by the Go server | Record what was manually verified in the commit body |
| `scripts/*.sh` | `shellcheck scripts/*.sh` | Before every commit touching shell |
| Phase 5–6 browser flows (login, screen 4-states) | `make test-e2e` (`server/e2e/`, build tag `e2e`) | chromedp + bundled Chromium (`/opt/pw-browsers/chromium`); passkeys via CDP WebAuthn virtual authenticator; screenshots kept as evidence. Replaces the manual browser checks in unattended runs; phase 5+ |
| Cross-cutting / relay behaviour against real Redmine | `scripts/test-stack.sh` | Requires a running RedmineDocker dev stack; phase 8+ |
| Every push / PR (independent verification) | GitHub Actions `.github/workflows/ci.yml` | Runs `make build` / `test-unit` / `test-api`, shellcheck, and `node --test` automatically. Green CI — not the agent's own claim — is what counts as verified in unattended runs |

Test-first is mandatory (CLAUDE.md §9-5): write the failing test, watch it
fail, then implement. A test that never failed proves nothing.

## Failure classification

- **Expected red**: the failing test you just wrote in the TDD cycle.
  Normal; not a lesson.
- **Unexpected failure**: a test that should have passed fails — a
  regression, a wrong assumption about Redmine/WebAuthn/SQLite behaviour, a
  misread convention, an environment gotcha, a flaky test. These trigger
  the lesson loop below.

## The lesson loop (failure → prevention rule)

Every unexpected failure must leave the repo better armed against its
recurrence:

1. **Fix the failure first.** Diagnose the root cause — not the symptom.
2. **Ask: could a written rule have prevented this?** If the cause was a
   one-off typo, no lesson is needed. If it was a wrong assumption, a
   misunderstood convention, an API surprise, or a repeated mistake —
   record it.
3. **Append an entry to `.claude/skills/test/LESSONS.md`** using its
   format, in the same commit as the fix. Write the rule as an imperative
   an agent can obey ("always X", "never Y before Z"), not as a story.
4. **Promote if general.** If the rule applies beyond one package or
   screen, move its substance into `CLAUDE.md` or `.claude/rules/` in the
   same commit and keep the LESSONS.md row as a one-line pointer. No
   duplication (CLAUDE.md §6).
5. **Add a regression test** pinning the failure whenever the failure mode
   is testable.

Reading LESSONS.md before starting any task is mandatory (enforced by the
`implement` skill). A recorded rule that gets violated again means the rule
was written unclearly — rewrite it, don't just re-record it.
