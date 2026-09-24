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
	switch envelope.Operation.Kind {
	case planner.StageArtifact:
		return verifyArtifactResult(envelope, result)
	case planner.InstallGeneration:
		return verifyGenerationResult(envelope, result)
	case planner.StartCandidate:
		return verifySystemdResult(envelope, result)
	case planner.VerifyCandidate:
		return verifyHealthResult(envelope, result)
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
	if observed.GenerationID != input.GenerationID || observed.Revision != input.Revision || observed.Unit != input.Unit || observed.Port != input.Port || observed.ReleaseDirectory != input.ReleaseDirectory {
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
	if observed.GenerationID != input.GenerationID || observed.Revision != input.Revision || observed.Port != input.Port || len(observed.Checks) == 0 {
		return errors.New("host health observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded {
		expected := []struct{ name, path string }{{"liveness", input.LivenessPath}, {"readiness", input.ReadinessPath}, {"candidateVerification", input.CandidateVerifyPath}}
		if observed.Status != host.CandidateHealthy || !observed.SwitchEligible || len(observed.Checks) != len(expected) {
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
