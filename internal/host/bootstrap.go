package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"provision/internal/config"
	"provision/internal/rollbackwindow"
)

const ExecutorPath = "/usr/local/libexec/provision-host-executor"

func AllowedOperations() []string {
	return []string{"inspect", "stageArtifact", "installGeneration", "startCandidate", "verifyCandidate", "switchEndpoint", "verifyActive", "drainPrevious", "retainPrevious", "prepareQueue", "installTaskGeneration", "verifyTaskGeneration", "installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate", "fenceWorkerIntake", "drainWorkerPrevious", "activateWorkerIntake", "verifyWorkerActive", "installScheduleRuntime", "handoffSchedule", "verifySchedule", "retainWorkerPrevious"}
}

var environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

type BootstrapStatus struct {
	SchemaVersion          string                `json:"schemaVersion,omitempty"`
	Environment            string                `json:"environment"`
	Operator               string                `json:"operator"`
	Account                string                `json:"account"`
	OS                     string                `json:"os"`
	OSVersion              string                `json:"osVersion"`
	Architecture           string                `json:"architecture"`
	SystemdVersion         string                `json:"systemdVersion,omitempty"`
	SSHServerVersion       string                `json:"sshServerVersion,omitempty"`
	CaddyVersion           string                `json:"caddyVersion,omitempty"`
	CaddyActive            bool                  `json:"caddyActive"`
	JournaldActive         bool                  `json:"journaldActive"`
	CgroupV2               bool                  `json:"cgroupV2"`
	ExecutorDigest         string                `json:"executorDigest,omitempty"`
	AuthorityKeyID         string                `json:"authorityKeyId,omitempty"`
	SSHHostKeyFingerprint  SSHHostKeyFingerprint `json:"sshHostKeyFingerprint,omitempty"`
	GenerationStorageReady bool                  `json:"generationStorageReady,omitempty"`
	CaddyConfigValid       bool                  `json:"caddyConfigValid,omitempty"`
	CaddyAdminReachable    bool                  `json:"caddyAdminReachable,omitempty"`
	CaddyConfigDurable     bool                  `json:"caddyConfigDurable,omitempty"`
	ListeningTCPPorts      []int                 `json:"listeningTcpPorts,omitempty"`
	Deployment             DeploymentStatus      `json:"deployment,omitempty"`
	Async                  *AsyncStatus          `json:"async,omitempty"`
	AllowedOperations      []string              `json:"allowedOperations"`
	Ready                  bool                  `json:"ready"`
	Findings               []string              `json:"findings"`
}

type AsyncStatus struct {
	SchemaVersion       string                `json:"schemaVersion"`
	ObservationComplete bool                  `json:"observationComplete"`
	Findings            []string              `json:"findings"`
	Capabilities        AsyncCapabilities     `json:"capabilities"`
	Deployment          AsyncDeploymentStatus `json:"deployment"`
}

type AsyncCapabilities struct {
	PodmanVersion               string `json:"podmanVersion"`
	Quadlet                     bool   `json:"quadlet"`
	RootlessEnvironmentAccount  bool   `json:"rootlessEnvironmentAccount"`
	SystemdCredentials          bool   `json:"systemdCredentials"`
	SubordinateIDs              bool   `json:"subordinateIds"`
	LingeringUserManager        bool   `json:"lingeringUserManager"`
	QuadletDefinitionRootOwned  bool   `json:"quadletDefinitionRootOwned"`
	DataPathEnvironmentOwned    bool   `json:"dataPathEnvironmentOwned"`
	EncryptedCredentialObserved bool   `json:"encryptedCredentialObserved"`
	WorkerAdmissionGate         bool   `json:"workerAdmissionGate"`
	RabbitMQQualificationDigest string `json:"rabbitmqQualificationDigest"`
	RabbitMQVersion             string `json:"rabbitmqVersion"`
	RabbitMQImageIndex          string `json:"rabbitmqImageIndex"`
	RabbitMQImageManifest       string `json:"rabbitmqImageManifest"`
	RabbitMQServiceUnit         string `json:"rabbitmqServiceUnit"`
	RabbitMQContainer           string `json:"rabbitmqContainer"`
	RabbitMQAccount             string `json:"rabbitmqAccount"`
	RabbitMQDataPath            string `json:"rabbitmqDataPath"`
	RabbitMQQuadletPath         string `json:"rabbitmqQuadletPath"`
	ScheduleAppletDigest        string `json:"scheduleAppletDigest"`
	ScheduleLedgerSchema        string `json:"scheduleLedgerSchema"`
}

