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
	if _, err := raw.Exec(`CREATE TABLE environment_leases (
        application TEXT NOT NULL,
        environment TEXT NOT NULL,
        fencing_token INTEGER NOT NULL,
        holder TEXT NOT NULL,
        plan_id TEXT NOT NULL,
        operation_id TEXT NOT NULL,
        attempt_id TEXT NOT NULL,
        acquired_at TEXT NOT NULL,
        expires_at TEXT NOT NULL,
        released_at TEXT,
        PRIMARY KEY (application, environment)
    ) STRICT`); err != nil {
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

func TestSQLiteBackendRequiresSuccessfulOperationDependencies(t *testing.T) {
	path := t.TempDir() + "/state.db"
	backend, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	plan := contractPlan(t, "application-a", "lab", "revision-a")
	plan.Operations = append(plan.Operations, planner.Operation{
		ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"},
		Input: planner.OperationInput{Generation: &planner.GenerationInput{
			GenerationReference: planner.GenerationReference{ID: "revision-a-333333333333", Revision: "revision-a", ArtifactDigest: "sha256:" + strings.Repeat("3", 64),
				Account: "provision-lab", ReleaseDirectory: "/var/lib/provision/environments/lab/releases/revision-a-333333333333"},
		}},
	})
	plan.Operations = append(plan.Operations, planner.Operation{
		ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-02"},
		Input: planner.OperationInput{Endpoint: &planner.EndpointInput{
			GenerationReference: planner.GenerationReference{ID: "revision-a-333333333333", Revision: "revision-a", ArtifactDigest: "sha256:" + strings.Repeat("3", 64),
				Account: "provision-lab", ReleaseDirectory: "/var/lib/provision/environments/lab/releases/revision-a-333333333333"},
			Unit: "provision-lab-web-333333333333.service", RouteID: "provision-lab-web", ListenPort: 18080,
			Upstream: "127.0.0.1:28181", UpstreamPort: 28181, DrainPolicy: "caddy-graceful-config-reload",
		}},
	})
	plan.Operations = append(plan.Operations, planner.Operation{
		ID: "op-06", Kind: planner.VerifyActive, DependsOn: []string{"op-05"},
	})
	plan.Operations = append(plan.Operations, planner.Operation{
		ID: "op-07", Kind: planner.DrainPrevious, DependsOn: []string{"op-06"},
	})
	plan.Operations = append(plan.Operations, planner.Operation{
		ID: "op-08", Kind: planner.RetainPrevious, DependsOn: []string{"op-07"},
	})
	plan.ID = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	plan.ID = "sha256:" + hex.EncodeToString(digest[:])
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if err := backend.StoreCurrentPlan(context.Background(), plan, now); err != nil {
		t.Fatal(err)
	}
	if err := backend.RecordApproval(context.Background(), plan.ID, ApprovalRecord{Actor: "tester", Decision: DecisionApproved, DecidedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-02", Holder: "holder", StartedAt: now.Add(time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "op-01 has no recorded outcome") {
		t.Fatalf("candidate began before Artifact preparation: %v", err)
	}
	first, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-01", Holder: "holder", StartedAt: now.Add(2 * time.Minute), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.CompleteOperation(context.Background(), CompleteOperationRequest{AttemptID: first.AttemptID, Holder: first.Holder, PlanID: plan.ID, OperationID: "op-01", FencingToken: first.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"staged"}`), CompletedAt: now.Add(2*time.Minute + time.Second)}); err != nil {
		t.Fatal(err)
	}
	second, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-02", Holder: "holder", StartedAt: now.Add(3 * time.Minute), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("candidate did not begin after successful dependency: %v", err)
	}
	if err := backend.CompleteOperation(context.Background(), CompleteOperationRequest{AttemptID: second.AttemptID, Holder: second.Holder, PlanID: plan.ID, OperationID: "op-02", FencingToken: second.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"installed"}`), CompletedAt: now.Add(3*time.Minute + time.Second)}); err != nil {
		t.Fatal(err)
	}
	endpointAttempt, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-05", Holder: "holder", StartedAt: now.Add(4 * time.Minute), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("Endpoint switch did not begin after successful dependency: %v", err)
	}
	if err := backend.CompleteOperation(context.Background(), CompleteOperationRequest{AttemptID: endpointAttempt.AttemptID, Holder: endpointAttempt.Holder, PlanID: plan.ID, OperationID: "op-05", FencingToken: endpointAttempt.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"active"}`), CompletedAt: now.Add(4*time.Minute + time.Second)}); err != nil {
		t.Fatal(err)
	}
	verifyAttempt, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-06", Holder: "holder", StartedAt: now.Add(5 * time.Minute), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("post-switch verification did not begin after successful Endpoint switch: %v", err)
	}
	if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-07", Holder: "holder", StartedAt: now.Add(6 * time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "op-06 has no recorded outcome") {
		t.Fatalf("HTTP drain began while stable verification was active: %v", err)
	}
	if err := backend.CompleteOperation(context.Background(), CompleteOperationRequest{AttemptID: verifyAttempt.AttemptID, Holder: verifyAttempt.Holder, PlanID: plan.ID, OperationID: "op-06", FencingToken: verifyAttempt.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"healthy"}`), CompletedAt: now.Add(5*time.Minute + time.Second)}); err != nil {
		t.Fatal(err)
	}
	drainAttempt, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-07", Holder: "holder", StartedAt: now.Add(6 * time.Minute), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("HTTP drain did not begin after successful stable verification: %v", err)
	}
	if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-08", Holder: "holder", StartedAt: now.Add(7 * time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "op-07 has no recorded outcome") {
		t.Fatalf("rollback retention began while drain was active: %v", err)
	}
	if err := backend.CompleteOperation(context.Background(), CompleteOperationRequest{AttemptID: drainAttempt.AttemptID, Holder: drainAttempt.Holder, PlanID: plan.ID, OperationID: "op-07", FencingToken: drainAttempt.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"drained"}`), CompletedAt: now.Add(6*time.Minute + time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-08", Holder: "holder", StartedAt: now.Add(7 * time.Minute), LeaseDuration: time.Minute}); err != nil {
		t.Fatalf("rollback retention did not begin after successful drain: %v", err)
	}
}

