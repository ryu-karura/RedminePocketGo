package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

type fakeDevices struct {
	creds     []store.Credential
	found     bool
	err       error
	deletedID []byte
}

func (f *fakeDevices) ListCredentialsByUser(context.Context, string) ([]store.Credential, error) {
	return f.creds, f.err
}
func (f *fakeDevices) RenameCredential(context.Context, []byte, string, string) (bool, error) {
	return f.found, f.err
}
func (f *fakeDevices) DeleteCredentialAndSessions(_ context.Context, id []byte, _ string) (bool, error) {
	f.deletedID = id
	return f.found, f.err
}

func TestDeviceEndpoints(t *testing.T) {
	lastUsed := time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC)
	devID := base64.RawURLEncoding.EncodeToString([]byte{9, 9})

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		authed     bool
		svc        fakeDevices
		wantStatus int
		wantBody   string
	}{
		{"list unauthenticated", "GET", "/api/devices", "", false, fakeDevices{}, 401, CodeUnauthenticated},
		{"list ok", "GET", "/api/devices", "", true, fakeDevices{creds: []store.Credential{{
			ID: []byte{9, 9}, DeviceLabel: "iPhone", DeviceKind: "mobile",
			BackupEligible: true, CreatedAt: lastUsed, LastUsedAt: &lastUsed,
		}}}, 200, `"label":"iPhone"`},
		{"list empty is 200", "GET", "/api/devices", "", true, fakeDevices{}, 200, `"devices":[]`},
		{"list service failure", "GET", "/api/devices", "", true, fakeDevices{err: fmt.Errorf("db down")}, 500, CodeInternalError},
		{"rename unauthenticated", "PATCH", "/api/devices/" + devID, `{"label":"x"}`, false, fakeDevices{}, 401, CodeUnauthenticated},
		{"rename malformed id", "PATCH", "/api/devices/!!!", `{"label":"x"}`, true, fakeDevices{}, 400, CodeInvalidRequest},
		{"rename empty label", "PATCH", "/api/devices/" + devID, `{}`, true, fakeDevices{}, 400, CodeInvalidRequest},
		{"rename not found", "PATCH", "/api/devices/" + devID, `{"label":"x"}`, true, fakeDevices{found: false}, 404, CodeNotFound},
		{"rename ok", "PATCH", "/api/devices/" + devID, `{"label":"x"}`, true, fakeDevices{found: true}, 200, `"renamed":true`},
		{"delete unauthenticated", "DELETE", "/api/devices/" + devID, "", false, fakeDevices{}, 401, CodeUnauthenticated},
		{"delete not found", "DELETE", "/api/devices/" + devID, "", true, fakeDevices{found: false}, 404, CodeNotFound},
		{"delete ok", "DELETE", "/api/devices/" + devID, "", true, fakeDevices{found: true}, 200, `"deleted":true`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			(&DeviceHandler{Devices: &tt.svc}).RegisterRoutes(mux)
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.authed {
				req = authedCtx(req)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("status = %d body = %s; want %d containing %q", rec.Code, rec.Body, tt.wantStatus, tt.wantBody)
			}
			if tt.name == "delete ok" && string(tt.svc.deletedID) != string([]byte{9, 9}) {
				t.Errorf("deleted id = %v; want [9 9]", tt.svc.deletedID)
			}
		})
	}
}
