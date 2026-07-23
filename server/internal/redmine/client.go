// Package redmine は Redmine REST API の型付きクライアントと、画面向けの
// 集約（ツリー化・詳細・メタ）を担う（Design.md §6.4、§9）。
package redmine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized は API キーが上流に拒否された（401）。呼び出し側は
// キーを無効化して 409 を返す。
var ErrUnauthorized = errors.New("redmine: API キーが拒否されました")

// ErrUpstream は上流（Redmine）の一時的・恒久的障害。呼び出し側は 502 に
// 写像する。httpapi へ依存しないよう redmine 側に番兵を置く（import 循環回避）。
var ErrUpstream = errors.New("redmine: 上流障害")

// Config は接続設定（config.Redmine から組み立てる）。
type Config struct {
	BaseURL        string
	SubURI         string
	Timeout        time.Duration
	MaxRetries     int
	MaxConcurrency int
	PageSize       int
}

// Client は Redmine への型付きアクセスを提供する。
type Client struct {
	http        *http.Client
	root        string // baseURL + subURI
	maxRetries  int
	pageSize    int
	sem         chan struct{} // 同時接続数の上限（Design.md §9）
	backoffBase time.Duration
}

func NewClient(cfg Config) *Client {
	return &Client{
		http:        &http.Client{Timeout: cfg.Timeout},
		root:        strings.TrimSuffix(cfg.BaseURL, "/") + cfg.SubURI,
		maxRetries:  cfg.MaxRetries,
		pageSize:    cfg.PageSize,
		sem:         make(chan struct{}, max(1, cfg.MaxConcurrency)),
		backoffBase: 200 * time.Millisecond,
	}
}

// ---- 型（画面が必要とする項目のみ） ----

// Ref は id + name の参照。
type Ref struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Project struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Parent     *Ref   `json:"parent,omitempty"`
}

type Issue struct {
	ID          int    `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
	Project     Ref    `json:"project"`
	Tracker     Ref    `json:"tracker"`
	Status      Ref    `json:"status"`
	Priority    Ref    `json:"priority"`
	AssignedTo  *Ref   `json:"assigned_to,omitempty"`
	Parent      *struct {
		ID int `json:"id"`
	} `json:"parent,omitempty"`
	StartDate    string             `json:"start_date,omitempty"`
	DueDate      string             `json:"due_date,omitempty"`
	DoneRatio    int                `json:"done_ratio"`
	CreatedOn    string             `json:"created_on,omitempty"`
	UpdatedOn    string             `json:"updated_on,omitempty"`
	Journals     []Journal          `json:"journals,omitempty"`
	Attachments  []Attachment       `json:"attachments,omitempty"`
	CustomFields []CustomFieldValue `json:"custom_fields,omitempty"`
}

// CustomFieldValue はチケットに設定されたカスタムフィールドの値 1 件。
// Value は素の値（文字列）、複数選択（配列）、未設定（null）のいずれも
// そのまま受け取る（型定義は CustomFieldDef 側が持つ。Design.md §6.4）。
type CustomFieldValue struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Value any    `json:"value"`
}

type Journal struct {
	ID        int    `json:"id"`
	Notes     string `json:"notes"`
	User      Ref    `json:"user"`
	CreatedOn string `json:"created_on"`
}

type Attachment struct {
	ID          int    `json:"id"`
	Filename    string `json:"filename"`
	Filesize    int64  `json:"filesize"`
	ContentType string `json:"content_type,omitempty"`
	ContentURL  string `json:"content_url,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
}

type Status struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	IsClosed bool   `json:"is_closed"`
}

// PossibleValue は list / key_value_list フォーマットの選択肢 1 件。
// Redmine は素の文字列（値=ラベル）か `{"value":..,"label":..}` の
// いずれかで返すため、両方を受け付ける（Design.md §6.4）。
type PossibleValue struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func (v *PossibleValue) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		v.Value, v.Label = s, s
		return nil
	}
	var obj struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("redmine: possible_value の解釈に失敗しました: %w", err)
	}
	v.Value = obj.Value
	v.Label = obj.Label
	if v.Label == "" {
		v.Label = v.Value
	}
	return nil
}

// CustomFieldDef はカスタムフィールドの定義（表示順・必須可否・長さ/上下限・
// 選択肢）。Redmine 上の並び順どおりにチケットへ返される値と id で突合する
// （Design.md §6.4、§7.8）。
type CustomFieldDef struct {
	ID             int             `json:"id"`
	Name           string          `json:"name"`
	CustomizedType string          `json:"customized_type"`
	FieldFormat    string          `json:"field_format"`
	IsRequired     bool            `json:"is_required"`
	Multiple       bool            `json:"multiple"`
	MinLength      int             `json:"min_length"`
	MaxLength      int             `json:"max_length"`
	PossibleValues []PossibleValue `json:"possible_values,omitempty"`
}