type AsyncDeploymentStatus struct {
	Queue        *QueueStatus               `json:"queue,omitempty"`
	ActiveWorker *WorkerGenerationStatus    `json:"activeWorker,omitempty"`
	Candidate    *WorkerGenerationStatus    `json:"candidateWorker,omitempty"`
	Previous     *WorkerGenerationStatus    `json:"previousWorker,omitempty"`
	ActiveTask   *TaskGenerationStatus      `json:"activeTask,omitempty"`
	Schedule     *ScheduleStatus            `json:"schedule,omitempty"`
	Occurrences  []ScheduleOccurrenceStatus `json:"occurrences,omitempty"`
	Invocations  []TaskInvocationStatus     `json:"taskInvocations,omitempty"`
	Messages     []QueueMessageStatus       `json:"messages,omitempty"`
}

type QueueMessageStatus struct {
	ID                          string                    `json:"id"`
	ProducerApplicationRevision string                    `json:"producerApplicationRevision"`
	TaskArtifactDigest          string                    `json:"taskArtifactDigest"`
	TaskInvocationID            string                    `json:"taskInvocationId"`
	Disposition                 string                    `json:"disposition"`
	WorkerApplicationRevision   string                    `json:"workerApplicationRevision,omitempty"`
	WorkerArtifactDigest        string                    `json:"workerArtifactDigest,omitempty"`
	WorkerEvents                []QueueMessageWorkerEvent `json:"workerEvents,omitempty"`
}

type QueueMessageWorkerEvent struct {
	Event                     string `json:"event"`
	WorkerApplicationRevision string `json:"workerApplicationRevision"`
	WorkerArtifactDigest      string `json:"workerArtifactDigest"`
}

type QueueStatus struct {
	ID                  string               `json:"id"`
	GenerationID        string               `json:"generationId,omitempty"`
	Exists              bool                 `json:"exists"`
	Ready               bool                 `json:"ready"`
	QueueType           string               `json:"queueType"`
	Members             int                  `json:"members"`
	Durable             bool                 `json:"durable"`
	ImageManifest       string               `json:"imageManifest"`
	ServiceUnit         string               `json:"serviceUnit"`
	Container           string               `json:"container"`
	Account             string               `json:"account"`
	DataPath            string               `json:"dataPath"`
	QuadletPath         string               `json:"quadletPath"`
	RabbitMQVersion     string               `json:"rabbitmqVersion,omitempty"`
	Health              string               `json:"health,omitempty"`
	Reason              string               `json:"reason,omitempty"`
	RecoveryAction      string               `json:"recoveryAction,omitempty"`
	RetryQueue          string               `json:"retryQueue,omitempty"`
	DeadLetterQueue     string               `json:"deadLetterQueue,omitempty"`
	WorkExchange        string               `json:"workExchange,omitempty"`
	RetryExchange       string               `json:"retryExchange,omitempty"`
	DeadLetterExchange  string               `json:"deadLetterExchange,omitempty"`
	Bindings            []QueueBindingStatus `json:"bindings,omitempty"`
	MessageTTL          string               `json:"messageTtl,omitempty"`
	RetryDelay          string               `json:"retryDelay,omitempty"`
	DeadLetterTTL       string               `json:"deadLetterTtl,omitempty"`
	DeliveryLimit       int                  `json:"deliveryLimit,omitempty"`
	Accepted            int                  `json:"accepted,omitempty"`
	Available           int                  `json:"available,omitempty"`
	Acknowledged        int                  `json:"acknowledged,omitempty"`
	DeadLettered        int                  `json:"deadLettered,omitempty"`
	ProbeMessageID      string               `json:"probeMessageId,omitempty"`
	SupportedGuarantees []string             `json:"supportedGuarantees,omitempty"`
	OwnedResources      []string             `json:"ownedResources,omitempty"`
}

