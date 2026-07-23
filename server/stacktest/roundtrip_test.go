//go:build stack

// Package stacktest は、起動中の RedmineDocker 開発スタック（実 Redmine）に
// 対して許可リスト経由で 1 往復する統合テスト。scripts/test-stack.sh から
// のみ実行され、RedmineDocker の起動が前提のため通常の go test / CI
// （test-unit・test-api）には含まれない（CLAUDE.md §5、docs/plan.md フェーズ 8）。
package stacktest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/config"
	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/proxy"
)

const stackTestUserID = "stacktest-user"

// staticKeyLoader は保管庫（credential.Vault）を介さず、環境変数から得た
// 実 API キーをそのまま返す proxy.KeyLoader 実装。credential.NewTestAPIKey
// はまさにこの「中継層のテストで保管庫を介さない配線」向けに用意されている。
type staticKeyLoader struct{ key string }

func (s staticKeyLoader) LoadAPIKey(context.Context, string) (*credential.APIKey, error) {
	return credential.NewTestAPIKey(s.key), nil
}
func (s staticKeyLoader) MarkInvalid(context.Context, string) error { return nil }

func withStackTestSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := httpapi.WithSession(r.Context(), &httpapi.SessionInfo{UserID: stackTestUserID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TestProxyRoundTripAgainstRealRedmine は許可リスト上の GET /issues.json を
// 実際の Redmine へ中継し、有効な JSON が返ることを確認する
// (CLAUDE.md §5「許可リスト経由の往復 1 件」)。
func TestProxyRoundTripAgainstRealRedmine(t *testing.T) {
	apiKey := os.Getenv("RMAPP_STACK_API_KEY")
	if apiKey == "" {
		t.Fatal("RMAPP_STACK_API_KEY が未設定です。docs/Setup.md §3.3 の手順で取得した Redmine API キーを設定してください")
	}
	cfgPath := os.Getenv("RMAPP_STACK_CONFIG")
	if cfgPath == "" {
		cfgPath = "../config/config.yaml"
	}

	cfg, err := config.Load(cfgPath, nil, nil)
	if err != nil {
		t.Fatalf("設定の読み込みに失敗しました（%s）: %v", cfgPath, err)
	}

	relay := proxy.New(staticKeyLoader{key: apiKey}, proxy.Config{
		BaseURL: cfg.Redmine.BaseURL,
		SubURI:  cfg.Redmine.SubURI,
		Timeout: time.Duration(cfg.Redmine.TimeoutSeconds) * time.Second,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/redmine/", relay.Handler("/api/redmine"))

	srv := httptest.NewServer(withStackTestSession(mux))
	defer srv.Close()

	// 上流がハングした場合でもテスト自体は時間内に終わるよう、既定の
	// http.DefaultClient（タイムアウトなし）ではなく明示的な期限を設ける。
	// リレー自身のコンテキスト期限（redmine.timeoutSeconds）に猶予を足す。
	client := &http.Client{Timeout: time.Duration(cfg.Redmine.TimeoutSeconds)*time.Second + 5*time.Second}
	resp, err := client.Get(srv.URL + "/api/redmine/issues.json?limit=1")
	if err != nil {
		t.Fatalf("GET /api/redmine/issues.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200（許可リスト経由の Redmine 往復に失敗。"+
			"redmine.baseURL/subURI と API キーを確認してください）", resp.StatusCode)
	}
	var body struct {
		Issues []json.RawMessage `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("応答が issues.json の形になっていません: %v", err)
	}
}