// Version はプロジェクトのバージョン（`version` フォーマットのカスタム
// フィールドを参照解決するために使う。Design.md §7.8）。
type Version struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Membership はプロジェクトのメンバー 1 件。グループのメンバーシップは
// User が nil（`user` フォーマットのカスタムフィールドの参照解決では
// スキップする。Design.md §7.8）。
type Membership struct {
	ID   int  `json:"id"`
	User *Ref `json:"user,omitempty"`
}

// ---- 取得 ----

// get は 1 リクエストを実行し JSON を v に読む。一時的な失敗（接続エラー、
// 502、503）に限り指数バックオフで最大 maxRetries 回再試行する。4xx は
// 再試行しない（Design.md §9）。
func (c *Client) get(ctx context.Context, apiKey, path string, query url.Values, v any) error {
	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	u := c.root + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("redmine: リクエスト作成に失敗しました: %w", err)
		}
		req.Header.Set("X-Redmine-Api-Key", apiKey)

		resp, err := c.http.Do(req)
		switch {
		case err != nil:
			// クライアント側の中断・期限切れは上流障害として数えない
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("redmine: リクエストが中断されました: %w", ctxErr)
			}
			lastErr = fmt.Errorf("%w: %w", ErrUpstream, err)
		case resp.StatusCode == http.StatusOK:
			defer resp.Body.Close()
			if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
				return fmt.Errorf("%w: レスポンスの解釈に失敗しました: %w", ErrUpstream, err)
			}
			return nil
		case resp.StatusCode == http.StatusUnauthorized:
			resp.Body.Close()
			return ErrUnauthorized
		case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable:
			resp.Body.Close()
			lastErr = fmt.Errorf("%w: 一時的な上流障害 (status %d)", ErrUpstream, resp.StatusCode)
		default:
			resp.Body.Close()
			return fmt.Errorf("%w: 上流が status %d を返しました", ErrUpstream, resp.StatusCode)
		}

		if attempt >= c.maxRetries {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("redmine: リクエストが中断されました: %w", ctx.Err())
		case <-time.After(backoff(c.backoffBase, attempt)): // 指数バックオフ
		}
	}
}

// backoff は base << attempt を返すが、桁あふれ（負値化）と過大待機を防ぐため
// 上限で頭打ちにする。
func backoff(base time.Duration, attempt int) time.Duration {
	const maxBackoff = 30 * time.Second
	if attempt < 0 || attempt >= 32 {
		return maxBackoff
	}
	d := base << attempt
	if d <= 0 || d > maxBackoff {
		return maxBackoff
	}
	return d
}

// paginate は offset/limit で全ページを取得する。fetch は 1 ページを取得し
// (件数, total_count) を返す。offset は要求 limit（pageSize）ずつ進める
// ——返り件数で進めると、途中ページが limit 未満のとき次ページと重複して
// 同じレコードを二重取得してしまうため。
func (c *Client) paginate(fetch func(offset int) (count, total int, err error)) error {
	offset := 0
	for {
		count, total, err := fetch(offset)
		if err != nil {
			return err
		}
		offset += c.pageSize
		if count == 0 || offset >= total {
			return nil
		}
	}
}

