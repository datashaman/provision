package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/planner"
	"provision/internal/scheduler"
)

const (
	asyncGenerationSchema = "provision.dev/async-generation/v1alpha1"
)

var taskTemplateUnit = regexp.MustCompile(`^provision-[a-z0-9-]+@\.service$`)

func isAsyncWorkloadKind(kind planner.OperationKind) bool {
	switch kind {
	case planner.InstallTaskGeneration, planner.VerifyTaskGeneration,
		planner.InstallWorkerGeneration, planner.StartWorkerCandidate, planner.VerifyWorkerCandidate,
		planner.ActivateWorkerIntake, planner.VerifyWorkerActive,
		planner.InstallScheduleRuntime, planner.HandoffSchedule, planner.VerifySchedule:
		return true
	default:
		return false
	}
}

type installedTaskGeneration struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Input         planner.AsyncTaskInput `json:"input"`
	Executable    string                 `json:"executable"`
	Verified      bool                   `json:"verified"`
}

type installedWorkerGeneration struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Input         planner.AsyncWorkerInput `json:"input"`
	Executable    string                   `json:"executable"`
	Verified      bool                     `json:"verified"`
	Active        bool                     `json:"active"`
}

type exampleWorkerState struct {
	SchemaVersion        string `json:"schemaVersion"`
	ApplicationRevision  string `json:"applicationRevision"`
	WorkerArtifactDigest string `json:"workerArtifactDigest"`
	Queue                string `json:"queue"`
	Connected            bool   `json:"connected"`
	Gated                bool   `json:"gated"`
	Consuming            bool   `json:"consuming"`
	InFlightMessageID    string `json:"inFlightMessageId,omitempty"`
}

func validateAsyncWorkloadOperation(planned planner.Operation, record bootstrapRecord, paths executionPaths) error {
	if planned.Input.Async == nil || planned.Input.Artifact != nil || planned.Input.Generation != nil || planned.Input.Systemd != nil || planned.Input.Health != nil || planned.Input.Endpoint != nil || planned.Input.Previous != nil || planned.Input.Drain != nil || planned.Input.Retention != nil {
		return errors.New("asynchronous operation requires only its typed asynchronous input")
	}
	input := planned.Input.Async
	switch planned.Kind {
	case planner.InstallTaskGeneration, planner.VerifyTaskGeneration:
		if input.Task == nil || input.Queue != nil || input.Artifact != nil || input.Worker != nil || input.Schedule != nil || input.Runtime != nil {
			return errors.New("Task operation requires only its typed Task input")
		}
		return validateAsyncTaskInput(*input.Task, record, paths)
	case planner.InstallWorkerGeneration, planner.StartWorkerCandidate, planner.VerifyWorkerCandidate, planner.ActivateWorkerIntake, planner.VerifyWorkerActive:
		if input.Worker == nil || input.Queue != nil || input.Artifact != nil || input.Task != nil || input.Schedule != nil || input.Runtime != nil {
			return errors.New("Worker operation requires only its typed Worker input")
		}
		return validateAsyncWorkerInput(*input.Worker, record, paths)
	case planner.InstallScheduleRuntime:
		if input.Runtime == nil || input.Queue != nil || input.Artifact != nil || input.Task != nil || input.Worker != nil || input.Schedule != nil {
			return errors.New("Schedule runtime operation requires only its typed runtime input")
		}
		if input.Runtime.LedgerSchema != scheduler.SchemaVersion || !digestPattern.MatchString(input.Runtime.AppletDigest) {
			return errors.New("Schedule runtime identity is unsupported")
		}
		return nil
	case planner.HandoffSchedule, planner.VerifySchedule:
		if input.Schedule == nil || input.Queue != nil || input.Artifact != nil || input.Task != nil || input.Worker != nil || input.Runtime != nil {
			return errors.New("Schedule operation requires only its typed Schedule input")
		}
		return validateAsyncScheduleInput(*input.Schedule, record)
	default:
		return errors.New("host executor does not allow this asynchronous operation kind")
	}
}

func validateAsyncTaskInput(input planner.AsyncTaskInput, record bootstrapRecord, paths executionPaths) error {
	expectedQueue := "provision-" + record.Environment + "-" + input.Queue
	expectedDirectory := filepath.Join(paths.environmentHome, "releases", input.GenerationID)
	if !deploymentIdentifier.MatchString(input.Component) || !deploymentIdentifier.MatchString(input.Queue) || input.QueueLogicalID != expectedQueue || !deploymentIdentifier.MatchString(input.GenerationID) || !deploymentIdentifier.MatchString(input.Revision) || !digestPattern.MatchString(input.ArtifactDigest) || !digestPattern.MatchString(input.ConfigurationDigest) || input.SystemdUnit != fmt.Sprintf("provision-%s-%s-%s@.service", record.Environment, input.Component, strings.TrimPrefix(input.ArtifactDigest, "sha256:")[:12]) || !taskTemplateUnit.MatchString(input.SystemdUnit) || input.Rollout != "required" {
		return errors.New("Task input does not match the bootstrapped Environment or fixed generation identity")
	}
	if expectedDirectory != asyncGenerationReference(input.GenerationID, input.Revision, input.ArtifactDigest, record, paths).ReleaseDirectory {
		return errors.New("Task release directory is invalid")
	}
	duration, err := time.ParseDuration(input.Timeout)
	if err != nil || duration < time.Second || duration > time.Hour || duration.String() != input.Timeout {
		return errors.New("Task timeout is unsupported")
	}
	return nil
}

func validateAsyncWorkerInput(input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) error {
	expectedQueue := "provision-" + record.Environment + "-" + input.Queue
	if !deploymentIdentifier.MatchString(input.Component) || !deploymentIdentifier.MatchString(input.Queue) || input.QueueLogicalID != expectedQueue || !deploymentIdentifier.MatchString(input.GenerationID) || !deploymentIdentifier.MatchString(input.Revision) || !digestPattern.MatchString(input.ArtifactDigest) || input.SystemdUnit != fmt.Sprintf("provision-%s-%s-%s.service", record.Environment, input.Component, strings.TrimPrefix(input.ArtifactDigest, "sha256:")[:12]) || !systemdUnit.MatchString(input.SystemdUnit) || input.Admission != "gated" || input.Drain.Mode != "bounded-in-flight" || input.Rollout != "required" {
		return errors.New("Worker input does not match the bootstrapped Environment or fixed generation identity")
	}
	duration, err := time.ParseDuration(input.Drain.MaxDuration)
	if err != nil || duration < time.Second || duration > 5*time.Minute || duration.String() != input.Drain.MaxDuration {
		return errors.New("Worker drain bound is unsupported")
	}
	if input.Previous != nil {
		if err := validatePlannedPreviousWorker(input, record, paths); err != nil {
			return err
		}
	}
	return validateGenerationReference(asyncGenerationReference(input.GenerationID, input.Revision, input.ArtifactDigest, record, paths), record, paths)
}

