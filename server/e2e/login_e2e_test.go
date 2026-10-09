//go:build e2e

// Package e2e はブラウザ実機（chromedp + 同梱 Chromium）で OAuth ログインと
// 各画面を自動検証する。無人実行では手動ブラウザ確認の代わりに
// これを回す（plan.md フェーズ 5・6 完了条件）。npm 依存なし。
//
// 実行: make test-e2e（build tag e2e）。Chromium は環境同梱の
// /opt/pw-browsers/chromium-*/chrome-linux/chrome を使う。
package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// upstreamState は擬似 Redmine の可変状態。OAuth 2.0 の提供側（認可・トークン・
// 失効・users/current）を最小限で再現し、テストが「トークン期限切れ」「利用者に
// よる取り消し」「同意拒否」を切り替えられるようにする。
//
// 実機（Redmine 7.0.2）で確認した挙動（Design.md §14）に合わせる: PKCE の検証、
// 認可コードの 1 回限り、リフレッシュでのリフレッシュトークンの入れ替えと旧トークンの
// 即時失効、失効後は 401。Redmine の API キーは存在しない — 受け取ったら記録して
// テスト末尾で失敗させる。
type upstreamState struct {
	mu sync.Mutex

	seq          int
	codes        map[string]string // 認可コード → code_challenge
	access       map[string]bool   // 有効なアクセストークン
	refresh      map[string]bool   // 有効なリフレッシュトークン
	denyNext     bool              // true なら次の認可要求を access_denied で返す
	refreshCalls int
	revokeCalls  int
	apiKeySeen   int // X-Redmine-Api-Key を受け取った回数（0 でなければならない）
}

const (
	e2eClientID     = "e2e-client"
	e2eClientSecret = "e2e-dummy-client-secret"
	e2eRedirectURI  = "http://localhost:18099/api/auth/callback"
)

func newUpstreamState() *upstreamState {
	return &upstreamState{codes: map[string]string{}, access: map[string]bool{}, refresh: map[string]bool{}}
}

func (s *upstreamState) setDenyNext(v bool) { s.mu.Lock(); s.denyNext = v; s.mu.Unlock() }

// expireAccess は発行済みのアクセストークンだけを失効させる（リフレッシュは有効）。
// 次の API 呼び出しは 401 → 黙ってリフレッシュ → 再試行で成功するはず。
func (s *upstreamState) expireAccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = map[string]bool{}
}

// revokeAll は利用者が Redmine で取り消した状況: アクセスもリフレッシュも無効。
func (s *upstreamState) revokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = map[string]bool{}
	s.refresh = map[string]bool{}
}

func (s *upstreamState) counters() (refreshCalls, revokeCalls, apiKeySeen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshCalls, s.revokeCalls, s.apiKeySeen
}

// authorized は Bearer が有効なアクセストークンか。API キーのヘッダーは記録する。
func (s *upstreamState) authorized(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("X-Redmine-Api-Key") != "" {
		s.apiKeySeen++
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok != "" && s.access[tok]
}

func (s *upstreamState) clientOK(r *http.Request) bool {
	return r.PostForm.Get("client_id") == e2eClientID && r.PostForm.Get("client_secret") == e2eClientSecret
}

func jsonErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}

