package platform

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

func newDevelopmentClient(t *testing.T, identity DevelopmentIdentity) (*httptest.Server, *http.Client) {
	t.Helper()
	server := httptest.NewServer(NewAdminHandler(NewMemoryPlatform(), identity))
	jar, err := cookiejar.New(nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return server, &http.Client{Jar: jar}
}

func TestDevelopmentIdentityCanOnlySwitchToServerApprovedTenant(t *testing.T) {
	server := httptest.NewServer(NewAdminHandler(NewMemoryPlatform(), DevelopmentIdentity{
		ID: "developer", Name: "Local Developer",
		Assignments: []TenantAssignment{{TenantID: "tenant-a", TenantName: "Tenant A", Role: RolePlatformAdmin}},
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	resp, err := client.Get(server.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var identity identityResponse
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || identity.ActiveTenantID != "tenant-a" || identity.Assignments[0].Role != RolePlatformAdmin {
		t.Fatalf("identity = %#v, status = %d", identity, resp.StatusCode)
	}

	resp, err = client.Post(server.URL+"/api/v1/auth/switch-tenant", "application/json", strings.NewReader(`{"tenant_id":"tenant-forged"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var apiErr errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || apiErr.Error.Code != "tenant_not_assigned" {
		t.Fatalf("got %d/%q", resp.StatusCode, apiErr.Error.Code)
	}
}

func TestTenantManagementIsServerAuthorizedAndDeterministic(t *testing.T) {
	server, client := newDevelopmentClient(t, DevelopmentIdentity{
		ID: "admin", Assignments: []TenantAssignment{{TenantID: "tenant-home", TenantName: "Home", Role: RolePlatformAdmin}},
	})
	defer server.Close()

	response, err := client.Post(server.URL+"/api/v1/admin/tenants", "application/json", bytes.NewBufferString(`{"id":"tenant-east","name":"East Team"}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}
	response.Body.Close()

	response, err = client.Get(server.URL + "/api/v1/admin/tenants/tenant-east")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var tenant Tenant
	if err := json.NewDecoder(response.Body).Decode(&tenant); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || tenant.ID != "tenant-east" || tenant.Name != "East Team" {
		t.Fatalf("tenant = %#v, status = %d", tenant, response.StatusCode)
	}

	duplicate, err := client.Post(server.URL+"/api/v1/admin/tenants", "application/json", bytes.NewBufferString(`{"id":"tenant-east","name":"Other"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Body.Close()
	var apiErr errorResponse
	_ = json.NewDecoder(duplicate.Body).Decode(&apiErr)
	if duplicate.StatusCode != http.StatusConflict || apiErr.Error.Code != "tenant_exists" {
		t.Fatalf("duplicate = %d/%q", duplicate.StatusCode, apiErr.Error.Code)
	}
}

func TestViewerCannotCreateTenantWithForgedTenantHeader(t *testing.T) {
	server, client := newDevelopmentClient(t, DevelopmentIdentity{
		ID: "viewer", Assignments: []TenantAssignment{{TenantID: "tenant-view", TenantName: "View", Role: RoleViewer}},
	})
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/admin/tenants", bytes.NewBufferString(`{"id":"forged","name":"Forged"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Tenant-ID", "tenant-admin")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var apiErr errorResponse
	_ = json.NewDecoder(response.Body).Decode(&apiErr)
	if response.StatusCode != http.StatusForbidden || apiErr.Error.Code != "forbidden" {
		t.Fatalf("got %d/%q", response.StatusCode, apiErr.Error.Code)
	}
}

func TestAgentAppsAreIsolatedByTrustedTenantContext(t *testing.T) {
	server, client := newDevelopmentClient(t, DevelopmentIdentity{ID: "admin", Assignments: []TenantAssignment{
		{TenantID: "tenant-one", TenantName: "One", Role: RoleTenantAdmin},
		{TenantID: "tenant-two", TenantName: "Two", Role: RoleTenantAdmin},
	}})
	defer server.Close()

	create := func(id, name string) {
		t.Helper()
		response, err := client.Post(server.URL+"/api/v1/admin/agent-apps", "application/json", bytes.NewBufferString(`{"id":"`+id+`","name":"`+name+`","tenant_id":"tenant-forged"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create %s = %d", id, response.StatusCode)
		}
	}
	create("app-one", "App One")
	switchResponse, err := client.Post(server.URL+"/api/v1/auth/switch-tenant", "application/json", bytes.NewBufferString(`{"tenant_id":"tenant-two"}`))
	if err != nil {
		t.Fatal(err)
	}
	switchResponse.Body.Close()
	create("app-two", "App Two")

	response, err := client.Get(server.URL + "/api/v1/admin/agent-apps")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var list struct {
		Items []AgentApp `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "app-two" || list.Items[0].TenantID != "tenant-two" {
		t.Fatalf("isolated list = %#v", list.Items)
	}

	response, err = client.Get(server.URL + "/api/v1/admin/agent-apps/app-one")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var apiErr errorResponse
	_ = json.NewDecoder(response.Body).Decode(&apiErr)
	if response.StatusCode != http.StatusNotFound || apiErr.Error.Code != "agent_app_not_found" {
		t.Fatalf("cross-tenant detail = %d/%q", response.StatusCode, apiErr.Error.Code)
	}
}

func TestDeploymentVersionsAreOrderedAndImmutable(t *testing.T) {
	server, client := newDevelopmentClient(t, DevelopmentIdentity{ID: "admin", Assignments: []TenantAssignment{{TenantID: "tenant-one", TenantName: "One", Role: RoleTenantAdmin}}})
	defer server.Close()
	postJSON := func(path, body string, want int) *http.Response {
		t.Helper()
		response, err := client.Post(server.URL+path, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			response.Body.Close()
			t.Fatalf("POST %s = %d, want %d", path, response.StatusCode, want)
		}
		return response
	}
	postJSON("/api/v1/admin/agent-apps", `{"id":"app-one","name":"App One"}`, http.StatusCreated).Body.Close()
	postJSON("/api/v1/admin/deployments", `{"id":"deploy-one","agent_app_id":"app-one"}`, http.StatusCreated).Body.Close()

	first := postJSON("/api/v1/admin/deployments/deploy-one/versions", `{"config":{"model":"fake-v1","nested":{"temperature":0}}}`, http.StatusCreated)
	var versionOne DeploymentVersion
	if err := json.NewDecoder(first.Body).Decode(&versionOne); err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	second := postJSON("/api/v1/admin/deployments/deploy-one/versions", `{"config":{"model":"fake-v2"}}`, http.StatusCreated)
	var versionTwo DeploymentVersion
	if err := json.NewDecoder(second.Body).Decode(&versionTwo); err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if versionOne.Number != 1 || versionTwo.Number != 2 || versionOne.ID == versionTwo.ID {
		t.Fatalf("versions = %#v, %#v", versionOne, versionTwo)
	}

	immutable := postJSON("/api/v1/admin/deployments/deploy-one/versions/"+versionOne.ID, `{"config":{"model":"changed"}}`, http.StatusMethodNotAllowed)
	defer immutable.Body.Close()
	var apiErr errorResponse
	_ = json.NewDecoder(immutable.Body).Decode(&apiErr)
	if apiErr.Error.Code != "immutable_version" {
		t.Fatalf("immutable error = %q", apiErr.Error.Code)
	}
}

func TestDeploymentLifecycleControlsRoutedExecution(t *testing.T) {
	server, client := newDevelopmentClient(t, DevelopmentIdentity{ID: "operator", Assignments: []TenantAssignment{{TenantID: "tenant-one", TenantName: "One", Role: RolePlatformAdmin}}})
	defer server.Close()
	post := func(path, body string, want int) []byte {
		t.Helper()
		response, err := client.Post(server.URL+path, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("POST %s = %d, want %d: %s", path, response.StatusCode, want, data)
		}
		return data
	}
	post("/api/v1/admin/agent-apps", `{"id":"app-one","name":"App One"}`, http.StatusCreated)
	post("/api/v1/admin/deployments", `{"id":"deploy-one","agent_app_id":"app-one"}`, http.StatusCreated)
	var version DeploymentVersion
	if err := json.Unmarshal(post("/api/v1/admin/deployments/deploy-one/versions", `{"config":{"runner":"fake"}}`, http.StatusCreated), &version); err != nil {
		t.Fatal(err)
	}
	post("/api/v1/admin/run", `{"app_id":"app-one","session_id":"s1","input":"hello"}`, http.StatusNotFound)
	post("/api/v1/admin/deployments/deploy-one/transition", `{"status":"active"}`, http.StatusConflict)
	post("/api/v1/admin/deployments/deploy-one/transition", `{"status":"published","version_id":"`+version.ID+`"}`, http.StatusOK)
	post("/api/v1/admin/deployments/deploy-one/transition", `{"status":"active"}`, http.StatusOK)
	var result GatewayResponse
	if err := json.Unmarshal(post("/api/v1/admin/run", `{"app_id":"app-one","session_id":"s1","input":"hello"}`, http.StatusOK), &result); err != nil {
		t.Fatal(err)
	}
	if result.Output != "echo:hello" || result.SessionID != "s1" {
		t.Fatalf("result = %#v", result)
	}
	post("/api/v1/admin/deployments/deploy-one/transition", `{"status":"paused"}`, http.StatusOK)
	post("/api/v1/admin/run", `{"app_id":"app-one","session_id":"s2","input":"hello"}`, http.StatusNotFound)
}
