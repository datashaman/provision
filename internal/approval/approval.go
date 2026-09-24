package approval

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"time"

	"provision/internal/planner"
	"provision/internal/state"
)

const SchemaVersion = "provision.dev/plan-status/v1alpha1"

type OperationCapability string

const CapabilityApprove OperationCapability = "approve"

type Authority struct {
	Actor        string
	Scope        Scope
	Capabilities []OperationCapability
}

type Scope struct {
	Application string
	Environment string
}

type StatusReason string

const (
	ReasonUnapproved StatusReason = "unapproved"
	ReasonSuperseded StatusReason = "superseded"
	ReasonExpired    StatusReason = "expired"
)

type Status struct {
	SchemaVersion string                 `json:"schemaVersion"`
	PlanID        string                 `json:"planId"`
	Plan          planner.Plan           `json:"plan"`
	Actor         string                 `json:"actor,omitempty"`
	Decision      state.ApprovalDecision `json:"decision,omitempty"`
	DecidedAt     time.Time              `json:"decidedAt,omitempty"`
	ExpiresAt     time.Time              `json:"expiresAt,omitempty"`
	Eligible      bool                   `json:"eligible"`
	Reason        StatusReason           `json:"reason,omitempty"`
}

// AuthenticateLocalActor establishes identity through the operating system.
// Access to the owner-only local State Backend is the initial external grant of
// the approve capability; the caller-provided actor is only a confirmation.
func AuthenticateLocalActor(claimedActor string, scope Scope) (Authority, error) {
	current, err := user.Current()
	if err != nil {
		return Authority{}, fmt.Errorf("authenticate local approval actor: %w", err)
	}
	if claimedActor == "" || claimedActor != current.Username {
		return Authority{}, fmt.Errorf("approval actor %q does not match authenticated local OS user %q", claimedActor, current.Username)
	}
	if scope.Application == "" || scope.Environment == "" {
		return Authority{}, errors.New("approval authority requires an Application and Environment scope")
	}
	return Authority{
		Actor:        current.Username,
		Scope:        scope,
		Capabilities: []OperationCapability{CapabilityApprove},
	}, nil
}

func Approve(ctx context.Context, backend state.Backend, planID string, authority Authority, decidedAt, expiresAt time.Time) (Status, error) {
	if !authority.allows(CapabilityApprove) {
		return Status{}, errors.New("authenticated actor lacks the approve Operation Capability")
	}
	decidedAt = decidedAt.UTC()
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(decidedAt) {
		return Status{}, errors.New("approval expiry must be after its decision time")
	}
	snapshot, err := backend.LoadPlanSnapshot(ctx, planID)
	if err != nil {
		return Status{}, err
	}
	if authority.Scope.Application != snapshot.Plan.Application || authority.Scope.Environment != snapshot.Plan.Environment {
		return Status{}, errors.New("approval authority is not scoped to the Plan's Application and Environment")
	}
	if !requiresCapability(snapshot.Plan, CapabilityApprove) {
		return Status{}, errors.New("Plan does not require the approve Operation Capability")
	}
	if err := backend.RecordApproval(ctx, planID, state.ApprovalRecord{
		Actor:     authority.Actor,
		Decision:  state.DecisionApproved,
		DecidedAt: decidedAt,
		ExpiresAt: expiresAt,
	}); err != nil {
		return Status{}, err
	}
	return Inspect(ctx, backend, planID, decidedAt)
}

func requiresCapability(plan planner.Plan, capability OperationCapability) bool {
	for _, requirement := range plan.ApprovalRequirements {
		if requirement.Capability == string(capability) {
			return true
		}
	}
	return false
}

func Inspect(ctx context.Context, backend state.Backend, planID string, now time.Time) (Status, error) {
	snapshot, err := backend.LoadPlanSnapshot(ctx, planID)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		SchemaVersion: SchemaVersion,
		PlanID:        planID,
		Plan:          snapshot.Plan,
	}
	if snapshot.Approval == nil {
		status.Reason = ReasonUnapproved
		return status, nil
	}
	status.Actor = snapshot.Approval.Actor
	status.Decision = snapshot.Approval.Decision
	status.DecidedAt = snapshot.Approval.DecidedAt
	status.ExpiresAt = snapshot.Approval.ExpiresAt
	switch {
	case snapshot.CurrentPlanID != planID:
		status.Reason = ReasonSuperseded
	case !snapshot.Approval.ExpiresAt.After(now.UTC()):
		status.Reason = ReasonExpired
	default:
		status.Eligible = true
	}
	return status, nil
}

func (a Authority) allows(capability OperationCapability) bool {
	for _, granted := range a.Capabilities {
		if granted == capability {
			return true
		}
	}
	return false
}