func validatePlannedPreviousWorker(input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) error {
	previous := input.Previous
	if previous.ID == input.GenerationID || previous.ArtifactDigest == input.ArtifactDigest || previous.SystemdUnit == input.SystemdUnit {
		return errors.New("Worker candidate must be a separate immutable Generation from the active Worker")
	}
	active, err := readWorkerGenerationRecord(filepath.Join(paths.environmentHome, "workers", "active.json"))
	if err != nil || !active.Active || active.Input.GenerationID != previous.ID || active.Input.Revision != previous.Revision || active.Input.ArtifactDigest != previous.ArtifactDigest || active.Input.SystemdUnit != previous.SystemdUnit {
		return errors.New("planned previous Worker does not match the durable active Worker record")
	}
	gate, err := os.ReadFile(workerGatePath(paths, active.Input.GenerationID))
	if err != nil || string(gate) != "open\n" {
		return errors.New("planned previous Worker intake is not durably open")
	}
	state, err := readWorkerState(workerStatePath(paths, active.Input.GenerationID))
	if err != nil || state.ApplicationRevision != active.Input.Revision || state.WorkerArtifactDigest != active.Input.ArtifactDigest || state.Queue != active.Input.QueueLogicalID || !state.Connected || state.Gated || !state.Consuming {
		return errors.New("planned previous Worker runtime state is not exact and active")
	}
	return nil
}

func validateAsyncScheduleInput(input planner.AsyncScheduleInput, record bootstrapRecord) error {
	if !deploymentIdentifier.MatchString(input.Component) || !deploymentIdentifier.MatchString(input.Task) || !deploymentIdentifier.MatchString(input.TaskGenerationID) || !deploymentIdentifier.MatchString(input.ApplicationRevision) || !digestPattern.MatchString(input.ConfigurationDigest) || !taskTemplateUnit.MatchString(input.TaskUnit) || input.TimerUnit != fmt.Sprintf("provision-%s-%s.timer", record.Environment, input.Component) || input.Expression != "* * * * *" || input.Timezone == "" || input.DaylightSaving != "wall-clock" || input.Overlap != "forbid" || input.Retry.MaxAttempts != 1 || input.MissedRun.Mode != "skip" || input.MissedRun.MaxOccurrences != 0 || input.Failure != "record" || input.Rollout != "required" || !digestPattern.MatchString(input.AppletDigest) || input.LedgerSchema != scheduler.SchemaVersion {
		return errors.New("Schedule input does not match the supported initial runtime contract")
	}
	return nil
}

func asyncGenerationReference(id, revision, digest string, record bootstrapRecord, paths executionPaths) planner.GenerationReference {
	return planner.GenerationReference{ID: id, Revision: revision, ArtifactDigest: digest, Account: record.Account, ReleaseDirectory: filepath.Join(paths.environmentHome, "releases", id)}
}

func observeAsyncWorkloadOperation(ctx context.Context, planned planner.Operation, record bootstrapRecord, paths executionPaths) (host.OperationObservation, error) {
	if err := validateAsyncWorkloadOperation(planned, record, paths); err != nil {
		return host.OperationObservation{}, err
	}
	var evidence any
	state := "pending"
	switch planned.Kind {
	case planner.InstallTaskGeneration:
		observed := observeTaskGeneration(ctx, *planned.Input.Async.Task, record, paths)
		evidence = observed
		state = asyncObservationState(observed.Status, "installed")
	case planner.VerifyTaskGeneration:
		observed := observeTaskGeneration(ctx, *planned.Input.Async.Task, record, paths)
		evidence = observed
		if observed.Verified {
			state = "satisfied"
		} else if observed.Status != "installed" {
			state = asyncObservationState(observed.Status, "installed")
		}
	case planner.InstallWorkerGeneration:
		observed := observeWorkerGeneration(ctx, *planned.Input.Async.Worker, record, paths)
		evidence = observed
		state = asyncObservationState(observed.Status, "installed")
	case planner.StartWorkerCandidate:
		observed := observeWorkerGeneration(ctx, *planned.Input.Async.Worker, record, paths)
		evidence = observed
		if observed.Status == "active-gated" || observed.Status == "active-open" {
			state = "satisfied"
		} else if observed.Status != "installed" {
			state = asyncObservationState(observed.Status, "installed")
		}
	case planner.VerifyWorkerCandidate:
		observed := observeWorkerGeneration(ctx, *planned.Input.Async.Worker, record, paths)
		evidence = observed
		if observed.Verified && observed.Worker.Gate == "closed" {
			state = "satisfied"
		} else if observed.Status != "active-gated" {
			state = asyncObservationState(observed.Status, "active-gated")
		}
	case planner.ActivateWorkerIntake, planner.VerifyWorkerActive:
		observed := observeWorkerGeneration(ctx, *planned.Input.Async.Worker, record, paths)
		evidence = observed
		if workerOperationSatisfied(planned.Kind, observed) {
			state = "satisfied"
		} else if observed.Status != "active-gated" && observed.Status != "active-open" {
			state = asyncObservationState(observed.Status, "active-gated")
		}
	case planner.InstallScheduleRuntime:
		observed := observeScheduleRuntime(*planned.Input.Async.Runtime, paths)
		evidence = observed
		state = asyncObservationState(observed.Status, "installed")
	case planner.HandoffSchedule:
		observed := observeInstalledSchedule(ctx, *planned.Input.Async.Schedule, record, paths, false)
		evidence = observed
		state = asyncObservationState(observed.Status, "active")
	case planner.VerifySchedule:
		observed := observeInstalledSchedule(ctx, *planned.Input.Async.Schedule, record, paths, true)
		evidence = observed
		state = asyncObservationState(observed.Status, "verified")
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return host.OperationObservation{}, err
	}
	return host.OperationObservation{State: state, Evidence: encoded}, nil
}

func workerOperationSatisfied(kind planner.OperationKind, observed host.AsyncWorkerOperationObservation) bool {
	if !observed.Verified || !observed.Worker.UnitActive || !observed.Worker.QueueConnected {
		return false
	}
	switch kind {
	case planner.VerifyWorkerActive:
		return observed.Status == "active-open" && observed.Worker.Gate == "open" && observed.Worker.Active
	case planner.ActivateWorkerIntake:
		return observed.Status == "active-open" && observed.Worker.Gate == "open"
	default:
		return false
	}
}

func asyncObservationState(status, satisfied string) string {
	if status == satisfied {
		return "satisfied"
	}
	if status == "absent" || status == "installed" || status == "active-gated" {
		return "pending"
	}
	return "unknown"
}

func applyAsyncWorkloadOperation(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths) (json.RawMessage, error) {
	if err := validateAsyncWorkloadOperation(planned, record, paths); err != nil {
		return nil, err
	}
	var observed any
	var actionErr error
	switch planned.Kind {
	case planner.InstallTaskGeneration:
		observed, actionErr = installTaskGeneration(ctx, *planned.Input.Async.Task, record, paths, claim.AttemptID)
	case planner.VerifyTaskGeneration:
		observed, actionErr = verifyTaskGeneration(ctx, *planned.Input.Async.Task, record, paths)
	case planner.InstallWorkerGeneration:
		observed, actionErr = installWorkerGeneration(ctx, *planned.Input.Async.Worker, record, paths, claim.AttemptID)
	case planner.StartWorkerCandidate:
		observed, actionErr = startWorkerCandidate(ctx, *planned.Input.Async.Worker, record, paths)
	case planner.VerifyWorkerCandidate:
		observed, actionErr = verifyWorkerCandidate(ctx, *planned.Input.Async.Worker, record, paths, true)
	case planner.ActivateWorkerIntake:
		observed, actionErr = activateWorkerIntake(ctx, *planned.Input.Async.Worker, record, paths)
	case planner.VerifyWorkerActive:
		observed, actionErr = verifyWorkerCandidate(ctx, *planned.Input.Async.Worker, record, paths, false)
	case planner.InstallScheduleRuntime:
		observed, actionErr = installScheduleRuntime(*planned.Input.Async.Runtime, paths)
	case planner.HandoffSchedule:
		observed, actionErr = handoffInitialSchedule(ctx, *planned.Input.Async.Schedule, claim.FencingToken, record, paths)
	case planner.VerifySchedule:
		observed, actionErr = verifyInitialSchedule(ctx, *planned.Input.Async.Schedule, record, paths)
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		return nil, err
	}
	return encoded, actionErr
}

