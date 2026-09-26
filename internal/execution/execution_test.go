package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
	"provision/internal/state"
)

type handlerFake struct {
	observations []HandlerObservation
	observeErr   error
	apply        func(operation.Envelope) (operation.Result, error)
	verifyErr    error
}

func (h *handlerFake) Observe(context.Context, planner.Operation) (HandlerObservation, error) {
	if h.observeErr != nil {
		return HandlerObservation{State: ObservationUnknown}, h.observeErr
	}
	if len(h.observations) == 0 {
		return HandlerObservation{State: ObservationUnknown}, errors.New("no observation configured")
	}
	observed := h.observations[0]
	h.observations = h.observations[1:]
	return observed, nil
}

func (h *handlerFake) ApplyOrResume(_ context.Context, envelope operation.Envelope) (operation.Result, error) {
	return h.apply(envelope)
}

func (h *handlerFake) Verify(envelope operation.Envelope, result operation.Result) error {
	if h.verifyErr != nil {
		return h.verifyErr
	}
	return result.ValidateAgainst(envelope)
}

func (h *handlerFake) Recovery(planned planner.Operation) planner.RecoveryMode {
	return planned.Recovery
}

type cancelingHandler struct {
	cancel                   context.CancelFunc
	observations             int
	satisfyAfterCancellation bool
}

type renewalFailureBackend struct{ state.Backend }

func (renewalFailureBackend) RenewExecutionLease(context.Context, state.RenewExecutionLeaseRequest) (time.Time, error) {
	return time.Time{}, errors.New("renewal unavailable")
}

type blockingHandler struct{ observations int }

type initialCancellationHandler struct {
	cancel    context.CancelFunc
	satisfied bool
}

func (h *initialCancellationHandler) Observe(context.Context, planner.Operation) (HandlerObservation, error) {
	h.cancel()
	if h.satisfied {
		return HandlerObservation{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)}, nil
	}
	return HandlerObservation{State: ObservationUnknown}, errors.New("initial observation canceled")
}

func (*initialCancellationHandler) ApplyOrResume(context.Context, operation.Envelope) (operation.Result, error) {
	return operation.Result{}, errors.New("must not apply")
}

func (*initialCancellationHandler) Verify(operation.Envelope, operation.Result) error { return nil }
func (*initialCancellationHandler) Recovery(planned planner.Operation) planner.RecoveryMode {
	return planned.Recovery
}

func (h *blockingHandler) Observe(context.Context, planner.Operation) (HandlerObservation, error) {
	h.observations++
	return HandlerObservation{State: ObservationPending, Evidence: json.RawMessage(`{"status":"absent"}`)}, nil
}

func (*blockingHandler) ApplyOrResume(ctx context.Context, _ operation.Envelope) (operation.Result, error) {
	<-ctx.Done()
	return operation.Result{}, ctx.Err()
}

func (*blockingHandler) Verify(operation.Envelope, operation.Result) error { return nil }
func (*blockingHandler) Recovery(planned planner.Operation) planner.RecoveryMode {
	return planned.Recovery
}

func (h *cancelingHandler) Observe(context.Context, planner.Operation) (HandlerObservation, error) {
	h.observations++
	if h.satisfyAfterCancellation && h.observations > 1 {
		return HandlerObservation{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)}, nil
	}
	return HandlerObservation{State: ObservationPending, Evidence: json.RawMessage(`{"status":"absent"}`)}, nil
}

func (h *cancelingHandler) ApplyOrResume(ctx context.Context, _ operation.Envelope) (operation.Result, error) {
	h.cancel()
	<-ctx.Done()
	return operation.Result{}, ctx.Err()
}

func (*cancelingHandler) Verify(operation.Envelope, operation.Result) error { return nil }
func (*cancelingHandler) Recovery(planned planner.Operation) planner.RecoveryMode {
	return planned.Recovery
}

