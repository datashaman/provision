package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"provision/internal/host"
)

const SchemaVersion = "provision.dev/schedule-ledger/v1alpha1"

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("schedule ledger requires an absolute path")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func OpenReadOnly(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("schedule ledger requires an absolute path")
	}
	values := url.Values{"mode": []string{"ro"}, "immutable": []string{"1"}}
	db, err := sql.Open("sqlite", "file:"+path+"?"+values.Encode())
	if err != nil {
		return nil, err
	}
	var schema string
	if err := db.QueryRow(`SELECT value FROM metadata WHERE key = 'schemaVersion'`).Scan(&schema); err != nil || schema != SchemaVersion {
		db.Close()
		return nil, errors.New("schedule ledger schema is unsupported")
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS metadata (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
INSERT OR IGNORE INTO metadata(key, value) VALUES ('schemaVersion', '`+SchemaVersion+`');
CREATE TABLE IF NOT EXISTS occurrences (
  id TEXT PRIMARY KEY,
  schedule TEXT NOT NULL,
  due_at TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  task_generation_id TEXT NOT NULL,
  fencing_token INTEGER NOT NULL,
  invocation_id TEXT NOT NULL UNIQUE,
  disposition TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS invocations (
  id TEXT PRIMARY KEY,
  task TEXT NOT NULL,
  task_generation_id TEXT NOT NULL,
  application_revision TEXT NOT NULL,
  configuration_digest TEXT NOT NULL,
  trigger_kind TEXT NOT NULL,
  created_at TEXT NOT NULL,
  outcome TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS attempts (
  invocation_id TEXT NOT NULL,
  number INTEGER NOT NULL,
  systemd_unit TEXT NOT NULL,
  started_at TEXT NOT NULL,
  completed_at TEXT,
  outcome TEXT NOT NULL,
  evidence_path TEXT,
  PRIMARY KEY(invocation_id, number),
  FOREIGN KEY(invocation_id) REFERENCES invocations(id)
);`)
	return err
}

type DueInput struct {
	Schedule            string
	Task                string
	TaskGenerationID    string
	ApplicationRevision string
	ConfigurationDigest string
	FencingToken        int64
	DueAt               time.Time
}

func (s *Store) RecordDue(ctx context.Context, input DueInput, now time.Time) (host.ScheduleOccurrenceStatus, host.TaskInvocationStatus, bool, error) {
	if input.Schedule == "" || input.Task == "" || input.TaskGenerationID == "" || input.ApplicationRevision == "" || input.ConfigurationDigest == "" || input.FencingToken < 1 || input.DueAt.IsZero() {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, errors.New("schedule occurrence input is incomplete")
	}
	due := input.DueAt.UTC().Truncate(time.Minute)
	occurrenceID := stableID("occ", input.Schedule, due.Format(time.RFC3339))
	invocationID := stableID("inv", input.Schedule, due.Format(time.RFC3339))
	recorded := now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO occurrences(id, schedule, due_at, recorded_at, task_generation_id, fencing_token, invocation_id, disposition) VALUES (?, ?, ?, ?, ?, ?, ?, 'recorded')`, occurrenceID, input.Schedule, formatTime(due), formatTime(recorded), input.TaskGenerationID, input.FencingToken, invocationID)
	if err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 1 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO invocations(id, task, task_generation_id, application_revision, configuration_digest, trigger_kind, created_at, outcome) VALUES (?, ?, ?, ?, ?, 'schedule', ?, 'pending')`, invocationID, input.Task, input.TaskGenerationID, input.ApplicationRevision, input.ConfigurationDigest, formatTime(recorded)); err != nil {
			return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	occurrence, invocation, err := s.load(ctx, occurrenceID, invocationID)
	return occurrence, invocation, inserted == 1, err
}

func (s *Store) BeginAttempt(ctx context.Context, invocationID, systemdUnit string, now time.Time) (int, error) {
	if invocationID == "" || systemdUnit == "" {
		return 0, errors.New("Task attempt identity is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var number int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM attempts WHERE invocation_id = ?`, invocationID).Scan(&number); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO attempts(invocation_id, number, systemd_unit, started_at, outcome) VALUES (?, ?, ?, ?, 'running')`, invocationID, number, systemdUnit, formatTime(now.UTC())); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invocations SET outcome = 'running' WHERE id = ?`, invocationID); err != nil {
		return 0, err
	}
	return number, tx.Commit()
}

