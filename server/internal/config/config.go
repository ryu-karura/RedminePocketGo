// Package config は server/config/config.yaml の読み込みと検証を担う。
// 優先順位はフラグ > 環境変数（接頭辞 RMAPP_）> 設定ファイル > 既定値
// （Design.md §10）。必須キーの欠落はキー名を示して起動を中止する。
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
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
	// TrustedProxies は X-Forwarded-For を信用してよい直接の接続元（CIDR か
	// 単一 IP）。リバースプロキシ（Host Apache）のアドレスを列挙する。
	// 空ならヘッダーを一切信用しない。既定はループバックのみ。
	TrustedProxies []string `yaml:"trustedProxies"`

	trustedNets []*net.IPNet

	Session  Session  `yaml:"session"`
	Crypto   Crypto   `yaml:"crypto"`
	Redmine  Redmine  `yaml:"redmine"`
	Database Database `yaml:"database"`
	Features Features `yaml:"features"`
}

// TrustedProxyNets は検証済みの trustedProxies を返す（Load 後に有効）。
func (c *Config) TrustedProxyNets() []*net.IPNet { return c.trustedNets }

type Session struct {
	IdleTimeoutHours     int    `yaml:"idleTimeoutHours"`
	AbsoluteTimeoutHours int    `yaml:"absoluteTimeoutHours"`
	SecureCookie         bool   `yaml:"secureCookie"`
	CookieName           string `yaml:"cookieName"`
	SecretFile           string `yaml:"secretFile"`
}

type Crypto struct {
	KEKFile    string `yaml:"kekFile"`
	KeyVersion int    `yaml:"keyVersion"`
}

