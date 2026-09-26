package execution

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

func artifactHandlerObservation(observed host.ArtifactObservation) (HandlerObservation, error) {
	evidence, err := json.Marshal(observed)
	if err != nil {
		return HandlerObservation{}, err
	}
	switch observed.Status {
	case host.ArtifactAlreadyPresent:
		return HandlerObservation{State: ObservationSatisfied, Evidence: evidence}, nil
	case host.ArtifactAbsent:
		return HandlerObservation{State: ObservationPending, Evidence: evidence}, nil
	case host.ArtifactInvalid, host.ArtifactUnknown:
		return HandlerObservation{State: ObservationUnknown, Evidence: evidence}, nil
	default:
		return HandlerObservation{}, errors.New("Host Target returned unsupported Artifact observation")
	}
}

func plannedArtifactInput(planned planner.Operation) (*planner.ArtifactInput, error) {
	if planned.Input.Artifact != nil {
		return planned.Input.Artifact, nil
	}
	if planned.Input.Async != nil && planned.Input.Async.Artifact != nil {
		artifact := planned.Input.Async.Artifact
		return &planner.ArtifactInput{Source: artifact.Source, Digest: artifact.Digest}, nil
	}
	return nil, errors.New("host Artifact preparation has no typed Artifact input")
}

func executeHostObservation(command *exec.Cmd, planned planner.Operation, subject string) (HandlerObservation, error) {
	encoded, err := json.Marshal(planned)
	if err != nil {
		return HandlerObservation{}, fmt.Errorf("encode host observation request: %w", err)
	}
	command.Stdin = bytes.NewReader(encoded)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	data, err := command.Output()
	if err != nil {
		return HandlerObservation{}, fmt.Errorf("%s failed: %w: %s", subject, err, bytes.TrimSpace(stderr.Bytes()))
	}
	var observed host.OperationObservation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil || !json.Valid(observed.Evidence) {
		return HandlerObservation{}, fmt.Errorf("%s returned invalid structured output", subject)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return HandlerObservation{}, fmt.Errorf("%s returned multiple structured values", subject)
	}
	state := ObservationState(observed.State)
	if state != ObservationPending && state != ObservationSatisfied && state != ObservationUnknown {
		return HandlerObservation{}, fmt.Errorf("%s returned an unsupported observation state", subject)
	}
	outcome := operation.Outcome(observed.Outcome)
	if outcome != "" && outcome != operation.OutcomeSucceeded && outcome != operation.OutcomeFailed {
		return HandlerObservation{}, fmt.Errorf("%s returned an unsupported observed outcome", subject)
	}
	if state != ObservationSatisfied && outcome != "" {
		return HandlerObservation{}, fmt.Errorf("%s returned an outcome for a non-satisfied observation", subject)
	}
	return HandlerObservation{State: state, Outcome: outcome, Evidence: observed.Evidence}, nil
}

func executeHostEnvelope(command *exec.Cmd, envelope operation.Envelope, subject string) (operation.Result, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return operation.Result{}, fmt.Errorf("encode host operation: %w", err)
	}
	command.Stdin = bytes.NewReader(encoded)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return operation.Result{}, fmt.Errorf("prepare %s output: %w", subject, err)
	}
	if err := command.Start(); err != nil {
		return operation.Result{}, fmt.Errorf("start %s: %w", subject, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, 1<<20+1))
	waitErr := command.Wait()
	if readErr != nil || len(data) > 1<<20 {
		return operation.Result{}, fmt.Errorf("%s returned invalid output", subject)
	}
	if waitErr != nil {
		return operation.Result{}, fmt.Errorf("%s failed: %w: %s", subject, waitErr, bytes.TrimSpace(stderr.Bytes()))
	}
	var result operation.Result
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return operation.Result{}, fmt.Errorf("%s returned invalid structured output", subject)
	}
	return result, nil
}

