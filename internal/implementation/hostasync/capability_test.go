package hostasync

import (
	"strings"
	"testing"

	"provision/internal/config"
	"provision/internal/host"
)

func TestInitialSchedulePolicyFailsClosedAroundFutureSemantics(t *testing.T) {
	supported := config.ScheduleContract{
		Retry:     config.ScheduleRetry{MaxAttempts: 1, Delay: "10s"},
		MissedRun: config.ScheduleMissedRun{Mode: "skip"},
	}
	if reason := InitialSchedulePolicyReason(supported); reason != "" {
		t.Fatalf("supported initial policy rejected: %s", reason)
	}
	retrying := supported
	retrying.Retry.MaxAttempts = 3
	if reason := InitialSchedulePolicyReason(retrying); !strings.Contains(reason, "exactly one") {
		t.Fatalf("retrying policy did not fail closed: %q", reason)
	}
	catchUp := supported
	catchUp.MissedRun = config.ScheduleMissedRun{Mode: "bounded-catch-up", MaxOccurrences: 2}
	if reason := InitialSchedulePolicyReason(catchUp); !strings.Contains(reason, "skip") {
		t.Fatalf("catch-up policy did not fail closed: %q", reason)
	}
}

func TestInitialReplacementFailsClosedUntilWorkerHandoffExists(t *testing.T) {
	observation := qualifiedAsyncObservation()
	observation.Async.Deployment.ActiveWorker = &host.WorkerGenerationStatus{
		ID: "revision-a-aaaaaaaaaaaa", ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
		UnitActive: true, QueueConnected: true, Gate: "open",
	}
	if reason := InitialReplacementReason(observation.Async.Deployment); !strings.Contains(reason, "does not support replacing") {
		t.Fatalf("active Worker replacement remained executable: %q", reason)
	}
	evaluation := Evaluate(observation, config.TargetSelection{Target: config.Target{Local: true}})
	for _, guarantee := range evaluation.Guarantees {
		if guarantee == "bounded-in-flight-worker-drain" || guarantee == "fenced-schedule-handoff" || guarantee == "previous-worker-generation-retention" {
			t.Fatalf("unimplemented guarantee advertised: %s", guarantee)
		}
	}
}

func qualifiedAsyncObservation() host.BootstrapStatus {
	return host.BootstrapStatus{
		SchemaVersion: "provision.dev/host-inspection/v1alpha1", Environment: "lab", OS: "ubuntu", OSVersion: "26.04", Architecture: "x86_64",
		SystemdVersion: "systemd 259", CgroupV2: true, JournaldActive: true, GenerationStorageReady: true,
		ExecutorDigest: "sha256:" + strings.Repeat("e", 64), AuthorityKeyID: "sha256:" + strings.Repeat("a", 64),
		AllowedOperations: []string{"prepareQueue"}, Ready: true,
		Async: &host.AsyncStatus{SchemaVersion: "provision.dev/host-async-inspection/v1alpha1", ObservationComplete: true, Capabilities: host.AsyncCapabilities{
			PodmanVersion: "5.7.0+ds2-3build1", Quadlet: true, RootlessEnvironmentAccount: true, SystemdCredentials: true,
			SubordinateIDs: true, LingeringUserManager: true, RabbitMQQualificationDigest: QualificationDigest,
			RabbitMQVersion: "4.3.6", RabbitMQImageIndex: ImageIndex, RabbitMQImageManifest: ImageManifest,
			RabbitMQServiceUnit: "provision-lab-rabbitmq.service", RabbitMQContainer: "provision-lab-rabbitmq", RabbitMQAccount: "provision-lab",
			RabbitMQDataPath: "/var/lib/provision/environments/lab/services/rabbitmq/data", RabbitMQQuadletPath: "/etc/containers/systemd/users/102/provision-lab-rabbitmq.container",
			WorkerAdmissionGate: true, ScheduleAppletDigest: "sha256:" + strings.Repeat("s", 64), ScheduleLedgerSchema: "provision.dev/schedule-ledger/v1alpha1",
		}},
	}
}
