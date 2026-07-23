package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

type fakeAggregator struct {
	projects        []redmine.Project
	issues          []redmine.Issue
	issue           *redmine.Issue
	trackers        []redmine.Ref
	statuses        []redmine.Status
	priorities      []redmine.Ref
	openCounts      map[int]int // projectID -> 未完了件数
	countErr        error       // 設定時、CountOpenIssues だけをこのエラーで失敗させる
	customFieldDefs []redmine.CustomFieldDef
	customFieldsErr error // 設定時、ListCustomFieldDefs だけをこのエラーで失敗させる
	versions        []redmine.Version
	versionsErr     error
	memberships     []redmine.Membership
	membershipsErr  error
	attachments     map[int]redmine.Attachment
	attachmentErr   error
	err             error
	calls           atomic.Int32
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
func (f *fakeAggregator) ListCustomFieldDefs(context.Context, string) ([]redmine.CustomFieldDef, error) {
	if f.customFieldsErr != nil {
		return nil, f.customFieldsErr
	}
	return f.customFieldDefs, f.err
}
func (f *fakeAggregator) ListProjectVersions(context.Context, string, int) ([]redmine.Version, error) {
	if f.versionsErr != nil {
		return nil, f.versionsErr
	}
	return f.versions, f.err
}
func (f *fakeAggregator) ListProjectMemberships(context.Context, string, int) ([]redmine.Membership, error) {
	if f.membershipsErr != nil {
		return nil, f.membershipsErr
	}
	return f.memberships, f.err
}
func (f *fakeAggregator) GetAttachment(_ context.Context, _ string, id int) (*redmine.Attachment, error) {
	if f.attachmentErr != nil {
		return nil, f.attachmentErr
	}
	att := f.attachments[id]
	return &att, f.err
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

func TestIssueDetailMergesCustomFieldDefs(t *testing.T) {
	agg := &fakeAggregator{
		issue: &redmine.Issue{ID: 42, Subject: "detail", Project: redmine.Ref{ID: 5},
			CustomFields: []redmine.CustomFieldValue{{ID: 3, Name: "優先タグ", Value: "a"}}},
		customFieldDefs: []redmine.CustomFieldDef{{
			ID: 3, Name: "優先タグ", FieldFormat: "list", IsRequired: true,
			PossibleValues: []redmine.PossibleValue{{Value: "a", Label: "重要"}},
		}},
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`"is_required":true`, `"display_value":"重要"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q: %s", want, body)
		}
	}
}

func TestIssueDetailDegradesWhenCustomFieldDefsUnavailable(t *testing.T) {
	// /custom_fields.json は管理者専用（Design.md §6.4）。403 相当のエラーでも
	// チケット詳細自体は失敗させず、生値表示に degrade する。
	agg := &fakeAggregator{
		issue: &redmine.Issue{ID: 42, Subject: "detail", Project: redmine.Ref{ID: 5},
			CustomFields: []redmine.CustomFieldValue{{ID: 3, Name: "備考", Value: "メモ"}}},
		customFieldsErr: redmine.ErrUpstream,
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d; want 200 (degraded, not failed): %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"value":"メモ"`) {
		t.Errorf("degraded body should still show raw value: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"is_required"`) {
		t.Errorf("degraded body should not claim required metadata it doesn't have: %s", rec.Body)
	}
}

// TestIssueDetailCustomFieldDefsUnauthorizedIs409 は、定義取得
// （GET /custom_fields.json）自体が上流 401 を返した場合、他の障害と違って
// 「定義なしの degrade」ではなく再紐付けを促す 409 にすることを確認する
// （Copilot レビュー指摘: customFieldDefs が 401 も無条件に飲み込んでいた）。
func TestIssueDetailCustomFieldDefsUnauthorizedIs409(t *testing.T) {
	agg := &fakeAggregator{
		issue:           &redmine.Issue{ID: 42, Project: redmine.Ref{ID: 5}},
		customFieldsErr: redmine.ErrUnauthorized,
	}
	keys := &fakeKeyLoader{key: "k"}
	mux := newAggMux(agg, keys)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409 on upstream 401 during custom field def lookup", rec.Code)
	}
	if keys.markedValid != "u1" {
		t.Errorf("credential should be marked invalid; markedValid = %q", keys.markedValid)
	}
}

func TestMetaCustomFieldDefsUnauthorizedIs409(t *testing.T) {
	agg := &fakeAggregator{
		trackers:        []redmine.Ref{{ID: 1, Name: "Bug"}},
		statuses:        []redmine.Status{{ID: 1, Name: "New"}},
		priorities:      []redmine.Ref{{ID: 2, Name: "Normal"}},
		customFieldsErr: redmine.ErrUnauthorized,
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/meta", nil)))
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409 on upstream 401 during custom field def lookup", rec.Code)
	}
}

func TestIssueDetailResolvesVersionUserAndAttachment(t *testing.T) {
	agg := &fakeAggregator{
		issue: &redmine.Issue{ID: 42, Subject: "detail", Project: redmine.Ref{ID: 5},
			CustomFields: []redmine.CustomFieldValue{
				{ID: 1, Name: "対応バージョン", Value: "3"},
				{ID: 2, Name: "レビュー担当", Value: "7"},
				{ID: 4, Name: "仕様書", Value: "9"},
			}},
		customFieldDefs: []redmine.CustomFieldDef{
			{ID: 1, FieldFormat: "version"},
			{ID: 2, FieldFormat: "user"},
			{ID: 4, FieldFormat: "attachment"},
		},
		versions:    []redmine.Version{{ID: 3, Name: "v2.0"}},
		memberships: []redmine.Membership{{ID: 1, User: &redmine.Ref{ID: 7, Name: "Alice"}}},
		attachments: map[int]redmine.Attachment{9: {ID: 9, Filename: "spec.pdf", Filesize: 2048}},
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`"display_value":"v2.0"`, `"display_value":"Alice"`, `"display_value":"spec.pdf"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q: %s", want, body)
		}
	}
}

func TestIssueDetailReferenceLookupUnauthorizedIs409(t *testing.T) {
	agg := &fakeAggregator{
		issue: &redmine.Issue{ID: 42, Project: redmine.Ref{ID: 5},
			CustomFields: []redmine.CustomFieldValue{{ID: 1, Value: "3"}}},
		customFieldDefs: []redmine.CustomFieldDef{{ID: 1, FieldFormat: "version"}},
		versionsErr:     redmine.ErrUnauthorized,
	}
	keys := &fakeKeyLoader{key: "k"}
	mux := newAggMux(agg, keys)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409 on upstream 401 during ref resolution", rec.Code)
	}
}

func TestIssueDetailMembershipLookupUnauthorizedIs409(t *testing.T) {
	agg := &fakeAggregator{
		issue: &redmine.Issue{ID: 42, Project: redmine.Ref{ID: 5},
			CustomFields: []redmine.CustomFieldValue{{ID: 2, Value: "7"}}},
		customFieldDefs: []redmine.CustomFieldDef{{ID: 2, FieldFormat: "user"}},
		membershipsErr:  redmine.ErrUnauthorized,
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409 on upstream 401 during membership lookup", rec.Code)
	}
}

func TestIssueDetailAttachmentLookupUnauthorizedIs409(t *testing.T) {
	agg := &fakeAggregator{
		issue: &redmine.Issue{ID: 42, Project: redmine.Ref{ID: 5},
			CustomFields: []redmine.CustomFieldValue{{ID: 4, Value: "9"}}},
		customFieldDefs: []redmine.CustomFieldDef{{ID: 4, FieldFormat: "attachment"}},
		attachmentErr:   redmine.ErrUnauthorized,
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409 on upstream 401 during attachment lookup", rec.Code)
	}
}

func TestIssueDetailReferenceLookupOtherErrorDegrades(t *testing.T) {
	// バージョン一覧の取得が一時障害でも、詳細取得自体は失敗させず生値表示にする。
	agg := &fakeAggregator{
		issue: &redmine.Issue{ID: 42, Project: redmine.Ref{ID: 5},
			CustomFields: []redmine.CustomFieldValue{{ID: 1, Name: "対応バージョン", Value: "3"}}},
		customFieldDefs: []redmine.CustomFieldDef{{ID: 1, FieldFormat: "version"}},
		versionsErr:     redmine.ErrUpstream,
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/issues/42/detail", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d; want 200 (degraded): %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"value":"3"`) {
		t.Errorf("degraded ref should still carry raw value: %s", rec.Body)
	}
}

func TestMetaIncludesCustomFieldDefs(t *testing.T) {
	agg := &fakeAggregator{
		trackers:        []redmine.Ref{{ID: 1, Name: "Bug"}},
		statuses:        []redmine.Status{{ID: 1, Name: "New"}},
		priorities:      []redmine.Ref{{ID: 2, Name: "Normal"}},
		customFieldDefs: []redmine.CustomFieldDef{{ID: 1, Name: "優先タグ", FieldFormat: "list"}},
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/meta", nil)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "優先タグ") {
		t.Errorf("status = %d body = %s; want customFields in meta", rec.Code, rec.Body)
	}
}

func TestMetaDegradesWhenCustomFieldDefsUnavailable(t *testing.T) {
	// 定義取得が 403 等で失敗しても /api/meta 全体は失敗させない（Design.md §6.4）。
	agg := &fakeAggregator{
		trackers:        []redmine.Ref{{ID: 1, Name: "Bug"}},
		statuses:        []redmine.Status{{ID: 1, Name: "New"}},
		priorities:      []redmine.Ref{{ID: 2, Name: "Normal"}},
		customFieldsErr: redmine.ErrUpstream,
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/meta", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d; want 200 (degraded): %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Bug") {
		t.Errorf("other meta should still be present: %s", rec.Body)
	}
}

// TestMetaConcurrentRequestsDoNotCorruptCachedMap は、同一ユーザーからの
// 並行な GET /api/meta が、キャッシュされた同一マップへ customFields を
// 書き込み合わない（concurrent map writes で fatal しない）ことを確認する。
// meta() は h.Cache.meta.get が返す（10 分キャッシュ内は同一参照の）
// map[string]any に直接 customFields を書き込んではならない。
func TestMetaConcurrentRequestsDoNotCorruptCachedMap(t *testing.T) {
	agg := &fakeAggregator{
		trackers:        []redmine.Ref{{ID: 1, Name: "Bug"}},
		statuses:        []redmine.Status{{ID: 1, Name: "New"}},
		priorities:      []redmine.Ref{{ID: 2, Name: "Normal"}},
		customFieldDefs: []redmine.CustomFieldDef{{ID: 1, Name: "優先タグ", FieldFormat: "list"}},
	}
	mux := newAggMux(agg, &fakeKeyLoader{key: "k"})

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/meta", nil)))
			if rec.Code != 200 {
				t.Errorf("status = %d", rec.Code)
			}
		}()
	}
	wg.Wait()
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
