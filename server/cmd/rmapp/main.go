// rmapp のエントリポイント。依存の組み立てと起動のみを行い、
// 業務ロジックは internal 配下のパッケージに置く（CLAUDE.md §4.1）。
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/auth"
	"github.com/ryu-karura/RedminePocketGo/server/internal/config"
	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/proxy"
	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
	"github.com/ryu-karura/RedminePocketGo/server/internal/webfs"
)

var version = "dev"

// readKEK はファイルから KEK を読み込む。ファイルは 16 進または生バイトの
// どちらでもよい（generate-secrets.sh は 16 進を書き出す）。KEK 自体は
// ログにもエラーにも出さない（CLAUDE.md §4.4）。
func readKEK(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("crypto.kekFile を読めません: %w", err)
	}
	// generate-secrets.sh は 64 桁の 16 進を書き出す。まずそれを優先。
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 64 {
		if decoded, err := hex.DecodeString(trimmed); err == nil {
			return decoded, nil
		}
	}
	// 生の 32 バイト（末尾改行の有無を許容）。生バイトは TrimSpace しない。
	if len(raw) == 32 {
		return raw, nil
	}
	if len(trimmed) == 32 {
		return []byte(trimmed), nil
	}
	return nil, fmt.Errorf("crypto.kekFile は 64 桁の 16 進または 32 バイトである必要があります")
}

func main() {
	if err := run(os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rmapp:", err)
		os.Exit(1)
	}
}