func TestEngineRecordsFailureAndUncertainRecoveryEvidence(t *testing.T) {
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		handler       func(*time.Time) *handlerFake
		wantOutcome   state.ExecutionOutcome
		wantKind      state.JournalEventKind
		wantError     bool
		wantErrorText string
		wantEvidence  string
		wantRejected  bool
	}{
		{
			name: "initial observation failure",
			handler: func(_ *time.Time) *handlerFake {
				return &handlerFake{observeErr: errors.New("observation unavailable"), apply: func(operation.Envelope) (operation.Result, error) {
					return operation.Result{}, errors.New("must not apply")
				}}
			},
			wantOutcome: state.ExecutionUncertain, wantKind: state.JournalOutcome, wantError: true, wantEvidence: "observation unavailable",
		},
		{
			name: "structured host failure",
			handler: func(_ *time.Time) *handlerFake {
				return &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, apply: func(envelope operation.Envelope) (operation.Result, error) {
					return matchingResult(envelope, operation.OutcomeFailed, `{"status":"failed","reason":"digest mismatch"}`), nil
				}}
			},
			wantOutcome: state.ExecutionFailed, wantKind: state.JournalOutcome, wantError: true,
			wantErrorText: "operation op-01 (stageArtifact) failed: digest mismatch", wantEvidence: "digest mismatch",
		},
		{
			name: "structured uncertain host outcome",
			handler: func(_ *time.Time) *handlerFake {
				return &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, apply: func(envelope operation.Envelope) (operation.Result, error) {
					return matchingResult(envelope, operation.OutcomeUncertain, `{"status":"uncertain","reason":"rollback cannot be proven"}`), nil
				}}
			},
			wantOutcome: state.ExecutionUncertain, wantKind: state.JournalOutcome, wantError: true, wantEvidence: "rollback cannot be proven",
		},
		{
			name: "result verification failure",
			handler: func(_ *time.Time) *handlerFake {
				return &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, verifyErr: errors.New("result evidence invalid"), apply: func(envelope operation.Envelope) (operation.Result, error) {
					return matchingResult(envelope, operation.OutcomeSucceeded, `{"status":"staged"}`), nil
				}}
			},
			wantOutcome: state.ExecutionUncertain, wantKind: state.JournalOutcome, wantError: true, wantEvidence: "result evidence invalid",
		},
		{
			name: "uncertain call observed as completed",
			handler: func(_ *time.Time) *handlerFake {
				return &handlerFake{observations: []HandlerObservation{
					{State: ObservationPending},
					{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)},
				}, apply: func(operation.Envelope) (operation.Result, error) {
					return operation.Result{}, errors.New("connection lost")
				}}
			},
			wantOutcome: state.ExecutionSucceeded, wantKind: state.JournalOutcome, wantEvidence: "already-present",
		},
		{
			name: "uncertain call remains unobserved",
			handler: func(_ *time.Time) *handlerFake {
				return &handlerFake{observations: []HandlerObservation{
					{State: ObservationPending},
					{State: ObservationPending, Evidence: json.RawMessage(`{"status":"absent"}`)},
				}, apply: func(operation.Envelope) (operation.Result, error) {
					return operation.Result{}, errors.New("connection lost")
				}}
			},
			wantOutcome: state.ExecutionUncertain, wantKind: state.JournalOutcome, wantError: true, wantEvidence: `"observed":"pending"`,
		},
		{
			name: "lease expires before result commit",
			handler: func(now *time.Time) *handlerFake {
				return &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, apply: func(envelope operation.Envelope) (operation.Result, error) {
					*now = start.Add(2 * time.Minute)
					return matchingResult(envelope, operation.OutcomeSucceeded, `{"status":"staged"}`), nil
				}}
			},
			wantOutcome: state.ExecutionSucceeded, wantError: true, wantEvidence: `"status":"staged"`, wantRejected: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := start
			engine, backend, plan := executionFixture(t, &now, test.handler(&now))
			defer backend.Close()
			_, err := engine.Execute(context.Background(), Request{PlanID: plan.ID, OperationID: "op-01", Holder: "test-holder", LeaseDuration: time.Minute})
			if (err != nil) != test.wantError {
				t.Fatalf("Execute error = %v, wantError=%v", err, test.wantError)
			}
			if test.wantErrorText != "" && (err == nil || !strings.Contains(err.Error(), test.wantErrorText)) {
				t.Fatalf("Execute error = %v, want text %q", err, test.wantErrorText)
			}
			events, err := backend.LoadJournal(context.Background(), plan.ID)
			if test.wantRejected {
				if err != nil || len(events) != 1 {
					t.Fatalf("authoritative journal = %+v, %v", events, err)
				}
				rejected, rejectErr := backend.LoadRejectedResults(context.Background(), plan.ID)
				if rejectErr != nil || len(rejected) != 1 || rejected[0].SubmittedOutcome != test.wantOutcome || !strings.Contains(string(rejected[0].SubmittedObservation), test.wantEvidence) {
					t.Fatalf("rejected results = %+v, %v", rejected, rejectErr)
				}
				return
			}
			if err != nil || len(events) != 2 {
				t.Fatalf("journal = %+v, %v", events, err)
			}
			last := events[1]
			if last.Kind != test.wantKind || last.Outcome != test.wantOutcome || !strings.Contains(string(last.Observation), test.wantEvidence) {
				t.Fatalf("last journal event = %+v", last)
			}
		})
	}
}

