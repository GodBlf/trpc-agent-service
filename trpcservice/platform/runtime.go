package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type RuntimeLifecycle interface {
	Acquire() (func(), bool)
	Done() <-chan struct{}
	IsClosing() bool
}

type ComponentRole string
type ComponentLifecycle string

const (
	ComponentGateway     ComponentRole      = "gateway"
	ComponentWorker      ComponentRole      = "worker"
	LifecycleHealthy     ComponentLifecycle = "healthy"
	LifecycleUnavailable ComponentLifecycle = "unavailable"
	LifecycleClosing     ComponentLifecycle = "closing"
	LifecycleError       ComponentLifecycle = "error"
)

type RuntimeComponentStatus struct {
	ID        string             `json:"id"`
	Role      ComponentRole      `json:"role"`
	Available bool               `json:"available"`
	Lifecycle ComponentLifecycle `json:"lifecycle"`
	Active    int64              `json:"active_executions"`
	Completed int64              `json:"completed_executions"`
	Failed    int64              `json:"failed_executions"`
}

type runtimeError struct {
	code string
	err  error
}

func (e *runtimeError) Error() string { return e.code }
func (e *runtimeError) Unwrap() error { return e.err }

type Runtime struct {
	platform       *MemoryPlatform
	worker         *StatelessWorker
	life           RuntimeLifecycle
	gates          sessionGates
	global         runtimeCounters
	counterMu      sync.Mutex
	tenantCounters map[string]*runtimeCounters
}

type runtimeCounters struct{ active, complete, failed atomic.Int64 }

// StatelessWorker is the Stage 1 execution boundary. It owns no Tenant,
// Deployment, or Session state and delegates only the resolved request.
type StatelessWorker struct {
	runner    RunnerAdapter
	available atomic.Bool
	lastError atomic.Bool
}

func NewStatelessWorker(runner RunnerAdapter) *StatelessWorker {
	worker := &StatelessWorker{runner: runner}
	worker.available.Store(true)
	return worker
}

func (w *StatelessWorker) Execute(ctx context.Context, request GatewayRequest) (GatewayResponse, error) {
	result, err := w.runner.Run(ctx, runnerRequestFromGateway(request))
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			w.lastError.Store(true)
		}
		return GatewayResponse{}, err
	}
	w.lastError.Store(false)
	return GatewayResponse{
		SessionID:   request.SessionID,
		Output:      result.Output,
		UsageTokens: result.UsageTokens,
		UsageKnown:  result.UsageKnown,
	}, nil
}

func (w *StatelessWorker) ExecuteEvents(ctx context.Context, request GatewayRequest) (<-chan RuntimeEvent, error) {
	streaming, ok := w.runner.(StreamingRunnerAdapter)
	if !ok {
		events := make(chan RuntimeEvent, 4)
		go func() {
			defer close(events)
			response, err := w.Execute(ctx, request)
			if err != nil {
				eventType := "run.failed"
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					eventType = "run.cancelled"
				}
				events <- RuntimeEvent{Type: eventType, Data: runtimeEventData(runnerRequestFromGateway(request), map[string]string{"error": err.Error()})}
				return
			}
			data := runtimeEventData(runnerRequestFromGateway(request), map[string]string{"delta": response.Output, "output": response.Output})
			if response.UsageKnown {
				data["usage_known"] = "true"
				data["usage_tokens"] = strconv.FormatInt(response.UsageTokens, 10)
			}
			events <- RuntimeEvent{Type: "message.delta", Data: data}
			events <- RuntimeEvent{Type: "message.completed", Data: data}
			events <- RuntimeEvent{Type: "run.completed", Data: runtimeEventData(runnerRequestFromGateway(request), data)}
		}()
		return events, nil
	}
	return streaming.RunEvents(ctx, runnerRequestFromGateway(request))
}

func runnerRequestFromGateway(request GatewayRequest) RunnerRequest {
	return RunnerRequest{
		TenantID: request.TenantID, AppID: request.AppID, SessionID: request.SessionID, UserID: request.UserID,
		Channel: request.Channel, ExternalSubject: request.ExternalSubject, Input: request.Input, RequestID: request.RequestID,
		TraceID: request.TraceID, DeploymentID: request.DeploymentID, VersionID: request.VersionID, PolicyRevision: request.PolicyRevision,
	}
}

func NewRuntime(platform *MemoryPlatform, runner RunnerAdapter, life RuntimeLifecycle) *Runtime {
	if runner == nil {
		runner = EchoRunner{}
	}
	return &Runtime{platform: platform, worker: NewStatelessWorker(runner), life: life, gates: sessionGates{items: make(map[string]*sessionGate)}, tenantCounters: make(map[string]*runtimeCounters)}
}

