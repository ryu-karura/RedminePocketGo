package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

type fakeAggregator struct {
	projects   []redmine.Project
	issues     []redmine.Issue
	issue      *redmine.Issue
	trackers   []redmine.Ref
	statuses   []redmine.Status
	priorities []redmine.Ref
	openCounts map[int]int // projectID -> 未完了件数
	countErr   error       // 設定時、CountOpenIssues だけをこのエラーで失敗させる
	err        error
	calls      atomic.Int32
}

func (f *fakeAggregator) CountOpenIssues(_ context.Context, _ string, projectID int) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	if f.err != nil {
		return 0, f.err
	}
	return f.openCounts[projectID], nil
}

func (f *fakeAggregator) ListProjects(context.Context, string) ([]redmine.Project, error) {
	f.calls.Add(1)
	return f.projects, f.err
}
func (f *fakeAggregator) ListProjectIssues(context.Context, string, int) ([]redmine.Issue, error) {
	f.calls.Add(1)
	return f.issues, f.err
}
func (f *fakeAggregator) GetIssue(context.Context, string, int) (*redmine.Issue, error) {
	f.calls.Add(1)
	return f.issue, f.err
}
func (f *fakeAggregator) ListTrackers(context.Context, string) ([]redmine.Ref, error) {
	return f.trackers, f.err
}
func (f *fakeAggregator) ListStatuses(context.Context, string) ([]redmine.Status, error) {
	return f.statuses, f.err
}
func (f *fakeAggregator) ListPriorities(context.Context, string) ([]redmine.Ref, error) {
	return f.priorities, f.err
}

type fakeKeyLoader struct {
	key         string
	err         error
	markedValid string // MarkInvalid が呼ばれた userID
}

func (f *fakeKeyLoader) APIKeyValue(context.Context, string) (string, error) {
	return f.key, f.err
}
func (f *fakeKeyLoader) MarkInvalid(_ context.Context, userID string) error {
	f.markedValid = userID
	return nil
}

func newAggMux(agg Aggregator, keys KeyProvider) *http.ServeMux {
	mux := http.NewServeMux()
	(&AggregateHandler{Redmine: agg, Keys: keys, Cache: NewAggCache()}).RegisterRoutes(mux)
	return mux
}

func TestProjectsTree(t *testing.T) {
	agg := &fakeAggregator{projects: []redmine.Project{
		{ID: 1, Name: "root"},
		{ID: 2, Name: "child", Parent: &redmine.Ref{ID: 1}},
	}}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})

	t.Run("unauthenticated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects/tree", nil))
		if rec.Code != 401 {
			t.Errorf("status = %d; want 401", rec.Code)
		}
	})

	t.Run("nested output", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
		if rec.Code != 200 {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `"children"`) || !strings.Contains(rec.Body.String(), `"child"`) {
			t.Errorf("body lacks nested tree: %s", rec.Body)
		}
	})
}

func TestProjectsTreeIncludesOpenCounts(t *testing.T) {
	agg := &fakeAggregator{
		projects: []redmine.Project{
			{ID: 1, Name: "root"},
			{ID: 2, Name: "child", Parent: &redmine.Ref{ID: 1}},
		},
		openCounts: map[int]int{1: 12, 2: 5},
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"openIssues":12`) || !strings.Contains(body, `"openIssues":5`) {
		t.Errorf("body lacks per-project open counts: %s", body)
	}
}

// TestProjectsTreeOpenCountCancellationNotCached は、未完了件数の取得中に
// リクエストが中断された（クライアント切断・タイムアウト）場合、欠測値の
// ままのツリーが 60 秒キャッシュされないことを確認する（ttlCache はエラー
// 時のみキャッシュしないため、enrichOpenCounts がこれをエラーとして正しく
// 伝播する必要がある）。
func TestProjectsTreeOpenCountCancellationNotCached(t *testing.T) {
	agg := &fakeAggregator{
		projects: []redmine.Project{{ID: 1, Name: "p"}},
		countErr: context.Canceled,
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
	if rec.Code == 200 {
		t.Fatalf("status = 200; want an error status so the degraded tree isn't cached (body %s)", rec.Body)
	}

	// キャッシュされていなければ、次回は上流が正常なら populated で返る。
	agg.countErr = nil
	agg.openCounts = map[int]int{1: 7}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"openIssues":7`) {
		t.Errorf("status = %d body = %s; want 200 with openIssues:7 (proves the canceled attempt wasn't cached)", rec.Code, rec.Body)
	}
}

