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
| 2 | 2026-07-20 | P5 / app/js | `node --test app/js/tests/` reported 1 failing "test" (Cannot find module .../tests) | node v22 treats a bare directory arg to `--test` as an entrypoint to run, not a discovery root | Invoke the Node test runner with a file glob (`node --test app/js/tests/*.test.js`), never a bare directory |
| 3 | 2026-07-21 | P6 / app/js app.js | projects E2E timed out; two `<section data-screen="projects">` existed and `querySelector('#projectsTree')` matched the empty one | `loadFragment` cached the resolved element, so two concurrent `route()` calls both saw a cache miss (fetch still pending) and each appended a section | Cache the in-flight Promise, not the resolved value, in any async id-keyed cache; and never fire a code path that both mutates `location.hash` and directly calls the `hashchange` handler |
| 4 | 2026-07-21 | P6 / e2e | assertions read the wrong screen's DOM when multiple `.screen` sections exist | non-active screens stay in the DOM (hidden via CSS), so a bare `#id` selector can match a stale duplicate | In screen E2E, scope selectors to the active screen (`.screen.active #id`) |
| 5 | 2026-07-22 | P6 / app.js common/modal.js | opening `#modal-issue-create/1` silently loaded the projects screen instead of the modal; e2e hung until the outer context timeout | `isModalHash`'s regex (`^#modal-[a-z0-9-]+$`) rejected any `/` after the key, so a hash carrying a route param (e.g. a project id) failed the modal check and fell through to normal screen routing, which doesn't recognize `modal-issue-create` as a screen key and defaults to `SCREENS[0]` | Any hash-route matcher that must also support `/<param>` segments needs that in its regex from the start (`(\/.*)?$`, not `$`); add a pure-function unit test for the matcher itself, not just the screens it gates |
| 6 | 2026-07-22 | P6 / e2e settings relink | clicking `#relinkSubmit` produced no network request and the screen stayed on "要再連携" until the poll timed out | a still-visible toast from an earlier step (4s auto-dismiss timer, `#toasts` is `position:fixed`) sat on top of the submit button's screen coordinates; chromedp's click dispatches at the element's computed center regardless of what's actually painted there, so the click landed on the toast instead | When a chromedp click follows shortly after any `toast(...)` call in the same test, clear `#toasts .toast` (or wait out the 4s TTL) immediately before the click — don't assume `WaitVisible` on the target proves it's actually hit-testable |