// handleOAuth は OAuth 関連のパスを処理し、処理したら true を返す。
// 実際の Redmine はここで利用者のログインと同意の画面を出すが、E2E は「Redmine に
// ログイン済みで同意済み」の状況として、認可要求を即座に承認して戻す。
func (s *upstreamState) handleOAuth(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/oauth/authorize":
		q := r.URL.Query()
		if q.Get("client_id") != e2eClientID || q.Get("redirect_uri") != e2eRedirectURI ||
			q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
			q.Get("code_challenge") == "" || q.Get("state") == "" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return true
		}
		s.mu.Lock()
		deny := s.denyNext
		s.denyNext = false
		s.seq++
		code := fmt.Sprintf("CODE-%d", s.seq)
		if !deny {
			s.codes[code] = q.Get("code_challenge")
		}
		s.mu.Unlock()
		u, _ := url.Parse(e2eRedirectURI)
		v := u.Query()
		v.Set("state", q.Get("state"))
		if deny {
			v.Set("error", "access_denied")
		} else {
			v.Set("code", code)
		}
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
		return true

	case "/oauth/token":
		_ = r.ParseForm()
		if !s.clientOK(r) {
			jsonErr(w, http.StatusUnauthorized, "invalid_client")
			return true
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			code := r.PostForm.Get("code")
			challenge, ok := s.codes[code]
			delete(s.codes, code) // 1 回限り
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if !ok || r.PostForm.Get("redirect_uri") != e2eRedirectURI ||
				base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				jsonErr(w, http.StatusBadRequest, "invalid_grant")
				return true
			}
		case "refresh_token":
			rt := r.PostForm.Get("refresh_token")
			if !s.refresh[rt] {
				jsonErr(w, http.StatusBadRequest, "invalid_grant")
				return true
			}
			delete(s.refresh, rt) // 入れ替わり: 旧リフレッシュトークンは即失効
			s.refreshCalls++
		default:
			jsonErr(w, http.StatusBadRequest, "unsupported_grant_type")
			return true
		}
		s.seq++
		at, rt := fmt.Sprintf("AT-%d", s.seq), fmt.Sprintf("RT-%d", s.seq)
		s.access[at], s.refresh[rt] = true, true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":7200,"refresh_token":%q,`+
			`"scope":"view_project view_issues add_issues edit_issues add_issue_notes view_members","created_at":%d}`,
			at, rt, time.Now().Unix())
		return true

	case "/oauth/revoke":
		_ = r.ParseForm()
		if !s.clientOK(r) {
			jsonErr(w, http.StatusUnauthorized, "invalid_client")
			return true
		}
		s.mu.Lock()
		tok := r.PostForm.Get("token")
		s.revokeCalls++
		if s.refresh[tok] {
			// Doorkeeper ではリフレッシュトークンの失効はトークンの組ごとの失効。
			s.access = map[string]bool{}
		}
		delete(s.refresh, tok)
		delete(s.access, tok)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
		return true

	case "/users/current.json":
		if !s.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"user":{"id":5,"login":"alice","firstname":"Alice","lastname":"Doe"}}`)
		return true
	}
	return false
}

func fakeRedmine(t *testing.T) (*httptest.Server, *upstreamState) {
	t.Helper()
	state := newUpstreamState()
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
		if state.handleOAuth(w, r) {
			return
		}
		// 個別チケット（詳細取得 / インライン更新）。/issues/{id}.json。
		if strings.HasPrefix(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, ".json") {
			if !state.authorized(r) {
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
				`"attachments":[],`+
				// カスタムフィールド（表示検証用に主要フォーマットを一通り含める。
				// text/list/bool/link/date。plan.md フェーズ 9）。
				`"custom_fields":[`+
				`{"id":10,"name":"備考","value":"1行目\n2行目"},`+
				`{"id":11,"name":"優先タグ","value":"a"},`+
				`{"id":12,"name":"承認済み","value":"1"},`+
				`{"id":13,"name":"参考リンク","value":"https://example.com/spec"},`+
				`{"id":14,"name":"対応期限メモ","value":"2026-09-01"}`+
				`]}}`, sid, sname)
			return
		}
		switch r.URL.Path {
		case "/projects.json":
			if !state.authorized(r) {
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
			if !state.authorized(r) {
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
			if !state.authorized(r) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"trackers":[{"id":1,"name":"バグ"}]}`)
		case "/issue_statuses.json":
			if !state.authorized(r) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"issue_statuses":[`+
				`{"id":1,"name":"新規","is_closed":false},`+
				`{"id":2,"name":"進行中","is_closed":false},`+
				`{"id":5,"name":"完了","is_closed":true}]}`)
		case "/enumerations/issue_priorities.json":
			if !state.authorized(r) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"issue_priorities":[`+
				`{"id":3,"name":"低"},{"id":4,"name":"通常"},{"id":6,"name":"高"}]}`)
		case "/custom_fields.json":
			if !state.authorized(r) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"custom_fields":[`+
				`{"id":10,"name":"備考","customized_type":"issue","field_format":"text","is_required":false},`+
				`{"id":11,"name":"優先タグ","customized_type":"issue","field_format":"list","is_required":true,`+
				`"possible_values":[{"value":"a","label":"重要"},{"value":"b","label":"通常"}]},`+
				`{"id":12,"name":"承認済み","customized_type":"issue","field_format":"bool"},`+
				`{"id":13,"name":"参考リンク","customized_type":"issue","field_format":"link"},`+
				`{"id":14,"name":"対応期限メモ","customized_type":"issue","field_format":"date"}`+
				`]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, state
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