func verifyHostResult(envelope operation.Envelope, result operation.Result) error {
	if err := result.ValidateAgainst(envelope); err != nil {
		return err
	}
	if result.Outcome == operation.OutcomeUncertain && envelope.Operation.Kind != planner.SwitchEndpoint && envelope.Operation.Kind != planner.VerifyActive && envelope.Operation.Kind != planner.DrainPrevious && envelope.Operation.Kind != planner.RetainPrevious && envelope.Operation.Kind != planner.VerifyWorkerActive {
		return errors.New("host operation kind cannot return an uncertain structured outcome")
	}
	switch envelope.Operation.Kind {
	case planner.PrepareQueue:
		return verifyQueueResult(envelope, result)
	case planner.StageArtifact:
		return verifyArtifactResult(envelope, result)
	case planner.InstallGeneration:
		return verifyGenerationResult(envelope, result)
	case planner.StartCandidate:
		return verifySystemdResult(envelope, result)
	case planner.VerifyCandidate:
		return verifyHealthResult(envelope, result)
	case planner.SwitchEndpoint:
		return verifyEndpointResult(envelope, result)
	case planner.VerifyActive:
		return verifyActiveResult(envelope, result)
	case planner.DrainPrevious:
		return verifyDrainResult(envelope, result)
	case planner.RetainPrevious:
		return verifyRetentionResult(envelope, result)
	case planner.InstallTaskGeneration, planner.VerifyTaskGeneration:
		return verifyAsyncTaskResult(envelope, result)
	case planner.VerifyWorkerActive:
		if envelope.Operation.Input.Async != nil && envelope.Operation.Input.Async.WorkerHandoff != nil {
			return verifyAsyncWorkerActiveVerificationResult(envelope, result)
		}
		return verifyAsyncWorkerResult(envelope, result)
	case planner.InstallWorkerGeneration, planner.StartWorkerCandidate, planner.VerifyWorkerCandidate, planner.ActivateWorkerIntake:
		return verifyAsyncWorkerResult(envelope, result)
	case planner.FenceWorkerIntake, planner.DrainWorkerPrevious, planner.RetainWorkerPrevious:
		return verifyAsyncWorkerHandoffResult(envelope, result)
	case planner.InstallScheduleRuntime:
		return verifyAsyncRuntimeResult(envelope, result)
	case planner.HandoffSchedule, planner.VerifySchedule:
		return verifyAsyncScheduleResult(envelope, result)
	default:
		return errors.New("host operation result kind is unsupported")
	}
}

func verifyAsyncWorkerActiveVerificationResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Async.WorkerHandoff
	if input == nil || input.Worker.Previous == nil {
		return errors.New("host Worker active verification has no planned handoff")
	}
	var observed host.AsyncWorkerActiveVerificationObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host Worker active verification observation is invalid")
	}
	digest, err := planner.OperationDigest(envelope.Operation)
	previous := input.Worker.Previous
	identitiesMatch := err == nil && observed.PlanID == envelope.Authorization.Claim.PlanID && observed.OperationDigest == digest && observed.QueueGenerationID == input.QueueGenerationID && observed.MessageID != "" && observed.Candidate.ID == input.Worker.GenerationID && observed.Candidate.Revision == input.Worker.Revision && observed.Candidate.ArtifactDigest == input.Worker.ArtifactDigest && observed.Previous.ID == previous.ID && observed.Previous.Revision == previous.Revision && observed.Previous.ArtifactDigest == previous.ArtifactDigest
	if !identitiesMatch {
		return errors.New("host Worker active verification observation does not match the Plan")
	}
	switch result.Outcome {
	case operation.OutcomeSucceeded:
		if observed.Status != host.WorkerActiveVerificationHealthy || !observed.PublisherConfirmed || !observed.CandidateProcessed || !observed.CandidateAcknowledged || observed.RollbackAttempted || observed.RollbackSucceeded || observed.Redelivered || observed.Restored != nil || observed.Candidate.Gate != "open" || !observed.Candidate.Active || !observed.Candidate.UnitActive || !observed.Candidate.QueueConnected || observed.Reason != "" || observed.RecoveryAction != "" {
			return errors.New("host healthy Worker verification observation is invalid")
		}
	case operation.OutcomeFailed:
		if observed.Status != host.WorkerActiveVerificationRolledBack || !observed.PublisherConfirmed || !observed.RollbackAttempted || !observed.RollbackSucceeded || !observed.PreviousProcessed || !observed.PreviousAcknowledged || observed.Restored == nil || observed.Restored.ID != previous.ID || observed.Restored.ArtifactDigest != previous.ArtifactDigest || observed.Restored.Gate != "open" || !observed.Restored.Active || !observed.Restored.UnitActive || !observed.Restored.QueueConnected || observed.Reason == "" || observed.RecoveryAction != "" || observed.Candidate.Gate != "closed" {
			return errors.New("host rolled-back Worker verification observation is invalid")
		}
	case operation.OutcomeUncertain:
		if observed.Status != host.WorkerActiveVerificationUncertain || observed.Reason == "" || observed.RecoveryAction == "" || observed.RollbackSucceeded {
			return errors.New("host uncertain Worker verification observation is invalid")
		}
	}
	return nil
}

