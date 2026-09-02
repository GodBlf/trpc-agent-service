package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestLoadBotConfigUsesPlatformCredentialsOnly(t *testing.T) {
	values := map[string]string{
		"TRPC_TELEGRAM_BOT_USERNAME": "agent_bot",
		"TRPC_TELEGRAM_BOT_TOKEN":    "telegram-token",
		"TRPC_WECOM_BOT_ID":          "wecom-bot-id",
		"TRPC_WECOM_BOT_SECRET":      "wecom-secret",
	}
	config := LoadBotConfig(func(key string) string { return values[key] })
	if config.TelegramUsername != "agent_bot" || config.TelegramToken != "telegram-token" || config.WeComBotID != "wecom-bot-id" || config.WeComSecret != "wecom-secret" {
		t.Fatalf("config = %#v", config)
	}
}

func TestProviderRouteAPIIsPlatformAdminOnly(t *testing.T) {
	client := newChannelTestClient(t, EchoRunner{})
	client.activateApp("app-one", "deploy-one")
	runtime := NewProviderRuntime(BotConfig{}, NewBotTenantAllowlist(), nil)
	client.handler.ConfigureProviderRuntime(runtime)
	var route BotRoute
	client.post("/api/v1/admin/providers/routes", `{"provider":"telegram","external_subject":"123","tenant_id":"tenant-one","app_id":"app-one","conversation_type":"group"}`, nil, http.StatusCreated, &route)
	if route.ExternalSubject != "123" || route.TenantID != "tenant-one" {
		t.Fatalf("route = %#v", route)
	}
	client.post("/api/v1/auth/switch-tenant", `{"tenant_id":"tenant-two"}`, nil, http.StatusOK, nil)
	response := client.do(http.MethodPost, "/api/v1/admin/providers/routes", `{"provider":"telegram","external_subject":"456","tenant_id":"tenant-two","app_id":"app-one"}`, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create status = %d", response.StatusCode)
	}
}

func TestTelegramProviderMessageRoutesToRunner(t *testing.T) {
	runs := make(chan RunnerRequest, 1)
	client := newChannelTestClient(t, requestCapturingRunner{requests: runs})
	client.activateApp("app-one", "deploy-one")
	routes := NewBotTenantAllowlist()
	if err := routes.Upsert(BotRoute{Provider: ChannelTelegram, ExternalSubject: "123", TenantID: "tenant-one", AppID: "app-one"}); err != nil {
		t.Fatal(err)
	}
	runtime := NewProviderRuntime(BotConfig{}, routes, nil)
	client.handler.ConfigureProviderRuntime(runtime)
	body := []byte(`{"update_id":7,"message":{"message_id":9,"chat":{"id":123},"from":{"id":456},"text":"hello"}}`)
	if err := client.handler.ProcessProviderMessage(context.Background(), ChannelTelegram, "agent_bot", body); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-runs:
		if request.TenantID != "tenant-one" || request.AppID != "app-one" || request.UserID != "456" || request.Input != "hello" {
			t.Fatalf("request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("provider message did not reach Runner")
	}
}

func TestTelegramPollDoesNotAdvanceOffsetWhenProcessingFails(t *testing.T) {
	runtime := NewProviderRuntime(BotConfig{TelegramToken: "redacted"}, nil, func(context.Context, string, string, []byte) error { return context.Canceled })
	runtime.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"ok": true, "result": []any{map[string]any{"update_id": 7}}})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	offset := int64(0)
	if err := runtime.pollTelegram(context.Background(), &offset); err == nil || offset != 0 {
		t.Fatalf("error=%v offset=%d", err, offset)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestBotTenantAllowlistRejectsConflictingRoute(t *testing.T) {
	allowlist := NewBotTenantAllowlist()
	route := BotRoute{Provider: ChannelTelegram, ExternalSubject: "chat-1", TenantID: "tenant-one", AppID: "app-one"}
	if err := allowlist.Upsert(route); err != nil {
		t.Fatal(err)
	}
	if err := allowlist.Upsert(BotRoute{Provider: ChannelTelegram, ExternalSubject: "chat-1", TenantID: "tenant-two", AppID: "app-two"}); err == nil {
		t.Fatal("expected conflicting route to be rejected")
	}
	got, ok := allowlist.Resolve(ChannelTelegram, "chat-1")
	if !ok || got.TenantID != "tenant-one" {
		t.Fatalf("route = %#v/%v", got, ok)
	}
}

func TestProviderRuntimeMissingCredentialsStaysAvailable(t *testing.T) {
	runtime := NewProviderRuntime(BotConfig{}, nil, nil)
	runtime.Start(context.Background())
	defer runtime.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		statuses := runtime.Statuses()
		if len(statuses) == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("statuses = %#v", runtime.Statuses())
}
