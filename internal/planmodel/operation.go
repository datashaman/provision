package planmodel

import (
	"provision/internal/config"
	"provision/internal/drain"
	"provision/internal/host"
	"provision/internal/rollbackwindow"
)

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
	GenerationID        string               `json:"generationId"`
	Implementation      string               `json:"implementation"`
	Lifecycle           string               `json:"lifecycle"`
	Rollout             string               `json:"rollout"`
	CredentialReference string               `json:"credentialReference"`
	RabbitMQVersion     string               `json:"rabbitmqVersion"`
	ImageIndex          string               `json:"imageIndex"`
	ImageManifest       string               `json:"imageManifest"`
	ImageReference      string               `json:"imageReference"`
	ServiceUnit         string               `json:"serviceUnit"`
	Container           string               `json:"container"`
	Account             string               `json:"account"`
	DataPath            string               `json:"dataPath"`
	QuadletPath         string               `json:"quadletPath"`
	AMQPPort            int                  `json:"amqpPort"`
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
	QueueLogicalID string                       `json:"queueLogicalId"`
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
	Component           string                     `json:"component"`
	Queue               string                     `json:"queue"`
	QueueLogicalID      string                     `json:"queueLogicalId"`
	GenerationID        string                     `json:"generationId"`
	Revision            string                     `json:"revision"`
	ConfigurationDigest string                     `json:"configurationDigest"`
	ArtifactDigest      string                     `json:"artifactDigest"`
	SystemdUnit         string                     `json:"systemdUnit"`
	Timeout             string                     `json:"timeout"`
	Rollout             string                     `json:"rollout"`
	Previous            *host.TaskGenerationStatus `json:"previous,omitempty"`
}

type AsyncScheduleInput struct {
	Component           string                   `json:"component"`
	Task                string                   `json:"task"`
	TaskGenerationID    string                   `json:"taskGenerationId"`
	TaskUnit            string                   `json:"taskUnit"`
	ApplicationRevision string                   `json:"applicationRevision"`
	ConfigurationDigest string                   `json:"configurationDigest"`
	TimerUnit           string                   `json:"timerUnit"`
	Expression          string                   `json:"expression"`
	Timezone            string                   `json:"timezone"`
	DaylightSaving      string                   `json:"daylightSaving"`
	Overlap             string                   `json:"overlap"`
	Retry               config.ScheduleRetry     `json:"retry"`
	MissedRun           config.ScheduleMissedRun `json:"missedRun"`
	Failure             string                   `json:"failure"`
	Rollout             string                   `json:"rollout"`
	AppletDigest        string                   `json:"appletDigest"`
	LedgerSchema        string                   `json:"ledgerSchema"`
	Previous            *host.ScheduleStatus     `json:"previous,omitempty"`
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
