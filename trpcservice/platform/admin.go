package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Role string

const (
	RolePlatformAdmin Role = "platform_admin"
	RoleTenantAdmin   Role = "tenant_admin"
	RoleOperator      Role = "operator"
	RoleViewer        Role = "viewer"
)

type TenantAssignment struct {
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Role       Role   `json:"role"`
}

type DevelopmentIdentity struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Assignments []TenantAssignment `json:"assignments"`
}

type identityResponse struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	ActiveTenantID string             `json:"active_tenant_id"`
	ActiveRole     Role               `json:"active_role"`
	Assignments    []TenantAssignment `json:"assignments"`
	AuthMode       string             `json:"auth_mode"`
}

// MemoryPlatform owns Stage 1 resource state. It is intentionally process-local;
// Stage 2 replaces storage through the frozen adapter boundary.
type MemoryPlatform struct {
	mu               sync.RWMutex
	tenants          map[string]Tenant
	apps             map[string]AgentApp
	deployments      map[string]Deployment
	versions         map[string][]DeploymentVersion
	versionCreations map[versionCreationKey]versionCreation
}

type versionCreationKey struct {
	tenantID       string
	deploymentID   string
	idempotencyKey string
}

type versionCreation struct {
	config  string
	version DeploymentVersion
}

func NewMemoryPlatform() *MemoryPlatform {
	return &MemoryPlatform{
		tenants: make(map[string]Tenant), apps: make(map[string]AgentApp),
		deployments: make(map[string]Deployment), versions: make(map[string][]DeploymentVersion),
		versionCreations: make(map[versionCreationKey]versionCreation),
	}
}

func (p *MemoryPlatform) seedTenant(assignment TenantAssignment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.tenants[assignment.TenantID]; !exists {
		p.tenants[assignment.TenantID] = Tenant{ID: assignment.TenantID, Name: assignment.TenantName, CreatedAt: time.Now().UTC()}
	}
}

func (p *MemoryPlatform) createTenant(tenant Tenant) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.tenants[tenant.ID]; exists {
		return false
	}
	p.tenants[tenant.ID] = tenant
	return true
}

func (p *MemoryPlatform) tenant(id string) (Tenant, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	tenant, ok := p.tenants[id]
	return tenant, ok
}

func (p *MemoryPlatform) DeploymentVersion(id string) (DeploymentVersion, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, versions := range p.versions {
		for _, version := range versions {
			if version.ID == id {
				if deployment, ok := p.deployments[resourceKey(version.TenantID, version.DeploymentID)]; ok {
					version.Active = deployment.Status == DeploymentActive && deployment.VersionID == version.ID ||
						deployment.Status == DeploymentActive && deployment.GrayPercentage > 0 && deployment.TargetVersionID == version.ID
				}
				return version, true
			}
		}
	}
	return DeploymentVersion{}, false
}

