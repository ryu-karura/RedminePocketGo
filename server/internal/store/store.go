// Package store は SQLite 永続化を担う。テンプレートのインメモリ実装と
// 異なり、users / credentials / sessions を再起動を跨いで保持する
// （パスキーは長寿命であり、再起動で全員ログアウトさせないため）。
package store

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // 純 Go の SQLite ドライバ（cgo 不要）

	"github.com/ryu-karura/RedminePocketGo/server/migrations"
)

type Store struct {
	db *sql.DB
}

// Open は DSN で SQLite を開く。マイグレーションは適用しない（Migrate を
// 明示的に呼ぶ）。SQLite は書き込みが直列のため接続数は 1 に固定する。
// プラグマは接続単位で失われるため Exec ではなく DSN に載せ、再接続後も
// 外部キー制約・busy_timeout・WAL が必ず効くようにする。
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", withPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("store: DB を開けません: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: DB に接続できません: %w", err)
	}
	return &Store{db: db}, nil
}

func withPragmas(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep +
		"_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)"
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

	applied := map[string]bool{}
	rows, err := s.db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("store: 適用状態の取得に失敗しました: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("store: 適用状態の読み取りに失敗しました: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: 適用状態の走査に失敗しました: %w", err)
	}
	rows.Close()

	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("store: マイグレーション一覧の取得に失敗しました: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
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
