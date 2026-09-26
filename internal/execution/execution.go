package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"provision/internal/authority"
	"provision/internal/operation"
	"provision/internal/planner"
	"provision/internal/state"
)

type Handler interface {
	Observe(context.Context, string, planner.Operation) (HandlerObservation, error)
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
	ResolvedSecrets map[string]string
	Now             func() time.Time
	renewalInterval time.Duration
}

type Request struct {
	PlanID        string
	OperationID   string
	Holder        string
	LeaseDuration time.Duration
}

type ResumeRequest struct {
	PlanID        string
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
	return e.execute(ctx, request, "")
}

func (e Engine) Resume(ctx context.Context, request ResumeRequest) (operation.Result, error) {
	if e.Backend == nil || e.Handler == nil || e.Now == nil {
		return operation.Result{}, errors.New("execution engine is not fully configured")
	}
	snapshot, err := e.Backend.LoadPlanSnapshot(ctx, request.PlanID)
	if err != nil {
		return operation.Result{}, err
	}
	events, err := e.Backend.LoadJournal(ctx, request.PlanID)
	if err != nil {
		return operation.Result{}, err
	}
	operationID, interruptedAttemptID, err := interruptedOperation(snapshot.Plan, events)
	if err != nil {
		return operation.Result{}, err
	}
	return e.execute(ctx, Request{
		PlanID: request.PlanID, OperationID: operationID, Holder: request.Holder, LeaseDuration: request.LeaseDuration,
	}, interruptedAttemptID)
}

func interruptedOperation(plan planner.Plan, events []state.JournalEvent) (string, string, error) {
	latest := map[string]state.JournalEvent{}
	for _, event := range events {
		latest[event.OperationID] = event
	}
	for _, planned := range plan.Operations {
		event, ok := latest[planned.ID]
		if !ok {
			continue
		}
		switch event.Kind {
		case state.JournalIntent:
			return planned.ID, event.AttemptID, nil
		case state.JournalOutcome:
			switch event.Outcome {
			case state.ExecutionSucceeded:
				continue
			case state.ExecutionUncertain:
				return planned.ID, event.AttemptID, nil
			case state.ExecutionFailed:
				return "", "", fmt.Errorf("operation %s has a known failed outcome and requires an explicit new execution decision", planned.ID)
			default:
				return "", "", fmt.Errorf("operation %s has an unsupported journal outcome", planned.ID)
			}
		default:
			return "", "", fmt.Errorf("operation %s has an unsupported journal event", planned.ID)
		}
	}
	return "", "", errors.New("deployment has no interrupted or uncertain operation to resume")
}

