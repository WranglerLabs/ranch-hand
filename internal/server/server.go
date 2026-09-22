package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const maxPlanSize = 1 << 20

type Server struct {
	token   string
	version string
	ui      fs.FS
}

func New(token, version string, ui fs.FS) http.Handler {
	s := &Server{token: token, version: version, ui: ui}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.Handle("GET /api/v1/status", s.authorize(http.HandlerFunc(s.status)))
	mux.Handle("POST /api/v1/plans/validate", s.authorize(http.HandlerFunc(s.validatePlan)))
	mux.Handle("/", s.spa())
	return s.securityHeaders(mux)
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"name": "Ranch Hand", "version": s.version, "apiVersion": "v1", "platform": runtime.GOOS + "/" + runtime.GOARCH,
	})
}

func (s *Server) validatePlan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPlanSize))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "deployment plan exceeds 1 MiB"})
		return
	}
	candidate, err := plan.DecodeAndValidate(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "plan": candidate})
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing launch token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) spa() http.Handler {
	files := http.FileServer(http.FS(s.ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(s.ui, path); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || origin == "http://"+r.Host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fmt.Printf("encode response: %v\n", err)
	}
}

func DefaultHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
}
