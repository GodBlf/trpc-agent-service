package platform

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type migrationResult struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	DryRun           bool   `json:"dry_run"`
	SourceCount      int    `json:"source_count"`
	DestinationCount int    `json:"destination_count"`
	Checksum         string `json:"checksum,omitempty"`
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
		writeJSON(w, http.StatusOK, map[string]any{"backend": store.Health(r.Context()).Backend, "health": store.Health(r.Context())})
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
		Address string `json:"address"`
	}
	if err := decodeStrict(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_backend", "backend is required")
		return
	}
	var store DataStore
	switch req.Backend {
	case "inmemory":
		store = NewInMemoryStore()
	case "redis":
		store = NewRedisStore(req.Address)
	case "sqlite":
		sqlite, err := NewSQLiteStore(req.Address)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_backend", err.Error())
			return
		}
		store = sqlite
	default:
		writeError(w, http.StatusBadRequest, "invalid_backend", "backend must be inmemory, redis, or sqlite")
		return
	}
	h.mu.Lock()
	h.backends[tenant.TenantID] = store
	h.mu.Unlock()
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

func (h *AdminHandler) handleMigration(w http.ResponseWriter, r *http.Request, store DataStore, tenant TenantContext, parts []string) {
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
		DryRun bool `json:"dry_run"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	result := migrationResult{ID: "migration-" + time.Now().UTC().Format("20060102150405.000000000"), Status: "completed", DryRun: req.DryRun, Message: "no source records"}
	writeJSON(w, http.StatusAccepted, result)
}

func isDataPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/admin/storage") || strings.HasPrefix(path, "/api/v1/admin/sessions") || strings.HasPrefix(path, "/api/v1/admin/memory") || strings.HasPrefix(path, "/api/v1/admin/migrations")
}