func (p *MemoryPlatform) listTenants() []Tenant {
	p.mu.RLock()
	defer p.mu.RUnlock()
	items := make([]Tenant, 0, len(p.tenants))
	for _, tenant := range p.tenants {
		items = append(items, tenant)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (p *MemoryPlatform) listTenantsFor(tenant TenantContext) []Tenant {
	p.mu.RLock()
	defer p.mu.RUnlock()
	items := make([]Tenant, 0, len(tenant.Assignments))
	for _, candidate := range p.tenants {
		if tenantCanSee(tenant, candidate.ID) {
			items = append(items, candidate)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

type developmentSession struct {
	activeTenantID string
	lastSeen       time.Time
}

type productionSession struct {
	identity identityResponse
	lastSeen time.Time
}

const maxDevelopmentSessions = 256

type AdminHandler struct {
	platform                 *MemoryPlatform
	identity                 DevelopmentIdentity
	identityProvider         IdentityProvider
	productionSessions       map[string]*productionSession
	governance               *GovernanceCenter
	mu                       sync.Mutex
	sessions                 map[string]*developmentSession
	runtime                  *Runtime
	backends                 *backendRegistry
	migrations               map[string]migrationResult
	backendSelectionPath     string
	backendCatalog           map[string]backendSelection
	migrationSourceAddress   string
	migrationDestinationPath string
	migrationCheckpointPath  string
	migrationCtx             context.Context
	migrationCancel          context.CancelFunc
	migrationWG              sync.WaitGroup
	migrationRunning         bool
	closing                  bool
	failureCtx               context.Context
	failureCancel            context.CancelFunc
	channels                 *ChannelCoordinator
	providers                *ProviderRuntime
	chatMu                   sync.Mutex
	activeRuns               map[string]activeChatRun
	chatCtx                  context.Context
	chatCancel               context.CancelFunc
	chatWG                   sync.WaitGroup
	capacityCtx              context.Context
	capacityCancel           context.CancelFunc
	capacityWG               sync.WaitGroup
	capacityMu               sync.Mutex
	capacityRuns             map[string]*capacityRun
	life                     RuntimeLifecycle
	drainState               DrainState
	drainStartedAt           time.Time
	drainCompletedAt         time.Time
	drainError               string
	faultInjectionEnabled    bool
}

// ConfigureIdentityProvider enables production identity mode. Development
// sessions are not consulted while a production provider is configured.
func (h *AdminHandler) ConfigureIdentityProvider(provider IdentityProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.faultInjectionEnabled = false
	h.identityProvider = provider
	h.sessions = make(map[string]*developmentSession)
	h.productionSessions = make(map[string]*productionSession)
	if source, ok := provider.(interface{ Assignments() []TenantAssignment }); ok {
		for _, assignment := range source.Assignments() {
			h.platform.seedTenant(assignment)
		}
	}
}

func (h *AdminHandler) ConfigureGovernance(center *GovernanceCenter) {
	if center == nil {
		return
	}
	h.governance = center
	if governed, ok := h.runtime.worker.runner.(interface{ SetGovernance(*GovernanceCenter) }); ok {
		governed.SetGovernance(center)
	}
}

func NewAdminHandler(platform *MemoryPlatform, identity DevelopmentIdentity) *AdminHandler {
	if platform == nil {
		platform = NewMemoryPlatform()
	}
	for _, assignment := range identity.Assignments {
		platform.seedTenant(assignment)
	}
	migrationCtx, migrationCancel := context.WithCancel(context.Background())
	failureCtx, failureCancel := context.WithCancel(context.Background())
	chatCtx, chatCancel := context.WithCancel(context.Background())
	capacityCtx, capacityCancel := context.WithCancel(context.Background())
	channels := NewChannelCoordinator(NewMockChannel())
	return &AdminHandler{
		platform: platform, identity: identity, sessions: make(map[string]*developmentSession),
		governance: NewGovernanceCenter(), productionSessions: make(map[string]*productionSession),
		runtime: NewRuntime(platform, EchoRunner{}, nil), backends: newBackendRegistry(NewInMemoryStore(), nil),
		migrations: make(map[string]migrationResult), backendCatalog: map[string]backendSelection{"inmemory": {Backend: "inmemory"}},
		migrationCtx: migrationCtx, migrationCancel: migrationCancel, failureCtx: failureCtx, failureCancel: failureCancel,
		channels: channels, activeRuns: make(map[string]activeChatRun),
		chatCtx: chatCtx, chatCancel: chatCancel, capacityCtx: capacityCtx, capacityCancel: capacityCancel,
		capacityRuns: make(map[string]*capacityRun), drainState: DrainIdle, faultInjectionEnabled: true,
	}
}

func (h *AdminHandler) ConfigureFaultInjection(enabled bool) {
	h.mu.Lock()
	h.faultInjectionEnabled = enabled
	h.mu.Unlock()
}

// ConfigureDataStore replaces the Stage 2 data backend. It is safe to call
// during process composition before serving requests.
func (h *AdminHandler) ConfigureDataStore(store DataStore) {
	if store == nil {
		return
	}
	h.backends.replaceDefault(store)
}

type backendSelection struct {
	Backend string `json:"backend"`
	Address string `json:"address"`
}

func (h *AdminHandler) ConfigureBackendSelections(path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backendSelectionPath = path
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var selections map[string]backendSelection
	if err := json.Unmarshal(data, &selections); err != nil {
		return err
	}
	h.backends.setSelections(selections)
	return nil
}
func (h *AdminHandler) ConfigureMigration(sourceAddress, destinationPath, checkpointPath string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.migrationSourceAddress = sourceAddress
	h.migrationDestinationPath = destinationPath
	h.migrationCheckpointPath = checkpointPath
}
func (h *AdminHandler) ConfigureBackendCatalog(redisAddress, sqlitePath string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if redisAddress != "" {
		h.backendCatalog["redis"] = backendSelection{Backend: "redis", Address: redisAddress}
	}
	if sqlitePath != "" {
		h.backendCatalog["sqlite"] = backendSelection{Backend: "sqlite", Address: sqlitePath}
	}
}

func (h *AdminHandler) ConfigurePostgresBackend(dsn string) {
	if dsn == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backendCatalog["postgres"] = backendSelection{Backend: "postgres", Address: dsn}
}

func (h *AdminHandler) selectBackend(tenantID string, selection backendSelection, store DataStore) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := h.backends.selectionSnapshot()
	next[tenantID] = selection
	if h.backendSelectionPath != "" {
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := os.WriteFile(h.backendSelectionPath, data, 0600); err != nil {
			return err
		}
	}
	h.backends.replace(tenantID, selection, store)
	return nil
}

func (h *AdminHandler) Close() error {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return nil
	}
	h.closing = true
	h.mu.Unlock()
	h.chatCancel()
	h.capacityCancel()
	if h.providers != nil {
		h.providers.Close()
	}
	h.backends.beginClose()
	h.migrationCancel()
	h.migrationWG.Wait()
	h.chatWG.Wait()
	h.capacityWG.Wait()
	h.failureCancel()
	runtimeErr := h.runtime.Close()
	storeErr := h.backends.close()
	if runtimeErr != nil {
		return runtimeErr
	}
	return storeErr
}

func (h *AdminHandler) ConfigureProviderRuntime(providers *ProviderRuntime) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providers = providers
	if providers != nil {
		h.channels.RegisterAdapter(ChannelTelegram, TelegramChannel{Sender: providers.sendTelegram})
		h.channels.RegisterAdapter(ChannelEnterpriseWeChat, EnterpriseWeChatChannel{Sender: providers.sendWeCom})
	}
}

func (h *AdminHandler) acquireStore(tenantID string) (DataStore, func(), error) {
	return h.backends.acquire(tenantID)
}

// ConfigureRuntime replaces the default fake runtime and attaches lifecycle
// admission. It is intended for process composition and deterministic tests.
func (h *AdminHandler) ConfigureRuntime(runner RunnerAdapter, life RuntimeLifecycle) {
	_ = h.runtime.Close()
	if governed, ok := runner.(interface{ SetGovernance(*GovernanceCenter) }); ok {
		governed.SetGovernance(h.governance)
	}
	h.runtime = NewRuntime(h.platform, runner, life)
	h.mu.Lock()
	h.life = life
	h.mu.Unlock()
}

func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auditWriter := &auditResponseWriter{ResponseWriter: w, buffered: r.Method != http.MethodGet || r.URL.Path == "/api/v1/auth/me"}
	w = auditWriter
	started := time.Now()
	defer func() {
		auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := h.auditHTTPRequest(auditCtx, r, auditWriter, started); err != nil && auditWriter.buffered && (auditWriter.status == 0 || auditWriter.status < http.StatusBadRequest) {
			auditWriter.auditUnavailable()
		}
		auditWriter.commit()
	}()
	if isDataPath(r.URL.Path) {
		trusted := h.trustedRequest(w, r)
		if trusted == nil {
			return
		}
		h.handleDataResource(w, trusted, strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/"), "/"), "/"))
		return
	}
	switch r.URL.Path {
	case "/api/v1/auth/login":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
			return
		}
		h.handleProductionLogin(w, r)
	case "/api/v1/auth/logout":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
			return
		}
		h.handleProductionLogout(w, r)
	case "/api/v1/auth/me":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
			return
		}
		h.handleIdentity(w, r)
	case "/api/v1/auth/switch-tenant":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
			return
		}
		h.handleSwitchTenant(w, r)
	case "/api/v1/admin/tenants":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleTenants(w, trusted)
		}
	case "/api/v1/chat/bindings":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleChannelBindings(w, trusted)
		}
	case "/api/v1/chat/channels/mock/callback":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleMockChannelCallback(w, trusted)
		}
	case "/api/v1/chat/mock/faults":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleMockFaults(w, trusted)
		}
	case "/api/v1/admin/providers/status":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleProviderStatus(w, trusted)
		}
	case "/api/v1/admin/providers/deliveries":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleProviderDeliveries(w, trusted)
		}
	case "/api/v1/admin/providers/routes":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleProviderRoutes(w, trusted)
		}
	case "/api/v1/admin/providers/replay":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleProviderReplay(w, trusted)
		}
	case "/api/v1/chat/sessions":
		trusted := h.trustedRequest(w, r)
		if trusted != nil {
			h.handleChatSessionResource(w, trusted, []string{})
		}
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/governance/") {
			trusted := h.trustedRequest(w, r)
			if trusted != nil {
				h.handleGovernance(w, trusted)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/chat/bindings/") {
			trusted := h.trustedRequest(w, r)
			if trusted != nil {
				h.handleChannelBindingResource(w, trusted, strings.TrimPrefix(r.URL.Path, "/api/v1/chat/bindings/"))
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/chat/sessions/") {
			trusted := h.trustedRequest(w, r)
			if trusted != nil {
				h.handleChatSessionResource(w, trusted, strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/chat/sessions/"), "/"), "/"))
			}
			return
		}
		if h.handleAdminResource(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/tenants/") {
			trusted := h.trustedRequest(w, r)
			if trusted != nil {
				h.handleTenant(w, trusted, strings.TrimPrefix(r.URL.Path, "/api/v1/admin/tenants/"))
			}
			return
		}
		http.NotFound(w, r)
	}
}

