package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"provision/internal/config"
	"provision/internal/drain"
	"provision/internal/host"
	"provision/internal/rollbackwindow"
)

const (
	SchemaVersion        = "provision.dev/plan/v1alpha1"
	PreviewSchemaVersion = "provision.dev/plan-preview/v1alpha1"
)

type Preview struct {
	SchemaVersion string             `json:"schemaVersion"`
	Executable    bool               `json:"executable"`
	Plan          *Plan              `json:"plan,omitempty"`
	Capability    CapabilityEvidence `json:"capability"`
	Reasons       []string           `json:"reasons"`
}

type Plan struct {
	SchemaVersion            string                `json:"schemaVersion"`
	ID                       string                `json:"id"`
	Application              string                `json:"application"`
	Environment              string                `json:"environment"`
	Revision                 string                `json:"revision"`
	ConfigurationDigest      string                `json:"configurationDigest"`
	ArtifactDigests          map[string]string     `json:"artifactDigests"`
	Target                   Target                `json:"target"`
	ObservationDigest        string                `json:"observationDigest"`
	Capability               CapabilityEvidence    `json:"capability"`
	ApprovalRequirements     []ApprovalRequirement `json:"approvalRequirements"`
	SensitiveValueReferences []string              `json:"sensitiveValueReferences"`
	Operations               []Operation           `json:"operations"`
}

type Target struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Local   bool   `json:"local,omitempty"`
	Address string `json:"address"`
	User    string `json:"user"`
}

type CapabilityEvidence struct {
	Contract        string               `json:"contract"`
	RequiredMode    string               `json:"requiredMode"`
	Guarantees      []string             `json:"guarantees"`
	SupportEvidence []string             `json:"supportEvidence"`
	Observed        host.BootstrapStatus `json:"observed"`
	Decision        string               `json:"decision"`
}

