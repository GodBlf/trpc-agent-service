package platform

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	serviceagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	frameworkagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/plugin"
	"trpc.group/trpc-go/trpc-agent-go/tool"
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

func TestGovernancePluginAuthorizesAtActualToolCallback(t *testing.T) {
	center := NewGovernanceCenter()
	_, _ = center.PutPolicy(context.Background(), TenantPolicy{TenantID: "tenant-one", AgentAppID: "app-one", AllowedTools: []string{"search"}})
	manager := plugin.MustNewManager(&governanceRuntimePlugin{center: center})
	callbacks := manager.ToolCallbacks()
	request := RunnerRequest{TenantID: "tenant-one", AppID: "app-one", UserID: "user-one", SessionID: "session-one", RequestID: "request-one", TraceID: "trace-one"}
	ctx := withRunnerIdentity(context.Background(), request)
	if _, err := callbacks.RunBeforeTool(ctx, &tool.BeforeToolArgs{ToolName: "search", Arguments: []byte(`{"query":"safe"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := callbacks.RunBeforeTool(ctx, &tool.BeforeToolArgs{ToolName: "delete", Arguments: []byte(`{}`)}); err == nil {
		t.Fatal("disallowed Tool reached invocation")
	}
}

func TestGovernancePluginConsumesDangerousConfirmationAndRecordsToolCompletion(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	center := NewGovernanceCenter()
	center.now = func() time.Time { return now }
	_, _ = center.PutPolicy(context.Background(), TenantPolicy{TenantID: "tenant-one", AgentAppID: "app-one", AllowedTools: []string{"deploy"}, DangerousTools: []string{"deploy"}})
	request := RunnerRequest{TenantID: "tenant-one", AppID: "app-one", UserID: "user-one", SessionID: "session-one", RequestID: "request-one", TraceID: "trace-one"}
	_, _ = center.Evaluate(context.Background(), GovernanceRequest{TenantID: request.TenantID, AgentAppID: request.AppID, UserID: request.UserID, SessionID: request.SessionID, RequestID: request.RequestID, RequiredTools: []string{"deploy"}})
	callbacks := plugin.MustNewManager(&governanceRuntimePlugin{center: center}).ToolCallbacks()
	ctx := withRunnerIdentity(context.Background(), request)
	args := &tool.BeforeToolArgs{ToolName: "deploy", Arguments: []byte(`{"target":"production"}`)}
	if _, err := callbacks.RunBeforeTool(ctx, args); !IsGovernanceError(err, "confirmation_required") {
		t.Fatalf("initial Tool callback error = %v", err)
	}
	pending := center.Confirmations("tenant-one")[0]
	if _, err := center.DecideConfirmation(context.Background(), "tenant-one", pending.ID, "operator", true); err != nil {
		t.Fatal(err)
	}
	if _, err := callbacks.RunBeforeTool(ctx, args); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Millisecond)
	if _, err := callbacks.RunAfterTool(ctx, &tool.AfterToolArgs{ToolName: "deploy", Arguments: args.Arguments, Result: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := callbacks.RunBeforeTool(ctx, args); !IsGovernanceError(err, "confirmation_consumed") {
		t.Fatalf("repeated Tool callback error = %v", err)
	}
	if metrics := center.Metrics("tenant-one"); metrics.ToolLatencyMS != 25 {
		t.Fatalf("Tool execution latency = %dms, want 25ms", metrics.ToolLatencyMS)
	}
}

func TestDefaultAgentFactoryInvokesConfiguredDeterministicToolAfterConfirmation(t *testing.T) {
	platform := activeTestPlatform(t)
	version, ok := platform.DeploymentVersion("deploy-one-v1")
	if !ok {
		t.Fatal("active deployment version not found")
	}
	version.Config = map[string]any{"runner": "framework", "tools": []any{"deploy"}, "deterministic_tool_call": "deploy"}
	center := NewGovernanceCenter()
	_, _ = center.PutPolicy(context.Background(), TenantPolicy{
		TenantID: "tenant-one", AgentAppID: "app-one",
		AllowedTools: []string{"deploy"}, DangerousTools: []string{"deploy"},
	})
	adapter := NewFrameworkRunnerAdapter(func(id string) (DeploymentVersion, bool) {
		if id != version.ID {
			return DeploymentVersion{}, false
		}
		return version, true
	}, nil)
	adapter.SetGovernance(center)
	request := RunnerRequest{
		TenantID: "tenant-one", AppID: "app-one", DeploymentID: "deploy-one", VersionID: version.ID,
		SessionID: "session-one", UserID: "user-one", RequestID: "request-one", TraceID: "trace-one", Input: "ship",
	}
	first := collectRuntimeEvents(t, adapter, request)
	if len(first) != 1 || first[0].Type != "run.failed" || !strings.Contains(first[0].Data["error"], "confirmation_required") {
		t.Fatalf("first Tool run events = %#v", first)
	}
	confirmations := center.Confirmations("tenant-one")
	if len(confirmations) != 1 || confirmations[0].ToolName != "deploy" {
		t.Fatalf("pending confirmations = %#v", confirmations)
	}
	if _, err := center.DecideConfirmation(context.Background(), "tenant-one", confirmations[0].ID, "operator", true); err != nil {
		t.Fatal(err)
	}
	second := collectRuntimeEvents(t, adapter, request)
	if len(second) < 2 || second[len(second)-1].Type != "run.completed" {
		t.Fatalf("approved Tool run events = %#v", second)
	}
	confirmations = center.Confirmations("tenant-one")
	if len(confirmations) != 1 || confirmations[0].Status != ConfirmationCompleted {
		t.Fatalf("completed confirmations = %#v", confirmations)
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

func TestFrameworkRunnerAdapterPreservesIdentity(t *testing.T) {
	platform := activeTestPlatform(t)
	seen := make(chan RunnerRequest, 1)
	adapter := NewFrameworkRunnerAdapter(platform.DeploymentVersion, func(_ context.Context, _ DeploymentVersion) (frameworkagent.Agent, error) {
		return identityCapturingAgent{seen: seen}, nil
	})
	request := RunnerRequest{TenantID: "tenant-one", AppID: "app-one", DeploymentID: "deploy-one", SessionID: "session-one", UserID: "user-one", Input: "hello", RequestID: "request-one", VersionID: "deploy-one-v1"}
	events := collectRuntimeEvents(t, adapter, request)
	got := <-seen
	if got != request {
		t.Fatalf("framework identity = %#v, want %#v", got, request)
	}
	for _, runtimeEvent := range events {
		for key, want := range map[string]string{
			"tenant_id": request.TenantID, "app_id": request.AppID, "deployment_id": request.DeploymentID,
			"version_id": request.VersionID, "session_id": request.SessionID, "request_id": request.RequestID,
		} {
			if runtimeEvent.Data[key] != want {
				t.Fatalf("event identity %q = %q, want %q", key, runtimeEvent.Data[key], want)
			}
		}
	}
}

func TestFrameworkRunnerAdapterIsolatesTenantVersions(t *testing.T) {
	versions := map[string]DeploymentVersion{
		"tenant-one-v1": {ID: "tenant-one-v1", TenantID: "tenant-one", AgentAppID: "app-one", DeploymentID: "deploy-one", Active: true},
		"tenant-two-v1": {ID: "tenant-two-v1", TenantID: "tenant-two", AgentAppID: "app-two", DeploymentID: "deploy-two", Active: true},
	}
	var factoryCalls atomic.Int64
	adapter := NewFrameworkRunnerAdapter(func(id string) (DeploymentVersion, bool) {
		version, ok := versions[id]
		return version, ok
	}, func(_ context.Context, version DeploymentVersion) (frameworkagent.Agent, error) {
		factoryCalls.Add(1)
		return serviceagent.NewDeterministicAgent(version.AgentAppID), nil
	})
	for _, request := range []RunnerRequest{
		{TenantID: "tenant-one", AppID: "app-one", DeploymentID: "deploy-one", SessionID: "session-one", UserID: "user-one", Input: "one", RequestID: "request-one", VersionID: "tenant-one-v1"},
		{TenantID: "tenant-two", AppID: "app-two", DeploymentID: "deploy-two", SessionID: "session-two", UserID: "user-two", Input: "two", RequestID: "request-two", VersionID: "tenant-two-v1"},
	} {
		_ = collectRuntimeEvents(t, adapter, request)
	}
	if factoryCalls.Load() != 2 {
		t.Fatalf("factory calls = %d, want 2", factoryCalls.Load())
	}
	if _, err := adapter.RunEvents(context.Background(), RunnerRequest{TenantID: "tenant-two", AppID: "app-two", DeploymentID: "deploy-two", SessionID: "session-one", UserID: "user-two", Input: "guess", RequestID: "request-three", VersionID: "tenant-one-v1"}); err == nil || err.Error() != "deployment_version_scope_mismatch" {
		t.Fatalf("cross-tenant version error = %v", err)
	}
}

func TestFrameworkRunnerAdapterEmitsCancellation(t *testing.T) {
	platform := activeTestPlatform(t)
	adapter := NewFrameworkRunnerAdapter(platform.DeploymentVersion, func(_ context.Context, _ DeploymentVersion) (frameworkagent.Agent, error) {
		return blockingAgent{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	events, err := adapter.RunEvents(ctx, RunnerRequest{TenantID: "tenant-one", AppID: "app-one", DeploymentID: "deploy-one", SessionID: "session-one", UserID: "user-one", Input: "hello", RequestID: "request-one", VersionID: "deploy-one-v1"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	runtimeEvent, ok := <-events
	if !ok {
		t.Fatal("event channel closed without cancellation")
	}
	if runtimeEvent.Type != "run.cancelled" {
		t.Fatalf("event type = %q, want run.cancelled", runtimeEvent.Type)
	}
}

func TestFrameworkRunnerAdapterCloseCancelsActiveRun(t *testing.T) {
	platform := activeTestPlatform(t)
	adapter := NewFrameworkRunnerAdapter(platform.DeploymentVersion, func(_ context.Context, _ DeploymentVersion) (frameworkagent.Agent, error) {
		return blockingAgent{}, nil
	})
	events, err := adapter.RunEvents(context.Background(), RunnerRequest{TenantID: "tenant-one", AppID: "app-one", DeploymentID: "deploy-one", SessionID: "session-one", UserID: "user-one", Input: "hello", RequestID: "request-one", VersionID: "deploy-one-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	runtimeEvent, ok := <-events
	if !ok || runtimeEvent.Type != "run.cancelled" {
		t.Fatalf("close event = %#v, want run.cancelled", runtimeEvent)
	}
}

func TestFrameworkRunnerAdapterRetiresInactiveVersion(t *testing.T) {
	platform := activeTestPlatform(t)
	adapter := NewFrameworkRunnerAdapter(platform.DeploymentVersion, nil)
	request := RunnerRequest{TenantID: "tenant-one", AppID: "app-one", DeploymentID: "deploy-one", SessionID: "session-one", UserID: "user-one", Input: "hello", RequestID: "request-one", VersionID: "deploy-one-v1"}
	_ = collectRuntimeEvents(t, adapter, request)
	deployment, ok := platform.deployment("tenant-one", "deploy-one")
	if !ok {
		t.Fatal("deployment not found")
	}
	if _, _, ok := platform.transition(deployment, DeploymentPaused, ""); !ok {
		t.Fatal("pause deployment")
	}
	if _, err := adapter.RunEvents(context.Background(), request); err == nil || err.Error() != "deployment_version_inactive" {
		t.Fatalf("inactive version error = %v", err)
	}
	adapter.RetireVersion(request.VersionID)
	if _, exists := adapter.runners[request.VersionID]; exists {
		t.Fatal("retired runner remains cached")
	}
}

func TestFrameworkChatSSEPersistsIdentityAndSupportsReplay(t *testing.T) {
	client := newChannelTestClient(t, EchoRunner{})
	client.activateApp("app-one", "deploy-one")
	client.handler.ConfigureRuntime(NewFrameworkRunnerAdapter(client.handler.platform.DeploymentVersion, nil), nil)
	client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)
	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusAccepted, nil)
	if err := waitForChatEvent(client, "session-one", "run.completed"); err != nil {
		t.Fatal(err)
	}
	envelopes := readSSE(t, client, "/api/v1/chat/sessions/session-one/stream?request_id=request-one", nil)
	if len(envelopes) < 4 || envelopes[len(envelopes)-1].Type != "run.completed" {
		t.Fatalf("framework SSE envelopes = %#v", envelopes)
	}
	for _, envelope := range envelopes {
		if envelope.RequestID != "request-one" || envelope.SessionID != "session-one" {
			t.Fatalf("framework SSE identity = %#v", envelope)
		}
	}
	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusOK, nil)
}

func TestFrameworkChatFailureAndCancellation(t *testing.T) {
	failureClient := newChannelTestClient(t, nil)
	failureClient.activateApp("app-one", "deploy-one")
	failureClient.handler.ConfigureRuntime(NewFrameworkRunnerAdapter(failureClient.handler.platform.DeploymentVersion, func(_ context.Context, _ DeploymentVersion) (frameworkagent.Agent, error) {
		return failingFrameworkAgent{}, nil
	}), nil)
	failureClient.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)
	failureClient.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusAccepted, nil)
	if err := waitForChatEvent(failureClient, "session-one", "run.failed"); err != nil {
		t.Fatal(err)
	}

	cancelClient := newChannelTestClient(t, nil)
	cancelClient.activateApp("app-one", "deploy-one")
	cancelClient.handler.ConfigureRuntime(NewFrameworkRunnerAdapter(cancelClient.handler.platform.DeploymentVersion, func(_ context.Context, _ DeploymentVersion) (frameworkagent.Agent, error) {
		return blockingAgent{}, nil
	}), nil)
	cancelClient.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)
	cancelClient.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusAccepted, nil)
	cancelClient.post("/api/v1/chat/sessions/session-one/cancel", `{"request_id":"request-one"}`, nil, http.StatusOK, nil)
	if err := waitForChatEvent(cancelClient, "session-one", "run.cancelled"); err != nil {
		t.Fatal(err)
	}
}

func TestFrameworkChatPersistsWithStage2Stores(t *testing.T) {
	tests := []struct {
		name  string
		store func(*testing.T) DataStore
	}{
		{name: "redis", store: func(t *testing.T) DataStore {
			server := miniredis.RunT(t)
			return NewRedisStore(server.Addr())
		}},
		{name: "sqlite", store: func(t *testing.T) DataStore {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "framework.db"))
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			if closer, ok := store.(interface{ Close() error }); ok {
				defer closer.Close()
			}
			client := newChannelTestClient(t, nil)
			client.handler.ConfigureDataStore(store)
			client.activateApp("app-one", "deploy-one")
			client.handler.ConfigureRuntime(NewFrameworkRunnerAdapter(client.handler.platform.DeploymentVersion, nil), nil)
			client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)
			client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusAccepted, nil)
			if err := waitForChatEvent(client, "session-one", "run.completed"); err != nil {
				t.Fatal(err)
			}
			events, err := store.ListSessionEvents(context.Background(), "tenant-one", "session-one", 0)
			if err != nil || len(events) < 4 {
				t.Fatalf("persisted events = %#v, err = %v", events, err)
			}
		})
	}
}

type identityCapturingAgent struct {
	seen chan<- RunnerRequest
}

func (a identityCapturingAgent) Run(ctx context.Context, invocation *frameworkagent.Invocation) (<-chan *event.Event, error) {
	request, _ := RunnerIdentityFromContext(ctx)
	a.seen <- request
	results := make(chan *event.Event, 1)
	results <- event.NewResponseEvent(invocation.InvocationID, "identity-agent", &model.Response{
		Object: model.ObjectTypeChatCompletion,
		Done:   true,
		Choices: []model.Choice{{Message: model.Message{
			Role: model.RoleAssistant, Content: "ok",
		}}},
	})
	close(results)
	return results, nil
}

func (identityCapturingAgent) Tools() []tool.Tool { return nil }
func (identityCapturingAgent) Info() frameworkagent.Info {
	return frameworkagent.Info{Name: "identity-agent"}
}
func (identityCapturingAgent) SubAgents() []frameworkagent.Agent        { return nil }
func (identityCapturingAgent) FindSubAgent(string) frameworkagent.Agent { return nil }

type blockingAgent struct{}

func (blockingAgent) Run(ctx context.Context, _ *frameworkagent.Invocation) (<-chan *event.Event, error) {
	results := make(chan *event.Event)
	go func() {
		<-ctx.Done()
		close(results)
	}()
	return results, nil
}

func (blockingAgent) Tools() []tool.Tool                       { return nil }
func (blockingAgent) Info() frameworkagent.Info                { return frameworkagent.Info{Name: "blocking-agent"} }
func (blockingAgent) SubAgents() []frameworkagent.Agent        { return nil }
func (blockingAgent) FindSubAgent(string) frameworkagent.Agent { return nil }

type failingFrameworkAgent struct{}

func (failingFrameworkAgent) Run(context.Context, *frameworkagent.Invocation) (<-chan *event.Event, error) {
	return nil, errors.New("framework failure")
}

func (failingFrameworkAgent) Tools() []tool.Tool {
	return nil
}

func (failingFrameworkAgent) Info() frameworkagent.Info {
	return frameworkagent.Info{Name: "failing-agent"}
}

func (failingFrameworkAgent) SubAgents() []frameworkagent.Agent {
	return nil
}

func (failingFrameworkAgent) FindSubAgent(string) frameworkagent.Agent {
	return nil
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
