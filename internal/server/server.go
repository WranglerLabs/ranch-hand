package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/adapter"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

const (
	maxPlanSize           = 1 << 20
	maxReleaseRequestSize = 64 << 10
	maxTargetRequestSize  = 2 << 20
)

type releaseVerifier interface {
	VerifyAndCache(context.Context, productrelease.Request) (productrelease.VerifiedArtifact, error)
}

type targetPreflighter interface {
	Preflight(context.Context, plan.DeploymentPlan, adapter.Credentials) adapter.Report
}

type Server struct {
	token           string
	version         string
	ui              fs.FS
	releaseVerifier releaseVerifier
	verifiedMu      sync.RWMutex
	verified        map[string]productrelease.VerifiedArtifact
	targets         targetPreflighter
}

func New(token, version string, ui fs.FS) http.Handler {
	verifier, err := productrelease.NewService("")
	if err != nil {
		verifier = nil
	}
	return NewWithReleaseVerifier(token, version, ui, verifier)
}

func NewWithReleaseVerifier(token, version string, ui fs.FS, verifier releaseVerifier) http.Handler {
	return NewWithDependencies(token, version, ui, verifier, adapter.NewRegistry())
}

func NewWithDependencies(token, version string, ui fs.FS, verifier releaseVerifier, targets targetPreflighter) http.Handler {
	s := &Server{token: token, version: version, ui: ui, releaseVerifier: verifier, verified: make(map[string]productrelease.VerifiedArtifact), targets: targets}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.Handle("GET /api/v1/status", s.authorize(http.HandlerFunc(s.status)))
	mux.Handle("POST /api/v1/plans/validate", s.authorize(http.HandlerFunc(s.validatePlan)))
	mux.Handle("POST /api/v1/plans/create", s.authorize(http.HandlerFunc(s.createPlan)))
	mux.Handle("POST /api/v1/plans/export", s.authorize(http.HandlerFunc(s.exportPlan)))
	mux.Handle("POST /api/v1/plans/preflight", s.authorize(http.HandlerFunc(s.preflightPlan)))
	mux.Handle("POST /api/v1/plans/dry-run", s.authorize(http.HandlerFunc(s.dryRunPlan)))
	mux.Handle("POST /api/v1/targets/preflight", s.authorize(http.HandlerFunc(s.preflightTarget)))
	mux.Handle("POST /api/v1/releases/verify", s.authorize(http.HandlerFunc(s.verifyRelease)))
	mux.Handle("/", s.spa())
	return s.securityHeaders(mux)
}

func (s *Server) preflightTarget(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if s.targets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "target adapters are unavailable"})
		return
	}
	var request struct {
		Plan        plan.DeploymentPlan `json:"plan"`
		Credentials adapter.Credentials `json:"credentials"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTargetRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid target preflight request"})
		return
	}
	defer request.Credentials.Clear()
	if err := request.Credentials.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := request.Plan.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	verified, found := s.verifiedPlan(request.Plan)
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan does not match a release verified during the current Ranch Hand session"})
		return
	}
	artifactReport := plan.Preflight(request.Plan, verified.CachePath)
	if !artifactReport.Ready {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "verified artifact preflight failed", "artifact": artifactReport})
		return
	}
	report := s.targets.Preflight(r.Context(), request.Plan, request.Credentials)
	writeJSON(w, http.StatusOK, report)
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

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	var request plan.CreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPlanSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid plan creation request"})
		return
	}
	verified, ok := s.lookupVerified(request.Version, request.Target)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "verify this exact release and target during the current Ranch Hand session before creating its plan"})
		return
	}
	candidate, err := plan.Create(request, plan.VerifiedRelease{
		ManifestURL: verified.ManifestURL, ManifestSHA256: verified.ManifestSHA256,
		ArtifactSHA256: verified.SHA256, ArtifactSize: verified.Size,
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	canonical, err := plan.CanonicalJSON(candidate)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"created": true, "plan": candidate, "canonicalJson": string(canonical)})
}

func (s *Server) exportPlan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	candidate, ok := readPlan(w, r)
	if !ok {
		return
	}
	if _, found := s.verifiedPlan(candidate); !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan does not match a release verified during the current Ranch Hand session"})
		return
	}
	contents, err := plan.CanonicalJSON(candidate)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="ranch-hand-deployment-plan.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

func (s *Server) preflightPlan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	candidate, ok := readPlan(w, r)
	if !ok {
		return
	}
	verified, found := s.verifiedPlan(candidate)
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan does not match a release verified during the current Ranch Hand session"})
		return
	}
	writeJSON(w, http.StatusOK, plan.Preflight(candidate, verified.CachePath))
}

func (s *Server) dryRunPlan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	candidate, ok := readPlan(w, r)
	if !ok {
		return
	}
	if _, found := s.verifiedPlan(candidate); !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan does not match a release verified during the current Ranch Hand session"})
		return
	}
	report, err := plan.DryRun(candidate)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) verifyRelease(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if s.releaseVerifier == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "release verification is unavailable because the local cache could not be initialized"})
		return
	}
	var request productrelease.Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReleaseRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid release verification request"})
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "release verification request contains trailing data"})
		return
	}
	verified, err := s.releaseVerifier.VerifyAndCache(r.Context(), request)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if verified.ProvenanceVerified && verified.SBOMVerified {
		s.verifiedMu.Lock()
		s.verified[verifiedKey(verified.Version, verified.Target)] = verified
		s.verifiedMu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "artifact": verified})
}

func readPlan(w http.ResponseWriter, r *http.Request) (plan.DeploymentPlan, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPlanSize))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "deployment plan exceeds 1 MiB"})
		return plan.DeploymentPlan{}, false
	}
	candidate, err := plan.DecodeAndValidate(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return plan.DeploymentPlan{}, false
	}
	return candidate, true
}

func verifiedKey(version, target string) string { return version + "\x00" + target }

func (s *Server) lookupVerified(version, target string) (productrelease.VerifiedArtifact, bool) {
	s.verifiedMu.RLock()
	defer s.verifiedMu.RUnlock()
	verified, ok := s.verified[verifiedKey(version, target)]
	return verified, ok
}

func (s *Server) verifiedPlan(candidate plan.DeploymentPlan) (productrelease.VerifiedArtifact, bool) {
	verified, ok := s.lookupVerified(candidate.Release.Version, candidate.Target.Kind)
	if !ok || verified.ManifestURL != candidate.Release.ManifestURL || !strings.EqualFold(verified.ManifestSHA256, candidate.Release.ManifestSHA256) ||
		!strings.EqualFold(verified.SHA256, candidate.Release.ArtifactSHA256) || verified.Size != candidate.Release.ArtifactSize {
		return productrelease.VerifiedArtifact{}, false
	}
	return verified, true
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
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second}
}