func (h *AdminHandler) trustedRequest(w http.ResponseWriter, r *http.Request) *http.Request {
	h.mu.Lock()
	provider := h.identityProvider
	h.mu.Unlock()
	if provider != nil {
		identity, err := h.productionIdentity(r, "")
		if err != nil {
			status, code, message := identityPublicError(err)
			writeError(w, status, code, message)
			return nil
		}
		tenant := TenantContext{TenantID: identity.ActiveTenantID, UserID: identity.ID, Role: identity.ActiveRole, Assignments: append([]TenantAssignment(nil), identity.Assignments...)}
		ctx := WithTenantContext(r.Context(), tenant)
		markAuditIdentity(w, tenant)
		return r.WithContext(ctx)
	}
	_, session, ok := h.session(w, r)
	if !ok {
		return r
	}
	assignment, ok := h.assignment(session.activeTenantID)
	if !ok {
		return r
	}
	identity := h.identitySnapshot()
	tenant := TenantContext{TenantID: assignment.TenantID, UserID: identity.ID, Role: assignment.Role, Assignments: append([]TenantAssignment(nil), identity.Assignments...)}
	markAuditIdentity(w, tenant)
	ctx := WithTenantContext(r.Context(), tenant)
	return r.WithContext(ctx)
}

func (h *AdminHandler) handleTenants(w http.ResponseWriter, r *http.Request) {
	trusted, ok := TenantContextFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"items": h.platform.listTenantsFor(trusted)})
	case http.MethodPost:
		if trusted.Role != RolePlatformAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "platform administrator role is required")
			return
		}
		var request struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || !validResourceID(request.ID) || !validDisplayName(request.Name) {
			writeError(w, http.StatusBadRequest, "invalid_tenant", "id and name must be valid")
			return
		}
		tenant := Tenant{ID: request.ID, Name: strings.TrimSpace(request.Name), CreatedAt: time.Now().UTC()}
		if !h.platform.createTenant(tenant) {
			writeError(w, http.StatusConflict, "tenant_exists", "tenant identifier already exists")
			return
		}
		h.mu.Lock()
		h.identity.Assignments = append(h.identity.Assignments, TenantAssignment{TenantID: tenant.ID, TenantName: tenant.Name, Role: RolePlatformAdmin})
		h.mu.Unlock()
		writeJSON(w, http.StatusCreated, tenant)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
	}
}

