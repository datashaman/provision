package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"provision/internal/operation"
	"provision/internal/planner"
)

const hostExecutorPath = "/usr/local/libexec/provision-host-executor"
const artifactCachePath = "/var/lib/provision/artifacts/sha256"
const maxObservedArtifactBytes int64 = 512 << 20

type LocalHostHandler struct{}

type localArtifactObservation struct {
	Status string `json:"status"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Reason string `json:"reason,omitempty"`
}

func (LocalHostHandler) Observe(ctx context.Context, planned planner.Operation) (HandlerObservation, error) {
	if planned.Kind != planner.StageArtifact || planned.Input.Artifact == nil {
		return HandlerObservation{}, errors.New("local Host Handler can observe only Artifact preparation")
	}
	select {
	case <-ctx.Done():
		return HandlerObservation{}, ctx.Err()
	default:
	}
	artifact := planned.Input.Artifact
	path, err := localArtifactPath(artifact.Digest)
	if err != nil {
		return HandlerObservation{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		evidence, _ := json.Marshal(localArtifactObservation{Status: "absent", Path: path, Digest: artifact.Digest})
		return HandlerObservation{State: ObservationPending, Evidence: evidence}, nil
	}
	if err != nil {
		return HandlerObservation{}, fmt.Errorf("observe Artifact cache: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0644 || info.Size() > maxObservedArtifactBytes {
		evidence, _ := json.Marshal(localArtifactObservation{Status: "invalid", Path: path, Digest: artifact.Digest, Size: info.Size(), Reason: "cache entry is not a safe bounded regular file"})
		return HandlerObservation{State: ObservationUnknown, Evidence: evidence}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return HandlerObservation{}, fmt.Errorf("observe Artifact cache: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxObservedArtifactBytes+1)); err != nil {
		return HandlerObservation{}, fmt.Errorf("observe Artifact cache: %w", err)
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != artifact.Digest {
		evidence, _ := json.Marshal(localArtifactObservation{Status: "invalid", Path: path, Digest: actual, Size: info.Size(), Reason: "cache entry digest does not match the Plan"})
		return HandlerObservation{State: ObservationUnknown, Evidence: evidence}, nil
	}
	evidence, _ := json.Marshal(localArtifactObservation{Status: "already-present", Path: path, Digest: actual, Size: info.Size()})
	return HandlerObservation{State: ObservationSatisfied, Evidence: evidence}, nil
}

func (LocalHostHandler) ApplyOrResume(ctx context.Context, envelope operation.Envelope) (operation.Result, error) {
	if envelope.SchemaVersion != operation.EnvelopeSchemaVersion {
		return operation.Result{}, errors.New("host operation envelope schema is unsupported")
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return operation.Result{}, fmt.Errorf("encode host operation: %w", err)
	}
	claim := envelope.Authorization.Claim
	command := exec.CommandContext(ctx, "sudo", "-n", hostExecutorPath, "execute", "--environment", claim.Environment, "--operator", claim.Target.Operator)
	command.Stdin = bytes.NewReader(encoded)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return operation.Result{}, fmt.Errorf("prepare host operation output: %w", err)
	}
	if err := command.Start(); err != nil {
		return operation.Result{}, fmt.Errorf("start restricted host operation: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, 1<<20+1))
	waitErr := command.Wait()
	if readErr != nil || len(data) > 1<<20 {
		return operation.Result{}, errors.New("restricted host operation returned invalid output")
	}
	if waitErr != nil {
		return operation.Result{}, fmt.Errorf("restricted host operation failed: %w: %s", waitErr, bytes.TrimSpace(stderr.Bytes()))
	}
	var result operation.Result
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return operation.Result{}, errors.New("restricted host operation returned invalid structured output")
	}
	return result, nil
}

func (LocalHostHandler) Verify(envelope operation.Envelope, result operation.Result) error {
	if err := result.ValidateAgainst(envelope); err != nil {
		return err
	}
	var observed localArtifactObservation
	decoder := json.NewDecoder(bytes.NewReader(result.Observation))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return errors.New("host Artifact observation is invalid")
	}
	artifact := envelope.Operation.Input.Artifact
	if artifact == nil {
		return errors.New("host Artifact observation has no planned Artifact")
	}
	expectedPath, err := localArtifactPath(artifact.Digest)
	if err != nil {
		return err
	}
	if observed.Path != expectedPath || observed.Digest != artifact.Digest || observed.Size < 0 || observed.Size > maxObservedArtifactBytes {
		return errors.New("host Artifact observation does not match the Plan")
	}
	if result.Outcome == operation.OutcomeSucceeded && observed.Status != "staged" && observed.Status != "already-present" {
		return errors.New("host Artifact success observation is invalid")
	}
	if result.Outcome == operation.OutcomeFailed && (observed.Status != "failed" || observed.Reason == "") {
		return errors.New("host Artifact failure observation is invalid")
	}
	return nil
}

func (LocalHostHandler) Recovery(planned planner.Operation) planner.RecoveryMode {
	return planned.Recovery
}

func localArtifactPath(digest string) (string, error) {
	encoded := strings.TrimPrefix(digest, "sha256:")
	decoded, err := hex.DecodeString(encoded)
	if !strings.HasPrefix(digest, "sha256:") || len(decoded) != sha256.Size || err != nil || strings.ToLower(digest) != digest {
		return "", errors.New("planned Artifact digest is invalid")
	}
	return filepath.Join(artifactCachePath, encoded), nil
}
