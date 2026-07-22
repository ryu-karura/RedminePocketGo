package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// UpstreamPinger は readyz が上流（Redmine）到達性を確認するために使う。
// internal/redmine.Client がこれを満たす。
type UpstreamPinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler は運用監視向けのエンドポイントを提供する（Setup.md §11）。
// baseURL の対象外、常にルート直下（/healthz, /readyz）で配信する。
type HealthHandler struct {
	Upstream UpstreamPinger
}

func (h *HealthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
}

// healthz はプロセスが HTTP に応答できるかだけを見る（依存先の障害と無関係。
// liveness）。
func (h *HealthHandler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeHealthStatus(w, http.StatusOK, "ok", "")
}

// readyz は Redmine への到達性を確認する（readiness）。
func (h *HealthHandler) readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.Upstream.Ping(r.Context()); err != nil {
		writeHealthStatus(w, http.StatusServiceUnavailable, "error", "upstream unreachable")
		return
	}
	writeHealthStatus(w, http.StatusOK, "ok", "")
}

func writeHealthStatus(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	body := map[string]string{"status": status}
	if message != "" {
		body["message"] = message
	}
	_ = json.NewEncoder(w).Encode(body)
}
