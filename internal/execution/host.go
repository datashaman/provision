package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"

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
	return HandlerObservation{State: state, Evidence: observed.Evidence}, nil
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
	if result.Outcome == operation.OutcomeUncertain && envelope.Operation.Kind != planner.SwitchEndpoint && envelope.Operation.Kind != planner.VerifyActive {
		return errors.New("host operation kind cannot return an uncertain structured outcome")
	}
	switch envelope.Operation.Kind {
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
	default:
		return errors.New("host operation result kind is unsupported")
	}
}

func verifyArtifactResult(envelope operation.Envelope, result operation.Result) error {
	var observed host.ArtifactObservation
	decoder := json.NewDecoder(bytes.NewReader(result.Observation))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return errors.New("host Artifact observation is invalid")
	}
	artifact := envelope.Operation.Input.Artifact
	if artifact == nil {
		return errors.New("host Artifact observation has no planned Artifact")
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
