package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"provision/internal/planner"

	_ "modernc.org/sqlite"
)

const SchemaVersion = "provision.dev/state/sqlite/v1alpha1"

type sqliteBackend struct {
	db *sql.DB
}

var _ Backend = (*sqliteBackend)(nil)

type sqliteOpenMode uint8

const (
	sqliteOpenOrCreate sqliteOpenMode = iota
	sqliteOpenReadOnly
	sqliteOpenForUpdate
)

func OpenSQLite(path string) (Backend, error) {
	return openSQLite(path, sqliteOpenOrCreate)
}

func OpenExistingSQLite(path string) (Backend, error) {
	return openSQLite(path, sqliteOpenReadOnly)
}

func OpenExistingSQLiteForUpdate(path string) (Backend, error) {
	return openSQLite(path, sqliteOpenForUpdate)
}

func openSQLite(path string, mode sqliteOpenMode) (*sqliteBackend, error) {
	if path == "" {
		return nil, errors.New("State Backend path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve State Backend path: %w", err)
	}
	parent, err := os.Stat(filepath.Dir(absolute))
	if err != nil || !parent.IsDir() {
		return nil, errors.New("State Backend parent directory must already exist")
	}
	created := false
	if info, err := os.Lstat(absolute); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("State Backend must be a regular file, not a symlink")
		}
		if info.Mode().Perm() != 0600 {
			return nil, errors.New("State Backend permissions must be 0600")
		}
	} else if errors.Is(err, os.ErrNotExist) && mode != sqliteOpenOrCreate {
		return nil, errors.New("State Backend does not exist")
	} else if errors.Is(err, os.ErrNotExist) {
		file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("create State Backend securely: %w", err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(absolute)
			return nil, fmt.Errorf("close new State Backend: %w", err)
		}
		created = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect State Backend: %w", err)
	}
	accessMode := "rw"
	if mode == sqliteOpenReadOnly {
		accessMode = "ro"
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=" + accessMode}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		if created {
			_ = os.Remove(absolute)
		}
		return nil, fmt.Errorf("open State Backend: %w", err)
	}
	db.SetMaxOpenConns(1)
	backend := &sqliteBackend{db: db}
	if mode == sqliteOpenOrCreate {
		err = backend.initialize(context.Background())
	} else {
		err = backend.validateSchema(context.Background())
	}
	if err != nil {
		_ = db.Close()
		if created {
			_ = os.Remove(absolute)
		}
		return nil, err
	}
	return backend, nil
}

func (b *sqliteBackend) Close() error { return b.db.Close() }

func (b *sqliteBackend) initialize(ctx context.Context) error {
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS state_metadata (
            singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
            schema_version TEXT NOT NULL
        ) STRICT`,
		`INSERT INTO state_metadata(singleton, schema_version) VALUES (1, '` + SchemaVersion + `')
         ON CONFLICT(singleton) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS plans (
            id TEXT PRIMARY KEY,
            schema_version TEXT NOT NULL,
            application TEXT NOT NULL,
            environment TEXT NOT NULL,
            configuration_digest TEXT NOT NULL,
            observation_digest TEXT NOT NULL,
            plan_json BLOB NOT NULL,
            stored_at TEXT NOT NULL
        ) STRICT`,
		`CREATE TABLE IF NOT EXISTS approval_decisions (
            sequence INTEGER PRIMARY KEY AUTOINCREMENT,
            plan_id TEXT NOT NULL REFERENCES plans(id),
            actor TEXT NOT NULL,
            decision TEXT NOT NULL CHECK (decision = 'approved'),
            decided_at TEXT NOT NULL,
            expires_at TEXT NOT NULL
        ) STRICT`,
		`CREATE TABLE IF NOT EXISTS environment_heads (
            application TEXT NOT NULL,
            environment TEXT NOT NULL,
            plan_id TEXT NOT NULL REFERENCES plans(id),
            updated_at TEXT NOT NULL,
            PRIMARY KEY (application, environment)
        ) STRICT`,
	} {
		if _, err := b.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize State Backend: %w", err)
		}
	}
	return b.validateSchema(ctx)
}

func (b *sqliteBackend) validateSchema(ctx context.Context) error {
	var version string
	if err := b.db.QueryRowContext(ctx, `SELECT schema_version FROM state_metadata WHERE singleton = 1`).Scan(&version); err != nil {
		return fmt.Errorf("read State Backend schema: %w", err)
	}
	if version != SchemaVersion {
		return fmt.Errorf("State Backend schema %q is unsupported", version)
	}
	return nil
}

func (b *sqliteBackend) StoreCurrentPlan(ctx context.Context, plan planner.Plan, storedAt time.Time) error {
	if err := plan.VerifyIdentity(); err != nil {
		return err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("encode Plan: %w", err)
	}
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin Plan storage: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO plans(id, schema_version, application, environment, configuration_digest, observation_digest, plan_json, stored_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		plan.ID, plan.SchemaVersion, plan.Application, plan.Environment, plan.ConfigurationDigest, plan.ObservationDigest, encoded, formatTime(storedAt)); err != nil {
		return fmt.Errorf("store Plan: %w", err)
	}
	var stored []byte
	if err := tx.QueryRowContext(ctx, `SELECT plan_json FROM plans WHERE id = ?`, plan.ID).Scan(&stored); err != nil {
		return fmt.Errorf("read stored Plan: %w", err)
	}
	if !bytes.Equal(stored, encoded) {
		return errors.New("stored Plan identity has different canonical bytes")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO environment_heads(application, environment, plan_id, updated_at) VALUES (?, ?, ?, ?)
        ON CONFLICT(application, environment) DO UPDATE SET plan_id = excluded.plan_id, updated_at = excluded.updated_at`,
		plan.Application, plan.Environment, plan.ID, formatTime(storedAt)); err != nil {
		return fmt.Errorf("advance Environment Plan head: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Plan storage: %w", err)
	}
	return nil
}