func installTaskGeneration(ctx context.Context, input planner.AsyncTaskInput, record bootstrapRecord, paths executionPaths, attempt string) (host.AsyncTaskOperationObservation, error) {
	reference := asyncGenerationReference(input.GenerationID, input.Revision, input.ArtifactDigest, record, paths)
	generation, err := installGeneration(paths, planner.GenerationInput{GenerationReference: reference}, attempt)
	if err != nil {
		return host.AsyncTaskOperationObservation{Status: "failed", Task: taskStatus(input, record, paths), Reason: err.Error()}, err
	}
	uid, gid := accountUID(record.Account), accountGID(record.Account)
	if err := ensureAsyncDataRoots(record, paths, uid, gid); err != nil {
		return host.AsyncTaskOperationObservation{Status: "failed", Task: taskStatus(input, record, paths), Reason: err.Error()}, err
	}
	unit := renderTaskUnit(input, record, paths, generation.Executable)
	if err := installExactFile(filepath.Join(paths.systemdUnits, input.SystemdUnit), []byte(unit), 0644, 0, 0); err != nil {
		return host.AsyncTaskOperationObservation{Status: "failed", Task: taskStatus(input, record, paths), Reason: err.Error()}, err
	}
	recordPath := taskGenerationRecordPath(paths, input.GenerationID)
	if err := ensureDirectory(filepath.Dir(recordPath), 0755, 0, 0); err != nil {
		return host.AsyncTaskOperationObservation{Status: "failed", Task: taskStatus(input, record, paths), Reason: err.Error()}, err
	}
	installed := installedTaskGeneration{SchemaVersion: asyncGenerationSchema, Input: input, Executable: generation.Executable}
	if err := writeJSONAtomic(recordPath, installed, 0444); err != nil {
		return host.AsyncTaskOperationObservation{Status: "failed", Task: taskStatus(input, record, paths), Reason: err.Error()}, err
	}
	_, actionErr := paths.systemd.Run(ctx, "daemon-reload")
	observed := observeTaskGeneration(ctx, input, record, paths)
	if actionErr != nil || observed.Status != "installed" {
		return observed, errors.New("installed Task generation failed exact verification")
	}
	return observed, nil
}

func verifyTaskGeneration(ctx context.Context, input planner.AsyncTaskInput, record bootstrapRecord, paths executionPaths) (host.AsyncTaskOperationObservation, error) {
	observed := observeTaskGeneration(ctx, input, record, paths)
	if observed.Status != "installed" || observed.Executable == "" {
		return observed, errors.New("Task generation is not installed exactly")
	}
	if output, err := execCommandContext(ctx, "systemd-analyze", "verify", filepath.Join(paths.systemdUnits, input.SystemdUnit)); err != nil {
		observed.Status = "failed"
		observed.Reason = "Task systemd template did not verify: " + strings.TrimSpace(string(output))
		return observed, errors.New(observed.Reason)
	}
	recordPath := taskGenerationRecordPath(paths, input.GenerationID)
	installed, err := readTaskGenerationRecord(recordPath)
	if err != nil {
		return observed, err
	}
	installed.Verified = true
	if err := writeJSONAtomic(recordPath, installed, 0444); err != nil {
		return observed, err
	}
	if err := writeJSONAtomic(filepath.Join(filepath.Dir(recordPath), "active.json"), installed, 0444); err != nil {
		return observed, err
	}
	return observeTaskGeneration(ctx, input, record, paths), nil
}

func installWorkerGeneration(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths, attempt string) (host.AsyncWorkerOperationObservation, error) {
	reference := asyncGenerationReference(input.GenerationID, input.Revision, input.ArtifactDigest, record, paths)
	generation, err := installGeneration(paths, planner.GenerationInput{GenerationReference: reference}, attempt)
	if err != nil {
		return host.AsyncWorkerOperationObservation{Status: "failed", Worker: workerStatus(input, record, paths), Reason: err.Error()}, err
	}
	uid, gid := accountUID(record.Account), accountGID(record.Account)
	if err := ensureAsyncDataRoots(record, paths, uid, gid); err != nil {
		return host.AsyncWorkerOperationObservation{Status: "failed", Worker: workerStatus(input, record, paths), Reason: err.Error()}, err
	}
	gatePath := workerGatePath(paths, input.GenerationID)
	if err := installExactFile(gatePath, []byte("closed\n"), 0644, 0, 0); err != nil {
		return host.AsyncWorkerOperationObservation{Status: "failed", Worker: workerStatus(input, record, paths), Reason: err.Error()}, err
	}
	unit := renderWorkerUnit(input, record, paths, generation.Executable)
	if err := installExactFile(filepath.Join(paths.systemdUnits, input.SystemdUnit), []byte(unit), 0644, 0, 0); err != nil {
		return host.AsyncWorkerOperationObservation{Status: "failed", Worker: workerStatus(input, record, paths), Reason: err.Error()}, err
	}
	recordPath := workerGenerationRecordPath(paths, input.GenerationID)
	if err := ensureDirectory(filepath.Dir(recordPath), 0755, 0, 0); err != nil {
		return host.AsyncWorkerOperationObservation{Status: "failed", Worker: workerStatus(input, record, paths), Reason: err.Error()}, err
	}
	installed := installedWorkerGeneration{SchemaVersion: asyncGenerationSchema, Input: input, Executable: generation.Executable}
	if err := writeJSONAtomic(recordPath, installed, 0444); err != nil {
		return host.AsyncWorkerOperationObservation{Status: "failed", Worker: workerStatus(input, record, paths), Reason: err.Error()}, err
	}
	_, actionErr := paths.systemd.Run(ctx, "daemon-reload")
	observed := observeWorkerGeneration(ctx, input, record, paths)
	if actionErr != nil || observed.Status != "installed" {
		return observed, errors.New("installed Worker generation failed exact verification")
	}
	return observed, nil
}

func startWorkerCandidate(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) (host.AsyncWorkerOperationObservation, error) {
	if _, err := paths.systemd.Run(ctx, "enable", "--now", input.SystemdUnit); err != nil {
		observed := observeWorkerGeneration(ctx, input, record, paths)
		observed.Status, observed.Reason = "failed", "start gated Worker candidate"
		return observed, errors.New(observed.Reason)
	}
	return waitForWorker(ctx, input, record, paths, true)
}

func verifyWorkerCandidate(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths, gated bool) (host.AsyncWorkerOperationObservation, error) {
	observed, err := waitForWorker(ctx, input, record, paths, gated)
	if err != nil {
		return observed, err
	}
	recordPath := workerGenerationRecordPath(paths, input.GenerationID)
	installed, err := readWorkerGenerationRecord(recordPath)
	if err != nil {
		return observed, err
	}
	installed.Verified = true
	if !gated {
		installed.Active = true
	}
	if err := writeJSONAtomic(recordPath, installed, 0444); err != nil {
		return observed, err
	}
	if !gated {
		if err := writeJSONAtomic(filepath.Join(filepath.Dir(recordPath), "active.json"), installed, 0444); err != nil {
			return observed, err
		}
	}
	observed = observeWorkerGeneration(ctx, input, record, paths)
	observed.Verified = true
	return observed, nil
}

