//go:build e2e

// Package e2e はブラウザ実機（chromedp + 同梱 Chromium）でパスキーの
// 登録・ログインを自動検証する。無人実行では手動ブラウザ確認の代わりに
// これを回す（plan.md フェーズ 5・6 完了条件）。npm 依存なし。
//
// 実行: make test-e2e（build tag e2e）。Chromium は環境同梱の
// /opt/pw-browsers/chromium-*/chrome-linux/chrome を使う。
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"
)

// fakeRedmine は E2E に必要な上流エンドポイントだけを返す:
//   - /my/account.json … ブートストラップ（BasicAuth → api_key を返す）
//   - /projects.json    … 集約 API（X-Redmine-Api-Key 検証、親子ツリー）
//
// subURI="" のため、上流パスにサブ URI は付かない。未対応パスは 404 にして
// 経路の取り違えを検知する。
func fakeRedmine(t *testing.T) *httptest.Server {
	t.Helper()
	// チケット 101 の状態はインライン編集で書き換わる（PUT を保持し GET に反映）。
	var mu sync.Mutex
	statusID, statusName := 2, "進行中"
	names := map[int]string{1: "新規", 2: "進行中", 5: "完了"}
	// 作成モーダルからの POST /issues.json は新規チケットとして保持し、
	// 続く GET /issues/{id}.json（詳細画面への遷移）で読み返せるようにする。
	type createdIssue struct {
		Subject, Description, TrackerName, PriorityName string
		TrackerID, PriorityID                           int
	}
	nextID := 999
	created := map[int]createdIssue{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 個別チケット（詳細取得 / インライン更新）。/issues/{id}.json。
		if strings.HasPrefix(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, ".json") {
			if r.Header.Get("X-Redmine-Api-Key") != "e2e-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodPut {
				var body struct {
					Issue struct {
						StatusID int `json:"status_id"`
					} `json:"issue"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Issue.StatusID != 0 {
					mu.Lock()
					statusID = body.Issue.StatusID
					statusName = names[body.Issue.StatusID]
					mu.Unlock()
				}
				w.WriteHeader(http.StatusOK) // Redmine は 204 だが 2xx を透過
				return
			}
			var id int
			_, _ = fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/issues/"), ".json"), "%d", &id)
			if id != 101 {
				mu.Lock()
				ci, ok := created[id]
				mu.Unlock()
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"issue":{"id":%d,"subject":%q,"description":%q,`+
					`"status":{"id":1,"name":"新規"},"priority":{"id":%d,"name":%q},`+
					`"tracker":{"id":%d,"name":%q},"journals":[],"attachments":[]}}`,
					id, ci.Subject, ci.Description, ci.PriorityID, ci.PriorityName, ci.TrackerID, ci.TrackerName)
				return
			}
			mu.Lock()
			sid, sname := statusID, statusName
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"issue":{"id":101,"subject":"帳票出力の刷新","description":"帳票まわりの刷新対応",`+
				`"status":{"id":%d,"name":%q},"priority":{"id":6,"name":"高"},"tracker":{"id":1,"name":"バグ"},`+
				`"assigned_to":{"id":7,"name":"山田"},"due_date":"2026-08-15","done_ratio":60,`+
				`"journals":[{"id":1,"notes":"初回の記録","user":{"id":7,"name":"山田"},"created_on":"2026-07-01T10:00:00Z"}],`+
				`"attachments":[]}}`, sid, sname)
			return
		}
		switch r.URL.Path {
		case "/my/account.json":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "alice" || pass != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"user":{"login":"alice","firstname":"Alice","lastname":"Doe","api_key":"e2e-key"}}`)
		case "/projects.json":
			if r.Header.Get("X-Redmine-Api-Key") != "e2e-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// 会計・在庫は 基幹システム の子。社内インフラ はルート。
			fmt.Fprint(w, `{"projects":[`+
				`{"id":1,"name":"基幹システム","identifier":"kikan"},`+
				`{"id":2,"name":"会計モジュール","identifier":"kaikei","parent":{"id":1}},`+
				`{"id":3,"name":"在庫モジュール","identifier":"zaiko","parent":{"id":1}},`+
				`{"id":4,"name":"社内インフラ","identifier":"infra"}`+
				`],"total_count":4,"offset":0,"limit":100}`)
		case "/issues.json":
			if r.Header.Get("X-Redmine-Api-Key") != "e2e-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodPost {
				var body struct {
					Issue struct {
						Subject     string `json:"subject"`
						Description string `json:"description"`
						TrackerID   int    `json:"tracker_id"`
						PriorityID  int    `json:"priority_id"`
					} `json:"issue"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				trackerNames := map[int]string{1: "バグ"}
				priorityNames := map[int]string{3: "低", 4: "通常", 6: "高"}
				mu.Lock()
				id := nextID
				nextID++
				created[id] = createdIssue{
					Subject: body.Issue.Subject, Description: body.Issue.Description,
					TrackerID: body.Issue.TrackerID, TrackerName: trackerNames[body.Issue.TrackerID],
					PriorityID: body.Issue.PriorityID, PriorityName: priorityNames[body.Issue.PriorityID],
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				fmt.Fprintf(w, `{"issue":{"id":%d,"subject":%q}}`, id, body.Issue.Subject)
				return
			}
			q := r.URL.Query()
			w.Header().Set("Content-Type", "application/json")
			// 件数クエリ（CountOpenIssues）: status_id=open & limit=1。
			if q.Get("status_id") == "open" && q.Get("limit") == "1" {
				counts := map[string]int{"1": 12, "2": 5, "3": 3, "4": 8}
				fmt.Fprintf(w, `{"issues":[],"total_count":%d,"offset":0,"limit":1}`,
					counts[q.Get("project_id")])
				return
			}
			// ツリークエリ（status_id=*）: プロジェクト 1 のチケットを返す
			//（親子 + 未完了/完了混在）。他プロジェクトは空。
			if q.Get("project_id") == "1" {
				fmt.Fprint(w, `{"issues":[`+
					`{"id":101,"subject":"帳票出力の刷新","status":{"id":2,"name":"進行中"},"priority":{"id":6,"name":"高"},"assigned_to":{"id":7,"name":"山田"}},`+
					`{"id":102,"subject":"PDF 出力","parent":{"id":101},"status":{"id":1,"name":"新規"},"priority":{"id":4,"name":"通常"}},`+
					`{"id":103,"subject":"締め処理の高速化","status":{"id":5,"name":"完了"},"priority":{"id":4,"name":"通常"}}`+
					`],"total_count":3,"offset":0,"limit":100}`)
				return
			}
			fmt.Fprint(w, `{"issues":[],"total_count":0,"offset":0,"limit":100}`)
		case "/trackers.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"trackers":[{"id":1,"name":"バグ"}]}`)
		case "/issue_statuses.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"issue_statuses":[`+
				`{"id":1,"name":"新規","is_closed":false},`+
				`{"id":2,"name":"進行中","is_closed":false},`+
				`{"id":5,"name":"完了","is_closed":true}]}`)
		case "/enumerations/issue_priorities.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"issue_priorities":[`+
				`{"id":3,"name":"低"},{"id":4,"name":"通常"},{"id":6,"name":"高"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func chromePath(t *testing.T) string {
	for _, g := range []string{
		"/opt/pw-browsers/chromium-*/chrome-linux/chrome",
		"/opt/pw-browsers/chromium_headless_shell-*/chrome-linux/headless_shell",
	} {
		if m, _ := filepath.Glob(g); len(m) > 0 {
			return m[0]
		}
	}
	t.Skip("bundled Chromium not found under /opt/pw-browsers; skipping e2e")
	return ""
}

func TestLoginBootstrapRegisterFlow(t *testing.T) {
	redmine := fakeRedmine(t)

	// rmapp をポート 18099 で起動（app/ を配信）。RP ID は localhost。
	srv := startRmapp(t, redmine.URL)
	defer srv.stop()

	execAlloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromePath(t)),
			chromedp.Flag("headless", true),
			chromedp.Flag("no-sandbox", true),
		)...)
	defer cancelAlloc()

	ctx, cancel := chromedp.NewContext(execAlloc)
	defer cancel()
	// 初回はブラウザの起動・初期化に時間がかかるため余裕を持たせる。
	ctx, cancelT := context.WithTimeout(ctx, 150*time.Second)
	defer cancelT()

	// ブラウザのコンソール・例外をテストログへ流す（診断用）。
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			parts := ""
			for _, a := range e.Args {
				parts += " " + string(a.Value)
			}
			t.Logf("console.%s:%s", e.Type, parts)
		case *runtime.EventExceptionThrown:
			if e.ExceptionDetails != nil {
				t.Logf("page exception: %s", e.ExceptionDetails.Text)
			}
		}
	})

	// CDP WebAuthn 仮想認証器: Discoverable(resident) + UV 成功を即時シミュレート。
	var authID webauthn.AuthenticatorID
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := webauthn.Enable().Do(ctx); err != nil {
				return err
			}
			id, err := webauthn.AddVirtualAuthenticator(&webauthn.VirtualAuthenticatorOptions{
				Protocol:                    webauthn.AuthenticatorProtocolCtap2,
				Transport:                   webauthn.AuthenticatorTransportInternal,
				HasResidentKey:              true,
				HasUserVerification:         true,
				AutomaticPresenceSimulation: true,
				IsUserVerified:              true,
			}).Do(ctx)
			if err != nil {
				return err
			}
			authID = id
			return nil
		}),
	); err != nil {
		t.Fatalf("virtual authenticator setup: %v", err)
	}
	_ = authID

	base := "http://localhost:18099"
	shot := func(name string) chromedp.Action {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			var buf []byte
			if err := chromedp.FullScreenshot(&buf, 90).Do(ctx); err != nil {
				return err
			}
			dir := os.Getenv("E2E_ARTIFACT_DIR")
			if dir == "" {
				dir = t.TempDir()
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, buf, 0o644); err != nil {
				return err
			}
			t.Logf("screenshot: %s", path)
			return nil
		})
	}

	// ブートストラップ経路でユーザー作成 + パスキー登録 → セッション発行。
	var meText string
	err := chromedp.Run(ctx,
		chromedp.Navigate(base),
		chromedp.WaitVisible(`#bootstrapLink`, chromedp.ByID),
		shot("01-login.png"),
		chromedp.Click(`#bootstrapLink`, chromedp.ByID),
		chromedp.WaitVisible(`#bsLogin`, chromedp.ByID),
		chromedp.SendKeys(`#bsLogin`, "alice", chromedp.ByID),
		chromedp.SendKeys(`#bsPass`, "secret", chromedp.ByID),
		chromedp.Click(`#bootstrapForm button[type=submit]`, chromedp.ByQuery),
		// 登録完了でオーバーレイが閉じ、ドロワーに画面リンクが出る。
		waitDrawerOrDump(t, 25*time.Second),
		shot("02-authenticated.png"),
		// /api/auth/me が認証済みを返すことを確認（Promise を待つ）。
		chromedp.Evaluate(`fetch('/api/auth/me').then(r=>r.text())`, &meText,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			}),
	)
	if err != nil {
		t.Fatalf("bootstrap+register flow: %v", err)
	}
	if !containsAll(meText, `"userId"`, `alice`) {
		t.Fatalf("/api/auth/me after register did not show the user: %s", meText)
	}

	// 一旦ログアウトして、パスキー単独でのログインも通ることを確認。
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`window.rmappLogout && window.rmappLogout()`, nil),
		chromedp.WaitVisible(`#passkeyBtn`, chromedp.ByID),
		chromedp.Click(`#passkeyBtn`, chromedp.ByID),
		chromedp.WaitVisible(`.drawer__link`, chromedp.ByQuery),
		shot("03-passkey-login.png"),
	)
	if err != nil {
		t.Fatalf("passkey login flow: %v", err)
	}

	// プロジェクト一覧が集約 API から dataTree で描画され、検索で絞り込めること
	// を確認する（populated → 検索で filtered、子が見える＝祖先が自動展開）。
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#projects"),
		// populated: ルートのプロジェクト名と未完了件数が出るまで待つ
		//（基幹システム=12 / 社内インフラ=8。アクティブ画面に限定）。
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active #projectsTree');`+
				`if(!t)return false;var s=t.innerText;`+
				`return s.indexOf('基幹システム')>=0 && s.indexOf('社内インフラ')>=0 `+
				`&& s.indexOf('12')>=0 && s.indexOf('8')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("04-projects.png"),
		// 検索で「会計」に絞り込む: 子の会計モジュールが見え（祖先が自動展開）、
		// 一致しない社内インフラは消える。
		chromedp.SendKeys(`.screen.active #projectSearch`, "会計", chromedp.ByQuery),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active #projectsTree');if(!t)return false;`+
				`var s=t.innerText;return s.indexOf('会計モジュール')>=0 `+
				`&& s.indexOf('社内インフラ')<0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("05-projects-search.png"),
	)
	if err != nil {
		t.Fatalf("projects screen flow: %v", err)
	}

	// チケット一覧: 集約 API + メタからバッジ付き 2 段組で描画され、完了は既定で
	// 畳まれ、状態フィルタで表示できることを確認する。
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#issues/1"),
		// populated: 未完了チケットと状態バッジが出る。完了（締め処理）は非表示。
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active #issuesTree');if(!t)return false;`+
				`var s=t.innerText;return s.indexOf('帳票出力の刷新')>=0 && s.indexOf('進行中')>=0 `+
				`&& s.indexOf('締め処理')<0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("06-issues.png"),
		// 状態フィルタを「すべて」にすると完了チケットも表示される。
		chromedp.Evaluate(
			`(function(){var s=document.querySelector('.screen.active #issueStatusFilter');`+
				`s.value='';s.dispatchEvent(new Event('change'));})()`, nil),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active #issuesTree');`+
				`return !!t && t.innerText.indexOf('締め処理')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("07-issues-all.png"),
	)
	if err != nil {
		t.Fatalf("issues screen flow: %v", err)
	}

	// チケット詳細: 詳細が描画され、状態のインライン編集が PUT され再取得に反映
	// される（変更項目のみ送信 → fake が保持 → バッジが更新）。
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#issue-detail/101"),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active .issue-detail');if(!t)return false;`+
				`var s=t.innerText;return s.indexOf('帳票出力の刷新')>=0 && s.indexOf('初回の記録')>=0 `+
				`&& s.indexOf('進行中')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("08-issue-detail.png"),
		// 状態を「完了」(id=5) に変更 → PUT → 再取得でバッジが「完了」になる。
		chromedp.Evaluate(
			`(function(){var s=document.querySelector('.screen.active #editStatus');`+
				`s.value='5';s.dispatchEvent(new Event('change'));})()`, nil),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active .issue-detail .badge.status-closed');`+
				`return !!t && t.innerText.indexOf('完了')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("09-issue-detail-edited.png"),
	)
	if err != nil {
		t.Fatalf("issue detail flow: %v", err)
	}

	// チケット作成モーダル: 一覧の FAB から開き、件名を入力して作成すると
	// 新しいチケットの詳細（#issue-detail/999）へ遷移することを確認する。
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#issues/1"),
		chromedp.WaitVisible(`.screen.active #issueCreateFab`, chromedp.ByQuery),
		chromedp.Click(`.screen.active #issueCreateFab`, chromedp.ByQuery),
		chromedp.WaitVisible(`#issueCreateForm`, chromedp.ByQuery),
		shot("10-issue-create-modal.png"),
		chromedp.SendKeys(`#createSubject`, "新規チケットE2E", chromedp.ByQuery),
		chromedp.Click(`#issueCreateSubmit`, chromedp.ByQuery),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active .issue-detail');if(!t)return false;`+
				`var s=t.innerText;return s.indexOf('新規チケットE2E')>=0 && s.indexOf('#999')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("11-issue-created.png"),
	)
	if err != nil {
		t.Fatalf("issue create modal flow: %v", err)
	}
}

// waitDrawerOrDump は .drawer__link の出現を待ち、時間切れならログイン
// パネルの中身（インラインエラー等）をログに出して失敗を分かりやすくする。
func waitDrawerOrDump(t *testing.T, d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		wctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		err := chromedp.WaitVisible(`.drawer__link`, chromedp.ByQuery).Do(wctx)
		if err != nil {
			var html string
			_ = chromedp.OuterHTML(`#loginOverlay`, &html, chromedp.ByID).Do(ctx)
			t.Logf("login overlay at timeout:\n%s", html)
		}
		return err
	})
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
