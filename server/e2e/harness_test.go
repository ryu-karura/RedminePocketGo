//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type rmapp struct {
	cmd *exec.Cmd
	t   *testing.T
}

// startRmapp はサーバーバイナリをビルドし、app/ を配信する設定で
// localhost:18099 に起動して、起動完了まで待つ。
func startRmapp(t *testing.T, redmineURL string) *rmapp {
	t.Helper()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()

	// シークレット（16 進 32 バイト）
	kek := randHex(t, 32)
	sk := randHex(t, 32)
	writeFile(t, filepath.Join(work, "kek.txt"), kek)
	writeFile(t, filepath.Join(work, "session_key.txt"), sk)

	cfg := fmt.Sprintf(`
listen: ":18099"
webroot: %q
serveStatic: true
session:
  secretFile: %q
  secureCookie: false
webauthn:
  rpId: "localhost"
  rpName: "RedminePocketGo"
  origins: ["http://localhost:18099"]
crypto:
  kekFile: %q
redmine:
  baseURL: %q
  subURI: ""
database:
  dsn: %q
`,
		filepath.Join(repoRoot, "app"),
		filepath.Join(work, "session_key.txt"),
		filepath.Join(work, "kek.txt"),
		redmineURL,
		"file:"+filepath.Join(work, "e2e.db"),
	)
	cfgPath := filepath.Join(work, "config.yaml")
	writeFile(t, cfgPath, cfg)

	bin := filepath.Join(work, "rmapp")
	build := exec.Command("go", "build", "-o", bin, "./cmd/rmapp")
	build.Dir = repoRoot + "/server"
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build rmapp: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "-config", cfgPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rmapp: %v", err)
	}
	r := &rmapp{cmd: cmd, t: t}

	// 起動待ち: /api/auth/me が応答するまで
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://localhost:18099/api/auth/me")
		if err == nil {
			resp.Body.Close()
			return r
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.stop()
	t.Fatal("rmapp did not become ready")
	return nil
}

func (r *rmapp) stop() {
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
		_, _ = r.cmd.Process.Wait()
	}
}

func randHex(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func writeFile(t *testing.T, path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
