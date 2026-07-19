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

	"github.com/ryu-karura/RedminePocketGo/server/internal/config"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
	"github.com/ryu-karura/RedminePocketGo/server/internal/webfs"
)

var version = "dev"

func main() {
	if err := run(os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rmapp:", err)
		os.Exit(1)
	}
}

// noopResolver はセッション解決の暫定実装。internal/auth（フェーズ 2）で
// 置き換わるまで、全リクエストを未認証として扱う。
type noopResolver struct{}

func (noopResolver) ResolveSession(context.Context, string) (*httpapi.SessionInfo, error) {
	return nil, nil
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

	mux := http.NewServeMux()
	// /api 配下は後続フェーズでハンドラが増える。未実装のパスはエンベロープの 404。
	mux.HandleFunc(cfg.BaseURL+"/api/", func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteError(w, httpapi.CodeNotFound, "no such endpoint")
	})
	if cfg.ServeStatic {
		mux.Handle("/", webfs.Handler(cfg.Webroot, cfg.BaseURL, cfg.NoCache))
	}

	handler := httpapi.Chain(logger, noopResolver{}, cfg.Session.CookieName)(mux)

	logger.Info("rmapp starting", "listen", cfg.Listen, "version", version)
	return http.ListenAndServe(cfg.Listen, handler)
}
