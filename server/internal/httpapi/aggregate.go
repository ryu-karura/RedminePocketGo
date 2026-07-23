package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

// ErrNoRedmineKey は利用者に有効な Redmine API キーが無い。
var ErrNoRedmineKey = errors.New("httpapi: redmine api key not available")

// Aggregator は Redmine クライアントが満たす（集約 API 用）。
type Aggregator interface {
	ListProjects(ctx context.Context, apiKey string) ([]redmine.Project, error)
	ListProjectIssues(ctx context.Context, apiKey string, projectID int) ([]redmine.Issue, error)
	GetIssue(ctx context.Context, apiKey string, id int) (*redmine.Issue, error)
	CountOpenIssues(ctx context.Context, apiKey string, projectID int) (int, error)
	ListTrackers(ctx context.Context, apiKey string) ([]redmine.Ref, error)
	ListStatuses(ctx context.Context, apiKey string) ([]redmine.Status, error)
	ListPriorities(ctx context.Context, apiKey string) ([]redmine.Ref, error)
	ListCustomFieldDefs(ctx context.Context, apiKey string) ([]redmine.CustomFieldDef, error)
	ListProjectVersions(ctx context.Context, apiKey string, projectID int) ([]redmine.Version, error)
	ListProjectMemberships(ctx context.Context, apiKey string, projectID int) ([]redmine.Membership, error)
	GetAttachment(ctx context.Context, apiKey string, id int) (*redmine.Attachment, error)
}

// KeyProvider は利用者の復号済み API キーを返し、上流 401 時に無効化する。
// credential.Vault を包むアダプタが実装する（キーの生存期間をハンドラ内に
// 閉じ込める）。未連携・無効なキーは ErrNoRedmineKey を返し、それ以外の
// エラー（DB 障害・復号失敗など）は素通しして 500 に写像させる。
type KeyProvider interface {
	APIKeyValue(ctx context.Context, userID string) (string, error)
	MarkInvalid(ctx context.Context, userID string) error
}

// AggregateHandler は画面向けの集約エンドポイント（Design.md §6.4）を提供する。
type AggregateHandler struct {
	Redmine Aggregator
	Keys    KeyProvider
	Cache   *AggCache
	Logger  *slog.Logger // 任意。500 経路の原因を記録する
}

func (h *AggregateHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/tree", h.projectsTree)
	mux.HandleFunc("GET /api/projects/{id}/issues/tree", h.issuesTree)
	mux.HandleFunc("GET /api/issues/{id}/detail", h.issueDetail)
	mux.HandleFunc("GET /api/meta", h.meta)
}

// resolve は認証・キー取得の共通前処理。
func (h *AggregateHandler) resolve(w http.ResponseWriter, r *http.Request) (userID, apiKey string, ok bool) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return "", "", false
	}
	apiKey, err := h.Keys.APIKeyValue(r.Context(), sess.UserID)
	if err != nil {
		if errors.Is(err, ErrNoRedmineKey) {
			// 未連携・無効化済み → 再紐付けを促す
			WriteError(w, CodeRedmineCredentialInvalid, "redmine account not linked")
			return "", "", false
		}
		// DB 障害・復号失敗などの一時的なサーバー側エラーは 500（再紐付けを促さない）
		h.logErr("api key load failed", sess.UserID, err)
		WriteError(w, CodeInternalError, "credential load failed")
		return "", "", false
	}
	return sess.UserID, apiKey, true
}

// writeUpstream は Redmine 由来のエラーを適切なコードへ写像する。上流 401 は
// proxy と同様に保存済みキーを無効化してから 409 を返す（両経路の挙動を揃える）。
func (h *AggregateHandler) writeUpstream(w http.ResponseWriter, r *http.Request, userID string, err error) {
	switch {
	case errors.Is(err, redmine.ErrUnauthorized):
		if merr := h.Keys.MarkInvalid(r.Context(), userID); merr != nil {
			h.logErr("mark credential invalid failed", userID, merr)
		}
		WriteError(w, CodeRedmineCredentialInvalid, "redmine credential is invalid; re-link required")
	case errors.Is(err, redmine.ErrUpstream):
		WriteError(w, CodeUpstreamError, "redmine is unavailable")
	default:
		h.logErr("aggregate upstream failed", userID, err)
		WriteError(w, CodeInternalError, "aggregate failed")
	}
}

