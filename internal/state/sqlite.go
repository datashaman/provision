package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"provision/internal/planner"

	_ "modernc.org/sqlite"
)

const (
	SchemaVersion         = "provision.dev/state/sqlite/v1alpha2"
	previousSchemaVersion = "provision.dev/state/sqlite/v1alpha1"
)

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
	if mode == sqliteOpenOrCreate || mode == sqliteOpenForUpdate {
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
	for _, statement := range []string{`PRAGMA foreign_keys = ON`, `PRAGMA busy_timeout = 5000`} {
		if _, err := b.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize State Backend: %w", err)
		}
	}
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin State Backend migration: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS state_metadata (
            singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
            schema_version TEXT NOT NULL
        ) STRICT`,
		`INSERT INTO state_metadata(singleton, schema_version) VALUES (1, '` + SchemaVersion + `')
         ON CONFLICT(singleton) DO NOTHING`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize State Backend metadata: %w", err)
		}
	}
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT schema_version FROM state_metadata WHERE singleton = 1`).Scan(&version); err != nil {
		return fmt.Errorf("read State Backend schema for migration: %w", err)
	}
	if version != SchemaVersion && version != previousSchemaVersion {
		return fmt.Errorf("State Backend schema %q is unsupported", version)
	}
	for _, statement := range []string{
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
		`CREATE TABLE IF NOT EXISTS environment_leases (
            application TEXT NOT NULL,
            environment TEXT NOT NULL,
            fencing_token INTEGER NOT NULL,
            holder TEXT NOT NULL,
            plan_id TEXT NOT NULL REFERENCES plans(id),
            operation_id TEXT NOT NULL,
            attempt_id TEXT NOT NULL,
            acquired_at TEXT NOT NULL,
            expires_at TEXT NOT NULL,
            released_at TEXT,
            PRIMARY KEY (application, environment)
        ) STRICT`,
		`CREATE TABLE IF NOT EXISTS journal_events (
            sequence INTEGER PRIMARY KEY AUTOINCREMENT,
            schema_version TEXT NOT NULL,
            application TEXT NOT NULL,
            environment TEXT NOT NULL,
            plan_id TEXT NOT NULL REFERENCES plans(id),
            operation_id TEXT NOT NULL,
            attempt_id TEXT NOT NULL,
            fencing_token INTEGER NOT NULL,
            kind TEXT NOT NULL CHECK (kind IN ('intent', 'outcome')),
            outcome TEXT NOT NULL CHECK (outcome IN ('', 'succeeded', 'failed', 'uncertain')),
            observation_json BLOB NOT NULL,
            occurred_at TEXT NOT NULL,
            UNIQUE (attempt_id, kind)
        ) STRICT`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize State Backend: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE state_metadata SET schema_version = ? WHERE singleton = 1 AND schema_version = ?`, SchemaVersion, previousSchemaVersion); err != nil {
		return fmt.Errorf("migrate State Backend schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit State Backend migration: %w", err)
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

func (b *sqliteBackend) BeginOperation(ctx context.Context, request BeginOperationRequest) (OperationAttempt, error) {
	request.StartedAt = request.StartedAt.UTC()
	if request.PlanID == "" || request.OperationID == "" || request.Holder == "" || request.Holder != strings.TrimSpace(request.Holder) || len(request.Holder) > 128 {
		return OperationAttempt{}, errors.New("operation identity and lease holder are required")
	}
	if request.LeaseDuration <= 0 || request.LeaseDuration > 5*time.Minute {
		return OperationAttempt{}, errors.New("operation lease duration must be between zero and five minutes")
	}
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return OperationAttempt{}, fmt.Errorf("begin operation: %w", err)
	}
	defer tx.Rollback()

	var encoded []byte
	var application, environment string
	if err := tx.QueryRowContext(ctx, `SELECT plan_json, application, environment FROM plans WHERE id = ?`, request.PlanID).Scan(&encoded, &application, &environment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OperationAttempt{}, errors.New("Plan is not present in the State Backend")
		}
		return OperationAttempt{}, fmt.Errorf("read Plan for operation: %w", err)
	}
	var plan planner.Plan
	if err := json.Unmarshal(encoded, &plan); err != nil || plan.VerifyIdentity() != nil {
		return OperationAttempt{}, errors.New("stored Plan is invalid")
	}
	var currentPlanID string
	if err := tx.QueryRowContext(ctx, `SELECT plan_id FROM environment_heads WHERE application = ? AND environment = ?`, application, environment).Scan(&currentPlanID); err != nil {
		return OperationAttempt{}, fmt.Errorf("read Environment Plan head: %w", err)
	}
	if currentPlanID != request.PlanID {
		return OperationAttempt{}, errors.New("Plan is superseded and cannot authorize execution")
	}
	var decision, approvalExpiry string
	if err := tx.QueryRowContext(ctx, `SELECT decision, expires_at FROM approval_decisions WHERE plan_id = ? ORDER BY sequence DESC LIMIT 1`, request.PlanID).Scan(&decision, &approvalExpiry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OperationAttempt{}, errors.New("Plan is unapproved and cannot authorize execution")
		}
		return OperationAttempt{}, fmt.Errorf("read execution approval: %w", err)
	}
	expiresAt, err := parseTime(approvalExpiry)
	if err != nil || decision != string(DecisionApproved) || !expiresAt.After(request.StartedAt) {
		return OperationAttempt{}, errors.New("Plan approval is not eligible for execution")
	}
	operation, ok := plannedOperation(plan, request.OperationID)
	if !ok {
		return OperationAttempt{}, errors.New("operation is not present in the approved Plan")
	}
	if operation.Kind != planner.StageArtifact || len(operation.DependsOn) != 0 {
		return OperationAttempt{}, errors.New("only the dependency-free Artifact preparation operation is enabled")
	}

	var priorToken int64
	var priorExpiry string
	var priorReleased sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT fencing_token, expires_at, released_at FROM environment_leases WHERE application = ? AND environment = ?`, application, environment).
		Scan(&priorToken, &priorExpiry, &priorReleased)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return OperationAttempt{}, fmt.Errorf("read Environment lease: %w", err)
	}
	if err == nil && !priorReleased.Valid {
		parsedExpiry, parseErr := parseTime(priorExpiry)
		if parseErr != nil {
			return OperationAttempt{}, parseErr
		}
		if parsedExpiry.After(request.StartedAt) {
			return OperationAttempt{}, errors.New("Environment already has an active mutating operation")
		}
	}
	token := priorToken + 1
	leaseExpiresAt := request.StartedAt.Add(request.LeaseDuration)
	if expiresAt.Before(leaseExpiresAt) {
		leaseExpiresAt = expiresAt
	}
	attemptID := operationAttemptID(request.PlanID, request.OperationID, request.Holder, token)
	if _, err := tx.ExecContext(ctx, `INSERT INTO environment_leases(application, environment, fencing_token, holder, plan_id, operation_id, attempt_id, acquired_at, expires_at, released_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
        ON CONFLICT(application, environment) DO UPDATE SET
            fencing_token = excluded.fencing_token,
            holder = excluded.holder,
            plan_id = excluded.plan_id,
            operation_id = excluded.operation_id,
            attempt_id = excluded.attempt_id,
            acquired_at = excluded.acquired_at,
            expires_at = excluded.expires_at,
            released_at = NULL`,
		application, environment, token, request.Holder, request.PlanID, request.OperationID, attemptID, formatTime(request.StartedAt), formatTime(leaseExpiresAt)); err != nil {
		return OperationAttempt{}, fmt.Errorf("acquire Environment lease: %w", err)
	}
	operationDigest, err := planner.OperationDigest(operation)
	if err != nil {
		return OperationAttempt{}, err
	}
	intent, err := json.Marshal(struct {
		OperationDigest string         `json:"operationDigest"`
		Target          planner.Target `json:"target"`
	}{operationDigest, plan.Target})
	if err != nil {
		return OperationAttempt{}, fmt.Errorf("encode operation intent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO journal_events(schema_version, application, environment, plan_id, operation_id, attempt_id, fencing_token, kind, outcome, observation_json, occurred_at)
        VALUES ('provision.dev/journal-event/v1alpha1', ?, ?, ?, ?, ?, ?, 'intent', '', ?, ?)`,
		application, environment, request.PlanID, request.OperationID, attemptID, token, intent, formatTime(request.StartedAt)); err != nil {
		return OperationAttempt{}, fmt.Errorf("journal operation intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OperationAttempt{}, fmt.Errorf("commit operation intent: %w", err)
	}
	return OperationAttempt{
		AttemptID:      attemptID,
		Holder:         request.Holder,
		FencingToken:   token,
		StartedAt:      request.StartedAt,
		LeaseExpiresAt: leaseExpiresAt,
		Plan:           plan,
		Operation:      operation,
	}, nil
}

func (b *sqliteBackend) CompleteOperation(ctx context.Context, request CompleteOperationRequest) error {
	request.CompletedAt = request.CompletedAt.UTC()
	if request.Outcome != ExecutionSucceeded && request.Outcome != ExecutionFailed && request.Outcome != ExecutionUncertain {
		return errors.New("operation outcome is invalid")
	}
	if len(request.Observation) == 0 || len(request.Observation) > 1<<20 || !json.Valid(request.Observation) {
		return errors.New("operation observation must be valid bounded JSON")
	}
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin operation completion: %w", err)
	}
	defer tx.Rollback()
	var application, environment string
	if err := tx.QueryRowContext(ctx, `SELECT application, environment FROM plans WHERE id = ?`, request.PlanID).Scan(&application, &environment); err != nil {
		return errors.New("operation Plan is not present in the State Backend")
	}
	var token int64
	var holder, planID, operationID, attemptID, expiresAt string
	var releasedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT fencing_token, holder, plan_id, operation_id, attempt_id, expires_at, released_at FROM environment_leases WHERE application = ? AND environment = ?`, application, environment).
		Scan(&token, &holder, &planID, &operationID, &attemptID, &expiresAt, &releasedAt); err != nil {
		return fmt.Errorf("read Environment lease for completion: %w", err)
	}
	leaseExpiry, err := parseTime(expiresAt)
	if err != nil {
		return err
	}
	if token != request.FencingToken || holder != request.Holder || planID != request.PlanID || operationID != request.OperationID || attemptID != request.AttemptID || releasedAt.Valid || !leaseExpiry.After(request.CompletedAt) {
		return errors.New("operation lease is stale or does not match its fencing authority")
	}
	var currentPlanID string
	if err := tx.QueryRowContext(ctx, `SELECT plan_id FROM environment_heads WHERE application = ? AND environment = ?`, application, environment).Scan(&currentPlanID); err != nil || currentPlanID != request.PlanID {
		return errors.New("operation Plan is no longer current")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO journal_events(schema_version, application, environment, plan_id, operation_id, attempt_id, fencing_token, kind, outcome, observation_json, occurred_at)
        VALUES ('provision.dev/journal-event/v1alpha1', ?, ?, ?, ?, ?, ?, 'outcome', ?, ?, ?)`,
		application, environment, request.PlanID, request.OperationID, request.AttemptID, request.FencingToken, request.Outcome, []byte(request.Observation), formatTime(request.CompletedAt)); err != nil {
		return fmt.Errorf("journal operation outcome: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE environment_leases SET released_at = ? WHERE application = ? AND environment = ? AND fencing_token = ? AND released_at IS NULL`,
		formatTime(request.CompletedAt), application, environment, request.FencingToken)
	if err != nil {
		return fmt.Errorf("release Environment lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("operation lease changed before release")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit operation outcome: %w", err)
	}
	return nil
}