type QueueBindingStatus struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	RoutingKey  string `json:"routingKey"`
}

type WorkerGenerationStatus struct {
	ID             string     `json:"id"`
	Revision       string     `json:"revision"`
	ArtifactDigest string     `json:"artifactDigest"`
	SystemdUnit    string     `json:"systemdUnit"`
	Active         bool       `json:"active"`
	Gate           string     `json:"gate"`
	UnitActive     bool       `json:"unitActive"`
	QueueConnected bool       `json:"queueConnected"`
	InFlight       int        `json:"inFlight"`
	StatePath      string     `json:"statePath,omitempty"`
	GatePath       string     `json:"gatePath,omitempty"`
	EvidencePath   string     `json:"evidencePath,omitempty"`
	Health         string     `json:"health,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	RecoveryAction string     `json:"recoveryAction,omitempty"`
	Restartable    bool       `json:"restartable,omitempty"`
	RetainUntil    *time.Time `json:"retainUntil,omitempty"`
}

type TaskGenerationStatus struct {
	ID                  string `json:"id"`
	Revision            string `json:"revision"`
	ArtifactDigest      string `json:"artifactDigest"`
	SystemdUnit         string `json:"systemdUnit"`
	Queue               string `json:"queue"`
	ConfigurationDigest string `json:"configurationDigest"`
	Timeout             string `json:"timeout"`
	EvidencePath        string `json:"evidencePath,omitempty"`
}

type ScheduleStatus struct {
	Component        string                   `json:"component"`
	TimerUnit        string                   `json:"timerUnit"`
	TaskGenerationID string                   `json:"taskGenerationId"`
	AppletDigest     string                   `json:"appletDigest"`
	LedgerSchema     string                   `json:"ledgerSchema"`
	LedgerDigest     string                   `json:"ledgerDigest"`
	FencingToken     int64                    `json:"fencingToken"`
	Timezone         string                   `json:"timezone"`
	Expression       string                   `json:"expression"`
	DaylightSaving   string                   `json:"daylightSaving"`
	Overlap          string                   `json:"overlap"`
	Retry            config.ScheduleRetry     `json:"retry"`
	MissedRun        config.ScheduleMissedRun `json:"missedRun"`
	Failure          string                   `json:"failure"`
	Active           bool                     `json:"active"`
}

type WorkerVerificationChecks struct {
	Liveness          bool `json:"liveness"`
	QueueConnectivity bool `json:"queueConnectivity"`
	RevisionIdentity  bool `json:"revisionIdentity"`
	IntakeDisabled    bool `json:"intakeDisabled"`
}

type ScheduleOccurrenceStatus struct {
	ID               string    `json:"id"`
	Schedule         string    `json:"schedule"`
	DueAt            time.Time `json:"dueAt"`
	RecordedAt       time.Time `json:"recordedAt"`
	TaskGenerationID string    `json:"taskGenerationId"`
	FencingToken     int64     `json:"fencingToken"`
	TaskInvocationID string    `json:"taskInvocationId"`
	Disposition      string    `json:"disposition"`
}

type TaskAttemptStatus struct {
	Number       int        `json:"number"`
	SystemdUnit  string     `json:"systemdUnit"`
	StartedAt    time.Time  `json:"startedAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	Outcome      string     `json:"outcome"`
	EvidencePath string     `json:"evidencePath,omitempty"`
}

