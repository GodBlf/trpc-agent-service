package platform

import (
	"context"
	"sync/atomic"
	"testing"

	serviceagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	frameworkagent "trpc.group/trpc-go/trpc-agent-go/agent"
)

func TestFrameworkRunnerAdapterStreamsAndReusesRunner(t *testing.T) {
	platform := activeTestPlatform(t)
	var factoryCalls atomic.Int64
	adapter := NewFrameworkRunnerAdapter(platform.DeploymentVersion, func(_ context.Context, version DeploymentVersion) (frameworkagent.Agent, error) {
		factoryCalls.Add(1)
		return serviceagent.NewDeterministicAgent(version.AgentAppID), nil
	})

	request := RunnerRequest{TenantID: "tenant-one", AppID: "app-one", DeploymentID: "deploy-one", SessionID: "session-one", UserID: "user-one", Input: "hello", RequestID: "request-one", VersionID: "deploy-one-v1"}
	first := collectRuntimeEvents(t, adapter, request)
	second := collectRuntimeEvents(t, adapter, request)
	if factoryCalls.Load() != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls.Load())
	}
	if len(first) != 3 || first[0].Type != "message.delta" || first[1].Type != "message.completed" || first[2].Type != "run.completed" {
		t.Fatalf("first events = %#v", first)
	}
	if first[0].Data["delta"] != "framework:hello" || first[1].Data["output"] != "framework:hello" {
		t.Fatalf("first event data = %#v", first)
	}
	if len(second) != 3 {
		t.Fatalf("second events = %#v", second)
	}
}

func TestFrameworkRunnerAdapterRejectsUnknownVersionAndClose(t *testing.T) {
	adapter := NewFrameworkRunnerAdapter(func(string) (DeploymentVersion, bool) { return DeploymentVersion{}, false }, nil)
	if _, err := adapter.RunEvents(context.Background(), RunnerRequest{VersionID: "missing"}); err == nil || err.Error() != "deployment_version_scope_mismatch" {
		t.Fatalf("unknown version error = %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.RunEvents(context.Background(), RunnerRequest{VersionID: "missing"}); err == nil || err.Error() != "framework_runtime_closed" {
		t.Fatalf("closed adapter error = %v", err)
	}
}

func TestRuntimeStreamPropagatesTenantUserAndCompletion(t *testing.T) {
	platform := activeTestPlatform(t)
	runtime := NewRuntime(platform, NewFrameworkRunnerAdapter(platform.DeploymentVersion, nil), nil)
	events, err := runtime.Stream(context.Background(), TenantContext{TenantID: "tenant-one", UserID: "user-one", Role: RoleOperator}, GatewayRequest{
		AppID: "app-one", SessionID: "session-one", Input: "hello", RequestID: "request-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for event := range events {
		types = append(types, event.Type)
	}
	if len(types) != 3 || types[0] != "message.delta" || types[1] != "message.completed" || types[2] != "run.completed" {
		t.Fatalf("runtime event types = %#v", types)
	}
}

func collectRuntimeEvents(t *testing.T, adapter *FrameworkRunnerAdapter, request RunnerRequest) []RuntimeEvent {
	t.Helper()
	events, err := adapter.RunEvents(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var collected []RuntimeEvent
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}
