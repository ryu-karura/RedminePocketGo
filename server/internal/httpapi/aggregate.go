package httpapi

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"net/http"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

// ErrNoRedmineKey は利用者に有効な Redmine API キーが無い。
var ErrNoRedmineKey = errors.New("httpapi: redmine api key not available")

// Aggregator は Redmine クライアントが満たす（集約 API 用）。
type Aggregator interface {
	ListProjects(ctx context.Context, apiKey string) ([]redmine.Project, error)
	ListProjectIssues(ctx context.Context, apiKey string, projectID int) ([]redmine.Issue, error)
	GetIssue(ctx context.Context, apiKey string, id int) (*redmine.Issue, error)
	ListTrackers(ctx context.Context, apiKey string) ([]redmine.Ref, error)
	ListStatuses(ctx context.Context, apiKey string) ([]redmine.Status, error)
	ListPriorities(ctx context.Context, apiKey string) ([]redmine.Ref, error)
}

// KeyProvider は利用者の復号済み API キーを返す。credential.Vault を包む
// アダプタが実装する（キーの生存期間をハンドラ内に閉じ込める）。
type KeyProvider interface {
	APIKeyValue(ctx context.Context, userID string) (string, error)
}

// AggregateHandler は画面向けの集約エンドポイント（Design.md §6.4）を提供する。
type AggregateHandler struct {
	Redmine Aggregator
	Keys    KeyProvider
	Cache   *AggCache
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
		WriteError(w, CodeRedmineCredentialInvalid, "redmine account not linked")
		return "", "", false
	}
	return sess.UserID, apiKey, true
}

// writeUpstream は Redmine 由来のエラーを適切なコードへ写像する。
func writeUpstream(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, redmine.ErrUnauthorized):
		WriteError(w, CodeRedmineCredentialInvalid, "redmine credential is invalid; re-link required")
	case errors.Is(err, ErrUpstream):
		WriteError(w, CodeUpstreamError, "redmine is unavailable")
	default:
		WriteError(w, CodeInternalError, "aggregate failed")
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
		return redmine.BuildProjectTree(projects), nil
	})
	if err != nil {
		writeUpstream(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"projects": v})
}

func (h *AggregateHandler) issuesTree(w http.ResponseWriter, r *http.Request) {
	_, apiKey, ok := h.resolve(w, r)
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
		writeUpstream(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"issues": redmine.BuildIssueTree(issues)})
}

func (h *AggregateHandler) issueDetail(w http.ResponseWriter, r *http.Request) {
	_, apiKey, ok := h.resolve(w, r)
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
		writeUpstream(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"issue": issue})
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
		writeUpstream(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, v)
}

// ---- キャッシュ（ユーザー単位で分離。Design.md §6.6） ----

// AggCache はメタとプロジェクトツリーのキャッシュを束ねる。
type AggCache struct {
	meta        *ttlCache
	projectTree *ttlCache
}

func NewAggCache() *AggCache {
	return &AggCache{
		meta:        newTTLCache(10 * time.Minute),
		projectTree: newTTLCache(60 * time.Second),
	}
}

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

// ttlCache は userID ごとに 1 値を TTL 付きで保持する。生成関数の実行は
// 同一キーで直列化し、TTL 内はキャッシュを返す。
type ttlCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]cacheEntry
	now func() time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, m: map[string]cacheEntry{}, now: time.Now}
}

func (c *ttlCache) get(key string, gen func() (any, error)) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok && c.now().Before(e.expiresAt) {
		return e.value, nil
	}
	v, err := gen()
	if err != nil {
		return nil, err
	}
	c.m[key] = cacheEntry{value: v, expiresAt: c.now().Add(c.ttl)}
	return v, nil
}
