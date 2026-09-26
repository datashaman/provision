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
			"bounded-redelivery-with-dead-lettering",
			"24-hour-message-retention",
		},
		SupportEvidence: []string{
			"restricted-bootstrap-ready",
			"tested-host-version-match",
			"qualified-rabbitmq-packaging",
			"rootless-quadlet-ready",
			"systemd-credentials-ready",
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
	if capability.WorkerAdmissionGate && strings.HasPrefix(capability.ScheduleAppletDigest, "sha256:") && len(capability.ScheduleAppletDigest) == 71 && capability.ScheduleLedgerSchema == "provision.dev/schedule-ledger/v1alpha1" {
		evaluation.Guarantees = append(evaluation.Guarantees,
			"gated-worker-candidate", "bounded-in-flight-worker-drain", "generation-specific-task",
			"stable-schedule-timer", "fenced-schedule-handoff", "previous-worker-generation-retention")
		evaluation.SupportEvidence = append(evaluation.SupportEvidence,
			"worker-admission-control-proven", "pinned-schedule-applet", "versioned-occurrence-ledger")
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
			queue.MessageTTL != "24h0m0s" || queue.DeliveryLimit != 3 ||
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