type TaskInvocationStatus struct {
	ID                  string              `json:"id"`
	Task                string              `json:"task"`
	TaskGenerationID    string              `json:"taskGenerationId"`
	ApplicationRevision string              `json:"applicationRevision"`
	ConfigurationDigest string              `json:"configurationDigest"`
	Trigger             string              `json:"trigger"`
	CreatedAt           time.Time           `json:"createdAt"`
	Outcome             string              `json:"outcome"`
	Attempts            []TaskAttemptStatus `json:"attempts"`
}

type AsyncTaskOperationObservation struct {
	Status     string               `json:"status"`
	Task       TaskGenerationStatus `json:"task"`
	Executable string               `json:"executable,omitempty"`
	Verified   bool                 `json:"verified"`
	Reason     string               `json:"reason,omitempty"`
}

type AsyncWorkerOperationObservation struct {
	Status            string                   `json:"status"`
	Worker            WorkerGenerationStatus   `json:"worker"`
	QueueGenerationID string                   `json:"queueGenerationId,omitempty"`
	Verified          bool                     `json:"verified"`
	Checks            WorkerVerificationChecks `json:"checks"`
	UnitDiagnostic    *SystemdUnitDiagnostic   `json:"unitDiagnostic,omitempty"`
	Reason            string                   `json:"reason,omitempty"`
}

type AsyncWorkerHandoffObservation struct {
	Status               string                 `json:"status"`
	PlanID               string                 `json:"planId,omitempty"`
	OperationDigest      string                 `json:"operationDigest"`
	DrainOperationDigest string                 `json:"drainOperationDigest,omitempty"`
	Candidate            WorkerGenerationStatus `json:"candidate"`
	Previous             WorkerGenerationStatus `json:"previous"`
	QueueGenerationID    string                 `json:"queueGenerationId"`
	InFlightMessageID    string                 `json:"inFlightMessageId,omitempty"`
	ReleasedMessageID    string                 `json:"releasedMessageId,omitempty"`
	BoundElapsed         bool                   `json:"boundElapsed"`
	DrainStartedAt       *time.Time             `json:"drainStartedAt,omitempty"`
	CompletionDeadline   *time.Time             `json:"completionDeadline,omitempty"`
	DrainDeadline        *time.Time             `json:"drainDeadline,omitempty"`
	ReleaseStartedAt     *time.Time             `json:"releaseStartedAt,omitempty"`
	DrainCompletedAt     *time.Time             `json:"drainCompletedAt,omitempty"`
	RollbackWindow       rollbackwindow.Window  `json:"rollbackWindow,omitempty"`
	RetainedAt           *time.Time             `json:"retainedAt,omitempty"`
	RetainUntil          *time.Time             `json:"retainUntil,omitempty"`
	Reason               string                 `json:"reason,omitempty"`
	RecoveryAction       string                 `json:"recoveryAction,omitempty"`
}

type WorkerActiveVerificationStatus string

const (
	WorkerActiveVerificationHealthy    WorkerActiveVerificationStatus = "healthy"
	WorkerActiveVerificationRolledBack WorkerActiveVerificationStatus = "rolled-back"
	WorkerActiveVerificationUncertain  WorkerActiveVerificationStatus = "uncertain"
)

