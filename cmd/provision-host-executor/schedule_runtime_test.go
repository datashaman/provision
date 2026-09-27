package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provision/internal/config"
	"provision/internal/scheduler"
)

func runtimeRecord(t *testing.T, overlap string, attempts int) installedScheduleRecord {
	t.Helper()
	root := t.TempDir()
	return installedScheduleRecord{
		SchemaVersion: scheduleRecordSchema, Environment: "lab", Component: "every-minute", Task: "publish",
		TaskGenerationID: "revision-a-task", TaskUnit: "provision-lab-publish-a@.service",
		ApplicationRevision: "revision-a", ConfigurationDigest: "sha256:" + strings.Repeat("a", 64),
		InputReferences: []string{"queue:provision-lab-messages"},
		DaylightSaving:  "wall-clock", Overlap: overlap, Retry: config.ScheduleRetry{MaxAttempts: attempts, Delay: "10s"},
		MissedRun: config.ScheduleMissedRun{Mode: "skip"}, Failure: "record", FencingToken: 7,
		LedgerSchema: scheduler.SchemaVersion, LedgerPath: filepath.Join(root, "ledger.db"), TaskEvidencePath: filepath.Join(root, "task.jsonl"),
	}
}

func invocationFromUnit(unit string) string {
	return strings.TrimSuffix(strings.SplitN(unit, "@", 2)[1], ".service")
}

