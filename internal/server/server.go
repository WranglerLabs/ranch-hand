package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/adapter"
	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/diagnostics"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/operations"
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

type releaseDiscoverer interface {
	Discover(context.Context, string, string) (productrelease.DiscoveredRelease, error)
}

type targetPreflighter interface {
	Preflight(context.Context, plan.DeploymentPlan, adapter.Credentials) adapter.Report
}

type targetRemnantCleaner interface {
	CleanupRemnant(context.Context, plan.DeploymentPlan, adapter.Credentials) error
}

type targetPrerequisiteInstaller interface {
	InstallPrerequisites(context.Context, plan.DeploymentPlan, adapter.Credentials) error
}

type bundleStager interface {
	Stage(productrelease.VerifiedArtifact) (bundle.StagedBundle, error)
}

type operationRunner interface {
	Run(context.Context, operations.Request) (operations.Result, error)
	RecoverActive(context.Context, string, adapter.Credentials) (operations.Result, error)
}

type installationReader interface {
	Installation(string) (lifecycle.InstallationRecord, error)
	Installations() ([]lifecycle.InstallationRecord, error)
	Backups(string) ([]lifecycle.BackupRecord, error)
	Active(string) (lifecycle.Journal, error)
	ActiveOperations() ([]lifecycle.Journal, error)
}

type rollbackPoolManager interface {
	RollbackPool(context.Context, plan.DeploymentPlan, []lifecycle.BackupRecord) ([]adapter.RollbackPoolEntry, error)
	PruneRollbackPool(context.Context, plan.DeploymentPlan, []lifecycle.BackupRecord, int) (adapter.RollbackPruneResult, error)
}

type Server struct {
	token             string
	version           string
	ui                fs.FS
	releaseVerifier   releaseVerifier
	releaseDiscoverer releaseDiscoverer
	verifiedMu        sync.RWMutex
	verified          map[string]productrelease.VerifiedArtifact
	targets           targetPreflighter
	remnantCleaner    targetRemnantCleaner
	prerequisites     targetPrerequisiteInstaller
	stager            bundleStager
	coordinator       operationRunner
	installations     installationReader
	rollbackPool      rollbackPoolManager
	readyMu           sync.RWMutex
	readyPlans        map[string]bool
}

func New(token, version string, ui fs.FS) http.Handler {
	verifier, err := productrelease.NewService("")
	if err != nil {
		verifier = nil
	}
	targets := adapter.NewRegistry()
	localDocker := adapter.NewLocalDocker()
	stager, stageErr := bundle.NewStager("")
	store, storeErr := lifecycle.NewStore("")
	var coordinator operationRunner
	var installations installationReader
	if storeErr == nil {
		installations = store
	}
	if stageErr == nil && storeErr == nil {
		coordinator, _ = operations.NewCoordinator(store, stager, operations.NewRegistry(map[string]operations.Mutator{
			"local-compose": localDocker, "local-wsl-compose": adapter.NewWSLCompose(), "azure-container-apps": adapter.NewAzureContainerApps(),
			"cloudflare": adapter.NewCloudflare(), "remote-linux-compose": adapter.NewRemoteLinuxCompose(),
		}))
	}
	return newWithServices(token, version, ui, verifier, targets, stager, coordinator, installations, localDocker)
}

func NewWithReleaseVerifier(token, version string, ui fs.FS, verifier releaseVerifier) http.Handler {
	return NewWithDependencies(token, version, ui, verifier, adapter.NewRegistry())
}

func NewWithDependencies(token, version string, ui fs.FS, verifier releaseVerifier, targets targetPreflighter) http.Handler {
	stager, _ := bundle.NewStager("")
	return newWithServices(token, version, ui, verifier, targets, stager, nil, nil, nil)
}