func (b *sqliteBackend) RecordApproval(ctx context.Context, planID string, approval ApprovalRecord) error {
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin approval storage: %w", err)
	}
	defer tx.Rollback()
	var application, environment string
	if err := tx.QueryRowContext(ctx, `SELECT application, environment FROM plans WHERE id = ?`, planID).Scan(&application, &environment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("Plan is not present in the State Backend")
		}
		return fmt.Errorf("read Plan for approval: %w", err)
	}
	var currentPlanID string
	if err := tx.QueryRowContext(ctx, `SELECT plan_id FROM environment_heads WHERE application = ? AND environment = ?`, application, environment).Scan(&currentPlanID); err != nil {
		return fmt.Errorf("read Environment Plan head: %w", err)
	}
	if currentPlanID != planID {
		return errors.New("Plan is superseded and cannot be approved")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approval_decisions(plan_id, actor, decision, decided_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		planID, approval.Actor, approval.Decision, formatTime(approval.DecidedAt), formatTime(approval.ExpiresAt)); err != nil {
		return fmt.Errorf("record approval decision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit approval storage: %w", err)
	}
	return nil
}

func (b *sqliteBackend) LoadPlanSnapshot(ctx context.Context, planID string) (PlanSnapshot, error) {
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("begin Plan snapshot: %w", err)
	}
	defer tx.Rollback()
	var encoded []byte
	var application, environment string
	if err := tx.QueryRowContext(ctx, `SELECT plan_json, application, environment FROM plans WHERE id = ?`, planID).Scan(&encoded, &application, &environment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlanSnapshot{}, errors.New("Plan is not present in the State Backend")
		}
		return PlanSnapshot{}, fmt.Errorf("read Plan: %w", err)
	}
	var plan planner.Plan
	if err := json.Unmarshal(encoded, &plan); err != nil || plan.VerifyIdentity() != nil {
		return PlanSnapshot{}, errors.New("stored Plan is invalid")
	}
	snapshot := PlanSnapshot{Plan: plan}
	if err := tx.QueryRowContext(ctx, `SELECT plan_id FROM environment_heads WHERE application = ? AND environment = ?`, application, environment).Scan(&snapshot.CurrentPlanID); err != nil {
		return PlanSnapshot{}, fmt.Errorf("read Environment Plan head: %w", err)
	}
	var approval ApprovalRecord
	var decision, decidedAt, expiresAt string
	err = tx.QueryRowContext(ctx, `SELECT actor, decision, decided_at, expires_at FROM approval_decisions WHERE plan_id = ? ORDER BY sequence DESC LIMIT 1`, planID).
		Scan(&approval.Actor, &decision, &decidedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return PlanSnapshot{}, fmt.Errorf("complete Plan snapshot: %w", err)
		}
		return snapshot, nil
	}
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("read approval decision: %w", err)
	}
	approval.Decision = ApprovalDecision(decision)
	approval.DecidedAt, err = parseTime(decidedAt)
	if err != nil {
		return PlanSnapshot{}, err
	}
	approval.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return PlanSnapshot{}, err
	}
	snapshot.Approval = &approval
	if err := tx.Commit(); err != nil {
		return PlanSnapshot{}, fmt.Errorf("complete Plan snapshot: %w", err)
	}
	return snapshot, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("stored approval time is invalid")
	}
	return parsed, nil
}
