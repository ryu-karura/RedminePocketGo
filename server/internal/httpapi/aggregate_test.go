package httpapi

import (
	"context"
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
	err        error
	calls      atomic.Int32
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
	key string
	err error
}

func (f *fakeKeyLoader) APIKeyValue(context.Context, string) (string, error) {
	return f.key, f.err
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
