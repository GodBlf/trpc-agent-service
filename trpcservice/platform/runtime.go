package platform

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
)

type RuntimeLifecycle interface {
	Acquire() (func(), bool)
	Done() <-chan struct{}
	IsClosing() bool
}

type RuntimeComponentStatus struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Available bool   `json:"available"`
	Lifecycle string `json:"lifecycle"`
	Active    int64  `json:"active_executions"`
	Completed int64  `json:"completed_executions"`
	Failed    int64  `json:"failed_executions"`
}

type runtimeError struct {
	code string
	err  error
}

func (e *runtimeError) Error() string { return e.code }
func (e *runtimeError) Unwrap() error { return e.err }

type Runtime struct {
	platform        *MemoryPlatform
	runner          RunnerAdapter
	life            RuntimeLifecycle
	gates           sessionGates
	active          atomic.Int64
	complete        atomic.Int64
	failed          atomic.Int64
	workerAvailable atomic.Bool
}

func NewRuntime(platform *MemoryPlatform, runner RunnerAdapter, life RuntimeLifecycle) *Runtime {
	if runner == nil {
		runner = EchoRunner{}
	}
	runtime := &Runtime{platform: platform, runner: runner, life: life, gates: sessionGates{items: make(map[string]*sessionGate)}}
	runtime.workerAvailable.Store(true)
	return runtime
}

func (rt *Runtime) SetWorkerAvailable(available bool) { rt.workerAvailable.Store(available) }

func (rt *Runtime) Handle(ctx context.Context, tenant TenantContext, request GatewayRequest) (GatewayResponse, error) {
	if tenant.TenantID == "" {
		return GatewayResponse{}, &runtimeError{code: "tenant_context_missing"}
	}
	if !canOperate(tenant.Role) {
		return GatewayResponse{}, &runtimeError{code: "forbidden"}
	}
	if !rt.workerAvailable.Load() {
		return GatewayResponse{}, &runtimeError{code: "worker_unavailable"}
	}
	if request.AppID == "" || request.SessionID == "" || request.Input == "" {
		return GatewayResponse{}, &runtimeError{code: "invalid_request"}
	}
	if !rt.platform.hasActiveDeployment(tenant.TenantID, request.AppID) {
		return GatewayResponse{}, &runtimeError{code: "active_deployment_not_found"}
	}
	var releaseLife func()
	if rt.life != nil {
		var ok bool
		releaseLife, ok = rt.life.Acquire()
		if !ok {
			return GatewayResponse{}, &runtimeError{code: "service_closing"}
		}
		defer releaseLife()
	}
	releaseGate, err := rt.gates.acquire(ctx, tenant.TenantID+"\x00"+request.AppID+"\x00"+request.SessionID)
	if err != nil {
		return GatewayResponse{}, &runtimeError{code: "request_cancelled", err: err}
	}
	defer releaseGate()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if rt.life != nil {
		go func() {
			select {
			case <-rt.life.Done():
				cancel()
			case <-runCtx.Done():
			}
		}()
	}
	rt.active.Add(1)
	defer rt.active.Add(-1)
	result, err := rt.runner.Run(runCtx, RunnerRequest{AppID: request.AppID, SessionID: request.SessionID, Input: request.Input})
	if err != nil {
		rt.failed.Add(1)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || runCtx.Err() != nil {
			return GatewayResponse{}, &runtimeError{code: "request_cancelled", err: err}
		}
		return GatewayResponse{}, &runtimeError{code: "runner_error", err: err}
	}
	rt.complete.Add(1)
	return GatewayResponse{SessionID: request.SessionID, Output: result.Output}, nil
}

func (rt *Runtime) Status() []RuntimeComponentStatus {
	lifecycle := "healthy"
	if rt.life != nil && rt.life.IsClosing() {
		lifecycle = "closing"
	}
	workerLifecycle := lifecycle
	if !rt.workerAvailable.Load() {
		workerLifecycle = "unavailable"
	}
	active, complete, failed := rt.active.Load(), rt.complete.Load(), rt.failed.Load()
	return []RuntimeComponentStatus{
		{ID: "gateway-local", Role: "gateway", Available: lifecycle == "healthy", Lifecycle: lifecycle, Active: active, Completed: complete, Failed: failed},
		{ID: "worker-local", Role: "worker", Available: workerLifecycle == "healthy", Lifecycle: workerLifecycle, Active: active, Completed: complete, Failed: failed},
	}
}

func (p *MemoryPlatform) hasActiveDeployment(tenantID, appID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, deployment := range p.deployments {
		if deployment.TenantID == tenantID && deployment.AgentAppID == appID && deployment.Status == DeploymentActive && deployment.VersionID != "" {
			return true
		}
	}
	return false
}

type sessionGate struct {
	token chan struct{}
	refs  int
}
type sessionGates struct {
	mu    sync.Mutex
	items map[string]*sessionGate
}

func (g *sessionGates) acquire(ctx context.Context, key string) (func(), error) {
	g.mu.Lock()
	gate := g.items[key]
	if gate == nil {
		gate = &sessionGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		g.items[key] = gate
	}
	gate.refs++
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		g.releaseRef(key, gate)
		return nil, ctx.Err()
	case <-gate.token:
		var once sync.Once
		return func() { once.Do(func() { gate.token <- struct{}{}; g.releaseRef(key, gate) }) }, nil
	}
}

func (g *sessionGates) releaseRef(key string, gate *sessionGate) {
	g.mu.Lock()
	defer g.mu.Unlock()
	gate.refs--
	if gate.refs == 0 {
		delete(g.items, key)
	}
}

func (h *AdminHandler) handleRoutedRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	var request GatewayRequest
	if err := decodeStrict(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "app_id, session_id, and input are required")
		return
	}
	response, err := h.runtime.Handle(r.Context(), tenant, request)
	if err != nil {
		code := err.Error()
		status := http.StatusBadGateway
		switch code {
		case "invalid_request":
			status = http.StatusBadRequest
		case "forbidden":
			status = http.StatusForbidden
		case "active_deployment_not_found":
			status = http.StatusNotFound
		case "request_cancelled":
			status = http.StatusRequestTimeout
		case "service_closing", "worker_unavailable":
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, code, "routed execution failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *AdminHandler) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
		return
	}
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	if tenant.Role == RoleViewer {
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": h.runtime.Status()})
}