func (e Engine) execute(ctx context.Context, request Request, resumeOfAttemptID string) (operation.Result, error) {
	if e.Backend == nil || e.Handler == nil || e.Now == nil {
		return operation.Result{}, errors.New("execution engine is not fully configured")
	}
	snapshot, err := e.Backend.LoadPlanSnapshot(ctx, request.PlanID)
	if err != nil {
		return operation.Result{}, err
	}
	sensitiveValues, err := resolvedValuesForOperation(snapshot.Plan, request.OperationID, e.ResolvedSecrets)
	if err != nil {
		return operation.Result{}, err
	}
	if snapshot.Plan.Capability.Observed.AuthorityKeyID == "" || snapshot.Plan.Capability.Observed.AuthorityKeyID != e.Signer.ID() {
		return operation.Result{}, errors.New("signing key does not match the authority observed in the Plan")
	}
	now := e.Now().UTC()
	attempt, err := e.Backend.BeginOperation(ctx, state.BeginOperationRequest{
		PlanID: request.PlanID, OperationID: request.OperationID, Holder: request.Holder,
		ResumeOfAttemptID: resumeOfAttemptID, StartedAt: now, LeaseDuration: request.LeaseDuration,
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
	envelope := operation.Envelope{SchemaVersion: operation.EnvelopeSchemaVersion, Authorization: proof, Operation: attempt.Operation, SensitiveValues: sensitiveValues}
	before, err := e.Handler.Observe(ctx, attempt.Plan.ID, attempt.Operation)
	if err != nil {
		journalContext, cancelJournal := failureContext(ctx)
		defer cancelJournal()
		return operation.Result{}, e.recordUncertain(journalContext, attempt, err, HandlerObservation{State: ObservationUnknown})
	}
	if resumeOfAttemptID != "" && before.State == ObservationUnknown {
		journalContext, cancelJournal := failureContext(ctx)
		defer cancelJournal()
		return operation.Result{}, e.recordUncertain(journalContext, attempt, errors.New("fresh Host Target observation is ambiguous; mutation was not replayed"), before)
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
		after, observeErr := e.Handler.Observe(observationContext, attempt.Plan.ID, attempt.Operation)
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
		reason := operationFailureReason(result.Observation)
		if reason == "" {
			reason = "Host Target returned failed outcome without diagnostic evidence"
		}
		prefix := fmt.Sprintf("operation %s (%s) failed: %s", attempt.Operation.ID, attempt.Operation.Kind, reason)
		if attempt.Operation.Kind == planner.VerifyActive {
			return result, fmt.Errorf("%s; the previous Generation was restored", prefix)
		}
		if attempt.Operation.Kind == planner.DrainPrevious {
			return result, fmt.Errorf("%s; the previous Generation was not declared drained", prefix)
		}
		if attempt.Operation.Kind == planner.RetainPrevious {
			return result, fmt.Errorf("%s; the previous Generation was not declared restartable and retained", prefix)
		}
		return result, errors.New(prefix)
	}
	if result.Outcome == operation.OutcomeUncertain {
		return result, errors.New("host operation requires explicit recovery because its outcome is uncertain")
	}
	return result, nil
}

func operationFailureReason(observation json.RawMessage) string {
	var diagnostic struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(observation, &diagnostic) != nil {
		return ""
	}
	reason := strings.Join(strings.Fields(diagnostic.Reason), " ")
	if len(reason) > 1024 {
		reason = reason[:1024] + "…"
	}
	return reason
}

func resolvedValuesForOperation(plan planner.Plan, operationID string, available map[string]string) (map[string]string, error) {
	for _, planned := range plan.Operations {
		if planned.ID != operationID {
			continue
		}
		if planned.Kind != planner.PrepareQueue {
			return nil, nil
		}
		if planned.Input.Async == nil || planned.Input.Async.Queue == nil || planned.Input.Async.Queue.CredentialReference == "" {
			return nil, errors.New("prepareQueue has no Queue credential Secret Reference")
		}
		reference := planned.Input.Async.Queue.CredentialReference
		value, ok := available[reference]
		if !ok || value == "" {
			return nil, fmt.Errorf("resolved value is required for Queue Secret Reference %s", reference)
		}
		return map[string]string{reference: value}, nil
	}
	return nil, errors.New("operation is not present in the Plan")
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
		Status            string               `json:"status"`
		Reason            string               `json:"reason"`
		Recovery          planner.RecoveryMode `json:"recovery"`
		RecoveryAction    string               `json:"recoveryAction"`
		ResumeOfAttemptID string               `json:"resumeOfAttemptId,omitempty"`
		Observed          ObservationState     `json:"observed"`
		Evidence          json.RawMessage      `json:"evidence,omitempty"`
	}{
		Status: "uncertain", Reason: cause.Error(), Recovery: e.Handler.Recovery(attempt.Operation),
		RecoveryAction: recoveryAction(attempt.Operation), ResumeOfAttemptID: attempt.ResumeOfAttemptID,
		Observed: observed.State, Evidence: observed.Evidence,
	})
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

func recoveryAction(planned planner.Operation) string {
	switch planned.Recovery {
	case planner.DiscardStaged:
		return "inspect the digest-addressed Artifact cache before retrying"
	case planner.RemoveCandidate, planner.StopCandidate:
		return "inspect the planned Generation and systemd unit before retrying or removing the candidate"
	case planner.LeaveEndpointUnchanged:
		return "inspect candidate health and confirm the stable Endpoint remains unchanged"
	case planner.RestorePreviousRoute:
		return "inspect the stable Endpoint and active/previous Generations before choosing retry or rollback"
	case planner.RetainBothGenerations:
		return "retain both Generations and inspect policy state before cleanup"
	case planner.RetainQueue:
		return "retain Queue data and observe the exact managed Queue generation before retrying"
	default:
		return "inspect the recorded evidence and Host Target before choosing the next operation"
	}
}
