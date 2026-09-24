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
