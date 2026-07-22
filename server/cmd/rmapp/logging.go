package main

import (
	"io"
	"log/slog"
)

// newLogger は設定の logLevel から構造化 JSON ロガーを作る。
// 記録禁止項目（ボディ、Cookie、セッション ID、API キー、WebAuthn の
// チャレンジ・署名）は各呼び出し側の責務（CLAUDE.md §4.6）。
func newLogger(level string, w io.Writer) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default: // "info"（config が検証済みのため他の値は来ない）
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l}))
}