func activateWorkerIntake(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) (host.AsyncWorkerOperationObservation, error) {
	if err := replaceGateFile(workerGatePath(paths, input.GenerationID), "open\n"); err != nil {
		observed := observeWorkerGeneration(ctx, input, record, paths)
		observed.Status, observed.Reason = "failed", err.Error()
		return observed, err
	}
	return waitForWorker(ctx, input, record, paths, false)
}

func waitForWorker(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths, gated bool) (host.AsyncWorkerOperationObservation, error) {
	deadline := time.Now().Add(paths.healthTimeout)
	for {
		observed := observeWorkerGeneration(ctx, input, record, paths)
		gatedReady := gated && observed.Worker.Gate == "closed" && observed.Checks.Liveness && observed.Checks.QueueConnectivity && observed.Checks.RevisionIdentity && observed.Checks.IntakeDisabled
		activeReady := !gated && observed.Worker.Gate == "open" && observed.Checks.Liveness && observed.Checks.QueueConnectivity && observed.Checks.RevisionIdentity
		if gatedReady || activeReady {
			return observed, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			observed.Status = "failed"
			diagnostic := inspectSystemdUnit(ctx, paths.systemd, input.SystemdUnit)
			observed.UnitDiagnostic = &diagnostic
			observed.Reason = summarizeWorkerVerificationFailure(input.SystemdUnit, observed.Checks, diagnostic)
			observed.Worker.Health = "candidate-unverified"
			observed.Worker.Reason = observed.Reason
			observed.Worker.RecoveryAction = "keep intake closed; inspect the failed verification checks and create a new approved Plan after correcting the candidate"
			return observed, errors.New(observed.Reason)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func summarizeWorkerVerificationFailure(unit string, checks host.WorkerVerificationChecks, diagnostic host.SystemdUnitDiagnostic) string {
	return fmt.Sprintf("Worker candidate verification failed for %s (liveness=%t, queueConnectivity=%t, revisionIdentity=%t, intakeDisabled=%t): %s", unit, checks.Liveness, checks.QueueConnectivity, checks.RevisionIdentity, checks.IntakeDisabled, summarizeWorkerUnitFailure(unit, diagnostic))
}

func inspectSystemdUnit(ctx context.Context, systemd systemdController, unit string) host.SystemdUnitDiagnostic {
	output, _ := systemd.Run(ctx, "show", unit, "--property=LoadState,ActiveState,SubState,Result,ExecMainCode,ExecMainStatus,NRestarts", "--no-pager")
	return parseSystemdUnitDiagnostic(output)
}

func parseSystemdUnitDiagnostic(output []byte) host.SystemdUnitDiagnostic {
	values := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	diagnostic := host.SystemdUnitDiagnostic{
		LoadState: values["LoadState"], ActiveState: values["ActiveState"], SubState: values["SubState"], Result: values["Result"],
	}
	diagnostic.ExecMainCode, _ = strconv.Atoi(values["ExecMainCode"])
	diagnostic.ExecMainStatus, _ = strconv.Atoi(values["ExecMainStatus"])
	diagnostic.RestartCount, _ = strconv.Atoi(values["NRestarts"])
	switch diagnostic.ExecMainStatus {
	case 200:
		diagnostic.FailureStage = "working-directory"
	case 203:
		diagnostic.FailureStage = "executable"
	case 216:
		diagnostic.FailureStage = "group"
	case 217:
		diagnostic.FailureStage = "user"
	case 243:
		diagnostic.FailureStage = "credentials"
	}
	return diagnostic
}

func summarizeWorkerUnitFailure(unit string, diagnostic host.SystemdUnitDiagnostic) string {
	if diagnostic.ExecMainStatus == 243 {
		return fmt.Sprintf("Worker unit %s failed during systemd credential setup (active=%s/%s, result=%s, exit status 243/CREDENTIALS, restarts=%d)", unit, diagnostic.ActiveState, diagnostic.SubState, diagnostic.Result, diagnostic.RestartCount)
	}
	if diagnostic.ActiveState != "" || diagnostic.Result != "" {
		return fmt.Sprintf("Worker unit %s did not reach its planned admission and Queue state (active=%s/%s, result=%s, exit code=%d, exit status=%d, restarts=%d)", unit, diagnostic.ActiveState, diagnostic.SubState, diagnostic.Result, diagnostic.ExecMainCode, diagnostic.ExecMainStatus, diagnostic.RestartCount)
	}
	return fmt.Sprintf("Worker unit %s did not reach its planned admission and Queue state; systemd returned no unit diagnostic", unit)
}

func observeTaskGeneration(ctx context.Context, input planner.AsyncTaskInput, record bootstrapRecord, paths executionPaths) host.AsyncTaskOperationObservation {
	status := taskStatus(input, record, paths)
	result := host.AsyncTaskOperationObservation{Status: "absent", Task: status}
	reference := asyncGenerationReference(input.GenerationID, input.Revision, input.ArtifactDigest, record, paths)
	generation := observeGeneration(planner.GenerationInput{GenerationReference: reference})
	if generation.Status == host.CandidateAbsent {
		return result
	}
	if generation.Status != host.CandidateInstalled {
		result.Status, result.Reason = "unknown", generation.Reason
		return result
	}
	recorded, err := readTaskGenerationRecord(taskGenerationRecordPath(paths, input.GenerationID))
	if err != nil || !reflect.DeepEqual(recorded.Input, input) || recorded.Executable != generation.Executable || recorded.SchemaVersion != asyncGenerationSchema {
		result.Status, result.Reason = "unknown", "Task generation record differs from the approved Plan"
		return result
	}
	wanted := renderTaskUnit(input, record, paths, generation.Executable)
	if !exactRootFile(filepath.Join(paths.systemdUnits, input.SystemdUnit), []byte(wanted), 0644) {
		result.Status, result.Reason = "unknown", "Task systemd template differs from the approved Plan"
		return result
	}
	result.Status, result.Executable, result.Verified = "installed", generation.Executable, recorded.Verified
	return result
}

func observeWorkerGeneration(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) host.AsyncWorkerOperationObservation {
	status := workerStatus(input, record, paths)
	result := host.AsyncWorkerOperationObservation{Status: "absent", Worker: status}
	reference := asyncGenerationReference(input.GenerationID, input.Revision, input.ArtifactDigest, record, paths)
	generation := observeGeneration(planner.GenerationInput{GenerationReference: reference})
	if generation.Status == host.CandidateAbsent {
		return result
	}
	if generation.Status != host.CandidateInstalled {
		result.Status, result.Reason = "unknown", generation.Reason
		return result
	}
	recorded, err := readWorkerGenerationRecord(workerGenerationRecordPath(paths, input.GenerationID))
	if err != nil || !reflect.DeepEqual(recorded.Input, input) || recorded.Executable != generation.Executable || recorded.SchemaVersion != asyncGenerationSchema {
		result.Status, result.Reason = "unknown", "Worker generation record differs from the approved Plan"
		return result
	}
	status.Active = recorded.Active && exactActiveWorkerRecord(paths, recorded)
	if !exactRootFile(filepath.Join(paths.systemdUnits, input.SystemdUnit), []byte(renderWorkerUnit(input, record, paths, generation.Executable)), 0644) {
		result.Status, result.Reason = "unknown", "Worker systemd unit differs from the approved Plan"
		return result
	}
	result.Status, result.Verified = "installed", recorded.Verified
	active, _ := paths.systemd.Run(ctx, "is-active", input.SystemdUnit)
	status.UnitActive = strings.TrimSpace(string(active)) == "active"
	result.Checks.Liveness = status.UnitActive
	if !status.UnitActive {
		status.Health = "candidate-unverified"
		status.Reason = "Worker systemd unit is not active"
		status.RecoveryAction = "keep intake closed; inspect the candidate unit and create a new approved Plan after correcting the Artifact"
		result.Worker = status
		return result
	}
	state, err := readWorkerState(status.StatePath)
	if err != nil || state.SchemaVersion != "provision.dev/example-async-worker-state/v1alpha1" || state.ApplicationRevision != input.Revision || state.WorkerArtifactDigest != input.ArtifactDigest || state.Queue != input.QueueLogicalID {
		result.Status, result.Reason = "unknown", "Worker runtime state is missing or does not match its Generation"
		status.Health = "candidate-unverified"
		status.Reason = result.Reason
		status.RecoveryAction = "keep intake closed; repair the candidate runtime identity and create a new approved Plan"
		result.Worker = status
		return result
	}
	result.Checks.RevisionIdentity = true
	status.QueueConnected = state.Connected
	result.Checks.QueueConnectivity = state.Connected
	status.InFlight = 0
	if state.InFlightMessageID != "" {
		status.InFlight = 1
	}
	gate, gateErr := os.ReadFile(status.GatePath)
	if gateErr != nil {
		gate = nil
	}
	status.Gate, result.Status, result.Checks.IntakeDisabled = workerAdmissionObservation(gate, state)
	if result.Status == "active-gated" && result.Checks.Liveness && result.Checks.QueueConnectivity && result.Checks.RevisionIdentity && result.Checks.IntakeDisabled {
		status.Health = "candidate-gated"
	} else if result.Status == "active-open" {
		status.Health = "active"
	} else {
		status.Health = "candidate-unverified"
		status.Reason = "Worker candidate has not satisfied every verification gate"
		status.RecoveryAction = "keep intake closed; inspect liveness, Queue connectivity, Revision identity, and gate state before replanning"
	}
	result.Worker = status
	return result
}

func workerAdmissionObservation(gate []byte, state exampleWorkerState) (string, string, bool) {
	switch string(gate) {
	case "closed\n":
		if state.Gated && !state.Consuming {
			return "closed", "active-gated", true
		}
		return "closed", "unknown", false
	case "open\n":
		if !state.Gated && state.Consuming {
			return "open", "active-open", false
		}
		return "open", "unknown", false
	default:
		return "unknown", "unknown", false
	}
}

func exactActiveWorkerRecord(paths executionPaths, recorded installedWorkerGeneration) bool {
	active, err := readWorkerGenerationRecord(filepath.Join(paths.environmentHome, "workers", "active.json"))
	return err == nil && reflect.DeepEqual(active, recorded)
}

func installScheduleRuntime(input planner.AsyncRuntimeInput, paths executionPaths) (host.AsyncRuntimeObservation, error) {
	runtimePath := scheduleRuntimePath(paths)
	result := host.AsyncRuntimeObservation{Status: "absent", Path: runtimePath, AppletDigest: input.AppletDigest, LedgerSchema: input.LedgerSchema}
	executorDigest := regularFileDigest(host.ExecutorPath)
	if executorDigest != input.AppletDigest {
		result.Status, result.Reason = "failed", "installed executor bytes do not match the approved Schedule applet digest"
		return result, errors.New(result.Reason)
	}
	data, err := os.ReadFile(host.ExecutorPath)
	if err != nil {
		result.Status, result.Reason = "failed", "read approved Schedule runtime bytes"
		return result, errors.New(result.Reason)
	}
	if err := ensureDirectory(filepath.Dir(runtimePath), 0755, 0, 0); err != nil {
		result.Status, result.Reason = "failed", err.Error()
		return result, err
	}
	if err := installExactFile(runtimePath, data, 0555, 0, 0); err != nil {
		result.Status, result.Reason = "failed", err.Error()
		return result, err
	}
	return observeScheduleRuntime(input, paths), nil
}

func observeScheduleRuntime(input planner.AsyncRuntimeInput, paths executionPaths) host.AsyncRuntimeObservation {
	runtimePath := scheduleRuntimePath(paths)
	result := host.AsyncRuntimeObservation{Status: "absent", Path: runtimePath, AppletDigest: input.AppletDigest, LedgerSchema: input.LedgerSchema}
	info, err := os.Lstat(runtimePath)
	if errors.Is(err, os.ErrNotExist) {
		return result
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0555 || !ownedByExecutor(info) || regularFileDigest(runtimePath) != input.AppletDigest {
		result.Status, result.Reason = "unknown", "Schedule runtime bytes or permissions differ from the approved Plan"
		return result
	}
	result.Status = "installed"
	return result
}

func handoffInitialSchedule(ctx context.Context, input planner.AsyncScheduleInput, fencingToken int64, record bootstrapRecord, paths executionPaths) (host.AsyncScheduleOperationObservation, error) {
	runtime := observeScheduleRuntime(planner.AsyncRuntimeInput{AppletDigest: input.AppletDigest, LedgerSchema: input.LedgerSchema}, paths)
	if runtime.Status != "installed" {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: "approved Schedule runtime is not installed"}, errors.New("approved Schedule runtime is not installed")
	}
	task, err := readTaskGenerationRecord(taskGenerationRecordPath(paths, input.TaskGenerationID))
	if err != nil || !task.Verified || task.Input.SystemdUnit != input.TaskUnit {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: "target Task generation is not verified"}, errors.New("target Task generation is not verified")
	}
	scheduleDir := filepath.Dir(installedSchedulePath(record.Environment, input.Component))
	if err := ensureDirectory(filepath.Dir(scheduleDir), 0755, 0, 0); err != nil && !strings.Contains(err.Error(), "unsafe") {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: err.Error()}, err
	}
	if err := ensureDirectory(scheduleDir, 0755, 0, 0); err != nil {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: err.Error()}, err
	}
	ledgerPath := filepath.Join(scheduleDir, "ledger.db")
	installed := installedScheduleRecord{
		SchemaVersion: scheduleRecordSchema, Environment: record.Environment, Component: input.Component,
		Task: input.Task, TaskGenerationID: input.TaskGenerationID, TaskUnit: input.TaskUnit,
		ApplicationRevision: input.ApplicationRevision, ConfigurationDigest: input.ConfigurationDigest,
		TimerUnit: input.TimerUnit, Expression: input.Expression, Timezone: input.Timezone,
		DaylightSaving: input.DaylightSaving, Overlap: input.Overlap, Retry: input.Retry, MissedRun: input.MissedRun, Failure: input.Failure,
		AppletDigest: input.AppletDigest, LedgerSchema: input.LedgerSchema, LedgerPath: ledgerPath,
		TaskEvidencePath: taskEvidencePath(paths), WorkerEvidencePath: workerEvidencePath(paths), FencingToken: fencingToken,
	}
	if err := writeJSONAtomic(installedSchedulePath(record.Environment, input.Component), installed, 0444); err != nil {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: err.Error()}, err
	}
	serviceName := strings.TrimSuffix(input.TimerUnit, ".timer") + ".service"
	if err := installExactFile(filepath.Join(paths.systemdUnits, serviceName), []byte(renderScheduleService(input, record)), 0644, 0, 0); err != nil {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: err.Error()}, err
	}
	if err := installExactFile(filepath.Join(paths.systemdUnits, input.TimerUnit), []byte(renderScheduleTimer(input, record)), 0644, 0, 0); err != nil {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: err.Error()}, err
	}
	if _, err := paths.systemd.Run(ctx, "daemon-reload"); err != nil {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: "reload Schedule units"}, errors.New("reload Schedule units")
	}
	if _, err := paths.systemd.Run(ctx, "enable", "--now", input.TimerUnit); err != nil {
		return host.AsyncScheduleOperationObservation{Status: "failed", Reason: "enable stable Schedule timer"}, errors.New("enable stable Schedule timer")
	}
	observed := observeInstalledSchedule(ctx, input, record, paths, false)
	if observed.Status != "active" {
		return observed, errors.New("stable Schedule timer failed exact verification")
	}
	return observed, nil
}