func verifyAsyncWorkerHandoffResult(envelope operation.Envelope, result operation.Result) error {
	if envelope.Operation.Input.Async == nil || envelope.Operation.Input.Async.WorkerHandoff == nil {
		return errors.New("host Worker handoff observation has no planned handoff")
	}
	input := envelope.Operation.Input.Async.WorkerHandoff
	var observed host.AsyncWorkerHandoffObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host Worker handoff observation is invalid")
	}
	previous := input.Worker.Previous
	digest, err := planner.OperationDigest(envelope.Operation)
	if err != nil || previous == nil || observed.OperationDigest != digest || observed.Candidate.ID != input.Worker.GenerationID || observed.Candidate.ArtifactDigest != input.Worker.ArtifactDigest || observed.Previous.ID != previous.ID || observed.Previous.ArtifactDigest != previous.ArtifactDigest || observed.QueueGenerationID != input.QueueGenerationID {
		return errors.New("host Worker handoff observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded {
		switch envelope.Operation.Kind {
		case planner.FenceWorkerIntake:
			if observed.Status != "previous-fenced" || observed.Previous.Gate != "closed" || observed.Candidate.Gate != "closed" {
				return errors.New("host Worker intake-fence observation is invalid")
			}
		case planner.DrainWorkerPrevious:
			maximum, durationErr := time.ParseDuration(input.Worker.Drain.MaxDuration)
			validDeadline := durationErr == nil && observed.DrainStartedAt != nil && observed.CompletionDeadline != nil && observed.DrainDeadline != nil && observed.DrainCompletedAt != nil && observed.DrainDeadline.Equal(observed.DrainStartedAt.Add(maximum)) && observed.CompletionDeadline.Equal(observed.DrainDeadline.Add(-planner.WorkerDrainReleaseBudget(maximum))) && !observed.DrainCompletedAt.Before(*observed.DrainStartedAt) && !observed.DrainCompletedAt.After(*observed.DrainDeadline)
			drained := observed.Status == "previous-drained" && !observed.BoundElapsed && observed.ReleasedMessageID == ""
			released := observed.Status == "previous-released" && observed.BoundElapsed && observed.CompletionDeadline != nil && observed.ReleaseStartedAt != nil && observed.DrainCompletedAt != nil && !observed.ReleaseStartedAt.Before(*observed.CompletionDeadline) && !observed.ReleaseStartedAt.After(*observed.DrainCompletedAt) && observed.InFlightMessageID != "" && observed.ReleasedMessageID == observed.InFlightMessageID
			if observed.PlanID != envelope.Authorization.Claim.PlanID || !validDeadline || !drained && !released || observed.Previous.UnitActive || observed.Previous.InFlight != 0 {
				return errors.New("host Worker drain observation is invalid")
			}
		case planner.RetainWorkerPrevious:
			duration, durationErr := input.RollbackWindow.Duration()
			if observed.PlanID != envelope.Authorization.Claim.PlanID || observed.DrainOperationDigest != input.DrainOperationDigest || observed.Status != "previous-retained" || observed.RollbackWindow != input.RollbackWindow || durationErr != nil || !observed.Previous.Restartable || observed.RetainUntil == nil || observed.RetainedAt == nil || !observed.RetainUntil.Equal(observed.RetainedAt.Add(duration)) {
				return errors.New("host Worker retention observation is invalid")
			}
		}
	} else if result.Outcome == operation.OutcomeFailed && (observed.Reason == "" || observed.RecoveryAction == "") {
		return errors.New("host Worker handoff failure observation is invalid")
	}
	return nil
}

