package platform

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestRedisToSQLMigrationDryRunAndRepeat(t *testing.T) {
	server := miniredis.RunT(t)
	source := NewRedisStore(server.Addr())
	defer source.Close()
	destination, err := NewSQLiteStore(filepath.Join(t.TempDir(), "destination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	ctx := context.Background()
	if err := source.AppendSessionEvent(ctx, SessionEvent{TenantID: "tenant-a", SessionID: "session-a", IdempotencyKey: "one", Type: "message", Payload: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if err := source.PutMemory(ctx, MemoryRecord{TenantID: "tenant-a", SessionID: "session-a", Key: "name", Value: "Ada"}); err != nil {
		t.Fatal(err)
	}
	dry, err := MigrateRedisToSQL(ctx, source, destination, MigrationOptions{TenantID: "tenant-a", DryRun: true})
	if err != nil || dry.SourceCount != 2 || dry.DestinationCount != 2 {
		t.Fatalf("dry=%#v err=%v", dry, err)
	}
	checkpoint := filepath.Join(t.TempDir(), "checkpoint.json")
	report, err := MigrateRedisToSQL(ctx, source, destination, MigrationOptions{TenantID: "tenant-a", CheckpointPath: checkpoint})
	if err != nil || report.Status != "completed" || report.SourceCount != report.DestinationCount {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	repeated, err := MigrateRedisToSQL(ctx, source, destination, MigrationOptions{TenantID: "tenant-a", CheckpointPath: checkpoint})
	if err != nil || repeated.DestinationCount != 2 {
		t.Fatalf("repeated=%#v err=%v", repeated, err)
	}
}