func verifyInitialSchedule(ctx context.Context, input planner.AsyncScheduleInput, record bootstrapRecord, paths executionPaths) (host.AsyncScheduleOperationObservation, error) {
	deadline := time.Now().Add(75 * time.Second)
	for {
		observed := observeInstalledSchedule(ctx, input, record, paths, true)
		if observed.Status == "verified" {
			return observed, nil
		}
		if observed.Status == "unknown" || observed.Status == "failed" || time.Now().After(deadline) || ctx.Err() != nil {
			if observed.Reason == "" {
				observed.Reason = "no complete Schedule-to-Task-to-Queue-to-Worker occurrence was observed within the bound"
			}
			observed.Status = "failed"
			return observed, errors.New(observed.Reason)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func observeInstalledSchedule(ctx context.Context, input planner.AsyncScheduleInput, record bootstrapRecord, paths executionPaths, complete bool) host.AsyncScheduleOperationObservation {
	result := host.AsyncScheduleOperationObservation{Status: "absent", Schedule: scheduleStatus(input)}
	installed, err := readInstalledSchedule(record.Environment, input.Component)
	if errors.Is(err, os.ErrNotExist) {
		return result
	}
	if err != nil || !scheduleRecordMatchesInput(installed, input, record.Environment) {
		result.Status, result.Reason = "unknown", "installed Schedule record differs from the approved Plan"
		return result
	}
	active, _ := paths.systemd.Run(ctx, "is-active", input.TimerUnit)
	if strings.TrimSpace(string(active)) != "active" || !exactRootFile(filepath.Join(paths.systemdUnits, input.TimerUnit), []byte(renderScheduleTimer(input, record)), 0644) || !exactRootFile(filepath.Join(paths.systemdUnits, strings.TrimSuffix(input.TimerUnit, ".timer")+".service"), []byte(renderScheduleService(input, record)), 0644) {
		result.Status, result.Reason = "unknown", "stable Schedule units are inactive or differ from the approved Plan"
		return result
	}
	result.Status = "active"
	result.Schedule = scheduleStatus(input)
	result.Schedule.FencingToken = installed.FencingToken
	result.Schedule.Active = true
	result.Schedule.LedgerDigest = regularFileDigest(installed.LedgerPath)
	if !complete {
		return result
	}
	store, err := scheduler.OpenReadOnly(installed.LedgerPath)
	if err != nil {
		return result
	}
	defer store.Close()
	occurrence, invocation, err := store.Latest(ctx, input.Component)
	if err != nil || occurrence == nil || invocation == nil {
		return result
	}
	result.Occurrence, result.Invocation = occurrence, invocation
	if occurrence.TaskGenerationID != input.TaskGenerationID || occurrence.FencingToken != installed.FencingToken || invocation.TaskGenerationID != input.TaskGenerationID || invocation.ApplicationRevision != input.ApplicationRevision || invocation.ConfigurationDigest != input.ConfigurationDigest || invocation.Outcome != "succeeded" || len(invocation.Attempts) != 1 || invocation.Attempts[0].Outcome != "succeeded" {
		return result
	}
	messageID, confirmed := taskConfirmedMessage(installed.TaskEvidencePath, invocation.ID)
	worker, workerErr := readWorkerGenerationRecord(filepath.Join(paths.environmentHome, "workers", "active.json"))
	if workerErr != nil {
		return result
	}
	processed, acknowledged := workerAcknowledgedMessage(installed.WorkerEvidencePath, messageID, worker.Input.Revision, worker.Input.ArtifactDigest)
	if !confirmed || !processed || !acknowledged {
		return result
	}
	result.Status, result.MessageID, result.Acknowledged = "verified", messageID, true
	result.Schedule.LedgerDigest = regularFileDigest(installed.LedgerPath)
	return result
}

func scheduleRecordMatchesInput(record installedScheduleRecord, input planner.AsyncScheduleInput, environment string) bool {
	return record.SchemaVersion == scheduleRecordSchema && record.Environment == environment && record.Component == input.Component && record.Task == input.Task && record.TaskGenerationID == input.TaskGenerationID && record.TaskUnit == input.TaskUnit && record.ApplicationRevision == input.ApplicationRevision && record.ConfigurationDigest == input.ConfigurationDigest && record.TimerUnit == input.TimerUnit && record.Expression == input.Expression && record.Timezone == input.Timezone && record.DaylightSaving == input.DaylightSaving && record.Overlap == input.Overlap && record.Retry == input.Retry && record.MissedRun == input.MissedRun && record.Failure == input.Failure && record.AppletDigest == input.AppletDigest && record.LedgerSchema == input.LedgerSchema && record.FencingToken > 0
}

func inspectAsyncDeployment(ctx context.Context, environment, account string, paths executionPaths) (host.AsyncDeploymentStatus, []string) {
	deployment := host.AsyncDeploymentStatus{}
	findings := []string{}
	record := bootstrapRecord{Environment: environment, Account: account}
	taskActivePath := filepath.Join(paths.environmentHome, "tasks", "active.json")
	if task, err := readTaskGenerationRecord(taskActivePath); err == nil {
		observed := observeTaskGeneration(ctx, task.Input, record, paths)
		if observed.Status != "installed" || !observed.Verified {
			findings = append(findings, "active Task generation is not an exact verified observation")
		} else {
			status := observed.Task
			deployment.ActiveTask = &status
		}
	} else if activeRecordShouldExist(taskActivePath, filepath.Join(paths.environmentHome, "tasks")) {
		findings = append(findings, "active Task generation record is unreadable")
	}
	workerActivePath := filepath.Join(paths.environmentHome, "workers", "active.json")
	activeWorkerID := ""
	if worker, err := readWorkerGenerationRecord(workerActivePath); err == nil {
		observed := observeWorkerGeneration(ctx, worker.Input, record, paths)
		if observed.Status != "active-open" || !observed.Verified {
			findings = append(findings, "active Worker generation is not an exact open observation")
		} else {
			status := observed.Worker
			deployment.ActiveWorker = &status
			activeWorkerID = status.ID
		}
	} else if activeRecordShouldExist(workerActivePath, filepath.Join(paths.environmentHome, "workers")) {
		findings = append(findings, "installed Worker generation has no readable active Worker record")
	}
	workerEntries, workerEntriesErr := os.ReadDir(filepath.Join(paths.environmentHome, "workers"))
	if workerEntriesErr == nil {
		for _, entry := range workerEntries {
			if entry.IsDir() || entry.Name() == "active.json" || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			candidate, readErr := readWorkerGenerationRecord(filepath.Join(paths.environmentHome, "workers", entry.Name()))
			if readErr != nil || candidate.Input.GenerationID == activeWorkerID {
				continue
			}
			if deployment.Candidate != nil {
				findings = append(findings, "more than one non-active Worker generation requires an explicit retention decision")
				continue
			}
			observed := observeWorkerGeneration(ctx, candidate.Input, record, paths)
			status := observed.Worker
			if observed.Reason != "" {
				status.Reason = observed.Reason
			}
			if status.Health == "" {
				status.Health = "candidate-unverified"
				status.RecoveryAction = "keep intake closed; inspect the candidate and create a new approved Plan before any Worker handoff"
			}
			deployment.Candidate = &status
		}
	} else if !errors.Is(workerEntriesErr, os.ErrNotExist) {
		findings = append(findings, "Worker generation inventory cannot be observed")
	}
	schedulesRoot := filepath.Join(paths.environmentHome, "schedules")
	entries, err := os.ReadDir(schedulesRoot)
	if err == nil {
		if len(entries) > 1 {
			findings = append(findings, "asynchronous tracer observed more than one Schedule")
		} else if len(entries) == 1 && entries[0].IsDir() {
			component := entries[0].Name()
			installed, readErr := readInstalledSchedule(environment, component)
			if readErr != nil {
				findings = append(findings, "installed Schedule record is unreadable")
			} else {
				active, _ := paths.systemd.Run(ctx, "is-active", installed.TimerUnit)
				status := host.ScheduleStatus{Component: installed.Component, TimerUnit: installed.TimerUnit, TaskGenerationID: installed.TaskGenerationID, AppletDigest: installed.AppletDigest, LedgerSchema: installed.LedgerSchema, LedgerDigest: regularFileDigest(installed.LedgerPath), FencingToken: installed.FencingToken, Timezone: installed.Timezone, Expression: installed.Expression, DaylightSaving: installed.DaylightSaving, Overlap: installed.Overlap, Retry: installed.Retry, MissedRun: installed.MissedRun, Failure: installed.Failure, Active: strings.TrimSpace(string(active)) == "active"}
				deployment.Schedule = &status
				if store, openErr := scheduler.OpenReadOnly(installed.LedgerPath); openErr == nil {
					occurrence, invocation, latestErr := store.Latest(ctx, component)
					_ = store.Close()
					if latestErr == nil && occurrence != nil && invocation != nil {
						deployment.Occurrences = []host.ScheduleOccurrenceStatus{*occurrence}
						deployment.Invocations = []host.TaskInvocationStatus{*invocation}
					}
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		findings = append(findings, "Schedule inventory cannot be observed")
	}
	messages, messageErr := inspectQueueMessages(paths)
	if messageErr != nil {
		findings = append(findings, "Queue message evidence cannot be observed: "+messageErr.Error())
	} else {
		deployment.Messages = messages
	}
	return deployment, findings
}

func inspectQueueMessages(paths executionPaths) ([]host.QueueMessageStatus, error) {
	messages := []host.QueueMessageStatus{}
	indexes := map[string]int{}
	taskFile, err := os.Open(taskEvidencePath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return messages, nil
	}
	if err != nil {
		return nil, errors.New("read Task confirmation evidence")
	}
	taskScanner := bufio.NewScanner(taskFile)
	for taskScanner.Scan() {
		var event struct {
			Event, MessageID, ApplicationRevision, TaskArtifactDigest, InvocationID string
		}
		if json.Unmarshal(taskScanner.Bytes(), &event) != nil {
			_ = taskFile.Close()
			return nil, errors.New("Task confirmation evidence is invalid")
		}
		if event.Event != "message_confirmed" {
			continue
		}
		if event.MessageID == "" || event.ApplicationRevision == "" || !digestPattern.MatchString(event.TaskArtifactDigest) || event.InvocationID == "" {
			_ = taskFile.Close()
			return nil, errors.New("Task confirmation evidence is incomplete")
		}
		if _, duplicate := indexes[event.MessageID]; duplicate {
			continue
		}
		indexes[event.MessageID] = len(messages)
		messages = append(messages, host.QueueMessageStatus{ID: event.MessageID, ProducerApplicationRevision: event.ApplicationRevision, TaskArtifactDigest: event.TaskArtifactDigest, TaskInvocationID: event.InvocationID, Disposition: "accepted-unsettled"})
	}
	if scanErr := taskScanner.Err(); scanErr != nil {
		_ = taskFile.Close()
		return nil, errors.New("scan Task confirmation evidence")
	}
	if closeErr := taskFile.Close(); closeErr != nil {
		return nil, errors.New("close Task confirmation evidence")
	}

	workerFile, err := os.Open(workerEvidencePath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return messages, nil
	}
	if err != nil {
		return nil, errors.New("read Worker settlement evidence")
	}
	defer workerFile.Close()
	workerScanner := bufio.NewScanner(workerFile)
	for workerScanner.Scan() {
		var event struct {
			Event, MessageID, WorkerApplicationRevision, WorkerArtifactDigest string
		}
		if json.Unmarshal(workerScanner.Bytes(), &event) != nil {
			return nil, errors.New("Worker settlement evidence is invalid")
		}
		index, accepted := indexes[event.MessageID]
		if !accepted {
			continue
		}
		if event.WorkerApplicationRevision == "" || !digestPattern.MatchString(event.WorkerArtifactDigest) {
			return nil, errors.New("Worker message evidence is incomplete")
		}
		workerEvent := host.QueueMessageWorkerEvent{Event: event.Event, WorkerApplicationRevision: event.WorkerApplicationRevision, WorkerArtifactDigest: event.WorkerArtifactDigest}
		messages[index].WorkerEvents = append(messages[index].WorkerEvents, workerEvent)
		switch event.Event {
		case "acknowledged", "requeued", "rejected":
		default:
			continue
		}
		messages[index].Disposition = event.Event
		messages[index].WorkerApplicationRevision = event.WorkerApplicationRevision
		messages[index].WorkerArtifactDigest = event.WorkerArtifactDigest
	}
	if workerScanner.Err() != nil {
		return nil, errors.New("scan Worker settlement evidence")
	}
	return messages, nil
}

func activeRecordShouldExist(activePath, recordsDirectory string) bool {
	if _, err := os.Lstat(activePath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return true
	}
	entries, err := os.ReadDir(recordsDirectory)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != "active.json" && strings.HasSuffix(entry.Name(), ".json") {
			return true
		}
	}
	return false
}

func renderTaskUnit(input planner.AsyncTaskInput, record bootstrapRecord, paths executionPaths, executable string) string {
	return fmt.Sprintf(`[Unit]
Description=Provision Task generation %s invocation %%i
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=%s
Group=%s
WorkingDirectory=%s
LoadCredentialEncrypted=rabbitmq-url:%s
ExecStart=%s/%s --broker-url-file ${CREDENTIALS_DIRECTORY}/rabbitmq-url --queue %s --application-revision %s --artifact-digest %s --invocation %%i --count 1 --behavior process
StandardOutput=append:%s
StandardError=journal
TimeoutStartSec=%s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s
`, input.GenerationID, record.Account, record.Account, filepath.Join(paths.environmentHome, "releases", input.GenerationID), queueURLCredentialPath(record.Environment), filepath.Join(paths.environmentHome, "releases", input.GenerationID), executable, input.QueueLogicalID, input.Revision, input.ArtifactDigest, taskEvidencePath(paths), input.Timeout, filepath.Join(paths.environmentHome, "async"))
}

func renderWorkerUnit(input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths, executable string) string {
	return fmt.Sprintf(`[Unit]
Description=Provision Worker generation %s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
LoadCredentialEncrypted=rabbitmq-url:%s
ExecStart=%s/%s --broker-url-file ${CREDENTIALS_DIRECTORY}/rabbitmq-url --queue %s --application-revision %s --artifact-digest %s --gate-file %s --state-file %s --evidence-file %s --ledger-dir %s --hold-dir %s
Restart=always
RestartSec=1s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s

[Install]
WantedBy=multi-user.target
`, input.GenerationID, record.Account, record.Account, filepath.Join(paths.environmentHome, "releases", input.GenerationID), queueURLCredentialPath(record.Environment), filepath.Join(paths.environmentHome, "releases", input.GenerationID), executable, input.QueueLogicalID, input.Revision, input.ArtifactDigest, workerGatePath(paths, input.GenerationID), workerStatePath(paths, input.GenerationID), workerEvidencePath(paths), workerLedgerPath(paths), workerHoldPath(paths), filepath.Join(paths.environmentHome, "async"))
}

func renderScheduleService(input planner.AsyncScheduleInput, record bootstrapRecord) string {
	return fmt.Sprintf(`[Unit]
Description=Provision Schedule %s runtime
After=network-online.target

[Service]
Type=oneshot
ExecStart=%s run --environment %s --schedule %s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/provision/environments/%s/schedules
`, input.Component, scheduleRuntimePath(systemExecutionPaths(record.Environment)), record.Environment, input.Component, record.Environment)
}

func renderScheduleTimer(input planner.AsyncScheduleInput, record bootstrapRecord) string {
	return fmt.Sprintf(`[Unit]
Description=Provision stable Schedule %s

[Timer]
OnCalendar=*-*-* *:*:00 %s
Persistent=false
AccuracySec=1s
Unit=%s

[Install]
WantedBy=timers.target
`, input.Component, input.Timezone, strings.TrimSuffix(input.TimerUnit, ".timer")+".service")
}

func ensureAsyncDataRoots(record bootstrapRecord, paths executionPaths, uid, gid int) error {
	root := filepath.Join(paths.environmentHome, "async")
	for _, path := range []string{root, filepath.Join(root, "worker"), filepath.Join(root, "worker", "ledger"), filepath.Join(root, "worker", "holds"), filepath.Join(root, "task")} {
		if err := ensureDirectory(path, 0750, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func taskStatus(input planner.AsyncTaskInput, record bootstrapRecord, paths executionPaths) host.TaskGenerationStatus {
	return host.TaskGenerationStatus{ID: input.GenerationID, Revision: input.Revision, ArtifactDigest: input.ArtifactDigest, SystemdUnit: input.SystemdUnit, Queue: input.QueueLogicalID, ConfigurationDigest: input.ConfigurationDigest, Timeout: input.Timeout, EvidencePath: taskEvidencePath(paths)}
}

func workerStatus(input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) host.WorkerGenerationStatus {
	return host.WorkerGenerationStatus{ID: input.GenerationID, Revision: input.Revision, ArtifactDigest: input.ArtifactDigest, SystemdUnit: input.SystemdUnit, Gate: "closed", StatePath: workerStatePath(paths, input.GenerationID), GatePath: workerGatePath(paths, input.GenerationID), EvidencePath: workerEvidencePath(paths)}
}

func scheduleStatus(input planner.AsyncScheduleInput) host.ScheduleStatus {
	return host.ScheduleStatus{Component: input.Component, TimerUnit: input.TimerUnit, TaskGenerationID: input.TaskGenerationID, AppletDigest: input.AppletDigest, LedgerSchema: input.LedgerSchema, Timezone: input.Timezone, Expression: input.Expression, DaylightSaving: input.DaylightSaving, Overlap: input.Overlap, Retry: input.Retry, MissedRun: input.MissedRun, Failure: input.Failure}
}

func taskGenerationRecordPath(paths executionPaths, generation string) string {
	return filepath.Join(paths.environmentHome, "tasks", generation+".json")
}
func workerGenerationRecordPath(paths executionPaths, generation string) string {
	return filepath.Join(paths.environmentHome, "workers", generation+".json")
}
func workerGatePath(paths executionPaths, generation string) string {
	return filepath.Join(paths.environmentHome, "async", "worker", generation+".gate")
}
func workerStatePath(paths executionPaths, generation string) string {
	return filepath.Join(paths.environmentHome, "async", "worker", generation+".state.json")
}
func workerEvidencePath(paths executionPaths) string {
	return filepath.Join(paths.environmentHome, "async", "worker", "evidence.jsonl")
}
func workerLedgerPath(paths executionPaths) string {
	return filepath.Join(paths.environmentHome, "async", "worker", "ledger")
}
func workerHoldPath(paths executionPaths) string {
	return filepath.Join(paths.environmentHome, "async", "worker", "holds")
}
func taskEvidencePath(paths executionPaths) string {
	return filepath.Join(paths.environmentHome, "async", "task", "evidence.jsonl")
}
func queueURLCredentialPath(environment string) string {
	return filepath.Join("/var/lib/provision/environments", environment, "credentials", "rabbitmq-url")
}

func readTaskGenerationRecord(path string) (installedTaskGeneration, error) {
	var record installedTaskGeneration
	err := readExactJSON(path, &record)
	return record, err
}
func readWorkerGenerationRecord(path string) (installedWorkerGeneration, error) {
	var record installedWorkerGeneration
	err := readExactJSON(path, &record)
	return record, err
}
func readWorkerState(path string) (exampleWorkerState, error) {
	var state exampleWorkerState
	err := readExactJSONUntrusted(path, &state)
	return state, err
}

func readExactJSON(path string, out any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0444 || !ownedByExecutor(info) {
		return errors.New("record is missing or unsafe")
	}
	return readJSONFile(path, out)
}
func readExactJSONUntrusted(path string, out any) error { return readJSONFile(path, out) }
func readJSONFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return errors.New("read structured runtime state")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("structured runtime state has trailing data")
	}
	return nil
}

func exactRootFile(path string, wanted []byte, mode os.FileMode) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode || !ownedByExecutor(info) {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && bytes.Equal(data, wanted)
}

func replaceGateFile(path, content string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0644 || !ownedByExecutor(info) {
		return errors.New("Worker admission gate is missing or unsafe")
	}
	existing, err := os.ReadFile(path)
	if err != nil || string(existing) != "closed\n" && string(existing) != "open\n" {
		return errors.New("Worker admission gate contains unsupported state")
	}
	if string(existing) == content {
		return nil
	}
	temporary := path + ".tmp"
	_ = os.Remove(temporary)
	if err := os.WriteFile(temporary, []byte(content), 0644); err != nil {
		return errors.New("write Worker admission gate")
	}
	if err := os.Chown(temporary, 0, 0); err != nil {
		_ = os.Remove(temporary)
		return errors.New("own Worker admission gate")
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return errors.New("commit Worker admission gate")
	}
	return nil
}

func taskConfirmedMessage(path, invocation string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct{ Event, MessageID, InvocationID string }
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Event == "message_confirmed" && event.InvocationID == invocation && event.MessageID != "" {
			return event.MessageID, true
		}
	}
	return "", false
}

func workerAcknowledgedMessage(path, messageID, workerRevision, workerArtifactDigest string) (bool, bool) {
	if messageID == "" {
		return false, false
	}
	file, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer file.Close()
	processed, acknowledged := false, false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct{ Event, MessageID, WorkerApplicationRevision, WorkerArtifactDigest string }
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.MessageID != messageID || event.WorkerApplicationRevision != workerRevision || event.WorkerArtifactDigest != workerArtifactDigest {
			continue
		}
		processed = processed || event.Event == "processed"
		acknowledged = acknowledged || event.Event == "acknowledged"
	}
	return processed, acknowledged
}

func scheduleRuntimePath(paths executionPaths) string {
	return filepath.Join(paths.environmentHome, "runtime", "provision-runtime-schedule")
}

func execCommandContext(ctx context.Context, name string, args ...string) ([]byte, error) {
	return commandRunner(ctx, name, args...)
}

var commandRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
