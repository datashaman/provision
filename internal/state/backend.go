package state

import (
	"context"
	"encoding/json"
	"time"

	"provision/internal/planner"
)

// Backend is Provision's persistence boundary for immutable Plans and their
// approval records. Policy is evaluated by the approval module, not by an
// individual storage adapter.
type Backend interface {
	StoreCurrentPlan(context.Context, planner.Plan, time.Time) error
	RecordApproval(context.Context, string, ApprovalRecord) error
	LoadPlanSnapshot(context.Context, string) (PlanSnapshot, error)
	BeginOperation(context.Context, BeginOperationRequest) (OperationAttempt, error)
	RenewExecutionLease(context.Context, RenewExecutionLeaseRequest) (time.Time, error)
	CompleteOperation(context.Context, CompleteOperationRequest) error
	LoadJournal(context.Context, string) ([]JournalEvent, error)
	Close() error
}

type ApprovalDecision string

const DecisionApproved ApprovalDecision = "approved"

type ApprovalRecord struct {
	Actor     string           `json:"actor"`
	Decision  ApprovalDecision `json:"decision"`
	DecidedAt time.Time        `json:"decidedAt"`
	ExpiresAt time.Time        `json:"expiresAt"`
}

type PlanSnapshot struct {
	Plan          planner.Plan
	Approval      *ApprovalRecord
	CurrentPlanID string
}

type BeginOperationRequest struct {
	PlanID        string
	OperationID   string
	Holder        string
	StartedAt     time.Time
	LeaseDuration time.Duration
}

type OperationAttempt struct {
	AttemptID      string            `json:"attemptId"`
	Holder         string            `json:"holder"`
	FencingToken   int64             `json:"fencingToken"`
	StartedAt      time.Time         `json:"startedAt"`
	LeaseExpiresAt time.Time         `json:"leaseExpiresAt"`
	Plan           planner.Plan      `json:"plan"`
	Operation      planner.Operation `json:"operation"`
}

type RenewExecutionLeaseRequest struct {
	AttemptID     string
	Holder        string
	PlanID        string
	OperationID   string
	FencingToken  int64
	RenewedAt     time.Time
	LeaseDuration time.Duration
}

type ExecutionOutcome string

const (
	ExecutionSucceeded ExecutionOutcome = "succeeded"
	ExecutionFailed    ExecutionOutcome = "failed"
	ExecutionUncertain ExecutionOutcome = "uncertain"
)

type CompleteOperationRequest struct {
	AttemptID    string
	Holder       string
	PlanID       string
	OperationID  string
	FencingToken int64
	Outcome      ExecutionOutcome
	Observation  json.RawMessage
	CompletedAt  time.Time
}

type JournalEventKind string

const (
	JournalIntent   JournalEventKind = "intent"
	JournalOutcome  JournalEventKind = "outcome"
	JournalRejected JournalEventKind = "rejected"
)

type JournalEvent struct {
	Sequence      int64            `json:"sequence"`
	SchemaVersion string           `json:"schemaVersion"`
	Application   string           `json:"application"`
	Environment   string           `json:"environment"`
	PlanID        string           `json:"planId"`
	OperationID   string           `json:"operationId"`
	AttemptID     string           `json:"attemptId"`
	FencingToken  int64            `json:"fencingToken"`
	Kind          JournalEventKind `json:"kind"`
	Outcome       ExecutionOutcome `json:"outcome,omitempty"`
	Observation   json.RawMessage  `json:"observation"`
	OccurredAt    time.Time        `json:"occurredAt"`
}