func TestOAuthLoginAndScreens(t *testing.T) {
	redmine, upstream := fakeRedmine(t)

	// rmapp をポート 18099 で起動（app/ を配信）。
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

	// OAuth ログイン: ボタン → /api/auth/login → 擬似 Redmine の認可（即承認）→
	// /api/auth/callback → セッション発行 → アプリ画面。
	var meText string
	err := chromedp.Run(ctx,
		chromedp.Navigate(base),
		chromedp.WaitVisible(`#loginBtn`, chromedp.ByID),
		shot("01-login.png"),
		chromedp.Click(`#loginBtn`, chromedp.ByID),
		// ログイン完了でオーバーレイが閉じ、ドロワーに画面リンクが出る。
		waitDrawerOrDump(t, 25*time.Second),
		shot("02-authenticated.png"),
		// /api/auth/me が認証済みを返し、連携状態とスコープが載ることを確認する。
		chromedp.Evaluate(`fetch('/api/auth/me').then(r=>r.text())`, &meText,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			}),
	)
	if err != nil {
		t.Fatalf("oauth login flow: %v", err)
	}
	if !containsAll(meText, `"userId"`, `alice`, `"redmineStatus":"active"`, `view_issues`) {
		t.Fatalf("/api/auth/me after login did not show the user and grant: %s", meText)
	}
	for _, leak := range []string{"AT-", "RT-", "access_token", "refresh_token"} {
		if strings.Contains(meText, leak) {
			t.Fatalf("/api/auth/me leaks token material (%q): %s", leak, meText)
		}
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
		// ツリーのアクセシビリティ属性を実 DOM で確認する（CLAUDE.md §3.4）:
		// コンテナは role=tree（Tabulator が role=grid で上書きするのを戻す）、
		// ルート行は role=treeitem・aria-level=1・aria-expanded を持つ。
		chromedp.Poll(
			`(function(){var c=document.querySelector('.screen.active #projectsTree');`+
				`if(!c||c.getAttribute('role')!=='tree')return false;`+
				`var rows=c.querySelectorAll('.tabulator-row');`+
				`var root=null;rows.forEach(function(r){if(r.innerText.indexOf('基幹システム')>=0)root=r;});`+
				`return !!root && root.getAttribute('role')==='treeitem' `+
				`&& root.getAttribute('aria-level')==='1' && root.hasAttribute('aria-expanded');})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		// 検索で「会計」に絞り込む: 子の会計モジュールが見え（祖先が自動展開）、
		// 一致しない社内インフラは消える。
		chromedp.SendKeys(`.screen.active #projectSearch`, "会計", chromedp.ByQuery),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active #projectsTree');if(!t)return false;`+
				`var s=t.innerText;return s.indexOf('会計モジュール')>=0 `+
				`&& s.indexOf('社内インフラ')<0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("05-projects-search.png"),
		// 自動展開で見えている子行（会計モジュール）の階層が aria-level=2 になる
		// ことを確認する。
		chromedp.Poll(
			`(function(){var c=document.querySelector('.screen.active #projectsTree');if(!c)return false;`+
				`var rows=c.querySelectorAll('.tabulator-row');var child=null;`+
				`rows.forEach(function(r){if(r.innerText.indexOf('会計モジュール')>=0)child=r;});`+
				`return !!child && child.getAttribute('role')==='treeitem' && child.getAttribute('aria-level')==='2';})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
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
		// empty: 該当チケットが 1 件もない優先度（低=id3）で絞り込むと、説明文
		// と「フィルタをクリア」導線が出る（Design.md §7.10）。
		chromedp.Evaluate(
			`(function(){var s=document.querySelector('.screen.active #issuePriorityFilter');`+
				`s.value='3';s.dispatchEvent(new Event('change'));})()`, nil),
		chromedp.WaitVisible(`.screen.active #issuesClearFilters`, chromedp.ByQuery),
		shot("07b-issues-empty.png"),
		chromedp.Click(`.screen.active #issuesClearFilters`, chromedp.ByQuery),
		chromedp.WaitVisible(`.screen.active #issuesTree`, chromedp.ByQuery),
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
		// カスタムフィールド（plan.md フェーズ 9）: 表示順どおりに描画され、
		// 長いテキストは改行保持、リストは選択肢ラベルに解決、真偽値は
		// 「はい」、日付・必須バッジが正しく出ることを確認する。
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active .custom-fields');if(!t)return false;`+
				`var s=t.innerText;return s.indexOf('備考')>=0 && s.indexOf('1行目')>=0 && s.indexOf('2行目')>=0 `+
				`&& s.indexOf('優先タグ')>=0 && s.indexOf('重要')>=0 && s.indexOf('必須')>=0 `+
				`&& s.indexOf('承認済み')>=0 && s.indexOf('はい')>=0 `+
				`&& s.indexOf('対応期限メモ')>=0 && s.indexOf('2026-09-01')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		// リンクフォーマットはクリック可能なアンカーになる。
		chromedp.Poll(
			`(function(){var a=document.querySelector('.screen.active .custom-fields a[href="https://example.com/spec"]');`+
				`return !!a && a.getAttribute('target')==='_blank';})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("08b-issue-detail-custom-fields.png"),
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

	// 設定画面: Redmine 連携の状態（連携済み）、付与スコープ、再認可ボタンが表示される。
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#settings"),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active');if(!t)return false;`+
				`var s=t.innerText;return s.indexOf('連携済み')>=0 && s.indexOf('view_issues')>=0 `+
				`&& !!t.querySelector('#reauthBtn') && s.indexOf('端末')<0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("12-settings.png"),
	)
	if err != nil {
		t.Fatalf("settings screen flow: %v", err)
	}

	// アクセストークンの期限切れ: 上流が 401 を返しても、サーバーが黙ってリフレッシュ
	// して再試行するので、利用者には何も見えない（再認可の案内は出ない）。
	upstream.expireAccess()
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#issue-detail/101"),
		chromedp.Poll(
			`(function(){var t=document.querySelector('.screen.active .issue-detail');`+
				`return !!t && t.innerText.indexOf('帳票出力の刷新')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("13-silent-refresh.png"),
	)
	if err != nil {
		t.Fatalf("silent refresh flow: %v", err)
	}
	if refreshes, _, _ := upstream.counters(); refreshes < 1 {
		t.Fatalf("refresh calls = %d; want >= 1 after the access token expired", refreshes)
	}
	var overlayActive bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`document.getElementById('loginOverlay').classList.contains('active')`, &overlayActive)); err != nil {
		t.Fatal(err)
	}
	if overlayActive {
		t.Fatal("a re-authorization prompt appeared although the refresh succeeded")
	}

	// 利用者が Redmine で許可を取り消した: リフレッシュも拒否される → 409
	// redmine_credential_invalid → アプリ全体で再認可の案内。再認可すると元の画面
	//（チケット詳細）へ戻り、再び表示できる（Design.md §4.4・§7.5）。
	// Redmine を呼ぶ画面（チケット詳細）で検出される。設定画面は me だけで
	// Redmine を呼ばないため、取り消しを検出するのは次に Redmine を呼ぶ時。
	upstream.revokeAll()
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#settings"),
		chromedp.Poll(`!!document.querySelector('.screen.active #reauthBtn')`, nil, chromedp.WithPollingTimeout(20*time.Second)),
		chromedp.Navigate(base+"/#issue-detail/101"),
		chromedp.Poll(`(function(){var o=document.getElementById('loginOverlay');return o.classList.contains('active') && !!o.querySelector('#reauthBtn');})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("14-reauthorize-prompt.png"),
	)
	if err != nil {
		t.Fatalf("reauthorize prompt: %v", err)
	}
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelectorAll('#toasts .toast').forEach(function(t){t.remove();})`, nil),
		chromedp.Evaluate(`window.__beforeReauth = true`, nil),
		chromedp.Click(`#loginOverlay #reauthBtn`, chromedp.ByQuery),
		// Redmine（擬似）の認可 → コールバック → 元のハッシュ（チケット詳細）へ戻り、
		// 直前まで失敗していた画面が表示できる。
		waitJS(30*time.Second,
			`(function(){if(window.__beforeReauth)return false;var t=document.querySelector('.screen.active .issue-detail');`+
				`return !!t && t.innerText.indexOf('帳票出力の刷新')>=0 && location.hash==='#issue-detail/101' `+
				`&& !document.getElementById('loginOverlay').classList.contains('active');})()`),
		shot("15-reauthorized-back-to-detail.png"),
		// 設定画面の連携状態も「連携済み」に戻っている。
		chromedp.Navigate(base+"/#settings"),
		chromedp.Poll(`(function(){var t=document.querySelector('.screen.active');return !!t && t.innerText.indexOf('連携済み')>=0;})()`,
			nil, chromedp.WithPollingTimeout(20*time.Second)),
		shot("16-settings-after-reauthorize.png"),
	)
	if err != nil {
		t.Fatalf("reauthorize flow: %v", err)
	}

	// 設定画面の「Redmine で再認可」ボタンからも同じフローを開始できる（再認可後は
	// #settings に戻る）。
	err = chromedp.Run(ctx,
		chromedp.Navigate(base+"/#settings"),
		chromedp.WaitVisible(`.screen.active #reauthBtn`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('#toasts .toast').forEach(function(t){t.remove();})`, nil),
		// 遷移前のページの目印。新しいページ（OAuth を一周して戻ってきた後）には無い。
		chromedp.Evaluate(`window.__beforeReauth = true`, nil),
		chromedp.Click(`.screen.active #reauthBtn`, chromedp.ByQuery),
		waitJS(30*time.Second,
			`(function(){if(window.__beforeReauth)return false;var t=document.querySelector('.screen.active');`+
				`return !!t && t.innerText.indexOf('連携済み')>=0 `+
				`&& location.hash==='#settings' && !document.getElementById('loginOverlay').classList.contains('active');})()`),
	)
	if err != nil {
		t.Fatalf("reauthorize from settings: %v", err)
	}

	// 同意の拒否: ログアウト後、Redmine 側で拒否されるとログイン画面に理由が出る
	//（セッションは発行されない）。もう一度ログインすれば入れる。
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`window.rmappLogout && window.rmappLogout()`, nil),
		chromedp.WaitVisible(`#loginBtn`, chromedp.ByID),
	)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	upstream.setDenyNext(true)
	var loginErrText string
	err = chromedp.Run(ctx,
		chromedp.Click(`#loginBtn`, chromedp.ByID),
		chromedp.WaitVisible(`#loginBtn`, chromedp.ByID),
		chromedp.Poll(`document.getElementById('loginError').innerText.length > 0`, nil, chromedp.WithPollingTimeout(20*time.Second)),
		chromedp.Text(`#loginError`, &loginErrText, chromedp.ByID),
		shot("17-login-denied.png"),
	)
	if err != nil {
		t.Fatalf("denied consent flow: %v", err)
	}
	if !strings.Contains(loginErrText, "許可") {
		t.Fatalf("denied-consent message = %q; want a message about the permission", loginErrText)
	}
	var hashAfter string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`location.hash`, &hashAfter))
	if strings.Contains(hashAfter, "error") {
		t.Fatalf("the error hash was not cleared: %q", hashAfter)
	}
	err = chromedp.Run(ctx,
		chromedp.Click(`#loginBtn`, chromedp.ByID),
		waitDrawerOrDump(t, 25*time.Second),
		shot("18-login-after-denied.png"),
	)
	if err != nil {
		t.Fatalf("login after a denied attempt: %v", err)
	}

	// ログアウトは最後の端末なので、Redmine 側のトークンも失効させる。
	_, revokesBefore, _ := upstream.counters()
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`window.rmappLogout && window.rmappLogout()`, nil),
		chromedp.WaitVisible(`#loginBtn`, chromedp.ByID),
	)
	if err != nil {
		t.Fatalf("final logout: %v", err)
	}
	if _, revokesAfter, _ := upstream.counters(); revokesAfter-revokesBefore < 2 {
		t.Errorf("revoke calls during logout = %d; want >= 2 (refresh + access token)", revokesAfter-revokesBefore)
	}

	// Redmine の API キーは一度も送られていない（CLAUDE.md §9-1）。
	if _, _, apiKeys := upstream.counters(); apiKeys != 0 {
		t.Errorf("upstream received X-Redmine-Api-Key %d times; want 0", apiKeys)
	}
}

// waitJS は式が true になるまで繰り返し評価する。ページ遷移の最中（OAuth の
// リダイレクトの連鎖）に評価が当たって「実行コンテキストが破棄された」エラー
// になるのは想定内なので、時間切れまで握りつぶして再試行する。
func waitJS(d time.Duration, expr string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(d)
		var last error
		for time.Now().Before(deadline) {
			var ok bool
			if err := chromedp.Evaluate(expr, &ok).Do(ctx); err == nil && ok {
				return nil
			} else {
				last = err
			}
			time.Sleep(150 * time.Millisecond)
		}
		return fmt.Errorf("waitJS timed out (%s): last error: %v", expr, last)
	})
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