func newWithServices(token, version string, ui fs.FS, verifier releaseVerifier, targets targetPreflighter, stager bundleStager, coordinator operationRunner, installations installationReader, rollbackPool rollbackPoolManager) http.Handler {
	discoverer, _ := verifier.(releaseDiscoverer)
	cleaner, _ := targets.(targetRemnantCleaner)
	prerequisites, _ := targets.(targetPrerequisiteInstaller)
	s := &Server{token: token, version: version, ui: ui, releaseVerifier: verifier, releaseDiscoverer: discoverer, verified: make(map[string]productrelease.VerifiedArtifact), targets: targets, remnantCleaner: cleaner, prerequisites: prerequisites, stager: stager, coordinator: coordinator, installations: installations, rollbackPool: rollbackPool, readyPlans: make(map[string]bool)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.Handle("GET /api/v1/status", s.authorize(http.HandlerFunc(s.status)))
	mux.Handle("POST /api/v1/plans/validate", s.authorize(http.HandlerFunc(s.validatePlan)))
	mux.Handle("POST /api/v1/plans/create", s.authorize(http.HandlerFunc(s.createPlan)))
	mux.Handle("POST /api/v1/plans/export", s.authorize(http.HandlerFunc(s.exportPlan)))
	mux.Handle("POST /api/v1/plans/preflight", s.authorize(http.HandlerFunc(s.preflightPlan)))
	mux.Handle("POST /api/v1/plans/dry-run", s.authorize(http.HandlerFunc(s.dryRunPlan)))
	mux.Handle("POST /api/v1/targets/preflight", s.authorize(http.HandlerFunc(s.preflightTarget)))
	mux.Handle("POST /api/v1/targets/remnants/cleanup", s.authorize(http.HandlerFunc(s.cleanupTargetRemnant)))
	mux.Handle("POST /api/v1/targets/prerequisites/install", s.authorize(http.HandlerFunc(s.installTargetPrerequisites)))
	mux.Handle("GET /api/v1/targets/wsl-distributions", s.authorize(http.HandlerFunc(s.wslDistributions)))
	mux.Handle("POST /api/v1/targets/remote-linux/host-key", s.authorize(http.HandlerFunc(s.inspectRemoteLinuxHostKey)))
	mux.Handle("POST /api/v1/bundles/stage", s.authorize(http.HandlerFunc(s.stageBundle)))
	mux.Handle("POST /api/v1/operations/run", s.authorize(http.HandlerFunc(s.runOperation)))
	mux.Handle("GET /api/v1/operations/active", s.authorize(http.HandlerFunc(s.listActiveOperations)))
	mux.Handle("POST /api/v1/operations/{deploymentID}/recover", s.authorize(http.HandlerFunc(s.recoverActiveOperation)))
	mux.Handle("GET /api/v1/installations", s.authorize(http.HandlerFunc(s.listInstallations)))
	mux.Handle("GET /api/v1/installations/{deploymentID}/backups", s.authorize(http.HandlerFunc(s.listBackups)))
	mux.Handle("GET /api/v1/installations/{deploymentID}/rollback-pool", s.authorize(http.HandlerFunc(s.listRollbackPool)))
	mux.Handle("POST /api/v1/installations/{deploymentID}/rollback-pool/prune", s.authorize(http.HandlerFunc(s.pruneRollbackPool)))
	mux.Handle("GET /api/v1/diagnostics", s.authorize(http.HandlerFunc(s.exportDiagnostics)))
	mux.Handle("POST /api/v1/releases/verify", s.authorize(http.HandlerFunc(s.verifyRelease)))
	mux.Handle("GET /api/v1/releases/recommended", s.authorize(http.HandlerFunc(s.recommendedRelease)))
	mux.Handle("/", s.spa())
	return s.securityHeaders(mux)
}

func (s *Server) installTargetPrerequisites(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if s.prerequisites == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guided prerequisite installation is unavailable"})
		return
	}
	var request struct {
		Plan        plan.DeploymentPlan `json:"plan"`
		Credentials adapter.Credentials `json:"credentials"`
		Confirmed   bool                `json:"confirmed"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTargetRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !request.Confirmed {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Docker prerequisite installation requires an explicit confirmed request"})
		return
	}
	defer request.Credentials.Clear()
	if err := request.Credentials.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := request.Plan.Validate(); err != nil || (request.Plan.Target.Kind != "local-compose" && request.Plan.Target.Kind != "local-wsl-compose" && request.Plan.Target.Kind != "remote-linux-compose") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a valid Docker Desktop, WSL, or Remote Linux Compose plan is required"})
		return
	}
	verified, found := s.verifiedPlan(request.Plan)
	if !found || !plan.Preflight(request.Plan, verified.CachePath).Ready {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan does not match a currently verified release artifact"})
		return
	}
	if err := s.prerequisites.InstallPrerequisites(r.Context(), request.Plan, request.Credentials); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	report := s.targets.Preflight(r.Context(), request.Plan, request.Credentials)
	report = s.annotateLifecycleTarget(request.Plan, report)
	if report.Ready {
		if key, err := planSessionKey(request.Plan); err == nil {
			s.readyMu.Lock()
			s.readyPlans[key] = true
			s.readyMu.Unlock()
		}
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) inspectRemoteLinuxHostKey(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	var request struct {
		Host string `json:"host"`
		Port string `json:"port"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReleaseRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid SSH host-key inspection request"})
		return
	}
	identity, err := adapter.InspectSSHHostKey(r.Context(), strings.TrimSpace(request.Host), strings.TrimSpace(request.Port))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identity": identity})
}