func TestProjectsTreeCachedPerUser(t *testing.T) {
	agg := &fakeAggregator{projects: []redmine.Project{{ID: 1, Name: "p"}}}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})

	call := func(userID string) {
		req := httptest.NewRequest("GET", "/api/projects/tree", nil)
		req = req.WithContext(WithSession(req.Context(), &SessionInfo{UserID: userID}))
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}
	call("u1")
	call("u1") // 2 回目はキャッシュ
	if agg.calls.Load() != 1 {
		t.Errorf("u1 upstream calls = %d; want 1 (cached)", agg.calls.Load())
	}
	call("u2") // 別ユーザーは別キャッシュ
	if agg.calls.Load() != 2 {
		t.Errorf("after u2: calls = %d; want 2 (per-user isolation)", agg.calls.Load())
	}
}

func TestIssuesTree(t *testing.T) {
	agg := &fakeAggregator{issues: []redmine.Issue{
		{ID: 10, Subject: "a"},
		{ID: 11, Subject: "b", Parent: &struct {
			ID int `json:"id"`
		}{ID: 10}},
	}}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/5/issues/tree", nil)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"children"`) {
		t.Errorf("status = %d body = %s", rec.Code, rec.Body)
	}
}

func TestIssueDetail(t *testing.T) {
	agg := &fakeAggregator{issue: &redmine.Issue{ID: 42, Subject: "detail"}}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"detail"`) {
		t.Errorf("status = %d body = %s", rec.Code, rec.Body)
	}
}

func TestMeta(t *testing.T) {
	agg := &fakeAggregator{
		trackers:   []redmine.Ref{{ID: 1, Name: "Bug"}},
		statuses:   []redmine.Status{{ID: 1, Name: "New"}},
		priorities: []redmine.Ref{{ID: 2, Name: "Normal"}},
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/meta", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	for _, want := range []string{"Bug", "New", "Normal"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("meta body lacks %q: %s", want, rec.Body)
		}
	}
}

func TestAggregateUpstreamErrorMaps(t *testing.T) {
	agg := &fakeAggregator{err: redmine.ErrUnauthorized}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), CodeRedmineCredentialInvalid) {
		t.Errorf("401 upstream: status = %d body = %s; want 409", rec.Code, rec.Body)
	}
}

func TestAggregateNoKeyIs409(t *testing.T) {
	agg := &fakeAggregator{}
	mux := newAggMux(agg, &fakeKeyLoader{err: ErrNoRedmineKey})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409 when no key linked", rec.Code)
	}
}

func TestCacheConcurrentSameKeyGeneratesOnce(t *testing.T) {
	agg := &fakeAggregator{projects: []redmine.Project{{ID: 1, Name: "p"}}}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})

	const n = 16
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/api/projects/tree", nil)
			req = req.WithContext(WithSession(req.Context(), &SessionInfo{UserID: "same"}))
			mux.ServeHTTP(httptest.NewRecorder(), req)
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	// 同一ユーザーの並行アクセスでも上流呼び出しは 1 回（キー単位で直列化）
	if agg.calls.Load() != 1 {
		t.Errorf("concurrent same-user calls = %d; want 1", agg.calls.Load())
	}
}

func TestAggregate401MarksCredentialInvalid(t *testing.T) {
	agg := &fakeAggregator{err: redmine.ErrUnauthorized}
	keys := &fakeKeyLoader{key: "k"}
	mux := newAggMux(agg, keys)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
	if rec.Code != 409 {
		t.Fatalf("status = %d; want 409", rec.Code)
	}
	if keys.markedValid != "u1" {
		t.Errorf("aggregate did not invalidate credential on upstream 401; markedValid = %q", keys.markedValid)
	}
}

func TestAggregateTransientKeyErrorIs500(t *testing.T) {
	// ErrNoRedmineKey 以外（DB 障害等）は 409 ではなく 500
	agg := &fakeAggregator{}
	mux := newAggMux(agg, &fakeKeyLoader{err: fmt.Errorf("db down")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/projects/tree", nil)))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), CodeInternalError) {
		t.Errorf("transient key error: status = %d body = %s; want 500", rec.Code, rec.Body)
	}
}
