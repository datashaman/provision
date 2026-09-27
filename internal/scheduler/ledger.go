package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

var (
	ErrAttemptRunning    = errors.New("Task Invocation already has a running attempt")
	ErrOverlap           = errors.New("Schedule forbids overlapping Task Invocations")
	ErrRetryNotDue       = errors.New("Task retry delay has not elapsed")
	ErrAttemptsExhausted = errors.New("Task Invocation has no remaining attempts")
)

func Open(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("schedule ledger requires an absolute path")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	db.SetMaxOpenConns(1)
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
PRAGMA busy_timeout=5000;
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
CREATE TABLE IF NOT EXISTS invocation_contracts (
  invocation_id TEXT PRIMARY KEY,
  occurrence_id TEXT NOT NULL,
  task_unit TEXT NOT NULL,
  input_references TEXT NOT NULL,
  max_attempts INTEGER NOT NULL,
  retry_delay_ns INTEGER NOT NULL,
  overlap TEXT NOT NULL,
  FOREIGN KEY(invocation_id) REFERENCES invocations(id)
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
);
CREATE TABLE IF NOT EXISTS attempt_launches (
  invocation_id TEXT NOT NULL,
  number INTEGER NOT NULL,
  systemd_invocation_id TEXT NOT NULL,
  PRIMARY KEY(invocation_id, number),
  FOREIGN KEY(invocation_id, number) REFERENCES attempts(invocation_id, number)
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
	TaskUnit            string
	InputReferences     []string
	MaxAttempts         int
	RetryDelay          time.Duration
	Overlap             string
}

func (s *Store) RecordDue(ctx context.Context, input DueInput, now time.Time) (host.ScheduleOccurrenceStatus, host.TaskInvocationStatus, bool, error) {
	if input.Schedule == "" || input.Task == "" || input.TaskGenerationID == "" || input.ApplicationRevision == "" || input.ConfigurationDigest == "" || input.FencingToken < 1 || input.DueAt.IsZero() {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, errors.New("schedule occurrence input is incomplete")
	}
	if input.TaskUnit == "" {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, errors.New("schedule occurrence requires an exact Task unit")
	}
	if input.MaxAttempts < 1 || input.MaxAttempts > 10 || input.RetryDelay < 0 || (input.Overlap != "allow" && input.Overlap != "forbid") {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, errors.New("schedule occurrence policy is unsupported")
	}
	due := input.DueAt.UTC().Truncate(time.Minute)
	occurrenceID := stableID("occ", input.Schedule, due.Format(time.RFC3339))
	invocationID := stableID("inv", input.Schedule, due.Format(time.RFC3339))
	recorded := now.UTC()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	result, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO occurrences(id, schedule, due_at, recorded_at, task_generation_id, fencing_token, invocation_id, disposition) VALUES (?, ?, ?, ?, ?, ?, ?, 'recorded')`, occurrenceID, input.Schedule, formatTime(due), formatTime(recorded), input.TaskGenerationID, input.FencingToken, invocationID)
	if err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 1 {
		if _, err := conn.ExecContext(ctx, `INSERT INTO invocations(id, task, task_generation_id, application_revision, configuration_digest, trigger_kind, created_at, outcome) VALUES (?, ?, ?, ?, ?, 'schedule', ?, 'pending')`, invocationID, input.Task, input.TaskGenerationID, input.ApplicationRevision, input.ConfigurationDigest, formatTime(recorded)); err != nil {
			return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
		}
		inputs, err := json.Marshal(input.InputReferences)
		if err != nil {
			return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO invocation_contracts(invocation_id, occurrence_id, task_unit, input_references, max_attempts, retry_delay_ns, overlap) VALUES (?, ?, ?, ?, ?, ?, ?)`, invocationID, occurrenceID, input.TaskUnit, string(inputs), input.MaxAttempts, input.RetryDelay.Nanoseconds(), input.Overlap); err != nil {
			return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	if err := conn.Close(); err != nil {
		return host.ScheduleOccurrenceStatus{}, host.TaskInvocationStatus{}, false, err
	}
	occurrence, invocation, err := s.load(ctx, occurrenceID, invocationID)
	return occurrence, invocation, inserted == 1, err
}

func (s *Store) BeginAttempt(ctx context.Context, invocationID, systemdUnit string, now time.Time) (int, error) {
	if invocationID == "" || systemdUnit == "" {
		return 0, errors.New("Task attempt identity is incomplete")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return 0, err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var taskUnit, overlap, outcome, schedule string
	var maxAttempts, retryDelayNS int64
	err = conn.QueryRowContext(ctx, `SELECT c.task_unit, c.overlap, c.max_attempts, c.retry_delay_ns, i.outcome, o.schedule FROM invocation_contracts c JOIN invocations i ON i.id = c.invocation_id JOIN occurrences o ON o.invocation_id = i.id WHERE i.id = ?`, invocationID).Scan(&taskUnit, &overlap, &maxAttempts, &retryDelayNS, &outcome, &schedule)
	if err != nil {
		return 0, fmt.Errorf("read Task Invocation contract: %w", err)
	}
	if !unitInstanceMatches(taskUnit, systemdUnit, invocationID) {
		return 0, errors.New("Task attempt unit differs from the recorded generation")
	}
	if outcome == "succeeded" || outcome == "skipped-overlap" {
		return 0, ErrAttemptsExhausted
	}
	var number int
	var lastOutcome string
	var lastCompleted sql.NullString
	err = conn.QueryRowContext(ctx, `SELECT number, outcome, completed_at FROM attempts WHERE invocation_id = ? ORDER BY number DESC LIMIT 1`, invocationID).Scan(&number, &lastOutcome, &lastCompleted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if lastOutcome == "running" {
		return 0, ErrAttemptRunning
	}
	if int64(number) >= maxAttempts {
		return 0, ErrAttemptsExhausted
	}
	if number > 0 && lastCompleted.Valid {
		completed, err := parseTime(lastCompleted.String)
		if err != nil {
			return 0, err
		}
		if now.Before(completed.Add(time.Duration(retryDelayNS))) {
			return 0, ErrRetryNotDue
		}
	}
	if overlap == "forbid" {
		var running int
		err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM invocations i JOIN occurrences o ON o.invocation_id = i.id WHERE o.schedule = ? AND i.id <> ? AND i.outcome = 'running'`, schedule, invocationID).Scan(&running)
		if err != nil {
			return 0, err
		}
		if running > 0 {
			return 0, ErrOverlap
		}
	}
	number++
	if _, err := conn.ExecContext(ctx, `INSERT INTO attempts(invocation_id, number, systemd_unit, started_at, outcome) VALUES (?, ?, ?, ?, 'running')`, invocationID, number, systemdUnit, formatTime(now.UTC())); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE invocations SET outcome = 'running' WHERE id = ?`, invocationID); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE occurrences SET disposition = 'running' WHERE invocation_id = ?`, invocationID); err != nil {
		return 0, err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return number, err
}

func unitInstanceMatches(template, instance, invocation string) bool {
	if len(template) < len("@.service") || template[len(template)-len("@.service"):] != "@.service" {
		return false
	}
	return template[:len(template)-len("@.service")]+"@"+invocation+".service" == instance
}

func (s *Store) CompleteAttempt(ctx context.Context, invocationID string, number int, outcome, evidencePath string, now time.Time) error {
	if outcome != "succeeded" && outcome != "failed" && outcome != "timed-out" && outcome != "uncertain" {
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

func (s *Store) MarkLaunched(ctx context.Context, invocationID string, number int, systemdInvocationID string) error {
	if systemdInvocationID == "" {
		return errors.New("systemd invocation identity is required")
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO attempt_launches(invocation_id, number, systemd_invocation_id) SELECT invocation_id, number, ? FROM attempts WHERE invocation_id = ? AND number = ? AND outcome = 'running'`, systemdInvocationID, invocationID, number)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	var recorded string
	if err := s.db.QueryRowContext(ctx, `SELECT systemd_invocation_id FROM attempt_launches WHERE invocation_id = ? AND number = ?`, invocationID, number).Scan(&recorded); err != nil || recorded != systemdInvocationID {
		return errors.New("Task attempt launch identity differs from recorded state")
	}
	return nil
}

// Pending returns unfinished invocations in occurrence order. A caller must
// observe a running unit before finalizing an interrupted delivery as uncertain.
func (s *Store) Pending(ctx context.Context, schedule string) ([]host.TaskInvocationStatus, error) {
	var legacy int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM occurrences o JOIN invocations i ON i.id = o.invocation_id LEFT JOIN invocation_contracts c ON c.invocation_id = i.id WHERE o.schedule = ? AND c.invocation_id IS NULL AND i.outcome IN ('pending', 'running')`, schedule).Scan(&legacy); err != nil {
		return nil, err
	}
	if legacy > 0 {
		return nil, errors.New("Schedule has unfinished legacy Task Invocations without pinned retry contracts; inspect the occurrence ledger before resuming")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.invocation_id FROM occurrences o JOIN invocations i ON i.id = o.invocation_id JOIN invocation_contracts c ON c.invocation_id = i.id WHERE o.schedule = ? AND i.outcome IN ('pending', 'running', 'failed', 'timed-out', 'uncertain') ORDER BY o.due_at`, schedule)
	if err != nil {
		return nil, err
	}
	type pair struct{ occurrence, invocation string }
	var ids []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.occurrence, &p.invocation); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var result []host.TaskInvocationStatus
	for _, p := range ids {
		_, invocation, err := s.load(ctx, p.occurrence, p.invocation)
		if err != nil {
			return nil, err
		}
		result = append(result, invocation)
	}
	return result, nil
}