func (h *AggregateHandler) logErr(msg, userID string, err error) {
	if h.Logger != nil {
		h.Logger.Error(msg, "error", err, "user_id", userID)
	}
}

func (h *AggregateHandler) projectsTree(w http.ResponseWriter, r *http.Request) {
	userID, apiKey, ok := h.resolve(w, r)
	if !ok {
		return
	}
	// プロジェクトツリーはユーザー単位で 60 秒キャッシュ（Design.md §6.6）
	v, err := h.Cache.projectTree.get(userID, func() (any, error) {
		projects, err := h.Redmine.ListProjects(r.Context(), apiKey)
		if err != nil {
			return nil, err
		}
		tree := redmine.BuildProjectTree(projects)
		if err := h.enrichOpenCounts(r.Context(), userID, apiKey, tree); err != nil {
			return nil, err
		}
		return tree, nil
	})
	if err != nil {
		h.writeUpstream(w, r, userID, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"projects": v})
}

// enrichOpenCounts は各プロジェクトノードに未完了チケット数を後付けする
// （Design.md §7.6）。件数は付随情報なので、上流 401（要再連携）だけを伝播し、
// 一時障害などその他のエラーは当該ノードの件数を欠測（nil）にしてツリー描画は
// 続行する。取得はキー単位で並行化するが、実際の上流並行数は Redmine
// クライアント側のセマフォで抑えられる。
func (h *AggregateHandler) enrichOpenCounts(ctx context.Context, userID, apiKey string, tree []*redmine.ProjectNode) error {
	nodes := flattenProjectNodes(tree)
	if len(nodes) == 0 {
		return nil
	}
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var reqErr error // 401、またはリクエスト自体の中断（欠測値をキャッシュしないため伝播）

	for _, nd := range nodes {
		wg.Add(1)
		go func(nd *redmine.ProjectNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n, err := h.Redmine.CountOpenIssues(ctx, apiKey, nd.ID)
			if err != nil {
				switch {
				case errors.Is(err, redmine.ErrUnauthorized):
					mu.Lock()
					reqErr = err
					mu.Unlock()
				case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
					// クライアント切断・タイムアウト等でリクエスト自体が
					// 中断された。1 ノードの一時障害ではないので、欠測値の
					// まま 60 秒キャッシュされないよう呼び出し元へ伝播する
					// （ttlCache はエラー時キャッシュしない）。
					mu.Lock()
					if reqErr == nil {
						reqErr = err
					}
					mu.Unlock()
				default:
					h.logErr("open issue count failed", userID, err)
				}
				return
			}
			cnt := n
			nd.OpenIssues = &cnt // 各 goroutine は別ノードだけを書く
		}(nd)
	}
	wg.Wait()
	return reqErr
}

// flattenProjectNodes はツリーを先行順の *ProjectNode 平坦列にする。
func flattenProjectNodes(tree []*redmine.ProjectNode) []*redmine.ProjectNode {
	var out []*redmine.ProjectNode
	var walk func(ns []*redmine.ProjectNode)
	walk = func(ns []*redmine.ProjectNode) {
		for _, n := range ns {
			out = append(out, n)
			walk(n.Children)
		}
	}
	walk(tree)
	return out
}

func (h *AggregateHandler) issuesTree(w http.ResponseWriter, r *http.Request) {
	userID, apiKey, ok := h.resolve(w, r)
	if !ok {
		return
	}
	projectID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		WriteError(w, CodeInvalidRequest, "invalid project id")
		return
	}
	issues, err := h.Redmine.ListProjectIssues(r.Context(), apiKey, projectID)
	if err != nil {
		h.writeUpstream(w, r, userID, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"issues": redmine.BuildIssueTree(issues)})
}

// issueDetailResponse はチケット詳細のレスポンス形。CustomFields は
// redmine.Issue.CustomFields（生値）を、定義突合・参照解決済みの
// redmine.ResolvedCustomField 列で上書きする（同名 JSON タグは浅い方が
// 勝つ Go の埋め込みルールを利用）。
type issueDetailResponse struct {
	redmine.Issue
	CustomFields []redmine.ResolvedCustomField `json:"custom_fields,omitempty"`
}

