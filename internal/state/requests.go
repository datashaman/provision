package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"provision/internal/config"
	"provision/internal/planner"
)

const RequestCaptureSchema = "provision.dev/request-capture/v1alpha1"
const RequestApprovalSchema = "provision.dev/request-approval/v1alpha1"

// RequestBackend extends persistence without requiring non-mediating adapters to implement ingress.
type RequestBackend interface {
	Backend
	CaptureRequest(context.Context, RequestCapture) (RequestSnapshot, error)
	LoadRequest(context.Context, string) (RequestSnapshot, error)
	StoreRequestPlan(context.Context, string, planner.Plan, string, time.Time) error
	RecordRequestApproval(context.Context, RequestApproval) error
	RecordRequestEvent(context.Context, RequestEvent) error
	LoadRequestEvents(context.Context, string) ([]RequestEvent, error)
}

type RequestCapture struct {
	SchemaVersion    string                `json:"schemaVersion"`
	ID               string                `json:"id"`
	Requester        string                `json:"requester"`
	UID              uint32                `json:"uid"`
	SubmissionID     string                `json:"submissionId"`
	SubmissionDigest string                `json:"submissionDigest"`
	Submission       []byte                `json:"submission"`
	Application      string                `json:"application"`
	Environment      string                `json:"environment"`
	Revision         string                `json:"revision"`
	Component        string                `json:"component"`
	Operation        planner.OperationKind `json:"operation"`
	Configuration    config.Compiled       `json:"configuration"`
	Denial           string                `json:"denial,omitempty"`
	CapturedAt       time.Time             `json:"capturedAt"`
}

type RequestApproval struct {
	SchemaVersion   string    `json:"schemaVersion"`
	Sequence        int64     `json:"sequence"`
	RequestID       string    `json:"requestId"`
	PlanID          string    `json:"planId"`
	OperationID     string    `json:"operationId"`
	OperationDigest string    `json:"operationDigest"`
	SigningKeyID    string    `json:"signingKeyId"`
	Actor           string    `json:"actor"`
	DecidedAt       time.Time `json:"decidedAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Decision        string    `json:"decision"`
}

type RequestSnapshot struct {
	Capture         RequestCapture   `json:"capture"`
	PlanID          string           `json:"planId,omitempty"`
	OperationID     string           `json:"operationId,omitempty"`
	OperationDigest string           `json:"operationDigest,omitempty"`
	Approval        *RequestApproval `json:"approval,omitempty"`
}

func (b *sqliteBackend) CaptureRequest(ctx context.Context, capture RequestCapture) (RequestSnapshot, error) {
	if capture.SchemaVersion != RequestCaptureSchema || capture.ID == "" || capture.Requester == "" || capture.SubmissionID == "" || (len(capture.Submission) == 0 || len(capture.Submission) > 64<<10) {
		return RequestSnapshot{}, errors.New("invalid request capture")
	}
	encoded, err := json.Marshal(capture)
	if err != nil {
		return RequestSnapshot{}, err
	}
	// The identity excludes capture time; repeated imports return the original capture and outcome.
	_, err = b.db.ExecContext(ctx, `INSERT INTO operation_requests(id, requester, submission_id, capture_json) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, capture.ID, capture.Requester, capture.SubmissionID, encoded)
	if err != nil {
		return RequestSnapshot{}, err
	}
	snapshot, err := b.LoadRequest(ctx, capture.ID)
	if err != nil {
		return RequestSnapshot{}, errors.New("submission identity was already used for different bytes; submit a new request")
	}
	prior := snapshot.Capture
	if prior.Requester != capture.Requester || prior.UID != capture.UID || prior.SubmissionID != capture.SubmissionID || prior.Application != capture.Application || prior.Environment != capture.Environment || prior.Revision != capture.Revision || prior.Component != capture.Component || prior.Operation != capture.Operation {
		return RequestSnapshot{}, errors.New("request capture identity collision")
	}
	return snapshot, nil
}