func verifyAsyncTaskResult(envelope operation.Envelope, result operation.Result) error {
	if envelope.Operation.Input.Async == nil || envelope.Operation.Input.Async.Task == nil {
		return errors.New("host Task observation has no planned Task")
	}
	input := envelope.Operation.Input.Async.Task
	var observed host.AsyncTaskOperationObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host Task observation is invalid")
	}
	if observed.Task.ID != input.GenerationID || observed.Task.Revision != input.Revision || observed.Task.ArtifactDigest != input.ArtifactDigest || observed.Task.SystemdUnit != input.SystemdUnit || observed.Task.Queue != input.QueueLogicalID || observed.Task.ConfigurationDigest != input.ConfigurationDigest {
		return errors.New("host Task observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded {
		if observed.Status != "installed" || observed.Executable == "" || envelope.Operation.Kind == planner.VerifyTaskGeneration && !observed.Verified {
			return errors.New("host Task success observation is invalid")
		}
	} else if result.Outcome == operation.OutcomeFailed && observed.Reason == "" {
		return errors.New("host Task failure observation is invalid")
	}
	return nil
}

func verifyAsyncWorkerResult(envelope operation.Envelope, result operation.Result) error {
	if envelope.Operation.Input.Async == nil || envelope.Operation.Input.Async.Worker == nil && envelope.Operation.Input.Async.WorkerHandoff == nil {
		return errors.New("host Worker observation has no planned Worker")
	}
	input := envelope.Operation.Input.Async.Worker
	expectedQueueGenerationID := ""
	if envelope.Operation.Input.Async.WorkerHandoff != nil {
		input = &envelope.Operation.Input.Async.WorkerHandoff.Worker
		expectedQueueGenerationID = envelope.Operation.Input.Async.WorkerHandoff.QueueGenerationID
	}
	var observed host.AsyncWorkerOperationObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host Worker observation is invalid")
	}
	if observed.Worker.ID != input.GenerationID || observed.Worker.Revision != input.Revision || observed.Worker.ArtifactDigest != input.ArtifactDigest || observed.Worker.SystemdUnit != input.SystemdUnit || observed.QueueGenerationID != expectedQueueGenerationID {
		return errors.New("host Worker observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded {
		switch envelope.Operation.Kind {
		case planner.InstallWorkerGeneration:
			if observed.Status != "installed" && observed.Status != "active-gated" && observed.Status != "active-open" {
				return errors.New("host Worker installation observation is invalid")
			}
		case planner.StartWorkerCandidate:
			if observed.Status != "active-gated" || !observed.Worker.UnitActive || !observed.Worker.QueueConnected || observed.Worker.Gate != "closed" {
				return errors.New("host gated Worker observation is invalid")
			}
		case planner.VerifyWorkerCandidate:
			if observed.Status != "active-gated" || !observed.Verified || !observed.Checks.Liveness || !observed.Checks.QueueConnectivity || !observed.Checks.RevisionIdentity || !observed.Checks.IntakeDisabled || observed.Worker.Gate != "closed" {
				return errors.New("host Worker candidate verification observation is invalid")
			}
		case planner.ActivateWorkerIntake:
			if observed.Status != "active-open" || !observed.Worker.UnitActive || !observed.Worker.QueueConnected || observed.Worker.Gate != "open" {
				return fmt.Errorf("host active Worker observation is invalid (status=%q gate=%q unitActive=%t queueConnected=%t consuming-state-required=true)", observed.Status, observed.Worker.Gate, observed.Worker.UnitActive, observed.Worker.QueueConnected)
			}
		case planner.VerifyWorkerActive:
			if observed.Status != "active-open" || !observed.Verified || !observed.Worker.Active || !observed.Worker.UnitActive || !observed.Worker.QueueConnected || observed.Worker.Gate != "open" {
				return fmt.Errorf("host verified active Worker observation is invalid (status=%q verified=%t active=%t gate=%q unitActive=%t queueConnected=%t)", observed.Status, observed.Verified, observed.Worker.Active, observed.Worker.Gate, observed.Worker.UnitActive, observed.Worker.QueueConnected)
			}
		}
	} else if result.Outcome == operation.OutcomeFailed && observed.Reason == "" {
		return errors.New("host Worker failure observation is invalid")
	}
	return nil
}

func verifyAsyncRuntimeResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Async
	if input == nil || input.Runtime == nil {
		return errors.New("host Schedule runtime observation has no planned runtime")
	}
	var observed host.AsyncRuntimeObservation
	if err := decodeObservation(result.Observation, &observed); err != nil || observed.AppletDigest != input.Runtime.AppletDigest || observed.LedgerSchema != input.Runtime.LedgerSchema {
		return errors.New("host Schedule runtime observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded && observed.Status != "installed" || result.Outcome == operation.OutcomeFailed && observed.Reason == "" {
		return errors.New("host Schedule runtime outcome is invalid")
	}
	return nil
}

func verifyAsyncScheduleResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Async
	if input == nil || input.Schedule == nil {
		return errors.New("host Schedule observation has no planned Schedule")
	}
	var observed host.AsyncScheduleOperationObservation
	if err := decodeObservation(result.Observation, &observed); err != nil || observed.Schedule.Component != input.Schedule.Component || observed.Schedule.TimerUnit != input.Schedule.TimerUnit || observed.Schedule.TaskGenerationID != input.Schedule.TaskGenerationID || observed.Schedule.AppletDigest != input.Schedule.AppletDigest || observed.Schedule.LedgerSchema != input.Schedule.LedgerSchema {
		return errors.New("host Schedule observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded {
		if envelope.Operation.Kind == planner.HandoffSchedule && (observed.Status != "active" || !observed.Schedule.Active || observed.Schedule.FencingToken < 1) {
			return errors.New("host Schedule handoff observation is invalid")
		}
		if envelope.Operation.Kind == planner.VerifySchedule && (observed.Status != "verified" || observed.Occurrence == nil || observed.Invocation == nil || observed.Invocation.Outcome != "succeeded" || observed.MessageID == "" || !observed.Acknowledged) {
			return errors.New("host Schedule verification observation is invalid")
		}
	} else if result.Outcome == operation.OutcomeFailed && observed.Reason == "" {
		return errors.New("host Schedule failure observation is invalid")
	}
	return nil
}

func verifyQueueResult(envelope operation.Envelope, result operation.Result) error {
	if envelope.Operation.Input.Async == nil || envelope.Operation.Input.Async.Queue == nil {
		return errors.New("host Queue observation has no planned Queue")
	}
	input := envelope.Operation.Input.Async.Queue
	var observed host.QueueStatus
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host Queue observation is invalid")
	}
	if observed.ID != input.LogicalID || observed.GenerationID != input.GenerationID || observed.QueueType != input.QueueType || observed.Members != input.Members || observed.ImageManifest != input.ImageManifest || observed.ServiceUnit != input.ServiceUnit || observed.Container != input.Container || observed.Account != input.Account || observed.DataPath != input.DataPath || observed.QuadletPath != input.QuadletPath {
		return errors.New("host Queue observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded && (!observed.Exists || !observed.Ready || !observed.Durable || observed.Health != "healthy" || observed.RabbitMQVersion != input.RabbitMQVersion || observed.Accepted != observed.Available+observed.Acknowledged+observed.DeadLettered || observed.Accepted < 1 || observed.DeliveryLimit != 3 || observed.MessageTTL != "24h0m0s" || observed.RetryDelay != "10s" || observed.DeadLetterTTL != "168h0m0s" || len(observed.Bindings) != 3 || len(observed.SupportedGuarantees) != 3 || len(observed.OwnedResources) == 0) {
		return errors.New("host Queue success observation is invalid")
	}
	if result.Outcome == operation.OutcomeFailed && (observed.Health != "failed" || observed.Reason == "" || observed.RecoveryAction == "") {
		return errors.New("host Queue failure observation is invalid")
	}
	return nil
}

func verifyArtifactResult(envelope operation.Envelope, result operation.Result) error {
	var observed host.ArtifactObservation
	decoder := json.NewDecoder(bytes.NewReader(result.Observation))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return errors.New("host Artifact observation is invalid")
	}
	artifact, err := plannedArtifactInput(envelope.Operation)
	if err != nil {
		return err
	}
	expectedPath, err := host.ArtifactCachePath(host.ArtifactCacheRoot, artifact.Digest)
	if err != nil {
		return err
	}
	if observed.Path != expectedPath || observed.Digest != artifact.Digest || observed.Size < 0 || observed.Size > host.MaxArtifactBytes {
		return errors.New("host Artifact observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded && observed.Status != host.ArtifactStaged && observed.Status != host.ArtifactAlreadyPresent {
		return errors.New("host Artifact success observation is invalid")
	}
	if result.Outcome == operation.OutcomeFailed && (observed.Status != host.ArtifactFailed || observed.Reason == "") {
		return errors.New("host Artifact failure observation is invalid")
	}
	return nil
}

func verifyGenerationResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Generation
	if input == nil {
		return errors.New("host Generation observation has no planned Generation")
	}
	var observed host.GenerationObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host Generation observation is invalid")
	}
	if observed.ID != input.ID || observed.Revision != input.Revision || observed.ArtifactDigest != input.ArtifactDigest || observed.ReleaseDirectory != input.ReleaseDirectory {
		return errors.New("host Generation observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded && (observed.Status != host.CandidateInstalled || observed.Executable == "") {
		return errors.New("host Generation success observation is invalid")
	}
	if result.Outcome == operation.OutcomeFailed && (observed.Status != host.CandidateFailed && observed.Status != host.CandidateInvalid || observed.Reason == "") {
		return errors.New("host Generation failure observation is invalid")
	}
	return nil
}

func verifySystemdResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Systemd
	if input == nil {
		return errors.New("host systemd observation has no planned candidate")
	}
	var observed host.SystemdObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host systemd observation is invalid")
	}
	if observed.GenerationID != input.ID || observed.Revision != input.Revision || observed.Unit != input.Unit || observed.Port != input.Port || observed.ReleaseDirectory != input.ReleaseDirectory {
		return errors.New("host systemd observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded && observed.Status != host.CandidateActive {
		return errors.New("host systemd success observation is invalid")
	}
	if result.Outcome == operation.OutcomeFailed && ((observed.Status != host.CandidateFailed && observed.Status != host.CandidateInvalid) || observed.Reason == "") {
		return errors.New("host systemd failure observation is invalid")
	}
	return nil
}

func verifyHealthResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Health
	if input == nil {
		return errors.New("host health observation has no planned Health Contract")
	}
	var observed host.HealthObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host health observation is invalid")
	}
	if observed.GenerationID != input.ID || observed.Revision != input.Revision || observed.ArtifactDigest != input.ArtifactDigest || observed.ReleaseDirectory != input.ReleaseDirectory || observed.Unit != input.Unit || observed.Port != input.Port {
		return errors.New("host health observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded {
		expected := []struct{ name, path string }{{"liveness", input.LivenessPath}, {"readiness", input.ReadinessPath}, {"candidateVerification", input.CandidateVerifyPath}}
		if observed.Status != host.CandidateHealthy || !observed.CandidateActive || !observed.SwitchEligible || observed.CandidateCleaned || len(observed.Checks) != len(expected) {
			return errors.New("host health success observation is invalid")
		}
		for index, check := range observed.Checks {
			if check.Name != expected[index].name || check.Path != expected[index].path || !check.Healthy || check.StatusCode < 200 || check.StatusCode >= 300 {
				return errors.New("host health success observation is invalid")
			}
		}
	}
	if result.Outcome == operation.OutcomeFailed && (observed.Status != host.CandidateFailed || observed.Reason == "" || observed.SwitchEligible) {
		return errors.New("host health failure observation is invalid")
	}
	return nil
}

func verifyEndpointResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Endpoint
	if input == nil {
		return errors.New("host Endpoint observation has no planned Endpoint")
	}
	var observed host.EndpointObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host Endpoint observation is invalid")
	}
	if observed.RouteID != input.RouteID || observed.ListenPort != input.ListenPort || observed.Upstream != input.Upstream || observed.DrainPolicy != input.DrainPolicy || !endpointGenerationMatches(observed.Active, *input) {
		return errors.New("host Endpoint observation does not match the Plan")
	}
	plannedPrevious := envelope.Operation.Input.Previous
	if plannedPrevious == nil != (observed.Previous == nil) || plannedPrevious != nil && !generationStatusIdentityMatches(*plannedPrevious, *observed.Previous) {
		return errors.New("host Endpoint previous Generation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded {
		if observed.Status != host.EndpointActive || !observed.CandidateVerified || !observed.GracefulReload || !observed.PreviousRetained || !observed.Active.UnitActive || !observed.Active.UnitMatches || !observed.Active.RouteObserved || !observed.Active.RouteMatches || observed.Active.RouteUpstream != input.Upstream {
			return errors.New("host Endpoint success observation is invalid")
		}
		if observed.Previous != nil && (!observed.Previous.UnitActive || !observed.Previous.UnitMatches || observed.Previous.RouteMatches) {
			return errors.New("host Endpoint retained previous Generation observation is invalid")
		}
	}
	if result.Outcome == operation.OutcomeFailed && (observed.Status != host.EndpointFailed || observed.Reason == "" || observed.CandidateVerified || observed.GracefulReload) {
		return errors.New("host Endpoint failure observation is invalid")
	}
	if result.Outcome == operation.OutcomeUncertain && (observed.Status != host.EndpointUncertain || observed.Reason == "" || observed.RecoveryAction == "" || observed.CandidateVerified || observed.GracefulReload) {
		return errors.New("host Endpoint uncertain observation is invalid")
	}
	return nil
}

