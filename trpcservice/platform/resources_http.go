package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (h *AdminHandler) handleAdminResource(w http.ResponseWriter, r *http.Request) bool {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/")
	if path == r.URL.Path {
		return false
	}
	trusted := h.trustedRequest(w, r)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "agent-apps":
		h.handleAgentApps(w, trusted, parts[1:])
		return true
	case "deployments":
		h.handleDeployments(w, trusted, parts[1:])
		return true
	case "run":
		h.handleRoutedRun(w, trusted)
		return true
	case "runtime":
		if len(parts) == 2 && parts[1] == "status" {
			h.handleRuntimeStatus(w, trusted)
			return true
		}
	}
	return false
}

func trustedTenant(r *http.Request) (TenantContext, bool) {
	return TenantContextFromContext(r.Context())
}

func canMutate(role Role) bool  { return role == RolePlatformAdmin || role == RoleTenantAdmin }
func canOperate(role Role) bool { return canMutate(role) || role == RoleOperator }

func (h *AdminHandler) handleAgentApps(w http.ResponseWriter, r *http.Request, parts []string) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"items": h.platform.listApps(tenant.TenantID)})
		case http.MethodPost:
			if !canMutate(tenant.Role) {
				writeError(w, http.StatusForbidden, "forbidden", "tenant administrator role is required")
				return
			}
			var request struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			// tenant_id is deliberately ignored: ownership always comes from Tenant Context.
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !validResourceID(request.ID) || !validDisplayName(request.Name) {
				writeError(w, http.StatusBadRequest, "invalid_agent_app", "id and name must be valid")
				return
			}
			app := AgentApp{ID: request.ID, TenantID: tenant.TenantID, Name: strings.TrimSpace(request.Name), CreatedAt: time.Now().UTC()}
			if !h.platform.createApp(app) {
				writeError(w, http.StatusConflict, "agent_app_exists", "Agent App identifier already exists in this tenant")
				return
			}
			writeJSON(w, http.StatusCreated, app)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
		}
		return
	}
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
		return
	}
	app, exists := h.platform.app(tenant.TenantID, parts[0])
	if !exists {
		writeError(w, http.StatusNotFound, "agent_app_not_found", "Agent App was not found")
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func resourceKey(tenantID, id string) string { return tenantID + "\x00" + id }

func (p *MemoryPlatform) createApp(app AgentApp) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := resourceKey(app.TenantID, app.ID)
	if _, exists := p.apps[key]; exists {
		return false
	}
	p.apps[key] = app
	return true
}

func (p *MemoryPlatform) app(tenantID, id string) (AgentApp, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	app, ok := p.apps[resourceKey(tenantID, id)]
	return app, ok
}

func (p *MemoryPlatform) listApps(tenantID string) []AgentApp {
	p.mu.RLock()
	defer p.mu.RUnlock()
	items := make([]AgentApp, 0)
	for _, app := range p.apps {
		if app.TenantID == tenantID {
			items = append(items, app)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (h *AdminHandler) handleDeployments(w http.ResponseWriter, r *http.Request, parts []string) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	if len(parts) == 0 {
		h.handleDeploymentCollection(w, r, tenant)
		return
	}
	deployment, exists := h.platform.deployment(tenant.TenantID, parts[0])
	if !exists {
		writeError(w, http.StatusNotFound, "deployment_not_found", "Deployment was not found")
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
			return
		}
		writeJSON(w, http.StatusOK, deployment)
		return
	}
	switch parts[1] {
	case "versions":
		h.handleVersions(w, r, tenant, deployment, parts[2:])
	case "transition":
		h.handleTransition(w, r, tenant, deployment)
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource was not found")
	}
}

func (h *AdminHandler) handleDeploymentCollection(w http.ResponseWriter, r *http.Request, tenant TenantContext) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"items": h.platform.listDeployments(tenant.TenantID)})
	case http.MethodPost:
		if !canMutate(tenant.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "tenant administrator role is required")
			return
		}
		var request struct {
			ID         string `json:"id"`
			AgentAppID string `json:"agent_app_id"`
		}
		if err := decodeStrict(r, &request); err != nil || !validResourceID(request.ID) || !validResourceID(request.AgentAppID) {
			writeError(w, http.StatusBadRequest, "invalid_deployment", "id and agent_app_id must be valid")
			return
		}
		if _, exists := h.platform.app(tenant.TenantID, request.AgentAppID); !exists {
			writeError(w, http.StatusNotFound, "agent_app_not_found", "Agent App was not found")
			return
		}
		deployment := Deployment{ID: request.ID, TenantID: tenant.TenantID, AgentAppID: request.AgentAppID, Status: DeploymentDraft, DesiredReplicas: 1, CreatedAt: time.Now().UTC()}
		if !h.platform.createDeployment(deployment) {
			writeError(w, http.StatusConflict, "deployment_exists", "Deployment identifier already exists")
			return
		}
		writeJSON(w, http.StatusCreated, deployment)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
	}
}

