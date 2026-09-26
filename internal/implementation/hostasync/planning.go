package hostasync

import (
	"fmt"
	"strings"

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planmodel"
)

type PlanningOutput struct {
	Transitions              TransitionSet
	SensitiveValueReferences []string
	QueueOnly                bool
	WorkerOnly               bool
}

type TransitionSet struct {
	QueuePreparation planmodel.Operation
	WorkerArtifact   planmodel.Operation
	TaskArtifact     planmodel.Operation
	Task             TransitionChain
	Worker           TransitionChain
	Schedule         ScheduleTransitionChain
}

type TransitionChain struct {
	Operations []planmodel.Operation
	EntryID    string
	ReadyID    string
}

type ScheduleTransitionChain struct {
	Operations   []planmodel.Operation
	RuntimeID    string
	ActivationID string
	ReadyID      string
}

// Plan translates the qualified Host/RabbitMQ implementation into typed,
// side-effect-free operations. Physical names and transition policy stay in
// this adapter; the core Planner only embeds the returned canonical sequence.
func Plan(compiled config.Compiled, observation host.BootstrapStatus) PlanningOutput {
	roles := roleNames(compiled)
	async := observation.Async
	queueName, workerName, taskName, scheduleName := roles["queue"], roles["worker"], roles["task"], roles["schedule"]
	queueComponent := compiled.Application.Components[queueName]
	queueImplementation := compiled.Environment.Implementations[queueName]
	queueInput := &planmodel.AsyncQueueInput{
		Component: queueName, LogicalID: "provision-" + compiled.Environment.Name + "-" + queueName,
		GenerationID:   "provision-" + compiled.Environment.Name + "-" + queueName + "-rabbitmq-4-3-6-" + strings.TrimPrefix(ImageManifest, "sha256:")[:12],
		Implementation: queueImplementation.Kind, Lifecycle: queueImplementation.Lifecycle, Rollout: queueImplementation.Rollout,
		CredentialReference: queueImplementation.Credential, RabbitMQVersion: async.Capabilities.RabbitMQVersion,
		ImageIndex: async.Capabilities.RabbitMQImageIndex, ImageManifest: async.Capabilities.RabbitMQImageManifest,
		ImageReference: "docker.io/library/rabbitmq@" + async.Capabilities.RabbitMQImageManifest,
		QueueType:      "quorum", Members: 1, AMQPPort: 25672,
		ServiceUnit: async.Capabilities.RabbitMQServiceUnit, Container: async.Capabilities.RabbitMQContainer,
		Account: async.Capabilities.RabbitMQAccount, DataPath: async.Capabilities.RabbitMQDataPath, QuadletPath: async.Capabilities.RabbitMQQuadletPath,
		Contract: queueComponent.Queue, Observed: async.Deployment.Queue,
	}
	queueOperation := planmodel.Operation{ID: "op-01", Kind: planmodel.PrepareQueue, DependsOn: []string{}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Queue: queueInput}}, Preconditions: conditions("rabbitmq-qualification", QualificationDigest, "matched"), ExpectedObservations: conditions("queue-generation", queueInput.LogicalID, "ready-single-member-quorum"), Recovery: planmodel.RetainQueue}
	if compiled.IsQueueOnly() {
		return PlanningOutput{
			Transitions:              TransitionSet{QueuePreparation: queueOperation},
			SensitiveValueReferences: []string{queueImplementation.Credential},
			QueueOnly:                true,
		}
	}

	workerComponent := compiled.Application.Components[workerName]
	taskComponent := compiled.Application.Components[taskName]
	scheduleComponent := compiled.Application.Components[scheduleName]
	workerImplementation := compiled.Environment.Implementations[workerName]
	taskImplementation := compiled.Environment.Implementations[taskName]
	scheduleImplementation := compiled.Environment.Implementations[scheduleName]
	workerArtifact := compiled.Revision.Artifacts[workerName]
	taskArtifact := compiled.Revision.Artifacts[taskName]
	workerDigestID := strings.TrimPrefix(workerArtifact.Digest, "sha256:")[:12]
	taskDigestID := strings.TrimPrefix(taskArtifact.Digest, "sha256:")[:12]
	workerGenerationID := compiled.Revision.Name + "-" + workerDigestID
	taskGenerationID := compiled.Revision.Name + "-" + taskDigestID
	workerUnit := fmt.Sprintf("provision-%s-%s-%s.service", compiled.Environment.Name, workerName, workerDigestID)
	taskUnit := fmt.Sprintf("provision-%s-%s-%s@.service", compiled.Environment.Name, taskName, taskDigestID)
	timerUnit := fmt.Sprintf("provision-%s-%s.timer", compiled.Environment.Name, scheduleName)
	workerInput := &planmodel.AsyncWorkerInput{
		Component: workerName, Queue: workerComponent.Worker.Queue, QueueLogicalID: queueInput.LogicalID, GenerationID: workerGenerationID,
		Revision: compiled.Revision.Name, ArtifactDigest: workerArtifact.Digest, SystemdUnit: workerUnit,
		Admission: workerImplementation.Worker.Admission, Drain: workerImplementation.Worker.Drain,
		Rollout:  workerImplementation.Rollout,
		Previous: async.Deployment.ActiveWorker,
	}
	workerHandoffInput := &planmodel.AsyncWorkerHandoffInput{
		Worker:            *workerInput,
		QueueGenerationID: queueInput.GenerationID,
		RollbackWindow:    compiled.Environment.RollbackWindow,
	}
	taskInput := &planmodel.AsyncTaskInput{
		Component: taskName, Queue: workerComponent.Worker.Queue, QueueLogicalID: queueInput.LogicalID, GenerationID: taskGenerationID, Revision: compiled.Revision.Name,
		ConfigurationDigest: compiled.Digest,
		ArtifactDigest:      taskArtifact.Digest, SystemdUnit: taskUnit, Timeout: taskComponent.Task.Timeout,
		Rollout:  taskImplementation.Rollout,
		Previous: async.Deployment.ActiveTask,
	}
	scheduleInput := &planmodel.AsyncScheduleInput{
		Component: scheduleName, Task: scheduleComponent.Schedule.Task, TaskGenerationID: taskGenerationID, TaskUnit: taskUnit,
		ApplicationRevision: compiled.Revision.Name, ConfigurationDigest: compiled.Digest,
		TimerUnit: timerUnit, Expression: scheduleComponent.Schedule.Expression, Timezone: scheduleComponent.Schedule.Timezone,
		DaylightSaving: scheduleComponent.Schedule.DaylightSaving, Overlap: scheduleComponent.Schedule.Overlap,
		Retry: scheduleComponent.Schedule.Retry, MissedRun: scheduleComponent.Schedule.MissedRun,
		Failure: scheduleComponent.Schedule.Failure, AppletDigest: async.Capabilities.ScheduleAppletDigest,
		Rollout:      scheduleImplementation.Rollout,
		LedgerSchema: async.Capabilities.ScheduleLedgerSchema, Previous: async.Deployment.Schedule,
	}
	workerArtifactInput := &planmodel.AsyncArtifactInput{Component: workerName, Role: "worker", Source: workerArtifact.Source, Digest: workerArtifact.Digest}
	taskArtifactInput := &planmodel.AsyncArtifactInput{Component: taskName, Role: "task", Source: taskArtifact.Source, Digest: taskArtifact.Digest}
	runtimeInput := &planmodel.AsyncRuntimeInput{AppletDigest: async.Capabilities.ScheduleAppletDigest, LedgerSchema: async.Capabilities.ScheduleLedgerSchema}
	previousSchedule := "none"
	if scheduleInput.Previous != nil {
		previousSchedule = scheduleInput.Previous.TaskGenerationID
	}

	transitions := TransitionSet{
		QueuePreparation: queueOperation,
		WorkerArtifact:   planmodel.Operation{ID: "op-02", Kind: planmodel.StageArtifact, DependsOn: []string{}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Artifact: workerArtifactInput}}, Preconditions: conditions("artifact-digest", workerArtifact.Source, workerArtifact.Digest), ExpectedObservations: conditions("artifact-cache", workerArtifact.Digest, "verified"), Recovery: planmodel.DiscardAsyncArtifact},
		TaskArtifact:     planmodel.Operation{ID: "op-03", Kind: planmodel.StageArtifact, DependsOn: []string{}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Artifact: taskArtifactInput}}, Preconditions: conditions("artifact-digest", taskArtifact.Source, taskArtifact.Digest), ExpectedObservations: conditions("artifact-cache", taskArtifact.Digest, "verified"), Recovery: planmodel.DiscardAsyncArtifact},
		Task: TransitionChain{
			EntryID: "op-04",
			ReadyID: "op-04-verify",
			Operations: []planmodel.Operation{
				{ID: "op-04", Kind: planmodel.InstallTaskGeneration, DependsOn: []string{}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Task: taskInput}}, Preconditions: conditions("artifact-cache", taskArtifact.Digest, "verified"), ExpectedObservations: conditions("task-generation", taskGenerationID, "installed"), Recovery: planmodel.RemoveTaskCandidate},
				{ID: "op-04-verify", Kind: planmodel.VerifyTaskGeneration, DependsOn: []string{"op-04"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Task: taskInput}}, Preconditions: conditions("task-generation", taskGenerationID, "installed"), ExpectedObservations: conditions("task-generation", taskGenerationID, "verified-runnable"), Recovery: planmodel.RemoveTaskCandidate},
			},
		},
		Schedule: ScheduleTransitionChain{
			RuntimeID:    "op-12",
			ActivationID: "op-13",
			ReadyID:      "op-14",
			Operations: []planmodel.Operation{
				{ID: "op-12", Kind: planmodel.InstallScheduleRuntime, DependsOn: []string{}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Runtime: runtimeInput}}, Preconditions: conditions("runtime-asset", runtimeInput.AppletDigest, "verified"), ExpectedObservations: conditions("schedule-runtime", runtimeInput.AppletDigest, "installed"), Recovery: planmodel.RetainQueue},
				{ID: "op-13", Kind: planmodel.HandoffSchedule, DependsOn: []string{"op-12"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Schedule: scheduleInput}}, Preconditions: conditions("previous-schedule-generation", previousSchedule, "fenced-or-absent"), ExpectedObservations: conditions("schedule-task-generation", taskGenerationID, "active-with-new-fence"), Recovery: planmodel.RestorePreviousScheduleFence},
				{ID: "op-14", Kind: planmodel.VerifySchedule, DependsOn: []string{"op-13"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Schedule: scheduleInput}}, Preconditions: conditions("schedule-task-generation", taskGenerationID, "active"), ExpectedObservations: conditions("schedule-occurrence", scheduleName, "recorded-before-invocation"), Recovery: planmodel.RestorePreviousScheduleFence},
			},
		},
	}
	workerOperations := []planmodel.Operation{
		{ID: "op-05", Kind: planmodel.InstallWorkerGeneration, DependsOn: []string{}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("queue-generation", queueInput.LogicalID, "ready"), ExpectedObservations: conditions("worker-generation", workerGenerationID, "installed-gated"), Recovery: planmodel.RemoveWorkerCandidate},
		{ID: "op-06", Kind: planmodel.StartWorkerCandidate, DependsOn: []string{"op-05"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "closed"), ExpectedObservations: conditions("systemd-unit", workerUnit, "active-gated"), Recovery: planmodel.KeepCandidateGated},
		{ID: "op-07", Kind: planmodel.VerifyWorkerCandidate, DependsOn: []string{"op-06"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "closed"), ExpectedObservations: conditions("worker-candidate", workerGenerationID, "gated-queue-connected"), Recovery: planmodel.KeepCandidateGated},
	}
	workerReadyID := "op-11"
	if workerInput.Previous == nil {
		workerOperations = append(workerOperations,
			planmodel.Operation{ID: "op-10", Kind: planmodel.ActivateWorkerIntake, DependsOn: []string{"op-07"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-candidate", workerGenerationID, "verified"), ExpectedObservations: conditions("worker-admission", workerGenerationID, "open"), Recovery: planmodel.KeepCandidateGated},
			planmodel.Operation{ID: "op-11", Kind: planmodel.VerifyWorkerActive, DependsOn: []string{"op-10"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "open"), ExpectedObservations: conditions("worker-intake", workerGenerationID, "open-queue-connected"), Recovery: planmodel.KeepCandidateGated},
		)
	} else {
		fence := planmodel.Operation{ID: "op-08", Kind: planmodel.FenceWorkerIntake, DependsOn: []string{"op-07"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{WorkerHandoff: workerHandoffInput}}, Preconditions: conditions("worker-candidate", workerGenerationID, "verified-gated"), ExpectedObservations: conditions("worker-admission", workerInput.Previous.ID, "closed-durable"), Recovery: planmodel.RestorePreviousWorkerIntake}
		drain := planmodel.Operation{ID: "op-09", Kind: planmodel.DrainWorkerPrevious, DependsOn: []string{"op-08"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{WorkerHandoff: workerHandoffInput}}, Preconditions: conditions("worker-admission", workerInput.Previous.ID, "closed-durable"), ExpectedObservations: conditions("worker-in-flight", workerInput.Previous.ID, "settled-or-safely-released"), Recovery: planmodel.ReleaseInflight}
		drainDigest, _ := planmodel.OperationDigest(drain)
		postDrainInput := *workerHandoffInput
		postDrainInput.DrainOperationDigest = drainDigest
		workerOperations = append(workerOperations, fence, drain,
			planmodel.Operation{ID: "op-10", Kind: planmodel.ActivateWorkerIntake, DependsOn: []string{"op-09"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{WorkerHandoff: &postDrainInput}}, Preconditions: conditions("worker-previous", workerInput.Previous.ID, "drained-or-released"), ExpectedObservations: conditions("worker-admission", workerGenerationID, "open"), Recovery: planmodel.RestorePreviousWorkerIntake},
			planmodel.Operation{ID: "op-11", Kind: planmodel.VerifyWorkerActive, DependsOn: []string{"op-10"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{WorkerHandoff: &postDrainInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "open"), ExpectedObservations: conditions("worker-intake", workerGenerationID, "sole-open-queue-consumer"), Recovery: planmodel.RestorePreviousWorkerIntake},
			planmodel.Operation{ID: "op-15", Kind: planmodel.RetainWorkerPrevious, DependsOn: []string{"op-11"}, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{WorkerHandoff: &postDrainInput}}, Preconditions: conditions("worker-active", workerGenerationID, "verified"), ExpectedObservations: conditions("worker-rollback-generation", workerInput.Previous.ID, "restartable-through-"+string(compiled.Environment.RollbackWindow)), Recovery: planmodel.RetainBothWorkerGenerations},
		)
		workerReadyID = "op-15"
	}
	transitions.Worker = TransitionChain{Operations: workerOperations, EntryID: "op-05", ReadyID: workerReadyID}
	return PlanningOutput{
		Transitions:              transitions,
		SensitiveValueReferences: []string{queueImplementation.Credential},
		QueueOnly:                false,
		WorkerOnly:               workerInput.Previous != nil,
	}
}

func roleNames(compiled config.Compiled) map[string]string {
	roles := make(map[string]string, len(compiled.Application.Components))
	for name, component := range compiled.Application.Components {
		roles[component.Role] = name
	}
	return roles
}

func conditions(kind, subject, expected string) []planmodel.TypedCondition {
	return []planmodel.TypedCondition{{Kind: kind, Subject: subject, Expected: expected}}
}

// DecisionObservation projects the exact Host observation onto the state that
// can affect an asynchronous deployment decision. The complete observation is
// still embedded in capability evidence. Occurrence, invocation, message,
// ledger-content, and instantaneous in-flight telemetry are re-read by the
// operations that use them and do not race a human Plan approval.
func DecisionObservation(observation host.BootstrapStatus) host.BootstrapStatus {
	if observation.Async == nil {
		return observation
	}
	stable := observation
	async := *observation.Async
	deployment := async.Deployment
	deployment.Occurrences = nil
	deployment.Invocations = nil
	deployment.Messages = nil
	if deployment.ActiveWorker != nil {
		worker := *deployment.ActiveWorker
		worker.InFlight = 0
		deployment.ActiveWorker = &worker
	}
	if deployment.Candidate != nil {
		worker := *deployment.Candidate
		worker.InFlight = 0
		deployment.Candidate = &worker
	}
	if deployment.Schedule != nil {
		schedule := *deployment.Schedule
		schedule.LedgerDigest = ""
		deployment.Schedule = &schedule
	}
	async.Deployment = deployment
	stable.Async = &async
	return stable
}
