package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"provision/internal/authority"
	"provision/internal/operation"
	"provision/internal/planner"
	"provision/internal/state"
)

type Handler interface {
	Execute(context.Context, operation.Envelope) (operation.Result, error)
}

type Engine struct {
	Backend state.Backend
	Signer  authority.Signer
	Handler Handler
	Now     func() time.Time
}

type Request struct {
	PlanID        string
	OperationID   string
	Holder        string
	LeaseDuration time.Duration
}

func NewLocalHolder() (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("create lease holder identity: %w", err)
	}
	return fmt.Sprintf("local-%d-%s", os.Getpid(), hex.EncodeToString(nonce)), nil
}

func (e Engine) Execute(ctx context.Context, request Request) (operation.Result, error) {
	if e.Backend == nil || e.Handler == nil || e.Now == nil {
		return operation.Result{}, errors.New("execution engine is not fully configured")
	}
	snapshot, err := e.Backend.LoadPlanSnapshot(ctx, request.PlanID)
	if err != nil {
		return operation.Result{}, err
	}
	if !snapshot.Plan.Target.Local {
		return operation.Result{}, errors.New("remote Host Target execution is not enabled; use the SSH tracer in issue #7")
	}
	if snapshot.Plan.Capability.Observed.AuthorityKeyID == "" || snapshot.Plan.Capability.Observed.AuthorityKeyID != e.Signer.ID() {
		return operation.Result{}, errors.New("signing key does not match the authority observed in the Plan")
	}
	now := e.Now().UTC()
	attempt, err := e.Backend.BeginOperation(ctx, state.BeginOperationRequest{
		PlanID:        request.PlanID,
		OperationID:   request.OperationID,
		Holder:        request.Holder,
		StartedAt:     now,
		LeaseDuration: request.LeaseDuration,
	})
	if err != nil {
		return operation.Result{}, err
	}
	if attempt.Plan.Target != snapshot.Plan.Target || attempt.Plan.ID != snapshot.Plan.ID {
		return operation.Result{}, errors.New("Plan changed while acquiring its Environment lease")
	}
	operationDigest, err := planner.OperationDigest(attempt.Operation)
	if err != nil {
		return operation.Result{}, e.recordUncertain(ctx, attempt, err)
	}
	proof, err := e.Signer.Sign(authority.Claim{
		PlanID:      attempt.Plan.ID,
		Application: attempt.Plan.Application,
		Environment: attempt.Plan.Environment,
		Target: authority.TargetIdentity{
			Name:           attempt.Plan.Target.Name,
			Local:          attempt.Plan.Target.Local,
			Address:        attempt.Plan.Target.Address,
			Operator:       attempt.Plan.Target.User,
			ExecutorDigest: attempt.Plan.Capability.Observed.ExecutorDigest,
		},
		OperationID:     attempt.Operation.ID,
		OperationKind:   string(attempt.Operation.Kind),
		OperationDigest: operationDigest,
		AttemptID:       attempt.AttemptID,
		FencingToken:    attempt.FencingToken,
		IssuedAt:        now,
		ExpiresAt:       attempt.LeaseExpiresAt,
	})
	if err != nil {
		return operation.Result{}, e.recordUncertain(ctx, attempt, err)
	}
	envelope := operation.Envelope{SchemaVersion: operation.EnvelopeSchemaVersion, Authorization: proof, Operation: attempt.Operation}
	result, executeErr := e.Handler.Execute(ctx, envelope)
	if executeErr != nil {
		return operation.Result{}, e.recordUncertain(ctx, attempt, executeErr)
	}
	if err := result.ValidateAgainst(envelope); err != nil {
		return operation.Result{}, e.recordUncertain(ctx, attempt, err)
	}
	outcome := state.ExecutionSucceeded
	if result.Outcome == operation.OutcomeFailed {
		outcome = state.ExecutionFailed
	}
	if err := e.Backend.CompleteOperation(ctx, state.CompleteOperationRequest{
		AttemptID: attempt.AttemptID, Holder: attempt.Holder,
		PlanID: attempt.Plan.ID, OperationID: attempt.Operation.ID,
		FencingToken: attempt.FencingToken, Outcome: outcome,
		Observation: result.Observation, CompletedAt: e.Now().UTC(),
	}); err != nil {
		return operation.Result{}, err
	}
	if result.Outcome == operation.OutcomeFailed {
		return result, errors.New("host preparation operation failed")
	}
	return result, nil
}

func (e Engine) recordUncertain(ctx context.Context, attempt state.OperationAttempt, cause error) error {
	observation, _ := json.Marshal(struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}{Status: "uncertain", Reason: "host operation did not return a verified result"})
	recordErr := e.Backend.CompleteOperation(ctx, state.CompleteOperationRequest{
		AttemptID: attempt.AttemptID, Holder: attempt.Holder,
		PlanID: attempt.Plan.ID, OperationID: attempt.Operation.ID,
		FencingToken: attempt.FencingToken, Outcome: state.ExecutionUncertain,
		Observation: observation, CompletedAt: e.Now().UTC(),
	})
	if recordErr != nil {
		return fmt.Errorf("%w; uncertain outcome could not be committed: %v", cause, recordErr)
	}
	return fmt.Errorf("%w; outcome recorded as uncertain", cause)
}
