package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const ControlPlaneSchemaVersion = 1

type persistedVersionCreation struct {
	TenantID       string            `json:"tenant_id"`
	DeploymentID   string            `json:"deployment_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	Config         string            `json:"config"`
	Version        DeploymentVersion `json:"version"`
}

type controlPlaneSnapshot struct {
	Tenants            map[string]Tenant              `json:"tenants"`
	Apps               map[string]AgentApp            `json:"agent_apps"`
	Deployments        map[string]Deployment          `json:"deployments"`
	Versions           map[string][]DeploymentVersion `json:"deployment_versions"`
	VersionCreations   []persistedVersionCreation     `json:"version_creations"`
	ChannelBindings    map[string]ChannelBinding      `json:"channel_bindings"`
	BackendSelections  map[string]backendSelection    `json:"backend_selections,omitempty"`
	GovernancePolicies map[string]TenantPolicy        `json:"governance_policies,omitempty"`
}

type controlPlanePersistence interface {
	Load() (controlPlaneSnapshot, int64, error)
	Save(controlPlaneSnapshot, int64) (int64, error)
	Close() error
}

type sqliteControlPlanePersistence struct{ db *sql.DB }
type postgresControlPlanePersistence struct{ db *sql.DB }

// MigrateSQLiteControlPlane applies forward-only control-plane migrations.
func MigrateSQLiteControlPlane(path string) error {
	if path == "" {
		return errors.New("control plane SQLite path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create control plane directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open control plane: %w", err)
	}
	defer db.Close()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS control_plane_schema (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), version INTEGER NOT NULL)`,
		`INSERT INTO control_plane_schema(singleton, version) VALUES (1, 1) ON CONFLICT(singleton) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS control_plane_state (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), revision INTEGER NOT NULL, payload BLOB NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("migrate control plane: %w", err)
		}
	}
	return nil
}

func MigratePostgresControlPlane(dsn string) error {
	if dsn == "" {
		return errors.New("control plane PostgreSQL DSN is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open control plane: %w", err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS control_plane_schema (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), version INTEGER NOT NULL)`,
		`INSERT INTO control_plane_schema(singleton, version) VALUES (1, 1) ON CONFLICT(singleton) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS control_plane_state (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), revision BIGINT NOT NULL, payload BYTEA NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS session_execution_leases (tenant_id TEXT NOT NULL, session_id TEXT NOT NULL, owner_id TEXT NOT NULL, fencing_token BIGINT NOT NULL, expires_at TIMESTAMPTZ NOT NULL, PRIMARY KEY (tenant_id, session_id))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("migrate control plane: %w", err)
		}
	}
	return nil
}

// NewSQLiteControlPlane opens an already-migrated development Control Plane
// Store. Service startup deliberately does not migrate the schema implicitly.
func NewSQLiteControlPlane(path string) (ControlPlaneStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open control plane: %w", err)
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM control_plane_schema WHERE singleton = 1`).Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("control plane schema is not initialized: %w", err)
	}
	if version != ControlPlaneSchemaVersion {
		db.Close()
		return nil, fmt.Errorf("control plane schema version %d is incompatible with required version %d", version, ControlPlaneSchemaVersion)
	}
	platform := NewMemoryPlatform()
	persistence := &sqliteControlPlanePersistence{db: db}
	snapshot, revision, err := persistence.Load()
	if err != nil {
		db.Close()
		return nil, err
	}
	applyControlPlaneSnapshot(platform, snapshot)
	platform.persistence = persistence
	platform.persistenceRevision = revision
	return platform, nil
}

