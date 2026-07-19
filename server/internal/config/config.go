// Package config は server/config/config.yaml の読み込みと検証を担う。
// 優先順位はフラグ > 環境変数（接頭辞 RMAPP_）> 設定ファイル > 既定値
// （Design.md §10）。必須キーの欠落はキー名を示して起動を中止する。
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen      string `yaml:"listen"`
	BaseURL     string `yaml:"baseURL"`
	Webroot     string `yaml:"webroot"`
	ServeStatic bool   `yaml:"serveStatic"`
	NoCache     bool   `yaml:"noCache"`
	LogLevel    string `yaml:"logLevel"`

	Session  Session  `yaml:"session"`
	WebAuthn WebAuthn `yaml:"webauthn"`
	Crypto   Crypto   `yaml:"crypto"`
	Redmine  Redmine  `yaml:"redmine"`
	Database Database `yaml:"database"`
	Features Features `yaml:"features"`
}

type Session struct {
	IdleTimeoutHours     int    `yaml:"idleTimeoutHours"`
	AbsoluteTimeoutHours int    `yaml:"absoluteTimeoutHours"`
	SecureCookie         bool   `yaml:"secureCookie"`
	CookieName           string `yaml:"cookieName"`
	SecretFile           string `yaml:"secretFile"`
}

type WebAuthn struct {
	RPID                string   `yaml:"rpId"`
	RPName              string   `yaml:"rpName"`
	Origins             []string `yaml:"origins"`
	UserVerification    string   `yaml:"userVerification"`
	ChallengeTTLMinutes int      `yaml:"challengeTTLMinutes"`
}

type Crypto struct {
	KEKFile    string `yaml:"kekFile"`
	KeyVersion int    `yaml:"keyVersion"`
}

