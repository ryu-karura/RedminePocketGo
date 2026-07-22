package redmine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(upstreamURL string, pageSize int) *Client {
	return NewClient(Config{
		BaseURL:        upstreamURL,
		SubURI:         "/redmine",
		Timeout:        2 * time.Second,
		MaxRetries:     2,
		MaxConcurrency: 4,
		PageSize:       pageSize,
	})
}

func TestClientInjectsKeyAndJoinsSubURI(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("X-Redmine-Api-Key")
		fmt.Fprint(w, `{"projects":[],"total_count":0,"offset":0,"limit":100}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 100)
	if _, err := c.ListProjects(context.Background(), "key-1"); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if gotPath != "/redmine/projects.json" {
		t.Errorf("path = %q; want /redmine/projects.json", gotPath)
	}
	if gotKey != "key-1" {
		t.Errorf("api key = %q; want key-1", gotKey)
	}
}

func TestClientCountOpenIssues(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		// 件数だけ必要。issues 本体は空でも total_count を返す。
		fmt.Fprint(w, `{"issues":[],"total_count":7,"offset":0,"limit":1}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 100)
	n, err := c.CountOpenIssues(context.Background(), "k", 42)
	if err != nil {
		t.Fatalf("CountOpenIssues: %v", err)
	}
	if n != 7 {
		t.Errorf("count = %d; want 7 (total_count)", n)
	}
	if gotPath != "/redmine/issues.json" {
		t.Errorf("path = %q; want /redmine/issues.json", gotPath)
	}
	for _, kv := range []struct{ k, want string }{
		{"project_id", "42"},
		{"status_id", "open"},
		{"subproject_id", "!*"}, // サブプロジェクトを除く直下の件数
		{"limit", "1"},          // 本体は要らないので最小
	} {
		if got := gotQuery.Get(kv.k); got != kv.want {
			t.Errorf("query %s = %q; want %q", kv.k, got, kv.want)
		}
	}
}

func TestClientPaginatesUntilTotal(t *testing.T) {
	// 250 件を pageSize=100 で 3 ページに分けて返す
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := 0
		fmt.Sscan(r.URL.Query().Get("offset"), &offset)
		limit := 0
		fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		if limit != 100 {
			t.Errorf("limit = %d; want 100", limit)
		}
		n := 100
		if offset+n > 250 {
			n = 250 - offset
		}
		fmt.Fprintf(w, `{"projects":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id":%d,"name":"p%d"}`, offset+i+1, offset+i+1)
		}
		fmt.Fprintf(w, `],"total_count":250,"offset":%d,"limit":100}`, offset)
	}))
	defer srv.Close()

	projects, err := newTestClient(srv.URL, 100).ListProjects(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 250 {
		t.Fatalf("got %d projects; want 250", len(projects))
	}
	if projects[0].ID != 1 || projects[249].ID != 250 {
		t.Errorf("pagination order wrong: first=%d last=%d", projects[0].ID, projects[249].ID)
	}
}

func TestClientRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(503) // 一時障害 × 2 回
			return
		}
		fmt.Fprint(w, `{"projects":[],"total_count":0,"offset":0,"limit":100}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 100)
	c.backoffBase = time.Millisecond // テストを速く
	if _, err := c.ListProjects(context.Background(), "k"); err != nil {
		t.Fatalf("should succeed after retries: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d; want 3 (1 + 2 retries)", calls.Load())
	}
}

func TestClientDoesNotRetry4xxAnd401IsTyped(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(401)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 100)
	c.backoffBase = time.Millisecond
	_, err := c.ListProjects(context.Background(), "bad-key")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v; want ErrUnauthorized", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d; 4xx must not be retried", calls.Load())
	}
}

func TestClientGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, 100)
	c.backoffBase = time.Millisecond
	_, err := c.ListProjects(context.Background(), "k")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v; want ErrUpstream", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d; want 3 (1 + maxRetries 2)", calls.Load())
	}
}

func TestClientConcurrencyCap(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		fmt.Fprint(w, `{"projects":[],"total_count":0,"offset":0,"limit":100}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, SubURI: "/redmine", Timeout: 2 * time.Second,
		MaxRetries: 0, MaxConcurrency: 2, PageSize: 100})
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			c.ListProjects(context.Background(), "k")
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if peak.Load() > 2 {
		t.Errorf("peak concurrency = %d; want <= 2", peak.Load())
	}
}

func TestGetIssueParsesDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redmine/issues/42.json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if inc := r.URL.Query().Get("include"); inc != "journals,attachments" {
			t.Errorf("include = %q", inc)
		}
		fmt.Fprint(w, `{"issue":{"id":42,"subject":"件名","status":{"id":1,"name":"新規"},
			"journals":[{"id":9,"notes":"メモ","user":{"id":2,"name":"Alice"},"created_on":"2026-07-20T00:00:00Z"}],
			"attachments":[{"id":3,"filename":"a.png","filesize":10,"content_url":"http://x/a.png"}]}}`)
	}))
	defer srv.Close()

	issue, err := newTestClient(srv.URL, 100).GetIssue(context.Background(), "k", 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.ID != 42 || issue.Subject != "件名" || issue.Status.Name != "新規" {
		t.Errorf("issue = %+v", issue)
	}
	if len(issue.Journals) != 1 || issue.Journals[0].Notes != "メモ" {
		t.Errorf("journals = %+v", issue.Journals)
	}
	if len(issue.Attachments) != 1 || issue.Attachments[0].Filename != "a.png" {
		t.Errorf("attachments = %+v", issue.Attachments)
	}
}

func TestClientPaginationNoDuplicateOnShortPage(t *testing.T) {
	// pageSize=100 だが各ページが 60 件しか返さない（total=150）状況。
	// offset を返り件数で進めると重複するが、pageSize で進めれば重複しない。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := 0
		fmt.Sscan(r.URL.Query().Get("offset"), &offset)
		start := offset + 1
		end := offset + 60
		if end > 150 {
			end = 150
		}
		fmt.Fprint(w, `{"projects":[`)
		first := true
		for id := start; id <= end; id++ {
			if !first {
				fmt.Fprint(w, ",")
			}
			first = false
			fmt.Fprintf(w, `{"id":%d,"name":"p%d"}`, id, id)
		}
		fmt.Fprint(w, `],"total_count":150}`)
	}))
	defer srv.Close()

	projects, err := newTestClient(srv.URL, 100).ListProjects(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	seen := map[int]bool{}
	for _, p := range projects {
		if seen[p.ID] {
			t.Errorf("duplicate project id %d (pagination overlap)", p.ID)
		}
		seen[p.ID] = true
	}
}

func TestClientPingReachable(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusFound) // Redmine の未ログインルートは 302 を返す。到達性のみ見る。
	}))
	defer srv.Close()

	if err := newTestClient(srv.URL, 100).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if gotPath != "/redmine/" {
		t.Errorf("path = %q; want /redmine/", gotPath)
	}
}

func TestClientPingUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unreachable サーバーへリクエストが飛んではいけない")
	}))
	srv.Close() // 閉じた直後の URL は接続不能

	err := newTestClient(srv.URL, 100).Ping(context.Background())
	if err == nil {
		t.Fatal("Ping: エラーなし; 到達不能を検知できていない")
	}
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("Ping error = %v; want wrapping ErrUpstream", err)
	}
}
