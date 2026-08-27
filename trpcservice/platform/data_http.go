package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type migrationResult struct {
	ID               string `json:"id"`
	TenantID         string `json:"-"`
	Status           string `json:"status"`
	DryRun           bool   `json:"dry_run"`
	Sessions         int    `json:"sessions"`
	SourceCount      int    `json:"source_count"`
	DestinationCount int    `json:"destination_count"`
	Checksum         string `json:"checksum,omitempty"`
	Resumed          bool   `json:"resumed"`
	Message          string `json:"message,omitempty"`
}

func (h *AdminHandler) handleDataResource(w http.ResponseWriter, r *http.Request, parts []string) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	store := h.storeForTenant(tenant.TenantID)
	if len(parts) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	switch parts[0] {
	case "storage":
		h.handleStorage(w, r, tenant, parts[1:])
	case "sessions":
		h.handleSessionData(w, r, store, tenant, parts[1:])
	case "memory":
		h.handleMemoryData(w, r, store, tenant, parts[1:])
	case "migrations":
		h.handleMigration(w, r, store, tenant, parts[1:])
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource was not found")
	}
}

func (h *AdminHandler) handleStorage(w http.ResponseWriter, r *http.Request, tenant TenantContext, parts []string) {
	if len(parts) != 1 || parts[0] != "backend" {
		writeError(w, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	if r.Method == http.MethodGet {
		store := h.storeForTenant(tenant.TenantID)
		h.mu.Lock()
		available := make([]string, 0, len(h.backendCatalog))
		for id := range h.backendCatalog {
			available = append(available, id)
		}
		h.mu.Unlock()
		sort.Strings(available)
		writeJSON(w, http.StatusOK, map[string]any{"backend": store.Health(r.Context()).Backend, "health": store.Health(r.Context()), "available_backends": available})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
		return
	}
	if !canMutate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "tenant administrator role is required")
		return
	}
	var req struct {
		Backend string `json:"backend"`
	}
	if err := decodeStrict(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_backend", "backend is required")
		return
	}
	h.mu.Lock()
	selection, configured := h.backendCatalog[req.Backend]
	h.mu.Unlock()
	if !configured {
		writeError(w, http.StatusBadRequest, "backend_not_configured", "backend is not configured by the server")
		return
	}
	var store DataStore
	switch selection.Backend {
	case "inmemory":
		store = NewInMemoryStore()
	case "redis":
		store = NewRedisStore(selection.Address)
	case "sqlite":
		sqlite, err := NewSQLiteStore(selection.Address)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_backend", err.Error())
			return
		}
		store = sqlite
	default:
		writeError(w, http.StatusBadRequest, "invalid_backend", "backend must be inmemory, redis, or sqlite")
		return
	}
	if err := h.selectBackend(tenant.TenantID, selection, store); err != nil {
		if closer, ok := store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		writeError(w, http.StatusInternalServerError, "backend_selection_not_persisted", "backend selection could not be persisted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backend": req.Backend, "health": store.Health(r.Context())})
}

func (h *AdminHandler) handleSessionData(w http.ResponseWriter, r *http.Request, store DataStore, tenant TenantContext, parts []string) {
	if len(parts) < 1 {
		writeError(w, http.StatusBadRequest, "session_required", "session id is required")
		return
	}
	sessionID := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		state, err := store.GetSessionState(r.Context(), tenant.TenantID, sessionID)
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "session_not_found", "session was not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		events, err := store.ListSessionEvents(r.Context(), tenant.TenantID, sessionID, 0)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": events})
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "resource was not found")
}

func (h *AdminHandler) handleMemoryData(w http.ResponseWriter, r *http.Request, store DataStore, tenant TenantContext, parts []string) {
	if len(parts) != 1 {
		writeError(w, http.StatusBadRequest, "session_required", "session id is required")
		return
	}
	sessionID := parts[0]
	switch r.Method {
	case http.MethodGet:
		items, err := store.ListMemory(r.Context(), tenant.TenantID, sessionID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		if !canOperate(tenant.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
			return
		}
		var req struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := decodeStrict(r, &req); err != nil || req.Key == "" {
			writeError(w, http.StatusBadRequest, "invalid_memory", "key and value are required")
			return
		}
		item := MemoryRecord{TenantID: tenant.TenantID, SessionID: sessionID, Key: req.Key, Value: req.Value}
		if err := store.PutMemory(r.Context(), item); err != nil {
			writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
			return
		}
		writeJSON(w, http.StatusCreated, item)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
	}
}

func (h *AdminHandler) handleMigration(w http.ResponseWriter, r *http.Request, _ DataStore, tenant TenantContext, parts []string) {
	if len(parts) == 1 && r.Method == http.MethodGet {
		h.mu.Lock()
		result, ok := h.migrations[parts[0]]
		h.mu.Unlock()
		if !ok || result.TenantID != tenant.TenantID {
			writeError(w, http.StatusNotFound, "migration_not_found", "migration was not found")
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if len(parts) != 0 {
		writeError(w, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	if !canMutate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "tenant administrator role is required")
		return
	}
	var req struct {
		DryRun    bool `json:"dry_run"`
		BatchSize int  `json:"batch_size"`
	}
	if err := decodeStrict(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_migration", "dry_run and batch_size must be valid")
		return
	}
	h.mu.Lock()
	sourceAddress, destinationPath, checkpointPath := h.migrationSourceAddress, h.migrationDestinationPath, h.migrationCheckpointPath
	h.mu.Unlock()
	if sourceAddress == "" || destinationPath == "" {
		writeError(w, http.StatusServiceUnavailable, "migration_not_configured", "migration endpoints are configured by the server")
		return
	}
	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	id := "migration-" + hex.EncodeToString(idBytes)
	result := migrationResult{ID: id, TenantID: tenant.TenantID, Status: "running", DryRun: req.DryRun}
	h.mu.Lock()
	if h.migrationRunning {
		h.mu.Unlock()
		writeError(w, http.StatusConflict, "migration_in_progress", "another migration is already running")
		return
	}
	h.migrationRunning = true
	h.migrations[id] = result
	h.mu.Unlock()
	h.migrationWG.Add(1)
	go func() {
		defer h.migrationWG.Done()
		defer func() { h.mu.Lock(); h.migrationRunning = false; h.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(h.migrationCtx, 10*time.Minute)
		defer cancel()
		source := NewRedisStore(sourceAddress)
		defer source.Close()
		destination, err := NewSQLiteStore(destinationPath)
		if err == nil {
			defer destination.Close()
		}
		var report MigrationReport
		if err == nil {
			report, err = MigrateRedisToSQL(ctx, source, destination, MigrationOptions{TenantID: tenant.TenantID, DryRun: req.DryRun, BatchSize: req.BatchSize, CheckpointPath: checkpointPath})
		}
		updated := migrationResult{ID: id, TenantID: tenant.TenantID, Status: report.Status, DryRun: req.DryRun, Sessions: report.Sessions, SourceCount: report.SourceCount, DestinationCount: report.DestinationCount, Checksum: report.Checksum, Resumed: report.Resumed}
		if err != nil {
			updated.Status = "failed"
			updated.Message = err.Error()
		}
		h.mu.Lock()
		h.migrations[id] = updated
		h.mu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, result)
}

func isDataPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/admin/storage") || strings.HasPrefix(path, "/api/v1/admin/sessions") || strings.HasPrefix(path, "/api/v1/admin/memory") || strings.HasPrefix(path, "/api/v1/admin/migrations")
}
