# Stage 2 Storage And Migration

Stage 2 adds immutable tenant-scoped Session Events, replayed Session/Summary state, Memory records, and selectable InMemory, Redis, SQLite, and PostgreSQL-compatible storage implementations. Routed executions persist input and output/failure events before they are exposed in the Management Console.

## Local Profiles

Start Redis and PostgreSQL:

```bash
docker compose -f compose.stage2.yml up -d
TRPC_TEST_REDIS_ADDR=127.0.0.1:6379 \
TRPC_TEST_POSTGRES_DSN='postgres://trpc:trpc@127.0.0.1:5432/trpc_agent?sslmode=disable' \
go test ./trpcservice/platform
```

Backend selection is tenant-scoped and server-authorized. Configure the selectable catalog with `TRPC_REDIS_ADDR` and `TRPC_SQLITE_PATH`, and set `TRPC_BACKEND_SELECTIONS` to a writable JSON control-plane file when selection must survive service restart. Clients choose only server-defined backend IDs; addresses and paths are never accepted or returned by the API.

Migration endpoints are server-owned. Configure `TRPC_MIGRATION_REDIS_ADDR`, `TRPC_MIGRATION_SQLITE_PATH`, and `TRPC_MIGRATION_CHECKPOINT_PATH`; tenant administrators can start and inspect jobs but cannot submit network addresses or filesystem paths.

## Redis-To-SQL Migration

Dry-run and execute with the same arguments:

```bash
go run ./cmd/storage-migrate -tenant tenant-dev -redis 127.0.0.1:6379 -sqlite data/stage2.db -dry-run
go run ./cmd/storage-migrate -tenant tenant-dev -redis 127.0.0.1:6379 -sqlite data/stage2.db
```

The command migrates in deterministic Session order, retries transient writes, persists an atomic checkpoint, resumes after interruption, and validates destination record counts and event-content checksums. Replays are idempotent because destination events retain their source idempotency keys.

## Contracts And Limits

- Event identity is scoped to Tenant, Session, and idempotency key. A replay with different content is rejected.
- Sequences are allocated atomically per Tenant and Session by Redis transactions or SQL constraints.
- Session and Summary state is rebuilt from immutable events; events cannot be updated through the API.
- Arbitrary SQL, destructive event editing, vector/object vendor migration, and online provider cutover are excluded.
- Redis and PostgreSQL integration profiles require the explicitly documented environment variables; local unit tests use deterministic isolated providers.