// AsyncWorkerActiveVerificationObservation is the durable, message-level
// proof for the externally visible Worker handoff. A rolled-back outcome is a
// known failure of the candidate, not a claim of exactly-once processing: the
// same stable message identity may have been redelivered to the restored
// Worker.
type AsyncWorkerActiveVerificationObservation struct {
	Status                WorkerActiveVerificationStatus `json:"status"`
	PlanID                string                         `json:"planId"`
	OperationDigest       string                         `json:"operationDigest"`
	QueueGenerationID     string                         `json:"queueGenerationId"`
	Candidate             WorkerGenerationStatus         `json:"candidate"`
	Previous              WorkerGenerationStatus         `json:"previous"`
	MessageID             string                         `json:"messageId"`
	PublisherConfirmed    bool                           `json:"publisherConfirmed"`
	CandidateProcessed    bool                           `json:"candidateProcessed"`
	CandidateAcknowledged bool                           `json:"candidateAcknowledged"`
	PreviousProcessed     bool                           `json:"previousProcessed"`
	PreviousAcknowledged  bool                           `json:"previousAcknowledged"`
	RollbackAttempted     bool                           `json:"rollbackAttempted"`
	RollbackSucceeded     bool                           `json:"rollbackSucceeded"`
	Redelivered           bool                           `json:"redelivered"`
	Restored              *WorkerGenerationStatus        `json:"restored,omitempty"`
	Reason                string                         `json:"reason,omitempty"`
	RecoveryAction        string                         `json:"recoveryAction,omitempty"`
}

type SystemdUnitDiagnostic struct {
	LoadState      string `json:"loadState,omitempty"`
	ActiveState    string `json:"activeState,omitempty"`
	SubState       string `json:"subState,omitempty"`
	Result         string `json:"result,omitempty"`
	ExecMainCode   int    `json:"execMainCode,omitempty"`
	ExecMainStatus int    `json:"execMainStatus,omitempty"`
	FailureStage   string `json:"failureStage,omitempty"`
	RestartCount   int    `json:"restartCount,omitempty"`
}

type AsyncRuntimeObservation struct {
	Status       string `json:"status"`
	Path         string `json:"path"`
	AppletDigest string `json:"appletDigest"`
	LedgerSchema string `json:"ledgerSchema"`
	Reason       string `json:"reason,omitempty"`
}

type AsyncScheduleOperationObservation struct {
	Status       string                    `json:"status"`
	Schedule     ScheduleStatus            `json:"schedule"`
	Occurrence   *ScheduleOccurrenceStatus `json:"occurrence,omitempty"`
	Invocation   *TaskInvocationStatus     `json:"taskInvocation,omitempty"`
	MessageID    string                    `json:"messageId,omitempty"`
	Acknowledged bool                      `json:"acknowledged"`
	Reason       string                    `json:"reason,omitempty"`
}

type DeploymentStatus struct {
	Active   *GenerationStatus `json:"active,omitempty"`
	Previous *GenerationStatus `json:"previous,omitempty"`
}

type GenerationStatus struct {
	ID               string `json:"id"`
	Revision         string `json:"revision"`
	ArtifactDigest   string `json:"artifactDigest"`
	SystemdUnit      string `json:"systemdUnit"`
	ReleaseDirectory string `json:"releaseDirectory"`
	Port             int    `json:"port"`
	RouteID          string `json:"routeId"`
	UnitActive       bool   `json:"unitActive"`
	UnitMatches      bool   `json:"unitMatches"`
	RouteObserved    bool   `json:"routeObserved"`
	RouteUpstream    string `json:"routeUpstream,omitempty"`
	RouteMatches     bool   `json:"routeMatches"`
}

type RollbackWindowRecord struct {
	OperationDigest string                `json:"operationDigest"`
	Window          rollbackwindow.Window `json:"window"`
	RecordedAt      time.Time             `json:"recordedAt"`
	RetainUntil     time.Time             `json:"retainUntil"`
}

type ActiveGenerationRecord struct {
	SchemaVersion                        string                `json:"schemaVersion"`
	PlanID                               string                `json:"planId"`
	CandidateVerificationOperationDigest string                `json:"candidateVerificationOperationDigest"`
	Active                               GenerationStatus      `json:"active"`
	Previous                             *GenerationStatus     `json:"previous,omitempty"`
	ListenPort                           int                   `json:"listenPort"`
	DrainPolicy                          string                `json:"drainPolicy"`
	SwitchedAt                           time.Time             `json:"switchedAt"`
	StableVerificationOperationDigest    string                `json:"stableVerificationOperationDigest,omitempty"`
	StableVerifiedAt                     *time.Time            `json:"stableVerifiedAt,omitempty"`
	PreviousDrainOperationDigest         string                `json:"previousDrainOperationDigest,omitempty"`
	PreviousDrainedAt                    *time.Time            `json:"previousDrainedAt,omitempty"`
	PreviousRollback                     *RollbackWindowRecord `json:"previousRollback,omitempty"`
}

