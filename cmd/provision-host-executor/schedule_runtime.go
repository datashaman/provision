package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"provision/internal/scheduler"
)

const scheduleRecordSchema = "provision.dev/installed-schedule/v1alpha1"

type installedScheduleRecord struct {
	SchemaVersion       string `json:"schemaVersion"`
	Environment         string `json:"environment"`
	Component           string `json:"component"`
	Task                string `json:"task"`
	TaskGenerationID    string `json:"taskGenerationId"`
	TaskUnit            string `json:"taskUnit"`
	ApplicationRevision string `json:"applicationRevision"`
	ConfigurationDigest string `json:"configurationDigest"`
	TimerUnit           string `json:"timerUnit"`
	Expression          string `json:"expression"`
	Timezone            string `json:"timezone"`
	AppletDigest        string `json:"appletDigest"`
	LedgerSchema        string `json:"ledgerSchema"`
	LedgerPath          string `json:"ledgerPath"`
	TaskEvidencePath    string `json:"taskEvidencePath"`
	WorkerEvidencePath  string `json:"workerEvidencePath"`
	FencingToken        int64  `json:"fencingToken"`
}

func runScheduleRuntime(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: provision-runtime-schedule run --environment NAME --schedule NAME")
	}
	flags := flag.NewFlagSet("runtime schedule", flag.ContinueOnError)
	environment := flags.String("environment", "", "Environment identity")
	scheduleName := flags.String("schedule", "", "Schedule component")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || !identifier.MatchString(*environment) || !deploymentIdentifier.MatchString(*scheduleName) {
		return errors.New("runtime schedule requires valid --environment and --schedule")
	}
	record, err := readInstalledSchedule(*environment, *scheduleName)
	if err != nil {
		return err
	}
	if record.LedgerSchema != scheduler.SchemaVersion {
		return errors.New("installed Schedule ledger schema is unsupported")
	}
	store, err := scheduler.Open(record.LedgerPath)
	if err != nil {
		return fmt.Errorf("open occurrence ledger: %w", err)
	}
	defer store.Close()
	now := time.Now().UTC()
	occurrence, invocation, inserted, err := store.RecordDue(context.Background(), scheduler.DueInput{
		Schedule: record.Component, Task: record.Task, TaskGenerationID: record.TaskGenerationID,
		ApplicationRevision: record.ApplicationRevision, ConfigurationDigest: record.ConfigurationDigest,
		FencingToken: record.FencingToken, DueAt: now,
	}, now)
	if err != nil {
		return fmt.Errorf("record due occurrence: %w", err)
	}
	if !inserted && invocation.Outcome == "succeeded" {
		return writeRuntimeResult(occurrence.ID, invocation.ID, invocation.Outcome)
	}
	instance, err := taskInstanceUnit(record.TaskUnit, invocation.ID)
	if err != nil {
		return err
	}
	attempt, err := store.BeginAttempt(context.Background(), invocation.ID, instance, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("record Task attempt before launch: %w", err)
	}
	command := exec.Command("systemctl", "start", instance)
	output, startErr := command.CombinedOutput()
	outcome := "succeeded"
	if startErr != nil {
		outcome = "failed"
	}
	if err := store.CompleteAttempt(context.Background(), invocation.ID, attempt, outcome, record.TaskEvidencePath, time.Now().UTC()); err != nil {
		return fmt.Errorf("record Task attempt outcome: %w", err)
	}
	if startErr != nil {
		return fmt.Errorf("Task unit %s failed: %w: %s", instance, startErr, bytes.TrimSpace(output))
	}
	return writeRuntimeResult(occurrence.ID, invocation.ID, outcome)
}

func readInstalledSchedule(environment, component string) (installedScheduleRecord, error) {
	path := installedSchedulePath(environment, component)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return installedScheduleRecord{}, os.ErrNotExist
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0444 || !ownedByExecutor(info) {
		return installedScheduleRecord{}, errors.New("installed Schedule record is missing or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64<<10 {
		return installedScheduleRecord{}, errors.New("installed Schedule record cannot be read")
	}
	var record installedScheduleRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return installedScheduleRecord{}, errors.New("installed Schedule record is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || record.SchemaVersion != scheduleRecordSchema || record.Environment != environment || record.Component != component || record.FencingToken < 1 {
		return installedScheduleRecord{}, errors.New("installed Schedule record does not match the invocation")
	}
	return record, nil
}

func taskInstanceUnit(template, invocation string) (string, error) {
	if !strings.HasSuffix(template, "@.service") || !deploymentIdentifier.MatchString(invocation) {
		return "", errors.New("installed Task template or Invocation identity is invalid")
	}
	return strings.TrimSuffix(template, "@.service") + "@" + invocation + ".service", nil
}

func installedSchedulePath(environment, component string) string {
	return filepath.Join("/var/lib/provision/environments", environment, "schedules", component, "schedule.json")
}

func writeRuntimeResult(occurrenceID, invocationID, outcome string) error {
	return json.NewEncoder(os.Stdout).Encode(struct {
		SchemaVersion string `json:"schemaVersion"`
		OccurrenceID  string `json:"occurrenceId"`
		InvocationID  string `json:"invocationId"`
		Outcome       string `json:"outcome"`
	}{"provision.dev/schedule-runtime-result/v1alpha1", occurrenceID, invocationID, outcome})
}