func TestSQLiteBackendEnablesIndependentQueuePreparation(t *testing.T) {
	path := t.TempDir() + "/state.db"
	backend, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	plan := contractPlan(t, "application-a", "lab", "revision-a")
	plan.Operations = []planner.Operation{{
		ID: "op-01", Kind: planner.PrepareQueue,
		Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Queue: &planner.AsyncQueueInput{
			LogicalID: "provision-lab-messages", GenerationID: "provision-lab-messages-rabbitmq-4-3-6-34fc91a9de04",
		}}},
	}}
	plan.ID = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	plan.ID = "sha256:" + hex.EncodeToString(digest[:])
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	if err := backend.StoreCurrentPlan(context.Background(), plan, now); err != nil {
		t.Fatal(err)
	}
	if err := backend.RecordApproval(context.Background(), plan.ID, ApprovalRecord{Actor: "tester", Decision: DecisionApproved, DecidedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-01", Holder: "holder", StartedAt: now.Add(time.Minute), LeaseDuration: time.Minute}); err != nil {
		t.Fatalf("independent Queue preparation was not enabled: %v", err)
	}
}

func TestSQLiteBackendBlocksWorkerIntakeAfterCandidateVerificationFailure(t *testing.T) {
	path := t.TempDir() + "/state.db"
	backend, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	plan := contractPlan(t, "application-a", "lab", "revision-b")
	plan.Operations = []planner.Operation{
		{ID: "op-01", Kind: planner.StageArtifact},
		{ID: "op-06", Kind: planner.StartWorkerCandidate, DependsOn: []string{"op-01"}},
		{ID: "op-07", Kind: planner.VerifyWorkerCandidate, DependsOn: []string{"op-06"}},
		{ID: "op-08", Kind: planner.FenceWorkerIntake, DependsOn: []string{"op-07"}},
		{ID: "op-09", Kind: planner.DrainWorkerPrevious, DependsOn: []string{"op-08"}},
		{ID: "op-10", Kind: planner.ActivateWorkerIntake, DependsOn: []string{"op-09"}},
	}
	plan.ID = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	plan.ID = "sha256:" + hex.EncodeToString(digest[:])
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if err := backend.StoreCurrentPlan(context.Background(), plan, now); err != nil {
		t.Fatal(err)
	}
	if err := backend.RecordApproval(context.Background(), plan.ID, ApprovalRecord{Actor: "tester", Decision: DecisionApproved, DecidedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	complete := func(id string, outcome ExecutionOutcome, offset time.Duration) {
		t.Helper()
		attempt, beginErr := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: id, Holder: "holder", StartedAt: now.Add(offset), LeaseDuration: time.Minute})
		if beginErr != nil {
			t.Fatalf("begin %s: %v", id, beginErr)
		}
		if completeErr := backend.CompleteOperation(context.Background(), CompleteOperationRequest{AttemptID: attempt.AttemptID, Holder: attempt.Holder, PlanID: plan.ID, OperationID: id, FencingToken: attempt.FencingToken, Outcome: outcome, Observation: json.RawMessage(`{"status":"recorded"}`), CompletedAt: now.Add(offset + time.Second)}); completeErr != nil {
			t.Fatalf("complete %s: %v", id, completeErr)
		}
	}
	complete("op-01", ExecutionSucceeded, time.Minute)
	complete("op-06", ExecutionSucceeded, 2*time.Minute)
	complete("op-07", ExecutionFailed, 3*time.Minute)
	for _, id := range []string{"op-08", "op-09", "op-10"} {
		if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: id, Holder: "holder", StartedAt: now.Add(4 * time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "operation dependency") {
			t.Fatalf("%s became executable after failed candidate verification: %v", id, err)
		}
	}
}