func loadRequest(ctx context.Context, tx *sql.Tx, id string) (RequestSnapshot, error) {
	var snapshot RequestSnapshot
	var encoded []byte
	if err := tx.QueryRowContext(ctx, `SELECT capture_json, plan_id, operation_id, operation_digest FROM operation_requests WHERE id = ?`, id).Scan(&encoded, &snapshot.PlanID, &snapshot.OperationID, &snapshot.OperationDigest); err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(encoded, &snapshot.Capture); err != nil || snapshot.Capture.SchemaVersion != RequestCaptureSchema || snapshot.Capture.ID != id {
		return RequestSnapshot{}, errors.New("stored request capture is invalid")
	}
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT sequence, approval_json FROM request_approvals WHERE request_id = ? ORDER BY sequence DESC LIMIT 1`, id).Scan(&sequence, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return RequestSnapshot{}, err
	}
	var approval RequestApproval
	if err := json.Unmarshal(encoded, &approval); err != nil || approval.SchemaVersion != RequestApprovalSchema {
		return RequestSnapshot{}, errors.New("stored request approval is invalid")
	}
	approval.Sequence = sequence
	snapshot.Approval = &approval
	return snapshot, nil
}

func (b *sqliteBackend) LoadRequest(ctx context.Context, id string) (RequestSnapshot, error) {
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RequestSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := loadRequest(ctx, tx, id)
	if err != nil {
		return RequestSnapshot{}, err
	}
	return snapshot, tx.Commit()
}

func (b *sqliteBackend) StoreRequestPlan(ctx context.Context, id string, plan planner.Plan, operationID string, now time.Time) error {
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := loadRequest(ctx, tx, id)
	if err != nil {
		return err
	}
	c := snapshot.Capture
	op, ok := plannedOperation(plan, operationID)
	if c.Denial != "" || !ok || op.Kind != c.Operation || op.Input.Artifact == nil || *op.Input.Artifact != (planner.ArtifactInput{Source: c.Configuration.Revision.Artifacts[c.Component].Source, Digest: c.Configuration.Revision.Artifacts[c.Component].Digest}) || plan.Application != c.Application || plan.Environment != c.Environment || plan.Revision != c.Revision || plan.ConfigurationDigest != c.Configuration.Digest {
		return errors.New("Plan and operation do not match the captured request")
	}
	digest, err := planner.OperationDigest(op)
	if err != nil {
		return err
	}
	if snapshot.PlanID != "" && (snapshot.PlanID != plan.ID || snapshot.OperationID != operationID || snapshot.OperationDigest != digest) {
		return errors.New("request already binds another Plan; submit a new request")
	}
	if err := storeCurrentPlan(ctx, tx, plan, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operation_requests SET plan_id = ?, operation_id = ?, operation_digest = ? WHERE id = ?`, plan.ID, operationID, digest, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *sqliteBackend) RecordRequestApproval(ctx context.Context, approval RequestApproval) error {
	if approval.SchemaVersion != RequestApprovalSchema || approval.Actor == "" || !approval.ExpiresAt.After(approval.DecidedAt) || (approval.Decision != "approved" && approval.Decision != "denied") {
		return errors.New("invalid mediated approval decision")
	}
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := loadRequest(ctx, tx, approval.RequestID)
	if err != nil {
		return err
	}
	var head string
	c := snapshot.Capture
	if err := tx.QueryRowContext(ctx, `SELECT plan_id FROM environment_heads WHERE application = ? AND environment = ?`, c.Application, c.Environment).Scan(&head); err != nil {
		return err
	}
	if c.Denial != "" || snapshot.PlanID == "" || head != snapshot.PlanID || approval.PlanID != snapshot.PlanID || approval.OperationID != snapshot.OperationID || approval.OperationDigest != snapshot.OperationDigest || approval.SigningKeyID == "" {
		return errors.New("mediated approval does not bind the current request, Plan and operation")
	}
	encoded, err := json.Marshal(approval)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_approvals(request_id, approval_json) VALUES (?, ?)`, approval.RequestID, encoded); err != nil {
		return err
	}
	return tx.Commit()
}

// executionApproval is called inside the fenced intent transaction, including generic CLI paths.
func executionApproval(ctx context.Context, tx *sql.Tx, request BeginOperationRequest, plan planner.Plan) (time.Time, int64, error) {
	var mediated int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_requests WHERE plan_id = ?`, plan.ID).Scan(&mediated); err != nil {
		return time.Time{}, 0, err
	}
	if mediated == 0 && request.RequestID == "" {
		var decision, expiry string
		if err := tx.QueryRowContext(ctx, `SELECT decision, expires_at FROM approval_decisions WHERE plan_id = ? ORDER BY sequence DESC LIMIT 1`, plan.ID).Scan(&decision, &expiry); err != nil {
			return time.Time{}, 0, errors.New("Plan is unapproved and cannot authorize execution")
		}
		until, err := parseTime(expiry)
		if err != nil || decision != string(DecisionApproved) || !until.After(request.StartedAt) {
			return time.Time{}, 0, errors.New("Plan approval is not eligible for execution")
		}
		return until, 0, nil
	}
	snapshot, err := loadRequest(ctx, tx, request.RequestID)
	if err != nil {
		return time.Time{}, 0, errors.New("mediated Plan requires its exact request approval")
	}
	a := snapshot.Approval
	op, ok := plannedOperation(plan, request.OperationID)
	digest, err := planner.OperationDigest(op)
	if err != nil {
		return time.Time{}, 0, err
	}
	if !ok || snapshot.Capture.Denial != "" || snapshot.PlanID != plan.ID || snapshot.OperationID != request.OperationID || snapshot.OperationDigest != digest || a == nil || a.Decision != "approved" || a.RequestID != request.RequestID || a.PlanID != plan.ID || a.OperationID != request.OperationID || a.OperationDigest != digest || a.SigningKeyID != request.SigningKeyID || !a.ExpiresAt.After(request.StartedAt) || a.DecidedAt.After(request.StartedAt) {
		return time.Time{}, 0, errors.New("mediated request approval is absent, expired or does not bind this operation and signer")
	}
	// Only this request's attempts control idempotency and the requirement for a fresh decision.
	var kind, outcome, occurred string
	err = tx.QueryRowContext(ctx, `SELECT j.kind, j.outcome, j.occurred_at FROM journal_events j JOIN journal_events i ON i.attempt_id = j.attempt_id AND i.kind = 'intent' WHERE j.plan_id = ? AND j.operation_id = ? AND json_extract(i.observation_json, '$.requestId') = ? ORDER BY j.sequence DESC LIMIT 1`, plan.ID, request.OperationID, request.RequestID).Scan(&kind, &outcome, &occurred)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, 0, err
	}
	if err == nil {
		at, err := parseTime(occurred)
		if err != nil {
			return time.Time{}, 0, err
		}
		if outcome == string(ExecutionSucceeded) {
			return time.Time{}, 0, errors.New("completed request returns its recorded outcome; submit a new request to repeat")
		}
		if outcome == string(ExecutionFailed) && !a.DecidedAt.After(at) {
			return time.Time{}, 0, errors.New("known failed request requires a fresh human decision")
		}
		if (kind == string(JournalIntent) || outcome == string(ExecutionUncertain)) && request.ResumeOfAttemptID == "" {
			return time.Time{}, 0, errors.New("interrupted request requires observation through resume")
		}
	}
	return a.ExpiresAt, a.Sequence, nil
}

const RequestEventSchema = "provision.dev/request-event/v1alpha1"

type RequestEvent struct {
	SchemaVersion string    `json:"schemaVersion"`
	RequestID     string    `json:"requestId"`
	Action        string    `json:"action"`
	Reason        string    `json:"reason"`
	Actor         string    `json:"actor"`
	OccurredAt    time.Time `json:"occurredAt"`
}

func (b *sqliteBackend) RecordRequestEvent(ctx context.Context, event RequestEvent) error {
	if event.SchemaVersion != RequestEventSchema || event.RequestID == "" || event.Actor == "" || event.Action == "" {
		return errors.New("invalid request audit event")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx, `INSERT INTO request_events(request_id, event_json) VALUES (?, ?)`, event.RequestID, data)
	return err
}

func (b *sqliteBackend) LoadRequestEvents(ctx context.Context, id string) ([]RequestEvent, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT event_json FROM request_events WHERE request_id = ? ORDER BY sequence`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []RequestEvent
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var event RequestEvent
		if err := json.Unmarshal(data, &event); err != nil || event.SchemaVersion != RequestEventSchema {
			return nil, errors.New("invalid stored request audit event")
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
