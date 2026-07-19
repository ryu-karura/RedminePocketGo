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
