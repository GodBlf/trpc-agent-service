package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	"github.com/liuzengh/trpc-agent-service/trpcservice/lifecycle"
	"github.com/liuzengh/trpc-agent-service/trpcservice/platform"
)

func TestNewHandlerHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	NewHandler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status body = %q, want ok", body["status"])
	}
}

func TestNewHandlerVersion(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	res := httptest.NewRecorder()
	NewHandler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["version"] != trpcservice.Version {
		t.Fatalf("version = %q, want %q", body["version"], trpcservice.Version)
	}
}

func TestNewHandlerUnknownRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	res := httptest.NewRecorder()
	NewHandler().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
}

func TestHandlerWithLifecycleRejectsNewWorkAfterShutdown(t *testing.T) {
	life := lifecycle.New()
	if err := life.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithRunnerAndLifecycle(platform.EchoRunner{}, platform.TenantContext{TenantID: "t"}, life)
	req := httptest.NewRequest(http.MethodPost, "/v1/run", strings.NewReader(`{"app_id":"a","session_id":"s","input":"x"}`))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusServiceUnavailable)
	}
}