func (s *Server) wslDistributions(w http.ResponseWriter, r *http.Request) {
	distributions, err := adapter.WSLDistributions(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"distributions": distributions})
}

func (s *Server) recommendedRelease(w http.ResponseWriter, r *http.Request) {
	if s.releaseDiscoverer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "release discovery is unavailable"})
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "stable"
	}
	discovered, err := s.releaseDiscoverer.Discover(r.Context(), channel, r.URL.Query().Get("target"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"release": discovered})
}

func (s *Server) listActiveOperations(w http.ResponseWriter, _ *http.Request) {
	if s.installations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "active operation inventory is unavailable"})
		return
	}
	active, err := s.installations.ActiveOperations()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read active operations: " + err.Error()})
		return
	}
	type summary struct {
		DeploymentID string                  `json:"deploymentId"`
		OperationID  string                  `json:"operationId"`
		Kind         lifecycle.OperationKind `json:"kind"`
		Target       string                  `json:"target"`
		FromVersion  string                  `json:"fromVersion,omitempty"`
		ToVersion    string                  `json:"toVersion"`
		Phase        lifecycle.Phase         `json:"phase"`
		UpdatedAt    time.Time               `json:"updatedAt"`
	}
	result := make([]summary, 0, len(active))
	for _, journal := range active {
		result = append(result, summary{
			DeploymentID: journal.DeploymentID, OperationID: journal.OperationID, Kind: journal.Kind,
			Target: journal.Target, FromVersion: journal.FromVersion, ToVersion: journal.ToVersion,
			Phase: journal.Phase, UpdatedAt: journal.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": result})
}

func (s *Server) recoverActiveOperation(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if s.coordinator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "lifecycle recovery is unavailable"})
		return
	}
	var request struct {
		Credentials adapter.Credentials `json:"credentials"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTargetRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lifecycle recovery request"})
		return
	}
	defer request.Credentials.Clear()
	result, err := s.coordinator.RecoverActive(r.Context(), r.PathValue("deploymentID"), request.Credentials)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "operation": result})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"completed": true, "operation": result})
}

func (s *Server) exportDiagnostics(w http.ResponseWriter, _ *http.Request) {
	if s.installations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "redacted diagnostics are unavailable"})
		return
	}
	snapshot, err := (diagnostics.Collector{}).Collect(s.version, s.installations)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "collect redacted diagnostics: " + err.Error()})
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="ranch-hand-diagnostics.json"`)
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	if s.installations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "backup records are unavailable"})
		return
	}
	records, err := s.installations.Backups(r.PathValue("deploymentID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read backup records: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": records})
}

func (s *Server) rollbackPoolInputs(deploymentID string) (plan.DeploymentPlan, []lifecycle.BackupRecord, error) {
	if s.installations == nil || s.rollbackPool == nil {
		return plan.DeploymentPlan{}, nil, errors.New("local rollback retention is unavailable")
	}
	record, err := s.installations.Installation(deploymentID)
	if err != nil {
		return plan.DeploymentPlan{}, nil, err
	}
	if record.State != lifecycle.InstallationActive || record.Target != "local-compose" {
		return plan.DeploymentPlan{}, nil, errors.New("rollback retention requires an active local Docker installation")
	}
	candidate, err := plan.DecodeAndValidate(record.Plan)
	if err != nil {
		return plan.DeploymentPlan{}, nil, errors.New("recorded local deployment plan is invalid")
	}
	backups, err := s.installations.Backups(deploymentID)
	return candidate, backups, err
}