type Redmine struct {
	BaseURL        string `yaml:"baseURL"`
	SubURI         string `yaml:"subURI"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
	MaxRetries     int    `yaml:"maxRetries"`
	MaxConcurrency int    `yaml:"maxConcurrency"`
	PageSize       int    `yaml:"pageSize"`
}

type Database struct {
	DSN string `yaml:"dsn"`
}

type Features struct {
	MapEnabled        bool `yaml:"mapEnabled"`
	IssueCreate       bool `yaml:"issueCreate"`
	PasswordBootstrap bool `yaml:"passwordBootstrap"`
}

// EnvPrefix は環境変数によるオーバーライドの接頭辞。
// キー "session.idleTimeoutHours" は RMAPP_SESSION_IDLETIMEOUTHOURS になる
// （"." を "_" に置換して大文字化）。
const EnvPrefix = "RMAPP_"

// LookupEnv は os.LookupEnv と同じ形。テストから差し替える。
type LookupEnv func(key string) (string, bool)

// setters はオーバーライド可能なキーの全列挙。キー名は config.yaml の
// 階層をドットで結んだもの。
var setters = map[string]func(*Config, string) error{
	"listen":   func(c *Config, v string) error { c.Listen = v; return nil },
	"baseURL":  func(c *Config, v string) error { c.BaseURL = v; return nil },
	"webroot":  func(c *Config, v string) error { c.Webroot = v; return nil },
	"logLevel": func(c *Config, v string) error { c.LogLevel = v; return nil },
	"serveStatic": func(c *Config, v string) error {
		return setBool(&c.ServeStatic, v)
	},
	"noCache": func(c *Config, v string) error { return setBool(&c.NoCache, v) },

	"session.idleTimeoutHours":     func(c *Config, v string) error { return setInt(&c.Session.IdleTimeoutHours, v) },
	"session.absoluteTimeoutHours": func(c *Config, v string) error { return setInt(&c.Session.AbsoluteTimeoutHours, v) },
	"session.secureCookie":         func(c *Config, v string) error { return setBool(&c.Session.SecureCookie, v) },
	"session.cookieName":           func(c *Config, v string) error { c.Session.CookieName = v; return nil },
	"session.secretFile":           func(c *Config, v string) error { c.Session.SecretFile = v; return nil },

	"webauthn.rpId":   func(c *Config, v string) error { c.WebAuthn.RPID = v; return nil },
	"webauthn.rpName": func(c *Config, v string) error { c.WebAuthn.RPName = v; return nil },
	"webauthn.origins": func(c *Config, v string) error {
		c.WebAuthn.Origins = splitList(v)
		return nil
	},
	"webauthn.userVerification":    func(c *Config, v string) error { c.WebAuthn.UserVerification = v; return nil },
	"webauthn.challengeTTLMinutes": func(c *Config, v string) error { return setInt(&c.WebAuthn.ChallengeTTLMinutes, v) },

	"crypto.kekFile":    func(c *Config, v string) error { c.Crypto.KEKFile = v; return nil },
	"crypto.keyVersion": func(c *Config, v string) error { return setInt(&c.Crypto.KeyVersion, v) },

	"redmine.baseURL":        func(c *Config, v string) error { c.Redmine.BaseURL = v; return nil },
	"redmine.subURI":         func(c *Config, v string) error { c.Redmine.SubURI = v; return nil },
	"redmine.timeoutSeconds": func(c *Config, v string) error { return setInt(&c.Redmine.TimeoutSeconds, v) },
	"redmine.maxRetries":     func(c *Config, v string) error { return setInt(&c.Redmine.MaxRetries, v) },
	"redmine.maxConcurrency": func(c *Config, v string) error { return setInt(&c.Redmine.MaxConcurrency, v) },
	"redmine.pageSize":       func(c *Config, v string) error { return setInt(&c.Redmine.PageSize, v) },

	"database.dsn": func(c *Config, v string) error { c.Database.DSN = v; return nil },

	"features.mapEnabled":        func(c *Config, v string) error { return setBool(&c.Features.MapEnabled, v) },
	"features.issueCreate":       func(c *Config, v string) error { return setBool(&c.Features.IssueCreate, v) },
	"features.passwordBootstrap": func(c *Config, v string) error { return setBool(&c.Features.PasswordBootstrap, v) },
}

// Load は path の YAML を読み込み、環境変数とオーバーライド（フラグ由来）を
// 適用し、検証済みの Config を返す。呼び出しは起動時に一度だけ。
func Load(path string, overrides map[string]string, lookupEnv LookupEnv) (*Config, error) {
	cfg := defaults()

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: 設定ファイルを開けません: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true) // タイプミスしたキーを黙って無視しない
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("config: %s に unknown または不正なキーがあります: %w", path, err)
	}

	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	for key, set := range setters {
		envKey := EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		if v, ok := lookupEnv(envKey); ok {
			if err := set(cfg, v); err != nil {
				return nil, fmt.Errorf("config: 環境変数 %s の値が不正です: %w", envKey, err)
			}
		}
	}

	for key, v := range overrides {
		set, ok := setters[key]
		if !ok {
			return nil, fmt.Errorf("config: unknown なオーバーライドキー %q", key)
		}
		if err := set(cfg, v); err != nil {
			return nil, fmt.Errorf("config: オーバーライド %s の値が不正です: %w", key, err)
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Listen:      ":8090",
		Webroot:     "../../app",
		ServeStatic: true,
		NoCache:     true,
		LogLevel:    "info",
		Session: Session{
			IdleTimeoutHours:     168,
			AbsoluteTimeoutHours: 720,
			SecureCookie:         true,
			CookieName:           "rmapp_session",
		},
		WebAuthn: WebAuthn{
			UserVerification:    "required",
			ChallengeTTLMinutes: 5,
		},
		Crypto: Crypto{KeyVersion: 1},
		Redmine: Redmine{
			SubURI:         "/redmine",
			TimeoutSeconds: 10,
			MaxRetries:     2,
			MaxConcurrency: 8,
			PageSize:       100,
		},
		Features: Features{
			IssueCreate:       true,
			PasswordBootstrap: true,
		},
	}
}

func (c *Config) validate() error {
	required := []struct {
		key   string
		empty bool
	}{
		{"session.secretFile", c.Session.SecretFile == ""},
		{"webauthn.rpId", c.WebAuthn.RPID == ""},
		{"webauthn.rpName", c.WebAuthn.RPName == ""},
		{"webauthn.origins", len(c.WebAuthn.Origins) == 0},
		{"crypto.kekFile", c.Crypto.KEKFile == ""},
		{"redmine.baseURL", c.Redmine.BaseURL == ""},
		{"database.dsn", c.Database.DSN == ""},
	}
	for _, r := range required {
		if r.empty {
			return fmt.Errorf("config: 必須キー %s が設定されていません", r.key)
		}
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: logLevel %q は不正です（debug / info / warn / error）", c.LogLevel)
	}

	switch c.WebAuthn.UserVerification {
	case "required", "preferred", "discouraged":
	default:
		return fmt.Errorf("config: webauthn.userVerification %q は不正です（required / preferred / discouraged）", c.WebAuthn.UserVerification)
	}

	if u, err := url.Parse(c.Redmine.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("config: redmine.baseURL %q は URL として不正です", c.Redmine.BaseURL)
	}

	positives := []struct {
		key string
		v   int
	}{
		{"session.idleTimeoutHours", c.Session.IdleTimeoutHours},
		{"session.absoluteTimeoutHours", c.Session.AbsoluteTimeoutHours},
		{"webauthn.challengeTTLMinutes", c.WebAuthn.ChallengeTTLMinutes},
		{"crypto.keyVersion", c.Crypto.KeyVersion},
		{"redmine.timeoutSeconds", c.Redmine.TimeoutSeconds},
		{"redmine.maxConcurrency", c.Redmine.MaxConcurrency},
		{"redmine.pageSize", c.Redmine.PageSize},
	}
	for _, p := range positives {
		if p.v <= 0 {
			return fmt.Errorf("config: %s は正の整数でなければなりません（現在: %d）", p.key, p.v)
		}
	}
	if c.Redmine.MaxRetries < 0 {
		return fmt.Errorf("config: redmine.maxRetries は 0 以上でなければなりません（現在: %d）", c.Redmine.MaxRetries)
	}
	return nil
}

func setBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("真偽値として解釈できません: %q", v)
	}
	*dst = b
	return nil
}

func setInt(dst *int, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("整数として解釈できません: %q", v)
	}
	*dst = n
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
