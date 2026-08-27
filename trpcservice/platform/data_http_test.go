package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestDataManagementAPIUsesTrustedTenantAndBackendSelection(t *testing.T) {
	redisServer := miniredis.RunT(t)
	h := NewAdminHandler(NewMemoryPlatform(), DevelopmentIdentity{ID: "admin", Name: "Admin", Assignments: []TenantAssignment{{TenantID: "tenant-a", TenantName: "A", Role: RolePlatformAdmin}, {TenantID: "tenant-b", TenantName: "B", Role: RoleViewer}}})
	h.ConfigureBackendCatalog(redisServer.Addr(), "")
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
	body, _ := json.Marshal(map[string]string{"backend": "redis"})
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

func TestMigrationAPIReportsProgressAndCompletion(t *testing.T) {
	redisServer := miniredis.RunT(t)
	source := NewRedisStore(redisServer.Addr())
	if err := source.AppendSessionEvent(context.Background(), SessionEvent{TenantID: "tenant-a", SessionID: "s", IdempotencyKey: "one", Type: "message", Payload: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	source.Close()
	handler := NewAdminHandler(nil, DevelopmentIdentity{ID: "admin", Assignments: []TenantAssignment{{TenantID: "tenant-a", Role: RolePlatformAdmin}}})
	defer handler.Close()
	handler.ConfigureMigration(redisServer.Addr(), filepath.Join(t.TempDir(), "destination.db"), filepath.Join(t.TempDir(), "checkpoint.json"))
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := server.Client().Post(server.URL+"/api/v1/admin/migrations", "application/json", bytes.NewBufferString(`{"dry_run":false,"batch_size":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var job migrationResult
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || job.Status != "running" {
		t.Fatalf("job=%#v status=%d", job, response.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = server.Client().Get(server.URL + "/api/v1/admin/migrations/" + job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if job.Status == "completed" {
			if job.ProcessedSessions != 1 || job.SourceCount != job.DestinationCount {
				t.Fatalf("job=%#v", job)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("migration did not complete: %#v", job)
}