func (s *Server) listRollbackPool(w http.ResponseWriter, r *http.Request) {
	candidate, backups, err := s.rollbackPoolInputs(r.PathValue("deploymentID"))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	entries, err := s.rollbackPool.RollbackPool(r.Context(), candidate, backups)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) pruneRollbackPool(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	var request struct {
		KeepLatest int  `json:"keepLatest"`
		Confirmed  bool `json:"confirmed"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReleaseRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !request.Confirmed {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rollback pruning requires an explicit confirmed retention request"})
		return
	}
	deploymentID := r.PathValue("deploymentID")
	if s.installations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "local rollback retention is unavailable"})
		return
	}
	if _, err := s.installations.Active(deploymentID); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "rollback pruning is blocked while a lifecycle operation is active"})
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "active operation state could not be verified"})
		return
	}
	candidate, backups, err := s.rollbackPoolInputs(deploymentID)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	result, err := s.rollbackPool.PruneRollbackPool(r.Context(), candidate, backups, request.KeepLatest)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "result": result})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"completed": true, "result": result})
}

func (s *Server) listInstallations(w http.ResponseWriter, _ *http.Request) {
	if s.installations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "installation records are unavailable"})
		return
	}
	records, err := s.installations.Installations()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read installation records: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"installations": records})
}

func (s *Server) runOperation(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if s.coordinator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "lifecycle operations are unavailable"})
		return
	}
	var request struct {
		Kind        lifecycle.OperationKind `json:"kind"`
		Plan        plan.DeploymentPlan     `json:"plan"`
		FromVersion string                  `json:"fromVersion,omitempty"`
		BackupID    string                  `json:"backupId,omitempty"`
		Credentials adapter.Credentials     `json:"credentials"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTargetRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lifecycle operation request"})
		return
	}
	defer request.Credentials.Clear()
	localOperation := request.Plan.Target.Kind == "local-compose" && (request.Kind == lifecycle.Install || request.Kind == lifecycle.Backup || request.Kind == lifecycle.Update || request.Kind == lifecycle.Restore || request.Kind == lifecycle.Rollback || request.Kind == lifecycle.Repair)
	wslOperation := request.Plan.Target.Kind == "local-wsl-compose" && request.Kind == lifecycle.Install
	azureOperation := request.Plan.Target.Kind == "azure-container-apps" && request.Kind == lifecycle.Install
	cloudflareOperation := request.Plan.Target.Kind == "cloudflare" && request.Kind == lifecycle.Install
	remoteOperation := request.Plan.Target.Kind == "remote-linux-compose" && request.Kind == lifecycle.Install
	if !localOperation && !wslOperation && !azureOperation && !cloudflareOperation && !remoteOperation {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this target and lifecycle operation is not enabled in the current build"})
		return
	}
	if err := request.Plan.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if request.Kind == lifecycle.Backup && request.FromVersion != request.Plan.Release.Version {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "local backup fromVersion must match the plan's explicit immutable release"})
		return
	}
	if request.Kind == lifecycle.Update && (request.FromVersion == "" || request.FromVersion == request.Plan.Release.Version) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "local update requires the different explicit immutable version currently installed"})
		return
	}
	if request.Kind == lifecycle.Restore && (request.BackupID == "" || request.FromVersion != request.Plan.Release.Version) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "local restore requires a selected backup and the currently installed immutable release"})
		return
	}
	if request.Kind == lifecycle.Rollback && (request.BackupID == "" || request.FromVersion == "" || request.FromVersion == request.Plan.Release.Version) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "local rollback requires a selected backup for a different explicit immutable release"})
		return
	}
	if request.Kind == lifecycle.Repair && (request.FromVersion == "" || request.FromVersion != request.Plan.Release.Version) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "local repair requires the currently installed immutable release"})
		return
	}
	if request.Kind != lifecycle.Restore && request.Kind != lifecycle.Rollback && request.BackupID != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "backupId is valid only for restore or rollback"})
		return
	}
	verified, found := s.verifiedPlan(request.Plan)
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the operation plan does not match a release verified during this Ranch Hand session"})
		return
	}
	key, err := planSessionKey(request.Plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.readyMu.RLock()
	ready := s.readyPlans[key]
	s.readyMu.RUnlock()
	if !ready {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "run a successful live target preflight for this exact plan before installation"})
		return
	}
	result, err := s.coordinator.Run(r.Context(), operations.Request{Kind: request.Kind, Plan: request.Plan, FromVersion: request.FromVersion, BackupID: request.BackupID, Artifact: verified, Credentials: request.Credentials})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "operation": result})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"completed": true, "operation": result})
}