func verifyActiveResult(envelope operation.Envelope, result operation.Result) error {
	health := envelope.Operation.Input.Health
	endpoint := envelope.Operation.Input.Endpoint
	if health == nil || endpoint == nil {
		return errors.New("host active verification observation has no planned Health Contract or Endpoint")
	}
	var observed host.ActiveVerificationObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host active verification observation is invalid")
	}
	if !endpointGenerationMatches(observed.Candidate, *endpoint) || envelope.Operation.Input.Previous == nil != (observed.Previous == nil) || observed.Previous != nil && !generationStatusIdentityMatches(*envelope.Operation.Input.Previous, *observed.Previous) {
		return errors.New("host active verification observation does not match the Plan")
	}
	activeHealthy := exactHealthChecks(observed.ActiveChecks, *health)
	switch result.Outcome {
	case operation.OutcomeSucceeded:
		if observed.Status != host.ActiveVerificationHealthy || !activeHealthy || observed.RollbackAttempted || observed.RollbackSucceeded || observed.Restored != nil || observed.Reason != "" || observed.RecoveryAction != "" || observed.ObservedUpstream != endpoint.Upstream {
			return errors.New("host active verification success observation is invalid")
		}
	case operation.OutcomeFailed:
		previous := envelope.Operation.Input.Previous
		if observed.Status != host.ActiveVerificationRolledBack || activeHealthy || previous == nil || !observed.RollbackAttempted || !observed.RollbackSucceeded || observed.Restored == nil || !generationStatusIdentityMatches(*previous, *observed.Restored) || observed.ObservedUpstream != fmt.Sprintf("127.0.0.1:%d", previous.Port) || observed.Reason == "" || observed.RecoveryAction != "" {
			return errors.New("host rolled-back verification observation is invalid")
		}
		previousHealth := planner.HealthInput{LivenessPath: health.LivenessPath, ReadinessPath: health.ReadinessPath, CandidateVerifyPath: health.CandidateVerifyPath}
		if !exactHealthChecks(observed.PreviousChecks, previousHealth) || !exactHealthChecks(observed.RollbackChecks, previousHealth) {
			return errors.New("host rolled-back verification health evidence is invalid")
		}
	case operation.OutcomeUncertain:
		if observed.Status != host.ActiveVerificationUncertain || observed.RollbackSucceeded || observed.Reason == "" || observed.RecoveryAction == "" {
			return errors.New("host uncertain verification observation is invalid")
		}
	default:
		return errors.New("host active verification outcome is unsupported")
	}
	return nil
}

func verifyDrainResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Drain
	if input == nil {
		return errors.New("host HTTP drain observation has no planned drain")
	}
	var observed host.DrainObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host HTTP drain observation is invalid")
	}
	digest, err := planner.OperationDigest(envelope.Operation)
	if err != nil {
		return err
	}
	if !endpointGenerationMatches(observed.Active, input.Endpoint) || !generationStatusIdentityMatches(observed.Previous, input.Previous) || observed.Mode != input.Mode || observed.HandoffPolicy != input.Endpoint.DrainPolicy || observed.MaxDuration != input.MaxDuration || observed.OperationDigest != digest {
		return errors.New("host HTTP drain observation does not match the Plan")
	}
	switch result.Outcome {
	case operation.OutcomeSucceeded:
		if observed.Status != host.DrainCompleted || !observed.BoundElapsed || !observed.StableRouteVerified || observed.PreviousUnitActive || !observed.PreviousUnitRetained || !observed.PreviousReleaseRetained || observed.SwitchedAt == nil || observed.SwitchedAt.IsZero() || observed.StableVerifiedAt == nil || observed.StableVerifiedAt.IsZero() || observed.Deadline == nil || observed.Deadline.IsZero() || observed.Reason != "" || observed.RecoveryAction != "" {
			return errors.New("host HTTP drain success observation is invalid")
		}
	case operation.OutcomeFailed:
		if observed.Status != host.DrainFailed || observed.Reason == "" || observed.RecoveryAction != "" {
			return errors.New("host HTTP drain failure observation is invalid")
		}
	case operation.OutcomeUncertain:
		if observed.Status != host.DrainUncertain || observed.Reason == "" || observed.RecoveryAction == "" {
			return errors.New("host HTTP drain uncertain observation is invalid")
		}
	default:
		return errors.New("host HTTP drain outcome is unsupported")
	}
	return nil
}

