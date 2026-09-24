package execution

import (
	"context"
	"errors"
	"os/exec"

	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

type LocalHostHandler struct{}

func (LocalHostHandler) Observe(ctx context.Context, planned planner.Operation) (HandlerObservation, error) {
	if planned.Kind != planner.StageArtifact || planned.Input.Artifact == nil {
		return HandlerObservation{}, errors.New("local Host Handler can observe only Artifact preparation")
	}
	select {
	case <-ctx.Done():
		return HandlerObservation{}, ctx.Err()
	default:
	}
	observed, err := host.ObserveArtifactCache(host.ArtifactCacheRoot, planned.Input.Artifact.Digest)
	if err != nil {
		return HandlerObservation{}, err
	}
	return artifactHandlerObservation(observed)
}

func (LocalHostHandler) ApplyOrResume(ctx context.Context, envelope operation.Envelope) (operation.Result, error) {
	if envelope.SchemaVersion != operation.EnvelopeSchemaVersion {
		return operation.Result{}, errors.New("host operation envelope schema is unsupported")
	}
	if !envelope.Authorization.Claim.Target.Local || envelope.Authorization.Claim.Target.Address != "" {
		return operation.Result{}, errors.New("local Host Handler received a different Host Target")
	}
	claim := envelope.Authorization.Claim
	command := exec.CommandContext(ctx, "sudo", "-n", host.ExecutorPath, "execute", "--environment", claim.Environment, "--operator", claim.Target.Operator)
	return executeHostEnvelope(command, envelope, "restricted host operation")
}

func (LocalHostHandler) Verify(envelope operation.Envelope, result operation.Result) error {
	return verifyHostResult(envelope, result)
}

func (LocalHostHandler) Recovery(planned planner.Operation) planner.RecoveryMode {
	return planned.Recovery
}