func (h *AdminHandler) handleTenant(w http.ResponseWriter, r *http.Request, id string) {
	trusted, ok := TenantContextFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
		return
	}
	if !tenantCanSee(trusted, id) {
		writeError(w, http.StatusNotFound, "tenant_not_found", "tenant was not found")
		return
	}
	tenant, exists := h.platform.tenant(id)
	if !exists {
		writeError(w, http.StatusNotFound, "tenant_not_found", "tenant was not found")
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

var resourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)

func validResourceID(id string) bool { return resourceIDPattern.MatchString(id) }

func validDisplayName(name string) bool {
	length := len([]rune(strings.TrimSpace(name)))
	return length >= 2 && length <= 80
}

func (h *AdminHandler) handleIdentity(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	provider := h.identityProvider
	h.mu.Unlock()
	if provider != nil {
		identity, err := h.productionIdentity(r, "")
		if err != nil {
			status, code, message := identityPublicError(err)
			writeError(w, status, code, message)
			return
		}
		markAuditIdentity(w, TenantContext{TenantID: identity.ActiveTenantID, UserID: identity.ID, Role: identity.ActiveRole, Assignments: append([]TenantAssignment(nil), identity.Assignments...)})
		writeJSON(w, http.StatusOK, identity)
		return
	}
	_, session, ok := h.session(w, r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "development_identity_unavailable", "development identity has no tenant assignments")
		return
	}
	assignment, _ := h.assignment(session.activeTenantID)
	identity := h.identitySnapshot()
	markAuditIdentity(w, TenantContext{TenantID: assignment.TenantID, UserID: identity.ID, Role: assignment.Role, Assignments: append([]TenantAssignment(nil), identity.Assignments...)})
	writeJSON(w, http.StatusOK, identityResponse{
		ID: identity.ID, Name: identity.Name, ActiveTenantID: assignment.TenantID,
		ActiveRole: assignment.Role, Assignments: identity.Assignments, AuthMode: "development",
	})
}

