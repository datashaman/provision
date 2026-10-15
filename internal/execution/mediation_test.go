package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"provision/internal/config"
	"provision/internal/operation"
	"provision/internal/planner"
	"provision/internal/state"
)

func mediatedEngine(t *testing.T, now *time.Time, handler Handler) (Engine, state.RequestBackend, planner.Plan, state.RequestApproval) {
	t.Helper()
	stage := planner.Operation{ID: "op-01", Kind: planner.StageArtifact, DependsOn: []string{}, Recovery: planner.DiscardStaged, Input: planner.OperationInput{Artifact: &planner.ArtifactInput{Source: "https://artifacts.example/release.tar.gz", Digest: "sha256:" + strings.Repeat("3", 64)}}}
	install := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}}
	engine, base, plan := executionFixture(t, now, handler, stage, install)
	t.Cleanup(func() { base.Close() })
	backend := base.(state.RequestBackend)
	captured := state.RequestCapture{SchemaVersion: state.RequestCaptureSchema, ID: "request-one", Requester: "agent", UID: 1234, SubmissionID: "one", SubmissionDigest: "captured-bytes", Submission: []byte(`{"id":"one"}`), Application: plan.Application, Environment: plan.Environment, Revision: plan.Revision, Component: "web", Operation: planner.StageArtifact, Configuration: config.Compiled{Digest: plan.ConfigurationDigest, Revision: config.Revision{Artifacts: map[string]config.Artifact{"web": {Source: stage.Input.Artifact.Source, Digest: stage.Input.Artifact.Digest}}}}, CapturedAt: *now}
	if _, err := backend.CaptureRequest(context.Background(), captured); err != nil {
		t.Fatal(err)
	}
	if err := backend.StoreRequestPlan(context.Background(), captured.ID, plan, "op-01", *now); err != nil {
		t.Fatal(err)
	}
	digest, err := planner.OperationDigest(stage)
	if err != nil {
		t.Fatal(err)
	}
	a := state.RequestApproval{SchemaVersion: state.RequestApprovalSchema, RequestID: captured.ID, PlanID: plan.ID, OperationID: "op-01", OperationDigest: digest, SigningKeyID: engine.Signer.ID(), Actor: "human", Decision: "approved", DecidedAt: *now, ExpiresAt: now.Add(time.Minute)}
	// The backend still enforces the binding when a caller passes this preliminary policy check.
	engine.Authorize = func(context.Context, string, string, time.Time) error { return nil }
	return engine, backend, plan, a
}

func TestMediatedExecutionRejectsPlanWideApprovalAndAnotherOperation(t *testing.T) {
	now := time.Now().UTC()
	handler := &handlerFake{apply: func(operation.Envelope) (operation.Result, error) {
		t.Fatal("unauthorized mutation")
		return operation.Result{}, nil
	}}
	engine, backend, plan, a := mediatedEngine(t, &now, handler)
	for _, request := range []Request{
		{PlanID: plan.ID, OperationID: "op-01", Holder: "generic", LeaseDuration: time.Minute},
		{RequestID: a.RequestID, PlanID: plan.ID, OperationID: "op-01", Holder: "unapproved", LeaseDuration: time.Minute},
	} {
		if _, err := engine.Execute(context.Background(), request); err == nil {
			t.Fatal("Plan-wide approval substituted for request approval")
		}
	}
	if err := backend.RecordRequestApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for _, request := range []Request{
		{PlanID: plan.ID, OperationID: "op-01", Holder: "generic", LeaseDuration: time.Minute},
		{RequestID: a.RequestID, PlanID: plan.ID, OperationID: "op-02", Holder: "installation", LeaseDuration: time.Minute},
		{RequestID: "other-request", PlanID: plan.ID, OperationID: "op-01", Holder: "substitute", LeaseDuration: time.Minute},
	} {
		if _, err := engine.Execute(context.Background(), request); err == nil {
			t.Fatal("approval binding bypassed")
		}
	}
	events, err := backend.LoadJournal(context.Background(), plan.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("unauthorized attempts reached journal: %+v %v", events, err)
	}
}

