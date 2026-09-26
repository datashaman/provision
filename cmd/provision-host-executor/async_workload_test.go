package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planner"
)

func TestInitialAsyncOperationValidationPinsTaskWorkerAndScheduleIdentities(t *testing.T) {
	record, paths, task, worker, schedule := asyncOperationFixture(t)
	operations := []planner.Operation{
		{ID: "op-04", Kind: planner.InstallTaskGeneration, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Task: &task}}},
		{ID: "op-05", Kind: planner.InstallWorkerGeneration, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Worker: &worker}}},
		{ID: "op-13", Kind: planner.HandoffSchedule, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Schedule: &schedule}}},
	}
	for _, operation := range operations {
		if err := validateAsyncWorkloadOperation(operation, record, paths); err != nil {
			t.Fatalf("valid %s rejected: %v", operation.Kind, err)
		}
	}

	tamperedTask := task
	tamperedTask.QueueLogicalID = "another-queue"
	if err := validateAsyncWorkloadOperation(planner.Operation{ID: "op-04", Kind: planner.InstallTaskGeneration, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Task: &tamperedTask}}}, record, paths); err == nil {
		t.Fatal("Task with caller-selected Queue identity accepted")
	}
	tamperedSchedule := schedule
	tamperedSchedule.Expression = "0 0 * * *"
	if err := validateAsyncWorkloadOperation(planner.Operation{ID: "op-13", Kind: planner.HandoffSchedule, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Schedule: &tamperedSchedule}}}, record, paths); err == nil {
		t.Fatal("unimplemented Schedule expression accepted")
	}
	tamperedSchedule = schedule
	tamperedSchedule.Retry.MaxAttempts = 3
	if err := validateAsyncWorkloadOperation(planner.Operation{ID: "op-13", Kind: planner.HandoffSchedule, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Schedule: &tamperedSchedule}}}, record, paths); err == nil {
		t.Fatal("unimplemented Schedule retry policy accepted")
	}
}

func TestRenderedAsyncUnitsAreGenerationBoundAndSecretFree(t *testing.T) {
	record, paths, task, worker, schedule := asyncOperationFixture(t)
	taskUnit := renderTaskUnit(task, record, paths, "provision-example-async-task")
	workerUnit := renderWorkerUnit(worker, record, paths, "provision-example-async-worker")
	for name, unit := range map[string]string{"Task": taskUnit, "Worker": workerUnit} {
		if !strings.Contains(unit, "LoadCredentialEncrypted=rabbitmq-url:") || !strings.Contains(unit, "NoNewPrivileges=true") || !strings.Contains(unit, "ProtectSystem=strict") || strings.Contains(unit, "amqp://") {
			t.Fatalf("%s unit omitted hardening or exposed connection material:\n%s", name, unit)
		}
	}
	if !strings.Contains(taskUnit, "--invocation %i") || !strings.Contains(taskUnit, task.ArtifactDigest) || !strings.Contains(workerUnit, "--gate-file ") || !strings.Contains(workerUnit, worker.ArtifactDigest) {
		t.Fatal("rendered units are not bound to generation-specific runtime inputs")
	}
	scheduleService := renderScheduleService(schedule, record)
	scheduleTimer := renderScheduleTimer(schedule, record)
	if !strings.Contains(scheduleService, "/var/lib/provision/environments/lab/runtime/provision-runtime-schedule") || strings.Contains(scheduleService, "/usr/local/libexec/provision-runtime-schedule") {
		t.Fatalf("Schedule service does not use an Environment-pinned runtime:\n%s", scheduleService)
	}
	if !strings.Contains(scheduleTimer, "Persistent=false") {
		t.Fatalf("initial missed-run skip policy is not explicit:\n%s", scheduleTimer)
	}
}

func TestScheduleVerificationJoinsPublisherAndWorkerEvidenceByMessageIdentity(t *testing.T) {
	directory := t.TempDir()
	taskPath := filepath.Join(directory, "task.jsonl")
	workerPath := filepath.Join(directory, "worker.jsonl")
	if err := os.WriteFile(taskPath, []byte("{\"event\":\"message_confirmed\",\"messageId\":\"msg-123\",\"invocationId\":\"inv-123\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workerRevision := "provision-example-async-v1"
	workerDigest := "sha256:" + strings.Repeat("a", 64)
	workerEvidence := "{\"event\":\"processed\",\"messageId\":\"msg-123\",\"workerApplicationRevision\":\"" + workerRevision + "\",\"workerArtifactDigest\":\"" + workerDigest + "\"}\n" +
		"{\"event\":\"acknowledged\",\"messageId\":\"msg-123\",\"workerApplicationRevision\":\"" + workerRevision + "\",\"workerArtifactDigest\":\"" + workerDigest + "\"}\n"
	if err := os.WriteFile(workerPath, []byte(workerEvidence), 0600); err != nil {
		t.Fatal(err)
	}
	messageID, confirmed := taskConfirmedMessage(taskPath, "inv-123")
	processed, acknowledged := workerAcknowledgedMessage(workerPath, messageID, workerRevision, workerDigest)
	if !confirmed || !processed || !acknowledged || messageID != "msg-123" {
		t.Fatalf("evidence was not joined exactly: id=%q confirmed=%t processed=%t acknowledged=%t", messageID, confirmed, processed, acknowledged)
	}
}

func TestScheduleVerificationRejectsDecisionDuplicateAndWrongWorkerEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.jsonl")
	evidence := "{\"event\":\"duplicate_ignored\",\"messageId\":\"msg-123\",\"workerApplicationRevision\":\"revision-a\",\"workerArtifactDigest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n" +
		"{\"event\":\"acknowledgement_decided\",\"messageId\":\"msg-123\",\"workerApplicationRevision\":\"revision-a\",\"workerArtifactDigest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n" +
		"{\"event\":\"acknowledged\",\"messageId\":\"msg-123\",\"workerApplicationRevision\":\"revision-b\",\"workerArtifactDigest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"}\n"
	if err := os.WriteFile(path, []byte(evidence), 0600); err != nil {
		t.Fatal(err)
	}
	processed, acknowledged := workerAcknowledgedMessage(path, "msg-123", "revision-a", "sha256:"+strings.Repeat("a", 64))
	if processed || acknowledged {
		t.Fatalf("decision, duplicate, or wrong-generation evidence satisfied verification: processed=%t acknowledged=%t", processed, acknowledged)
	}
}

