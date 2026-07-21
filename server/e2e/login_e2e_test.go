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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"
)

// fakeRedmine は E2E に必要な上流エンドポイントだけを返す:
//   - /my/account.json … ブートストラップ（BasicAuth → api_key を返す）
//   - /projects.json    … 集約 API（X-Redmine-Api-Key 検証、親子ツリー）
// subURI="" のため、上流パスにサブ URI は付かない。未対応パスは 404 にして
// 経路の取り違えを検知する。
func fakeRedmine(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			// プロジェクト別 未完了件数（CountOpenIssues。project_id で件数を返す）。
			if r.Header.Get("X-Redmine-Api-Key") != "e2e-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			counts := map[string]int{"1": 12, "2": 5, "3": 3, "4": 8}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"issues":[],"total_count":%d,"offset":0,"limit":1}`,
				counts[r.URL.Query().Get("project_id")])
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
