package platform

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/lifecycle"
)

func activeTestPlatform(t *testing.T) *MemoryPlatform {
	t.Helper()
	store := NewMemoryPlatform()
	store.seedTenant(TenantAssignment{TenantID: "tenant-one", TenantName: "One", Role: RoleOperator})
	if !store.createApp(AgentApp{ID: "app-one", TenantID: "tenant-one", Name: "App One"}) {
		t.Fatal("create app")
	}
	deployment := Deployment{ID: "deploy-one", TenantID: "tenant-one", AgentAppID: "app-one", Status: DeploymentDraft}
	if !store.createDeployment(deployment) {
		t.Fatal("create deployment")
	}
	version := store.createVersion(deployment, map[string]any{"model": "fake"})
	published, _, ok := store.transition(deployment, DeploymentPublished, version.ID)
	if !ok {
		t.Fatal("publish")
	}
	if _, _, ok := store.transition(published, DeploymentActive, ""); !ok {
		t.Fatal("activate")
	}
	return store
}

type blockingRunner struct {
	mu      sync.Mutex
	entered chan string
	release chan struct{}
	active  int
	max     int
}

func (r *blockingRunner) Run(ctx context.Context, request RunnerRequest) (RunnerResponse, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.max {
		r.max = r.active
	}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active--; r.mu.Unlock() }()
	select {
	case r.entered <- request.SessionID:
	case <-ctx.Done():
		return RunnerResponse{}, ctx.Err()
	}
	select {
	case <-r.release:
		return RunnerResponse{Output: "fake:" + request.Input}, nil
	case <-ctx.Done():
		return RunnerResponse{}, ctx.Err()
	}
}

func TestRuntimeSerializesSameSessionAndRunsDifferentSessionsConcurrently(t *testing.T) {
	runner := &blockingRunner{entered: make(chan string, 4), release: make(chan struct{}, 4)}
	runtime := NewRuntime(activeTestPlatform(t), runner, nil)
	tenant := TenantContext{TenantID: "tenant-one", Role: RoleOperator}
	run := func(session string, done chan<- error) {
		_, err := runtime.Handle(context.Background(), tenant, GatewayRequest{AppID: "app-one", SessionID: session, Input: session})
		done <- err
	}
	done := make(chan error, 3)
	go run("same", done)
	if got := <-runner.entered; got != "same" {
		t.Fatalf("first = %s", got)
	}
	go run("same", done)
	go run("other", done)
	if got := <-runner.entered; got != "other" {
		t.Fatalf("parallel = %s", got)
	}
	select {
	case got := <-runner.entered:
		t.Fatalf("same Session entered early: %s", got)
	case <-time.After(20 * time.Millisecond):
	}
	runner.release <- struct{}{}
	if got := <-runner.entered; got != "same" {
		t.Fatalf("queued = %s", got)
	}
	runner.release <- struct{}{}
	runner.release <- struct{}{}
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	runner.mu.Lock()
	max := runner.max
	runner.mu.Unlock()
	if max != 2 {
		t.Fatalf("max concurrency = %d, want 2", max)
	}
}

func TestRuntimeDoesNotShareSessionGateAcrossTenants(t *testing.T) {
	store := activeTestPlatform(t)
	store.seedTenant(TenantAssignment{TenantID: "tenant-two", TenantName: "Two", Role: RoleOperator})
	if !store.createApp(AgentApp{ID: "app-one", TenantID: "tenant-two", Name: "App One"}) {
		t.Fatal("create second app")
	}
	deployment := Deployment{ID: "deploy-two", TenantID: "tenant-two", AgentAppID: "app-one", Status: DeploymentDraft}
	if !store.createDeployment(deployment) {
		t.Fatal("create second deployment")
	}
	version := store.createVersion(deployment, map[string]any{"model": "fake"})
	published, _, ok := store.transition(deployment, DeploymentPublished, version.ID)
	if !ok {
		t.Fatal("publish second")
	}
	if _, _, ok := store.transition(published, DeploymentActive, ""); !ok {
		t.Fatal("activate second")
	}

	runner := &blockingRunner{entered: make(chan string, 2), release: make(chan struct{}, 2)}
	runtime := NewRuntime(store, runner, nil)
	done := make(chan error, 2)
	for _, tenantID := range []string{"tenant-one", "tenant-two"} {
		go func(id string) {
			_, err := runtime.Handle(context.Background(), TenantContext{TenantID: id, Role: RoleOperator}, GatewayRequest{AppID: "app-one", SessionID: "shared-name", Input: id})
			done <- err
		}(tenantID)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-runner.entered:
		case <-time.After(time.Second):
			t.Fatal("different Tenant was serialized")
		}
	}
	runner.release <- struct{}{}
	runner.release <- struct{}{}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeCancelledWaiterNeverExecutes(t *testing.T) {
	runner := &blockingRunner{entered: make(chan string, 3), release: make(chan struct{}, 2)}
	runtime := NewRuntime(activeTestPlatform(t), runner, nil)
	tenant := TenantContext{TenantID: "tenant-one", Role: RoleOperator}
	done := make(chan error, 2)
	go func() {
		_, err := runtime.Handle(context.Background(), tenant, GatewayRequest{AppID: "app-one", SessionID: "same", Input: "first"})
		done <- err
	}()
	<-runner.entered
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := runtime.Handle(ctx, tenant, GatewayRequest{AppID: "app-one", SessionID: "same", Input: "second"})
		done <- err
	}()
	cancel()
	if err := <-done; err == nil || err.Error() != "request_cancelled" {
		t.Fatalf("cancelled waiter = %v", err)
	}
	runner.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-runner.entered:
		t.Fatalf("cancelled waiter executed: %s", got)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRuntimeShutdownCancelsActiveWorkAndReportsClosing(t *testing.T) {
	runner := &blockingRunner{entered: make(chan string, 1), release: make(chan struct{})}
	life := lifecycle.New()
	runtime := NewRuntime(activeTestPlatform(t), runner, life)
	tenant := TenantContext{TenantID: "tenant-one", Role: RoleOperator}
	done := make(chan error, 1)
	go func() {
		_, err := runtime.Handle(context.Background(), tenant, GatewayRequest{AppID: "app-one", SessionID: "session", Input: "secret input"})
		done <- err
	}()
	<-runner.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := life.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("active result = %v", err)
	}
	status := runtime.Status()
	if status[0].Lifecycle != "closing" || status[0].Available || status[1].Lifecycle != "closing" {
		t.Fatalf("status = %#v", status)
	}
	if _, err := runtime.Handle(context.Background(), tenant, GatewayRequest{AppID: "app-one", SessionID: "new", Input: "x"}); err == nil || err.Error() != "service_closing" {
		t.Fatalf("new work = %v", err)
	}
}

func TestUnavailableWorkerReturnsStableError(t *testing.T) {
	runtime := NewRuntime(activeTestPlatform(t), EchoRunner{}, nil)
	runtime.SetWorkerAvailable(false)
	_, err := runtime.Handle(context.Background(), TenantContext{TenantID: "tenant-one", Role: RoleOperator}, GatewayRequest{AppID: "app-one", SessionID: "session", Input: "x"})
	if err == nil || err.Error() != "worker_unavailable" {
		t.Fatalf("error = %v", err)
	}
	if got := runtime.Status()[1].Lifecycle; got != "unavailable" {
		t.Fatalf("worker status = %q", got)
	}
}
