package hostasync

import (
	"fmt"
	"strings"

	"provision/internal/config"
	"provision/internal/host"
)

const (
	QualificationDigest = "sha256:af41714b1aa2270ba6cd151bd24876ac117218e401e1e87515451a7081ac4c6d"
	ImageIndex          = "sha256:d0bffe70e755f348625415f32b0a090662e5f06b3ba3f82a4c7aaa18621b1279"
	ImageManifest       = "sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91"
)

type Evaluation struct {
	Contract        string
	Guarantees      []string
	SupportEvidence []string
	Reasons         []string
}

func Evaluate(observation host.BootstrapStatus, target config.TargetSelection) Evaluation {
	evaluation := Evaluation{
		Contract: "host-rabbitmq-systemd-async/v1alpha1",
		Guarantees: []string{
			"digest-pinned-rabbitmq-quorum-queue",
			"publisher-confirmed-at-least-once-delivery",
			"manual-consumer-acknowledgement",
		},
		SupportEvidence: []string{
			"restricted-bootstrap-ready",
			"tested-host-version-match",
			"qualified-rabbitmq-packaging",
			"rootless-quadlet-ready",
			"systemd-credentials-ready",
			"exact-retry-dead-letter-retention-configuration",
			"journald-active",
			"restricted-executor-identity-matched",
		},
	}

	issues := append([]string{}, observation.Findings...)
	if observation.SchemaVersion != "provision.dev/host-inspection/v1alpha1" {
		issues = append(issues, "host inspection schema does not provide asynchronous capability evidence")
	}
	if !observation.Ready {
		issues = append(issues, "restricted Host Target bootstrap is not ready")
	}
	if observation.OS != "ubuntu" || observation.OSVersion != "26.04" {
		issues = append(issues, fmt.Sprintf("Ubuntu version %s has no tested asynchronous evidence", observed(observation.OSVersion)))
	}
	if observation.Architecture != "x86_64" {
		issues = append(issues, fmt.Sprintf("architecture %s has no tested asynchronous evidence", observed(observation.Architecture)))
	}
	if !strings.HasPrefix(observation.SystemdVersion, "systemd 259") || !observation.CgroupV2 || !observation.JournaldActive || !observation.GenerationStorageReady {
		issues = append(issues, "Host systemd, cgroup v2, journald, or generation storage evidence is incomplete")
	}
	if observation.ExecutorDigest == "" || observation.AuthorityKeyID == "" {
		issues = append(issues, "restricted host executor or authority identity is not observed")
	}
	foundPrepareQueue := false
	for _, operation := range observation.AllowedOperations {
		if operation == "prepareQueue" {
			foundPrepareQueue = true
			break
		}
	}
	if !foundPrepareQueue {
		issues = append(issues, "host executor does not allow typed prepareQueue operations")
	}
	if !target.Target.Local && observation.SSHHostKeyFingerprint == "" {
		issues = append(issues, "SSH host key identity is not observed")
	}
	if observation.Async == nil || observation.Async.SchemaVersion != "provision.dev/host-async-inspection/v1alpha1" {
		evaluation.Reasons = unique(append(issues, "Host observation has no supported asynchronous capability evidence"))
		return evaluation
	}

	async := observation.Async
	issues = append(issues, async.Findings...)
	deployment := async.Deployment
	if !async.ObservationComplete {
		issues = append(issues, "asynchronous deployment observation is incomplete")
	}

	capability := async.Capabilities
	if capability.PodmanVersion != "5.7.0+ds2-3build1" || !capability.Quadlet || !capability.RootlessEnvironmentAccount {
		issues = append(issues, "qualified rootless Podman and Quadlet capability is not observed")
	}
	if !capability.SystemdCredentials {
		issues = append(issues, "encrypted systemd credential delivery is not observed")
	}
	detailedPackaging := capability.SubordinateIDs && capability.LingeringUserManager
	if !detailedPackaging {
		issues = append(issues, "rootless Queue account, subordinate IDs, or lingering manager evidence is incomplete")
	}
	if capability.RabbitMQQualificationDigest != QualificationDigest ||
		capability.RabbitMQVersion != "4.3.6" ||
		capability.RabbitMQImageIndex != ImageIndex ||
		capability.RabbitMQImageManifest != ImageManifest {
		issues = append(issues, "RabbitMQ product identity does not match the qualified packaging evidence")
	}

	expectedService := "provision-" + observation.Environment + "-rabbitmq"
	if capability.RabbitMQServiceUnit != expectedService+".service" ||
		capability.RabbitMQContainer != expectedService ||
		capability.RabbitMQAccount != "provision-"+observation.Environment ||
		capability.RabbitMQDataPath != "/var/lib/provision/environments/"+observation.Environment+"/services/rabbitmq/data" ||
		capability.RabbitMQQuadletPath == "" {
		issues = append(issues, "RabbitMQ service identity or owned paths do not match the Environment")
	}
	workerHandoffOperations := []string{
		"installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate",
		"fenceWorkerIntake", "drainWorkerPrevious", "activateWorkerIntake",
		"verifyWorkerActive", "retainWorkerPrevious",
	}
	if capability.WorkerAdmissionGate && allowsAll(observation.AllowedOperations, workerHandoffOperations...) && strings.HasPrefix(capability.ScheduleAppletDigest, "sha256:") && len(capability.ScheduleAppletDigest) == 71 && capability.ScheduleLedgerSchema == "provision.dev/schedule-ledger/v1alpha1" {
		evaluation.Guarantees = append(evaluation.Guarantees,
			"gated-worker-candidate", "bounded-in-flight-worker-drain", "previous-worker-generation-retention", "generation-specific-task", "stable-schedule-timer")
		evaluation.SupportEvidence = append(evaluation.SupportEvidence,
			"worker-admission-control-proven", "stable-message-identity", "manual-settlement-evidence", "pinned-schedule-applet", "versioned-occurrence-ledger")
	}
	if queue := deployment.Queue; queue != nil && queue.Exists {
		if !queue.Ready ||
			queue.GenerationID == "" ||
			queue.QueueType != "quorum" ||
			queue.Members != 1 ||
			!queue.Durable ||
			queue.RabbitMQVersion != "4.3.6" ||
			queue.Health != "healthy" ||
			queue.RetryQueue == "" || queue.DeadLetterQueue == "" ||
			queue.WorkExchange == "" || queue.RetryExchange == "" || queue.DeadLetterExchange == "" || len(queue.Bindings) != 3 ||
			queue.MessageTTL != "24h0m0s" || queue.RetryDelay != "10s" || queue.DeadLetterTTL != "168h0m0s" || queue.DeliveryLimit != 3 ||
			queue.Accepted != queue.Available+queue.Acknowledged+queue.DeadLettered ||
			queue.Accepted < 1 || len(queue.SupportedGuarantees) == 0 || len(queue.OwnedResources) == 0 ||
			queue.ImageManifest != ImageManifest ||
			queue.ServiceUnit != capability.RabbitMQServiceUnit ||
			queue.Container != capability.RabbitMQContainer ||
			queue.Account != capability.RabbitMQAccount ||
			queue.DataPath != capability.RabbitMQDataPath ||
			queue.QuadletPath != capability.RabbitMQQuadletPath {
			issues = append(issues, "observed Queue does not match the qualified single-member quorum generation")
		}
	}
	if worker := deployment.ActiveWorker; worker != nil {
		if worker.ID == "" ||
			worker.ArtifactDigest == "" ||
			!worker.UnitActive ||
			!worker.QueueConnected ||
			worker.Gate != "open" ||
			worker.InFlight < 0 {
			issues = append(issues, "active Worker generation does not match observed deployment state")
		}
	}
	if task := deployment.ActiveTask; task != nil {
		if task.ID == "" ||
			task.Revision == "" ||
			!strings.HasPrefix(task.ArtifactDigest, "sha256:") ||
			task.SystemdUnit == "" {
			issues = append(issues, "active Task generation does not match observed deployment state")
		}
	}
	if schedule := deployment.Schedule; schedule != nil {
		if schedule.TimerUnit == "" ||
			schedule.TaskGenerationID == "" ||
			!strings.HasPrefix(schedule.AppletDigest, "sha256:") ||
			schedule.LedgerSchema != "provision.dev/schedule-ledger/v1alpha1" ||
			!strings.HasPrefix(schedule.LedgerDigest, "sha256:") ||
			schedule.FencingToken < 1 ||
			deployment.ActiveTask == nil ||
			schedule.TaskGenerationID != deployment.ActiveTask.ID {
			issues = append(issues, "active Schedule and Task generation do not match observed fenced runtime state")
		}
	}

	evaluation.Reasons = unique(issues)
	return evaluation
}