func TestMediatedDispatchExpiresButAlreadyDispatchedWorkCanFinish(t *testing.T) {
	now := time.Now().UTC()
	var signedUntil time.Time
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, apply: func(e operation.Envelope) (operation.Result, error) {
		signedUntil = e.Authorization.Claim.ExpiresAt
		now = now.Add(2 * time.Second)
		return matchingResult(e, operation.OutcomeSucceeded, `{"status":"staged"}`), nil
	}}
	engine, backend, plan, a := mediatedEngine(t, &now, handler)
	a.ExpiresAt = now.Add(time.Second)
	if err := backend.RecordRequestApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	request := Request{RequestID: a.RequestID, PlanID: plan.ID, OperationID: "op-01", Holder: "bounded", LeaseDuration: time.Minute}
	result, err := engine.Execute(context.Background(), request)
	if err != nil || result.Outcome != operation.OutcomeSucceeded || !signedUntil.Equal(a.ExpiresAt) {
		t.Fatalf("in-flight work=%+v %v expiry=%s", result, err, signedUntil)
	}
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("expired approval dispatched again")
	}
	events, err := backend.LoadJournal(context.Background(), plan.ID)
	if err != nil || len(events) != 2 || events[1].Outcome != state.ExecutionSucceeded {
		t.Fatalf("late completion was not journaled: %+v %v", events, err)
	}
}

func TestMediatedKnownFailureRequiresFreshHumanDecision(t *testing.T) {
	now := time.Now().UTC()
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, apply: func(e operation.Envelope) (operation.Result, error) {
		now = now.Add(time.Second)
		return matchingResult(e, operation.OutcomeFailed, `{"status":"failed","reason":"known failure"}`), nil
	}}
	engine, backend, plan, a := mediatedEngine(t, &now, handler)
	if err := backend.RecordRequestApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	request := Request{RequestID: a.RequestID, PlanID: plan.ID, OperationID: "op-01", Holder: "failure", LeaseDuration: time.Minute}
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("expected failed result")
	}
	if _, err := engine.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "fresh human decision") {
		t.Fatalf("failure retried: %v", err)
	}
	now = now.Add(time.Second)
	a.DecidedAt = now
	if err := backend.RecordRequestApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	handler.observations = []HandlerObservation{{State: ObservationPending}}
	handler.apply = func(e operation.Envelope) (operation.Result, error) {
		return matchingResult(e, operation.OutcomeSucceeded, `{"status":"staged"}`), nil
	}
	if _, err := engine.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "completed request") {
		t.Fatalf("completed request repeated: %v", err)
	}
}

func TestMediatedRecoveryObservesBeforeResumingBoundOperation(t *testing.T) {
	now := time.Now().UTC()
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationPending}, {State: ObservationUnknown}}, apply: func(operation.Envelope) (operation.Result, error) {
		return operation.Result{}, errors.New("connection lost")
	}}
	engine, backend, plan, a := mediatedEngine(t, &now, handler)
	if err := backend.RecordRequestApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	request := Request{RequestID: a.RequestID, PlanID: plan.ID, OperationID: "op-01", Holder: "first", LeaseDuration: time.Minute}
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("expected uncertain result")
	}
	if _, err := engine.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "resume") {
		t.Fatalf("uncertain replay without observation: %v", err)
	}
	handler.observations = []HandlerObservation{{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)}}
	handler.apply = func(operation.Envelope) (operation.Result, error) {
		t.Fatal("already completed operation replayed")
		return operation.Result{}, nil
	}
	if _, err := engine.Resume(context.Background(), ResumeRequest{RequestID: a.RequestID, PlanID: plan.ID, Holder: "recovery", LeaseDuration: time.Minute}); err != nil {
		t.Fatal(err)
	}
	events, err := backend.LoadJournal(context.Background(), plan.ID)
	if err != nil || len(events) != 4 || !strings.Contains(string(events[2].Observation), "resumeOfAttemptId") {
		t.Fatalf("recovery provenance=%+v %v", events, err)
	}
}