func TestEngineJournalsInitialObservationCancellation(t *testing.T) {
	for _, satisfied := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain", true: "satisfied"}[satisfied], func(t *testing.T) {
			now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
			ctx, cancel := context.WithCancel(context.Background())
			handler := &initialCancellationHandler{cancel: cancel, satisfied: satisfied}
			engine, backend, plan := executionFixture(t, &now, handler)
			defer backend.Close()
			result, err := engine.Execute(ctx, Request{PlanID: plan.ID, OperationID: "op-01", Holder: "test-holder", LeaseDuration: time.Minute})
			wantOutcome := state.ExecutionUncertain
			if satisfied {
				wantOutcome = state.ExecutionSucceeded
				if err != nil || result.Outcome != operation.OutcomeSucceeded {
					t.Fatalf("satisfied initial observation = %+v, %v", result, err)
				}
			} else if err == nil {
				t.Fatal("canceled initial observation unexpectedly succeeded")
			}
			events, journalErr := backend.LoadJournal(context.Background(), plan.ID)
			if journalErr != nil || len(events) != 2 || events[1].Outcome != wantOutcome {
				t.Fatalf("initial cancellation journal = %+v, %v", events, journalErr)
			}
		})
	}
}

func TestEngineJournalsAfterCallerCancellation(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	handler := &cancelingHandler{cancel: cancel}
	engine, backend, plan := executionFixture(t, &now, handler)
	defer backend.Close()
	_, err := engine.Execute(ctx, Request{PlanID: plan.ID, OperationID: "op-01", Holder: "test-holder", LeaseDuration: time.Minute})
	if err == nil || handler.observations != 2 {
		t.Fatalf("canceled execution = %v, observations=%d", err, handler.observations)
	}
	events, journalErr := backend.LoadJournal(context.Background(), plan.ID)
	if journalErr != nil || len(events) != 2 || events[1].Kind != state.JournalOutcome || events[1].Outcome != state.ExecutionUncertain || !strings.Contains(string(events[1].Observation), `"observed":"pending"`) {
		t.Fatalf("canceled execution journal = %+v, %v", events, journalErr)
	}
}

