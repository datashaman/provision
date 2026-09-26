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

func TestQueueMessageInspectionJoinsConfirmedMessagesOnlyToDurableSettlement(t *testing.T) {
	root := t.TempDir()
	paths := executionPaths{environmentHome: root}
	if err := os.MkdirAll(filepath.Dir(taskEvidencePath(paths)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(workerEvidencePath(paths)), 0755); err != nil {
		t.Fatal(err)
	}
	taskDigest := "sha256:" + strings.Repeat("b", 64)
	activeDigest := "sha256:" + strings.Repeat("a", 64)
	candidateDigest := "sha256:" + strings.Repeat("c", 64)
	taskEvidence := "{\"event\":\"message_confirmed\",\"messageId\":\"msg-1\",\"applicationRevision\":\"revision-a\",\"taskArtifactDigest\":\"" + taskDigest + "\",\"invocationId\":\"inv-1\"}\n" +
		"{\"event\":\"message_confirmed\",\"messageId\":\"msg-2\",\"applicationRevision\":\"revision-a\",\"taskArtifactDigest\":\"" + taskDigest + "\",\"invocationId\":\"inv-2\"}\n"
	workerEvidence := "{\"event\":\"acknowledgement_decided\",\"messageId\":\"msg-1\",\"workerApplicationRevision\":\"revision-a\",\"workerArtifactDigest\":\"" + activeDigest + "\"}\n" +
		"{\"event\":\"connected_gated\",\"workerApplicationRevision\":\"revision-b\",\"workerArtifactDigest\":\"" + candidateDigest + "\"}\n" +
		"{\"event\":\"acknowledged\",\"messageId\":\"msg-1\",\"workerApplicationRevision\":\"revision-a\",\"workerArtifactDigest\":\"" + activeDigest + "\"}\n"
	if err := os.WriteFile(taskEvidencePath(paths), []byte(taskEvidence), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerEvidencePath(paths), []byte(workerEvidence), 0600); err != nil {
		t.Fatal(err)
	}
	messages, err := inspectQueueMessages(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Disposition != "acknowledged" || messages[0].WorkerArtifactDigest != activeDigest || messages[1].Disposition != "accepted-unsettled" || messages[1].WorkerArtifactDigest != "" {
		t.Fatalf("message accounting = %+v", messages)
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
	verification := summarizeWorkerVerificationFailure("provision-lab-consumer.service", host.WorkerVerificationChecks{QueueConnectivity: true, RevisionIdentity: true, IntakeDisabled: true}, diagnostic)
	for _, evidence := range []string{"liveness=false", "queueConnectivity=true", "revisionIdentity=true", "intakeDisabled=true", "exit status 243/CREDENTIALS"} {
		if !strings.Contains(verification, evidence) {
			t.Fatalf("verification summary omitted %q: %s", evidence, verification)
		}
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

func TestWorkerCandidateRequiresAndPreservesExactDistinctActiveGeneration(t *testing.T) {
	record, paths, _, candidate, _ := asyncOperationFixture(t)
	activeDigest := "sha256:" + strings.Repeat("f", 64)
	activeInput := planner.AsyncWorkerInput{
		Component: "consumer", Queue: "messages", QueueLogicalID: "provision-lab-messages",
		GenerationID: "provision-example-async-v0-ffffffffffff", Revision: "provision-example-async-v0", ArtifactDigest: activeDigest,
		SystemdUnit: "provision-lab-consumer-ffffffffffff.service", Admission: "gated",
		Drain: config.WorkerDrain{Mode: "bounded-in-flight", MaxDuration: "30s"}, Rollout: "required",
	}
	active := installedWorkerGeneration{SchemaVersion: asyncGenerationSchema, Input: activeInput, Executable: "provision-example-async-worker", Verified: true, Active: true}
	workersRoot := filepath.Join(paths.environmentHome, "workers")
	if err := os.MkdirAll(workersRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(workersRoot, "active.json"), active, 0444); err != nil {
		t.Fatal(err)
	}
	gatePath := workerGatePath(paths, activeInput.GenerationID)
	if err := os.MkdirAll(filepath.Dir(gatePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gatePath, []byte("open\n"), 0644); err != nil {
		t.Fatal(err)
	}
	state := exampleWorkerState{SchemaVersion: "provision.dev/example-async-worker-state/v1alpha1", ApplicationRevision: activeInput.Revision, WorkerArtifactDigest: activeInput.ArtifactDigest, Queue: activeInput.QueueLogicalID, Connected: true, Consuming: true}
	if err := writeJSONAtomic(workerStatePath(paths, activeInput.GenerationID), state, 0644); err != nil {
		t.Fatal(err)
	}
	candidate.Previous = &host.WorkerGenerationStatus{ID: activeInput.GenerationID, Revision: activeInput.Revision, ArtifactDigest: activeInput.ArtifactDigest, SystemdUnit: activeInput.SystemdUnit, Active: true, Gate: "open", UnitActive: true, QueueConnected: true}
	before, err := os.ReadFile(filepath.Join(workersRoot, "active.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAsyncWorkerInput(candidate, record, paths); err != nil {
		t.Fatalf("distinct candidate rejected: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(workersRoot, "active.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("candidate validation mutated the active Worker: %v", err)
	}
	tampered := candidate
	tampered.Previous = &host.WorkerGenerationStatus{ID: activeInput.GenerationID, Revision: activeInput.Revision, ArtifactDigest: candidate.ArtifactDigest, SystemdUnit: activeInput.SystemdUnit}
	if err := validateAsyncWorkerInput(tampered, record, paths); err == nil || !strings.Contains(err.Error(), "separate immutable Generation") {
		t.Fatalf("candidate matching the planned active Artifact was accepted: %v", err)
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
