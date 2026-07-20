// Package proxy は Redmine REST API への中継を担う。中継できるのは
// allowlist に明示列挙した (メソッド, パスパターン) の組だけで、一致しない
// リクエストは 404 になる（前方一致の一括透過はしない。Design.md §6.2）。
package proxy

import (
	"net/http"
	"regexp"
	"strings"
)

// rule は許可された 1 経路。pattern は `/projects/{id}.json` のような
// プレースホルダ記法で書き、コンパイル時に正規表現へ変換する。
type rule struct {
	method  string
	pattern string
	re      *regexp.Regexp
}

// allowlist は許可する (メソッド, パス) の宣言的列挙（Design.md §6.2）。
// パスは Redmine のサブ URI 配下（`/redmine` を除いた API パス）で書く。
// 管理系（ユーザー作成・グループ等）は列挙しない。
var allowlist = compileRules([]struct{ method, pattern string }{
	{http.MethodGet, "/projects.json"},
	{http.MethodGet, "/projects/{id}.json"},
	{http.MethodGet, "/issues.json"},
	{http.MethodGet, "/issues/{id}.json"},
	{http.MethodPut, "/issues/{id}.json"},
	{http.MethodPost, "/issues.json"},
	{http.MethodGet, "/issue_statuses.json"},
	{http.MethodGet, "/trackers.json"},
	{http.MethodGet, "/enumerations/issue_priorities.json"},
	{http.MethodGet, "/projects/{id}/memberships.json"},
	{http.MethodGet, "/attachments/{id}.json"},
	{http.MethodGet, "/my/account.json"},
})

// placeholderRe は `{id}` などのプレースホルダを見つける。
var placeholderRe = regexp.MustCompile(`\{[a-zA-Z_]+\}`)

func compileRules(specs []struct{ method, pattern string }) []rule {
	rules := make([]rule, 0, len(specs))
	for _, s := range specs {
		// プレースホルダ以外はリテラルとしてエスケープし、`{id}` は
		// スラッシュを含まない 1 セグメント（数値または語）にマッチさせる。
		var b strings.Builder
		b.WriteString("^")
		last := 0
		for _, loc := range placeholderRe.FindAllStringIndex(s.pattern, -1) {
			b.WriteString(regexp.QuoteMeta(s.pattern[last:loc[0]]))
			b.WriteString(`[^/]+`)
			last = loc[1]
		}
		b.WriteString(regexp.QuoteMeta(s.pattern[last:]))
		b.WriteString("$")
		rules = append(rules, rule{
			method:  s.method,
			pattern: s.pattern,
			re:      regexp.MustCompile(b.String()),
		})
	}
	return rules
}

// Allowed は (メソッド, APIパス) が許可リストに一致するかを返す。
// path は `/redmine` を含まない API パス（例 `/issues/42.json`）。
func Allowed(method, path string) bool {
	for _, r := range allowlist {
		if r.method == method && r.re.MatchString(path) {
			return true
		}
	}
	return false
}
