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
| 7 | 2026-10-09 | P10 / e2e OAuth リダイレクト | OAuth を一周する操作（クリック → 認可 → コールバック → 戻り）の直後の `chromedp.Poll` が `Execution context was destroyed` で落ちた。待ち条件がクリック前の旧ページでも成立し、遷移が終わる前に次の手順（ログアウト）が走って、後から遷移が割り込んだ | リダイレクトの連鎖中は評価が遷移に当たってコンテキストが壊れる。また「連携済み」「ハッシュが #settings」など遷移前後で同じ見た目になる条件は、旧ページでも真になる | ページ遷移を伴う操作の後の待機は、(1) 操作前に `window.__marker = true` を置き、新しいページ（マーカー無し）になったことを条件に含める、(2) 評価エラーは握りつぶして再試行する `waitJS` を使う。素の `Poll` / `WaitVisible` を遷移の直後に置かない |
| 8 | 2026-10-09 | P10 / e2e 再認可の検出 | 取り消し（`revokeAll`）後に `#settings` を開いても再認可の案内が出ずタイムアウトした | 設定画面は `/api/auth/me`（DB の状態）しか呼ばず Redmine に触れない。取り消しは「次に Redmine を呼んだ時」にしか分からない | 「連携切れの検出」を試す画面は、実際に Redmine へ要求を出す画面（チケット詳細など）にする。上流を呼ばない画面で上流の状態変化を期待しない |
| 9 | 2026-10-09 | P10 / store マイグレーション | 設計時の見落とし（失敗前に防止）: `users` の作り直し（`DROP TABLE users`）を外部キー有効のまま行うと、`ON DELETE CASCADE` で `sessions` などの子の行が丸ごと消える（変異確認で再現） | SQLite の `DROP TABLE` は外部キー有効だと暗黙の `DELETE` を行い、CASCADE が走る | テーブルを作り直すマイグレーションは、先頭に `-- migrate:foreign-keys=off` を置き（`Store.applyMigration` が接続を固定して切り、コミット前に `foreign_key_check` を通す）、旧データが残ることをテストで確かめる（印を外すと落ちることまで確認する） |
