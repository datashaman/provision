package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"provision/internal/planner"
)

type backendContractFactory struct {
	create func() (Backend, error)
	reopen func() (Backend, error)
}

func TestSQLiteBackendContract(t *testing.T) {
	path := t.TempDir() + "/state.db"
	runBackendContract(t, backendContractFactory{
		create: func() (Backend, error) { return OpenSQLite(path) },
		reopen: func() (Backend, error) { return OpenExistingSQLite(path) },
	})
}

func runBackendContract(t *testing.T, factory backendContractFactory) {
	t.Helper()
	ctx := context.Background()
	backend, err := factory.create()
	if err != nil {
		t.Fatal(err)
	}
	first := contractPlan(t, "application-a", "shared-name", "revision-a")
	second := contractPlan(t, "application-a", "shared-name", "revision-b")
	otherApplication := contractPlan(t, "application-b", "shared-name", "revision-a")
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)

	if err := backend.StoreCurrentPlan(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	initial, err := backend.LoadPlanSnapshot(ctx, first.ID)
	if err != nil || initial.Plan.ID != first.ID || initial.CurrentPlanID != first.ID || initial.Approval != nil {
		t.Fatalf("initial snapshot = %+v, %v", initial, err)
	}
	approval := ApprovalRecord{
		Actor:     "contract-actor",
		Decision:  DecisionApproved,
		DecidedAt: now.Add(time.Minute),
		ExpiresAt: now.Add(time.Hour),
	}
	if err := backend.RecordApproval(ctx, first.ID, approval); err != nil {
		t.Fatal(err)
	}
	if err := backend.StoreCurrentPlan(ctx, second, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	superseded, err := backend.LoadPlanSnapshot(ctx, first.ID)
	if err != nil || superseded.CurrentPlanID != second.ID || superseded.Approval == nil || superseded.Approval.Actor != approval.Actor {
		t.Fatalf("superseded snapshot = %+v, %v", superseded, err)
	}
	if err := backend.RecordApproval(ctx, first.ID, approval); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("superseded Plan approval error = %v", err)
	}
	if err := backend.StoreCurrentPlan(ctx, otherApplication, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stillCurrent, err := backend.LoadPlanSnapshot(ctx, second.ID)
	if err != nil || stillCurrent.CurrentPlanID != second.ID {
		t.Fatalf("same-named Environment in another Application changed head: %+v, %v", stillCurrent, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := factory.reopen()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	durable, err := reopened.LoadPlanSnapshot(ctx, first.ID)
	if err != nil || durable.Plan.ID != first.ID || durable.CurrentPlanID != second.ID || durable.Approval == nil || durable.Approval.Decision != DecisionApproved {
		t.Fatalf("durable snapshot = %+v, %v", durable, err)
	}
}

func contractPlan(t *testing.T, application, environment, revision string) planner.Plan {
	t.Helper()
	plan := planner.Plan{
		SchemaVersion:        planner.SchemaVersion,
		Application:          application,
		Environment:          environment,
		Revision:             revision,
		ConfigurationDigest:  "sha256:" + strings.Repeat("1", 64),
		ArtifactDigests:      map[string]string{},
		ObservationDigest:    "sha256:" + strings.Repeat("2", 64),
		ApprovalRequirements: []planner.ApprovalRequirement{{Capability: "approve", Reason: "contract"}},
		Operations:           []planner.Operation{},
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	plan.ID = "sha256:" + hex.EncodeToString(digest[:])
	return plan
}