func (h *AggregateHandler) issueDetail(w http.ResponseWriter, r *http.Request) {
	userID, apiKey, ok := h.resolve(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		WriteError(w, CodeInvalidRequest, "invalid issue id")
		return
	}
	issue, err := h.Redmine.GetIssue(r.Context(), apiKey, id)
	if err != nil {
		h.writeUpstream(w, r, userID, err)
		return
	}

	defs, err := h.customFieldDefs(r.Context(), userID, apiKey)
	if err != nil {
		h.writeUpstream(w, r, userID, err)
		return
	}
	merged := redmine.MergeCustomFields(issue.CustomFields, defs)
	if err := h.resolveCustomFieldRefs(r.Context(), userID, apiKey, issue.Project.ID, merged); err != nil {
		h.writeUpstream(w, r, userID, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"issue": issueDetailResponse{Issue: *issue, CustomFields: merged}})
}

// resolveCustomFieldRefs は version/user/attachment フォーマットの値
// （参照先の ID）を、対応するプロジェクトのバージョン一覧・メンバー一覧・
// 添付情報から人が読める名前へ解決する（Design.md §7.8）。参照先の一覧
// 取得自体が上流 401 の場合のみ呼び出し元へ伝播し（再紐付けを促す）、
// それ以外の障害は当該フィールドを生値表示のまま残して続行する
// （チケット詳細のキャッシュはない——Design.md §6.6——ので、
// enrichOpenCounts のような「中断はキャッシュしない」配慮は不要）。
func (h *AggregateHandler) resolveCustomFieldRefs(ctx context.Context, userID, apiKey string, projectID int, fields []redmine.ResolvedCustomField) error {
	var needVersions, needUsers, needAttachments bool
	for _, f := range fields {
		switch f.FieldFormat {
		case "version":
			needVersions = true
		case "user":
			needUsers = true
		case "attachment":
			needAttachments = true
		}
	}

	versionNames := map[string]string{}
	if needVersions {
		versions, err := h.Redmine.ListProjectVersions(ctx, apiKey, projectID)
		if err != nil {
			if errors.Is(err, redmine.ErrUnauthorized) {
				return err
			}
			h.logErr("custom field version lookup failed; showing raw value", userID, err)
		}
		for _, v := range versions {
			versionNames[strconv.Itoa(v.ID)] = v.Name
		}
	}

	userNames := map[string]string{}
	if needUsers {
		members, err := h.Redmine.ListProjectMemberships(ctx, apiKey, projectID)
		if err != nil {
			if errors.Is(err, redmine.ErrUnauthorized) {
				return err
			}
			h.logErr("custom field user lookup failed; showing raw value", userID, err)
		}
		for _, m := range members {
			if m.User != nil {
				userNames[strconv.Itoa(m.User.ID)] = m.User.Name
			}
		}
	}

	attachmentNames := map[string]string{}
	if needAttachments {
		for _, f := range fields {
			if f.FieldFormat != "attachment" {
				continue
			}
			for _, raw := range redmine.RawCustomFieldValues(f.Value) {
				if _, done := attachmentNames[raw]; done {
					continue
				}
				attID, convErr := strconv.Atoi(raw)
				if convErr != nil {
					continue
				}
				att, err := h.Redmine.GetAttachment(ctx, apiKey, attID)
				if err != nil {
					if errors.Is(err, redmine.ErrUnauthorized) {
						return err
					}
					h.logErr("custom field attachment lookup failed; showing raw value", userID, err)
					continue
				}
				attachmentNames[raw] = att.Filename
			}
		}
	}

	for i := range fields {
		switch fields[i].FieldFormat {
		case "version":
			fields[i].ResolveDisplayValue(func(raw string) string { return versionNames[raw] })
		case "user":
			fields[i].ResolveDisplayValue(func(raw string) string { return userNames[raw] })
		case "attachment":
			fields[i].ResolveDisplayValue(func(raw string) string { return attachmentNames[raw] })
		}
	}
	return nil
}

