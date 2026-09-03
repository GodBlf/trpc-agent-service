package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	serviceagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	frameworkagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/plugin"
	frameworkrunner "trpc.group/trpc-go/trpc-agent-go/runner"
	frameworktool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type RuntimeEvent struct {
	Type string
	Data map[string]string
}

type StreamingRunnerAdapter interface {
	RunnerAdapter
	RunEvents(context.Context, RunnerRequest) (<-chan RuntimeEvent, error)
	Close() error
}

type AgentFactory func(context.Context, DeploymentVersion) (frameworkagent.Agent, error)

type governanceReplayContextKey struct{}

func DefaultAgentFactory() AgentFactory {
	return func(_ context.Context, version DeploymentVersion) (frameworkagent.Agent, error) {
		if toolName, _ := version.Config["deterministic_tool_call"].(string); toolName != "" {
			declared := false
			for _, candidate := range configStrings(version.Config, "tools") {
				if candidate == toolName {
					declared = true
					break
				}
			}
			if !declared {
				return nil, fmt.Errorf("deterministic Tool %q is not declared", toolName)
			}
			return serviceagent.NewDeterministicToolAgent(version.AgentAppID, toolName), nil
		}
		return serviceagent.NewDeterministicAgent(version.AgentAppID), nil
	}
}

type FrameworkRunnerAdapter struct {
	resolve    func(string) (DeploymentVersion, bool)
	factory    AgentFactory
	mu         sync.Mutex
	runners    map[string]frameworkrunner.Runner
	runs       map[string]map[uint64]context.CancelFunc
	nextRun    uint64
	closed     bool
	governance *GovernanceCenter
}

func NewFrameworkRunnerAdapter(resolve func(string) (DeploymentVersion, bool), factory AgentFactory) *FrameworkRunnerAdapter {
	if factory == nil {
		factory = DefaultAgentFactory()
	}
	return &FrameworkRunnerAdapter{resolve: resolve, factory: factory, runners: make(map[string]frameworkrunner.Runner), runs: make(map[string]map[uint64]context.CancelFunc)}
}

func (a *FrameworkRunnerAdapter) runner(ctx context.Context, versionID string) (frameworkrunner.Runner, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("framework_runtime_closed")
	}
	version, ok := a.resolve(versionID)
	if !ok || version.ID != versionID {
		return nil, errors.New("deployment_version_not_found")
	}
	if !version.Active {
		return nil, errors.New("deployment_version_inactive")
	}
	if runner := a.runners[versionID]; runner != nil {
		return runner, nil
	}
	agent, err := a.factory(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("agent_factory: %w", err)
	}
	options := []frameworkrunner.Option{}
	if a.governance != nil {
		options = append(options, frameworkrunner.WithPlugins(&governanceRuntimePlugin{center: a.governance}))
	}
	runner := frameworkrunner.NewRunner(version.AgentAppID, agent, options...)
	a.runners[versionID] = runner
	return runner, nil
}

func (a *FrameworkRunnerAdapter) SetGovernance(center *GovernanceCenter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.governance = center
}

type governanceRuntimePlugin struct{ center *GovernanceCenter }

func (p *governanceRuntimePlugin) Name() string { return "platform-governance" }

