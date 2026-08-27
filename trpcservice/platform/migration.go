package platform

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"time"
)

type sessionLister interface {
	ListSessionIDs(context.Context, string) ([]string, error)
}
type MigrationOptions struct {
	TenantID       string
	DryRun         bool
	BatchSize      int
	CheckpointPath string
	MaxRetries     int
}
type MigrationReport struct {
	Status           string `json:"status"`
	DryRun           bool   `json:"dry_run"`
	Sessions         int    `json:"sessions"`
	SourceCount      int    `json:"source_count"`
	DestinationCount int    `json:"destination_count"`
	Checksum         string `json:"checksum"`
	Resumed          bool   `json:"resumed"`
	Message          string `json:"message,omitempty"`
}
type migrationCheckpoint struct {
	TenantID string `json:"tenant_id"`
	Next     int    `json:"next"`
}

func MigrateRedisToSQL(ctx context.Context, source DataStore, destination DataStore, options MigrationOptions) (MigrationReport, error) {
	lister, ok := source.(sessionLister)
	if !ok {
		return MigrationReport{}, errors.New("source does not support enumeration")
	}
	sessions, err := lister.ListSessionIDs(ctx, options.TenantID)
	if err != nil {
		return MigrationReport{}, err
	}
	sort.Strings(sessions)
	if options.BatchSize <= 0 {
		options.BatchSize = 100
	}
	if options.MaxRetries <= 0 {
		options.MaxRetries = 3
	}
	report := MigrationReport{Status: "running", DryRun: options.DryRun, Sessions: len(sessions)}
	type sessionData struct {
		events []SessionEvent
		memory []MemoryRecord
	}
	sourceData := make(map[string]sessionData, len(sessions))
	var sourceEvents []SessionEvent
	for _, session := range sessions {
		events, err := source.ListSessionEvents(ctx, options.TenantID, session, 0)
		if err != nil {
			return report, err
		}
		memory, err := source.ListMemory(ctx, options.TenantID, session)
		if err != nil {
			return report, err
		}
		sourceData[session] = sessionData{events: events, memory: memory}
		sourceEvents = append(sourceEvents, events...)
		report.SourceCount += len(events) + len(memory)
	}
	report.Checksum = eventChecksum(sourceEvents)
	checkpoint := migrationCheckpoint{TenantID: options.TenantID}
	if data, err := os.ReadFile(options.CheckpointPath); err == nil {
		if json.Unmarshal(data, &checkpoint) == nil && checkpoint.TenantID == options.TenantID {
			report.Resumed = checkpoint.Next > 0
		}
	}
	for index := checkpoint.Next; index < len(sessions); index++ {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		session := sessions[index]
		data := sourceData[session]
		if !options.DryRun {
			for _, event := range data.events {
				if err := retry(ctx, options.MaxRetries, func() error { return destination.AppendSessionEvent(ctx, event) }); err != nil {
					return report, err
				}
			}
			for _, item := range data.memory {
				if err := retry(ctx, options.MaxRetries, func() error { return destination.PutMemory(ctx, item) }); err != nil {
					return report, err
				}
			}
			if (index+1)%options.BatchSize == 0 || index+1 == len(sessions) {
				checkpoint.Next = index + 1
				if err := writeCheckpoint(options.CheckpointPath, checkpoint); err != nil {
					return report, err
				}
			}
		}
	}
	var destinationEvents []SessionEvent
	if !options.DryRun {
		for _, session := range sessions {
			events, err := destination.ListSessionEvents(ctx, options.TenantID, session, 0)
			if err != nil {
				return report, err
			}
			memory, err := destination.ListMemory(ctx, options.TenantID, session)
			if err != nil {
				return report, err
			}
			destinationEvents = append(destinationEvents, events...)
			report.DestinationCount += len(events) + len(memory)
		}
		if report.SourceCount != report.DestinationCount {
			report.Status = "failed"
			report.Message = "source and destination counts differ"
			return report, errors.New(report.Message)
		}
		_ = os.Remove(options.CheckpointPath)
	} else {
		report.DestinationCount = report.SourceCount
	}
	if !options.DryRun && report.Checksum != eventChecksum(destinationEvents) {
		report.Status = "failed"
		report.Message = "source and destination content differ"
		return report, errors.New(report.Message)
	}
	report.Status = "completed"
	return report, nil
}
func retry(ctx context.Context, max int, fn func() error) error {
	var err error
	for i := 0; i < max; i++ {
		if err = fn(); err == nil || errors.Is(err, ErrDuplicateEvent) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 10 * time.Millisecond):
		}
	}
	return err
}
func writeCheckpoint(path string, value migrationCheckpoint) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (s *InMemoryStore) ListSessionIDs(ctx context.Context, tenant string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := []string{}
	prefix := tenant + "\x00"
	for key := range s.events {
		if strings.HasPrefix(key, prefix) {
			ids = append(ids, strings.TrimPrefix(key, prefix))
		}
	}
	return ids, nil
}
func (s *SQLStore) ListSessionIDs(ctx context.Context, tenant string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT DISTINCT session_id FROM session_events WHERE tenant_id=? ORDER BY session_id`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *RedisStore) ListSessionIDs(ctx context.Context, tenant string) ([]string, error) {
	ids := map[string]struct{}{}
	for _, kind := range []string{"events", "memory"} {
		prefix := s.prefix + kind + ":" + tenant + ":"
		var cursor uint64
		for {
			keys, next, err := s.client.Scan(ctx, cursor, prefix+"*", 100).Result()
			if err != nil {
				return nil, err
			}
			for _, key := range keys {
				ids[strings.TrimPrefix(key, prefix)] = struct{}{}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}
