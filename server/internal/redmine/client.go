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
	StartDate   string       `json:"start_date,omitempty"`
	DueDate     string       `json:"due_date,omitempty"`
	DoneRatio   int          `json:"done_ratio"`
	CreatedOn   string       `json:"created_on,omitempty"`
	UpdatedOn   string       `json:"updated_on,omitempty"`
	Journals    []Journal    `json:"journals,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
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
