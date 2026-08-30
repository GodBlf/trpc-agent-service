package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type flakyTerminalStore struct {
	DataStore
	mu               sync.Mutex
	terminalAttempts int
}

func (s *flakyTerminalStore) AppendSessionEvent(ctx context.Context, event SessionEvent) error {
	if event.IdempotencyKey != "request-one:terminal" {
		return s.DataStore.AppendSessionEvent(ctx, event)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminalAttempts++
	if s.terminalAttempts == 1 {
		return errors.New("transient storage failure")
	}
	return s.DataStore.AppendSessionEvent(ctx, event)
}

func TestChatTerminalEventRetriesTransientStorageFailure(t *testing.T) {
	store := &flakyTerminalStore{DataStore: NewInMemoryStore()}
	client := newChannelTestClient(t, failingRunner{})
	client.handler.ConfigureDataStore(store)
	client.activateApp("app-one", "deploy-one")
	client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)
	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusAccepted, nil)

	if err := waitForChatEvent(client, "session-one", "run.failed"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	attempts := store.terminalAttempts
	store.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("terminal attempts = %d, want 2", attempts)
	}
}

func TestChatRetryAfterFailureReturnsOriginalRunWithoutDuplicateExecution(t *testing.T) {
	runs := make(chan RunnerRequest, 2)
	client := newChannelTestClient(t, requestCapturingRunner{requests: runs})
	client.activateApp("app-one", "deploy-one")
	client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)
	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusAccepted, nil)
	<-runs
	if err := waitForChatEvent(client, "session-one", "run.completed"); err != nil {
		t.Fatal(err)
	}

	var result chatRunResponse
	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusOK, &result)
	if result.Status != "completed" || result.RequestID != "request-one" {
		t.Fatalf("retry result = %#v", result)
	}
	events := chatEventsForTest(t, client, "session-one")
	inputs := 0
	for _, event := range events {
		if event.Type == "message.input" {
			inputs++
		}
	}
	if inputs != 1 {
		t.Fatalf("input events = %d, want 1", inputs)
	}
}

func TestConcurrentChatRetriesShareOneLogicalRun(t *testing.T) {
	runner := &chatBlockingRunner{started: make(chan struct{}, 1), once: make(chan struct{})}
	client := newChannelTestClient(t, runner)
	client.activateApp("app-one", "deploy-one")
	client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)

	results := make(chan chatRunResponse, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var result chatRunResponse
			response := client.do(http.MethodPost, "/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"})
			defer response.Body.Close()
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				client.T.Fatal(err)
			}
			results <- result
		}()
	}
	<-runner.started
	client.post("/api/v1/chat/sessions/session-one/cancel", `{"request_id":"request-one"}`, nil, http.StatusOK, nil)
	for i := 0; i < 2; i++ {
		if result := <-results; result.RequestID != "request-one" {
			client.T.Fatalf("result = %#v", result)
		}
	}
	if err := waitForChatEvent(client, "session-one", "run.cancelled"); err != nil {
		t.Fatal(err)
	}
	events := chatEventsForTest(t, client, "session-one")
	inputs, terminals := 0, 0
	for _, event := range events {
		if event.Type == "message.input" {
			inputs++
		}
		if event.Type == "run.cancelled" {
			terminals++
		}
	}
	if inputs != 1 || terminals != 1 {
		t.Fatalf("inputs = %d, terminals = %d", inputs, terminals)
	}
}

func TestConcurrentChatReplayWithDifferentInputIsRejected(t *testing.T) {
	runner := &chatBlockingRunner{started: make(chan struct{}, 1), once: make(chan struct{})}
	client := newChannelTestClient(t, runner)
	client.activateApp("app-one", "deploy-one")
	client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)

	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello"}`, map[string]string{"X-Request-ID": "request-one"}, http.StatusAccepted, nil)
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}

	response := client.do(http.MethodPost, "/api/v1/chat/sessions/session-one/messages", `{"input":"different"}`, map[string]string{"X-Request-ID": "request-one"})
	assertChannelAPIError(t, response, http.StatusConflict, "idempotency_key_reused")

	client.post("/api/v1/chat/sessions/session-one/cancel", `{"request_id":"request-one"}`, nil, http.StatusOK, nil)
	if err := waitForChatEvent(client, "session-one", "run.cancelled"); err != nil {
		t.Fatal(err)
	}
}
