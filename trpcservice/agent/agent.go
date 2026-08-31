package agent

import (
	"context"
	"time"

	frameworkagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type DeterministicAgent struct {
	name string
}

func NewDeterministicAgent(name string) *DeterministicAgent {
	if name == "" {
		name = "deterministic-agent"
	}
	return &DeterministicAgent{name: name}
}

func (a *DeterministicAgent) Run(ctx context.Context, invocation *frameworkagent.Invocation) (<-chan *event.Event, error) {
	results := make(chan *event.Event, 3)
	go func() {
		defer close(results)
		input := invocation.Message.Content
		output := "framework:" + input
		partial := event.NewResponseEvent(invocation.InvocationID, a.name, &model.Response{
			Object:    model.ObjectTypeChatCompletionChunk,
			IsPartial: true,
			Choices:   []model.Choice{{Delta: model.Message{Role: model.RoleAssistant, Content: output}}},
		})
		select {
		case <-ctx.Done():
			return
		case results <- partial:
		}
		final := event.NewResponseEvent(invocation.InvocationID, a.name, &model.Response{
			Object:  model.ObjectTypeChatCompletion,
			Done:    true,
			Choices: []model.Choice{{Message: model.Message{Role: model.RoleAssistant, Content: output}}},
		})
		final.Timestamp = time.Now().UTC()
		select {
		case <-ctx.Done():
		case results <- final:
		}
	}()
	return results, nil
}

func (a *DeterministicAgent) Tools() []tool.Tool { return nil }

func (a *DeterministicAgent) Info() frameworkagent.Info {
	return frameworkagent.Info{Name: a.name, Description: "deterministic framework integration agent"}
}

func (a *DeterministicAgent) SubAgents() []frameworkagent.Agent { return nil }

func (a *DeterministicAgent) FindSubAgent(string) frameworkagent.Agent { return nil }