func confirmTask(t *testing.T, path, unit string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"Event":"message_confirmed","MessageID":"message-1","InvocationID":"` + invocationFromUnit(unit) + `"}` + "\n")
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestScheduleRuntimeRetriesKeepOccurrenceIdentityAndGeneration(t *testing.T) {
	record := runtimeRecord(t, "forbid", 3)
	now := time.Date(2026, 9, 27, 10, 15, 0, 0, time.UTC)
	var units []string
	start := func(unit string) (string, string, error) {
		units = append(units, unit)
		if len(units) == 1 {
			return "timed-out", "systemd-a", errors.New("injected timeout")
		}
		confirmTask(t, record.TaskEvidencePath, unit)
		return "succeeded", "systemd-b", nil
	}
	run := func() error {
		return runScheduleRuntimeWith([]string{"run", "--environment", "lab", "--schedule", "every-minute"},
			func(_, _ string) (installedScheduleRecord, error) { return record, nil }, func() time.Time { return now },
			start, func(string) (string, string, error) { return "running", "systemd-a", nil }, func(string) error { return nil }, &bytes.Buffer{})
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "injected timeout") {
		t.Fatalf("first attempt: %v", err)
	}
	store, err := scheduler.Open(record.LedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, first, err := store.Latest(context.Background(), record.Component)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if first.Outcome != "timed-out" || len(first.Attempts) != 1 || first.Attempts[0].Outcome != "timed-out" {
		t.Fatalf("timeout history: %+v", first)
	}
	// The installed Schedule may change while an older occurrence is pending.
	record.TaskGenerationID = "revision-b-task"
	record.TaskUnit = "provision-lab-publish-b@.service"
	record.ApplicationRevision = "revision-b"
	now = now.Add(5 * time.Second)
	if err := run(); err != nil || len(units) != 1 {
		t.Fatalf("early retry launched: %v %v", err, units)
	}
	now = now.Add(5 * time.Second)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 || units[0] != units[1] || !strings.HasPrefix(units[1], "provision-lab-publish-a@") {
		t.Fatalf("retry changed generation or identity: %v", units)
	}
	store, err = scheduler.Open(record.LedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	occurrence, invocation, err := store.Latest(context.Background(), record.Component)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.OccurrenceID != occurrence.ID || invocation.TaskGenerationID != "revision-a-task" || invocation.ApplicationRevision != "revision-a" || invocation.ConfigurationDigest != "sha256:"+strings.Repeat("a", 64) || len(invocation.InputReferences) != 1 || invocation.InputReferences[0] != "queue:provision-lab-messages" || invocation.Outcome != "succeeded" || len(invocation.Attempts) != 2 || invocation.Attempts[0].Outcome != "timed-out" || invocation.Attempts[1].Outcome != "succeeded" {
		t.Fatalf("retry lost durable identity, inputs, or attempt history: %+v %+v", occurrence, invocation)
	}
}

func TestScheduleRuntimeEnforcesOverlapFromRunningLedgerState(t *testing.T) {
	for _, overlap := range []string{"forbid", "allow"} {
		t.Run(overlap, func(t *testing.T) {
			record := runtimeRecord(t, overlap, 2)
			now := time.Date(2026, 9, 27, 10, 15, 0, 0, time.UTC)
			starts := 0
			run := func() error {
				return runScheduleRuntimeWith([]string{"run", "--environment", "lab", "--schedule", "every-minute"},
					func(_, _ string) (installedScheduleRecord, error) { return record, nil }, func() time.Time { return now },
					func(string) (string, string, error) { starts++; return "running", "systemd-a", nil },
					func(string) (string, string, error) { return "running", "systemd-a", nil }, func(string) error { return nil }, &bytes.Buffer{})
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if err := run(); err != nil {
				t.Fatal(err)
			}
			store, err := scheduler.Open(record.LedgerPath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			occurrence, invocation, err := store.Latest(context.Background(), record.Component)
			if err != nil {
				t.Fatal(err)
			}
			if overlap == "forbid" && (starts != 1 || occurrence.Disposition != "skipped-overlap" || len(invocation.Attempts) != 0) {
				t.Fatalf("forbidden overlap ran: %d %+v %+v", starts, occurrence, invocation)
			}
			if overlap == "allow" && (starts != 2 || invocation.Outcome != "running" || len(invocation.Attempts) != 1) {
				t.Fatalf("allowed overlap did not run: %d %+v", starts, invocation)
			}
			occurrences, invocations, err := store.Recent(context.Background(), record.Component, 20)
			if err != nil || len(occurrences) != 2 || len(invocations) != 2 || occurrences[0].ID == occurrences[1].ID || invocations[0].ID == invocations[1].ID || invocations[0].DeliverySemantics != "at-least-once" {
				t.Fatalf("status lost occurrence or invocation history: %v %+v %+v", err, occurrences, invocations)
			}
		})
	}
}

func TestScheduleRuntimeRecoversAmbiguousDeliveryWithSameInvocation(t *testing.T) {
	record := runtimeRecord(t, "forbid", 2)
	now := time.Date(2026, 9, 27, 10, 15, 0, 0, time.UTC)
	store, err := scheduler.Open(record.LedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, invocation, _, err := store.RecordDue(context.Background(), scheduler.DueInput{
		Schedule: record.Component, Task: record.Task, TaskGenerationID: record.TaskGenerationID, TaskUnit: record.TaskUnit,
		ApplicationRevision: record.ApplicationRevision, ConfigurationDigest: record.ConfigurationDigest,
		FencingToken: record.FencingToken, DueAt: now, MaxAttempts: 2, RetryDelay: 10 * time.Second, Overlap: "forbid",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := taskInstanceUnit(record.TaskUnit, invocation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAttempt(context.Background(), invocation.ID, unit, now); err != nil {
		t.Fatal(err)
	}
	store.Close()
	starts := 0
	run := func() error {
		return runScheduleRuntimeWith([]string{"run", "--environment", "lab", "--schedule", "every-minute"},
			func(_, _ string) (installedScheduleRecord, error) { return record, nil }, func() time.Time { return now },
			func(unit string) (string, string, error) {
				starts++
				confirmTask(t, record.TaskEvidencePath, unit)
				return "succeeded", "systemd-b", nil
			},
			func(string) (string, string, error) { return "succeeded", "systemd-old", nil }, func(string) error { return nil }, &bytes.Buffer{})
	}
	if err := run(); err != nil || starts != 0 {
		t.Fatalf("unproved delivery was replayed immediately: %v %d", err, starts)
	}
	now = now.Add(10 * time.Second)
	if err := run(); err != nil || starts != 1 {
		t.Fatalf("retry did not use stable identity: %v %d", err, starts)
	}
	store, err = scheduler.Open(record.LedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, latest, err := store.Latest(context.Background(), record.Component)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != invocation.ID || len(latest.Attempts) != 2 || latest.Attempts[0].Outcome != "uncertain" || latest.Attempts[1].Outcome != "succeeded" {
		t.Fatalf("ambiguous attempt was lost or replaced: %+v", latest)
	}
}

func TestScheduleRuntimeCompletesLongTaskFromRecordedSystemdLaunch(t *testing.T) {
	record := runtimeRecord(t, "forbid", 2)
	now := time.Date(2026, 9, 27, 10, 15, 0, 0, time.UTC)
	starts := 0
	var firstUnit string
	run := func(observedID string) error {
		return runScheduleRuntimeWith([]string{"run", "--environment", "lab", "--schedule", "every-minute"},
			func(_, _ string) (installedScheduleRecord, error) { return record, nil }, func() time.Time { return now },
			func(unit string) (string, string, error) {
				starts++
				firstUnit = unit
				return "running", "systemd-a", nil
			},
			func(string) (string, string, error) {
				confirmTask(t, record.TaskEvidencePath, firstUnit)
				return "succeeded", observedID, nil
			}, func(string) error { return nil }, &bytes.Buffer{})
	}
	if err := run("systemd-a"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	if err := run("systemd-a"); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("completed Task relaunched: %d", starts)
	}
	store, err := scheduler.Open(record.LedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, invocation, err := store.Latest(context.Background(), record.Component)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Outcome != "succeeded" || len(invocation.Attempts) != 1 || invocation.Attempts[0].SystemdInvocationID != "systemd-a" {
		t.Fatalf("recorded launch was not reconciled: %+v", invocation)
	}
}

func TestCompletedRetainedOneshotIsNotReportedRunning(t *testing.T) {
	for _, tc := range []struct{ properties, want string }{
		{"ActiveState=activating\nSubState=start\nResult=success\nJob=42/start\n", "running"},
		{"ActiveState=active\nSubState=exited\nResult=success\nJob=\n", "succeeded"},
		{"ActiveState=failed\nSubState=failed\nResult=timeout\nJob=\n", "timed-out"},
	} {
		got, err := classifyTaskUnitState(tc.properties)
		if err != nil || got != tc.want {
			t.Fatalf("systemd state %q: got %q, err %v", tc.properties, got, err)
		}
	}
}

func TestFailedTaskAttemptRemainsVisibleAfterSuccessfulRetry(t *testing.T) {
	record := runtimeRecord(t, "forbid", 2)
	now := time.Date(2026, 9, 27, 10, 15, 0, 0, time.UTC)
	starts := 0
	run := func() error {
		return runScheduleRuntimeWith([]string{"run", "--environment", "lab", "--schedule", "every-minute"},
			func(_, _ string) (installedScheduleRecord, error) { return record, nil }, func() time.Time { return now },
			func(unit string) (string, string, error) {
				starts++
				if starts == 1 {
					return "failed", "systemd-a", errors.New("injected exit failure")
				}
				confirmTask(t, record.TaskEvidencePath, unit)
				return "succeeded", "systemd-b", nil
			}, func(string) (string, string, error) { return "running", "systemd-a", nil }, func(string) error { return nil }, &bytes.Buffer{})
	}
	if err := run(); err == nil {
		t.Fatal("failed Task returned success")
	}
	now = now.Add(10 * time.Second)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	store, err := scheduler.Open(record.LedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, invocation, err := store.Latest(context.Background(), record.Component)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Outcome != "succeeded" || len(invocation.Attempts) != 2 || invocation.Attempts[0].Outcome != "failed" || invocation.Attempts[1].Outcome != "succeeded" || invocation.Attempts[0].CompletedAt == nil {
		t.Fatalf("failed attempt was erased: %+v", invocation)
	}
}

func TestRetryRunningAtNextDueTimeSkipsForbiddenOverlap(t *testing.T) {
	record := runtimeRecord(t, "forbid", 2)
	now := time.Date(2026, 9, 27, 10, 15, 0, 0, time.UTC)
	starts := 0
	run := func() error {
		return runScheduleRuntimeWith([]string{"run", "--environment", "lab", "--schedule", "every-minute"},
			func(_, _ string) (installedScheduleRecord, error) { return record, nil }, func() time.Time { return now },
			func(string) (string, string, error) {
				starts++
				if starts == 1 {
					return "failed", "systemd-a", errors.New("injected failure")
				}
				return "running", "systemd-b", nil
			}, func(string) (string, string, error) { return "running", "systemd-b", nil }, func(string) error { return nil }, &bytes.Buffer{})
	}
	if err := run(); err == nil {
		t.Fatal("first failure was hidden")
	}
	now = now.Add(time.Minute)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	store, err := scheduler.Open(record.LedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	occurrences, invocations, err := store.Recent(context.Background(), record.Component, 2)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 2 || occurrences[0].Disposition != "skipped-overlap" || invocations[0].Outcome != "skipped-overlap" || len(invocations[0].Attempts) != 0 || len(invocations[1].Attempts) != 2 || invocations[1].Outcome != "running" {
		t.Fatalf("retry admitted forbidden new occurrence: starts=%d occurrences=%+v invocations=%+v", starts, occurrences, invocations)
	}
}
