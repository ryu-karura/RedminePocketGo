package credential

import (
	"context"
	"errors"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

// Upstream は Redmine REST クライアント（*redmine.Client）が満たす集約用の
// 取得口。トークンは呼び出しごとに引数で渡す。
type Upstream interface {
	ListProjects(ctx context.Context, token string) ([]redmine.Project, error)
	ListProjectIssues(ctx context.Context, token string, projectID int) ([]redmine.Issue, error)
	GetIssue(ctx context.Context, token string, id int) (*redmine.Issue, error)
	CountOpenIssues(ctx context.Context, token string, projectID int) (int, error)
	ListTrackers(ctx context.Context, token string) ([]redmine.Ref, error)
	ListStatuses(ctx context.Context, token string) ([]redmine.Status, error)
	ListPriorities(ctx context.Context, token string) ([]redmine.Ref, error)
	ListCustomFieldDefs(ctx context.Context, token string) ([]redmine.CustomFieldDef, error)
	ListProjectVersions(ctx context.Context, token string, projectID int) ([]redmine.Version, error)
	ListProjectMemberships(ctx context.Context, token string, projectID int) ([]redmine.Membership, error)
	GetAttachment(ctx context.Context, token string, id int) (*redmine.Attachment, error)
}

// Authed は Upstream の各メソッドの「トークン」引数を「利用者 ID」に置き換える
// 装飾役。呼び出しごとに有効なアクセストークンを Manager から得て上流へ渡し、
// 上流が 401 を返したら更新して 1 回だけ再試行する。2 回目も 401 なら
// ErrUnauthorized をそのまま返す（呼び出し側が組を無効にして再認可を求める）。
//
// これにより、集約ハンドラはトークンの取得・更新・再試行を一切意識しない。
// 各メソッドの第 2 引数（string）は、ここでは利用者 ID を指す。
type Authed struct {
	up     Upstream
	tokens *Manager
}

func NewAuthed(up Upstream, tokens *Manager) *Authed { return &Authed{up: up, tokens: tokens} }

func withToken[T any](ctx context.Context, a *Authed, userID string, call func(token string) (T, error)) (T, error) {
	var zero T
	token, err := a.tokens.AccessToken(ctx, userID)
	if err != nil {
		return zero, err
	}
	v, err := call(token)
	if !errors.Is(err, redmine.ErrUnauthorized) {
		return v, err
	}
	token, rerr := a.tokens.ForceRefresh(ctx, userID, token)
	if rerr != nil {
		return zero, rerr
	}
	return call(token)
}

func (a *Authed) ListProjects(ctx context.Context, userID string) ([]redmine.Project, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.Project, error) { return a.up.ListProjects(ctx, t) })
}

func (a *Authed) ListProjectIssues(ctx context.Context, userID string, projectID int) ([]redmine.Issue, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.Issue, error) { return a.up.ListProjectIssues(ctx, t, projectID) })
}

func (a *Authed) GetIssue(ctx context.Context, userID string, id int) (*redmine.Issue, error) {
	return withToken(ctx, a, userID, func(t string) (*redmine.Issue, error) { return a.up.GetIssue(ctx, t, id) })
}

func (a *Authed) CountOpenIssues(ctx context.Context, userID string, projectID int) (int, error) {
	return withToken(ctx, a, userID, func(t string) (int, error) { return a.up.CountOpenIssues(ctx, t, projectID) })
}

func (a *Authed) ListTrackers(ctx context.Context, userID string) ([]redmine.Ref, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.Ref, error) { return a.up.ListTrackers(ctx, t) })
}

func (a *Authed) ListStatuses(ctx context.Context, userID string) ([]redmine.Status, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.Status, error) { return a.up.ListStatuses(ctx, t) })
}

func (a *Authed) ListPriorities(ctx context.Context, userID string) ([]redmine.Ref, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.Ref, error) { return a.up.ListPriorities(ctx, t) })
}

func (a *Authed) ListCustomFieldDefs(ctx context.Context, userID string) ([]redmine.CustomFieldDef, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.CustomFieldDef, error) { return a.up.ListCustomFieldDefs(ctx, t) })
}

func (a *Authed) ListProjectVersions(ctx context.Context, userID string, projectID int) ([]redmine.Version, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.Version, error) { return a.up.ListProjectVersions(ctx, t, projectID) })
}

func (a *Authed) ListProjectMemberships(ctx context.Context, userID string, projectID int) ([]redmine.Membership, error) {
	return withToken(ctx, a, userID, func(t string) ([]redmine.Membership, error) { return a.up.ListProjectMemberships(ctx, t, projectID) })
}

func (a *Authed) GetAttachment(ctx context.Context, userID string, id int) (*redmine.Attachment, error) {
	return withToken(ctx, a, userID, func(t string) (*redmine.Attachment, error) { return a.up.GetAttachment(ctx, t, id) })
}
