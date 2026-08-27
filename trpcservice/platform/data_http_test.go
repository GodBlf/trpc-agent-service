package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDataManagementAPIUsesTrustedTenantAndBackendSelection(t *testing.T) {
	h := NewAdminHandler(NewMemoryPlatform(), DevelopmentIdentity{ID: "admin", Name: "Admin", Assignments: []TenantAssignment{{TenantID: "tenant-a", TenantName: "A", Role: RolePlatformAdmin}, {TenantID: "tenant-b", TenantName: "B", Role: RoleViewer}}})
	ts := httptest.NewServer(h)
	defer ts.Close()
	client := ts.Client()
	resp, err := client.Get(ts.URL + "/api/v1/admin/storage/backend")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health=%d", resp.StatusCode)
	}
	resp.Body.Close()
	body, _ := json.Marshal(map[string]string{"backend": "redis", "address": "api-test"})
	resp, err = client.Post(ts.URL+"/api/v1/admin/storage/backend", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("select=%d", resp.StatusCode)
	}
	resp.Body.Close()
	store := h.storeForTenant("tenant-a")
	if err := store.AppendSessionEvent(context.Background(), SessionEvent{TenantID: "tenant-a", SessionID: "s", IdempotencyKey: "k", Type: "message", Payload: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	resp, err = client.Get(ts.URL + "/api/v1/admin/sessions/s/events")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, err = client.Post(ts.URL+"/api/v1/admin/storage/backend", "application/json", bytes.NewReader([]byte(`{"backend":"redis"}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
