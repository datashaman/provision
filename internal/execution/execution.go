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
	Observe(context.Context, planner.Operation) (HandlerObservation, error)
	ApplyOrResume(context.Context, operation.Envelope) (operation.Result, error)
	Verify(operation.Envelope, operation.Result) error
	Recovery(planner.Operation) planner.RecoveryMode
}

type ObservationState string

const (
	ObservationPending   ObservationState = "pending"
	ObservationSatisfied ObservationState = "satisfied"
	ObservationUnknown   ObservationState = "unknown"
)

type HandlerObservation struct {
	State    ObservationState
	Evidence json.RawMessage
}

type Engine struct {
	Backend         state.Backend
	Signer          authority.Signer
	Handler         Handler
	Now             func() time.Time
	renewalInterval time.Duration
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
		return operation.Result{}, errors.New("Plan changed while acquiring its fenced execution lease")
	}
	operationDigest, err := planner.OperationDigest(attempt.Operation)
	if err != nil {
		return operation.Result{}, e.recordUncertain(ctx, attempt, err, HandlerObservation{State: ObservationUnknown})
	}
	proof, err := e.Signer.Sign(authority.Claim{
		PlanID:      attempt.Plan.ID,
		Application: attempt.Plan.Application,
		Environment: attempt.Plan.Environment,
		Target: authority.TargetIdentity{
			Name:                  attempt.Plan.Target.Name,
			Local:                 attempt.Plan.Target.Local,
			Address:               attempt.Plan.Target.Address,
			Operator:              attempt.Plan.Target.User,
			ExecutorDigest:        attempt.Plan.Capability.Observed.ExecutorDigest,
			SSHHostKeyFingerprint: attempt.Plan.Capability.Observed.SSHHostKeyFingerprint,
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
		return operation.Result{}, e.recordUncertain(ctx, attempt, err, HandlerObservation{State: ObservationUnknown})
	}
	envelope := operation.Envelope{SchemaVersion: operation.EnvelopeSchemaVersion, Authorization: proof, Operation: attempt.Operation}
	before, err := e.Handler.Observe(ctx, attempt.Operation)
	if err != nil {
		journalContext, cancelJournal := failureContext(ctx)
		defer cancelJournal()
		return operation.Result{}, e.recordUncertain(journalContext, attempt, err, HandlerObservation{State: ObservationUnknown})
	}
	if before.State == ObservationSatisfied {
		result := operation.Result{
			SchemaVersion: operation.ResultSchemaVersion, PlanID: attempt.Plan.ID, OperationID: attempt.Operation.ID,
			AttemptID: attempt.AttemptID, FencingToken: attempt.FencingToken, Outcome: operation.OutcomeSucceeded, Observation: before.Evidence,
		}
		commitContext, cancelCommit := failureContext(ctx)
		defer cancelCommit()
		if err := e.commitResult(commitContext, attempt, envelope, result); err != nil {
			return operation.Result{}, err
		}
		return result, nil
	}
	result, executeErr := e.executeWithLeaseRenewal(ctx, request, attempt, envelope)
	if executeErr != nil {
		observationContext, cancelObservation := failureContext(ctx)
		after, observeErr := e.Handler.Observe(observationContext, attempt.Operation)
		cancelObservation()
		if observeErr == nil && after.State == ObservationSatisfied {
			result = operation.Result{
				SchemaVersion: operation.ResultSchemaVersion, PlanID: attempt.Plan.ID, OperationID: attempt.Operation.ID,
				AttemptID: attempt.AttemptID, FencingToken: attempt.FencingToken, Outcome: operation.OutcomeSucceeded, Observation: after.Evidence,
			}
			commitContext, cancelCommit := failureContext(ctx)
			defer cancelCommit()
			if err := e.commitResult(commitContext, attempt, envelope, result); err != nil {
				return operation.Result{}, err
			}
			return result, nil
		}
		if observeErr != nil {
			executeErr = fmt.Errorf("%w; post-failure observation: %v", executeErr, observeErr)
			after = HandlerObservation{State: ObservationUnknown}
		}
		journalContext, cancelJournal := failureContext(ctx)
		defer cancelJournal()
		return operation.Result{}, e.recordUncertain(journalContext, attempt, executeErr, after)
	}
	if err := e.commitResult(ctx, attempt, envelope, result); err != nil {
		return operation.Result{}, err
	}
	if result.Outcome == operation.OutcomeFailed {
		if attempt.Operation.Kind == planner.VerifyActive {
			return result, errors.New("post-switch verification failed; the previous Generation was restored")
		}
		return result, errors.New("host preparation operation failed")
	}
	if result.Outcome == operation.OutcomeUncertain {
		return result, errors.New("host operation requires explicit recovery because its outcome is uncertain")
	}
	return result, nil
}

func failureContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent.Err() == nil {
		return parent, func() {}
	}
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (e Engine) commitResult(ctx context.Context, attempt state.OperationAttempt, envelope operation.Envelope, result operation.Result) error {
	if err := e.Handler.Verify(envelope, result); err != nil {
		return e.recordUncertain(ctx, attempt, err, HandlerObservation{State: ObservationUnknown})
	}
	outcome := state.ExecutionSucceeded
	if result.Outcome == operation.OutcomeFailed {
		outcome = state.ExecutionFailed
	} else if result.Outcome == operation.OutcomeUncertain {
		outcome = state.ExecutionUncertain
	}
	if err := e.Backend.CompleteOperation(ctx, state.CompleteOperationRequest{
		AttemptID: attempt.AttemptID, Holder: attempt.Holder,
		PlanID: attempt.Plan.ID, OperationID: attempt.Operation.ID,
		FencingToken: attempt.FencingToken, Outcome: outcome,
		Observation: result.Observation, CompletedAt: e.Now().UTC(),
	}); err != nil {
		return err
	}
	return nil
}

type handlerResponse struct {
	result operation.Result
	err    error
}

func (e Engine) executeWithLeaseRenewal(ctx context.Context, request Request, attempt state.OperationAttempt, envelope operation.Envelope) (operation.Result, error) {
	handlerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	response := make(chan handlerResponse, 1)
	go func() {
		result, err := e.Handler.ApplyOrResume(handlerContext, envelope)
		response <- handlerResponse{result: result, err: err}
	}()
	renewEvery := request.LeaseDuration / 2
	if renewEvery < time.Second {
		renewEvery = time.Second
	}
	if e.renewalInterval > 0 {
		renewEvery = e.renewalInterval
	}
	ticker := time.NewTicker(renewEvery)
	defer ticker.Stop()
	for {
		select {
		case completed := <-response:
			return completed.result, completed.err
		case <-ticker.C:
			_, err := e.Backend.RenewExecutionLease(ctx, state.RenewExecutionLeaseRequest{
				AttemptID: attempt.AttemptID, Holder: attempt.Holder,
				PlanID: attempt.Plan.ID, OperationID: attempt.Operation.ID,
				FencingToken: attempt.FencingToken, RenewedAt: e.Now().UTC(), LeaseDuration: request.LeaseDuration,
			})
			if err != nil {
				cancel()
				<-response
				return operation.Result{}, fmt.Errorf("renew fenced execution lease: %w", err)
			}
		case <-ctx.Done():
			cancel()
			<-response
			return operation.Result{}, ctx.Err()
		}
	}
}

func (e Engine) recordUncertain(ctx context.Context, attempt state.OperationAttempt, cause error, observed HandlerObservation) error {
	observation, _ := json.Marshal(struct {
		Status   string               `json:"status"`
		Reason   string               `json:"reason"`
		Recovery planner.RecoveryMode `json:"recovery"`
		Observed ObservationState     `json:"observed"`
		Evidence json.RawMessage      `json:"evidence,omitempty"`
	}{Status: "uncertain", Reason: cause.Error(), Recovery: e.Handler.Recovery(attempt.Operation), Observed: observed.State, Evidence: observed.Evidence})
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
