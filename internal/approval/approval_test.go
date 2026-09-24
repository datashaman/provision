package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"provision/internal/planner"
	"provision/internal/state"
)

type backendFake struct {
	snapshot state.PlanSnapshot
	recorded bool
}

func (f *backendFake) StoreCurrentPlan(context.Context, planner.Plan, time.Time) error {
	return errors.New("not used")
}

func (f *backendFake) RecordApproval(_ context.Context, planID string, record state.ApprovalRecord) error {
	if planID != f.snapshot.Plan.ID || planID != f.snapshot.CurrentPlanID {
		return errors.New("Plan is not current")
	}
	f.recorded = true
	f.snapshot.Approval = &record
	return nil
}

func (f *backendFake) LoadPlanSnapshot(context.Context, string) (state.PlanSnapshot, error) {
	return f.snapshot, nil
}

func (f *backendFake) BeginOperation(context.Context, state.BeginOperationRequest) (state.OperationAttempt, error) {
	return state.OperationAttempt{}, errors.New("not used")
}

func (f *backendFake) RenewExecutionLease(context.Context, state.RenewExecutionLeaseRequest) (time.Time, error) {
	return time.Time{}, errors.New("not used")
}

func (f *backendFake) CompleteOperation(context.Context, state.CompleteOperationRequest) error {
	return errors.New("not used")
}

func (f *backendFake) LoadJournal(context.Context, string) ([]state.JournalEvent, error) {
	return nil, errors.New("not used")
}

func (f *backendFake) Close() error { return nil }

func TestApproveUsesEnvironmentScopedCapabilityThroughBackend(t *testing.T) {
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	plan := planner.Plan{
		ID:          "plan-a",
		Application: "application-a",
		Environment: "environment-a",
		ApprovalRequirements: []planner.ApprovalRequirement{{
			Capability: "approve",
			Reason:     "environment policy",
		}},
	}
	backend := &backendFake{snapshot: state.PlanSnapshot{Plan: plan, CurrentPlanID: plan.ID}}
	authority := Authority{
		Actor:        "actor-a",
		Scope:        Scope{Application: plan.Application, Environment: "environment-b"},
		Capabilities: []OperationCapability{CapabilityApprove},
	}
	if _, err := Approve(context.Background(), backend, plan.ID, authority, now, now.Add(time.Hour)); err == nil || backend.recorded {
		t.Fatalf("out-of-scope authority recorded approval: %v", err)
	}

	authority.Scope.Environment = plan.Environment
	status, err := Approve(context.Background(), backend, plan.ID, authority, now, now.Add(time.Hour))
	if err != nil || !backend.recorded || !status.Eligible || status.Actor != authority.Actor {
		t.Fatalf("scoped approval = %+v, recorded=%v, err=%v", status, backend.recorded, err)
	}
}
