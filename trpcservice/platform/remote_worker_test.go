package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/lifecycle"
)

func TestRemoteWorkerPreservesResolvedIdentityTraceAndVersion(t *testing.T) {
	requests := make(chan RunnerRequest, 1)
	versions := make(chan DeploymentVersion, 1)
	server := httptest.NewServer(NewWorkerServer(WorkerServerConfig{
		Token: "worker-secret",
		BeforeRun: func(request RunnerRequest, version DeploymentVersion) {
			requests <- request
			versions <- version
		},
	}))
	defer server.Close()

	runtime := NewRuntime(activeTestPlatform(t), NewRemoteRunnerAdapter(server.URL, "worker-secret"), lifecycle.New())
	events, err := runtime.Stream(context.Background(), TenantContext{TenantID: "tenant-one", UserID: "user-one", Role: RoleOperator}, GatewayRequest{
		AppID: "app-one", SessionID: "session-one", Input: "hello", RequestID: "request-one", TraceID: "trace-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "run.failed" {
			t.Fatalf("worker event failed: %#v", event)
		}
	}

	select {
	case request := <-requests:
		if request.TenantID != "tenant-one" || request.AppID != "app-one" || request.SessionID != "session-one" ||
			request.RequestID != "request-one" || request.TraceID != "trace-one" || request.VersionID != "deploy-one-v1" {
			t.Fatalf("worker request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("worker request was not captured")
	}
	select {
	case version := <-versions:
		if version.ID != "deploy-one-v1" || version.Config["model"] != "fake" {
			t.Fatalf("worker version = %#v", version)
		}
	case <-time.After(time.Second):
		t.Fatal("worker version was not captured")
	}
}

func TestRemoteWorkerUnavailableAndRestartRecoveryUseStableErrors(t *testing.T) {
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			writeError(w, http.StatusServiceUnavailable, "worker_unavailable", "worker unavailable")
			return
		}
		NewWorkerServer(WorkerServerConfig{Token: "worker-secret"}).ServeHTTP(w, r)
	}))
	defer server.Close()

	runtime := NewRuntime(activeTestPlatform(t), NewRemoteRunnerAdapter(server.URL, "worker-secret"), lifecycle.New())
	tenant := TenantContext{TenantID: "tenant-one", UserID: "user-one", Role: RoleOperator}
	unavailable.Store(true)
	_, err := runtime.Stream(context.Background(), tenant, GatewayRequest{AppID: "app-one", SessionID: "session-one", Input: "hello"})
	var runtimeErr *runtimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.code != "worker_unavailable" {
		t.Fatalf("unavailable error = %v", err)
	}

	unavailable.Store(false)
	events, err := runtime.Stream(context.Background(), tenant, GatewayRequest{AppID: "app-one", SessionID: "session-one", Input: "hello", RequestID: "request-two", TraceID: "trace-two"})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "run.failed" {
			t.Fatalf("restarted worker failed: %#v", event)
		}
	}
}
