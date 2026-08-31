package platform

import (
	"context"
	"errors"
	"fmt"
	"sync"

	serviceagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	frameworkagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	frameworkrunner "trpc.group/trpc-go/trpc-agent-go/runner"
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

func DefaultAgentFactory() AgentFactory {
	return func(_ context.Context, version DeploymentVersion) (frameworkagent.Agent, error) {
		return serviceagent.NewDeterministicAgent(version.AgentAppID), nil
	}
}

type FrameworkRunnerAdapter struct {
	resolve func(string) (DeploymentVersion, bool)
	factory AgentFactory
	mu      sync.Mutex
	runners map[string]frameworkrunner.Runner
	closed  bool
}

func NewFrameworkRunnerAdapter(resolve func(string) (DeploymentVersion, bool), factory AgentFactory) *FrameworkRunnerAdapter {
	if factory == nil {
		factory = DefaultAgentFactory()
	}
	return &FrameworkRunnerAdapter{resolve: resolve, factory: factory, runners: make(map[string]frameworkrunner.Runner)}
}

func (a *FrameworkRunnerAdapter) runner(ctx context.Context, versionID string) (frameworkrunner.Runner, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("framework_runtime_closed")
	}
	if runner := a.runners[versionID]; runner != nil {
		return runner, nil
	}
	version, ok := a.resolve(versionID)
	if !ok || version.ID != versionID {
		return nil, errors.New("deployment_version_not_found")
	}
	agent, err := a.factory(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("agent_factory: %w", err)
	}
	runner := frameworkrunner.NewRunner(version.AgentAppID, agent)
	a.runners[versionID] = runner
	return runner, nil
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
	upstream, err := runner.Run(ctx, request.UserID, request.SessionID, model.NewUserMessage(request.Input), frameworkagent.WithRequestID(request.RequestID))
	if err != nil {
		return nil, err
	}
	results := make(chan RuntimeEvent, 4)
	go func() {
		defer close(results)
		for upstreamEvent := range upstream {
			if upstreamEvent == nil || upstreamEvent.Response == nil {
				continue
			}
			if upstreamEvent.IsError() {
				a.emit(ctx, results, RuntimeEvent{Type: "run.failed", Data: map[string]string{"error": upstreamEvent.Response.Error.Error()}})
				return
			}
			content := eventContent(upstreamEvent)
			if content == "" {
				continue
			}
			eventType := "message.delta"
			if !upstreamEvent.Response.IsPartial {
				eventType = "message.completed"
			}
			if !a.emit(ctx, results, RuntimeEvent{Type: eventType, Data: map[string]string{"request_id": request.RequestID, "delta": content, "output": content}}) {
				return
			}
		}
		a.emit(ctx, results, RuntimeEvent{Type: "run.completed", Data: map[string]string{"request_id": request.RequestID}})
	}()
	return results, nil
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
