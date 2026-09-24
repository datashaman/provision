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
	apply        func(operation.Envelope) (operation.Result, error)
	verifyErr    error
}

func (h *handlerFake) Observe(context.Context, planner.Operation) (HandlerObservation, error) {
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

func TestEngineRecordsFailureAndUncertainRecoveryEvidence(t *testing.T) {
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		handler      func(*time.Time) *handlerFake
		wantOutcome  state.ExecutionOutcome
		wantKind     state.JournalEventKind
		wantError    bool
		wantEvidence string
	}{
		{
			name: "structured host failure",
			handler: func(_ *time.Time) *handlerFake {
				return &handlerFake{observations: []HandlerObservation{{State: ObservationPending}}, apply: func(envelope operation.Envelope) (operation.Result, error) {
					return matchingResult(envelope, operation.OutcomeFailed, `{"status":"failed","reason":"digest mismatch"}`), nil
				}}
			},
			wantOutcome: state.ExecutionFailed, wantKind: state.JournalOutcome, wantError: true, wantEvidence: "digest mismatch",
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
			wantOutcome: state.ExecutionSucceeded, wantKind: state.JournalRejected, wantError: true, wantEvidence: `"status":"rejected"`,
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
			events, err := backend.LoadJournal(context.Background(), plan.ID)
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