func run(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("rmapp", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		configPath  = fs.String("config", "config/config.yaml", "設定ファイルのパス")
		listen      = fs.String("listen", "", "待ち受けアドレス（設定ファイルより優先）")
		logLevel    = fs.String("logLevel", "", "ログレベル（設定ファイルより優先）")
		showVersion = fs.Bool("version", false, "バージョンを表示して終了")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintf(out, "rmapp %s\n", version)
		return nil
	}

	// フラグ > 環境変数 > ファイル > 既定値（config.Load が後段を担う）
	overrides := map[string]string{}
	if *listen != "" {
		overrides["listen"] = *listen
	}
	if *logLevel != "" {
		overrides["logLevel"] = *logLevel
	}

	cfg, err := config.Load(*configPath, overrides, nil)
	if err != nil {
		return err
	}

	// Redmine で発行したクライアントシークレットが置かれていること（空・欠落は
	// キー名付きで起動を中止する）。値はここでは使わない。
	clientSecret, err := cfg.Redmine.OAuth.LoadClientSecret()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel, os.Stderr)
	slog.SetDefault(logger)

	st, err := store.Open(cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		return err
	}

	sessions := auth.NewSessions(st, auth.Config{
		IdleTimeout:     time.Duration(cfg.Session.IdleTimeoutHours) * time.Hour,
		AbsoluteTimeout: time.Duration(cfg.Session.AbsoluteTimeoutHours) * time.Hour,
		CookieName:      cfg.Session.CookieName,
		SecureCookie:    cfg.Session.SecureCookie,
	})
	kek, err := readKEK(cfg.Crypto.KEKFile)
	if err != nil {
		return err
	}
	vault, err := credential.NewVault(st, kek, cfg.Crypto.KeyVersion)
	if err != nil {
		return err
	}

	apiMux := http.NewServeMux()
	// 未実装の /api パスはエンベロープの 404（個別ルートが優先される）。
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteError(w, httpapi.CodeNotFound, "no such endpoint")
	})
	// Redmine への接続。トークンは Manager が供給・更新し、中継と集約の両方が
	// 同じ Manager を使う（利用者ごとの更新を 1 本に直列化するため）。
	rmClient := redmine.NewClient(redmine.Config{
		BaseURL:        cfg.Redmine.BaseURL,
		SubURI:         cfg.Redmine.SubURI,
		Timeout:        time.Duration(cfg.Redmine.TimeoutSeconds) * time.Second,
		MaxRetries:     cfg.Redmine.MaxRetries,
		MaxConcurrency: cfg.Redmine.MaxConcurrency,
		PageSize:       cfg.Redmine.PageSize,
	})
	rmOAuth := rmClient.OAuth(redmine.OAuthConfig{
		ClientID:      cfg.Redmine.OAuth.ClientID,
		ClientSecret:  clientSecret,
		RedirectURI:   cfg.Redmine.OAuth.RedirectURI,
		Scopes:        cfg.Redmine.OAuth.Scopes,
		PublicBaseURL: cfg.Redmine.PublicBaseURL,
	})
	tokens := credential.NewManager(vault, rmOAuth, time.Duration(cfg.Redmine.OAuth.RefreshSkewSeconds)*time.Second)

	// ログイン完了とログアウト後始末が同じ利用者の組を取り合わないための排他。
	userLocks := auth.NewUserLocks()

	grantCleaner := &auth.GrantCleaner{
		Store: st, Vault: vault, OAuth: rmOAuth, Logger: logger, Locks: userLocks,
		IdleTimeout: time.Duration(cfg.Session.IdleTimeoutHours) * time.Hour,
	}

	// 認証: OAuth ログイン / コールバック（Design.md §3.3）と現在セッションの API。
	stateTTL := time.Duration(cfg.Redmine.OAuth.StateTTLMinutes) * time.Minute
	(&httpapi.OAuthHandler{
		Login: auth.NewOAuthLogin(auth.OAuthLoginDeps{
			Store: st, Vault: vault, OAuth: rmOAuth, Identity: rmClient, Sessions: sessions, StateTTL: stateTTL, Locks: userLocks, Revoker: rmOAuth, Logger: logger,
		}),
		Sessions:          sessions,
		Limiter:           auth.NewRateLimiter(5, 60*time.Second),
		Logger:            logger,
		TrustedProxies:    cfg.TrustedProxyNets(),
		SessionCookieName: cfg.Session.CookieName,
		StateCookieName:   "rmapp_oauth_state",
		StateCookiePath:   cfg.BaseURL + "/api/auth/",
		StateCookieSecure: cfg.Session.SecureCookie,
		StateCookieTTL:    stateTTL,
		AppURL:            cfg.BaseURL + "/",
	}).RegisterRoutes(apiMux)
	(&httpapi.AuthHandler{
		Sessions: sessions,
		Users:    st,
		Grants:   st,
		Cleanup:  grantCleaner,
		Logger:   logger,

		CookieName: cfg.Session.CookieName,
		LoginPath:  cfg.BaseURL + "/api/auth/login",
	}).RegisterRoutes(apiMux)

	// Redmine 中継（許可リスト経由。/api/redmine/ 配下）
	relay := proxy.New(tokens, proxy.Config{
		BaseURL: cfg.Redmine.BaseURL,
		SubURI:  cfg.Redmine.SubURI,
		Timeout: time.Duration(cfg.Redmine.TimeoutSeconds) * time.Second,
	})
	apiMux.HandleFunc("/api/redmine/", relay.Handler("/api/redmine"))

	// 集約 API（画面向け。ツリー化・詳細・メタ）
	(&httpapi.AggregateHandler{
		Redmine: credential.NewAuthed(rmClient, tokens),
		Gate:    tokens,
		Cache:   httpapi.NewAggCache(),
		Logger:  logger,
	}).RegisterRoutes(apiMux)

	mux := http.NewServeMux()
	if cfg.BaseURL != "" {
		mux.Handle(cfg.BaseURL+"/api/", http.StripPrefix(cfg.BaseURL, apiMux))
	} else {
		mux.Handle("/api/", apiMux)
	}
	// 運用監視エンドポイント（Setup.md §11）。baseURL 非対象・常にルート直下。
	(&httpapi.HealthHandler{Upstream: rmClient}).RegisterRoutes(mux)
	if cfg.ServeStatic {
		mux.Handle("/", webfs.Handler(cfg.Webroot, cfg.BaseURL, cfg.NoCache))
	}

	handler := httpapi.Chain(logger, sessions, cfg.Session.CookieName)(mux)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// SIGINT / SIGTERM で受け付けを止め、処理中のリクエストを待ってから
	// 返る（defer の st.Close を確実に走らせる）。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 無操作・絶対期限でセッションを失った利用者の付与を Redmine 側でも失効させる。
	go sweepGrants(ctx, grantCleaner, grantSweepInterval)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("rmapp starting", "listen", cfg.Listen, "version", version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("rmapp shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// grantSweepInterval は孤児になった OAuth 付与の掃除間隔。
const grantSweepInterval = time.Hour

// sweepGrants は起動直後と interval ごとに SweepOrphans を呼ぶ。ctx の終了で止まる。
func sweepGrants(ctx context.Context, c *auth.GrantCleaner, interval time.Duration) {
	c.SweepOrphans(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.SweepOrphans(ctx)
		}
	}
}