// CheckBootstrap invokes only the root-owned inspector. It cannot request a
// deployment operation, and remote connections require a trusted host key.
func CheckBootstrap(ctx context.Context, target Target, environment, operator string) (BootstrapStatus, error) {
	if err := target.Validate(); err != nil {
		return BootstrapStatus{}, err
	}
	if !environmentPattern.MatchString(environment) {
		return BootstrapStatus{}, errors.New("invalid environment identifier")
	}
	if !userPattern.MatchString(operator) || operator == "root" {
		return BootstrapStatus{}, errors.New("invalid operator user")
	}
	command := []string{"sudo", "-n", ExecutorPath, "inspect", "--environment", environment, "--operator", operator}
	name := command[0]
	args := command[1:]
	if !target.Local {
		name = "ssh"
		args = StrictSSHArguments(target, command...)
	}
	output, err := runCommand(ctx, name, args...)
	if err != nil {
		return BootstrapStatus{}, fmt.Errorf("host bootstrap check: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var status BootstrapStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return BootstrapStatus{}, fmt.Errorf("host bootstrap check returned invalid status: %w", err)
	}
	if status.Environment != environment || status.Operator != operator || status.Account != "provision-"+environment {
		return BootstrapStatus{}, errors.New("host bootstrap check returned mismatched identity")
	}
	wantedOperations := AllowedOperations()
	if mismatch := operationCapabilityMismatch(wantedOperations, status.AllowedOperations); mismatch != "" {
		return BootstrapStatus{}, fmt.Errorf("host executor operation capabilities differ: %s", mismatch)
	}
	if status.Ready && (status.OS != "ubuntu" || status.Architecture == "" || !strings.HasPrefix(status.SystemdVersion, "systemd ") || status.SSHServerVersion == "" || status.CaddyVersion == "" || !status.CaddyActive || !status.CaddyConfigValid || !status.CaddyAdminReachable || !status.CaddyConfigDurable || !status.GenerationStorageReady || !status.JournaldActive || !strings.HasPrefix(status.ExecutorDigest, "sha256:") || !strings.HasPrefix(status.AuthorityKeyID, "sha256:") || !target.Local && !status.SSHHostKeyFingerprint.Valid() || len(status.Findings) != 0) {
		return BootstrapStatus{}, errors.New("host bootstrap check returned incomplete readiness evidence")
	}
	return status, nil
}

func operationCapabilityMismatch(wanted, observed []string) string {
	wantedSet := make(map[string]struct{}, len(wanted))
	observedSet := make(map[string]struct{}, len(observed))
	for _, operation := range wanted {
		wantedSet[operation] = struct{}{}
	}
	for _, operation := range observed {
		observedSet[operation] = struct{}{}
	}

	missing := make([]string, 0)
	for _, operation := range wanted {
		if _, ok := observedSet[operation]; !ok {
			missing = append(missing, operation)
		}
	}
	unexpected := make([]string, 0)
	for _, operation := range observed {
		if _, ok := wantedSet[operation]; !ok {
			unexpected = append(unexpected, operation)
		}
	}

	if len(missing) > 0 || len(unexpected) > 0 {
		return fmt.Sprintf("missing=%v unexpected=%v", missing, unexpected)
	}
	if len(wanted) != len(observed) {
		return fmt.Sprintf("expected=%v observed=%v", wanted, observed)
	}
	for index := range wanted {
		if wanted[index] != observed[index] {
			return fmt.Sprintf("operation order differs: expected=%v observed=%v", wanted, observed)
		}
	}
	return ""
}