func TestTaskInstanceUsesStableInvocationIdentity(t *testing.T) {
	unit, err := taskInstanceUnit("provision-lab-publish-bbbbbbbbbbbb@.service", "inv-1234567890abcdef")
	if err != nil || unit != "provision-lab-publish-bbbbbbbbbbbb@inv-1234567890abcdef.service" {
		t.Fatalf("Task instance = %q, %v", unit, err)
	}
	if _, err := taskInstanceUnit("provision-lab-publish.service", "inv-1234567890abcdef"); err == nil {
		t.Fatal("non-template Task unit accepted")
	}
}

func TestWorkerUnitDiagnosticNamesSystemdCredentialFailure(t *testing.T) {
	diagnostic := parseSystemdUnitDiagnostic([]byte("LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\nNRestarts=5\nExecMainCode=1\nExecMainStatus=243\n"))
	if diagnostic.FailureStage != "credentials" || diagnostic.ExecMainStatus != 243 || diagnostic.RestartCount != 5 {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
	summary := summarizeWorkerUnitFailure("provision-lab-consumer.service", diagnostic)
	if !strings.Contains(summary, "systemd credential setup") || !strings.Contains(summary, "exit status 243/CREDENTIALS") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestVerifyWorkerActiveRequiresDurableActiveRecord(t *testing.T) {
	observed := host.AsyncWorkerOperationObservation{
		Status: "active-open", Verified: true,
		Worker: host.WorkerGenerationStatus{Gate: "open", UnitActive: true, QueueConnected: true},
	}
	if workerOperationSatisfied(planner.VerifyWorkerActive, observed) {
		t.Fatal("open verified Worker without a durable active record satisfied verifyWorkerActive")
	}
	observed.Worker.Active = true
	if !workerOperationSatisfied(planner.VerifyWorkerActive, observed) {
		t.Fatal("exact active Worker did not satisfy verifyWorkerActive")
	}
}

func asyncOperationFixture(t *testing.T) (bootstrapRecord, executionPaths, planner.AsyncTaskInput, planner.AsyncWorkerInput, planner.AsyncScheduleInput) {
	t.Helper()
	root := t.TempDir()
	paths := executionPaths{environmentHome: filepath.Join(root, "environments", "lab"), systemdUnits: filepath.Join(root, "systemd")}
	record := bootstrapRecord{Environment: "lab", Account: "provision-lab"}
	taskDigest := "sha256:" + strings.Repeat("b", 64)
	workerDigest := "sha256:" + strings.Repeat("a", 64)
	configDigest := "sha256:" + strings.Repeat("c", 64)
	appletDigest := "sha256:" + strings.Repeat("d", 64)
	task := planner.AsyncTaskInput{Component: "publish", Queue: "messages", QueueLogicalID: "provision-lab-messages", GenerationID: "provision-example-async-v1-bbbbbbbbbbbb", Revision: "provision-example-async-v1", ConfigurationDigest: configDigest, ArtifactDigest: taskDigest, SystemdUnit: "provision-lab-publish-bbbbbbbbbbbb@.service", Timeout: "1m0s", Rollout: "required"}
	worker := planner.AsyncWorkerInput{Component: "consumer", Queue: "messages", QueueLogicalID: "provision-lab-messages", GenerationID: "provision-example-async-v1-aaaaaaaaaaaa", Revision: "provision-example-async-v1", ArtifactDigest: workerDigest, SystemdUnit: "provision-lab-consumer-aaaaaaaaaaaa.service", Admission: "gated", Drain: config.WorkerDrain{Mode: "bounded-in-flight", MaxDuration: "30s"}, Rollout: "required"}
	schedule := planner.AsyncScheduleInput{Component: "every-minute", Task: "publish", TaskGenerationID: task.GenerationID, TaskUnit: task.SystemdUnit, ApplicationRevision: task.Revision, ConfigurationDigest: configDigest, TimerUnit: "provision-lab-every-minute.timer", Expression: "* * * * *", Timezone: "Africa/Johannesburg", DaylightSaving: "wall-clock", Overlap: "forbid", Retry: config.ScheduleRetry{MaxAttempts: 1, Delay: "10s"}, MissedRun: config.ScheduleMissedRun{Mode: "skip", MaxOccurrences: 0}, Failure: "record", Rollout: "required", AppletDigest: appletDigest, LedgerSchema: "provision.dev/schedule-ledger/v1alpha1"}
	return record, paths, task, worker, schedule
}
