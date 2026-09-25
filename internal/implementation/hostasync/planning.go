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
}

type TransitionSet struct {
	QueuePreparation    planmodel.Operation
	WorkerArtifact      planmodel.Operation
	TaskArtifact        planmodel.Operation
	TaskInstallation    planmodel.Operation
	TaskVerification    planmodel.Operation
	WorkerInstallation  planmodel.Operation
	WorkerStart         planmodel.Operation
	WorkerVerification  planmodel.Operation
	WorkerFence         *planmodel.Operation
	WorkerDrain         *planmodel.Operation
	WorkerActivation    planmodel.Operation
	WorkerActiveVerify  planmodel.Operation
	ScheduleRuntime     planmodel.Operation
	ScheduleHandoff     planmodel.Operation
	ScheduleVerify      planmodel.Operation
	PreviousWorkerStore *planmodel.Operation
}

// Plan translates the qualified Host/RabbitMQ implementation into typed,
// side-effect-free operations. Physical names and transition policy stay in
// this adapter; the core Planner only embeds the returned canonical sequence.
func Plan(compiled config.Compiled, observation host.BootstrapStatus) PlanningOutput {
	roles := roleNames(compiled)
	async := observation.Async
	queueName, workerName, taskName, scheduleName := roles["queue"], roles["worker"], roles["task"], roles["schedule"]
	queueComponent := compiled.Application.Components[queueName]
	workerComponent := compiled.Application.Components[workerName]
	taskComponent := compiled.Application.Components[taskName]
	scheduleComponent := compiled.Application.Components[scheduleName]
	queueImplementation := compiled.Environment.Implementations[queueName]
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
	taskUnit := fmt.Sprintf("provision-%s-%s-%s.service", compiled.Environment.Name, taskName, taskDigestID)
	timerUnit := fmt.Sprintf("provision-%s-%s.timer", compiled.Environment.Name, scheduleName)

	queueInput := &planmodel.AsyncQueueInput{
		Component: queueName, LogicalID: "provision-" + compiled.Environment.Name + "-" + queueName,
		Implementation: queueImplementation.Kind, Lifecycle: queueImplementation.Lifecycle, Rollout: queueImplementation.Rollout,
		CredentialReference: queueImplementation.Credential, RabbitMQVersion: async.Capabilities.RabbitMQVersion,
		ImageIndex: async.Capabilities.RabbitMQImageIndex, ImageManifest: async.Capabilities.RabbitMQImageManifest, QueueType: "quorum", Members: 1,
		ServiceUnit: async.Capabilities.RabbitMQServiceUnit, Container: async.Capabilities.RabbitMQContainer,
		Account: async.Capabilities.RabbitMQAccount, DataPath: async.Capabilities.RabbitMQDataPath, QuadletPath: async.Capabilities.RabbitMQQuadletPath,
		Contract: queueComponent.Queue, Observed: async.Deployment.Queue,
	}
	workerInput := &planmodel.AsyncWorkerInput{
		Component: workerName, Queue: workerComponent.Worker.Queue, GenerationID: workerGenerationID,
		Revision: compiled.Revision.Name, ArtifactDigest: workerArtifact.Digest, SystemdUnit: workerUnit,
		Admission: workerImplementation.Worker.Admission, Drain: workerImplementation.Worker.Drain,
		Rollout:  workerImplementation.Rollout,
		Previous: async.Deployment.ActiveWorker,
	}
	taskInput := &planmodel.AsyncTaskInput{
		Component: taskName, GenerationID: taskGenerationID, Revision: compiled.Revision.Name,
		ArtifactDigest: taskArtifact.Digest, SystemdUnit: taskUnit, Timeout: taskComponent.Task.Timeout,
		Rollout:  taskImplementation.Rollout,
		Previous: async.Deployment.ActiveTask,
	}
	scheduleInput := &planmodel.AsyncScheduleInput{
		Component: scheduleName, Task: scheduleComponent.Schedule.Task, TaskGenerationID: taskGenerationID,
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
	previousWorker := "none"
	if workerInput.Previous != nil {
		previousWorker = workerInput.Previous.ID
	}
	previousSchedule := "none"
	if scheduleInput.Previous != nil {
		previousSchedule = scheduleInput.Previous.TaskGenerationID
	}

	transitions := TransitionSet{
		QueuePreparation:   planmodel.Operation{Kind: planmodel.PrepareQueue, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Queue: queueInput}}, Preconditions: conditions("rabbitmq-qualification", QualificationDigest, "matched"), ExpectedObservations: conditions("queue-generation", queueInput.LogicalID, "ready-single-member-quorum"), Recovery: planmodel.RetainQueue},
		WorkerArtifact:     planmodel.Operation{Kind: planmodel.StageArtifact, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Artifact: workerArtifactInput}}, Preconditions: conditions("artifact-digest", workerArtifact.Source, workerArtifact.Digest), ExpectedObservations: conditions("artifact-cache", workerArtifact.Digest, "verified"), Recovery: planmodel.DiscardAsyncArtifact},
		TaskArtifact:       planmodel.Operation{Kind: planmodel.StageArtifact, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Artifact: taskArtifactInput}}, Preconditions: conditions("artifact-digest", taskArtifact.Source, taskArtifact.Digest), ExpectedObservations: conditions("artifact-cache", taskArtifact.Digest, "verified"), Recovery: planmodel.DiscardAsyncArtifact},
		TaskInstallation:   planmodel.Operation{Kind: planmodel.InstallTaskGeneration, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Task: taskInput}}, Preconditions: conditions("artifact-cache", taskArtifact.Digest, "verified"), ExpectedObservations: conditions("task-generation", taskGenerationID, "installed"), Recovery: planmodel.RemoveTaskCandidate},
		TaskVerification:   planmodel.Operation{Kind: planmodel.VerifyTaskGeneration, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Task: taskInput}}, Preconditions: conditions("task-generation", taskGenerationID, "installed"), ExpectedObservations: conditions("task-generation", taskGenerationID, "verified-runnable"), Recovery: planmodel.RemoveTaskCandidate},
		WorkerInstallation: planmodel.Operation{Kind: planmodel.InstallWorkerGeneration, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("queue-generation", queueInput.LogicalID, "ready"), ExpectedObservations: conditions("worker-generation", workerGenerationID, "installed-gated"), Recovery: planmodel.RemoveWorkerCandidate},
		WorkerStart:        planmodel.Operation{Kind: planmodel.StartWorkerCandidate, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "closed"), ExpectedObservations: conditions("systemd-unit", workerUnit, "active-gated"), Recovery: planmodel.KeepCandidateGated},
		WorkerVerification: planmodel.Operation{Kind: planmodel.VerifyWorkerCandidate, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "closed"), ExpectedObservations: conditions("worker-candidate", workerGenerationID, "gated-queue-connected"), Recovery: planmodel.KeepCandidateGated},
		WorkerActivation:   planmodel.Operation{Kind: planmodel.ActivateWorkerIntake, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-candidate", workerGenerationID, "verified"), ExpectedObservations: conditions("worker-admission", workerGenerationID, "open"), Recovery: planmodel.RestorePreviousWorkerIntake},
		WorkerActiveVerify: planmodel.Operation{Kind: planmodel.VerifyWorkerActive, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "open"), ExpectedObservations: conditions("worker-processing", workerGenerationID, "verified-at-least-once"), Recovery: planmodel.RestorePreviousWorkerIntake},
		ScheduleRuntime:    planmodel.Operation{Kind: planmodel.InstallScheduleRuntime, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Runtime: runtimeInput}}, Preconditions: conditions("runtime-asset", runtimeInput.AppletDigest, "verified"), ExpectedObservations: conditions("schedule-runtime", runtimeInput.AppletDigest, "installed"), Recovery: planmodel.RetainQueue},
		ScheduleHandoff:    planmodel.Operation{Kind: planmodel.HandoffSchedule, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Schedule: scheduleInput}}, Preconditions: conditions("previous-schedule-generation", previousSchedule, "fenced-or-absent"), ExpectedObservations: conditions("schedule-task-generation", taskGenerationID, "active-with-new-fence"), Recovery: planmodel.RestorePreviousScheduleFence},
		ScheduleVerify:     planmodel.Operation{Kind: planmodel.VerifySchedule, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Schedule: scheduleInput}}, Preconditions: conditions("schedule-task-generation", taskGenerationID, "active"), ExpectedObservations: conditions("schedule-occurrence", scheduleName, "recorded-before-invocation"), Recovery: planmodel.RestorePreviousScheduleFence},
	}
	if workerInput.Previous == nil {
		transitions.WorkerActivation.Recovery = planmodel.KeepCandidateGated
		transitions.WorkerActiveVerify.Recovery = planmodel.KeepCandidateGated
	} else {
		fence := planmodel.Operation{Kind: planmodel.FenceWorkerIntake, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("active-worker", previousWorker, "observed"), ExpectedObservations: conditions("previous-worker-admission", previousWorker, "closed"), Recovery: planmodel.RestorePreviousWorkerIntake}
		drain := planmodel.Operation{Kind: planmodel.DrainWorkerPrevious, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("previous-worker-admission", previousWorker, "closed"), ExpectedObservations: conditions("previous-worker-in-flight", previousWorker, "drained-or-safely-released"), Recovery: planmodel.ReleaseInflight}
		retention := planmodel.Operation{Kind: planmodel.RetainWorkerPrevious, Input: planmodel.OperationInput{Async: &planmodel.AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-generation", workerGenerationID, "verified-active"), ExpectedObservations: conditions("previous-worker-generation", previousWorker, "retained-for-rollback-window"), Recovery: planmodel.RetainBothWorkerGenerations}
		transitions.WorkerFence = &fence
		transitions.WorkerDrain = &drain
		transitions.PreviousWorkerStore = &retention
	}
	return PlanningOutput{
		Transitions:              transitions,
		SensitiveValueReferences: []string{queueImplementation.Credential},
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
