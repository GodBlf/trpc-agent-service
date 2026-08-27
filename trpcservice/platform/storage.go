package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
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
	state   map[string]SessionState
	memory  map[string]MemoryRecord
	backend string
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{events: map[string][]SessionEvent{}, byKey: map[memoryEventKey]SessionEvent{}, state: map[string]SessionState{}, memory: map[string]MemoryRecord{}, backend: "inmemory"}
}

func storageSessionKey(tenant, session string) string { return tenant + "\x00" + session }
func storageMemoryKey(tenant, session, key string) string {
	return tenant + "\x00" + session + "\x00" + key
}

func (s *InMemoryStore) GetSession(_ context.Context, tenant, session string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.state[storageSessionKey(tenant, session)]
	if !ok {
		return Session{}, ErrNotFound
	}
	return state.Session, nil
}

func (s *InMemoryStore) GetSessionState(_ context.Context, tenant, session string) (SessionState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.state[storageSessionKey(tenant, session)]
	if !ok {
		return SessionState{}, ErrNotFound
	}
	return cloneState(state), nil
}

func cloneState(state SessionState) SessionState { return state }

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
	state := s.state[stream]
	state.Session = Session{ID: event.SessionID, TenantID: event.TenantID, Sequence: event.Sequence}
	state.EventCount = len(s.events[stream])
	state.UpdatedAt = event.OccurredAt
	if event.Type == "summary" {
		state.Summary = string(event.Payload)
	}
	s.state[stream] = state
	return nil
}

func (s *InMemoryStore) ListSessionEvents(_ context.Context, tenant, session string, after uint64) ([]SessionEvent, error) {
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

func (s *InMemoryStore) ListMemory(_ context.Context, tenant, session string) ([]MemoryRecord, error) {
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

func (s *InMemoryStore) Health(context.Context) BackendHealth {
	return BackendHealth{Backend: s.backend, Status: "healthy", Checked: time.Now().UTC()}
}

// RedisStore models a shared Redis namespace without requiring a provider in
// local tests. Instances created with the same address share the same state.
type RedisStore struct {
	*InMemoryStore
	address string
}

var redisNamespaces sync.Map

func NewRedisStore(address string) *RedisStore {
	if address == "" {
		address = "local"
	}
	value, _ := redisNamespaces.LoadOrStore(address, NewInMemoryStore())
	store := value.(*InMemoryStore)
	store.backend = "redis"
	return &RedisStore{InMemoryStore: store, address: address}
}
func (s *RedisStore) Health(context.Context) BackendHealth {
	return BackendHealth{Backend: "redis", Status: "healthy", Message: s.address, Checked: time.Now().UTC()}
}

// SQLiteStore persists the reference store as JSON. The shape is deliberately
// SQL-compatible and keeps local development free from CGO/provider setup.
type SQLiteStore struct {
	*InMemoryStore
	path string
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	s := &SQLiteStore{InMemoryStore: NewInMemoryStore(), path: path}
	s.backend = "sqlite"
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var dump struct {
		Events map[string][]SessionEvent
		State  map[string]SessionState
		Memory map[string]MemoryRecord
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil, err
	}
	s.events, s.state, s.memory = dump.Events, dump.State, dump.Memory
	s.byKey = map[memoryEventKey]SessionEvent{}
	for _, stream := range s.events {
		for _, event := range stream {
			s.byKey[memoryEventKey{event.TenantID, event.SessionID, event.IdempotencyKey}] = event
		}
	}
	return s, nil
}
func (s *SQLiteStore) persist() error {
	if s.path == "" {
		return nil
	}
	s.mu.RLock()
	dump := struct {
		Events map[string][]SessionEvent
		State  map[string]SessionState
		Memory map[string]MemoryRecord
	}{s.events, s.state, s.memory}
	data, err := json.Marshal(dump)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}
func (s *SQLiteStore) AppendSessionEvent(ctx context.Context, e SessionEvent) error {
	if err := s.InMemoryStore.AppendSessionEvent(ctx, e); err != nil {
		return err
	}
	return s.persist()
}
func (s *SQLiteStore) PutMemory(ctx context.Context, e MemoryRecord) error {
	if err := s.InMemoryStore.PutMemory(ctx, e); err != nil {
		return err
	}
	return s.persist()
}
func (s *SQLiteStore) Health(context.Context) BackendHealth {
	return BackendHealth{Backend: "sqlite", Status: "healthy", Message: s.path, Checked: time.Now().UTC()}
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