func (p *governanceRuntimePlugin) Register(registry *plugin.Registry) {
	registry.BeforeAgent(func(ctx context.Context, _ *frameworkagent.BeforeAgentArgs) (*frameworkagent.BeforeAgentResult, error) {
		if request, ok := RunnerIdentityFromContext(ctx); ok && p.center != nil {
			if err := p.center.RecordSpan(GovernanceRequest{TenantID: request.TenantID, AgentAppID: request.AppID, UserID: request.UserID, SessionID: request.SessionID, RequestID: request.RequestID}, request.TraceID, "plugin.before_agent", "ok"); err != nil {
				return nil, &GovernanceError{Code: "audit_unavailable", TraceID: request.TraceID}
			}
		}
		return nil, nil
	})
	registry.AfterAgent(func(ctx context.Context, _ *frameworkagent.AfterAgentArgs) (*frameworkagent.AfterAgentResult, error) {
		if request, ok := RunnerIdentityFromContext(ctx); ok && p.center != nil {
			if err := p.center.RecordSpan(GovernanceRequest{TenantID: request.TenantID, AgentAppID: request.AppID, UserID: request.UserID, SessionID: request.SessionID, RequestID: request.RequestID}, request.TraceID, "plugin.after_agent", "ok"); err != nil {
				return nil, &GovernanceError{Code: "audit_unavailable", TraceID: request.TraceID}
			}
		}
		return nil, nil
	})
	registry.BeforeTool(func(ctx context.Context, args *frameworktool.BeforeToolArgs) (*frameworktool.BeforeToolResult, error) {
		request, ok := RunnerIdentityFromContext(ctx)
		if !ok || p.center == nil {
			return nil, nil
		}
		err := p.center.AuthorizeTool(ctx, GovernanceRequest{
			TenantID: request.TenantID, AgentAppID: request.AppID, UserID: request.UserID,
			SessionID: request.SessionID, RequestID: request.RequestID,
		}, request.TraceID, args.ToolName, args.Arguments)
		if _, replayEnabled := ctx.Value(governanceReplayContextKey{}).(bool); replayEnabled && IsGovernanceError(err, "confirmation_consumed") {
			if replay, ok := p.center.ToolReplayResult(request.TenantID, request.RequestID, args.ToolName); ok {
				return &frameworktool.BeforeToolResult{CustomResult: replay}, nil
			}
		}
		return nil, err
	})
	registry.AfterTool(func(ctx context.Context, args *frameworktool.AfterToolArgs) (*frameworktool.AfterToolResult, error) {
		request, ok := RunnerIdentityFromContext(ctx)
		if !ok || p.center == nil {
			return nil, nil
		}
		err := p.center.CompleteTool(ctx, GovernanceRequest{
			TenantID: request.TenantID, AgentAppID: request.AppID, UserID: request.UserID,
			SessionID: request.SessionID, RequestID: request.RequestID,
		}, request.TraceID, args.ToolName, args.Error)
		return nil, err
	})
}

func (a *FrameworkRunnerAdapter) Run(ctx context.Context, request RunnerRequest) (RunnerResponse, error) {
	events, err := a.RunEvents(ctx, request)
	if err != nil {
		return RunnerResponse{}, err
	}
	var output string
	for runtimeEvent := range events {
		if runtimeEvent.Type == "message.delta" {
			output += runtimeEvent.Data["delta"]
		}
		if runtimeEvent.Type == "run.failed" {
			return RunnerResponse{}, errors.New(runtimeEvent.Data["error"])
		}
		if runtimeEvent.Type == "run.cancelled" {
			return RunnerResponse{}, context.Canceled
		}
	}
	return RunnerResponse{Output: output}, nil
}

func (a *FrameworkRunnerAdapter) RunEvents(ctx context.Context, request RunnerRequest) (<-chan RuntimeEvent, error) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil, errors.New("framework_runtime_closed")
	}
	version, ok := a.resolve(request.VersionID)
	if !ok || version.TenantID != request.TenantID || version.AgentAppID != request.AppID || version.DeploymentID != request.DeploymentID {
		return nil, errors.New("deployment_version_scope_mismatch")
	}
	runner, err := a.runner(ctx, request.VersionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.WithValue(withRunnerIdentity(ctx, request), governanceReplayContextKey{}, true))
	runID, registered := a.registerRun(request.VersionID, cancel)
	if !registered {
		cancel()
		return nil, errors.New("framework_runtime_closed")
	}
	upstream, err := runner.Run(runCtx, request.UserID, request.SessionID, model.NewUserMessage(request.Input), frameworkagent.WithRequestID(request.RequestID))
	if err != nil {
		a.unregisterRun(request.VersionID, runID)
		cancel()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			results := make(chan RuntimeEvent, 1)
			results <- RuntimeEvent{Type: "run.cancelled", Data: runtimeEventData(request, map[string]string{"error": "run cancelled"})}
			close(results)
			return results, nil
		}
		return nil, err
	}
	results := make(chan RuntimeEvent, 4)
	go func() {
		defer close(results)
		defer a.unregisterRun(request.VersionID, runID)
		defer cancel()
		for {
			select {
			case <-runCtx.Done():
				results <- RuntimeEvent{Type: "run.cancelled", Data: runtimeEventData(request, map[string]string{"error": "run cancelled"})}
				return
			case upstreamEvent, ok := <-upstream:
				if !ok {
					results <- RuntimeEvent{Type: "run.completed", Data: runtimeEventData(request, nil)}
					return
				}
				if upstreamEvent == nil || upstreamEvent.Response == nil {
					continue
				}
				if upstreamEvent.IsError() {
					a.emit(context.Background(), results, RuntimeEvent{Type: "run.failed", Data: runtimeEventData(request, map[string]string{"error": upstreamEvent.Response.Error.Error()})})
					return
				}
				content := eventContent(upstreamEvent)
				if content == "" {
					continue
				}
				// The upstream function-call processor surfaces BeforeTool plugin
				// failures as a model-visible completed message. Stop here so a
				// pending confirmation cannot be mistaken for a successful run.
				if !upstreamEvent.Response.IsPartial && strings.HasPrefix(content, "tool callback error:") {
					if !a.emit(runCtx, results, RuntimeEvent{Type: "run.failed", Data: runtimeEventData(request, map[string]string{"error": content})}) {
						return
					}
					// The framework may still be unwinding its flow after the
					// callback error. Cancel and drain its event channel before
					// releasing the request ID so an approved retry can start.
					cancel()
					for range upstream {
					}
					return
				}
				eventType := "message.delta"
				if !upstreamEvent.Response.IsPartial {
					eventType = "message.completed"
				}
				if !a.emit(runCtx, results, RuntimeEvent{Type: eventType, Data: runtimeEventData(request, map[string]string{"delta": content, "output": content})}) {
					return
				}
			}
		}
	}()
	return results, nil
}

