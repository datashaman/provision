package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"provision/internal/operation"
)

const hostExecutorPath = "/usr/local/libexec/provision-host-executor"

type LocalHostHandler struct{}

func (LocalHostHandler) Execute(ctx context.Context, envelope operation.Envelope) (operation.Result, error) {
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