func (s *Server) stageBundle(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if s.stager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "verified bundle staging is unavailable"})
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
	staged, err := s.stager.Stage(verified)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"staged": true, "bundle": staged})
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
	report = s.annotateLifecycleTarget(request.Plan, report)
	if report.Ready {
		if key, err := planSessionKey(request.Plan); err == nil {
			s.readyMu.Lock()
			s.readyPlans[key] = true
			s.readyMu.Unlock()
		}
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) annotateLifecycleTarget(candidate plan.DeploymentPlan, report adapter.Report) adapter.Report {
	if s.installations == nil {
		return report
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return report
	}
	report.DeploymentID = deploymentID
	if journal, err := s.installations.Active(deploymentID); err == nil {
		report.Ready = false
		report.State = "recovery-required"
		return replaceBoundaryCheck(report, "Ranch Hand owns this directory and found an interrupted "+string(journal.Kind)+" operation at phase "+string(journal.Phase)+". Run the ownership-checked recovery below, then preflight again.")
	} else if !errors.Is(err, os.ErrNotExist) {
		report.Ready = false
		report.State = "lifecycle-unavailable"
		return replaceBoundaryCheck(report, "Ranch Hand could not safely read the lifecycle record for this target. No target changes were made.")
	}
	record, err := s.installations.Installation(deploymentID)
	if err == nil && record.State == lifecycle.InstallationActive {
		report.Ready = false
		report.State = "already-installed"
		return replaceBoundaryCheck(report, "Ranch Hand already has an active installation record for this target at "+record.Version+". It will not start a duplicate installation.")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		report.Ready = false
		report.State = "lifecycle-unavailable"
		return replaceBoundaryCheck(report, "Ranch Hand could not safely read the installation record for this target. No target changes were made.")
	}
	if candidate.Target.Kind == "local-wsl-compose" && failedCheck(report, "wsl-directory-boundary") {
		report.Ready = false
		report.State = "orphan-cleanup-available"
		return replaceBoundaryCheck(report, "The dedicated WSL directory exists without an active Ranch Hand lifecycle record. Inspect and remove Ranch Hand remnants below, then preflight again.")
	}
	return report
}

func failedCheck(report adapter.Report, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name && !check.OK {
			return true
		}
	}
	return false
}

func (s *Server) cleanupTargetRemnant(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if s.targets == nil || s.remnantCleaner == nil || s.installations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ownership-safe target remnant cleanup is unavailable"})
		return
	}
	var request struct {
		Plan        plan.DeploymentPlan `json:"plan"`
		Credentials adapter.Credentials `json:"credentials"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTargetRequestSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid target remnant cleanup request"})
		return
	}
	defer request.Credentials.Clear()
	if err := request.Credentials.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := request.Plan.Validate(); err != nil || request.Plan.Target.Kind != "local-wsl-compose" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a valid local WSL Compose plan is required"})
		return
	}
	verified, found := s.verifiedPlan(request.Plan)
	if !found || !plan.Preflight(request.Plan, verified.CachePath).Ready {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan does not match a currently verified release artifact"})
		return
	}
	deploymentID, err := lifecycle.DeploymentID(request.Plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, err = s.installations.Active(deploymentID); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an active lifecycle operation exists; use its normal recovery action"})
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the active lifecycle inventory could not be read safely"})
		return
	}
	if record, recordErr := s.installations.Installation(deploymentID); recordErr == nil && record.State == lifecycle.InstallationActive {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an active installation record exists; Ranch Hand will not remove it as a remnant"})
		return
	} else if recordErr != nil && !errors.Is(recordErr, os.ErrNotExist) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the installation inventory could not be read safely"})
		return
	}
	report := s.targets.Preflight(r.Context(), request.Plan, request.Credentials)
	if !failedCheck(report, "wsl-directory-boundary") {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the target no longer has an orphaned WSL directory collision"})
		return
	}
	if err := s.remnantCleaner.CleanupRemnant(r.Context(), request.Plan, request.Credentials); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if key, keyErr := planSessionKey(request.Plan); keyErr == nil {
		s.readyMu.Lock()
		delete(s.readyPlans, key)
		s.readyMu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleaned": true, "deploymentId": deploymentID})
}

func replaceBoundaryCheck(report adapter.Report, message string) adapter.Report {
	for index := range report.Checks {
		if strings.Contains(report.Checks[index].Name, "boundary") {
			report.Checks[index].OK = false
			report.Checks[index].Message = message
			return report
		}
	}
	report.Checks = append(report.Checks, adapter.Check{Name: "lifecycle-boundary", OK: false, Message: message})
	return report
}

func planSessionKey(candidate plan.DeploymentPlan) (string, error) {
	canonical, err := plan.CanonicalJSON(candidate)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
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
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 70 * time.Minute, IdleTimeout: 60 * time.Second}
}
