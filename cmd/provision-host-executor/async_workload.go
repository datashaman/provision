package main

import (
	"bufio"
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
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
	"provision/internal/rollbackwindow"
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
		planner.FenceWorkerIntake, planner.DrainWorkerPrevious, planner.RetainWorkerPrevious,
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

type retainedWorkerGeneration struct {
	SchemaVersion        string                `json:"schemaVersion"`
	PlanID               string                `json:"planId"`
	OperationDigest      string                `json:"operationDigest"`
	DrainOperationDigest string                `json:"drainOperationDigest"`
	CandidateID          string                `json:"candidateId"`
	PreviousID           string                `json:"previousId"`
	RollbackWindow       rollbackwindow.Window `json:"rollbackWindow"`
	RetainedAt           time.Time             `json:"retainedAt"`
	RetainUntil          time.Time             `json:"retainUntil"`
}

type drainedWorkerGeneration struct {
	SchemaVersion      string     `json:"schemaVersion"`
	PlanID             string     `json:"planId"`
	OperationDigest    string     `json:"operationDigest"`
	CandidateID        string     `json:"candidateId"`
	PreviousID         string     `json:"previousId"`
	QueueGenerationID  string     `json:"queueGenerationId"`
	StartedAt          time.Time  `json:"startedAt"`
	CompletionDeadline time.Time  `json:"completionDeadline"`
	Deadline           time.Time  `json:"deadline"`
	ReleaseStartedAt   *time.Time `json:"releaseStartedAt,omitempty"`
	InFlightMessageID  string     `json:"inFlightMessageId,omitempty"`
	ReleasedMessageID  string     `json:"releasedMessageId,omitempty"`
	BoundElapsed       bool       `json:"boundElapsed"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
}

type workerActiveVerificationRecord struct {
	SchemaVersion           string     `json:"schemaVersion"`
	PlanID                  string     `json:"planId"`
	OperationDigest         string     `json:"operationDigest"`
	QueueGenerationID       string     `json:"queueGenerationId"`
	CandidateID             string     `json:"candidateId"`
	PreviousID              string     `json:"previousId"`
	MessageID               string     `json:"messageId"`
	PublishStartedAt        time.Time  `json:"publishStartedAt"`
	PublisherConfirmedAt    *time.Time `json:"publisherConfirmedAt,omitempty"`
	CandidateAcknowledgedAt *time.Time `json:"candidateAcknowledgedAt,omitempty"`
	RollbackAttemptedAt     *time.Time `json:"rollbackAttemptedAt,omitempty"`
	CandidateFencedAt       *time.Time `json:"candidateFencedAt,omitempty"`
	PreviousStartedAt       *time.Time `json:"previousStartedAt,omitempty"`
	RollbackAcknowledgedAt  *time.Time `json:"rollbackAcknowledgedAt,omitempty"`
	CompletedAt             *time.Time `json:"completedAt,omitempty"`
	Outcome                 string     `json:"outcome,omitempty"`
	FailureReason           string     `json:"failureReason,omitempty"`
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
		if input.Task == nil || input.Queue != nil || input.Artifact != nil || input.Worker != nil || input.WorkerHandoff != nil || input.Schedule != nil || input.Runtime != nil {
			return errors.New("Task operation requires only its typed Task input")
		}
		return validateAsyncTaskInput(*input.Task, record, paths)
	case planner.InstallWorkerGeneration, planner.StartWorkerCandidate, planner.VerifyWorkerCandidate:
		if input.Worker == nil || input.Queue != nil || input.Artifact != nil || input.Task != nil || input.WorkerHandoff != nil || input.Schedule != nil || input.Runtime != nil {
			return errors.New("Worker operation requires only its typed Worker input")
		}
		if err := validateAsyncWorkerInput(*input.Worker, record, paths); err != nil {
			return err
		}
		return validateWorkerTransitionState(planned.Kind, *input.Worker, paths)
	case planner.ActivateWorkerIntake, planner.VerifyWorkerActive:
		if input.Queue != nil || input.Artifact != nil || input.Task != nil || input.Schedule != nil || input.Runtime != nil || (input.Worker == nil) == (input.WorkerHandoff == nil) {
			return errors.New("Worker activation operation requires exactly one typed Worker or Worker handoff input")
		}
		if input.WorkerHandoff != nil {
			if err := validateAsyncWorkerHandoffInput(*input.WorkerHandoff, record, paths); err != nil {
				return err
			}
			if !digestPattern.MatchString(input.WorkerHandoff.DrainOperationDigest) {
				return errors.New("Worker activation requires the exact approved drain operation digest")
			}
			return validateWorkerTransitionState(planned.Kind, input.WorkerHandoff.Worker, paths)
		}
		if err := validateAsyncWorkerInput(*input.Worker, record, paths); err != nil {
			return err
		}
		return validateWorkerTransitionState(planned.Kind, *input.Worker, paths)
	case planner.FenceWorkerIntake, planner.DrainWorkerPrevious, planner.RetainWorkerPrevious:
		if input.WorkerHandoff == nil || input.Queue != nil || input.Artifact != nil || input.Task != nil || input.Worker != nil || input.Schedule != nil || input.Runtime != nil {
			return errors.New("Worker handoff operation requires only its typed Worker handoff input")
		}
		if err := validateAsyncWorkerHandoffInput(*input.WorkerHandoff, record, paths); err != nil {
			return err
		}
		if planned.Kind == planner.RetainWorkerPrevious {
			if !digestPattern.MatchString(input.WorkerHandoff.DrainOperationDigest) {
				return errors.New("Worker retention requires the exact approved drain operation digest")
			}
		} else if input.WorkerHandoff.DrainOperationDigest != "" {
			return errors.New("Worker fence and drain inputs cannot claim a prior drain operation")
		}
		return validateWorkerTransitionState(planned.Kind, input.WorkerHandoff.Worker, paths)
	case planner.InstallScheduleRuntime:
		if input.Runtime == nil || input.Queue != nil || input.Artifact != nil || input.Task != nil || input.Worker != nil || input.WorkerHandoff != nil || input.Schedule != nil {
			return errors.New("Schedule runtime operation requires only its typed runtime input")
		}
		if input.Runtime.LedgerSchema != scheduler.SchemaVersion || !digestPattern.MatchString(input.Runtime.AppletDigest) {
			return errors.New("Schedule runtime identity is unsupported")
		}
		return nil
	case planner.HandoffSchedule, planner.VerifySchedule:
		if input.Schedule == nil || input.Queue != nil || input.Artifact != nil || input.Task != nil || input.Worker != nil || input.WorkerHandoff != nil || input.Runtime != nil {
			return errors.New("Schedule operation requires only its typed Schedule input")
		}
		return validateAsyncScheduleInput(*input.Schedule, record)
	default:
		return errors.New("host executor does not allow this asynchronous operation kind")
	}
}

func validateAsyncWorkerHandoffInput(input planner.AsyncWorkerHandoffInput, record bootstrapRecord, paths executionPaths) error {
	if input.Worker.Previous == nil || !deploymentIdentifier.MatchString(input.QueueGenerationID) {
		return errors.New("Worker handoff requires exact previous Worker and Queue generation identities")
	}
	if _, err := input.RollbackWindow.Duration(); err != nil {
		return errors.New("Worker handoff rollback window is unsupported")
	}
	return validateAsyncWorkerInput(input.Worker, record, paths)
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
	installed, err := readWorkerGenerationRecord(workerGenerationRecordPath(paths, previous.ID))
	if err != nil || !installed.Verified || !installed.Active || installed.Input.GenerationID != previous.ID || installed.Input.Revision != previous.Revision || installed.Input.ArtifactDigest != previous.ArtifactDigest || installed.Input.SystemdUnit != previous.SystemdUnit || installed.Input.QueueLogicalID != input.QueueLogicalID {
		return errors.New("planned previous Worker does not match its immutable generation record")
	}
	return nil
}

func validateWorkerTransitionState(kind planner.OperationKind, input planner.AsyncWorkerInput, paths executionPaths) error {
	if input.Previous == nil {
		return nil
	}
	active, err := readWorkerGenerationRecord(filepath.Join(paths.environmentHome, "workers", "active.json"))
	if err != nil || !active.Active {
		return errors.New("durable active Worker record is unreadable or inactive")
	}
	previousActive := workerRecordMatchesStatus(active, *input.Previous)
	candidateActive := workerRecordMatchesInput(active, input)
	switch kind {
	case planner.ActivateWorkerIntake, planner.VerifyWorkerActive:
		if !previousActive && !candidateActive {
			return errors.New("durable active Worker record matches neither generation in the approved handoff")
		}
	case planner.RetainWorkerPrevious:
		if !candidateActive {
			return errors.New("durable active Worker record does not identify the approved candidate")
		}
	default:
		if !previousActive {
			return errors.New("durable active Worker record does not identify the planned previous generation")
		}
	}
	return nil
}

func workerRecordMatchesInput(record installedWorkerGeneration, input planner.AsyncWorkerInput) bool {
	return record.Input.GenerationID == input.GenerationID && record.Input.Revision == input.Revision && record.Input.ArtifactDigest == input.ArtifactDigest && record.Input.SystemdUnit == input.SystemdUnit && record.Input.QueueLogicalID == input.QueueLogicalID
}

func workerRecordMatchesStatus(record installedWorkerGeneration, status host.WorkerGenerationStatus) bool {
	return record.Input.GenerationID == status.ID && record.Input.Revision == status.Revision && record.Input.ArtifactDigest == status.ArtifactDigest && record.Input.SystemdUnit == status.SystemdUnit
}

func plannedWorkerInput(planned planner.Operation) planner.AsyncWorkerInput {
	if planned.Input.Async.WorkerHandoff != nil {
		return planned.Input.Async.WorkerHandoff.Worker
	}
	return *planned.Input.Async.Worker
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

func observeAsyncWorkloadOperation(ctx context.Context, planID string, planned planner.Operation, record bootstrapRecord, paths executionPaths) (host.OperationObservation, error) {
	if err := validateAsyncWorkloadOperation(planned, record, paths); err != nil {
		return host.OperationObservation{}, err
	}
	var evidence any
	state := "pending"
	outcome := ""
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
		if observed.Status == "installed" || observed.Status == "active-gated" || observed.Status == "active-open" {
			state = "satisfied"
		} else {
			state = asyncObservationState(observed.Status, "installed")
		}
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
	case planner.FenceWorkerIntake, planner.DrainWorkerPrevious, planner.RetainWorkerPrevious:
		observed := observeWorkerHandoff(ctx, planID, planned, record, paths)
		evidence = observed
		if workerHandoffSatisfied(planned.Kind, observed) {
			state = "satisfied"
		} else if observed.Status == "unknown" || observed.Status == "failed" {
			state = "unknown"
		}
	case planner.VerifyWorkerActive:
		if planned.Input.Async.WorkerHandoff != nil {
			observed := observeWorkerActiveVerification(ctx, planID, planned, record, paths)
			evidence = observed
			if observed.Status == host.WorkerActiveVerificationHealthy {
				state = "satisfied"
			} else if observed.Status == host.WorkerActiveVerificationRolledBack {
				state = "satisfied"
				outcome = string(operation.OutcomeFailed)
			} else if observed.Status == host.WorkerActiveVerificationUncertain {
				state = "unknown"
			} else if observed.Status == host.WorkerActiveVerificationPending {
				state = "pending"
			}
			break
		}
		fallthrough
	case planner.ActivateWorkerIntake:
		observed := observeWorkerGeneration(ctx, plannedWorkerInput(planned), record, paths)
		if planned.Input.Async.WorkerHandoff != nil {
			observed.QueueGenerationID = planned.Input.Async.WorkerHandoff.QueueGenerationID
		}
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
	return host.OperationObservation{State: state, Outcome: outcome, Evidence: encoded}, nil
}

func workerHandoffSatisfied(kind planner.OperationKind, observed host.AsyncWorkerHandoffObservation) bool {
	switch kind {
	case planner.FenceWorkerIntake:
		return observed.Status == "previous-fenced" && observed.Previous.Gate == "closed" && observed.Candidate.Gate == "closed"
	case planner.DrainWorkerPrevious:
		return (observed.Status == "previous-drained" || observed.Status == "previous-released") && !observed.Previous.UnitActive && observed.Previous.InFlight == 0
	case planner.RetainWorkerPrevious:
		return observed.Status == "previous-retained" && observed.Previous.Restartable && observed.RetainUntil != nil
	default:
		return false
	}
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
	case planner.FenceWorkerIntake:
		observed, actionErr = fencePreviousWorkerIntake(ctx, planned, claim, record, paths)
	case planner.DrainWorkerPrevious:
		observed, actionErr = drainPreviousWorker(ctx, planned, claim, record, paths)
	case planner.ActivateWorkerIntake:
		if planned.Input.Async.WorkerHandoff != nil {
			observed, actionErr = activateWorkerHandoff(ctx, *planned.Input.Async.WorkerHandoff, claim.PlanID, record, paths)
		} else {
			observed, actionErr = activateWorkerIntake(ctx, *planned.Input.Async.Worker, record, paths)
		}
	case planner.VerifyWorkerActive:
		if planned.Input.Async.WorkerHandoff != nil {
			observed, actionErr = verifyWorkerHandoffActive(ctx, planned, claim, record, paths)
		} else {
			observed, actionErr = verifyWorkerCandidate(ctx, *planned.Input.Async.Worker, record, paths, false)
		}
	case planner.RetainWorkerPrevious:
		observed, actionErr = retainPreviousWorker(ctx, planned, claim, record, paths)
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
	if observed := observeWorkerGeneration(ctx, input, record, paths); observed.Status == "installed" || observed.Status == "active-gated" || observed.Status == "active-open" {
		return observed, nil
	}
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
	if actionErr != nil || observed.Status != "installed" && observed.Status != "active-gated" {
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

func activateWorkerHandoff(ctx context.Context, input planner.AsyncWorkerHandoffInput, planID string, record bootstrapRecord, paths executionPaths) (host.AsyncWorkerOperationObservation, error) {
	if err := requirePreviousWorkerDrained(ctx, input, planID, record, paths); err != nil {
		observed := observeWorkerGeneration(ctx, input.Worker, record, paths)
		observed.Status, observed.Reason = "failed", err.Error()
		return observed, err
	}
	observed, err := activateWorkerIntake(ctx, input.Worker, record, paths)
	observed.QueueGenerationID = input.QueueGenerationID
	return observed, err
}

func verifyWorkerHandoffActive(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths) (host.AsyncWorkerActiveVerificationObservation, error) {
	input := *planned.Input.Async.WorkerHandoff
	observed := observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths)
	if observed.Status == host.WorkerActiveVerificationHealthy {
		return observed, nil
	}
	if observed.Status == host.WorkerActiveVerificationRolledBack {
		return observed, errors.New("active Worker verification failed and the retained previous Worker was restored")
	}
	verificationPath := workerActiveVerificationPath(paths, input.Worker.GenerationID)
	var verification workerActiveVerificationRecord
	if err := readExactJSON(verificationPath, &verification); err == nil {
		digest, _ := planner.OperationDigest(planned)
		if err := validateWorkerActiveVerificationRecord(verification, input, claim.PlanID, digest); err != nil {
			return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observed, err.Error())
		}
		if verification.PublisherConfirmedAt == nil {
			return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observed, "Worker verification publish started but publisher confirmation is not durably recorded; message disposition is ambiguous")
		}
		return continueWorkerActiveVerification(ctx, planned, claim, record, paths, verification)
	} else if _, statErr := os.Lstat(verificationPath); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
		return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observed, "durable Worker verification record is unreadable or unsafe")
	}
	if err := requirePreviousWorkerDrained(ctx, input, claim.PlanID, record, paths); err != nil {
		return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observed, err.Error())
	}
	queueInput, err := exactWorkerHandoffQueue(ctx, input, record, paths)
	if err != nil {
		return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observed, err.Error())
	}
	candidateInstalled, candidateRecordErr := readWorkerGenerationRecord(workerGenerationRecordPath(paths, input.Worker.GenerationID))
	candidateGate, candidateGateErr := os.ReadFile(workerGatePath(paths, input.Worker.GenerationID))
	if candidateRecordErr != nil || !candidateInstalled.Verified || !workerRecordMatchesInput(candidateInstalled, input.Worker) || candidateGateErr != nil || string(candidateGate) != "open\n" {
		return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observed, "candidate Worker is not the exact verified generation selected for active message verification")
	}
	digest, _ := planner.OperationDigest(planned)
	now := time.Now().UTC()
	verification = workerActiveVerificationRecord{
		SchemaVersion: "provision.dev/worker-active-verification/v1alpha1", PlanID: claim.PlanID, OperationDigest: digest,
		QueueGenerationID: input.QueueGenerationID, CandidateID: input.Worker.GenerationID, PreviousID: input.Worker.Previous.ID,
		MessageID: workerVerificationMessageID(claim.PlanID, digest), PublishStartedAt: now,
	}
	if err := writeJSONAtomic(verificationPath, verification, 0444); err != nil {
		return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observed, "record Worker verification intent before publishing: "+err.Error())
	}
	if err := publishWorkerVerificationMessage(ctx, queueInput, record.Environment, claim.PlanID, digest, verification.MessageID); err != nil {
		return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), "Worker verification message publish is ambiguous: "+err.Error())
	}
	confirmedAt := time.Now().UTC()
	verification.PublisherConfirmedAt = &confirmedAt
	if err := writeJSONAtomic(verificationPath, verification, 0444); err != nil {
		return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), "record publisher-confirmed Worker verification message: "+err.Error())
	}
	return continueWorkerActiveVerification(ctx, planned, claim, record, paths, verification)
}

func continueWorkerActiveVerification(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths, verification workerActiveVerificationRecord) (host.AsyncWorkerActiveVerificationObservation, error) {
	input := *planned.Input.Async.WorkerHandoff
	verificationPath := workerActiveVerificationPath(paths, input.Worker.GenerationID)
	failureReason := verification.FailureReason
	if verification.RollbackAttemptedAt == nil {
		failureReason = "candidate Worker did not process and acknowledge the publisher-confirmed verification message"
		deadline := verification.PublisherConfirmedAt.Add(paths.healthTimeout)
		for {
			processed, acknowledged := workerHandledMessage(workerEvidencePath(paths), verification.MessageID, input.Worker.Revision, input.Worker.ArtifactDigest)
			candidate := observeWorkerGeneration(ctx, input.Worker, record, paths)
			if processed && acknowledged {
				acknowledgedAt := time.Now().UTC()
				verification.CandidateAcknowledgedAt = &acknowledgedAt
				if err := commitActiveWorker(input.Worker, paths); err != nil {
					return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), "candidate processed the verification message but durable Worker authority could not be committed: "+err.Error())
				}
				completedAt := time.Now().UTC()
				verification.CompletedAt, verification.Outcome = &completedAt, "healthy"
				if err := writeJSONAtomic(verificationPath, verification, 0444); err != nil {
					return uncertainWorkerActiveVerificationWithCandidateFence(ctx, input.Worker, record, paths, observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), "candidate Worker became authoritative but verification completion could not be recorded: "+err.Error())
				}
				return observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), nil
			}
			if candidate.Status != "active-open" || !candidate.Worker.UnitActive || !candidate.Worker.QueueConnected || candidate.Worker.Gate != "open" {
				failureReason = "candidate Worker lost its exact active Queue-connected state before acknowledging the verification message"
				break
			}
			if !time.Now().Before(deadline) || ctx.Err() != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		rollbackAt := time.Now().UTC()
		verification.RollbackAttemptedAt = &rollbackAt
		verification.FailureReason = failureReason
		if err := writeJSONAtomic(verificationPath, verification, 0444); err != nil {
			return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; rollback intent could not be recorded")
		}
	}
	if err := fenceFailedWorkerCandidate(ctx, input.Worker, record, paths); err != nil {
		return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; candidate intake could not be proved fenced: "+err.Error())
	}
	if verification.CandidateFencedAt == nil {
		fencedAt := time.Now().UTC()
		verification.CandidateFencedAt = &fencedAt
		if err := writeJSONAtomic(verificationPath, verification, 0444); err != nil {
			return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; candidate fence completion could not be recorded")
		}
	}
	if processed, acknowledged := workerHandledMessage(workerEvidencePath(paths), verification.MessageID, input.Worker.Revision, input.Worker.ArtifactDigest); processed && acknowledged {
		return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; candidate acknowledgement raced with its intake fence")
	}
	if _, err := exactWorkerHandoffQueue(ctx, input, record, paths); err != nil {
		return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; "+err.Error())
	}
	previousInstalled, err := readWorkerGenerationRecord(workerGenerationRecordPath(paths, input.Worker.Previous.ID))
	if err != nil || !workerGenerationRestartable(previousInstalled, record, paths) {
		return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; retained previous Worker is not exactly restartable")
	}
	previous := observeWorkerGeneration(ctx, previousInstalled.Input, record, paths)
	if previous.Status != "active-open" || !previous.Worker.UnitActive || !previous.Worker.QueueConnected {
		if err := restorePreviousWorker(ctx, previousInstalled.Input, record, paths); err != nil {
			return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; retained previous Worker could not be restored: "+err.Error())
		}
	}
	if verification.PreviousStartedAt == nil {
		previousStartedAt := time.Now().UTC()
		verification.PreviousStartedAt = &previousStartedAt
		if err := writeJSONAtomic(verificationPath, verification, 0444); err != nil {
			return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; restored previous Worker start could not be recorded")
		}
	}
	rollbackDeadline := verification.PreviousStartedAt.Add(paths.healthTimeout)
	for {
		processed, acknowledged := workerHandledMessage(workerEvidencePath(paths), verification.MessageID, previousInstalled.Input.Revision, previousInstalled.Input.ArtifactDigest)
		if processed && acknowledged {
			if _, err := exactWorkerHandoffQueue(ctx, input, record, paths); err != nil {
				return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; restored Worker acknowledged work but "+err.Error())
			}
			if err := commitActiveWorker(previousInstalled.Input, paths); err != nil {
				return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; restored Worker acknowledged work but durable authority could not be committed: "+err.Error())
			}
			acknowledgedAt := time.Now().UTC()
			verification.RollbackAcknowledgedAt = &acknowledgedAt
			verification.CompletedAt, verification.Outcome = &acknowledgedAt, "rolled-back"
			if err := writeJSONAtomic(verificationPath, verification, 0444); err != nil {
				return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; rollback succeeded but completion could not be recorded")
			}
			rolledBack := observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths)
			return rolledBack, errors.New("active Worker verification failed and the retained previous Worker was restored")
		}
		previous := observeWorkerGeneration(ctx, previousInstalled.Input, record, paths)
		if previous.Status != "active-open" || !previous.Worker.UnitActive || !previous.Worker.QueueConnected || time.Now().After(rollbackDeadline) || ctx.Err() != nil {
			return uncertainWorkerActiveVerification(observeWorkerActiveVerification(ctx, claim.PlanID, planned, record, paths), failureReason+"; restored previous Worker did not acknowledge the same stable message identity")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func observeWorkerActiveVerification(ctx context.Context, planID string, planned planner.Operation, record bootstrapRecord, paths executionPaths) host.AsyncWorkerActiveVerificationObservation {
	input := *planned.Input.Async.WorkerHandoff
	digest, _ := planner.OperationDigest(planned)
	candidate := observeWorkerGeneration(ctx, input.Worker, record, paths)
	result := host.AsyncWorkerActiveVerificationObservation{
		PlanID: planID, OperationDigest: digest, QueueGenerationID: input.QueueGenerationID,
		Candidate: candidate.Worker, MessageID: workerVerificationMessageID(planID, digest),
	}
	previousInstalled, previousErr := readWorkerGenerationRecord(workerGenerationRecordPath(paths, input.Worker.Previous.ID))
	if previousErr == nil {
		result.Previous = observeWorkerGeneration(ctx, previousInstalled.Input, record, paths).Worker
	} else {
		result.Previous = *input.Worker.Previous
	}
	var verification workerActiveVerificationRecord
	if err := readExactJSON(workerActiveVerificationPath(paths, input.Worker.GenerationID), &verification); err != nil {
		if _, statErr := os.Lstat(workerActiveVerificationPath(paths, input.Worker.GenerationID)); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			result.Status = host.WorkerActiveVerificationUncertain
			result.Reason = "durable Worker verification record is unreadable or unsafe"
			result.RecoveryAction = "keep candidate intake fenced and inspect the exact Worker, Queue, and verification records before choosing recovery"
		}
		return result
	}
	if err := validateWorkerActiveVerificationRecord(verification, input, planID, digest); err != nil {
		result.Status = host.WorkerActiveVerificationUncertain
		result.Reason = err.Error()
		result.RecoveryAction = "keep candidate intake fenced and inspect the conflicting Worker verification record"
		return result
	}
	result.PublisherConfirmed = verification.PublisherConfirmedAt != nil
	result.CandidateProcessed, result.CandidateAcknowledged = workerHandledMessage(workerEvidencePath(paths), verification.MessageID, input.Worker.Revision, input.Worker.ArtifactDigest)
	if previousErr == nil {
		result.PreviousProcessed, result.PreviousAcknowledged = workerHandledMessage(workerEvidencePath(paths), verification.MessageID, previousInstalled.Input.Revision, previousInstalled.Input.ArtifactDigest)
	}
	result.RollbackAttempted = verification.RollbackAttemptedAt != nil
	result.Redelivered = workerMessageEvent(workerEvidencePath(paths), verification.MessageID, input.Worker.Revision, input.Worker.ArtifactDigest, "requeued")
	switch verification.Outcome {
	case "healthy":
		if verification.CompletedAt != nil && result.PublisherConfirmed && result.CandidateProcessed && result.CandidateAcknowledged && candidate.Status == "active-open" && candidate.Verified && candidate.Worker.Active {
			result.Status = host.WorkerActiveVerificationHealthy
			return result
		}
	case "rolled-back":
		previous := observeWorkerGeneration(ctx, previousInstalled.Input, record, paths)
		result.Previous = previous.Worker
		result.Restored = &result.Previous
		if verification.CompletedAt != nil && verification.RollbackAcknowledgedAt != nil && result.PublisherConfirmed && result.PreviousProcessed && result.PreviousAcknowledged && previous.Status == "active-open" && previous.Verified && previous.Worker.Active && candidate.Worker.Gate == "closed" && !candidate.Worker.UnitActive {
			result.Status = host.WorkerActiveVerificationRolledBack
			result.RollbackSucceeded = true
			result.Reason = verification.FailureReason
			return result
		}
	}
	if verification.Outcome == "" && verification.PublisherConfirmedAt != nil {
		result.Status = host.WorkerActiveVerificationPending
		result.Reason = "publisher-confirmed Worker verification has a resumable durable checkpoint"
		result.RecoveryAction = "resume the exact approved operation under a higher fencing token; observe Queue, message, gates, units, and active authority before each remaining mutation"
		return result
	}
	result.Status = host.WorkerActiveVerificationUncertain
	result.Reason = "durable Worker verification has no exact completed outcome"
	result.RecoveryAction = "keep candidate intake fenced and reconcile the stable message, Queue, and Worker authority before retrying"
	return result
}

func validateWorkerActiveVerificationRecord(verification workerActiveVerificationRecord, input planner.AsyncWorkerHandoffInput, planID, digest string) error {
	if verification.SchemaVersion != "provision.dev/worker-active-verification/v1alpha1" || verification.PlanID != planID || verification.OperationDigest != digest || verification.QueueGenerationID != input.QueueGenerationID || verification.CandidateID != input.Worker.GenerationID || verification.PreviousID != input.Worker.Previous.ID || verification.MessageID != workerVerificationMessageID(planID, digest) || verification.PublishStartedAt.IsZero() {
		return errors.New("durable Worker verification record differs from the exact approved operation")
	}
	ordered := []*time.Time{verification.PublisherConfirmedAt, verification.CandidateAcknowledgedAt, verification.RollbackAttemptedAt, verification.CandidateFencedAt, verification.PreviousStartedAt, verification.RollbackAcknowledgedAt, verification.CompletedAt}
	last := verification.PublishStartedAt
	for _, moment := range ordered {
		if moment == nil {
			continue
		}
		if moment.Before(last) {
			return errors.New("durable Worker verification record contains out-of-order evidence")
		}
		last = *moment
	}
	if verification.Outcome != "" && verification.Outcome != "healthy" && verification.Outcome != "rolled-back" || verification.Outcome != "" && verification.CompletedAt == nil {
		return errors.New("durable Worker verification record contains an unsupported outcome")
	}
	if verification.Outcome == "healthy" && (verification.PublisherConfirmedAt == nil || verification.CandidateAcknowledgedAt == nil || verification.RollbackAttemptedAt != nil) {
		return errors.New("durable healthy Worker verification record is incomplete")
	}
	if verification.Outcome == "rolled-back" && (verification.PublisherConfirmedAt == nil || verification.RollbackAttemptedAt == nil || verification.CandidateFencedAt == nil || verification.PreviousStartedAt == nil || verification.RollbackAcknowledgedAt == nil || verification.FailureReason == "") {
		return errors.New("durable rolled-back Worker verification record is incomplete")
	}
	return nil
}

func workerVerificationMessageID(planID, operationDigest string) string {
	revision := "provision-worker-verification"
	invocation := "verify-" + strings.TrimPrefix(planID, "sha256:")[:16]
	digest := sha256.Sum256([]byte(fmt.Sprintf("provision-example-async\x00%s\x00%s\x00%s\x00%d", revision, operationDigest, invocation, 1)))
	return "msg-" + hex.EncodeToString(digest[:])
}

func workerVerificationPayload(planID, operationDigest, messageID string) ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion       string `json:"schemaVersion"`
		MessageID           string `json:"messageId"`
		ApplicationRevision string `json:"applicationRevision"`
		TaskArtifactDigest  string `json:"taskArtifactDigest"`
		InvocationID        string `json:"invocationId"`
		Sequence            int    `json:"sequence"`
		Behavior            string `json:"behavior"`
	}{
		SchemaVersion: "provision.dev/example-async-message/v1alpha1", MessageID: messageID,
		ApplicationRevision: "provision-worker-verification", TaskArtifactDigest: operationDigest,
		InvocationID: "verify-" + strings.TrimPrefix(planID, "sha256:")[:16], Sequence: 1, Behavior: "process",
	})
}

func exactWorkerHandoffQueue(ctx context.Context, input planner.AsyncWorkerHandoffInput, record bootstrapRecord, paths executionPaths) (planner.AsyncQueueInput, error) {
	var generation queueGenerationRecord
	path := filepath.Join(paths.environmentHome, "services", "rabbitmq", "generation.json")
	if err := readExactJSON(path, &generation); err != nil || generation.SchemaVersion != queueGenerationSchema || generation.Queue.GenerationID != input.QueueGenerationID || generation.Queue.LogicalID != input.Worker.QueueLogicalID {
		return planner.AsyncQueueInput{}, errors.New("live Queue generation differs from the approved Worker verification")
	}
	queue, findings := inspectRecordedQueue(ctx, record.Environment, record.Account, paths)
	if queue == nil || len(findings) != 0 || !queue.Ready || queue.GenerationID != input.QueueGenerationID {
		return planner.AsyncQueueInput{}, errors.New("live Queue health or identity cannot be proved for Worker verification")
	}
	return generation.Queue, nil
}

func publishWorkerVerificationMessage(ctx context.Context, queue planner.AsyncQueueInput, environment, planID, operationDigest, messageID string) error {
	command := exec.CommandContext(ctx, "systemd-creds", "decrypt", "--name=rabbitmq-url", queueURLCredentialPath(environment), "-")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	credential, err := command.Output()
	if err != nil {
		return fmt.Errorf("decrypt managed Queue credential: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	username, password, err := parseQueueSecret(strings.TrimSpace(string(credential)), queue.AMQPPort)
	for index := range credential {
		credential[index] = 0
	}
	if err != nil {
		return err
	}
	config := amqp.Config{SASL: []amqp.Authentication{&amqp.PlainAuth{Username: username, Password: password}}, Heartbeat: 10 * time.Second, Locale: "en_US", Dial: amqp.DefaultDial(5 * time.Second), Properties: amqp.Table{"connection_name": "provision-worker-verification"}}
	connection, err := amqp.DialConfig(fmt.Sprintf("amqp://127.0.0.1:%d/", queue.AMQPPort), config)
	password = ""
	if err != nil {
		return errors.New("connect to managed Queue for Worker verification")
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		return errors.New("open managed Queue channel for Worker verification")
	}
	defer channel.Close()
	if err := channel.Confirm(false); err != nil {
		return errors.New("enable publisher confirms for Worker verification")
	}
	confirmations := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	returns := channel.NotifyReturn(make(chan amqp.Return, 1))
	topology := topologyFor(queue.LogicalID)
	payload, err := workerVerificationPayload(planID, operationDigest, messageID)
	if err != nil {
		return errors.New("encode Worker verification message")
	}
	if err := channel.PublishWithContext(ctx, topology.WorkExchange, queue.LogicalID, true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: messageID, Timestamp: time.Now().UTC(), Body: payload}); err != nil {
		return errors.New("publish Worker verification message")
	}
	select {
	case returned := <-returns:
		return fmt.Errorf("Worker verification message was returned as unroutable with reply code %d", returned.ReplyCode)
	case confirmation := <-confirmations:
		if !confirmation.Ack {
			return errors.New("Worker verification message was negatively acknowledged")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(15 * time.Second):
		return errors.New("Worker verification publisher confirm timed out")
	}
}

func commitActiveWorker(input planner.AsyncWorkerInput, paths executionPaths) error {
	recordPath := workerGenerationRecordPath(paths, input.GenerationID)
	installed, err := readWorkerGenerationRecord(recordPath)
	if err != nil || !installed.Verified || !workerRecordMatchesInput(installed, input) {
		return errors.New("Worker immutable generation record is not exactly verified")
	}
	installed.Active = true
	if err := writeJSONAtomic(recordPath, installed, 0444); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(filepath.Dir(recordPath), "active.json"), installed, 0444)
}

func fenceFailedWorkerCandidate(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) error {
	if err := replaceGateFile(workerGatePath(paths, input.GenerationID), "closed\n"); err != nil {
		return err
	}
	deadline := time.Now().Add(paths.healthTimeout)
	for {
		active, _ := paths.systemd.Run(ctx, "is-active", input.SystemdUnit)
		if strings.TrimSpace(string(active)) != "active" {
			break
		}
		state, stateErr := readWorkerState(workerStatePath(paths, input.GenerationID))
		if stateErr == nil && state.Gated && !state.Consuming {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("candidate Worker did not close intake within the verification bound")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if output, err := paths.systemd.Run(ctx, "disable", "--now", input.SystemdUnit); err != nil {
		return fmt.Errorf("stop and disable failed Worker candidate: %v: %s", err, strings.TrimSpace(string(output)))
	}
	active, _ := paths.systemd.Run(ctx, "is-active", input.SystemdUnit)
	gate, gateErr := os.ReadFile(workerGatePath(paths, input.GenerationID))
	if strings.TrimSpace(string(active)) == "active" || gateErr != nil || string(gate) != "closed\n" {
		return errors.New("candidate Worker intake fence and stopped unit cannot be proved")
	}
	return nil
}

func restorePreviousWorker(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths) error {
	if output, err := paths.systemd.Run(ctx, "enable", "--now", input.SystemdUnit); err != nil {
		return fmt.Errorf("start retained previous Worker: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if _, err := waitForWorker(ctx, input, record, paths, true); err != nil {
		return err
	}
	if err := replaceGateFile(workerGatePath(paths, input.GenerationID), "open\n"); err != nil {
		return err
	}
	_, err := waitForWorker(ctx, input, record, paths, false)
	return err
}

func uncertainWorkerActiveVerification(observed host.AsyncWorkerActiveVerificationObservation, reason string) (host.AsyncWorkerActiveVerificationObservation, error) {
	observed.Status = host.WorkerActiveVerificationUncertain
	observed.Reason = reason
	observed.RollbackSucceeded = false
	observed.RecoveryAction = "keep candidate intake fenced and inspect the exact Queue message, Worker gates, units, and durable authority before choosing recovery"
	return observed, fmt.Errorf("%w: %s", errUncertainRecovery, reason)
}

func uncertainWorkerActiveVerificationWithCandidateFence(ctx context.Context, candidate planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths, observed host.AsyncWorkerActiveVerificationObservation, reason string) (host.AsyncWorkerActiveVerificationObservation, error) {
	if err := fenceFailedWorkerCandidate(ctx, candidate, record, paths); err != nil {
		return uncertainWorkerActiveVerification(observed, reason+"; candidate intake could not be proved fenced: "+err.Error())
	}
	observed.Status = host.WorkerActiveVerificationUncertain
	observed.Candidate = observeWorkerGeneration(ctx, candidate, record, paths).Worker
	observed.RollbackSucceeded = false
	observed.Reason = reason
	observed.RecoveryAction = "candidate intake is fenced; inspect the exact Queue message, Worker units, and durable authority before choosing recovery"
	return observed, fmt.Errorf("%w: %s", errUncertainRecovery, reason)
}

func fencePreviousWorkerIntake(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths) (host.AsyncWorkerHandoffObservation, error) {
	input := *planned.Input.Async.WorkerHandoff
	observed := observeWorkerHandoff(ctx, claim.PlanID, planned, record, paths)
	if observed.Status == "previous-fenced" {
		return observed, nil
	}
	candidate := observeWorkerGeneration(ctx, input.Worker, record, paths)
	if !candidate.Verified || candidate.Status != "active-gated" || candidate.Worker.Gate != "closed" {
		observed.Status, observed.Reason = "failed", "Worker candidate is not exactly verified with intake closed"
		observed.RecoveryAction = "leave the previous Worker open and correct or replace the candidate before retrying"
		return observed, errors.New(observed.Reason)
	}
	if err := requireExactActiveWorker(input.Worker, paths); err != nil {
		observed.Status, observed.Reason = "failed", err.Error()
		observed.RecoveryAction = "inspect the active Worker identity before retrying the approved handoff"
		return observed, err
	}
	if err := replaceGateFile(workerGatePath(paths, input.Worker.Previous.ID), "closed\n"); err != nil {
		observed.Status, observed.Reason = "failed", err.Error()
		observed.RecoveryAction = "keep candidate intake closed and inspect the previous Worker gate"
		return observed, err
	}
	deadline := time.Now().Add(paths.healthTimeout)
	for {
		state, stateErr := readWorkerState(workerStatePath(paths, input.Worker.Previous.ID))
		gate, gateErr := os.ReadFile(workerGatePath(paths, input.Worker.Previous.ID))
		if stateErr == nil && gateErr == nil && string(gate) == "closed\n" && state.Connected && state.Gated && !state.Consuming {
			observed = observeWorkerHandoff(ctx, claim.PlanID, planned, record, paths)
			if observed.Status == "previous-fenced" {
				return observed, nil
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			observed.Status = "failed"
			observed.Reason = "previous Worker did not durably stop intake within the verification bound"
			observed.RecoveryAction = "keep candidate intake closed; inspect the previous Worker gate and consumer cancellation"
			return observed, errors.New(observed.Reason)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func drainPreviousWorker(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths) (host.AsyncWorkerHandoffObservation, error) {
	input := *planned.Input.Async.WorkerHandoff
	observed := observeWorkerHandoff(ctx, claim.PlanID, planned, record, paths)
	if workerHandoffSatisfied(planner.DrainWorkerPrevious, observed) && observed.PlanID == claim.PlanID {
		return observed, nil
	}
	digest, _ := planner.OperationDigest(planned)
	maximum, _ := time.ParseDuration(input.Worker.Drain.MaxDuration)
	drainPath := workerDrainRecordPath(paths, input.Worker.Previous.ID)
	var drain drainedWorkerGeneration
	readErr := readExactJSON(drainPath, &drain)
	if readErr != nil {
		if _, statErr := os.Lstat(drainPath); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			observed.Status, observed.Reason = "failed", "durable previous Worker drain record exists but is unreadable or unsafe"
			observed.RecoveryAction = "keep candidate intake closed and inspect the conflicting durable drain record"
			return observed, errors.New(observed.Reason)
		}
		state, fenceErr := requirePreviousWorkerFenced(ctx, input, paths)
		if fenceErr != nil {
			observed.Status, observed.Reason = "failed", fenceErr.Error()
			observed.RecoveryAction = "keep candidate intake closed and complete the previous Worker fence before draining"
			return observed, fenceErr
		}
		now := time.Now().UTC()
		deadline := now.Add(maximum)
		drain = drainedWorkerGeneration{
			SchemaVersion: "provision.dev/worker-drain/v1alpha2", PlanID: claim.PlanID, OperationDigest: digest,
			CandidateID: input.Worker.GenerationID, PreviousID: input.Worker.Previous.ID, QueueGenerationID: input.QueueGenerationID,
			StartedAt: now, CompletionDeadline: deadline.Add(-planner.WorkerDrainReleaseBudget(maximum)), Deadline: deadline, InFlightMessageID: state.InFlightMessageID,
		}
		if err := writeJSONAtomic(drainPath, drain, 0444); err != nil {
			observed.Status, observed.Reason = "failed", "record durable previous Worker drain deadline: "+err.Error()
			observed.RecoveryAction = "keep candidate intake closed and retry the exact approved drain"
			return observed, errors.New(observed.Reason)
		}
	} else if err := validateDrainRecord(drain, input, claim.PlanID, digest, maximum); err != nil {
		observed.Status, observed.Reason = "failed", err.Error()
		observed.RecoveryAction = "keep candidate intake closed and inspect the conflicting durable drain record"
		return observed, err
	}
	if drain.CompletedAt != nil {
		return observeWorkerHandoff(ctx, claim.PlanID, planned, record, paths), nil
	}
	if drain.ReleaseStartedAt == nil {
		for {
			active, _ := paths.systemd.Run(ctx, "is-active", input.Worker.Previous.SystemdUnit)
			state, stateErr := readWorkerState(workerStatePath(paths, input.Worker.Previous.ID))
			if stateErr != nil {
				observed.Status, observed.Reason = "failed", "previous Worker runtime state became unreadable during drain"
				observed.RecoveryAction = "keep candidate intake closed and inspect the previous Worker runtime state"
				return observed, errors.New(observed.Reason)
			}
			if strings.TrimSpace(string(active)) != "active" || state.InFlightMessageID == "" {
				break
			}
			if drain.InFlightMessageID == "" {
				drain.InFlightMessageID = state.InFlightMessageID
				if err := writeJSONAtomic(drainPath, drain, 0444); err != nil {
					return observed, fmt.Errorf("record exact in-flight Worker message identity: %w", err)
				}
			}
			if !time.Now().Before(drain.CompletionDeadline) {
				releaseStartedAt := time.Now().UTC()
				if releaseStartedAt.After(drain.Deadline) {
					observed.Status, observed.Reason = "failed", "previous Worker release could not start before the durable drain deadline"
					observed.RecoveryAction = "keep candidate intake closed and inspect the previous Worker shutdown path"
					return observed, errors.New(observed.Reason)
				}
				drain.ReleaseStartedAt = &releaseStartedAt
				if err := writeJSONAtomic(drainPath, drain, 0444); err != nil {
					return observed, fmt.Errorf("record bounded previous Worker release start: %w", err)
				}
				break
			}
			select {
			case <-ctx.Done():
				observed.Status = "failed"
				observed.Reason = "previous Worker drain was interrupted after its durable deadline was recorded: " + ctx.Err().Error()
				observed.RecoveryAction = "keep candidate intake closed and retry the exact approved drain; the original deadline will be reused"
				return observed, errors.New(observed.Reason)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	stopCtx, cancelStop := context.WithDeadline(ctx, drain.Deadline)
	stopOutput, stopErr := paths.systemd.Run(stopCtx, "stop", input.Worker.Previous.SystemdUnit)
	cancelStop()
	if stopErr != nil {
		observed.Status, observed.Reason = "failed", fmt.Sprintf("stop fenced previous Worker before durable drain deadline: %v: %s", stopErr, bytes.TrimSpace(stopOutput))
		observed.RecoveryAction = "keep candidate intake closed and inspect the exact previous Worker shutdown before retrying"
		return observed, errors.New(observed.Reason)
	}
	for {
		now := time.Now().UTC()
		active, _ := paths.systemd.Run(ctx, "is-active", input.Worker.Previous.SystemdUnit)
		state, stateErr := readWorkerState(workerStatePath(paths, input.Worker.Previous.ID))
		if strings.TrimSpace(string(active)) != "active" && stateErr == nil && state.InFlightMessageID == "" && state.Gated && !state.Consuming && !now.After(drain.Deadline) {
			break
		}
		if !now.Before(drain.Deadline) || ctx.Err() != nil {
			observed.Status, observed.Reason = "failed", "previous Worker did not stop and settle its in-flight delivery before the durable drain deadline"
			observed.RecoveryAction = "keep candidate intake closed and inspect the exact previous Worker unit, broker delivery, and settlement evidence"
			return observed, errors.New(observed.Reason)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if drain.InFlightMessageID != "" {
		acknowledged := workerMessageEvent(workerEvidencePath(paths), drain.InFlightMessageID, input.Worker.Previous.Revision, input.Worker.Previous.ArtifactDigest, "acknowledged")
		requeued := workerMessageEvent(workerEvidencePath(paths), drain.InFlightMessageID, input.Worker.Previous.Revision, input.Worker.Previous.ArtifactDigest, "requeued")
		if acknowledged == requeued {
			observed.Status, observed.Reason = "failed", "stopped previous Worker does not have exactly one durable acknowledgement or requeue outcome for its in-flight message"
			observed.RecoveryAction = "keep candidate intake closed and inspect RabbitMQ plus the previous Worker settlement evidence"
			return observed, errors.New(observed.Reason)
		}
		if requeued {
			drain.BoundElapsed = true
			drain.ReleasedMessageID = drain.InFlightMessageID
		}
	}
	completedAt := time.Now().UTC()
	if completedAt.After(drain.Deadline) {
		observed.Status, observed.Reason = "failed", "previous Worker settlement completed after the durable drain deadline"
		observed.RecoveryAction = "keep candidate intake closed and inspect the previous Worker shutdown and broker settlement latency"
		return observed, errors.New(observed.Reason)
	}
	drain.CompletedAt = &completedAt
	if err := writeJSONAtomic(drainPath, drain, 0444); err != nil {
		observed.Status, observed.Reason = "failed", "record completed previous Worker drain: "+err.Error()
		observed.RecoveryAction = "keep candidate intake closed and retry exact drain observation"
		return observed, errors.New(observed.Reason)
	}
	return observeWorkerHandoff(ctx, claim.PlanID, planned, record, paths), nil
}

func validateDrainRecord(drain drainedWorkerGeneration, input planner.AsyncWorkerHandoffInput, planID, digest string, maximum time.Duration) error {
	if drain.SchemaVersion != "provision.dev/worker-drain/v1alpha2" || drain.PlanID != planID || drain.OperationDigest != digest || drain.CandidateID != input.Worker.GenerationID || drain.PreviousID != input.Worker.Previous.ID || drain.QueueGenerationID != input.QueueGenerationID || drain.StartedAt.IsZero() || !drain.Deadline.Equal(drain.StartedAt.Add(maximum)) || !drain.CompletionDeadline.Equal(drain.Deadline.Add(-planner.WorkerDrainReleaseBudget(maximum))) {
		return errors.New("durable previous Worker drain record differs from the exact approved operation")
	}
	if drain.ReleaseStartedAt != nil && (drain.ReleaseStartedAt.Before(drain.CompletionDeadline) || drain.ReleaseStartedAt.After(drain.Deadline)) {
		return errors.New("durable previous Worker release start is outside its reserved deadline budget")
	}
	if drain.CompletedAt != nil && (drain.CompletedAt.Before(drain.StartedAt) || drain.CompletedAt.After(drain.Deadline) || drain.ReleaseStartedAt != nil && drain.CompletedAt.Before(*drain.ReleaseStartedAt)) {
		return errors.New("durable previous Worker drain completion is outside its recorded bound")
	}
	if drain.CompletedAt == nil && (drain.BoundElapsed || drain.ReleasedMessageID != "") {
		return errors.New("in-progress previous Worker drain record claims a completed release")
	}
	if drain.CompletedAt != nil {
		released := drain.BoundElapsed && drain.ReleaseStartedAt != nil && drain.InFlightMessageID != "" && drain.ReleasedMessageID == drain.InFlightMessageID
		drained := !drain.BoundElapsed && drain.ReleasedMessageID == ""
		if !released && !drained {
			return errors.New("completed previous Worker drain record has inconsistent settlement evidence")
		}
	}
	return nil
}

func requirePreviousWorkerFenced(ctx context.Context, input planner.AsyncWorkerHandoffInput, paths executionPaths) (exampleWorkerState, error) {
	if err := requireExactActiveWorker(input.Worker, paths); err != nil {
		return exampleWorkerState{}, err
	}
	active, _ := paths.systemd.Run(ctx, "is-active", input.Worker.Previous.SystemdUnit)
	gate, gateErr := os.ReadFile(workerGatePath(paths, input.Worker.Previous.ID))
	state, stateErr := readWorkerState(workerStatePath(paths, input.Worker.Previous.ID))
	if strings.TrimSpace(string(active)) != "active" || gateErr != nil || string(gate) != "closed\n" || stateErr != nil || !state.Connected || !state.Gated || state.Consuming {
		return exampleWorkerState{}, errors.New("previous Worker intake fence is not durably observed")
	}
	return state, nil
}

func retainPreviousWorker(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths) (host.AsyncWorkerHandoffObservation, error) {
	input := *planned.Input.Async.WorkerHandoff
	observed := observeWorkerHandoff(ctx, claim.PlanID, planned, record, paths)
	if workerHandoffSatisfied(planner.RetainWorkerPrevious, observed) && observed.PlanID == claim.PlanID {
		return observed, nil
	}
	if err := requirePreviousWorkerDrained(ctx, input, claim.PlanID, record, paths); err != nil {
		observed.Status, observed.Reason = "failed", err.Error()
		observed.RecoveryAction = "retain both generations and inspect the completed Worker handoff before retrying"
		return observed, err
	}
	active := observeWorkerGeneration(ctx, input.Worker, record, paths)
	if active.Status != "active-open" || !active.Verified || !active.Worker.Active {
		observed.Status, observed.Reason = "failed", "candidate Worker is not the exact verified active generation"
		observed.RecoveryAction = "retain both generations and verify the active Worker record before retrying"
		return observed, errors.New(observed.Reason)
	}
	duration, _ := input.RollbackWindow.Duration()
	now := time.Now().UTC()
	digest, _ := planner.OperationDigest(planned)
	retention := retainedWorkerGeneration{
		SchemaVersion: "provision.dev/worker-retention/v1alpha1", PlanID: claim.PlanID, OperationDigest: digest,
		DrainOperationDigest: input.DrainOperationDigest,
		CandidateID:          input.Worker.GenerationID, PreviousID: input.Worker.Previous.ID, RollbackWindow: input.RollbackWindow,
		RetainedAt: now, RetainUntil: now.Add(duration),
	}
	if err := writeJSONAtomic(workerRetentionRecordPath(paths), retention, 0444); err != nil {
		observed.Status, observed.Reason = "failed", "record previous Worker rollback window: "+err.Error()
		observed.RecoveryAction = "retain both immutable generations and retry exact retention observation"
		return observed, errors.New(observed.Reason)
	}
	return observeWorkerHandoff(ctx, claim.PlanID, planned, record, paths), nil
}

func observeWorkerHandoff(ctx context.Context, planID string, planned planner.Operation, record bootstrapRecord, paths executionPaths) host.AsyncWorkerHandoffObservation {
	input := *planned.Input.Async.WorkerHandoff
	digest, _ := planner.OperationDigest(planned)
	result := host.AsyncWorkerHandoffObservation{Status: "pending", OperationDigest: digest, QueueGenerationID: input.QueueGenerationID}
	candidate := observeWorkerGeneration(ctx, input.Worker, record, paths)
	result.Candidate = candidate.Worker
	previousInstalled, err := readWorkerGenerationRecord(workerGenerationRecordPath(paths, input.Worker.Previous.ID))
	if err != nil {
		result.Status, result.Reason = "unknown", "previous Worker generation record is unreadable"
		result.RecoveryAction = "keep both Worker gates unchanged and restore the exact immutable previous Worker generation record before resuming"
		return result
	}
	previous := observeWorkerGeneration(ctx, previousInstalled.Input, record, paths)
	result.Previous = previous.Worker
	state, stateErr := readWorkerState(workerStatePath(paths, input.Worker.Previous.ID))
	if stateErr == nil {
		result.InFlightMessageID = state.InFlightMessageID
	}
	queue, queueFindings := inspectRecordedQueue(ctx, record.Environment, record.Account, paths)
	if queue == nil || len(queueFindings) != 0 || queue.GenerationID != input.QueueGenerationID {
		result.Status, result.Reason = "unknown", "Queue generation differs from the approved Worker handoff"
		result.RecoveryAction = "pause intake changes and restore or explicitly replan against the exact approved Queue generation"
		return result
	}
	if candidate.Status != "active-gated" && candidate.Status != "active-open" || !candidate.Verified {
		result.Status, result.Reason = "unknown", "Worker candidate is not an exact verified observation"
		result.RecoveryAction = "keep candidate intake closed and inspect its immutable Artifact, unit, gate, runtime state, and verification record"
		return result
	}
	switch planned.Kind {
	case planner.FenceWorkerIntake:
		if err := requireExactActiveWorker(input.Worker, paths); err != nil {
			result.Status, result.Reason = "unknown", err.Error()
			result.RecoveryAction = "do not change either Worker gate; reconcile durable Worker authority with the approved previous generation"
			return result
		}
		if previous.Status == "active-gated" && previous.Worker.UnitActive && stateErr == nil && state.Connected && state.Gated && !state.Consuming {
			result.Status = "previous-fenced"
		}
	case planner.DrainWorkerPrevious:
		var drained drainedWorkerGeneration
		if readExactJSON(workerDrainRecordPath(paths, input.Worker.Previous.ID), &drained) != nil {
			return result
		}
		maximum, durationErr := time.ParseDuration(input.Worker.Drain.MaxDuration)
		if durationErr != nil || validateDrainRecord(drained, input, planID, digest, maximum) != nil || drained.CompletedAt == nil || previous.Worker.UnitActive || previous.Worker.InFlight != 0 || previous.Worker.Gate != "closed" {
			result.Status, result.Reason = "unknown", "durable previous Worker drain record does not match observed generations"
			result.RecoveryAction = "keep candidate intake closed and reconcile the exact drain deadline, previous unit, in-flight identity, and broker settlement"
			return result
		}
		result.PlanID = drained.PlanID
		result.InFlightMessageID = drained.InFlightMessageID
		result.ReleasedMessageID = drained.ReleasedMessageID
		result.BoundElapsed = drained.BoundElapsed
		result.DrainStartedAt = &drained.StartedAt
		result.CompletionDeadline = &drained.CompletionDeadline
		result.DrainDeadline = &drained.Deadline
		result.ReleaseStartedAt = drained.ReleaseStartedAt
		result.DrainCompletedAt = drained.CompletedAt
		if drained.ReleasedMessageID != "" {
			result.Status = "previous-released"
		} else {
			result.Status = "previous-drained"
		}
	case planner.RetainWorkerPrevious:
		var retained retainedWorkerGeneration
		if readExactJSON(workerRetentionRecordPath(paths), &retained) != nil {
			return result
		}
		if validateRetentionRecord(retained, input, planID, digest) != nil || !workerGenerationRestartable(previousInstalled, record, paths) {
			result.Status, result.Reason = "unknown", "previous Worker retention record or restartable generation differs from the approved handoff"
			result.RecoveryAction = "retain both generations and reconcile the rollback window, previous Artifact, unit, gate, and durable active authority"
			return result
		}
		result.Status = "previous-retained"
		result.PlanID = retained.PlanID
		result.DrainOperationDigest = retained.DrainOperationDigest
		result.RollbackWindow = retained.RollbackWindow
		result.RetainedAt = &retained.RetainedAt
		result.RetainUntil = &retained.RetainUntil
		result.Previous.Restartable = true
		result.Previous.RetainUntil = &retained.RetainUntil
	}
	return result
}

func validateRetentionRecord(retained retainedWorkerGeneration, input planner.AsyncWorkerHandoffInput, planID, digest string) error {
	duration, err := input.RollbackWindow.Duration()
	if err != nil || retained.SchemaVersion != "provision.dev/worker-retention/v1alpha1" || retained.PlanID != planID || retained.OperationDigest != digest || retained.DrainOperationDigest != input.DrainOperationDigest || retained.CandidateID != input.Worker.GenerationID || retained.PreviousID != input.Worker.Previous.ID || retained.RollbackWindow != input.RollbackWindow || retained.RetainedAt.IsZero() || !retained.RetainUntil.Equal(retained.RetainedAt.Add(duration)) {
		return errors.New("durable previous Worker retention record differs from the exact approved operation")
	}
	return nil
}

func requireExactActiveWorker(input planner.AsyncWorkerInput, paths executionPaths) error {
	if input.Previous == nil {
		return errors.New("Worker handoff has no planned previous generation")
	}
	active, err := readWorkerGenerationRecord(filepath.Join(paths.environmentHome, "workers", "active.json"))
	if err != nil || !active.Active || active.Input.GenerationID != input.Previous.ID || active.Input.Revision != input.Previous.Revision || active.Input.ArtifactDigest != input.Previous.ArtifactDigest || active.Input.SystemdUnit != input.Previous.SystemdUnit {
		return errors.New("durable active Worker record does not identify the planned previous generation")
	}
	return nil
}

func requirePreviousWorkerDrained(ctx context.Context, input planner.AsyncWorkerHandoffInput, planID string, record bootstrapRecord, paths executionPaths) error {
	if input.Worker.Previous == nil {
		return nil
	}
	previousInstalled, err := readWorkerGenerationRecord(workerGenerationRecordPath(paths, input.Worker.Previous.ID))
	if err != nil || !workerGenerationRestartable(previousInstalled, record, paths) {
		return errors.New("previous Worker immutable generation is not restartable")
	}
	active, _ := paths.systemd.Run(ctx, "is-active", input.Worker.Previous.SystemdUnit)
	gate, gateErr := os.ReadFile(workerGatePath(paths, input.Worker.Previous.ID))
	state, stateErr := readWorkerState(workerStatePath(paths, input.Worker.Previous.ID))
	if strings.TrimSpace(string(active)) == "active" || gateErr != nil || string(gate) != "closed\n" || stateErr != nil || state.InFlightMessageID != "" || !state.Gated || state.Consuming {
		return errors.New("previous Worker is not durably fenced, stopped, and free of in-flight deliveries")
	}
	queue, queueFindings := inspectRecordedQueue(ctx, record.Environment, record.Account, paths)
	if queue == nil || len(queueFindings) != 0 || queue.GenerationID != input.QueueGenerationID {
		return errors.New("live Queue generation differs from the approved Worker handoff")
	}
	var drained drainedWorkerGeneration
	if readExactJSON(workerDrainRecordPath(paths, input.Worker.Previous.ID), &drained) != nil || drained.CompletedAt == nil || drained.PlanID != planID || drained.OperationDigest != input.DrainOperationDigest || drained.CandidateID != input.Worker.GenerationID || drained.PreviousID != input.Worker.Previous.ID || drained.QueueGenerationID != input.QueueGenerationID {
		return errors.New("previous Worker has no exact durable drain or release record")
	}
	return nil
}

func workerGenerationRestartable(installed installedWorkerGeneration, record bootstrapRecord, paths executionPaths) bool {
	reference := asyncGenerationReference(installed.Input.GenerationID, installed.Input.Revision, installed.Input.ArtifactDigest, record, paths)
	generation := observeGeneration(planner.GenerationInput{GenerationReference: reference})
	return installed.SchemaVersion == asyncGenerationSchema && installed.Verified && installed.Active && generation.Status == host.CandidateInstalled && generation.Executable == installed.Executable && exactRootFile(filepath.Join(paths.systemdUnits, installed.Input.SystemdUnit), []byte(renderWorkerUnit(installed.Input, record, paths, installed.Executable)), 0644) && exactRootFile(workerGatePath(paths, installed.Input.GenerationID), []byte("closed\n"), 0644)
}

func workerMessageEvent(path, messageID, revision, digest, eventName string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct{ Event, MessageID, WorkerApplicationRevision, WorkerArtifactDigest string }
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Event == eventName && event.MessageID == messageID && event.WorkerApplicationRevision == revision && event.WorkerArtifactDigest == digest {
			return true
		}
	}
	return false
}

func waitForWorker(ctx context.Context, input planner.AsyncWorkerInput, record bootstrapRecord, paths executionPaths, gated bool) (host.AsyncWorkerOperationObservation, error) {
	deadline := time.Now().Add(paths.healthTimeout)
	for {
		observed := observeWorkerGeneration(ctx, input, record, paths)
		gatedReady := gated && observed.Status == "active-gated" && observed.Worker.Gate == "closed" && observed.Checks.Liveness && observed.Checks.QueueConnectivity && observed.Checks.RevisionIdentity && observed.Checks.IntakeDisabled
		activeReady := !gated && observed.Status == "active-open" && observed.Worker.Gate == "open" && observed.Checks.Liveness && observed.Checks.QueueConnectivity && observed.Checks.RevisionIdentity
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
	activeWorkerPreviousID := ""
	if worker, err := readWorkerGenerationRecord(workerActivePath); err == nil {
		observed := observeWorkerGeneration(ctx, worker.Input, record, paths)
		if observed.Status != "active-open" || !observed.Verified {
			findings = append(findings, "active Worker generation is not an exact open observation")
		} else {
			status := observed.Worker
			deployment.ActiveWorker = &status
			activeWorkerID = status.ID
			if worker.Input.Previous != nil {
				activeWorkerPreviousID = worker.Input.Previous.ID
			}
		}
	} else if activeRecordShouldExist(workerActivePath, filepath.Join(paths.environmentHome, "workers")) {
		findings = append(findings, "installed Worker generation has no readable active Worker record")
	}
	previousWorkerID := ""
	var retained retainedWorkerGeneration
	if err := readExactJSON(workerRetentionRecordPath(paths), &retained); err == nil {
		previousInstalled, previousErr := readWorkerGenerationRecord(workerGenerationRecordPath(paths, retained.PreviousID))
		if previousErr != nil || retained.SchemaVersion != "provision.dev/worker-retention/v1alpha1" || retained.CandidateID != activeWorkerID || !workerGenerationRestartable(previousInstalled, record, paths) {
			findings = append(findings, "retained previous Worker generation is not an exact restartable observation")
		} else {
			status := workerStatus(previousInstalled.Input, record, paths)
			gate, _ := os.ReadFile(status.GatePath)
			status.Gate = strings.TrimSpace(string(gate))
			status.Health = "retained"
			status.Restartable = true
			status.RetainUntil = &retained.RetainUntil
			deployment.Previous = &status
			previousWorkerID = retained.PreviousID
		}
	} else if _, statErr := os.Lstat(workerRetentionRecordPath(paths)); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
		findings = append(findings, "previous Worker retention record is unreadable")
	} else if activeWorkerID != "" && activeWorkerPreviousID != "" {
		var verification workerActiveVerificationRecord
		previousInstalled, previousErr := readWorkerGenerationRecord(workerGenerationRecordPath(paths, activeWorkerPreviousID))
		verificationErr := readExactJSON(workerActiveVerificationPath(paths, activeWorkerID), &verification)
		if previousErr != nil || verificationErr != nil || verification.Outcome != "healthy" || verification.CandidateID != activeWorkerID || verification.PreviousID != activeWorkerPreviousID || !workerGenerationRestartable(previousInstalled, record, paths) {
			findings = append(findings, "post-verification previous Worker generation is not an exact rollback-ready observation")
		} else {
			status := workerStatus(previousInstalled.Input, record, paths)
			status.Health = "rollback-ready"
			status.Restartable = true
			deployment.Previous = &status
			previousWorkerID = activeWorkerPreviousID
		}
	}
	workerEntries, workerEntriesErr := os.ReadDir(filepath.Join(paths.environmentHome, "workers"))
	if workerEntriesErr == nil {
		for _, entry := range workerEntries {
			if entry.IsDir() || entry.Name() == "active.json" || entry.Name() == "previous.json" || strings.HasSuffix(entry.Name(), ".drain.json") || strings.HasSuffix(entry.Name(), ".verification.json") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			candidate, readErr := readWorkerGenerationRecord(filepath.Join(paths.environmentHome, "workers", entry.Name()))
			if readErr != nil || candidate.Input.GenerationID == activeWorkerID || candidate.Input.GenerationID == previousWorkerID {
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
func workerDrainRecordPath(paths executionPaths, generation string) string {
	return filepath.Join(paths.environmentHome, "workers", generation+".drain.json")
}
func workerRetentionRecordPath(paths executionPaths) string {
	return filepath.Join(paths.environmentHome, "workers", "previous.json")
}
func workerActiveVerificationPath(paths executionPaths, generation string) string {
	return filepath.Join(paths.environmentHome, "workers", generation+".verification.json")
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
	if err := os.Chown(temporary, os.Geteuid(), os.Getegid()); err != nil {
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

func workerHandledMessage(path, messageID, workerRevision, workerArtifactDigest string) (bool, bool) {
	if messageID == "" {
		return false, false
	}
	file, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer file.Close()
	handled, acknowledged := false, false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct{ Event, MessageID, WorkerApplicationRevision, WorkerArtifactDigest string }
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.MessageID != messageID || event.WorkerApplicationRevision != workerRevision || event.WorkerArtifactDigest != workerArtifactDigest {
			continue
		}
		handled = handled || event.Event == "processed" || event.Event == "duplicate_ignored"
		acknowledged = acknowledged || event.Event == "acknowledged"
	}
	return handled, acknowledged
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