func (rt *Runtime) SetWorkerAvailable(available bool) { rt.worker.available.Store(available) }

func (rt *Runtime) Close() error {
	if streaming, ok := rt.worker.runner.(StreamingRunnerAdapter); ok {
		return streaming.Close()
	}
	return nil
}

func (rt *Runtime) RetireVersion(versionID string) error {
	if streaming, ok := rt.worker.runner.(interface{ RetireVersion(string) error }); ok {
		return streaming.RetireVersion(versionID)
	}
	return nil
}

func (rt *Runtime) Stream(ctx context.Context, tenant TenantContext, request GatewayRequest) (<-chan RuntimeEvent, error) {
	if tenant.TenantID == "" {
		return nil, &runtimeError{code: "tenant_context_missing"}
	}
	if !canOperate(tenant.Role) {
		return nil, &runtimeError{code: "forbidden"}
	}
	if !rt.worker.available.Load() {
		return nil, &runtimeError{code: "worker_unavailable"}
	}
	if request.AppID == "" || request.SessionID == "" || request.Input == "" {
		return nil, &runtimeError{code: "invalid_request"}
	}
	deployment, found := rt.platform.activeDeployment(tenant.TenantID, request.AppID)
	if !found {
		return nil, &runtimeError{code: "active_deployment_not_found"}
	}
	request.TenantID, request.DeploymentID, request.VersionID, request.UserID = tenant.TenantID, deployment.ID, deployment.VersionID, tenant.UserID
	streamCtx, cancel := context.WithCancel(ctx)
	var releaseLife func()
	if rt.life != nil {
		var ok bool
		releaseLife, ok = rt.life.Acquire()
		if !ok {
			cancel()
			return nil, &runtimeError{code: "service_closing"}
		}
		go func() {
			select {
			case <-rt.life.Done():
				cancel()
			case <-streamCtx.Done():
			}
		}()
	}
	streamCtx = context.WithValue(streamCtx, governanceExternalCompletionContextKey{}, true)
	releaseGate, err := rt.gates.acquire(streamCtx, tenant.TenantID+"\x00"+request.AppID+"\x00"+request.SessionID)
	if err != nil {
		if releaseLife != nil {
			releaseLife()
		}
		cancel()
		return nil, &runtimeError{code: "request_cancelled", err: err}
	}
	events, err := rt.worker.ExecuteEvents(streamCtx, request)
	if err != nil {
		releaseGate()
		if releaseLife != nil {
			releaseLife()
		}
		cancel()
		return nil, err
	}
	output := make(chan RuntimeEvent, 4)
	go func() {
		defer close(output)
		defer releaseGate()
		if releaseLife != nil {
			defer releaseLife()
		}
		defer cancel()
		for event := range events {
			select {
			case <-streamCtx.Done():
				return
			case output <- event:
			}
		}
	}()
	return output, nil
}

func (rt *Runtime) Handle(ctx context.Context, tenant TenantContext, request GatewayRequest) (GatewayResponse, error) {
	if tenant.TenantID == "" {
		return GatewayResponse{}, &runtimeError{code: "tenant_context_missing"}
	}
	if !canOperate(tenant.Role) {
		return GatewayResponse{}, &runtimeError{code: "forbidden"}
	}
	if !rt.worker.available.Load() {
		return GatewayResponse{}, &runtimeError{code: "worker_unavailable"}
	}
	if request.AppID == "" || request.SessionID == "" || request.Input == "" {
		return GatewayResponse{}, &runtimeError{code: "invalid_request"}
	}
	deployment, found := rt.platform.activeDeployment(tenant.TenantID, request.AppID)
	if !found {
		return GatewayResponse{}, &runtimeError{code: "active_deployment_not_found"}
	}
	request.DeploymentID, request.VersionID = deployment.ID, deployment.VersionID
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var releaseLife func()
	if rt.life != nil {
		var ok bool
		releaseLife, ok = rt.life.Acquire()
		if !ok {
			return GatewayResponse{}, &runtimeError{code: "service_closing"}
		}
		defer releaseLife()
		go func() {
			select {
			case <-rt.life.Done():
				cancel()
			case <-runCtx.Done():
			}
		}()
	}
	releaseGate, err := rt.gates.acquire(runCtx, tenant.TenantID+"\x00"+request.AppID+"\x00"+request.SessionID)
	if err != nil {
		return GatewayResponse{}, &runtimeError{code: "request_cancelled", err: err}
	}
	defer releaseGate()

	if err := runCtx.Err(); err != nil {
		return GatewayResponse{}, &runtimeError{code: "request_cancelled", err: err}
	}
	if rt.life != nil && rt.life.IsClosing() {
		return GatewayResponse{}, &runtimeError{code: "request_cancelled", err: context.Canceled}
	}
	counters := rt.countersFor(tenant.TenantID)
	rt.global.active.Add(1)
	counters.active.Add(1)
	defer rt.global.active.Add(-1)
	defer counters.active.Add(-1)
	request.TenantID, request.UserID = tenant.TenantID, tenant.UserID
	result, err := rt.worker.Execute(runCtx, request)
	if err != nil {
		rt.global.failed.Add(1)
		counters.failed.Add(1)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || runCtx.Err() != nil {
			return GatewayResponse{}, &runtimeError{code: "request_cancelled", err: err}
		}
		return GatewayResponse{}, &runtimeError{code: "runner_error", err: err}
	}
	rt.global.complete.Add(1)
	counters.complete.Add(1)
	return result, nil
}

