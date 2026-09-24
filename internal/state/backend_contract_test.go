package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
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

func TestSQLiteBackendMigratesPreviousSchemaAtomically(t *testing.T) {
	path := t.TempDir() + "/state.db"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE state_metadata (singleton INTEGER PRIMARY KEY, schema_version TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO state_metadata(singleton, schema_version) VALUES (1, ?)`, previousSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	backend, err := OpenExistingSQLiteForUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	journal, err := backend.LoadJournal(context.Background(), "sha256:"+strings.Repeat("0", 64))
	if err != nil || len(journal) != 0 {
		t.Fatalf("migrated journal = %+v, %v", journal, err)
	}
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
	if _, err := backend.BeginOperation(ctx, BeginOperationRequest{PlanID: first.ID, OperationID: "op-01", Holder: "holder-a", StartedAt: now, LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "unapproved") {
		t.Fatalf("unapproved Plan began execution: %v", err)
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
	firstAttempt, err := backend.BeginOperation(ctx, BeginOperationRequest{PlanID: first.ID, OperationID: "op-01", Holder: "holder-a", StartedAt: now.Add(2 * time.Minute), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	renewedUntil, err := backend.RenewExecutionLease(ctx, RenewExecutionLeaseRequest{
		AttemptID: firstAttempt.AttemptID, Holder: firstAttempt.Holder, PlanID: first.ID, OperationID: "op-01",
		FencingToken: firstAttempt.FencingToken, RenewedAt: now.Add(2*time.Minute + 30*time.Second), LeaseDuration: time.Minute,
	})
	if err != nil || !renewedUntil.Equal(now.Add(3*time.Minute+30*time.Second)) {
		t.Fatalf("renewed lease expires at %v, %v", renewedUntil, err)
	}
	if _, err := backend.BeginOperation(ctx, BeginOperationRequest{PlanID: first.ID, OperationID: "op-01", Holder: "holder-b", StartedAt: now.Add(3 * time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "active mutating operation") {
		t.Fatalf("renewed execution lease did not fence a concurrent operation: %v", err)
	}
	if _, err := backend.BeginOperation(ctx, BeginOperationRequest{PlanID: first.ID, OperationID: "op-01", Holder: "holder-b", StartedAt: now.Add(2 * time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "active mutating operation") {
		t.Fatalf("concurrent lease was accepted: %v", err)
	}
	secondAttempt, err := backend.BeginOperation(ctx, BeginOperationRequest{PlanID: first.ID, OperationID: "op-01", Holder: "holder-b", StartedAt: now.Add(4 * time.Minute), LeaseDuration: time.Minute})
	if err != nil || secondAttempt.FencingToken <= firstAttempt.FencingToken {
		t.Fatalf("replacement lease = %+v, %v", secondAttempt, err)
	}
	if err := backend.CompleteOperation(ctx, CompleteOperationRequest{
		AttemptID: firstAttempt.AttemptID, Holder: firstAttempt.Holder, PlanID: first.ID, OperationID: "op-01",
		FencingToken: firstAttempt.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"staged"}`), CompletedAt: now.Add(4*time.Minute + time.Second),
	}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale executor committed: %v", err)
	}
	if err := backend.CompleteOperation(ctx, CompleteOperationRequest{
		AttemptID: secondAttempt.AttemptID, Holder: secondAttempt.Holder, PlanID: first.ID, OperationID: "op-01",
		FencingToken: secondAttempt.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"staged"}`), CompletedAt: now.Add(4*time.Minute + time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	journal, err := backend.LoadJournal(ctx, first.ID)
	if err != nil || len(journal) != 4 || journal[0].Kind != JournalIntent || journal[2].Kind != JournalRejected || journal[2].Outcome != ExecutionSucceeded || journal[3].Kind != JournalOutcome || journal[3].Outcome != ExecutionSucceeded {
		t.Fatalf("journal = %+v, %v", journal, err)
	}
	if !strings.Contains(string(journal[2].Observation), `"status":"rejected"`) || !strings.Contains(string(journal[2].Observation), `"submittedObservation":{"status":"staged"}`) {
		t.Fatalf("rejected stale result lost its evidence: %s", journal[2].Observation)
	}
	if err := backend.StoreCurrentPlan(ctx, second, now.Add(6*time.Minute)); err != nil {
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
	durableJournal, err := reopened.LoadJournal(ctx, first.ID)
	if err != nil || len(durableJournal) != 4 || durableJournal[2].Kind != JournalRejected || durableJournal[3].Outcome != ExecutionSucceeded {
		t.Fatalf("durable journal = %+v, %v", durableJournal, err)
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
		Operations: []planner.Operation{{
			ID: "op-01", Kind: planner.StageArtifact, DependsOn: []string{},
			Input: planner.OperationInput{Artifact: &planner.ArtifactInput{Source: "https://artifacts.example/release.tar.gz", Digest: "sha256:" + strings.Repeat("3", 64)}},
		}},
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	plan.ID = "sha256:" + hex.EncodeToString(digest[:])
	return plan
}