// customFieldDefs はカスタムフィールド定義を取得する（10 分キャッシュ、
// ユーザー単位）。上流仕様上 `GET /custom_fields.json` は管理者権限が
// 必要なため、非管理者アカウントでの失敗（403 等）は定義なし（空スライス）
// として degrade し、呼び出し元を失敗させない（Design.md §6.4）。ただし
// 上流 401（API キー自体が無効）は他の経路と同様に呼び出し元へ伝播し、
// 再紐付けを促す——ここだけ黙って degrade すると、キーが無効なのに
// チケット詳細やメタ情報だけは中途半端に表示され続けてしまう。
func (h *AggregateHandler) customFieldDefs(ctx context.Context, userID, apiKey string) ([]redmine.CustomFieldDef, error) {
	v, err := h.Cache.customFieldDefs.get(userID, func() (any, error) {
		defs, err := h.Redmine.ListCustomFieldDefs(ctx, apiKey)
		if err != nil {
			if errors.Is(err, redmine.ErrUnauthorized) {
				return nil, err // 呼び出し元へ伝播。キャッシュしない
			}
			h.logErr("custom field defs unavailable; degrading to raw display", userID, err)
			return []redmine.CustomFieldDef{}, nil
		}
		return defs, nil
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]redmine.CustomFieldDef), nil
}

func (h *AggregateHandler) meta(w http.ResponseWriter, r *http.Request) {
	userID, apiKey, ok := h.resolve(w, r)
	if !ok {
		return
	}
	// メタ（トラッカー・ステータス・優先度）はユーザー単位で 10 分キャッシュ
	v, err := h.Cache.meta.get(userID, func() (any, error) {
		trackers, err := h.Redmine.ListTrackers(r.Context(), apiKey)
		if err != nil {
			return nil, err
		}
		statuses, err := h.Redmine.ListStatuses(r.Context(), apiKey)
		if err != nil {
			return nil, err
		}
		priorities, err := h.Redmine.ListPriorities(r.Context(), apiKey)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"trackers":   trackers,
			"statuses":   statuses,
			"priorities": priorities,
		}, nil
	})
	if err != nil {
		h.writeUpstream(w, r, userID, err)
		return
	}
	// カスタムフィールド定義は別キャッシュ・別 degrade ポリシー（§6.4）のため、
	// 上の必須メタとは独立に取得しマージする。v はキャッシュヒット時に複数
	// リクエストへ同一 map 参照が返るため、直接書き込むと並行アクセスで
	// "concurrent map writes" fatal を起こす——書き込み用に新しい map へコピーする。
	defs, err := h.customFieldDefs(r.Context(), userID, apiKey)
	if err != nil {
		h.writeUpstream(w, r, userID, err)
		return
	}
	cached := v.(map[string]any)
	out := make(map[string]any, len(cached)+1)
	for k, val := range cached {
		out[k] = val
	}
	out["customFields"] = defs
	WriteJSON(w, http.StatusOK, out)
}

// ---- キャッシュ（ユーザー単位で分離。Design.md §6.6） ----

// AggCache はメタ・プロジェクトツリー・カスタムフィールド定義のキャッシュを
// 束ねる。
type AggCache struct {
	meta            *ttlCache
	projectTree     *ttlCache
	customFieldDefs *ttlCache
}

func NewAggCache() *AggCache {
	return &AggCache{
		meta:            newTTLCache(10 * time.Minute),
		projectTree:     newTTLCache(60 * time.Second),
		customFieldDefs: newTTLCache(10 * time.Minute),
	}
}

// cacheSlot は 1 キー分の値と、そのキー専用のロック。gen() の実行を
// キー単位で直列化しつつ、別キー（別ユーザー）は互いにブロックしない。
type cacheSlot struct {
	mu        sync.Mutex
	value     any
	expiresAt time.Time
	set       bool
}

// ttlCache は userID ごとに 1 値を TTL 付きで保持する。グローバルロックは
// スロットの取得・生成の間だけ握り、上流取得（gen）中はキー専用ロックのみ
// を握るので、あるユーザーの遅い上流呼び出しが他ユーザーを止めない。
type ttlCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]*cacheSlot
	now func() time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, m: map[string]*cacheSlot{}, now: time.Now}
}

func (c *ttlCache) get(key string, gen func() (any, error)) (any, error) {
	c.mu.Lock()
	slot := c.m[key]
	if slot == nil {
		slot = &cacheSlot{}
		c.m[key] = slot
	}
	c.mu.Unlock()

	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.set && c.now().Before(slot.expiresAt) {
		return slot.value, nil
	}
	v, err := gen()
	if err != nil {
		return nil, err // エラーはキャッシュしない
	}
	slot.value = v
	slot.expiresAt = c.now().Add(c.ttl)
	slot.set = true
	return v, nil
}