func TestEngineCommitsObservedSuccessAfterCallerCancellation(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	handler := &cancelingHandler{cancel: cancel, satisfyAfterCancellation: true}
	engine, backend, plan := executionFixture(t, &now, handler)
	defer backend.Close()
	result, err := engine.Execute(ctx, Request{PlanID: plan.ID, OperationID: "op-01", Holder: "test-holder", LeaseDuration: time.Minute})
	if err != nil || result.Outcome != operation.OutcomeSucceeded || handler.observations != 2 {
		t.Fatalf("recovered canceled execution = %+v, %v, observations=%d", result, err, handler.observations)
	}
	events, journalErr := backend.LoadJournal(context.Background(), plan.ID)
	if journalErr != nil || len(events) != 2 || events[1].Outcome != state.ExecutionSucceeded || !strings.Contains(string(events[1].Observation), "already-present") {
		t.Fatalf("recovered cancellation journal = %+v, %v", events, journalErr)
	}
}

func TestEngineCancelsAndJournalsWhenLeaseRenewalFails(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	handler := &blockingHandler{}
	engine, backend, plan := executionFixture(t, &now, handler)
	defer backend.Close()
	engine.Backend = renewalFailureBackend{Backend: backend}
	engine.renewalInterval = time.Millisecond
	_, err := engine.Execute(context.Background(), Request{PlanID: plan.ID, OperationID: "op-01", Holder: "test-holder", LeaseDuration: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "renewal unavailable") || handler.observations != 2 {
		t.Fatalf("renewal failure = %v, observations=%d", err, handler.observations)
	}
	events, journalErr := backend.LoadJournal(context.Background(), plan.ID)
	if journalErr != nil || len(events) != 2 || events[1].Outcome != state.ExecutionUncertain || !strings.Contains(string(events[1].Observation), "renewal unavailable") {
		t.Fatalf("renewal failure journal = %+v, %v", events, journalErr)
	}
}

func TestEngineResumesInterruptedOperationFromFreshSatisfiedObservation(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	applyCalled := false
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)}}, apply: func(operation.Envelope) (operation.Result, error) {
		applyCalled = true
		return operation.Result{}, errors.New("must not replay a satisfied operation")
	}}
	engine, backend, plan := executionFixture(t, &now, handler)
	defer backend.Close()
	interrupted, err := backend.BeginOperation(context.Background(), state.BeginOperationRequest{
		PlanID: plan.ID, OperationID: "op-01", Holder: "interrupted-holder", StartedAt: now, LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	result, err := engine.Resume(context.Background(), ResumeRequest{PlanID: plan.ID, Holder: "resume-holder", LeaseDuration: time.Minute})
	if err != nil || result.Outcome != operation.OutcomeSucceeded || result.OperationID != "op-01" || result.AttemptID == interrupted.AttemptID || result.FencingToken <= interrupted.FencingToken || applyCalled {
		t.Fatalf("resumed result = %+v, %v, applyCalled=%t", result, err, applyCalled)
	}
	events, err := backend.LoadJournal(context.Background(), plan.ID)
	if err != nil || len(events) != 3 || events[1].Kind != state.JournalIntent || !strings.Contains(string(events[1].Observation), interrupted.AttemptID) || events[2].Outcome != state.ExecutionSucceeded {
		t.Fatalf("resume journal = %+v, %v", events, err)
	}
	lateObservation := json.RawMessage(`{"status":"staged"}`)
	lateErr := backend.CompleteOperation(context.Background(), state.CompleteOperationRequest{
		AttemptID: interrupted.AttemptID, Holder: interrupted.Holder, PlanID: plan.ID, OperationID: "op-01",
		FencingToken: interrupted.FencingToken, Outcome: state.ExecutionSucceeded, Observation: lateObservation, CompletedAt: now,
	})
	if lateErr == nil || !strings.Contains(lateErr.Error(), "stale") {
		t.Fatalf("stale interrupted executor committed after resume: %v", lateErr)
	}
	rejected, err := backend.LoadRejectedResults(context.Background(), plan.ID)
	if err != nil || len(rejected) != 1 || rejected[0].AttemptID != interrupted.AttemptID {
		t.Fatalf("rejected interrupted result = %+v, %v", rejected, err)
	}
}

func TestEngineResumePausesOnAmbiguousFreshObservation(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	applyCalled := false
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationUnknown, Evidence: json.RawMessage(`{"status":"unknown","reason":"route drift"}`)}}, apply: func(operation.Envelope) (operation.Result, error) {
		applyCalled = true
		return operation.Result{}, errors.New("must not replay an ambiguous operation")
	}}
	engine, backend, plan := executionFixture(t, &now, handler)
	defer backend.Close()
	interrupted, err := backend.BeginOperation(context.Background(), state.BeginOperationRequest{
		PlanID: plan.ID, OperationID: "op-01", Holder: "interrupted-holder", StartedAt: now, LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	_, err = engine.Resume(context.Background(), ResumeRequest{PlanID: plan.ID, Holder: "resume-holder", LeaseDuration: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "mutation was not replayed") || applyCalled {
		t.Fatalf("ambiguous resume = %v, applyCalled=%t", err, applyCalled)
	}
	events, journalErr := backend.LoadJournal(context.Background(), plan.ID)
	if journalErr != nil || len(events) != 3 || events[2].Outcome != state.ExecutionUncertain || !strings.Contains(string(events[2].Observation), interrupted.AttemptID) || !strings.Contains(string(events[2].Observation), "recoveryAction") || !strings.Contains(string(events[2].Observation), "route drift") {
		t.Fatalf("ambiguous resume journal = %+v, %v", events, journalErr)
	}
	uncertainAttemptID := events[2].AttemptID
	handler.observations = append(handler.observations, HandlerObservation{State: ObservationSatisfied, Evidence: json.RawMessage(`{"status":"already-present"}`)})
	now = now.Add(time.Second)
	result, err := engine.Resume(context.Background(), ResumeRequest{PlanID: plan.ID, Holder: "recovery-holder", LeaseDuration: time.Minute})
	if err != nil || result.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("explicit recovery after uncertain observation = %+v, %v", result, err)
	}
	events, journalErr = backend.LoadJournal(context.Background(), plan.ID)
	if journalErr != nil || len(events) != 5 || !strings.Contains(string(events[3].Observation), uncertainAttemptID) || events[4].Outcome != state.ExecutionSucceeded {
		t.Fatalf("recovered uncertain journal = %+v, %v", events, journalErr)
	}
}

func TestEngineResumeAppliesOnlyAfterFreshPendingObservation(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	applyCalled := false
	handler := &handlerFake{observations: []HandlerObservation{{State: ObservationPending, Evidence: json.RawMessage(`{"status":"absent"}`)}}, apply: func(envelope operation.Envelope) (operation.Result, error) {
		applyCalled = true
		return matchingResult(envelope, operation.OutcomeSucceeded, `{"status":"staged"}`), nil
	}}
	engine, backend, plan := executionFixture(t, &now, handler)
	defer backend.Close()
	interrupted, err := backend.BeginOperation(context.Background(), state.BeginOperationRequest{
		PlanID: plan.ID, OperationID: "op-01", Holder: "interrupted-holder", StartedAt: now, LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	result, err := engine.Resume(context.Background(), ResumeRequest{PlanID: plan.ID, Holder: "resume-holder", LeaseDuration: time.Minute})
	if err != nil || result.Outcome != operation.OutcomeSucceeded || !applyCalled {
		t.Fatalf("pending resume = %+v, %v, applyCalled=%t", result, err, applyCalled)
	}
	events, journalErr := backend.LoadJournal(context.Background(), plan.ID)
	if journalErr != nil || len(events) != 3 || !strings.Contains(string(events[1].Observation), interrupted.AttemptID) || events[2].Outcome != state.ExecutionSucceeded {
		t.Fatalf("pending resume journal = %+v, %v", events, journalErr)
	}
}

func TestEngineResumeRequiresInterruptedOrUncertainJournalState(t *testing.T) {
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	handler := &handlerFake{}
	engine, backend, plan := executionFixture(t, &now, handler)
	defer backend.Close()
	if _, err := engine.Resume(context.Background(), ResumeRequest{PlanID: plan.ID, Holder: "resume-holder", LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "no interrupted") {
		t.Fatalf("empty deployment resumed: %v", err)
	}
	attempt, err := backend.BeginOperation(context.Background(), state.BeginOperationRequest{
		PlanID: plan.ID, OperationID: "op-01", Holder: "failed-holder", StartedAt: now, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.CompleteOperation(context.Background(), state.CompleteOperationRequest{
		AttemptID: attempt.AttemptID, Holder: attempt.Holder, PlanID: plan.ID, OperationID: "op-01",
		FencingToken: attempt.FencingToken, Outcome: state.ExecutionFailed, Observation: json.RawMessage(`{"status":"failed"}`), CompletedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, err := engine.Resume(context.Background(), ResumeRequest{PlanID: plan.ID, Holder: "resume-holder", LeaseDuration: time.Minute}); err == nil || !strings.Contains(err.Error(), "known failed outcome") {
		t.Fatalf("known failed operation resumed automatically: %v", err)
	}
}

func executionFixture(t *testing.T, now *time.Time, handler Handler) (Engine, state.Backend, planner.Plan) {
	t.Helper()
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "authority.key")
	publicPath := filepath.Join(dir, "authority.pub")
	if _, err := authority.GenerateKeyPair(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	signer, err := authority.LoadSigner(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan{
		SchemaVersion: planner.SchemaVersion, Application: "application", Environment: "lab", Revision: "revision",
		ConfigurationDigest: "sha256:" + strings.Repeat("1", 64), ArtifactDigests: map[string]string{"web": "sha256:" + strings.Repeat("3", 64)},
		Target: planner.Target{Name: "current", Kind: "host", Local: true, User: "operator"}, ObservationDigest: "sha256:" + strings.Repeat("2", 64),
		Capability:           planner.CapabilityEvidence{Observed: structHostObservation(signer.ID())},
		ApprovalRequirements: []planner.ApprovalRequirement{{Capability: "approve", Reason: "test"}},
		Operations: []planner.Operation{{
			ID: "op-01", Kind: planner.StageArtifact, DependsOn: []string{}, Recovery: planner.DiscardStaged,
			Input: planner.OperationInput{Artifact: &planner.ArtifactInput{Source: "https://artifacts.example/release.tar.gz", Digest: "sha256:" + strings.Repeat("3", 64)}},
		}},
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	plan.ID = "sha256:" + hex.EncodeToString(digest[:])
	backend, err := state.OpenSQLite(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.StoreCurrentPlan(context.Background(), plan, *now); err != nil {
		t.Fatal(err)
	}
	if err := backend.RecordApproval(context.Background(), plan.ID, state.ApprovalRecord{Actor: "operator", Decision: state.DecisionApproved, DecidedAt: *now, ExpiresAt: (*now).Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return Engine{Backend: backend, Signer: signer, Handler: handler, Now: func() time.Time { return *now }}, backend, plan
}

func structHostObservation(keyID string) host.BootstrapStatus {
	return host.BootstrapStatus{AuthorityKeyID: keyID, ExecutorDigest: "sha256:" + strings.Repeat("e", 64)}
}

func matchingResult(envelope operation.Envelope, outcome operation.Outcome, observed string) operation.Result {
	claim := envelope.Authorization.Claim
	return operation.Result{
		SchemaVersion: operation.ResultSchemaVersion, PlanID: claim.PlanID, OperationID: claim.OperationID,
		AttemptID: claim.AttemptID, FencingToken: claim.FencingToken, Outcome: outcome, Observation: json.RawMessage(observed),
	}
}
