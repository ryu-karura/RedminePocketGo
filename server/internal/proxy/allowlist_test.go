package proxy

import (
	"net/http"
	"testing"
)

func TestAllowed(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		// 許可されるもの
		{http.MethodGet, "/projects.json", true},
		{http.MethodGet, "/projects/42.json", true},
		{http.MethodGet, "/issues.json", true},
		{http.MethodGet, "/issues/100.json", true},
		{http.MethodPut, "/issues/100.json", true},
		{http.MethodPost, "/issues.json", true},
		{http.MethodGet, "/projects/7/memberships.json", true},
		{http.MethodGet, "/projects/7/versions.json", true},
		{http.MethodGet, "/custom_fields.json", true},

		// /my/account.json は中継しない（応答に api_key を含むため。§9-1）
		{http.MethodGet, "/my/account.json", false},

		// メソッド不一致
		{http.MethodDelete, "/issues/1.json", false},
		{http.MethodPost, "/projects.json", false},
		{http.MethodPut, "/projects/1.json", false},

		// 列挙外のパス（管理系・未知）
		{http.MethodGet, "/users.json", false},
		{http.MethodGet, "/groups.json", false},
		{http.MethodGet, "/issues/1/watchers.json", false},

		// プレースホルダはスラッシュを跨がない
		{http.MethodGet, "/projects/1/2.json", false},
		{http.MethodGet, "/issues/1/extra.json", false},

		// 前方一致で透過しない
		{http.MethodGet, "/projects.json/evil", false},
		{http.MethodGet, "/issues.json.bak", false},
		{http.MethodGet, "/projects", false},
	}
	for _, tt := range tests {
		if got := Allowed(tt.method, tt.path); got != tt.want {
			t.Errorf("Allowed(%s, %s) = %v; want %v", tt.method, tt.path, got, tt.want)
		}
	}
}
