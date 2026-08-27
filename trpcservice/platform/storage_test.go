package platform

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestInMemoryStoreOrdersAndDeduplicatesEvents(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	for _, event := range []SessionEvent{{TenantID: "t", SessionID: "s", IdempotencyKey: "one", Type: "message", Payload: []byte("a")}, {TenantID: "t", SessionID: "s", IdempotencyKey: "two", Type: "summary", Payload: []byte("sum")}} {
		if err := s.AppendSessionEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendSessionEvent(ctx, SessionEvent{TenantID: "t", SessionID: "s", IdempotencyKey: "one", Type: "message", Payload: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendSessionEvent(ctx, SessionEvent{TenantID: "t", SessionID: "s", IdempotencyKey: "one", Type: "message", Payload: []byte("changed")}); err != ErrDuplicateEvent {
		t.Fatalf("error=%v", err)
	}
	events, err := s.ListSessionEvents(ctx, "t", "s", 0)
	if err != nil || len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	state, err := s.GetSessionState(ctx, "t", "s")
	if err != nil || state.Summary != "sum" || state.EventCount != 2 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestPersistedUnavailableSQLiteDoesNotFallBackToMemory(t *testing.T) {
	config := filepath.Join(t.TempDir(), "backends.json")
	if err := os.WriteFile(config, []byte(`{"tenant-a":{"backend":"sqlite","address":"/missing-parent/store.db"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	handler := NewAdminHandler(nil, DevelopmentIdentity{ID: "admin", Assignments: []TenantAssignment{{TenantID: "tenant-a", Role: RolePlatformAdmin}}})
	if err := handler.ConfigureBackendSelections(config); err != nil {
		t.Fatal(err)
	}
	store := handler.storeForTenant("tenant-a")
	if health := store.Health(context.Background()); health.Status != "unavailable" {
		t.Fatalf("health=%#v", health)
	}
	if err := store.PutMemory(context.Background(), MemoryRecord{TenantID: "tenant-a", SessionID: "s", Key: "k"}); err == nil {
		t.Fatal("unavailable store accepted a write")
	}
}

func TestInMemoryStoreConcurrentAppendsHaveUniqueSequences(t *testing.T) {
	s := NewInMemoryStore()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.AppendSessionEvent(context.Background(), SessionEvent{TenantID: "t", SessionID: "s", IdempotencyKey: itoa(uint64(i)), Type: "message", Payload: []byte("x")})
		}(i)
	}
	wg.Wait()
	events, _ := s.ListSessionEvents(context.Background(), "t", "s", 0)
	if len(events) != 20 {
		t.Fatalf("events=%d", len(events))
	}
	for i, e := range events {
		if e.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d at %d", e.Sequence, i)
		}
	}
}

func TestRedisInstancesShareNamespace(t *testing.T) {
	server := miniredis.RunT(t)
	a := NewRedisStore(server.Addr())
	b := NewRedisStore(server.Addr())
	if err := a.AppendSessionEvent(context.Background(), SessionEvent{TenantID: "t", SessionID: "s", IdempotencyKey: "k", Type: "message", Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	events, err := b.ListSessionEvents(context.Background(), "t", "s", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestSQLiteStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	a, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AppendSessionEvent(context.Background(), SessionEvent{TenantID: "t", SessionID: "s", IdempotencyKey: "k", Type: "message", Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	b, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := b.ListSessionEvents(context.Background(), "t", "s", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}
