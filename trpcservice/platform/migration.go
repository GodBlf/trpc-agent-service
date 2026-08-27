package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Progress       func(MigrationReport)
}
type MigrationReport struct {
	Status            string `json:"status"`
	DryRun            bool   `json:"dry_run"`
	Sessions          int    `json:"sessions"`
	ProcessedSessions int    `json:"processed_sessions"`
	SourceCount       int    `json:"source_count"`
	DestinationCount  int    `json:"destination_count"`
	Checksum          string `json:"checksum"`
	Resumed           bool   `json:"resumed"`
	Message           string `json:"message,omitempty"`
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
	sourceHash := sha256.New()
	for _, session := range sessions {
		events, memory, err := readSessionData(ctx, source, options.TenantID, session)
		if err != nil {
			return report, err
		}
		sourceHash.Write([]byte(migrationChecksum(events, memory)))
		report.SourceCount += len(events) + len(memory)
	}
	report.Checksum = hex.EncodeToString(sourceHash.Sum(nil))
	checkpoint := migrationCheckpoint{TenantID: options.TenantID}
	if data, err := os.ReadFile(options.CheckpointPath); err == nil {
		if json.Unmarshal(data, &checkpoint) == nil && checkpoint.TenantID == options.TenantID {
			report.Resumed = checkpoint.Next > 0
		}
	}
	for start := checkpoint.Next; start < len(sessions); start += options.BatchSize {
		end := start + options.BatchSize
		if end > len(sessions) {
			end = len(sessions)
		}
		for _, session := range sessions[start:end] {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			events, memory, err := readSessionData(ctx, source, options.TenantID, session)
			if err != nil {
				return report, err
			}
			if !options.DryRun {
				for _, event := range events {
					if err := retry(ctx, options.MaxRetries, func() error { return destination.AppendSessionEvent(ctx, event) }); err != nil {
						return report, err
					}
				}
				for _, item := range memory {
					if err := retry(ctx, options.MaxRetries, func() error { return destination.PutMemory(ctx, item) }); err != nil {
						return report, err
					}
				}
			}
		}
		report.ProcessedSessions = end
		if !options.DryRun {
			checkpoint.Next = end
			if err := writeCheckpoint(options.CheckpointPath, checkpoint); err != nil {
				return report, err
			}
		}
		if options.Progress != nil {
			options.Progress(report)
		}
	}
	destinationHash := sha256.New()
	if !options.DryRun {
		for _, session := range sessions {
			events, memory, err := readSessionData(ctx, destination, options.TenantID, session)
			if err != nil {
				return report, err
			}
			destinationHash.Write([]byte(migrationChecksum(events, memory)))
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
	if !options.DryRun && report.Checksum != hex.EncodeToString(destinationHash.Sum(nil)) {
		report.Status = "failed"
		report.Message = "source and destination content differ"
		return report, errors.New(report.Message)
	}
	report.Status = "completed"
	return report, nil
}
func readSessionData(ctx context.Context, store DataStore, tenant, session string) ([]SessionEvent, []MemoryRecord, error) {
	events, err := store.ListSessionEvents(ctx, tenant, session, 0)
	if err != nil {
		return nil, nil, err
	}
	memory, err := store.ListMemory(ctx, tenant, session)
	return events, memory, err
}
func retry(ctx context.Context, max int, fn func() error) error {
	var err error
	for i := 0; i < max; i++ {
		if err = fn(); err == nil {
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
	set := map[string]struct{}{}
	prefix := tenant + "\x00"
	for key := range s.events {
		if strings.HasPrefix(key, prefix) {
			set[strings.TrimPrefix(key, prefix)] = struct{}{}
		}
	}
	for key := range s.memory {
		if strings.HasPrefix(key, prefix) {
			remainder := strings.TrimPrefix(key, prefix)
			if index := strings.IndexByte(remainder, '\x00'); index >= 0 {
				set[remainder[:index]] = struct{}{}
			}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
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
