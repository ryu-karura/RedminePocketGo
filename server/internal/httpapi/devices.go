package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// DeviceService は端末（パスキー）管理。*store.Store が実装する。
type DeviceService interface {
	ListCredentialsByUser(ctx context.Context, userID string) ([]store.Credential, error)
	RenameCredential(ctx context.Context, id []byte, userID, label string) (bool, error)
	DeleteCredentialAndSessions(ctx context.Context, id []byte, userID string) (bool, error)
}

// DeviceHandler は設定画面の端末管理 API を提供する（Design.md §7.9）。
type DeviceHandler struct {
	Devices DeviceService
}

func (h *DeviceHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/devices", h.list)
	mux.HandleFunc("PATCH /api/devices/{id}", h.rename)
	mux.HandleFunc("DELETE /api/devices/{id}", h.remove)
}

type deviceJSON struct {
	ID             string `json:"id"` // Credential ID の base64url
	Label          string `json:"label"`
	Kind           string `json:"kind"`
	BackupEligible bool   `json:"backupEligible"`
	CreatedAt      string `json:"createdAt"`
	LastUsedAt     string `json:"lastUsedAt,omitempty"`
}

func (h *DeviceHandler) list(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	creds, err := h.Devices.ListCredentialsByUser(r.Context(), sess.UserID)
	if err != nil {
		WriteError(w, CodeInternalError, "device list failed")
		return
	}
	out := make([]deviceJSON, 0, len(creds))
	for _, c := range creds {
		d := deviceJSON{
			ID:             base64.RawURLEncoding.EncodeToString(c.ID),
			Label:          c.DeviceLabel,
			Kind:           c.DeviceKind,
			BackupEligible: c.BackupEligible,
			CreatedAt:      c.CreatedAt.Format(time.RFC3339),
		}
		if c.LastUsedAt != nil {
			d.LastUsedAt = c.LastUsedAt.Format(time.RFC3339)
		}
		out = append(out, d)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (h *DeviceHandler) rename(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	id, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil {
		WriteError(w, CodeInvalidRequest, "malformed device id")
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Label == "" {
		WriteError(w, CodeInvalidRequest, "label is required")
		return
	}
	ok, err := h.Devices.RenameCredential(r.Context(), id, sess.UserID, body.Label)
	if err != nil {
		WriteError(w, CodeInternalError, "rename failed")
		return
	}
	if !ok {
		WriteError(w, CodeNotFound, "no such device")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"renamed": true})
}

// remove はパスキーを削除する。該当パスキーで発行された全セッションが
// 同時に失効する（削除した端末は即座にログアウトされる）。
func (h *DeviceHandler) remove(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	id, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil {
		WriteError(w, CodeInvalidRequest, "malformed device id")
		return
	}
	ok, err := h.Devices.DeleteCredentialAndSessions(r.Context(), id, sess.UserID)
	if err != nil {
		WriteError(w, CodeInternalError, "delete failed")
		return
	}
	if !ok {
		WriteError(w, CodeNotFound, "no such device")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
