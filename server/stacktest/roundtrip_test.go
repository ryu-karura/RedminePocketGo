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

// staticTokens は保管庫・更新を介さず、環境変数から得た実アクセストークンを
// そのまま返す proxy.TokenSource 実装。トークンは scripts/redmine-seed-testdata.sh が
// Redmine 側（rails runner）で発行する。Redmine の API キーは使わない
// （CLAUDE.md §9-1）。401 のときは更新せず、そのまま失敗させる。
type staticTokens struct{ token string }

func (s staticTokens) AccessToken(context.Context, string) (string, error) { return s.token, nil }
func (s staticTokens) ForceRefresh(_ context.Context, _, _ string) (string, error) {
	return "", credential.ErrCredentialInvalid
}
func (s staticTokens) MarkInvalid(context.Context, string) error { return nil }

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
	accessToken := os.Getenv("RMAPP_STACK_ACCESS_TOKEN")
	if accessToken == "" {
		t.Fatal("RMAPP_STACK_ACCESS_TOKEN が未設定です。scripts/redmine-seed-testdata.sh が Redmine で発行する OAuth アクセストークンを設定してください")
	}
	cfgPath := os.Getenv("RMAPP_STACK_CONFIG")
	if cfgPath == "" {
		cfgPath = "../config/config.yaml"
	}

	cfg, err := config.Load(cfgPath, nil, nil)
	if err != nil {
		t.Fatalf("設定の読み込みに失敗しました（%s）: %v", cfgPath, err)
	}

	relay := proxy.New(staticTokens{token: accessToken}, proxy.Config{
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
			"redmine.baseURL/subURI とアクセストークンを確認してください）", resp.StatusCode)
	}
	var body struct {
		Issues []json.RawMessage `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("応答が issues.json の形になっていません: %v", err)
	}
}
