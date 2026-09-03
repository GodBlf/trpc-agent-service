package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGovernanceManagementAPIIsTenantScopedAndRoleEnforced(t *testing.T) {
	server, client := newDevelopmentClient(t, DevelopmentIdentity{ID: "admin", Assignments: []TenantAssignment{
		{TenantID: "tenant-a", TenantName: "A", Role: RoleTenantAdmin},
		{TenantID: "tenant-b", TenantName: "B", Role: RoleViewer},
	}})
	defer server.Close()
	requireJSONResponse(t, client, server.URL+"/api/v1/admin/agent-apps", `{"id":"app-a","name":"App A"}`, "", http.StatusCreated, nil)

	var policy TenantPolicy
	requireJSONResponse(t, client, server.URL+"/api/v1/admin/governance/policy", `{
		"agent_app_id":"app-a","allowed_tools":["search"],"allowed_mcp":["calendar"],
		"dangerous_tools":[],"denied_input_patterns":["blocked"],"redacted_patterns":["secret"],
		"token_budget":100,"estimated_tokens_per_run":5,"rate_limit":10,"rate_window_seconds":60
	}`, "", http.StatusOK, &policy)
	if policy.TenantID != "tenant-a" || policy.Revision != 1 {
		t.Fatalf("policy = %#v", policy)
	}

	response, err := client.Get(server.URL + "/api/v1/admin/governance/policy?app_id=app-a")
	if err != nil {
		t.Fatal(err)
	}
	decodeResponse(t, response, http.StatusOK, &policy)

	var auditList struct {
		Items []AuditEvent `json:"items"`
	}
	response, _ = client.Get(server.URL + "/api/v1/admin/governance/audit?decision=policy.updated")
	decodeResponse(t, response, http.StatusOK, &auditList)
	if len(auditList.Items) != 1 || auditList.Items[0].TenantID != "tenant-a" {
		t.Fatalf("audits = %#v", auditList.Items)
	}

	requireJSONResponse(t, client, server.URL+"/api/v1/auth/switch-tenant", `{"tenant_id":"tenant-b"}`, "", http.StatusOK, new(identityResponse))
	response = postJSONWithKey(t, client, server.URL+"/api/v1/admin/governance/policy", `{"agent_app_id":"app-a"}`, "")
	assertAPIError(t, response, http.StatusForbidden, "forbidden")
	response, _ = client.Get(server.URL + "/api/v1/admin/governance/policy?app_id=app-a")
	assertAPIError(t, response, http.StatusNotFound, "policy_not_found")
}

func TestGovernancePolicyTreatsRedactionPatternsAsWriteOnly(t *testing.T) {
	server, client := newDevelopmentClient(t, DevelopmentIdentity{ID: "admin", Assignments: []TenantAssignment{{TenantID: "tenant-a", Role: RoleTenantAdmin}}})
	defer server.Close()
	requireJSONResponse(t, client, server.URL+"/api/v1/admin/agent-apps", `{"id":"app-a","name":"App A"}`, "", http.StatusCreated, nil)
	response := postJSONWithKey(t, client, server.URL+"/api/v1/admin/governance/policy", `{"agent_app_id":"app-a","redacted_patterns":["stage5-secret-canary"]}`, "")
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || bytes.Contains(data, []byte("stage5-secret-canary")) || !bytes.Contains(data, []byte("[REDACTED]")) {
		t.Fatalf("response = %d %s", response.StatusCode, data)
	}
	response, _ = client.Get(server.URL + "/api/v1/admin/governance/policy?app_id=app-a")
	data, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || bytes.Contains(data, []byte("stage5-secret-canary")) {
		t.Fatalf("read response = %d %s", response.StatusCode, data)
	}
}

func TestBackendAuthorizationDenialProducesAuditEvent(t *testing.T) {
	handler := NewAdminHandler(nil, DevelopmentIdentity{ID: "viewer", Assignments: []TenantAssignment{{TenantID: "tenant-a", Role: RoleViewer}}})
	server, client := newHandlerClient(t, handler)
	defer server.Close()
	response, _ := client.Post(server.URL+"/api/v1/admin/tenants", "application/json", bytes.NewBufferString(`{"id":"tenant-x","name":"Tenant X"}`))
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", response.StatusCode)
	}
	audits := handler.governance.AuditEvents(AuditQuery{TenantID: "tenant-a", Decision: "authorization.denied"})
	if len(audits) != 1 || audits[0].UserID != "viewer" {
		t.Fatalf("audits = %#v", audits)
	}
}