func (s *Store) SkipOverlap(ctx context.Context, invocationID string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var running int
	err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM invocations i JOIN occurrences o ON o.invocation_id = i.id WHERE o.schedule = (SELECT schedule FROM occurrences WHERE invocation_id = ?) AND i.id <> ? AND i.outcome = 'running'`, invocationID, invocationID).Scan(&running)
	if err != nil {
		return err
	}
	if running == 0 {
		return ErrOverlap
	}
	result, err := conn.ExecContext(ctx, `UPDATE invocations SET outcome = 'skipped-overlap' WHERE id = ? AND outcome = 'pending'`, invocationID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("Task Invocation is no longer pending")
	}
	if _, err = conn.ExecContext(ctx, `UPDATE occurrences SET disposition = 'skipped-overlap' WHERE invocation_id = ?`, invocationID); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
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

func (s *Store) Recent(ctx context.Context, schedule string, limit int) ([]host.ScheduleOccurrenceStatus, []host.TaskInvocationStatus, error) {
	if limit < 1 || limit > 100 {
		return nil, nil, errors.New("Schedule history limit must be between 1 and 100")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, invocation_id FROM occurrences WHERE schedule = ? ORDER BY due_at DESC LIMIT ?`, schedule, limit)
	if err != nil {
		return nil, nil, err
	}
	type pair struct{ occurrence, invocation string }
	var ids []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.occurrence, &p.invocation); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	occurrences := make([]host.ScheduleOccurrenceStatus, 0, len(ids))
	invocations := make([]host.TaskInvocationStatus, 0, len(ids))
	for _, p := range ids {
		occurrence, invocation, err := s.load(ctx, p.occurrence, p.invocation)
		if err != nil {
			return nil, nil, err
		}
		occurrences = append(occurrences, occurrence)
		invocations = append(invocations, invocation)
	}
	return occurrences, invocations, nil
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
	invocation.DeliverySemantics = "at-least-once"
	invocation.OccurrenceID = occurrenceID
	var inputJSON string
	err = s.db.QueryRowContext(ctx, `SELECT task_unit, input_references FROM invocation_contracts WHERE invocation_id = ?`, invocationID).Scan(&invocation.TaskUnit, &inputJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return occurrence, invocation, err
	}
	if inputJSON != "" {
		if err := json.Unmarshal([]byte(inputJSON), &invocation.InputReferences); err != nil {
			return occurrence, invocation, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.number, a.systemd_unit, a.started_at, a.completed_at, a.outcome, a.evidence_path, l.systemd_invocation_id FROM attempts a LEFT JOIN attempt_launches l ON l.invocation_id = a.invocation_id AND l.number = a.number WHERE a.invocation_id = ? ORDER BY a.number`, invocationID)
	if err != nil {
		return occurrence, invocation, err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt host.TaskAttemptStatus
		var started string
		var completed, evidence, systemdInvocation sql.NullString
		if err := rows.Scan(&attempt.Number, &attempt.SystemdUnit, &started, &completed, &attempt.Outcome, &evidence, &systemdInvocation); err != nil {
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
		if systemdInvocation.Valid {
			attempt.SystemdInvocationID = systemdInvocation.String
		}
		invocation.Attempts = append(invocation.Attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return occurrence, invocation, err
	}
	if len(invocation.Attempts) > 0 && invocation.Outcome != "running" {
		invocation.CompletedAt = invocation.Attempts[len(invocation.Attempts)-1].CompletedAt
	}
	return occurrence, invocation, nil
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
