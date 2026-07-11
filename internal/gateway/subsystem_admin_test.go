package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/store"
)

func TestAdminPublicHealth(t *testing.T) {
	admin := newTestAdmin(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	admin.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", rr.Header().Get("Content-Type"))
	}
}

func TestAdminSessionsRequiresCorrectBearerToken(t *testing.T) {
	db := openAdminTestDB(t)
	admin := newTestAdmin(t, db)
	tests := []struct {
		name string
		auth string
	}{
		{name: "missing"},
		{name: "wrong", auth: "Bearer wrong-token"},
		{name: "wrong scheme", auth: "Basic correct-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
			req.Header.Set("Authorization", tt.auth)
			rr := httptest.NewRecorder()

			admin.handler().ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
			}
			if rr.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", rr.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestAdminSessionsReturnsJSONWithoutToken(t *testing.T) {
	db := openAdminTestDB(t)
	if _, err := db.Exec(`INSERT INTO sessions (id, channel, channel_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"session-1", "telegram", "channel-1", "2026-07-11T10:00:00Z", "2026-07-11T11:00:00Z"); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	admin := newTestAdmin(t, db)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	rr := httptest.NewRecorder()

	admin.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var sessions []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0]["id"] != "session-1" {
		t.Fatalf("sessions = %#v, want inserted session", sessions)
	}
	if strings.Contains(rr.Body.String(), "correct-token") {
		t.Fatal("response exposed bearer token")
	}
}

func TestAdminSessionsRejectsUnsupportedMethodBeforeDatabase(t *testing.T) {
	admin := newTestAdmin(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	rr := httptest.NewRecorder()

	admin.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	if rr.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", rr.Header().Get("Allow"))
	}
}

func TestAdminUnknownAPIReturnsNotFoundBeforeDatabase(t *testing.T) {
	admin := newTestAdmin(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/unknown", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	rr := httptest.NewRecorder()

	admin.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestAdminDatabaseErrorIsGeneric(t *testing.T) {
	db := openAdminTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	admin := newTestAdmin(t, db)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	rr := httptest.NewRecorder()

	admin.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	if rr.Body.String() != http.StatusText(http.StatusInternalServerError)+"\n" {
		t.Fatalf("body = %q, want generic status text", rr.Body.String())
	}
}

func TestAdminInitFailsClosedWithoutToken(t *testing.T) {
	_, err := InitAdmin(config.ServerConfig{Enabled: true, Addr: "127.0.0.1:0", Token: "  "}, nil)
	if err == nil || !strings.Contains(err.Error(), "server.token") {
		t.Fatalf("InitAdmin() error = %v, want server.token error", err)
	}
}

func TestAdminServerHasFiniteTimeouts(t *testing.T) {
	admin := newTestAdmin(t, nil)
	if admin.srv.ReadHeaderTimeout <= 0 || admin.srv.ReadTimeout <= 0 ||
		admin.srv.WriteTimeout <= 0 || admin.srv.IdleTimeout <= 0 {
		t.Fatalf("server timeouts must be non-zero: %+v", admin.srv)
	}
}

func TestAdminStartReportsBindErrorSynchronously(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	defer func() { _ = ln.Close() }()
	admin, err := InitAdmin(config.ServerConfig{Enabled: true, Addr: ln.Addr().String(), Token: "correct-token"}, nil)
	if err != nil {
		t.Fatalf("InitAdmin() error = %v", err)
	}

	if err := admin.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want bind error")
	}
}

func TestAdminLifecycle(t *testing.T) {
	admin, err := InitAdmin(config.ServerConfig{Enabled: true, Addr: "127.0.0.1:0", Token: "correct-token"}, nil)
	if err != nil {
		t.Fatalf("InitAdmin() error = %v", err)
	}
	if admin.Name() != "admin" {
		t.Fatalf("Name() = %q, want admin", admin.Name())
	}
	if err := admin.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if admin.listener == nil {
		t.Fatal("Start() did not retain listener")
	}
	if err := admin.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func newTestAdmin(t *testing.T, db *store.DB) *AdminSubsystem {
	t.Helper()
	admin, err := InitAdmin(config.ServerConfig{Addr: "127.0.0.1:0", Token: "correct-token"}, db)
	if err != nil {
		t.Fatalf("InitAdmin() error = %v", err)
	}
	return admin
}

func openAdminTestDB(t *testing.T) *store.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		channel TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create sessions table: %v", err)
	}
	db := &store.DB{DB: sqlDB}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
