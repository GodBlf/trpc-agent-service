package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

// SessionState is the materialized, tenant-scoped view of a Session.
type SessionState struct {
	Session
	Summary    string    `json:"summary"`
	EventCount int       `json:"event_count"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type MemoryRecord struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	SessionID string    `json:"session_id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BackendHealth struct {
	Backend string    `json:"backend"`
	Status  string    `json:"status"`
	Message string    `json:"message,omitempty"`
	Checked time.Time `json:"checked_at"`
}

// DataStore is the Stage 2 storage boundary. Implementations must preserve
// event immutability, tenant isolation, idempotency, and sequence ordering.
type DataStore interface {
	StorageAdapter
	GetSessionState(context.Context, string, string) (SessionState, error)
	ListMemory(context.Context, string, string) ([]MemoryRecord, error)
	PutMemory(context.Context, MemoryRecord) error
	Health(context.Context) BackendHealth
}

type memoryEventKey struct{ tenant, session, key string }

// InMemoryStore is the deterministic reference implementation used by local
// development and tests.
type InMemoryStore struct {
	mu      sync.RWMutex
	events  map[string][]SessionEvent
	byKey   map[memoryEventKey]SessionEvent
	memory  map[string]MemoryRecord
	backend string
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{events: map[string][]SessionEvent{}, byKey: map[memoryEventKey]SessionEvent{}, memory: map[string]MemoryRecord{}, backend: "inmemory"}
}

func storageSessionKey(tenant, session string) string { return tenant + "\x00" + session }
func storageMemoryKey(tenant, session, key string) string {
	return tenant + "\x00" + session + "\x00" + key
}

func (s *InMemoryStore) GetSession(ctx context.Context, tenant, session string) (Session, error) {
	state, err := s.GetSessionState(ctx, tenant, session)
	return state.Session, err
}

func (s *InMemoryStore) GetSessionState(ctx context.Context, tenant, session string) (SessionState, error) {
	events, err := s.ListSessionEvents(ctx, tenant, session, 0)
	if err != nil {
		return SessionState{}, err
	}
	return materializeSession(tenant, session, events)
}

func materializeSession(tenant, session string, events []SessionEvent) (SessionState, error) {
	if len(events) == 0 {
		return SessionState{}, ErrNotFound
	}
	state := SessionState{Session: Session{ID: session, TenantID: tenant}, EventCount: len(events)}
	for _, event := range events {
		state.Sequence = event.Sequence
		state.UpdatedAt = event.OccurredAt
		if event.Type == "summary" {
			state.Summary = string(event.Payload)
		}
	}
	return state, nil
}

func (s *InMemoryStore) AppendSessionEvent(ctx context.Context, event SessionEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.TenantID == "" || event.SessionID == "" || event.IdempotencyKey == "" {
		return errors.New("platform: invalid session event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryEventKey{event.TenantID, event.SessionID, event.IdempotencyKey}
	if prior, ok := s.byKey[key]; ok {
		if prior.Type == event.Type && string(prior.Payload) == string(event.Payload) {
			return nil
		}
		return ErrDuplicateEvent
	}
	stream := storageSessionKey(event.TenantID, event.SessionID)
	event.Sequence = uint64(len(s.events[stream]) + 1)
	if event.ID == "" {
		event.ID = event.TenantID + ":" + event.SessionID + ":" + itoa(event.Sequence)
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	event.Payload = append([]byte(nil), event.Payload...)
	s.events[stream] = append(s.events[stream], event)
	s.byKey[key] = event
	return nil
}

func (s *InMemoryStore) ListSessionEvents(ctx context.Context, tenant, session string, after uint64) ([]SessionEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stream := s.events[storageSessionKey(tenant, session)]
	result := make([]SessionEvent, 0, len(stream))
	for _, event := range stream {
		if event.Sequence > after {
			event.Payload = append([]byte(nil), event.Payload...)
			result = append(result, event)
		}
	}
	return result, nil
}

func (s *InMemoryStore) ListMemory(ctx context.Context, tenant, session string) ([]MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []MemoryRecord{}
	prefix := storageSessionKey(tenant, session) + "\x00"
	for key, item := range s.memory {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func (s *InMemoryStore) PutMemory(ctx context.Context, item MemoryRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.TenantID == "" || item.SessionID == "" || item.Key == "" {
		return errors.New("platform: invalid memory record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == "" {
		item.ID = item.TenantID + ":" + item.SessionID + ":" + item.Key
	}
	item.UpdatedAt = time.Now().UTC()
	s.memory[storageMemoryKey(item.TenantID, item.SessionID, item.Key)] = item
	return nil
}

func (s *InMemoryStore) Health(ctx context.Context) BackendHealth {
	if err := ctx.Err(); err != nil {
		return BackendHealth{Backend: s.backend, Status: "unavailable", Checked: time.Now().UTC()}
	}
	return BackendHealth{Backend: s.backend, Status: "healthy", Checked: time.Now().UTC()}
}

func itoa(v uint64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}
func eventChecksum(events []SessionEvent) string {
	h := sha256.New()
	for _, e := range events {
		h.Write(e.Payload)
	}
	return hex.EncodeToString(h.Sum(nil))
}