func (rt *Runtime) Status() []RuntimeComponentStatus {
	return rt.status(&rt.global)
}

func (rt *Runtime) StatusFor(tenant TenantContext) []RuntimeComponentStatus {
	return rt.status(rt.countersFor(tenant.TenantID))
}

func (rt *Runtime) status(counters *runtimeCounters) []RuntimeComponentStatus {
	lifecycle := LifecycleHealthy
	if rt.life != nil && rt.life.IsClosing() {
		lifecycle = LifecycleClosing
	}
	workerLifecycle := lifecycle
	if !rt.worker.available.Load() {
		workerLifecycle = LifecycleUnavailable
	} else if lifecycle == LifecycleHealthy && rt.worker.lastError.Load() {
		workerLifecycle = LifecycleError
	}
	active, complete, failed := counters.active.Load(), counters.complete.Load(), counters.failed.Load()
	return []RuntimeComponentStatus{
		{ID: "gateway-local", Role: ComponentGateway, Available: lifecycle == LifecycleHealthy, Lifecycle: lifecycle, Active: active, Completed: complete, Failed: failed},
		{ID: "worker-local", Role: ComponentWorker, Available: workerLifecycle == LifecycleHealthy, Lifecycle: workerLifecycle, Active: active, Completed: complete, Failed: failed},
	}
}

func (rt *Runtime) countersFor(tenantID string) *runtimeCounters {
	rt.counterMu.Lock()
	defer rt.counterMu.Unlock()
	counters := rt.tenantCounters[tenantID]
	if counters == nil {
		counters = &runtimeCounters{}
		rt.tenantCounters[tenantID] = counters
	}
	return counters
}

func (p *MemoryPlatform) activeDeployment(tenantID, appID string) (Deployment, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, deployment := range p.deployments {
		if deployment.TenantID == tenantID && deployment.AgentAppID == appID && deployment.Status == DeploymentActive && deployment.VersionID != "" {
			return deployment, true
		}
	}
	return Deployment{}, false
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
	requestID := r.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(requestID) {
		requestID = time.Now().UTC().Format("20060102150405.000000000")
	}
	request.RequestID = requestID
	store, releaseStore, err := h.acquireStore(tenant.TenantID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "service_closing", "service is closing")
		return
	}
	defer releaseStore()
	input, _ := json.Marshal(map[string]string{"app_id": request.AppID, "input": request.Input})
	if err := store.AppendSessionEvent(r.Context(), SessionEvent{TenantID: tenant.TenantID, SessionID: request.SessionID, IdempotencyKey: requestID + ":input", Type: "message.input", Payload: input}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "session event could not be persisted")
		return
	}
	response, err := h.runtime.Handle(r.Context(), tenant, request)
	if err != nil {
		failureCtx, cancelFailure := context.WithTimeout(h.failureCtx, 2*time.Second)
		_ = store.AppendSessionEvent(failureCtx, SessionEvent{TenantID: tenant.TenantID, SessionID: request.SessionID, IdempotencyKey: requestID + ":failed", Type: "run.failed", Payload: []byte(err.Error())})
		cancelFailure()
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
	output, _ := json.Marshal(map[string]string{"output": response.Output})
	if err := store.AppendSessionEvent(r.Context(), SessionEvent{TenantID: tenant.TenantID, SessionID: request.SessionID, IdempotencyKey: requestID + ":output", Type: "message.output", Payload: output}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "session event could not be persisted")
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
	writeJSON(w, http.StatusOK, map[string]any{"items": h.runtime.StatusFor(tenant)})
}