func allowsAll(observed []string, required ...string) bool {
	allowed := make(map[string]struct{}, len(observed))
	for _, operation := range observed {
		allowed[operation] = struct{}{}
	}
	for _, operation := range required {
		if _, ok := allowed[operation]; !ok {
			return false
		}
	}
	return true
}

func InitialSchedulePolicyReason(schedule config.ScheduleContract) string {
	if schedule.Retry.MaxAttempts != 1 {
		return "initial Schedule runtime supports exactly one Task attempt"
	}
	if schedule.MissedRun.Mode != "skip" || schedule.MissedRun.MaxOccurrences != 0 {
		return "initial Schedule runtime supports only missed-run skip policy"
	}
	return ""
}

func ReplacementReason(compiled config.Compiled, async host.AsyncStatus) string {
	deployment := async.Deployment
	if deployment.ActiveWorker == nil && deployment.ActiveTask == nil && deployment.Schedule == nil {
		return ""
	}
	if deployment.ActiveWorker == nil || deployment.ActiveTask == nil || deployment.Schedule == nil {
		return "asynchronous replacement requires exact active Worker, Task, and Schedule observations"
	}
	roles := roleNames(compiled)
	workerArtifact := compiled.Revision.Artifacts[roles["worker"]]
	taskArtifact := compiled.Revision.Artifacts[roles["task"]]
	taskComponent := compiled.Application.Components[roles["task"]]
	scheduleComponent := compiled.Application.Components[roles["schedule"]]
	scheduleImplementation := compiled.Environment.Implementations[roles["schedule"]]

	if workerArtifact.Digest == deployment.ActiveWorker.ArtifactDigest {
		return "Worker replacement requires a different immutable Worker Artifact"
	}
	if taskArtifact.Digest != deployment.ActiveTask.ArtifactDigest ||
		deployment.ActiveTask.Queue != "provision-"+compiled.Environment.Name+"-"+roles["queue"] ||
		deployment.ActiveTask.Timeout != taskComponent.Task.Timeout {
		return "current tracer supports only Worker replacement; the active Task contract or Artifact differs"
	}
	expectedTaskUnit := fmt.Sprintf("provision-%s-%s-%s@.service", compiled.Environment.Name, roles["task"], strings.TrimPrefix(taskArtifact.Digest, "sha256:")[:12])
	if deployment.ActiveTask.SystemdUnit != expectedTaskUnit {
		return "current tracer supports only Worker replacement; the active Task generation identity differs"
	}
	schedule := deployment.Schedule
	contract := scheduleComponent.Schedule
	expectedTimer := fmt.Sprintf("provision-%s-%s.timer", compiled.Environment.Name, roles["schedule"])
	if !schedule.Active || schedule.TimerUnit != expectedTimer || schedule.TaskGenerationID != deployment.ActiveTask.ID ||
		!strings.HasPrefix(schedule.AppletDigest, "sha256:") || schedule.LedgerSchema != scheduleImplementation.Schedule.LedgerSchema ||
		schedule.Expression != contract.Expression || schedule.Timezone != contract.Timezone || schedule.DaylightSaving != contract.DaylightSaving ||
		schedule.Overlap != contract.Overlap || schedule.Retry != contract.Retry || schedule.MissedRun != contract.MissedRun || schedule.Failure != contract.Failure {
		return "current tracer supports only Worker replacement; the active Schedule contract or Task binding differs"
	}
	return ""
}

func observed(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
