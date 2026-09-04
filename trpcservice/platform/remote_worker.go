package platform

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type RemoteRunnerAdapter struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewRemoteRunnerAdapter(endpoint, token string) *RemoteRunnerAdapter {
	return &RemoteRunnerAdapter{endpoint: strings.TrimRight(endpoint, "/"), token: token, client: &http.Client{}}
}

func (a *RemoteRunnerAdapter) Run(ctx context.Context, request RunnerRequest) (RunnerResponse, error) {
	events, err := a.RunEvents(ctx, request)
	if err != nil {
		return RunnerResponse{}, err
	}
	response := RunnerResponse{}
	for event := range events {
		if event.Type == "message.completed" || event.Type == "run.completed" {
			if output := event.Data["output"]; output != "" {
				response.Output = output
			}
			if event.Data["usage_known"] == "true" {
				response.UsageKnown = true
				fmt.Sscanf(event.Data["usage_tokens"], "%d", &response.UsageTokens)
			}
		}
	}
	if response.Output == "" {
		return RunnerResponse{}, &runtimeError{code: "worker_unavailable"}
	}
	return response, nil
}

func (a *RemoteRunnerAdapter) RunEvents(ctx context.Context, request RunnerRequest) (<-chan RuntimeEvent, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, &runtimeError{code: "worker_unavailable", err: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/internal/worker/run", bytes.NewReader(payload))
	if err != nil {
		return nil, &runtimeError{code: "worker_unavailable", err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(httpRequest)
	if err != nil {
		return nil, &runtimeError{code: "worker_unavailable", err: err}
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		var apiErr errorResponse
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&apiErr)
		code := "worker_unavailable"
		if apiErr.Error.Code != "" {
			code = apiErr.Error.Code
		}
		return nil, &runtimeError{code: code}
	}
	if response.Header.Get("Content-Type") != "text/event-stream" {
		response.Body.Close()
		return nil, &runtimeError{code: "worker_unavailable"}
	}
	events := make(chan RuntimeEvent, 4)
	go func() {
		defer close(events)
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event RuntimeEvent
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case events <- event:
			}
		}
	}()
	return events, nil
}

func (a *RemoteRunnerAdapter) Close() error { return nil }

type WorkerServerConfig struct {
	Token     string
	Factory   AgentFactory
	BeforeRun func(RunnerRequest, DeploymentVersion)
}

type WorkerServer struct {
	config   WorkerServerConfig
	versions *workerVersionStore
	runner   *FrameworkRunnerAdapter
}

func NewWorkerServer(config WorkerServerConfig) *WorkerServer {
	if config.Token == "" {
		config.Token = "development-worker"
	}
	store := newWorkerVersionStore()
	runner := NewFrameworkRunnerAdapter(store.Resolve, config.Factory)
	return &WorkerServer{config: config, versions: store, runner: runner}
}

func (s *WorkerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/internal/worker/run" {
		writeError(w, http.StatusNotFound, "not_found", "worker endpoint was not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	expected := []byte(s.config.Token)
	provided := []byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if len(expected) == 0 || subtle.ConstantTimeCompare(provided, expected) != 1 {
		writeError(w, http.StatusUnauthorized, "worker_unauthorized", "worker authorization failed")
		return
	}
	var request RunnerRequest
	if err := decodeStrict(r, &request); err != nil || request.TenantID == "" || request.AppID == "" ||
		request.SessionID == "" || request.RequestID == "" || request.DeploymentID == "" || request.VersionID == "" ||
		request.Input == "" || request.Version == nil {
		writeError(w, http.StatusBadRequest, "invalid_worker_request", "resolved worker request is invalid")
		return
	}
	version := *request.Version
	if version.ID != request.VersionID || version.TenantID != request.TenantID || version.AgentAppID != request.AppID ||
		version.DeploymentID != request.DeploymentID || len(version.Config) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_worker_version", "resolved Deployment Version is invalid")
		return
	}
	version.Active = true
	if err := s.versions.Put(version); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_worker_version", "resolved Deployment Version is invalid")
		return
	}
	request.Version = nil
	if s.config.BeforeRun != nil {
		s.config.BeforeRun(request, version)
	}
	events, err := s.runner.RunEvents(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "worker_unavailable", "worker execution failed")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	for event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

type workerVersionStore struct {
	mu       sync.RWMutex
	versions map[string]DeploymentVersion
}

func newWorkerVersionStore() *workerVersionStore {
	return &workerVersionStore{versions: make(map[string]DeploymentVersion)}
}

func (s *workerVersionStore) Put(version DeploymentVersion) error {
	if version.ID == "" || version.TenantID == "" || version.AgentAppID == "" || version.DeploymentID == "" || len(version.Config) == 0 {
		return errors.New("invalid worker version")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.versions[version.ID]; exists {
		existing.Config = cloneConfig(existing.Config)
		version.Config = cloneConfig(version.Config)
		if fmt.Sprintf("%v", existing.Config) != fmt.Sprintf("%v", version.Config) {
			return errors.New("worker version conflict")
		}
		return nil
	}
	version.Config = cloneConfig(version.Config)
	s.versions[version.ID] = version
	return nil
}

func (s *workerVersionStore) Resolve(versionID string) (DeploymentVersion, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	version, exists := s.versions[versionID]
	if exists {
		version.Config = cloneConfig(version.Config)
	}
	return version, exists
}
