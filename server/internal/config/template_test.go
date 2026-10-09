package config

import (
	"strings"
	"testing"
)

// リポジトリ同梱の設定雛形が常に Load を通ることを保証する。
// 雛形が必須キーを欠いたり不正値を含んだりした時点でこのテストが落ちる。
func TestLoadShippedTemplate(t *testing.T) {
	cfg, err := Load("../../config/config.yaml", nil, noEnv)
	if err != nil {
		t.Fatalf("同梱の config.yaml が Load を通りません: %v", err)
	}
	if cfg.Redmine.SubURI != "/redmine" {
		t.Errorf("redmine.subURI = %q; want /redmine", cfg.Redmine.SubURI)
	}
	o := cfg.Redmine.OAuth
	if got := strings.Join(o.Scopes, " "); got != "view_project view_issues add_issues edit_issues add_issue_notes view_members" {
		t.Errorf("redmine.oauth.scopes = %q; Design.md §3.6 の初期値であること", got)
	}
	if o.ClientSecretFile != "../secrets/redmine_oauth_client_secret.txt" {
		t.Errorf("redmine.oauth.clientSecretFile = %q", o.ClientSecretFile)
	}
	if !cfg.Session.SecureCookie {
		// 開発値でも secureCookie は既定 true のまま明示しておく方針
		t.Errorf("session.secureCookie = false; 雛形は true を明示すること")
	}
}
