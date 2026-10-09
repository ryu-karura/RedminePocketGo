package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validYAML は必須キーをすべて満たす最小の設定ファイル。
const validYAML = `
session:
  secretFile: /tmp/session_key.txt
webauthn:
  rpId: example.com
  rpName: RedminePocketGo
  origins:
    - https://example.com
crypto:
  kekFile: /tmp/kek.txt
redmine:
  baseURL: http://localhost:8080
  oauth:
    clientId: rmapp-client
    clientSecretFile: /tmp/redmine_oauth_client_secret.txt
    redirectURI: https://example.com/api/auth/callback
    scopes: [view_project, view_issues]
database:
  dsn: "file:data/rmapp.db?_fk=1"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noEnv(string) (string, bool) { return "", false }

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML), nil, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"listen", cfg.Listen, ":8090"},
		{"baseURL", cfg.BaseURL, ""},
		{"webroot", cfg.Webroot, "../../app"},
		{"serveStatic", cfg.ServeStatic, true},
		{"noCache", cfg.NoCache, true},
		{"logLevel", cfg.LogLevel, "info"},
		{"session.idleTimeoutHours", cfg.Session.IdleTimeoutHours, 168},
		{"session.absoluteTimeoutHours", cfg.Session.AbsoluteTimeoutHours, 720},
		{"session.secureCookie", cfg.Session.SecureCookie, true},
		{"session.cookieName", cfg.Session.CookieName, "rmapp_session"},
		{"webauthn.userVerification", cfg.WebAuthn.UserVerification, "required"},
		{"webauthn.challengeTTLMinutes", cfg.WebAuthn.ChallengeTTLMinutes, 5},
		{"crypto.keyVersion", cfg.Crypto.KeyVersion, 1},
		{"redmine.subURI", cfg.Redmine.SubURI, "/redmine"},
		{"redmine.timeoutSeconds", cfg.Redmine.TimeoutSeconds, 10},
		{"redmine.maxRetries", cfg.Redmine.MaxRetries, 2},
		{"redmine.maxConcurrency", cfg.Redmine.MaxConcurrency, 8},
		{"redmine.pageSize", cfg.Redmine.PageSize, 100},
		{"features.mapEnabled", cfg.Features.MapEnabled, false},
		{"features.issueCreate", cfg.Features.IssueCreate, true},
		{"features.passwordBootstrap", cfg.Features.PasswordBootstrap, true},
		{"redmine.publicBaseURL", cfg.Redmine.PublicBaseURL, "http://localhost:8080"},
		{"redmine.oauth.clientId", cfg.Redmine.OAuth.ClientID, "rmapp-client"},
		{"redmine.oauth.stateTTLMinutes", cfg.Redmine.OAuth.StateTTLMinutes, 10},
		{"redmine.oauth.refreshSkewSeconds", cfg.Redmine.OAuth.RefreshSkewSeconds, 60},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v; want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestLoadMissingRequiredKey(t *testing.T) {
	tests := []struct {
		key    string // エラーメッセージに含まれるべきキー名
		remove string // validYAML から取り除く行の目印
	}{
		{"session.secretFile", "secretFile"},
		{"webauthn.rpId", "rpId"},
		{"webauthn.rpName", "rpName"},
		{"webauthn.origins", "origins"},
		{"crypto.kekFile", "kekFile"},
		{"redmine.baseURL", "baseURL"},
		{"database.dsn", "dsn"},
		{"redmine.oauth.clientId", "clientId"},
		{"redmine.oauth.clientSecretFile", "clientSecretFile"},
		{"redmine.oauth.redirectURI", "redirectURI"},
		{"redmine.oauth.scopes", "scopes"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			var lines []string
			skipNext := false
			for _, line := range strings.Split(validYAML, "\n") {
				if skipNext { // origins のリスト行
					skipNext = false
					continue
				}
				if strings.Contains(line, tt.remove) {
					if tt.remove == "origins" {
						skipNext = true
					}
					continue
				}
				lines = append(lines, line)
			}
			_, err := Load(writeConfig(t, strings.Join(lines, "\n")), nil, noEnv)
			if err == nil {
				t.Fatalf("missing %s: want error, got nil", tt.key)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q does not name key %q", err, tt.key)
			}
		})
	}
}

func TestLoadInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		wants string
	}{
		{"bad logLevel", validYAML + "logLevel: verbose\n", "logLevel"},
		{"bad userVerification", strings.Replace(validYAML, "rpName: RedminePocketGo", "rpName: RedminePocketGo\n  userVerification: always", 1), "userVerification"},
		{"unknown key", validYAML + "unknownKey: 1\n", "unknown"},
		{"bad redmine URL", strings.Replace(validYAML, "http://localhost:8080", "'::not a url'", 1), "redmine.baseURL"},
		{"redirectURI not absolute", strings.Replace(validYAML, "https://example.com/api/auth/callback", "/api/auth/callback", 1), "redmine.oauth.redirectURI"},
		{"redirectURI plain http on public host", strings.Replace(validYAML, "https://example.com/api/auth/callback", "http://example.com/api/auth/callback", 1), "redmine.oauth.redirectURI"},
		{"redirectURI wrong path", strings.Replace(validYAML, "https://example.com/api/auth/callback", "https://example.com/callback", 1), "redmine.oauth.redirectURI"},
		{"redirectURI with query", strings.Replace(validYAML, "https://example.com/api/auth/callback", "https://example.com/api/auth/callback?x=1", 1), "redmine.oauth.redirectURI"},
		{"scope with space-like junk", strings.Replace(validYAML, "[view_project, view_issues]", "[\"view issues\"]", 1), "redmine.oauth.scopes"},
		{"admin scope forbidden", strings.Replace(validYAML, "[view_project, view_issues]", "[view_project, admin]", 1), "redmine.oauth.scopes"},
		{"bad publicBaseURL", strings.Replace(validYAML, "  oauth:", "  publicBaseURL: \"::nope\"\n  oauth:", 1), "redmine.publicBaseURL"},
		{"non-positive stateTTL", strings.Replace(validYAML, "clientId: rmapp-client", "clientId: rmapp-client\n    stateTTLMinutes: 0", 1), "redmine.oauth.stateTTLMinutes"},
		{"negative refreshSkew", strings.Replace(validYAML, "clientId: rmapp-client", "clientId: rmapp-client\n    refreshSkewSeconds: -1", 1), "redmine.oauth.refreshSkewSeconds"},
		{"non-positive timeout", strings.Replace(validYAML, "baseURL: http://localhost:8080", "baseURL: http://localhost:8080\n  timeoutSeconds: 0", 1), "redmine.timeoutSeconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.yaml), nil, noEnv)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wants)) {
				t.Errorf("error %q does not mention %q", err, tt.wants)
			}
		})
	}
}

func TestLoadPrecedence(t *testing.T) {
	// ファイルに listen を書き、env がそれに勝ち、flag(overrides) が env に勝つ。
	path := writeConfig(t, validYAML+"listen: \":1111\"\n")

	env := func(key string) (string, bool) {
		switch key {
		case "RMAPP_LISTEN":
			return ":2222", true
		case "RMAPP_LOGLEVEL":
			return "debug", true
		case "RMAPP_SESSION_IDLETIMEOUTHOURS":
			return "24", true
		case "RMAPP_WEBAUTHN_ORIGINS":
			return "https://a.example,https://b.example", true
		case "RMAPP_REDMINE_OAUTH_SCOPES":
			return "view_project, view_issues ,add_issues", true
		case "RMAPP_REDMINE_PUBLICBASEURL":
			return "https://redmine.example/", true
		}
		return "", false
	}

	cfg, err := Load(path, map[string]string{"listen": ":3333"}, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":3333" {
		t.Errorf("flag should beat env and file: listen = %q; want :3333", cfg.Listen)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("env should beat default: logLevel = %q; want debug", cfg.LogLevel)
	}
	if cfg.Session.IdleTimeoutHours != 24 {
		t.Errorf("env int override: idleTimeoutHours = %d; want 24", cfg.Session.IdleTimeoutHours)
	}
	if got := strings.Join(cfg.Redmine.OAuth.Scopes, " "); got != "view_project view_issues add_issues" {
		t.Errorf("env scopes override = %q", got)
	}
	if cfg.Redmine.PublicBaseURL != "https://redmine.example" {
		t.Errorf("publicBaseURL = %q; 末尾スラッシュは除去される", cfg.Redmine.PublicBaseURL)
	}
	want := []string{"https://a.example", "https://b.example"}
	if len(cfg.WebAuthn.Origins) != 2 || cfg.WebAuthn.Origins[0] != want[0] || cfg.WebAuthn.Origins[1] != want[1] {
		t.Errorf("env list override: origins = %v; want %v", cfg.WebAuthn.Origins, want)
	}
}

func TestLoadBadEnvValue(t *testing.T) {
	env := func(key string) (string, bool) {
		if key == "RMAPP_NOCACHE" {
			return "yes-please", true
		}
		return "", false
	}
	_, err := Load(writeConfig(t, validYAML), nil, env)
	if err == nil {
		t.Fatal("want error for unparsable bool env, got nil")
	}
	if !strings.Contains(err.Error(), "RMAPP_NOCACHE") {
		t.Errorf("error %q does not name RMAPP_NOCACHE", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), nil, noEnv)
	if err == nil {
		t.Fatal("want error for missing file, got nil")
	}
}

func TestLoadUnknownOverrideKey(t *testing.T) {
	_, err := Load(writeConfig(t, validYAML), map[string]string{"nope.key": "x"}, noEnv)
	if err == nil {
		t.Fatal("want error for unknown override key, got nil")
	}
}

func TestBaseURLMustStartWithSlash(t *testing.T) {
	_, err := Load(writeConfig(t, validYAML+"baseURL: rmapp\n"), nil, noEnv)
	if err == nil || !strings.Contains(err.Error(), "baseURL") {
		t.Fatalf("baseURL without leading slash: err = %v; want error naming baseURL", err)
	}
	if _, err := Load(writeConfig(t, validYAML+"baseURL: /rmapp\n"), nil, noEnv); err != nil {
		t.Fatalf("valid /rmapp rejected: %v", err)
	}
}

func TestSubURIValidation(t *testing.T) {
	tests := []struct {
		subURI string
		wantOK bool
	}{
		{"/redmine", true},
		{"", true},         // ルート配信は許容
		{"redmine", false}, // 先頭スラッシュなし
		{"/redmine/", false},
	}
	for _, tt := range tests {
		yaml := strings.Replace(validYAML, "baseURL: http://localhost:8080",
			"baseURL: http://localhost:8080\n  subURI: \""+tt.subURI+"\"", 1)
		_, err := Load(writeConfig(t, yaml), nil, noEnv)
		if tt.wantOK && err != nil {
			t.Errorf("subURI %q: unexpected error %v", tt.subURI, err)
		}
		if !tt.wantOK && (err == nil || !strings.Contains(err.Error(), "subURI")) {
			t.Errorf("subURI %q: err = %v; want error naming subURI", tt.subURI, err)
		}
	}
}

func TestLoadClientSecret(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr string // 空なら成功を期待
	}{
		{"value with trailing newline", write("ok.txt", "s3cr3t\n"), "s3cr3t", ""},
		{"empty file", write("empty.txt", ""), "", "redmine.oauth.clientSecretFile"},
		{"whitespace only", write("ws.txt", " \n\t\n"), "", "redmine.oauth.clientSecretFile"},
		{"missing file", filepath.Join(dir, "nope.txt"), "", "redmine.oauth.clientSecretFile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := OAuth{ClientSecretFile: tt.path}
			got, err := o.LoadClientSecret()
			if tt.wantErr == "" {
				if err != nil || got != tt.want {
					t.Fatalf("LoadClientSecret = %q, %v; want %q", got, err, tt.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v; want error naming %s", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("error leaks secret content: %v", err)
			}
		})
	}
}

func TestOAuthRedirectURIAllowsLoopbackHTTP(t *testing.T) {
	for _, host := range []string{"localhost:8090", "127.0.0.1:8090", "[::1]:8090"} {
		yaml := strings.Replace(validYAML, "https://example.com/api/auth/callback", "http://"+host+"/api/auth/callback", 1)
		if _, err := Load(writeConfig(t, yaml), nil, noEnv); err != nil {
			t.Errorf("loopback http redirectURI %s rejected: %v", host, err)
		}
	}
}
