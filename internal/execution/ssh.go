package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

type SSHHostHandler struct {
	target      planner.Target
	environment string
}

func NewHostHandler(target planner.Target, environment string) (Handler, error) {
	if target.Kind != "host" || target.Name == "" || environment == "" {
		return nil, errors.New("Host Handler requires an identified Host Target and Environment")
	}
	if target.Local {
		if target.Address != "" || target.User == "" {
			return nil, errors.New("local Host Target is invalid")
		}
		return LocalHostHandler{target: target, environment: environment}, nil
	}
	if err := (host.Target{Address: target.Address, User: target.User}).Validate(); err != nil {
		return nil, err
	}
	return SSHHostHandler{target: target, environment: environment}, nil
}

func (h SSHHostHandler) Observe(ctx context.Context, planned planner.Operation) (HandlerObservation, error) {
	if planned.Kind != planner.StageArtifact {
		command := exec.CommandContext(ctx, "ssh", host.StrictSSHArguments(host.Target{Address: h.target.Address, User: h.target.User},
			"sudo", "-n", host.ExecutorPath, "observe-operation",
			"--environment", h.environment,
			"--operator", h.target.User,
		)...)
		return executeHostObservation(command, planned, "remote Host Target observation")
	}
	artifact, err := plannedArtifactInput(planned)
	if err != nil {
		return HandlerObservation{}, err
	}
	if _, err := host.ArtifactCachePath(host.ArtifactCacheRoot, artifact.Digest); err != nil {
		return HandlerObservation{}, err
	}
	command := exec.CommandContext(ctx, "ssh", host.StrictSSHArguments(host.Target{Address: h.target.Address, User: h.target.User},
		"sudo", "-n", host.ExecutorPath, "observe-artifact",
		"--environment", h.environment,
		"--operator", h.target.User,
		"--digest", artifact.Digest,
	)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return HandlerObservation{}, fmt.Errorf("observe remote Host Target: %w: %s", err, bytes.TrimSpace(output))
	}
	var observed host.ArtifactObservation
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return HandlerObservation{}, errors.New("remote Host Target returned invalid Artifact observation")
	}
	return artifactHandlerObservation(observed)
}

func (h SSHHostHandler) ApplyOrResume(ctx context.Context, envelope operation.Envelope) (operation.Result, error) {
	if envelope.SchemaVersion != operation.EnvelopeSchemaVersion {
		return operation.Result{}, errors.New("host operation envelope schema is unsupported")
	}
	claim := envelope.Authorization.Claim
	if claim.Target.Local || claim.Target.Name != h.target.Name || claim.Target.Address != h.target.Address || claim.Target.Operator != h.target.User || claim.Environment != h.environment {
		return operation.Result{}, errors.New("SSH Host Handler received a different Host Target")
	}
	command := exec.CommandContext(ctx, "ssh", host.StrictSSHArguments(host.Target{Address: h.target.Address, User: h.target.User},
		"sudo", "-n", host.ExecutorPath, "execute",
		"--environment", h.environment,
		"--operator", h.target.User,
	)...)
	return executeHostEnvelope(command, envelope, "remote Host Target operation")
}

func (h SSHHostHandler) Verify(envelope operation.Envelope, result operation.Result) error {
	return verifyHostResult(envelope, result)
}

func (h SSHHostHandler) Recovery(planned planner.Operation) planner.RecoveryMode {
	return planned.Recovery
}