// ListProjects は全プロジェクトを取得する（ページング）。
func (c *Client) ListProjects(ctx context.Context, apiKey string) ([]Project, error) {
	var out []Project
	err := c.paginate(func(offset int) (int, int, error) {
		var page struct {
			Projects   []Project `json:"projects"`
			TotalCount int       `json:"total_count"`
		}
		q := url.Values{
			"offset": {strconv.Itoa(offset)},
			"limit":  {strconv.Itoa(c.pageSize)},
		}
		if err := c.get(ctx, apiKey, "/projects.json", q, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Projects...)
		return len(page.Projects), page.TotalCount, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListProjectIssues はプロジェクト配下のチケットを取得する（ページング）。
// closed も含めて取得し、表示側で折りたたむ（Design.md §7.7）。
func (c *Client) ListProjectIssues(ctx context.Context, apiKey string, projectID int) ([]Issue, error) {
	var out []Issue
	err := c.paginate(func(offset int) (int, int, error) {
		var page struct {
			Issues     []Issue `json:"issues"`
			TotalCount int     `json:"total_count"`
		}
		q := url.Values{
			"project_id": {strconv.Itoa(projectID)},
			"status_id":  {"*"},
			"offset":     {strconv.Itoa(offset)},
			"limit":      {strconv.Itoa(c.pageSize)},
		}
		if err := c.get(ctx, apiKey, "/issues.json", q, &page); err != nil {
			return 0, 0, err
		}
		out = append(out, page.Issues...)
		return len(page.Issues), page.TotalCount, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CountOpenIssues はプロジェクト直下（サブプロジェクトを除く）の未完了チケット
// 数を返す。件数だけが必要なので limit=1 として total_count のみを読む
// （Design.md §7.6 のプロジェクト一覧右端の数字）。
func (c *Client) CountOpenIssues(ctx context.Context, apiKey string, projectID int) (int, error) {
	var page struct {
		TotalCount int `json:"total_count"`
	}
	q := url.Values{
		"project_id":    {strconv.Itoa(projectID)},
		"status_id":     {"open"},
		"subproject_id": {"!*"}, // 各ノードの数字を独立させる（子の件数を重複計上しない）
		"limit":         {"1"},
	}
	if err := c.get(ctx, apiKey, "/issues.json", q, &page); err != nil {
		return 0, err
	}
	return page.TotalCount, nil
}

// GetIssue はチケット本体を履歴・添付込みで取得する。
func (c *Client) GetIssue(ctx context.Context, apiKey string, id int) (*Issue, error) {
	var wrap struct {
		Issue Issue `json:"issue"`
	}
	q := url.Values{"include": {"journals,attachments"}}
	if err := c.get(ctx, apiKey, "/issues/"+strconv.Itoa(id)+".json", q, &wrap); err != nil {
		return nil, err
	}
	return &wrap.Issue, nil
}

// ListTrackers はトラッカー一覧を返す。
func (c *Client) ListTrackers(ctx context.Context, apiKey string) ([]Ref, error) {
	var wrap struct {
		Trackers []Ref `json:"trackers"`
	}
	if err := c.get(ctx, apiKey, "/trackers.json", nil, &wrap); err != nil {
		return nil, err
	}
	return wrap.Trackers, nil
}

// ListStatuses はステータス一覧を返す（closed 判定込み）。
func (c *Client) ListStatuses(ctx context.Context, apiKey string) ([]Status, error) {
	var wrap struct {
		Statuses []Status `json:"issue_statuses"`
	}
	if err := c.get(ctx, apiKey, "/issue_statuses.json", nil, &wrap); err != nil {
		return nil, err
	}
	return wrap.Statuses, nil
}

// ListCustomFieldDefs はカスタムフィールドの定義一覧を返す
// （`customized_type=="issue"` のみに絞る）。上流仕様上、管理者権限が
// 必要なエンドポイントのため、非管理者アカウントでは ErrUpstream（403）
// になりうる——呼び出し側（httpapi）は定義なしの degrade 表示に切り替える
// こと（Design.md §6.4）。
func (c *Client) ListCustomFieldDefs(ctx context.Context, apiKey string) ([]CustomFieldDef, error) {
	var wrap struct {
		CustomFields []CustomFieldDef `json:"custom_fields"`
	}
	if err := c.get(ctx, apiKey, "/custom_fields.json", nil, &wrap); err != nil {
		return nil, err
	}
	out := make([]CustomFieldDef, 0, len(wrap.CustomFields))
	for _, d := range wrap.CustomFields {
		if d.CustomizedType == "issue" {
			out = append(out, d)
		}
	}
	return out, nil
}

// ListProjectVersions はプロジェクトのバージョン一覧を返す
// （`version` フォーマットのカスタムフィールド参照解決用）。
func (c *Client) ListProjectVersions(ctx context.Context, apiKey string, projectID int) ([]Version, error) {
	var wrap struct {
		Versions []Version `json:"versions"`
	}
	path := "/projects/" + strconv.Itoa(projectID) + "/versions.json"
	if err := c.get(ctx, apiKey, path, nil, &wrap); err != nil {
		return nil, err
	}
	return wrap.Versions, nil
}

// ListProjectMemberships はプロジェクトのメンバー一覧を返す
// （`user` フォーマットのカスタムフィールド参照解決用）。
func (c *Client) ListProjectMemberships(ctx context.Context, apiKey string, projectID int) ([]Membership, error) {
	var wrap struct {
		Memberships []Membership `json:"memberships"`
	}
	path := "/projects/" + strconv.Itoa(projectID) + "/memberships.json"
	if err := c.get(ctx, apiKey, path, nil, &wrap); err != nil {
		return nil, err
	}
	return wrap.Memberships, nil
}

// ListPriorities は優先度一覧を返す。
func (c *Client) ListPriorities(ctx context.Context, apiKey string) ([]Ref, error) {
	var wrap struct {
		Priorities []Ref `json:"issue_priorities"`
	}
	if err := c.get(ctx, apiKey, "/enumerations/issue_priorities.json", nil, &wrap); err != nil {
		return nil, err
	}
	return wrap.Priorities, nil
}