func NewPostgresControlPlane(dsn string) (ControlPlaneStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open control plane: %w", err)
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM control_plane_schema WHERE singleton = 1`).Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("control plane schema is not initialized: %w", err)
	}
	if version != ControlPlaneSchemaVersion {
		db.Close()
		return nil, fmt.Errorf("control plane schema version %d is incompatible with required version %d", version, ControlPlaneSchemaVersion)
	}
	persistence := &postgresControlPlanePersistence{db: db}
	return loadPersistentControlPlane(persistence)
}

func loadPersistentControlPlane(persistence controlPlanePersistence) (ControlPlaneStore, error) {
	platform := NewMemoryPlatform()
	snapshot, revision, err := persistence.Load()
	if err != nil {
		persistence.Close()
		return nil, err
	}
	applyControlPlaneSnapshot(platform, snapshot)
	platform.persistence = persistence
	platform.persistenceRevision = revision
	return platform, nil
}

func (p *sqliteControlPlanePersistence) Load() (controlPlaneSnapshot, int64, error) {
	var payload []byte
	var revision int64
	err := p.db.QueryRow(`SELECT revision, payload FROM control_plane_state WHERE singleton = 1`).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return controlPlaneSnapshot{}, 0, nil
	}
	if err != nil {
		return controlPlaneSnapshot{}, 0, fmt.Errorf("load control plane: %w", err)
	}
	var snapshot controlPlaneSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return controlPlaneSnapshot{}, 0, fmt.Errorf("decode control plane: %w", err)
	}
	return snapshot, revision, nil
}

func (p *sqliteControlPlanePersistence) Save(snapshot controlPlaneSnapshot, _ int64) (int64, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return 0, fmt.Errorf("encode control plane: %w", err)
	}
	_, err = p.db.ExecContext(context.Background(), `INSERT INTO control_plane_state(singleton, revision, payload) VALUES (1, 1, ?)
		ON CONFLICT(singleton) DO UPDATE SET revision = control_plane_state.revision + 1, payload = excluded.payload`, payload)
	if err != nil {
		return 0, fmt.Errorf("save control plane: %w", err)
	}
	var revision int64
	if err := p.db.QueryRow(`SELECT revision FROM control_plane_state WHERE singleton = 1`).Scan(&revision); err != nil {
		return 0, fmt.Errorf("read control plane revision: %w", err)
	}
	return revision, nil
}

func (p *sqliteControlPlanePersistence) Close() error { return p.db.Close() }

func (p *postgresControlPlanePersistence) Load() (controlPlaneSnapshot, int64, error) {
	var payload []byte
	var revision int64
	err := p.db.QueryRow(`SELECT revision, payload FROM control_plane_state WHERE singleton = 1`).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return controlPlaneSnapshot{}, 0, nil
	}
	if err != nil {
		return controlPlaneSnapshot{}, 0, fmt.Errorf("load control plane: %w", err)
	}
	var snapshot controlPlaneSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return controlPlaneSnapshot{}, 0, fmt.Errorf("decode control plane: %w", err)
	}
	return snapshot, revision, nil
}

func (p *postgresControlPlanePersistence) Save(snapshot controlPlaneSnapshot, expectedRevision int64) (int64, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return 0, fmt.Errorf("encode control plane: %w", err)
	}
	tx, err := p.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(83726104)`); err != nil {
		return 0, err
	}
	var current int64
	err = tx.QueryRow(`SELECT revision FROM control_plane_state WHERE singleton = 1 FOR UPDATE`).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = 0
	} else if err != nil {
		return 0, err
	}
	if current != expectedRevision {
		return 0, errors.New("control plane revision changed")
	}
	next := current + 1
	if _, err := tx.Exec(`INSERT INTO control_plane_state(singleton, revision, payload) VALUES (1, $1, $2)
		ON CONFLICT(singleton) DO UPDATE SET revision = excluded.revision, payload = excluded.payload`, next, payload); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func (p *postgresControlPlanePersistence) Close() error { return p.db.Close() }

func controlPlaneSnapshotFrom(platform *MemoryPlatform) controlPlaneSnapshot {
	snapshot := controlPlaneSnapshot{
		Tenants: platform.tenants, Apps: platform.apps, Deployments: platform.deployments,
		Versions:           platform.versions,
		ChannelBindings:    platform.channelBindings,
		BackendSelections:  platform.backendSelections,
		GovernancePolicies: platform.governancePolicies,
		VersionCreations:   make([]persistedVersionCreation, 0, len(platform.versionCreations)),
	}
	for key, creation := range platform.versionCreations {
		snapshot.VersionCreations = append(snapshot.VersionCreations, persistedVersionCreation{
			TenantID: key.tenantID, DeploymentID: key.deploymentID, IdempotencyKey: key.idempotencyKey,
			Config: creation.config, Version: creation.version,
		})
	}
	return snapshot
}

func applyControlPlaneSnapshot(platform *MemoryPlatform, snapshot controlPlaneSnapshot) {
	if snapshot.Tenants != nil {
		platform.tenants = snapshot.Tenants
	}
	if snapshot.Apps != nil {
		platform.apps = snapshot.Apps
	}
	if snapshot.Deployments != nil {
		platform.deployments = snapshot.Deployments
	}
	if snapshot.Versions != nil {
		platform.versions = snapshot.Versions
	}
	if snapshot.ChannelBindings != nil {
		platform.channelBindings = snapshot.ChannelBindings
	}
	if snapshot.BackendSelections != nil {
		platform.backendSelections = snapshot.BackendSelections
	}
	if snapshot.GovernancePolicies != nil {
		platform.governancePolicies = snapshot.GovernancePolicies
	}
	for _, creation := range snapshot.VersionCreations {
		key := versionCreationKey{tenantID: creation.TenantID, deploymentID: creation.DeploymentID, idempotencyKey: creation.IdempotencyKey}
		platform.versionCreations[key] = versionCreation{config: creation.Config, version: creation.Version}
	}
}
