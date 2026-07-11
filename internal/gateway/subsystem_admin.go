package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/store"
)

type AdminSubsystem struct {
	enabled  bool
	addr     string
	token    string
	db       *store.DB
	srv      *http.Server
	listener net.Listener
}

func InitAdmin(cfg config.ServerConfig, db *store.DB) (*AdminSubsystem, error) {
	if cfg.Enabled && strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("server.token is required when server is enabled")
	}

	admin := &AdminSubsystem{
		enabled: cfg.Enabled,
		addr:    cfg.Addr,
		token:   cfg.Token,
		db:      db,
	}
	admin.srv = &http.Server{
		Addr:              cfg.Addr,
		Handler:           admin.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return admin, nil
}

func (a *AdminSubsystem) Name() string { return "admin" }

func (a *AdminSubsystem) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/api/", a.requireBearer(http.HandlerFunc(a.handleAPI)))
	return mux
}

func (a *AdminSubsystem) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		supplied := ""
		if strings.HasPrefix(auth, prefix) {
			supplied = strings.TrimPrefix(auth, prefix)
		}
		if len(supplied) != len(a.token) || subtle.ConstantTimeCompare([]byte(supplied), []byte(a.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AdminSubsystem) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/sessions" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	rows, err := a.db.QueryContext(r.Context(),
		`SELECT id, channel, channel_id, created_at, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 50`)
	if err != nil {
		slog.Error("admin: list sessions failed", "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	type sessionInfo struct {
		ID        string `json:"id"`
		Channel   string `json:"channel"`
		ChannelID string `json:"channel_id"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	sessions := make([]sessionInfo, 0)
	for rows.Next() {
		var session sessionInfo
		if err := rows.Scan(&session.ID, &session.Channel, &session.ChannelID, &session.CreatedAt, &session.UpdatedAt); err != nil {
			slog.Error("admin: scan session failed", "err", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		slog.Error("admin: iterate sessions failed", "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, sessions)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("admin: encode response failed", "err", err)
	}
}

func (a *AdminSubsystem) Start(context.Context) error {
	if !a.enabled {
		return nil
	}
	listener, err := net.Listen("tcp", a.addr)
	if err != nil {
		return err
	}
	a.listener = listener
	go func() {
		if err := a.srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("admin server failed", "err", err)
		}
	}()
	return nil
}

func (a *AdminSubsystem) Stop(context.Context) error {
	if a.listener == nil {
		return nil
	}
	a.listener = nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.srv.Shutdown(ctx)
}