func (h *AdminHandler) handleSwitchTenant(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TenantID string `json:"tenant_id"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.TenantID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "tenant_id is required")
		return
	}
	h.mu.Lock()
	provider := h.identityProvider
	h.mu.Unlock()
	if provider != nil {
		identity, err := h.productionIdentity(r, request.TenantID)
		if err != nil {
			status, code, message := identityPublicError(err)
			writeError(w, status, code, message)
			return
		}
		markAuditIdentity(w, TenantContext{TenantID: identity.ActiveTenantID, UserID: identity.ID, Role: identity.ActiveRole, Assignments: append([]TenantAssignment(nil), identity.Assignments...)})
		writeJSON(w, http.StatusOK, identity)
		return
	}
	assignment, approved := h.assignment(request.TenantID)
	if !approved {
		writeError(w, http.StatusForbidden, "tenant_not_assigned", "tenant is not assigned to the development identity")
		return
	}
	token, _, ok := h.session(w, r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "development_identity_unavailable", "development identity has no tenant assignments")
		return
	}
	h.mu.Lock()
	if session := h.sessions[token]; session != nil {
		session.activeTenantID = request.TenantID
		session.lastSeen = time.Now().UTC()
	}
	h.mu.Unlock()
	identity := h.identitySnapshot()
	markAuditIdentity(w, TenantContext{TenantID: assignment.TenantID, UserID: identity.ID, Role: assignment.Role, Assignments: append([]TenantAssignment(nil), identity.Assignments...)})
	writeJSON(w, http.StatusOK, identityResponse{
		ID: identity.ID, Name: identity.Name, ActiveTenantID: assignment.TenantID,
		ActiveRole: assignment.Role, Assignments: identity.Assignments, AuthMode: "development",
	})
}

func (h *AdminHandler) assignment(tenantID string) (TenantAssignment, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.assignmentLocked(tenantID)
}

func (h *AdminHandler) assignmentLocked(tenantID string) (TenantAssignment, bool) {
	for _, assignment := range h.identity.Assignments {
		if assignment.TenantID == tenantID {
			return assignment, true
		}
	}
	return TenantAssignment{}, false
}

func (h *AdminHandler) identitySnapshot() DevelopmentIdentity {
	h.mu.Lock()
	defer h.mu.Unlock()
	identity := h.identity
	identity.Assignments = append([]TenantAssignment(nil), h.identity.Assignments...)
	return identity
}

func (h *AdminHandler) session(w http.ResponseWriter, r *http.Request) (string, developmentSession, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cookie, err := r.Cookie("trpc_dev_session"); err == nil {
		if session := h.sessions[cookie.Value]; session != nil {
			session.lastSeen = time.Now().UTC()
			return cookie.Value, *session, true
		}
	}
	if len(h.identity.Assignments) == 0 {
		return "", developmentSession{}, false
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", developmentSession{}, false
	}
	if len(h.sessions) >= maxDevelopmentSessions {
		var oldestToken string
		var oldest time.Time
		for candidate, session := range h.sessions {
			if oldestToken == "" || session.lastSeen.Before(oldest) {
				oldestToken, oldest = candidate, session.lastSeen
			}
		}
		delete(h.sessions, oldestToken)
	}
	token := hex.EncodeToString(tokenBytes)
	session := &developmentSession{activeTenantID: h.identity.Assignments[0].TenantID, lastSeen: time.Now().UTC()}
	h.sessions[token] = session
	http.SetCookie(w, &http.Cookie{
		Name: "trpc_dev_session", Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	return token, *session, true
}
