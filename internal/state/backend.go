package state

import (
	"context"
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