type Redmine struct {
	BaseURL string `yaml:"baseURL"`
	// PublicBaseURL はブラウザから見える Redmine の起点 URL（/oauth/authorize
	// への遷移に使う）。空なら BaseURL。末尾スラッシュは除去される。
	PublicBaseURL  string `yaml:"publicBaseURL"`
	SubURI         string `yaml:"subURI"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
	MaxRetries     int    `yaml:"maxRetries"`
	MaxConcurrency int    `yaml:"maxConcurrency"`
	PageSize       int    `yaml:"pageSize"`

	OAuth OAuth `yaml:"oauth"`
}

// OAuth は Redmine に登録した OAuth アプリケーション（Doorkeeper）の設定
// （Design.md §10.3）。
type OAuth struct {
	ClientID string `yaml:"clientId"`
	// ClientSecretFile はクライアントシークレットのファイル。値そのものは
	// 設定ファイルに書かない（CLAUDE.md §4.5）。
	ClientSecretFile   string   `yaml:"clientSecretFile"`
	RedirectURI        string   `yaml:"redirectURI"`
	Scopes             []string `yaml:"scopes"`
	StateTTLMinutes    int      `yaml:"stateTTLMinutes"`
	RefreshSkewSeconds int      `yaml:"refreshSkewSeconds"`
}

// callbackPath は redirectURI の末尾に必須のパス（internal/auth の
// コールバックルートと一致させる）。
const callbackPath = "/api/auth/callback"

// LoadClientSecret はクライアントシークレットファイルを読み、前後の空白を
// 除いた値を返す。ファイルが無い・空の場合は、キー名付きのエラーにする
// （値自体はエラーに含めない）。
func (o OAuth) LoadClientSecret() (string, error) {
	b, err := os.ReadFile(o.ClientSecretFile)
	if err != nil {
		return "", fmt.Errorf("config: redmine.oauth.clientSecretFile を読めません（Redmine で発行したシークレットを置いてください）: %w", err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("config: redmine.oauth.clientSecretFile %q が空です（Redmine のアプリケーション登録時に一度だけ表示される Client Secret を書き込んでください）", o.ClientSecretFile)
	}
	return v, nil
}

type Database struct {
	DSN string `yaml:"dsn"`
}

type Features struct {
	MapEnabled  bool `yaml:"mapEnabled"`
	IssueCreate bool `yaml:"issueCreate"`
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
	"trustedProxies": func(c *Config, v string) error {
		c.TrustedProxies = splitList(v)
		return nil
	},

	"session.idleTimeoutHours":     func(c *Config, v string) error { return setInt(&c.Session.IdleTimeoutHours, v) },
	"session.absoluteTimeoutHours": func(c *Config, v string) error { return setInt(&c.Session.AbsoluteTimeoutHours, v) },
	"session.secureCookie":         func(c *Config, v string) error { return setBool(&c.Session.SecureCookie, v) },
	"session.cookieName":           func(c *Config, v string) error { c.Session.CookieName = v; return nil },
	"session.secretFile":           func(c *Config, v string) error { c.Session.SecretFile = v; return nil },

	"crypto.kekFile":    func(c *Config, v string) error { c.Crypto.KEKFile = v; return nil },
	"crypto.keyVersion": func(c *Config, v string) error { return setInt(&c.Crypto.KeyVersion, v) },

	"redmine.baseURL":        func(c *Config, v string) error { c.Redmine.BaseURL = v; return nil },
	"redmine.publicBaseURL":  func(c *Config, v string) error { c.Redmine.PublicBaseURL = v; return nil },
	"redmine.subURI":         func(c *Config, v string) error { c.Redmine.SubURI = v; return nil },
	"redmine.timeoutSeconds": func(c *Config, v string) error { return setInt(&c.Redmine.TimeoutSeconds, v) },
	"redmine.maxRetries":     func(c *Config, v string) error { return setInt(&c.Redmine.MaxRetries, v) },
	"redmine.maxConcurrency": func(c *Config, v string) error { return setInt(&c.Redmine.MaxConcurrency, v) },
	"redmine.pageSize":       func(c *Config, v string) error { return setInt(&c.Redmine.PageSize, v) },

	"redmine.oauth.clientId":         func(c *Config, v string) error { c.Redmine.OAuth.ClientID = v; return nil },
	"redmine.oauth.clientSecretFile": func(c *Config, v string) error { c.Redmine.OAuth.ClientSecretFile = v; return nil },
	"redmine.oauth.redirectURI":      func(c *Config, v string) error { c.Redmine.OAuth.RedirectURI = v; return nil },
	"redmine.oauth.scopes": func(c *Config, v string) error {
		c.Redmine.OAuth.Scopes = splitList(v)
		return nil
	},
	"redmine.oauth.stateTTLMinutes":    func(c *Config, v string) error { return setInt(&c.Redmine.OAuth.StateTTLMinutes, v) },
	"redmine.oauth.refreshSkewSeconds": func(c *Config, v string) error { return setInt(&c.Redmine.OAuth.RefreshSkewSeconds, v) },

	"database.dsn": func(c *Config, v string) error { c.Database.DSN = v; return nil },

	"features.mapEnabled":  func(c *Config, v string) error { return setBool(&c.Features.MapEnabled, v) },
	"features.issueCreate": func(c *Config, v string) error { return setBool(&c.Features.IssueCreate, v) },
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
		// Host Apache は同一ホストのループバックから中継する想定。
		TrustedProxies: []string{"127.0.0.0/8", "::1/128"},
		Session: Session{
			IdleTimeoutHours:     168,
			AbsoluteTimeoutHours: 720,
			SecureCookie:         true,
			CookieName:           "rmapp_session",
		},
		Crypto: Crypto{KeyVersion: 1},
		Redmine: Redmine{
			SubURI:         "/redmine",
			TimeoutSeconds: 10,
			MaxRetries:     2,
			MaxConcurrency: 8,
			PageSize:       100,
			OAuth: OAuth{
				StateTTLMinutes:    10,
				RefreshSkewSeconds: 60,
			},
		},
		Features: Features{
			IssueCreate: true,
		},
	}
}

func (c *Config) validate() error {
	required := []struct {
		key   string
		empty bool
	}{
		{"session.secretFile", c.Session.SecretFile == ""},
		{"crypto.kekFile", c.Crypto.KEKFile == ""},
		{"redmine.baseURL", c.Redmine.BaseURL == ""},
		{"redmine.oauth.clientId", c.Redmine.OAuth.ClientID == ""},
		{"redmine.oauth.clientSecretFile", c.Redmine.OAuth.ClientSecretFile == ""},
		{"redmine.oauth.redirectURI", c.Redmine.OAuth.RedirectURI == ""},
		{"redmine.oauth.scopes", len(c.Redmine.OAuth.Scopes) == 0},
		{"database.dsn", c.Database.DSN == ""},
	}
	for _, r := range required {
		if r.empty {
			return fmt.Errorf("config: 必須キー %s が設定されていません", r.key)
		}
	}

	if c.BaseURL != "" && !strings.HasPrefix(c.BaseURL, "/") {
		return fmt.Errorf("config: baseURL %q は \"/\" で始まらなければなりません", c.BaseURL)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: logLevel %q は不正です（debug / info / warn / error）", c.LogLevel)
	}

	nets, err := parseTrustedProxies(c.TrustedProxies)
	if err != nil {
		return err
	}
	c.trustedNets = nets

	if u, err := url.Parse(c.Redmine.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("config: redmine.baseURL %q は URL として不正です", c.Redmine.BaseURL)
	}

	// ブラウザ向けの起点 URL。未指定ならサーバー間通信用と同じ。
	c.Redmine.PublicBaseURL = strings.TrimRight(c.Redmine.PublicBaseURL, "/")
	if c.Redmine.PublicBaseURL == "" {
		c.Redmine.PublicBaseURL = strings.TrimRight(c.Redmine.BaseURL, "/")
	}
	if u, err := url.Parse(c.Redmine.PublicBaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("config: redmine.publicBaseURL %q は URL として不正です", c.Redmine.PublicBaseURL)
	}

	if err := c.Redmine.OAuth.validate(); err != nil {
		return err
	}

	// サブ URI は上流 URL の結合に直接使う。先頭スラッシュ必須・末尾
	// スラッシュ禁止にして、二重スラッシュや host 直結の事故を防ぐ。
	if c.Redmine.SubURI != "" {
		if !strings.HasPrefix(c.Redmine.SubURI, "/") || strings.HasSuffix(c.Redmine.SubURI, "/") {
			return fmt.Errorf("config: redmine.subURI %q は \"/\" で始まり \"/\" で終わらない必要があります", c.Redmine.SubURI)
		}
	}

	positives := []struct {
		key string
		v   int
	}{
		{"session.idleTimeoutHours", c.Session.IdleTimeoutHours},
		{"session.absoluteTimeoutHours", c.Session.AbsoluteTimeoutHours},
		{"crypto.keyVersion", c.Crypto.KeyVersion},
		{"redmine.timeoutSeconds", c.Redmine.TimeoutSeconds},
		{"redmine.maxConcurrency", c.Redmine.MaxConcurrency},
		{"redmine.pageSize", c.Redmine.PageSize},
		{"redmine.oauth.stateTTLMinutes", c.Redmine.OAuth.StateTTLMinutes},
	}
	for _, p := range positives {
		if p.v <= 0 {
			return fmt.Errorf("config: %s は正の整数でなければなりません（現在: %d）", p.key, p.v)
		}
	}
	if c.Redmine.OAuth.RefreshSkewSeconds < 0 {
		return fmt.Errorf("config: redmine.oauth.refreshSkewSeconds は 0 以上でなければなりません（現在: %d）", c.Redmine.OAuth.RefreshSkewSeconds)
	}
	if c.Redmine.MaxRetries < 0 {
		return fmt.Errorf("config: redmine.maxRetries は 0 以上でなければなりません（現在: %d）", c.Redmine.MaxRetries)
	}
	return nil
}

var scopeName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (o OAuth) validate() error {
	u, err := url.Parse(o.RedirectURI)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("config: redmine.oauth.redirectURI %q は query / fragment を持たない絶対 URL でなければなりません", o.RedirectURI)
	}
	switch u.Scheme {
	case "https":
	case "http":
		// 開発用にループバックのみ平文を許す（本番は TLS 必須: Design.md §11.1）。
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			return fmt.Errorf("config: redmine.oauth.redirectURI %q は https でなければなりません（http はループバックのみ）", o.RedirectURI)
		}
	default:
		return fmt.Errorf("config: redmine.oauth.redirectURI %q のスキームが不正です", o.RedirectURI)
	}
	if !strings.HasSuffix(u.Path, callbackPath) {
		return fmt.Errorf("config: redmine.oauth.redirectURI %q は %q で終わらなければなりません", o.RedirectURI, callbackPath)
	}

	for _, sc := range o.Scopes {
		if !scopeName.MatchString(sc) {
			return fmt.Errorf("config: redmine.oauth.scopes に不正な名前 %q があります（Redmine の権限名: 英小文字・数字・_）", sc)
		}
		if sc == "admin" {
			// 管理者スコープは要求しない（最小権限。Design.md §3.6）。
			return fmt.Errorf("config: redmine.oauth.scopes に admin は指定できません（最小権限の方針）")
		}
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

// parseTrustedProxies は CIDR または単一 IP の一覧を IPNet に変換する。
func parseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("config: trustedProxies の要素 %q は IP でも CIDR でもありません", e)
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("config: trustedProxies の要素 %q は CIDR として不正です: %w", e, err)
		}
		out = append(out, n)
	}
	return out, nil
}
