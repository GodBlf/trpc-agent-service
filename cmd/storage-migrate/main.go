package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/liuzengh/trpc-agent-service/trpcservice/platform"
)

func main() {
	redisAddress := flag.String("redis", "127.0.0.1:6379", "Redis address or URL")
	sqlitePath := flag.String("sqlite", "data/stage2.db", "SQLite destination path")
	tenant := flag.String("tenant", "", "tenant to migrate")
	checkpoint := flag.String("checkpoint", "data/stage2-migration.checkpoint", "resume checkpoint path")
	batch := flag.Int("batch-size", 100, "sessions per checkpoint batch")
	dryRun := flag.Bool("dry-run", false, "validate without writing")
	flag.Parse()
	if *tenant == "" {
		fmt.Fprintln(os.Stderr, "error: -tenant is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	source := platform.NewRedisStore(*redisAddress)
	defer source.Close()
	destination, err := platform.NewSQLiteStore(*sqlitePath)
	if err != nil {
		fatal(err)
	}
	defer destination.Close()
	report, err := platform.MigrateRedisToSQL(ctx, source, destination, platform.MigrationOptions{TenantID: *tenant, DryRun: *dryRun, BatchSize: *batch, CheckpointPath: *checkpoint})
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		fatal(err)
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