type runnerIdentityKey struct{}

func withRunnerIdentity(ctx context.Context, request RunnerRequest) context.Context {
	return context.WithValue(ctx, runnerIdentityKey{}, request)
}

func RunnerIdentityFromContext(ctx context.Context) (RunnerRequest, bool) {
	request, ok := ctx.Value(runnerIdentityKey{}).(RunnerRequest)
	return request, ok
}

func runtimeEventData(request RunnerRequest, values map[string]string) map[string]string {
	data := map[string]string{
		"tenant_id": request.TenantID, "app_id": request.AppID, "deployment_id": request.DeploymentID,
		"version_id": request.VersionID, "session_id": request.SessionID, "request_id": request.RequestID, "trace_id": request.TraceID,
	}
	for key, value := range values {
		data[key] = value
	}
	return data
}

func (a *FrameworkRunnerAdapter) registerRun(versionID string, cancel context.CancelFunc) (uint64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, false
	}
	a.nextRun++
	if a.runs[versionID] == nil {
		a.runs[versionID] = make(map[uint64]context.CancelFunc)
	}
	a.runs[versionID][a.nextRun] = cancel
	return a.nextRun, true
}

func (a *FrameworkRunnerAdapter) unregisterRun(versionID string, runID uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if runs := a.runs[versionID]; runs != nil {
		delete(runs, runID)
		if len(runs) == 0 {
			delete(a.runs, versionID)
		}
	}
}

func (a *FrameworkRunnerAdapter) RetireVersion(versionID string) error {
	a.mu.Lock()
	for _, cancel := range a.runs[versionID] {
		cancel()
	}
	runner := a.runners[versionID]
	delete(a.runners, versionID)
	a.mu.Unlock()
	if runner == nil {
		return nil
	}
	return runner.Close()
}

func (a *FrameworkRunnerAdapter) emit(ctx context.Context, results chan<- RuntimeEvent, value RuntimeEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case results <- value:
		return true
	}
}

func (a *FrameworkRunnerAdapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	for _, runs := range a.runs {
		for _, cancel := range runs {
			cancel()
		}
	}
	runners := make([]frameworkrunner.Runner, 0, len(a.runners))
	for _, runner := range a.runners {
		runners = append(runners, runner)
	}
	a.mu.Unlock()
	var firstErr error
	for _, runner := range runners {
		if err := runner.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func eventContent(upstreamEvent *event.Event) string {
	if upstreamEvent.Response == nil || len(upstreamEvent.Response.Choices) == 0 {
		return ""
	}
	choice := upstreamEvent.Response.Choices[0]
	if upstreamEvent.Response.IsPartial {
		return choice.Delta.Content
	}
	return choice.Message.Content
}