func verifyRetentionResult(envelope operation.Envelope, result operation.Result) error {
	input := envelope.Operation.Input.Retention
	if input == nil {
		return errors.New("host rollback retention observation has no planned retention")
	}
	var observed host.RetentionObservation
	if err := decodeObservation(result.Observation, &observed); err != nil {
		return errors.New("host rollback retention observation is invalid")
	}
	digest, err := planner.OperationDigest(envelope.Operation)
	if err != nil {
		return err
	}
	if !endpointGenerationMatches(observed.Active, input.Endpoint) || !generationStatusIdentityMatches(observed.Previous, input.Previous) || observed.Policy != input.Policy || observed.RollbackWindow != input.RollbackWindow || observed.OperationDigest != digest {
		return errors.New("host rollback retention observation does not match the Plan")
	}
	switch result.Outcome {
	case operation.OutcomeSucceeded:
		duration, durationErr := input.RollbackWindow.Duration()
		validDeadline := durationErr == nil && observed.SwitchedAt != nil && !observed.SwitchedAt.IsZero() && observed.RetainUntil != nil && observed.RetainUntil.Equal(observed.SwitchedAt.Add(duration))
		validTimes := observed.DrainedAt != nil && !observed.DrainedAt.IsZero() && observed.RetainedAt != nil && !observed.RetainedAt.IsZero() && !observed.RetainedAt.Before(*observed.DrainedAt)
		if observed.Status != host.RetentionCompleted || !validDigest(observed.DrainOperationDigest) || !validDeadline || !validTimes || !observed.StableRouteVerified || observed.PreviousUnitActive || !observed.PreviousUnitRetained || !observed.PreviousGenerationDirectoryRetained || !observed.PreviousManifestRetained || !observed.PreviousArtifactRetained || !observed.Restartable || observed.CleanupPerformed || observed.Reason != "" || observed.RecoveryAction != "" {
			return errors.New("host rollback retention success observation is invalid")
		}
	case operation.OutcomeFailed:
		if observed.Status != host.RetentionFailed || observed.Reason == "" || observed.RecoveryAction != "" || observed.CleanupPerformed {
			return errors.New("host rollback retention failure observation is invalid")
		}
	case operation.OutcomeUncertain:
		if observed.Status != host.RetentionUncertain || observed.Reason == "" || observed.RecoveryAction == "" || observed.CleanupPerformed {
			return errors.New("host rollback retention uncertain observation is invalid")
		}
	default:
		return errors.New("host rollback retention outcome is unsupported")
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func exactHealthChecks(checks []host.HealthCheckObservation, input planner.HealthInput) bool {
	expected := []struct{ name, path string }{{"liveness", input.LivenessPath}, {"readiness", input.ReadinessPath}, {"candidateVerification", input.CandidateVerifyPath}}
	if len(checks) != len(expected) {
		return false
	}
	for index, check := range checks {
		if check.Name != expected[index].name || check.Path != expected[index].path || !check.Healthy || check.StatusCode < 200 || check.StatusCode >= 300 || check.Reason != "" {
			return false
		}
	}
	return true
}

func endpointGenerationMatches(observed host.GenerationStatus, input planner.EndpointInput) bool {
	return observed.ID == input.ID && observed.Revision == input.Revision && observed.ArtifactDigest == input.ArtifactDigest && observed.SystemdUnit == input.Unit && observed.ReleaseDirectory == input.ReleaseDirectory && observed.Port == input.UpstreamPort && observed.RouteID == input.RouteID
}

func generationStatusIdentityMatches(left, right host.GenerationStatus) bool {
	return left.ID == right.ID && left.Revision == right.Revision && left.ArtifactDigest == right.ArtifactDigest && left.SystemdUnit == right.SystemdUnit && left.ReleaseDirectory == right.ReleaseDirectory && left.Port == right.Port && left.RouteID == right.RouteID
}

func decodeObservation(data json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("observation must contain exactly one JSON value")
	}
	return nil
}
