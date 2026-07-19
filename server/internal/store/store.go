// Package store は SQLite 永続化を担う。テンプレートのインメモリ実装と
// 異なり、users / credentials / sessions を再起動を跨いで保持する
// （パスキーは長寿命であり、再起動で全員ログアウトさせないため）。
package store

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"

	_ "modernc.org/sqlite" // 純 Go の SQLite ドライバ（cgo 不要）

	"github.com/ryu-karura/RedminePocketGo/server/migrations"
)

type Store struct {
	db *sql.DB
}

// Open は DSN で SQLite を開く。マイグレーションは適用しない（Migrate を
// 明示的に呼ぶ）。SQLite は書き込みが直列のため接続数は 1 に固定し、
// 外部キー制約と busy_timeout を接続に対して有効化する。
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: DB を開けません: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: %s に失敗しました: %w", pragma, err)
		}
	}
	return &Store{db: db}, nil
}

// DB は下位層の *sql.DB を返す。リポジトリ実装（後続フェーズ）とテストが使う。
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

// Migrate は埋め込みマイグレーションを名前順に適用する。適用済みの
// バージョンは schema_migrations で管理し、再実行しても安全（冪等）。
func (s *Store) Migrate() error {
	if _, err := s.db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	); err != nil {
		return fmt.Errorf("store: schema_migrations の作成に失敗しました: %w", err)
	}

	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("store: マイグレーション一覧の取得に失敗しました: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		var applied int
		if err := s.db.QueryRow(
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("store: 適用状態の確認に失敗しました (%s): %w", name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return fmt.Errorf("store: %s を読めません: %w", name, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: トランザクション開始に失敗しました (%s): %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: マイグレーション %s の適用に失敗しました: %w", name, err)
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version) VALUES (?)", name,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: %s の適用記録に失敗しました: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: %s のコミットに失敗しました: %w", name, err)
		}
	}
	return nil
}