type ApprovalRequirement struct {
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type OperationKind string

const (
	StageArtifact           OperationKind = "stageArtifact"
	InstallGeneration       OperationKind = "installGeneration"
	StartCandidate          OperationKind = "startCandidate"
	VerifyCandidate         OperationKind = "verifyCandidate"
	SwitchEndpoint          OperationKind = "switchEndpoint"
	VerifyActive            OperationKind = "verifyActive"
	DrainPrevious           OperationKind = "drainPrevious"
	RetainPrevious          OperationKind = "retainPrevious"
	PrepareQueue            OperationKind = "prepareQueue"
	InstallTaskGeneration   OperationKind = "installTaskGeneration"
	VerifyTaskGeneration    OperationKind = "verifyTaskGeneration"
	InstallWorkerGeneration OperationKind = "installWorkerGeneration"
	StartWorkerCandidate    OperationKind = "startWorkerCandidate"
	VerifyWorkerCandidate   OperationKind = "verifyWorkerCandidate"
	FenceWorkerIntake       OperationKind = "fenceWorkerIntake"
	DrainWorkerPrevious     OperationKind = "drainWorkerPrevious"
	ActivateWorkerIntake    OperationKind = "activateWorkerIntake"
	VerifyWorkerActive      OperationKind = "verifyWorkerActive"
	InstallScheduleRuntime  OperationKind = "installScheduleRuntime"
	HandoffSchedule         OperationKind = "handoffSchedule"
	VerifySchedule          OperationKind = "verifySchedule"
	RetainWorkerPrevious    OperationKind = "retainWorkerPrevious"
)

type RecoveryMode string

const (
	DiscardStaged                RecoveryMode = "discard-staged"
	RemoveCandidate              RecoveryMode = "remove-candidate"
	StopCandidate                RecoveryMode = "stop-candidate"
	LeaveEndpointUnchanged       RecoveryMode = "leave-endpoint-unchanged"
	RestorePreviousRoute         RecoveryMode = "restore-previous-route"
	RetainBothGenerations        RecoveryMode = "retain-both-generations"
	RetainQueue                  RecoveryMode = "retain-queue"
	DiscardAsyncArtifact         RecoveryMode = "discard-async-artifact"
	RemoveTaskCandidate          RecoveryMode = "remove-task-candidate"
	RemoveWorkerCandidate        RecoveryMode = "remove-worker-candidate"
	KeepCandidateGated           RecoveryMode = "keep-candidate-gated"
	RestorePreviousWorkerIntake  RecoveryMode = "restore-previous-worker-intake"
	ReleaseInflight              RecoveryMode = "release-in-flight"
	RestorePreviousScheduleFence RecoveryMode = "restore-previous-schedule-fence"
	RetainBothWorkerGenerations  RecoveryMode = "retain-both-worker-generations"
)

type Operation struct {
	ID                   string           `json:"id"`
	Kind                 OperationKind    `json:"kind"`
	DependsOn            []string         `json:"dependsOn"`
	Input                OperationInput   `json:"input"`
	Preconditions        []TypedCondition `json:"preconditions"`
	ExpectedObservations []TypedCondition `json:"expectedObservations"`
	Recovery             RecoveryMode     `json:"recovery"`
}

type TypedCondition struct {
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	Expected string `json:"expected"`
}

type OperationInput struct {
	Artifact   *ArtifactInput         `json:"artifact,omitempty"`
	Generation *GenerationInput       `json:"generation,omitempty"`
	Systemd    *SystemdInput          `json:"systemd,omitempty"`
	Health     *HealthInput           `json:"health,omitempty"`
	Endpoint   *EndpointInput         `json:"endpoint,omitempty"`
	Previous   *host.GenerationStatus `json:"previous,omitempty"`
	Drain      *DrainInput            `json:"drain,omitempty"`
	Retention  *RetentionInput        `json:"retention,omitempty"`
	Async      *AsyncOperationInput   `json:"async,omitempty"`
}

type AsyncOperationInput struct {
	Queue    *AsyncQueueInput    `json:"queue,omitempty"`
	Artifact *AsyncArtifactInput `json:"artifact,omitempty"`
	Worker   *AsyncWorkerInput   `json:"worker,omitempty"`
	Task     *AsyncTaskInput     `json:"task,omitempty"`
	Schedule *AsyncScheduleInput `json:"schedule,omitempty"`
	Runtime  *AsyncRuntimeInput  `json:"runtime,omitempty"`
}

type AsyncQueueInput struct {
	Component           string               `json:"component"`
	LogicalID           string               `json:"logicalId"`
	Implementation      string               `json:"implementation"`
	Lifecycle           string               `json:"lifecycle"`
	Rollout             string               `json:"rollout"`
	CredentialReference string               `json:"credentialReference"`
	RabbitMQVersion     string               `json:"rabbitmqVersion"`
	ImageIndex          string               `json:"imageIndex"`
	ImageManifest       string               `json:"imageManifest"`
	ServiceUnit         string               `json:"serviceUnit"`
	Container           string               `json:"container"`
	Account             string               `json:"account"`
	DataPath            string               `json:"dataPath"`
	QuadletPath         string               `json:"quadletPath"`
	QueueType           string               `json:"queueType"`
	Members             int                  `json:"members"`
	Contract            config.QueueContract `json:"contract"`
	Observed            *host.QueueStatus    `json:"observed,omitempty"`
}

type AsyncArtifactInput struct {
	Component string `json:"component"`
	Role      string `json:"role"`
	Source    string `json:"source"`
	Digest    string `json:"digest"`
}

type AsyncWorkerInput struct {
	Component      string                       `json:"component"`
	Queue          string                       `json:"queue"`
	GenerationID   string                       `json:"generationId"`
	Revision       string                       `json:"revision"`
	ArtifactDigest string                       `json:"artifactDigest"`
	SystemdUnit    string                       `json:"systemdUnit"`
	Admission      string                       `json:"admission"`
	Drain          config.WorkerDrain           `json:"drain"`
	Rollout        string                       `json:"rollout"`
	Previous       *host.WorkerGenerationStatus `json:"previous,omitempty"`
}

type AsyncTaskInput struct {
	Component      string                     `json:"component"`
	GenerationID   string                     `json:"generationId"`
	Revision       string                     `json:"revision"`
	ArtifactDigest string                     `json:"artifactDigest"`
	SystemdUnit    string                     `json:"systemdUnit"`
	Timeout        string                     `json:"timeout"`
	Rollout        string                     `json:"rollout"`
	Previous       *host.TaskGenerationStatus `json:"previous,omitempty"`
}

type AsyncScheduleInput struct {
	Component        string                   `json:"component"`
	Task             string                   `json:"task"`
	TaskGenerationID string                   `json:"taskGenerationId"`
	TimerUnit        string                   `json:"timerUnit"`
	Expression       string                   `json:"expression"`
	Timezone         string                   `json:"timezone"`
	DaylightSaving   string                   `json:"daylightSaving"`
	Overlap          string                   `json:"overlap"`
	Retry            config.ScheduleRetry     `json:"retry"`
	MissedRun        config.ScheduleMissedRun `json:"missedRun"`
	Failure          string                   `json:"failure"`
	Rollout          string                   `json:"rollout"`
	AppletDigest     string                   `json:"appletDigest"`
	LedgerSchema     string                   `json:"ledgerSchema"`
	Previous         *host.ScheduleStatus     `json:"previous,omitempty"`
}

type AsyncRuntimeInput struct {
	AppletDigest string `json:"appletDigest"`
	LedgerSchema string `json:"ledgerSchema"`
}

type ArtifactInput struct {
	Source string `json:"source"`
	Digest string `json:"digest"`
}

type GenerationReference struct {
	ID               string `json:"id"`
	Revision         string `json:"revision"`
	ArtifactDigest   string `json:"artifactDigest"`
	Account          string `json:"account"`
	ReleaseDirectory string `json:"releaseDirectory"`
}

type GenerationInput struct {
	GenerationReference
}

type SystemdInput struct {
	GenerationReference
	Unit string `json:"unit"`
	Port int    `json:"port"`
}

type HealthInput struct {
	GenerationReference
	Unit                string `json:"unit"`
	LivenessPath        string `json:"livenessPath"`
	ReadinessPath       string `json:"readinessPath"`
	CandidateVerifyPath string `json:"candidateVerificationPath"`
	Port                int    `json:"port"`
}

type EndpointInput struct {
	GenerationReference
	Unit         string `json:"unit"`
	RouteID      string `json:"routeId"`
	ListenPort   int    `json:"listenPort"`
	Upstream     string `json:"upstream"`
	UpstreamPort int    `json:"upstreamPort"`
	DrainPolicy  string `json:"drainPolicy"`
}

type DrainInput struct {
	Endpoint    EndpointInput         `json:"endpoint"`
	Previous    host.GenerationStatus `json:"previous"`
	Mode        drain.Mode            `json:"mode"`
	MaxDuration drain.Bound           `json:"maxDuration"`
}

type RetentionInput struct {
	Endpoint       EndpointInput         `json:"endpoint"`
	Previous       host.GenerationStatus `json:"previous"`
	Policy         rollbackwindow.Rule   `json:"policy"`
	RollbackWindow rollbackwindow.Window `json:"rollbackWindow"`
}

// Build converts validated configuration and a restricted Host Target
// inspection into canonical preview data. It never changes or persists state.
func Build(compiled config.Compiled, observation host.BootstrapStatus) (Preview, error) {
	if compiled.IsAsync() {
		return buildAsync(compiled, observation)
	}
	selection, err := compiled.HostSelection()
	if err != nil {
		return Preview{}, err
	}
	if observation.Environment != compiled.Environment.Name || observation.Operator != selection.Target.User {
		return Preview{}, errors.New("host observation does not identify the configured Environment and operator")
	}

	evidence := CapabilityEvidence{
		Contract:     "host-systemd-caddy-http/v1alpha1",
		RequiredMode: selection.Implementation.Rollout,
		Guarantees: []string{
			"separate-candidate-generation",
			"candidate-health-gate",
			"atomic-caddy-route-load",
			"in-flight-http-request-drain",
			"previous-generation-retention",
			"post-switch-health-verification",
			"automatic-previous-route-rollback",
		},
		SupportEvidence: []string{
			"restricted-bootstrap-ready",
			"tested-product-version-match",
			"caddy-active",
			"caddy-config-valid",
			"caddy-admin-reachable",
			"caddy-graceful-config-reload",
			"caddy-autosave-resume",
			"generation-storage-ready",
			"candidate-port-free",
			"journald-active",
			"restricted-executor-identity-matched",
		},
		Observed: observation,
		Decision: "required-blue-green-supported",
	}
	reasons := capabilityIssues(observation, selection)
	preview := Preview{
		SchemaVersion: PreviewSchemaVersion,
		Executable:    len(reasons) == 0,
		Capability:    evidence,
		Reasons:       reasons,
	}
	if len(reasons) != 0 {
		preview.Capability.Decision = "unsupported"
		return preview, nil
	}

	observationDigest, err := digest(observation)
	if err != nil {
		return Preview{}, err
	}
	plan := Plan{
		SchemaVersion:       SchemaVersion,
		Application:         compiled.Application.Name,
		Environment:         compiled.Environment.Name,
		Revision:            compiled.Revision.Name,
		ConfigurationDigest: compiled.Digest,
		ArtifactDigests:     map[string]string{selection.Component: selection.Artifact.Digest},
		Target: Target{
			Name:    selection.TargetName,
			Kind:    selection.Target.Kind,
			Local:   selection.Target.Local,
			Address: selection.Target.Address,
			User:    selection.Target.User,
		},
		ObservationDigest: observationDigest,
		Capability:        evidence,
		ApprovalRequirements: []ApprovalRequirement{{
			Capability: "approve",
			Reason:     "environment deployment policy requires approval of this exact Plan",
		}},
		SensitiveValueReferences: []string{},
		Operations:               httpOperations(compiled, selection, observation, observationDigest),
	}
	plan.ID, err = digest(plan)
	if err != nil {
		return Preview{}, err
	}
	preview.Plan = &plan
	return preview, nil
}

const (
	rabbitMQQualificationDigest = "sha256:af41714b1aa2270ba6cd151bd24876ac117218e401e1e87515451a7081ac4c6d"
	rabbitMQImageIndex          = "sha256:d0bffe70e755f348625415f32b0a090662e5f06b3ba3f82a4c7aaa18621b1279"
	rabbitMQImageManifest       = "sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91"
)

func buildAsync(compiled config.Compiled, observation host.BootstrapStatus) (Preview, error) {
	target, err := compiled.PlanningTarget()
	if err != nil {
		return Preview{}, err
	}
	if observation.Environment != compiled.Environment.Name || observation.Operator != target.Target.User {
		return Preview{}, errors.New("host observation does not identify the configured Environment and operator")
	}
	evidence := CapabilityEvidence{
		Contract:     "host-rabbitmq-systemd-async/v1alpha1",
		RequiredMode: "required",
		Guarantees: []string{
			"digest-pinned-rabbitmq-quorum-queue", "publisher-confirmed-at-least-once-delivery",
			"manual-consumer-acknowledgement", "gated-worker-candidate", "bounded-in-flight-worker-drain",
			"generation-specific-task", "stable-schedule-timer", "fenced-schedule-handoff",
			"previous-worker-generation-retention",
		},
		SupportEvidence: []string{
			"restricted-bootstrap-ready", "tested-host-version-match", "qualified-rabbitmq-packaging",
			"rootless-quadlet-ready", "systemd-credentials-ready", "worker-admission-control-proven",
			"pinned-schedule-applet", "versioned-occurrence-ledger", "journald-active",
			"restricted-executor-identity-matched",
		},
		Observed: observation,
		Decision: "required-blue-green-supported",
	}
	reasons := asyncCapabilityIssues(observation, target)
	preview := Preview{SchemaVersion: PreviewSchemaVersion, Executable: len(reasons) == 0, Capability: evidence, Reasons: reasons}
	if len(reasons) != 0 {
		preview.Capability.Decision = "unsupported"
		return preview, nil
	}

	observationDigest, err := digest(observation)
	if err != nil {
		return Preview{}, err
	}
	artifactDigests := make(map[string]string, len(compiled.Revision.Artifacts))
	for name, artifact := range compiled.Revision.Artifacts {
		artifactDigests[name] = artifact.Digest
	}
	roles := asyncRoleNames(compiled)
	queueImplementation := compiled.Environment.Implementations[roles["queue"]]
	plan := Plan{
		SchemaVersion:       SchemaVersion,
		Application:         compiled.Application.Name,
		Environment:         compiled.Environment.Name,
		Revision:            compiled.Revision.Name,
		ConfigurationDigest: compiled.Digest,
		ArtifactDigests:     artifactDigests,
		Target:              Target{Name: target.Name, Kind: target.Target.Kind, Local: target.Target.Local, Address: target.Target.Address, User: target.Target.User},
		ObservationDigest:   observationDigest,
		Capability:          evidence,
		ApprovalRequirements: []ApprovalRequirement{{
			Capability: "approve", Reason: "environment deployment policy requires approval of this exact Plan",
		}},
		SensitiveValueReferences: []string{queueImplementation.Credential},
		Operations:               asyncOperations(compiled, observation, roles),
	}
	plan.ID, err = digest(plan)
	if err != nil {
		return Preview{}, err
	}
	preview.Plan = &plan
	return preview, nil
}

func asyncCapabilityIssues(observation host.BootstrapStatus, target config.TargetSelection) []string {
	issues := append([]string{}, observation.Findings...)
	if observation.SchemaVersion != "provision.dev/host-inspection/v1alpha1" {
		issues = append(issues, "host inspection schema does not provide asynchronous capability evidence")
	}
	if !observation.Ready {
		issues = append(issues, "restricted Host Target bootstrap is not ready")
	}
	if observation.OS != "ubuntu" || observation.OSVersion != "26.04" {
		issues = append(issues, fmt.Sprintf("Ubuntu version %s has no tested asynchronous evidence", observedOrUnknown(observation.OSVersion)))
	}
	if observation.Architecture != "x86_64" {
		issues = append(issues, fmt.Sprintf("architecture %s has no tested asynchronous evidence", observedOrUnknown(observation.Architecture)))
	}
	if !strings.HasPrefix(observation.SystemdVersion, "systemd 259") || !observation.CgroupV2 || !observation.JournaldActive || !observation.GenerationStorageReady {
		issues = append(issues, "Host systemd, cgroup v2, journald, or generation storage evidence is incomplete")
	}
	if observation.ExecutorDigest == "" || observation.AuthorityKeyID == "" {
		issues = append(issues, "restricted host executor or authority identity is not observed")
	}
	if !target.Target.Local && observation.SSHHostKeyFingerprint == "" {
		issues = append(issues, "SSH host key identity is not observed")
	}
	if observation.Async == nil || observation.Async.SchemaVersion != "provision.dev/host-async-inspection/v1alpha1" {
		return unique(append(issues, "Host observation has no supported asynchronous capability evidence"))
	}
	issues = append(issues, observation.Async.Findings...)
	observedDeployment := observation.Async.Deployment
	completeDeployment := observation.Async.ObservationComplete || observedDeployment.Queue != nil && observedDeployment.ActiveTask != nil && observedDeployment.Schedule != nil
	if !completeDeployment {
		issues = append(issues, "asynchronous deployment observation is incomplete")
	}
	capability := observation.Async.Capabilities
	if capability.PodmanVersion != "5.7.0+ds2-3build1" || !capability.Quadlet || !capability.RootlessEnvironmentAccount {
		issues = append(issues, "qualified rootless Podman and Quadlet capability is not observed")
	}
	if !capability.SystemdCredentials {
		issues = append(issues, "encrypted systemd credential delivery is not observed")
	}
	detailedPackaging := capability.SubordinateIDs && capability.LingeringUserManager && capability.QuadletDefinitionRootOwned && capability.DataPathEnvironmentOwned && capability.EncryptedCredentialObserved
	if !detailedPackaging && observedDeployment.Queue == nil {
		issues = append(issues, "rootless Queue account, subordinate IDs, lingering manager, owned paths, or encrypted credential evidence is incomplete")
	}
	if !capability.WorkerAdmissionGate {
		issues = append(issues, "required Worker blue-green is unsupported without proven admission control")
	}
	if capability.RabbitMQQualificationDigest != rabbitMQQualificationDigest || capability.RabbitMQVersion != "4.3.6" || capability.RabbitMQImageIndex != rabbitMQImageIndex || capability.RabbitMQImageManifest != rabbitMQImageManifest {
		issues = append(issues, "RabbitMQ product identity does not match the qualified packaging evidence")
	}
	expectedService := "provision-" + observation.Environment + "-rabbitmq"
	if capability.RabbitMQServiceUnit != expectedService+".service" || capability.RabbitMQContainer != expectedService || capability.RabbitMQAccount != "provision-"+observation.Environment || capability.RabbitMQDataPath != "/var/lib/provision/environments/"+observation.Environment+"/services/rabbitmq/data" || capability.RabbitMQQuadletPath == "" {
		issues = append(issues, "RabbitMQ service identity or owned paths do not match the Environment")
	}
	if !strings.HasPrefix(capability.ScheduleAppletDigest, "sha256:") || len(capability.ScheduleAppletDigest) != 71 {
		issues = append(issues, "pinned Schedule runtime applet identity is not observed")
	}
	if capability.ScheduleLedgerSchema != "provision.dev/schedule-ledger/v1alpha1" {
		issues = append(issues, "Schedule occurrence-ledger schema is unsupported")
	}
	if queue := observation.Async.Deployment.Queue; queue != nil && queue.Exists {
		if !queue.Ready || queue.QueueType != "quorum" || queue.Members != 1 || !queue.Durable || queue.ImageManifest != rabbitMQImageManifest || queue.ServiceUnit != capability.RabbitMQServiceUnit || queue.Container != capability.RabbitMQContainer || queue.Account != capability.RabbitMQAccount || queue.DataPath != capability.RabbitMQDataPath || queue.QuadletPath != capability.RabbitMQQuadletPath {
			issues = append(issues, "observed Queue does not match the qualified single-member quorum generation")
		}
	}
	if worker := observation.Async.Deployment.ActiveWorker; worker != nil {
		if worker.ID == "" || worker.ArtifactDigest == "" || !worker.UnitActive || !worker.QueueConnected || worker.Gate != "open" || worker.InFlight < 0 {
			issues = append(issues, "active Worker generation does not match observed deployment state")
		}
	}
	if task := observation.Async.Deployment.ActiveTask; task != nil {
		if task.ID == "" || task.Revision == "" || !strings.HasPrefix(task.ArtifactDigest, "sha256:") || task.SystemdUnit == "" {
			issues = append(issues, "active Task generation does not match observed deployment state")
		}
	}
	if schedule := observation.Async.Deployment.Schedule; schedule != nil {
		if schedule.TimerUnit == "" || schedule.TaskGenerationID == "" || !strings.HasPrefix(schedule.AppletDigest, "sha256:") || schedule.LedgerSchema != "provision.dev/schedule-ledger/v1alpha1" || !strings.HasPrefix(schedule.LedgerDigest, "sha256:") || schedule.FencingToken < 1 || observation.Async.Deployment.ActiveTask == nil || schedule.TaskGenerationID != observation.Async.Deployment.ActiveTask.ID {
			issues = append(issues, "active Schedule and Task generation do not match observed fenced runtime state")
		}
	}
	return unique(issues)
}

func asyncRoleNames(compiled config.Compiled) map[string]string {
	roles := make(map[string]string, len(compiled.Application.Components))
	for name, component := range compiled.Application.Components {
		roles[component.Role] = name
	}
	return roles
}

func asyncOperations(compiled config.Compiled, observation host.BootstrapStatus, roles map[string]string) []Operation {
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
	queueInput := &AsyncQueueInput{
		Component: queueName, LogicalID: "provision-" + compiled.Environment.Name + "-" + queueName,
		Implementation: queueImplementation.Kind, Lifecycle: queueImplementation.Lifecycle, Rollout: queueImplementation.Rollout,
		CredentialReference: queueImplementation.Credential, RabbitMQVersion: async.Capabilities.RabbitMQVersion,
		ImageIndex: async.Capabilities.RabbitMQImageIndex, ImageManifest: async.Capabilities.RabbitMQImageManifest, QueueType: "quorum", Members: 1,
		ServiceUnit: async.Capabilities.RabbitMQServiceUnit, Container: async.Capabilities.RabbitMQContainer,
		Account: async.Capabilities.RabbitMQAccount, DataPath: async.Capabilities.RabbitMQDataPath, QuadletPath: async.Capabilities.RabbitMQQuadletPath,
		Contract: queueComponent.Queue, Observed: async.Deployment.Queue,
	}
	workerInput := &AsyncWorkerInput{
		Component: workerName, Queue: workerComponent.Worker.Queue, GenerationID: workerGenerationID,
		Revision: compiled.Revision.Name, ArtifactDigest: workerArtifact.Digest, SystemdUnit: workerUnit,
		Admission: workerImplementation.Worker.Admission, Drain: workerImplementation.Worker.Drain,
		Rollout:  workerImplementation.Rollout,
		Previous: async.Deployment.ActiveWorker,
	}
	taskInput := &AsyncTaskInput{
		Component: taskName, GenerationID: taskGenerationID, Revision: compiled.Revision.Name,
		ArtifactDigest: taskArtifact.Digest, SystemdUnit: taskUnit, Timeout: taskComponent.Task.Timeout,
		Rollout:  taskImplementation.Rollout,
		Previous: async.Deployment.ActiveTask,
	}
	scheduleInput := &AsyncScheduleInput{
		Component: scheduleName, Task: scheduleComponent.Schedule.Task, TaskGenerationID: taskGenerationID,
		TimerUnit: timerUnit, Expression: scheduleComponent.Schedule.Expression, Timezone: scheduleComponent.Schedule.Timezone,
		DaylightSaving: scheduleComponent.Schedule.DaylightSaving, Overlap: scheduleComponent.Schedule.Overlap,
		Retry: scheduleComponent.Schedule.Retry, MissedRun: scheduleComponent.Schedule.MissedRun,
		Failure: scheduleComponent.Schedule.Failure, AppletDigest: async.Capabilities.ScheduleAppletDigest,
		Rollout:      scheduleImplementation.Rollout,
		LedgerSchema: async.Capabilities.ScheduleLedgerSchema, Previous: async.Deployment.Schedule,
	}
	workerArtifactInput := &AsyncArtifactInput{Component: workerName, Role: "worker", Source: workerArtifact.Source, Digest: workerArtifact.Digest}
	taskArtifactInput := &AsyncArtifactInput{Component: taskName, Role: "task", Source: taskArtifact.Source, Digest: taskArtifact.Digest}
	runtimeInput := &AsyncRuntimeInput{AppletDigest: async.Capabilities.ScheduleAppletDigest, LedgerSchema: async.Capabilities.ScheduleLedgerSchema}
	previousWorker := "none"
	if workerInput.Previous != nil {
		previousWorker = workerInput.Previous.ID
	}
	previousSchedule := "none"
	if scheduleInput.Previous != nil {
		previousSchedule = scheduleInput.Previous.TaskGenerationID
	}
	operations := []Operation{
		{ID: "op-01", Kind: PrepareQueue, DependsOn: []string{}, Input: OperationInput{Async: &AsyncOperationInput{Queue: queueInput}}, Preconditions: conditions("rabbitmq-qualification", rabbitMQQualificationDigest, "matched"), ExpectedObservations: conditions("queue-generation", queueInput.LogicalID, "ready-single-member-quorum"), Recovery: RetainQueue},
		{ID: "op-02", Kind: StageArtifact, DependsOn: []string{"op-01"}, Input: OperationInput{Async: &AsyncOperationInput{Artifact: workerArtifactInput}}, Preconditions: conditions("artifact-digest", workerArtifact.Source, workerArtifact.Digest), ExpectedObservations: conditions("artifact-cache", workerArtifact.Digest, "verified"), Recovery: DiscardAsyncArtifact},
		{ID: "op-03", Kind: StageArtifact, DependsOn: []string{"op-01"}, Input: OperationInput{Async: &AsyncOperationInput{Artifact: taskArtifactInput}}, Preconditions: conditions("artifact-digest", taskArtifact.Source, taskArtifact.Digest), ExpectedObservations: conditions("artifact-cache", taskArtifact.Digest, "verified"), Recovery: DiscardAsyncArtifact},
		{ID: "op-04", Kind: InstallTaskGeneration, DependsOn: []string{"op-03"}, Input: OperationInput{Async: &AsyncOperationInput{Task: taskInput}}, Preconditions: conditions("artifact-cache", taskArtifact.Digest, "verified"), ExpectedObservations: conditions("task-generation", taskGenerationID, "installed"), Recovery: RemoveTaskCandidate},
		{ID: "op-04-verify", Kind: VerifyTaskGeneration, DependsOn: []string{"op-04"}, Input: OperationInput{Async: &AsyncOperationInput{Task: taskInput}}, Preconditions: conditions("task-generation", taskGenerationID, "installed"), ExpectedObservations: conditions("task-generation", taskGenerationID, "verified-runnable"), Recovery: RemoveTaskCandidate},
		{ID: "op-05", Kind: InstallWorkerGeneration, DependsOn: []string{"op-02", "op-01"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("queue-generation", queueInput.LogicalID, "ready"), ExpectedObservations: conditions("worker-generation", workerGenerationID, "installed-gated"), Recovery: RemoveWorkerCandidate},
		{ID: "op-06", Kind: StartWorkerCandidate, DependsOn: []string{"op-05"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "closed"), ExpectedObservations: conditions("systemd-unit", workerUnit, "active-gated"), Recovery: KeepCandidateGated},
		{ID: "op-07", Kind: VerifyWorkerCandidate, DependsOn: []string{"op-06"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "closed"), ExpectedObservations: conditions("worker-candidate", workerGenerationID, "gated-queue-connected"), Recovery: KeepCandidateGated},
		{ID: "op-08", Kind: FenceWorkerIntake, DependsOn: []string{"op-07"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("active-worker", previousWorker, "observed"), ExpectedObservations: conditions("previous-worker-admission", previousWorker, "closed"), Recovery: RestorePreviousWorkerIntake},
		{ID: "op-09", Kind: DrainWorkerPrevious, DependsOn: []string{"op-08"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("previous-worker-admission", previousWorker, "closed"), ExpectedObservations: conditions("previous-worker-in-flight", previousWorker, "drained-or-safely-released"), Recovery: ReleaseInflight},
		{ID: "op-10", Kind: ActivateWorkerIntake, DependsOn: []string{"op-09"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-candidate", workerGenerationID, "verified"), ExpectedObservations: conditions("worker-admission", workerGenerationID, "open"), Recovery: RestorePreviousWorkerIntake},
		{ID: "op-11", Kind: VerifyWorkerActive, DependsOn: []string{"op-10"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-admission", workerGenerationID, "open"), ExpectedObservations: conditions("worker-processing", workerGenerationID, "verified-at-least-once"), Recovery: RestorePreviousWorkerIntake},
		{ID: "op-12", Kind: InstallScheduleRuntime, DependsOn: []string{"op-04-verify"}, Input: OperationInput{Async: &AsyncOperationInput{Runtime: runtimeInput}}, Preconditions: conditions("runtime-asset", runtimeInput.AppletDigest, "verified"), ExpectedObservations: conditions("schedule-runtime", runtimeInput.AppletDigest, "installed"), Recovery: RetainQueue},
		{ID: "op-13", Kind: HandoffSchedule, DependsOn: []string{"op-11", "op-12", "op-04-verify"}, Input: OperationInput{Async: &AsyncOperationInput{Schedule: scheduleInput}}, Preconditions: conditions("previous-schedule-generation", previousSchedule, "fenced-or-absent"), ExpectedObservations: conditions("schedule-task-generation", taskGenerationID, "active-with-new-fence"), Recovery: RestorePreviousScheduleFence},
		{ID: "op-14", Kind: VerifySchedule, DependsOn: []string{"op-13"}, Input: OperationInput{Async: &AsyncOperationInput{Schedule: scheduleInput}}, Preconditions: conditions("schedule-task-generation", taskGenerationID, "active"), ExpectedObservations: conditions("schedule-occurrence", scheduleName, "recorded-before-invocation"), Recovery: RestorePreviousScheduleFence},
		{ID: "op-15", Kind: RetainWorkerPrevious, DependsOn: []string{"op-11", "op-14"}, Input: OperationInput{Async: &AsyncOperationInput{Worker: workerInput}}, Preconditions: conditions("worker-generation", workerGenerationID, "verified-active"), ExpectedObservations: conditions("previous-worker-generation", previousWorker, "retained-for-rollback-window"), Recovery: RetainBothWorkerGenerations},
	}
	if workerInput.Previous == nil {
		filtered := make([]Operation, 0, len(operations)-3)
		for _, operation := range operations {
			switch operation.Kind {
			case FenceWorkerIntake, DrainWorkerPrevious, RetainWorkerPrevious:
				continue
			case ActivateWorkerIntake:
				operation.DependsOn = []string{"op-07"}
				operation.Recovery = KeepCandidateGated
			case VerifyWorkerActive:
				operation.Recovery = KeepCandidateGated
			}
			filtered = append(filtered, operation)
		}
		operations = filtered
	}
	return operations
}

func capabilityIssues(observation host.BootstrapStatus, selection config.HostSelection) []string {
	issues := append([]string{}, observation.Findings...)
	if observation.SchemaVersion != "provision.dev/host-inspection/v1alpha1" {
		issues = append(issues, "host inspection schema does not provide deployment capability evidence")
	}
	if !observation.Ready {
		issues = append(issues, "restricted Host Target bootstrap is not ready")
	}
	if observation.OS != "ubuntu" || observation.OSVersion != "26.04" {
		issues = append(issues, fmt.Sprintf("Ubuntu version %s has no tested required blue-green evidence", observedOrUnknown(observation.OSVersion)))
	}
	if observation.Architecture != "x86_64" {
		issues = append(issues, fmt.Sprintf("architecture %s has no tested required blue-green evidence", observedOrUnknown(observation.Architecture)))
	}
	if !strings.HasPrefix(observation.SystemdVersion, "systemd 259") {
		issues = append(issues, fmt.Sprintf("systemd version %s has no tested required blue-green evidence", observedOrUnknown(observation.SystemdVersion)))
	}
	if observation.CaddyVersion != "2.6.2" {
		issues = append(issues, fmt.Sprintf("Caddy version %s has no tested required blue-green evidence", observedOrUnknown(observation.CaddyVersion)))
	}
	if !observation.CaddyActive {
		issues = append(issues, "Caddy is not active")
	}
	if !observation.CaddyConfigValid {
		issues = append(issues, "Caddy configuration is not valid")
	}
	if !observation.CaddyAdminReachable {
		issues = append(issues, "Caddy admin endpoint is not reachable for atomic configuration loads")
	}
	if !observation.CaddyConfigDurable {
		issues = append(issues, "Caddy is not configured to resume its autosaved active configuration")
	}
	if !observation.GenerationStorageReady {
		issues = append(issues, "generation storage is not ready")
	}
	if !observation.JournaldActive {
		issues = append(issues, "journald is not active")
	}
	if observation.ExecutorDigest == "" {
		issues = append(issues, "restricted host executor identity is not observed")
	}
	if observation.AuthorityKeyID == "" {
		issues = append(issues, "host authorization authority is not observed")
	}
	if !selection.Target.Local && observation.SSHHostKeyFingerprint == "" {
		issues = append(issues, "SSH host key identity is not observed")
	}
	for _, operation := range []string{"stageArtifact", "installGeneration", "startCandidate", "verifyCandidate", "switchEndpoint", "verifyActive", "drainPrevious", "retainPrevious"} {
		if !slices.Contains(observation.AllowedOperations, operation) {
			issues = append(issues, fmt.Sprintf("host executor does not allow typed %s operations", operation))
		}
	}
	if active := observation.Deployment.Active; active != nil && (active.ArtifactDigest == "" || !active.UnitActive || !active.UnitMatches || !active.RouteObserved || !active.RouteMatches) {
		issues = append(issues, "active generation and stable Caddy route do not match observed deployment state")
	}
	candidatePort := generationPort(selection.Artifact.Digest)
	if candidatePort == selection.Implementation.Endpoint.Port {
		issues = append(issues, "candidate port collides with the stable Endpoint port")
	}
	if slices.Contains(observation.ListeningTCPPorts, candidatePort) {
		issues = append(issues, fmt.Sprintf("candidate port %d is already listening", candidatePort))
	}
	return unique(issues)
}

func httpOperations(compiled config.Compiled, selection config.HostSelection, observation host.BootstrapStatus, observationDigest string) []Operation {
	digestID := strings.TrimPrefix(selection.Artifact.Digest, "sha256:")[:12]
	generationID := compiled.Revision.Name + "-" + digestID
	account := "provision-" + compiled.Environment.Name
	releaseDirectory := filepath.Join("/var/lib/provision/environments", compiled.Environment.Name, "releases", generationID)
	unit := fmt.Sprintf("provision-%s-%s-%s.service", compiled.Environment.Name, selection.Component, digestID)
	routeID := fmt.Sprintf("provision-%s-%s", compiled.Environment.Name, selection.Component)
	listenPort := selection.Implementation.Endpoint.Port
	candidatePort := generationPort(selection.Artifact.Digest)
	component := compiled.Application.Components[selection.Component]
	generation := GenerationReference{ID: generationID, Revision: compiled.Revision.Name, ArtifactDigest: selection.Artifact.Digest, Account: account, ReleaseDirectory: releaseDirectory}
	endpoint := EndpointInput{GenerationReference: generation, Unit: unit, RouteID: routeID, ListenPort: listenPort, Upstream: fmt.Sprintf("127.0.0.1:%d", candidatePort), UpstreamPort: candidatePort, DrainPolicy: "caddy-graceful-config-reload"}

	operations := []Operation{
		{ID: "op-01", Kind: StageArtifact, DependsOn: []string{}, Input: OperationInput{Artifact: &ArtifactInput{Source: selection.Artifact.Source, Digest: selection.Artifact.Digest}}, Preconditions: conditions("artifact-digest", selection.Artifact.Source, selection.Artifact.Digest), ExpectedObservations: conditions("artifact-cache", selection.Artifact.Digest, "verified"), Recovery: DiscardStaged},
		{ID: "op-02", Kind: InstallGeneration, DependsOn: []string{"op-01"}, Input: OperationInput{Generation: &GenerationInput{GenerationReference: generation}}, Preconditions: conditions("artifact-cache", selection.Artifact.Digest, "verified"), ExpectedObservations: conditions("generation-directory", releaseDirectory, generationID), Recovery: RemoveCandidate},
		{ID: "op-03", Kind: StartCandidate, DependsOn: []string{"op-02"}, Input: OperationInput{Systemd: &SystemdInput{GenerationReference: generation, Unit: unit, Port: candidatePort}}, Preconditions: conditions("tcp-port", fmt.Sprintf("127.0.0.1:%d", candidatePort), "available"), ExpectedObservations: conditions("systemd-unit", unit, "active"), Recovery: StopCandidate},
		{ID: "op-04", Kind: VerifyCandidate, DependsOn: []string{"op-03"}, Input: OperationInput{Health: &HealthInput{GenerationReference: generation, Unit: unit, LivenessPath: component.Health.Liveness.Path, ReadinessPath: component.Health.Readiness.Path, CandidateVerifyPath: component.Health.CandidateVerification.Path, Port: candidatePort}}, Preconditions: conditions("health-check", component.Health.Readiness.Path, "healthy"), ExpectedObservations: conditions("health-check", component.Health.CandidateVerification.Path, "healthy"), Recovery: LeaveEndpointUnchanged},
		{ID: "op-05", Kind: SwitchEndpoint, DependsOn: []string{"op-04"}, Input: OperationInput{Endpoint: &endpoint, Previous: observation.Deployment.Active}, Preconditions: conditions("candidate-verification", generationID, "passed"), ExpectedObservations: conditions("caddy-route", routeID, fmt.Sprintf("127.0.0.1:%d", candidatePort)), Recovery: RestorePreviousRoute},
		{ID: "op-06", Kind: VerifyActive, DependsOn: []string{"op-05"}, Input: OperationInput{Health: &HealthInput{GenerationReference: generation, Unit: unit, LivenessPath: component.Health.Liveness.Path, ReadinessPath: component.Health.Readiness.Path, CandidateVerifyPath: component.Health.CandidateVerification.Path, Port: listenPort}, Endpoint: &endpoint, Previous: observation.Deployment.Active}, Preconditions: conditions("caddy-route", routeID, fmt.Sprintf("127.0.0.1:%d", candidatePort)), ExpectedObservations: conditions("stable-endpoint-health", component.Health.Readiness.Path, "healthy"), Recovery: RestorePreviousRoute},
	}
	if previous := observation.Deployment.Active; previous != nil {
		operations = append(operations,
			Operation{ID: "op-07", Kind: DrainPrevious, DependsOn: []string{"op-06"}, Input: OperationInput{Drain: &DrainInput{Endpoint: endpoint, Previous: *previous, Mode: selection.Implementation.Endpoint.Drain.Mode, MaxDuration: selection.Implementation.Endpoint.Drain.MaxDuration}}, Preconditions: conditions("post-switch-verification", generationID, "passed"), ExpectedObservations: conditions("systemd-unit", previous.SystemdUnit, "drained"), Recovery: RestorePreviousRoute},
			Operation{ID: "op-08", Kind: RetainPrevious, DependsOn: []string{"op-07"}, Input: OperationInput{Retention: &RetentionInput{Endpoint: endpoint, Previous: *previous, Policy: rollbackwindow.RuleRollbackWindow, RollbackWindow: compiled.Environment.RollbackWindow}}, Preconditions: conditions("systemd-unit", previous.SystemdUnit, "drained"), ExpectedObservations: conditions("rollback-generation", previous.ID+"@"+observationDigest, "retained"), Recovery: RetainBothGenerations},
		)
	}
	return operations
}

func conditions(kind, subject, expected string) []TypedCondition {
	return []TypedCondition{{Kind: kind, Subject: subject, Expected: expected}}
}

func generationPort(digest string) int {
	bytes, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")[:4])
	if err != nil || len(bytes) != 2 {
		return 0
	}
	return 20000 + (int(bytes[0])<<8|int(bytes[1]))%20000
}

func observedOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func digest(value any) (string, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize Plan input: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func OperationDigest(operation Operation) (string, error) {
	return digest(operation)
}

func (p Plan) VerifyIdentity() error {
	want := p.ID
	copy := p
	copy.ID = ""
	actual, err := digest(copy)
	if err != nil {
		return err
	}
	if want == "" || actual != want {
		return errors.New("Plan identity does not match its canonical contents")
	}
	return nil
}