func (b *sqliteBackend) LoadJournal(ctx context.Context, planID string) ([]JournalEvent, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT sequence, schema_version, application, environment, plan_id, operation_id, attempt_id, fencing_token, kind, outcome, observation_json, occurred_at
        FROM journal_events WHERE plan_id = ? ORDER BY sequence`, planID)
	if err != nil {
		return nil, fmt.Errorf("read execution journal: %w", err)
	}
	defer rows.Close()
	events := []JournalEvent{}
	for rows.Next() {
		var event JournalEvent
		var kind, outcome, occurredAt string
		if err := rows.Scan(&event.Sequence, &event.SchemaVersion, &event.Application, &event.Environment, &event.PlanID, &event.OperationID, &event.AttemptID, &event.FencingToken, &kind, &outcome, &event.Observation, &occurredAt); err != nil {
			return nil, fmt.Errorf("decode execution journal: %w", err)
		}
		event.Kind = JournalEventKind(kind)
		event.Outcome = ExecutionOutcome(outcome)
		event.OccurredAt, err = parseTime(occurredAt)
		if err != nil || !json.Valid(event.Observation) {
			return nil, errors.New("stored execution journal event is invalid")
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read execution journal: %w", err)
	}
	return events, nil
}

func plannedOperation(plan planner.Plan, operationID string) (planner.Operation, bool) {
	for _, operation := range plan.Operations {
		if operation.ID == operationID {
			return operation, true
		}
	}
	return planner.Operation{}, false
}

func operationAttemptID(planID, operationID, holder string, token int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%s\n%d", planID, operationID, holder, token)))
	return "attempt-" + hex.EncodeToString(digest[:16])
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("stored approval time is invalid")
	}
	return parsed, nil
}
