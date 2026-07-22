package config

import "testing"

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
	if !cfg.Session.SecureCookie {
		// 開発値でも secureCookie は既定 true のまま明示しておく方針
		t.Errorf("session.secureCookie = false; 雛形は true を明示すること")
	}
}