func TestSQLiteBackendAllowsArtifactStagingAfterQueuePreparation(t *testing.T) {
	path := t.TempDir() + "/state.db"
	backend, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	plan := contractPlan(t, "application-a", "lab", "revision-a")
	plan.Operations = []planner.Operation{
		{
			ID: "op-01", Kind: planner.PrepareQueue,
			Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Queue: &planner.AsyncQueueInput{
				LogicalID: "provision-lab-messages", GenerationID: "provision-lab-messages-rabbitmq-4-3-6-34fc91a9de04",
			}}},
		},
		{
			ID: "op-02", Kind: planner.StageArtifact, DependsOn: []string{"op-01"},
			Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Artifact: &planner.AsyncArtifactInput{
				Component: "consumer", Role: "worker", Source: "https://artifacts.example/consumer.tar.gz", Digest: "sha256:" + strings.Repeat("3", 64),
			}}},
		},
	}
	plan.ID = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	plan.ID = "sha256:" + hex.EncodeToString(digest[:])
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if err := backend.StoreCurrentPlan(context.Background(), plan, now); err != nil {
		t.Fatal(err)
	}
	if err := backend.RecordApproval(context.Background(), plan.ID, ApprovalRecord{Actor: "tester", Decision: DecisionApproved, DecidedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-02", Holder: "holder", StartedAt: now.Add(time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "op-01 has no recorded outcome") {
		t.Fatalf("asynchronous Artifact staging began before Queue preparation: %v", err)
	}
	queueAttempt, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-01", Holder: "holder", StartedAt: now.Add(2 * time.Minute), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.CompleteOperation(context.Background(), CompleteOperationRequest{AttemptID: queueAttempt.AttemptID, Holder: queueAttempt.Holder, PlanID: plan.ID, OperationID: "op-01", FencingToken: queueAttempt.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"ready"}`), CompletedAt: now.Add(2*time.Minute + time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginOperation(context.Background(), BeginOperationRequest{PlanID: plan.ID, OperationID: "op-02", Holder: "holder", StartedAt: now.Add(3 * time.Minute), LeaseDuration: time.Minute}); err != nil {
		t.Fatalf("asynchronous Artifact staging did not begin after successful Queue preparation: %v", err)
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
	if _, err := backend.BeginOperation(ctx, BeginOperationRequest{PlanID: first.ID, OperationID: "op-01", Holder: "holder-b", ResumeOfAttemptID: "attempt-not-the-latest", StartedAt: now.Add(4 * time.Minute), LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "latest resumable") {
		t.Fatalf("resume of unrelated attempt was accepted: %v", err)
	}
	secondAttempt, err := backend.BeginOperation(ctx, BeginOperationRequest{PlanID: first.ID, OperationID: "op-01", Holder: "holder-b", ResumeOfAttemptID: firstAttempt.AttemptID, StartedAt: now.Add(4 * time.Minute), LeaseDuration: time.Minute})
	if err != nil || secondAttempt.FencingToken <= firstAttempt.FencingToken {
		t.Fatalf("replacement lease = %+v, %v", secondAttempt, err)
	}
	if secondAttempt.ResumeOfAttemptID != firstAttempt.AttemptID {
		t.Fatalf("replacement attempt omitted resume provenance: %+v", secondAttempt)
	}
	if err := backend.CompleteOperation(ctx, CompleteOperationRequest{
		AttemptID: firstAttempt.AttemptID, Holder: firstAttempt.Holder, PlanID: first.ID, OperationID: "op-01",
		FencingToken: firstAttempt.FencingToken, Outcome: ExecutionSucceeded, Observation: json.RawMessage(`{"status":"staged"}`), CompletedAt: now.Add(4*time.Minute + time.Second),
	}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale executor committed: %v", err)
	}
	if err := backend.CompleteOperation(ctx, CompleteOperationRequest{
		AttemptID: secondAttempt.AttemptID, Holder: secondAttempt.Holder, PlanID: first.ID, OperationID: "op-01",
		FencingToken: secondAttempt.FencingToken, Outcome: ExecutionUncertain, Observation: json.RawMessage(`{"status":"uncertain","reason":"recovery requires inspection"}`), CompletedAt: now.Add(4*time.Minute + time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	journal, err := backend.LoadJournal(ctx, first.ID)
	if err != nil || len(journal) != 3 || journal[0].Kind != JournalIntent || journal[2].Kind != JournalOutcome || journal[2].Outcome != ExecutionUncertain {
		t.Fatalf("journal = %+v, %v", journal, err)
	}
	rejected, err := backend.LoadRejectedResults(ctx, first.ID)
	if err != nil || len(rejected) != 1 || rejected[0].SubmittedOutcome != ExecutionSucceeded || !strings.Contains(string(rejected[0].SubmittedObservation), `"status":"staged"`) || !strings.Contains(rejected[0].Reason, "stale") {
		t.Fatalf("rejected stale result = %+v, %v", rejected, err)
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
	if err != nil || len(durableJournal) != 3 || durableJournal[2].Outcome != ExecutionUncertain || !strings.Contains(string(durableJournal[2].Observation), "recovery requires inspection") {
		t.Fatalf("durable journal = %+v, %v", durableJournal, err)
	}
	durableRejected, err := reopened.LoadRejectedResults(ctx, first.ID)
	if err != nil || len(durableRejected) != 1 || durableRejected[0].AttemptID != firstAttempt.AttemptID {
		t.Fatalf("durable rejected results = %+v, %v", durableRejected, err)
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
