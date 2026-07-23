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

func TestListCustomFieldDefsFiltersToIssueType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redmine/custom_fields.json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"custom_fields":[
			{"id":1,"name":"対応バージョン","customized_type":"issue","field_format":"version",
			 "is_required":true,"min_length":0,"max_length":0,"multiple":false},
			{"id":2,"name":"部署","customized_type":"user","field_format":"list",
			 "possible_values":["総務","営業"]},
			{"id":3,"name":"優先タグ","customized_type":"issue","field_format":"list",
			 "is_required":false,"multiple":true,
			 "possible_values":[{"value":"a","label":"重要"},{"value":"b","label":"通常"}]}
		]}`)
	}))
	defer srv.Close()

	defs, err := newTestClient(srv.URL, 100).ListCustomFieldDefs(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListCustomFieldDefs: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d defs; want 2 (issue-typed only): %+v", len(defs), defs)
	}
	if defs[0].ID != 1 || !defs[0].IsRequired || defs[0].FieldFormat != "version" {
		t.Errorf("defs[0] = %+v", defs[0])
	}
	if defs[1].ID != 3 || !defs[1].Multiple {
		t.Errorf("defs[1] = %+v", defs[1])
	}
	if len(defs[1].PossibleValues) != 2 ||
		defs[1].PossibleValues[0].Value != "a" || defs[1].PossibleValues[0].Label != "重要" {
		t.Errorf("possible_values not parsed from {value,label} objects: %+v", defs[1].PossibleValues)
	}
}

func TestListCustomFieldDefsPossibleValuesAsPlainStrings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"custom_fields":[
			{"id":1,"name":"色","customized_type":"issue","field_format":"list",
			 "possible_values":["赤","青"]}
		]}`)
	}))
	defer srv.Close()

	defs, err := newTestClient(srv.URL, 100).ListCustomFieldDefs(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListCustomFieldDefs: %v", err)
	}
	if len(defs) != 1 || len(defs[0].PossibleValues) != 2 {
		t.Fatalf("defs = %+v", defs)
	}
	if defs[0].PossibleValues[0].Value != "赤" || defs[0].PossibleValues[0].Label != "赤" {
		t.Errorf("plain-string possible_values should have Value==Label: %+v", defs[0].PossibleValues[0])
	}
}

func TestListCustomFieldDefsForbiddenIsUpstreamError(t *testing.T) {
	// /custom_fields.json は上流仕様上、管理者専用（Design.md §6.4）。
	// 非管理者アカウントでは 403 になりうるので、呼び出し側が degrade できる
	// よう ErrUpstream として区別可能に返す。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL, 100).ListCustomFieldDefs(context.Background(), "k")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v; want ErrUpstream (403 forbidden)", err)
	}
}

func TestGetIssueParsesCustomFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"issue":{"id":42,"subject":"件名","status":{"id":1,"name":"新規"},
			"custom_fields":[
				{"id":1,"name":"対応バージョン","value":"3"},
				{"id":2,"name":"優先タグ","value":["a","b"]},
				{"id":3,"name":"備考","value":null}
			]}}`)
	}))
	defer srv.Close()

	issue, err := newTestClient(srv.URL, 100).GetIssue(context.Background(), "k", 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(issue.CustomFields) != 3 {
		t.Fatalf("custom_fields = %+v; want 3 entries", issue.CustomFields)
	}
	if issue.CustomFields[0].ID != 1 || issue.CustomFields[0].Value != "3" {
		t.Errorf("custom_fields[0] = %+v", issue.CustomFields[0])
	}
	multi, ok := issue.CustomFields[1].Value.([]any)
	if !ok || len(multi) != 2 || multi[0] != "a" {
		t.Errorf("custom_fields[1] (multiple) = %+v", issue.CustomFields[1].Value)
	}
	if issue.CustomFields[2].Value != nil {
		t.Errorf("custom_fields[2] = %+v; want nil value", issue.CustomFields[2].Value)
	}
}

func TestListProjectVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redmine/projects/5/versions.json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"versions":[{"id":3,"name":"v2.0"},{"id":4,"name":"v3.0"}]}`)
	}))
	defer srv.Close()

	versions, err := newTestClient(srv.URL, 100).ListProjectVersions(context.Background(), "k", 5)
	if err != nil {
		t.Fatalf("ListProjectVersions: %v", err)
	}
	if len(versions) != 2 || versions[0].ID != 3 || versions[0].Name != "v2.0" {
		t.Errorf("versions = %+v", versions)
	}
}

func TestListProjectMemberships(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redmine/projects/5/memberships.json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"memberships":[
			{"id":1,"user":{"id":2,"name":"Alice"}},
			{"id":2,"group":{"id":9,"name":"開発チーム"}}
		]}`)
	}))
	defer srv.Close()

	ms, err := newTestClient(srv.URL, 100).ListProjectMemberships(context.Background(), "k", 5)
	if err != nil {
		t.Fatalf("ListProjectMemberships: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("memberships = %+v; want 2", ms)
	}
	if ms[0].User == nil || ms[0].User.Name != "Alice" {
		t.Errorf("memberships[0].User = %+v", ms[0].User)
	}
	if ms[1].User != nil {
		t.Errorf("memberships[1] (group, no user) should have nil User: %+v", ms[1].User)
	}
}

func TestGetAttachment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redmine/attachments/9.json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"attachment":{"id":9,"filename":"spec.pdf","filesize":2048,"content_url":"http://x/spec.pdf"}}`)
	}))
	defer srv.Close()

	att, err := newTestClient(srv.URL, 100).GetAttachment(context.Background(), "k", 9)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if att.ID != 9 || att.Filename != "spec.pdf" || att.Filesize != 2048 {
		t.Errorf("attachment = %+v", att)
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
