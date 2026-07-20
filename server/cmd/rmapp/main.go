// rmapp のエントリポイント。依存の組み立てと起動のみを行い、
// 業務ロジックは internal 配下のパッケージに置く（CLAUDE.md §4.1）。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/auth"
	"github.com/ryu-karura/RedminePocketGo/server/internal/config"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
	"github.com/ryu-karura/RedminePocketGo/server/internal/webfs"
)

var version = "dev"

// stubVault はフェーズ 3（internal/credential）が入るまでの暫定実装。
// 平文保存はしない方針のため、キーは保存せず警告だけ残す（キー自体は
// ログに書かない）。
type stubVault struct{ logger *slog.Logger }

func (v stubVault) SaveAPIKey(_ context.Context, userID, _ string) error {
	v.logger.Warn("credential vault not yet implemented; api key NOT stored", "user_id", userID)
	return nil
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
	wa, err := auth.NewWebAuthn(st, auth.WebAuthnConfig{
		RPID:             cfg.WebAuthn.RPID,
		RPName:           cfg.WebAuthn.RPName,
		Origins:          cfg.WebAuthn.Origins,
		UserVerification: cfg.WebAuthn.UserVerification,
		ChallengeTTL:     time.Duration(cfg.WebAuthn.ChallengeTTLMinutes) * time.Minute,
	})
	if err != nil {
		return err
	}

	var bootstrapSvc httpapi.BootstrapService
	if cfg.Features.PasswordBootstrap {
		bootstrapSvc = auth.NewBootstrap(st, wa, stubVault{logger}, auth.BootstrapConfig{
			BaseURL: cfg.Redmine.BaseURL,
			SubURI:  cfg.Redmine.SubURI,
			Timeout: time.Duration(cfg.Redmine.TimeoutSeconds) * time.Second,
		})
	}

	apiMux := http.NewServeMux()
	// 未実装の /api パスはエンベロープの 404（個別ルートが優先される）。
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteError(w, httpapi.CodeNotFound, "no such endpoint")
	})
	(&httpapi.AuthHandler{
		WebAuthn:   wa,
		Sessions:   sessions,
		Users:      st,
		Limiter:    auth.NewRateLimiter(5, 60*time.Second),
		Bootstrap:  bootstrapSvc,
		Enrollment: auth.NewEnrollment(st, wa),
		CookieName: cfg.Session.CookieName,
	}).RegisterRoutes(apiMux)
	(&httpapi.DeviceHandler{Devices: st}).RegisterRoutes(apiMux)

	mux := http.NewServeMux()
	if cfg.BaseURL != "" {
		mux.Handle(cfg.BaseURL+"/api/", http.StripPrefix(cfg.BaseURL, apiMux))
	} else {
		mux.Handle("/api/", apiMux)
	}
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