func TestGovernancePolicyRunsBeforeChatRunnerAndPropagatesTrace(t *testing.T) {
	runs := make(chan RunnerRequest, 2)
	client := newChannelTestClient(t, requestCapturingRunner{requests: runs})
	client.activateApp("app-one", "deploy-one")
	_, err := client.handler.governance.PutPolicy(context.Background(), TenantPolicy{
		TenantID: "tenant-one", AgentAppID: "app-one", DeniedInputPatterns: []string{"blocked"}, RedactedPatterns: []string{"canary-secret"}, TokenBudget: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)

	response := client.do(http.MethodPost, "/api/v1/chat/sessions/session-one/messages", `{"input":"blocked"}`, map[string]string{"X-Request-ID": "request-denied"})
	assertChannelAPIError(t, response, http.StatusForbidden, "policy_denied")
	select {
	case request := <-runs:
		t.Fatalf("denied input reached Runner: %#v", request)
	case <-time.After(30 * time.Millisecond):
	}

	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"hello canary-secret"}`, map[string]string{"X-Request-ID": "request-allowed"}, http.StatusAccepted, nil)
	select {
	case request := <-runs:
		if request.Input != "hello [REDACTED]" {
			t.Fatalf("Runner input = %q", request.Input)
		}
	case <-time.After(time.Second):
		t.Fatal("allowed input did not reach Runner")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		audits := client.handler.governance.AuditEvents(AuditQuery{TenantID: "tenant-one", RequestID: "request-allowed"})
		if len(audits) >= 2 {
			trace, found := client.handler.governance.Trace("tenant-one", audits[0].TraceID, "")
			if !found || trace.RequestID != "request-allowed" {
				t.Fatalf("trace = %#v, found = %v", trace, found)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("completion audit was not recorded")
}

func TestExternalIMUserPolicyDeniesBeforeRunner(t *testing.T) {
	runs := make(chan RunnerRequest, 2)
	client := newChannelTestClient(t, requestCapturingRunner{requests: runs})
	client.activateApp("app-one", "deploy-one")
	_, _ = client.handler.governance.PutPolicy(context.Background(), TenantPolicy{
		TenantID: "tenant-one", AgentAppID: "app-one", AllowedIMUsers: []string{"allowed-user"},
	})
	routes := NewBotTenantAllowlist()
	if err := routes.Upsert(BotRoute{Provider: ChannelTelegram, ExternalSubject: "123", TenantID: "tenant-one", AppID: "app-one", ConversationType: ConversationSingle}); err != nil {
		t.Fatal(err)
	}
	runtime := NewProviderRuntime(BotConfig{}, routes, nil)
	client.handler.ConfigureProviderRuntime(runtime)
	body := []byte(`{"update_id":7,"message":{"message_id":9,"chat":{"id":123},"from":{"id":456},"text":"hello"}}`)
	if err := client.handler.ProcessProviderMessage(context.Background(), ChannelTelegram, "bot", body); !errors.Is(err, ErrProviderMessageIgnored) {
		t.Fatalf("provider error = %v", err)
	}
	select {
	case request := <-runs:
		t.Fatalf("denied provider message reached Runner: %#v", request)
	case <-time.After(30 * time.Millisecond):
	}
	deliveries := runtime.Deliveries("tenant-one")
	if len(deliveries) != 1 || deliveries[0].Code != "im_user_denied" || deliveries[0].Status != "rejected" {
		t.Fatalf("deliveries = %#v", deliveries)
	}
}

func TestDeploymentToolPolicyAndDangerousConfirmationGateRunner(t *testing.T) {
	runs := make(chan RunnerRequest, 2)
	client := newChannelTestClient(t, requestCapturingRunner{requests: runs})
	client.post("/api/v1/admin/agent-apps", `{"id":"app-one","name":"App"}`, nil, http.StatusCreated, nil)
	client.post("/api/v1/admin/deployments", `{"id":"deploy-one","agent_app_id":"app-one"}`, nil, http.StatusCreated, nil)
	var version DeploymentVersion
	client.post("/api/v1/admin/deployments/deploy-one/versions", `{"config":{"runner":"mock","tools":["deploy"],"mcp":["calendar"]}}`, map[string]string{"Idempotency-Key": "governance-tools"}, http.StatusCreated, &version)
	client.post("/api/v1/admin/deployments/deploy-one/transition", `{"status":"published","version_id":"`+version.ID+`"}`, nil, http.StatusOK, nil)
	client.post("/api/v1/admin/deployments/deploy-one/transition", `{"status":"active"}`, nil, http.StatusOK, nil)
	client.post("/api/v1/chat/sessions", `{"app_id":"app-one","session_id":"session-one"}`, nil, http.StatusCreated, nil)

	_, _ = client.handler.governance.PutPolicy(context.Background(), TenantPolicy{TenantID: "tenant-one", AgentAppID: "app-one", AllowedTools: []string{"search"}, AllowedMCP: []string{"calendar"}})
	response := client.do(http.MethodPost, "/api/v1/chat/sessions/session-one/messages", `{"input":"ship"}`, map[string]string{"X-Request-ID": "request-denied-tool"})
	assertChannelAPIError(t, response, http.StatusForbidden, "tool_not_allowed")

	_, _ = client.handler.governance.PutPolicy(context.Background(), TenantPolicy{TenantID: "tenant-one", AgentAppID: "app-one", AllowedTools: []string{"deploy"}, AllowedMCP: []string{"calendar"}, DangerousTools: []string{"deploy"}})
	response = client.do(http.MethodPost, "/api/v1/chat/sessions/session-one/messages", `{"input":"ship"}`, map[string]string{"X-Request-ID": "request-confirm"})
	var pending struct {
		Error          errorBody `json:"error"`
		ConfirmationID string    `json:"confirmation_id"`
		TraceID        string    `json:"trace_id"`
	}
	decodeResponse(t, response, http.StatusConflict, &pending)
	if pending.Error.Code != "confirmation_required" || pending.ConfirmationID == "" || pending.TraceID == "" {
		t.Fatalf("pending = %#v", pending)
	}
	var confirmation ToolConfirmation
	client.post("/api/v1/admin/governance/confirmations/"+pending.ConfirmationID+"/decision", `{"approve":true}`, nil, http.StatusOK, &confirmation)
	client.post("/api/v1/chat/sessions/session-one/messages", `{"input":"ship"}`, map[string]string{"X-Request-ID": "request-confirm"}, http.StatusAccepted, nil)
	select {
	case request := <-runs:
		if request.TraceID != pending.TraceID || request.Input != "ship" {
			t.Fatalf("Runner request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("approved Tool request did not reach Runner")
	}
	select {
	case request := <-runs:
		t.Fatalf("Tool request reached Runner more than once: %#v", request)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestGovernanceConfirmationMetricsAndTraceAPIs(t *testing.T) {
	handler := NewAdminHandler(NewMemoryPlatform(), DevelopmentIdentity{ID: "operator", Assignments: []TenantAssignment{{TenantID: "tenant-a", TenantName: "A", Role: RoleOperator}}})
	_, _ = handler.governance.PutPolicy(context.Background(), TenantPolicy{TenantID: "tenant-a", AgentAppID: "app-a", AllowedTools: []string{"deploy"}, DangerousTools: []string{"deploy"}})
	result, _ := handler.governance.Evaluate(context.Background(), GovernanceRequest{TenantID: "tenant-a", AgentAppID: "app-a", UserID: "operator", SessionID: "session-a", RequestID: "request-a", Input: "ship", RequiredTools: []string{"deploy"}})
	server, client := newHandlerClient(t, handler)
	defer server.Close()

	var confirmations struct {
		Items []ToolConfirmation `json:"items"`
	}
	response, _ := client.Get(server.URL + "/api/v1/admin/governance/confirmations")
	decodeResponse(t, response, http.StatusOK, &confirmations)
	if len(confirmations.Items) != 1 || confirmations.Items[0].ID != result.ConfirmationID {
		t.Fatalf("confirmations = %#v", confirmations.Items)
	}
	var decided ToolConfirmation
	requireJSONResponse(t, client, server.URL+"/api/v1/admin/governance/confirmations/"+result.ConfirmationID+"/decision", `{"approve":true}`, "", http.StatusOK, &decided)
	if decided.Status != ConfirmationApproved {
		t.Fatalf("decision = %#v", decided)
	}

	var metrics TenantMetrics
	response, _ = client.Get(server.URL + "/api/v1/admin/governance/metrics")
	decodeResponse(t, response, http.StatusOK, &metrics)
	if metrics.Requests == 0 {
		t.Fatalf("metrics = %#v", metrics)
	}
	var trace PlatformTrace
	response, _ = client.Get(server.URL + "/api/v1/admin/governance/traces?trace_id=" + result.TraceID)
	decodeResponse(t, response, http.StatusOK, &trace)
	if trace.TraceID != result.TraceID || len(trace.Spans) == 0 {
		t.Fatalf("trace = %#v", trace)
	}

	encoded, _ := json.Marshal(confirmations)
	if string(encoded) == "" {
		t.Fatal("confirmation response was not serializable")
	}
}

func newHandlerClient(t *testing.T, handler *AdminHandler) (*httptest.Server, *http.Client) {
	t.Helper()
	server := httptest.NewServer(handler)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, &http.Client{Jar: jar}
}