func decodeStrict(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (h *AdminHandler) handleVersions(w http.ResponseWriter, r *http.Request, tenant TenantContext, deployment Deployment, tail []string) {
	if len(tail) > 0 {
		writeError(w, http.StatusMethodNotAllowed, "immutable_version", "Deployment Versions cannot be modified in place")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"items": h.platform.listVersions(tenant.TenantID, deployment.ID)})
	case http.MethodPost:
		if !canMutate(tenant.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "tenant administrator role is required")
			return
		}
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
			return
		}
		if !validIdempotencyKey(idempotencyKey) {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must contain 1 to 128 printable ASCII characters")
			return
		}
		var request struct {
			Config map[string]any `json:"config"`
		}
		if err := decodeStrict(r, &request); err != nil || len(request.Config) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_deployment_config", "config must be a non-empty JSON object")
			return
		}
		version, code, ok := h.platform.createVersion(deployment, idempotencyKey, request.Config)
		if !ok {
			writeError(w, http.StatusConflict, code, "Idempotency-Key was already used with different configuration")
			return
		}
		writeJSON(w, http.StatusCreated, version)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
	}
}

func validIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

func (h *AdminHandler) handleTransition(w http.ResponseWriter, r *http.Request, tenant TenantContext, deployment Deployment) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	if !canOperate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
		return
	}
	var request struct {
		Status    DeploymentStatus `json:"status"`
		VersionID string           `json:"version_id"`
	}
	if err := decodeStrict(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_transition", "status is required")
		return
	}
	updated, code, ok := h.platform.transition(deployment, request.Status, request.VersionID)
	if !ok {
		message := "Deployment lifecycle transition is not allowed"
		if code == "agent_app_already_has_active_deployment" {
			message = "Agent App already has an active Deployment"
		}
		writeError(w, http.StatusConflict, code, message)
		return
	}
	if request.Status == DeploymentPaused && updated.VersionID != "" {
		if err := h.runtime.RetireVersion(updated.VersionID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "runtime_close_failed", "deployment runtime could not be retired")
			return
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

func (p *MemoryPlatform) createDeployment(deployment Deployment) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := resourceKey(deployment.TenantID, deployment.ID)
	if _, exists := p.deployments[key]; exists {
		return false
	}
	p.deployments[key] = deployment
	return true
}

func (p *MemoryPlatform) deployment(tenantID, id string) (Deployment, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	deployment, ok := p.deployments[resourceKey(tenantID, id)]
	return deployment, ok
}

func (p *MemoryPlatform) listDeployments(tenantID string) []Deployment {
	p.mu.RLock()
	defer p.mu.RUnlock()
	items := []Deployment{}
	for _, item := range p.deployments {
		if item.TenantID == tenantID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (p *MemoryPlatform) createVersion(deployment Deployment, idempotencyKey string, config map[string]any) (DeploymentVersion, string, bool) {
	canonical, _ := json.Marshal(config)
	p.mu.Lock()
	defer p.mu.Unlock()
	key := resourceKey(deployment.TenantID, deployment.ID)
	creationKey := versionCreationKey{tenantID: deployment.TenantID, deploymentID: deployment.ID, idempotencyKey: idempotencyKey}
	if creation, exists := p.versionCreations[creationKey]; exists {
		if creation.config != string(canonical) {
			return DeploymentVersion{}, "idempotency_key_reused", false
		}
		version := creation.version
		version.Config = cloneConfig(version.Config)
		return version, "", true
	}
	number := len(p.versions[key]) + 1
	version := DeploymentVersion{ID: fmt.Sprintf("%s-v%d", deployment.ID, number), TenantID: deployment.TenantID, AgentAppID: deployment.AgentAppID, DeploymentID: deployment.ID, Number: number, Config: cloneConfig(config), CreatedAt: time.Now().UTC()}
	p.versions[key] = append(p.versions[key], version)
	p.versionCreations[creationKey] = versionCreation{config: string(canonical), version: version}
	version.Config = cloneConfig(version.Config)
	return version, "", true
}

func cloneConfig(config map[string]any) map[string]any {
	data, _ := json.Marshal(config)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func (p *MemoryPlatform) listVersions(tenantID, deploymentID string) []DeploymentVersion {
	p.mu.RLock()
	defer p.mu.RUnlock()
	source := p.versions[resourceKey(tenantID, deploymentID)]
	items := make([]DeploymentVersion, len(source))
	for i, version := range source {
		items[i] = version
		items[i].Config = cloneConfig(version.Config)
	}
	return items
}

func (p *MemoryPlatform) transition(deployment Deployment, next DeploymentStatus, versionID string) (Deployment, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := resourceKey(deployment.TenantID, deployment.ID)
	current := p.deployments[key]
	valid := (current.Status == DeploymentDraft && next == DeploymentPublished) || (current.Status == DeploymentPublished && next == DeploymentActive) || (current.Status == DeploymentActive && next == DeploymentPaused)
	if !valid {
		return Deployment{}, "invalid_deployment_transition", false
	}
	if next == DeploymentActive {
		for _, item := range p.deployments {
			if item.TenantID == current.TenantID && item.AgentAppID == current.AgentAppID && item.ID != current.ID && item.Status == DeploymentActive {
				return Deployment{}, "agent_app_already_has_active_deployment", false
			}
		}
	}
	if next == DeploymentPublished {
		found := false
		for _, version := range p.versions[key] {
			if version.ID == versionID {
				found = true
				break
			}
		}
		if !found {
			return Deployment{}, "deployment_version_not_found", false
		}
		current.VersionID = versionID
	}
	current.Status = next
	p.deployments[key] = current
	return current, "", true
}