func TestMediatedInFlightLeaseCanRenewAfterApprovalExpiry(t *testing.T) {
	now := time.Now().UTC()
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	var expires time.Time
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, apply: func(e operation.Envelope) (operation.Result, error) {
		clock.Store(expires.Add(time.Second).UnixNano())
		time.Sleep(50 * time.Millisecond)
		return matchingResult(e, operation.OutcomeSucceeded, `{"status":"staged"}`), nil
	}}
	engine, backend, plan, a := mediatedEngine(t, &now, handler)
	a.ExpiresAt = now.Add(time.Second)
	expires = a.ExpiresAt
	if err := backend.RecordRequestApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	engine.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	engine.renewalInterval = time.Millisecond
	if result, err := engine.Execute(context.Background(), Request{RequestID: a.RequestID, PlanID: plan.ID, OperationID: "op-01", Holder: "renewing", LeaseDuration: time.Minute}); err != nil || result.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("in-flight renewal after approval expiry=%+v %v", result, err)
	}
}

func TestMediatedRecoveryCannotCrossRequestsSharingOnePlan(t *testing.T) {
	now := time.Now().UTC()
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationPending}, {State: ObservationUnknown}}, apply: func(operation.Envelope) (operation.Result, error) {
		return operation.Result{}, errors.New("lost response")
	}}
	engine, backend, plan, a := mediatedEngine(t, &now, handler)
	if err := backend.RecordRequestApproval(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	request := Request{RequestID: a.RequestID, PlanID: plan.ID, OperationID: "op-01", Holder: "a", LeaseDuration: time.Minute}
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("expected uncertain A")
	}
	captureA, err := backend.LoadRequest(context.Background(), a.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	captureB := captureA.Capture
	captureB.ID = "request-two"
	captureB.SubmissionID = "two"
	captureB.Submission = []byte(`{"id":"two"}`)
	if _, err := backend.CaptureRequest(context.Background(), captureB); err != nil {
		t.Fatal(err)
	}
	if err := backend.StoreRequestPlan(context.Background(), captureB.ID, plan, "op-01", now); err != nil {
		t.Fatal(err)
	}
	b := a
	b.RequestID = captureB.ID
	if err := backend.RecordRequestApproval(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	handler.observations = []HandlerObservation{{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)}}
	if _, err := engine.Resume(context.Background(), ResumeRequest{RequestID: b.RequestID, PlanID: plan.ID, Holder: "b-resume", LeaseDuration: time.Minute}); err == nil {
		t.Fatal("B resumed A's uncertain attempt")
	}
	// The persistence boundary also rejects a direct attempt to borrow A's resume provenance.
	events, err := backend.LoadJournal(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.BeginOperation(context.Background(), state.BeginOperationRequest{RequestID: b.RequestID, SigningKeyID: engine.Signer.ID(), PlanID: plan.ID, OperationID: "op-01", Holder: "b-direct-resume", ResumeOfAttemptID: events[0].AttemptID, StartedAt: now, LeaseDuration: time.Minute}); err == nil {
		t.Fatal("backend accepted cross-request resume provenance")
	}
	handler.observations = []HandlerObservation{{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)}}
	if _, err := engine.Execute(context.Background(), Request{RequestID: b.RequestID, PlanID: plan.ID, OperationID: "op-01", Holder: "b", LeaseDuration: time.Minute}); err != nil {
		t.Fatal(err)
	}
	handler.observations = []HandlerObservation{{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)}}
	if _, err := engine.Resume(context.Background(), ResumeRequest{RequestID: a.RequestID, PlanID: plan.ID, Holder: "a-resume", LeaseDuration: time.Minute}); err != nil {
		t.Fatalf("B's success hid A's resumable attempt: %v", err)
	}
	events, err = backend.LoadJournal(context.Background(), plan.ID)
	if err != nil || len(events) != 6 || !strings.Contains(string(events[4].Observation), events[0].AttemptID) {
		t.Fatalf("A recovery provenance=%+v %v", events, err)
	}
}