func (s *Store) CompleteAttempt(ctx context.Context, invocationID string, number int, outcome, evidencePath string, now time.Time) error {
	if outcome != "succeeded" && outcome != "failed" {
		return errors.New("Task attempt outcome is unsupported")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE attempts SET completed_at = ?, outcome = ?, evidence_path = ? WHERE invocation_id = ? AND number = ? AND outcome = 'running'`, formatTime(now.UTC()), outcome, evidencePath, invocationID, number)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Task attempt is not running")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invocations SET outcome = ? WHERE id = ?`, outcome, invocationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE occurrences SET disposition = ? WHERE invocation_id = ?`, outcome, invocationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Latest(ctx context.Context, schedule string) (*host.ScheduleOccurrenceStatus, *host.TaskInvocationStatus, error) {
	var occurrenceID, invocationID string
	err := s.db.QueryRowContext(ctx, `SELECT id, invocation_id FROM occurrences WHERE schedule = ? ORDER BY due_at DESC LIMIT 1`, schedule).Scan(&occurrenceID, &invocationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	occurrence, invocation, err := s.load(ctx, occurrenceID, invocationID)
	return &occurrence, &invocation, err
}

func (s *Store) load(ctx context.Context, occurrenceID, invocationID string) (host.ScheduleOccurrenceStatus, host.TaskInvocationStatus, error) {
	var occurrence host.ScheduleOccurrenceStatus
	var dueAt, recordedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, schedule, due_at, recorded_at, task_generation_id, fencing_token, invocation_id, disposition FROM occurrences WHERE id = ?`, occurrenceID).Scan(&occurrence.ID, &occurrence.Schedule, &dueAt, &recordedAt, &occurrence.TaskGenerationID, &occurrence.FencingToken, &occurrence.TaskInvocationID, &occurrence.Disposition)
	if err != nil {
		return occurrence, host.TaskInvocationStatus{}, err
	}
	occurrence.DueAt, err = parseTime(dueAt)
	if err != nil {
		return occurrence, host.TaskInvocationStatus{}, err
	}
	occurrence.RecordedAt, err = parseTime(recordedAt)
	if err != nil {
		return occurrence, host.TaskInvocationStatus{}, err
	}
	var invocation host.TaskInvocationStatus
	var createdAt string
	err = s.db.QueryRowContext(ctx, `SELECT id, task, task_generation_id, application_revision, configuration_digest, trigger_kind, created_at, outcome FROM invocations WHERE id = ?`, invocationID).Scan(&invocation.ID, &invocation.Task, &invocation.TaskGenerationID, &invocation.ApplicationRevision, &invocation.ConfigurationDigest, &invocation.Trigger, &createdAt, &invocation.Outcome)
	if err != nil {
		return occurrence, invocation, err
	}
	invocation.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return occurrence, invocation, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT number, systemd_unit, started_at, completed_at, outcome, evidence_path FROM attempts WHERE invocation_id = ? ORDER BY number`, invocationID)
	if err != nil {
		return occurrence, invocation, err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt host.TaskAttemptStatus
		var started string
		var completed, evidence sql.NullString
		if err := rows.Scan(&attempt.Number, &attempt.SystemdUnit, &started, &completed, &attempt.Outcome, &evidence); err != nil {
			return occurrence, invocation, err
		}
		attempt.StartedAt, err = parseTime(started)
		if err != nil {
			return occurrence, invocation, err
		}
		if completed.Valid {
			parsed, parseErr := parseTime(completed.String)
			if parseErr != nil {
				return occurrence, invocation, parseErr
			}
			attempt.CompletedAt = &parsed
		}
		if evidence.Valid {
			attempt.EvidencePath = evidence.String
		}
		invocation.Attempts = append(invocation.Attempts, attempt)
	}
	return occurrence, invocation, rows.Err()
}

func stableID(prefix string, values ...string) string {
	h := sha256.New()
	for _, value := range values {
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(h.Sum(nil))[:24])
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
