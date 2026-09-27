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

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/scheduler"
)

const scheduleRecordSchema = "provision.dev/installed-schedule/v1alpha1"

type installedScheduleRecord struct {
	SchemaVersion       string                   `json:"schemaVersion"`
	Environment         string                   `json:"environment"`
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
	AppletDigest        string                   `json:"appletDigest"`
	LedgerSchema        string                   `json:"ledgerSchema"`
	LedgerPath          string                   `json:"ledgerPath"`
	TaskEvidencePath    string                   `json:"taskEvidencePath"`
	InputReferences     []string                 `json:"inputReferences,omitempty"`
	WorkerEvidencePath  string                   `json:"workerEvidencePath"`
	FencingToken        int64                    `json:"fencingToken"`
}

func runScheduleRuntime(args []string) error {
	return runScheduleRuntimeWith(args, readInstalledSchedule, time.Now, startTaskUnit, taskUnitOutcome, stopTaskUnit, os.Stdout)
}

func runScheduleRuntimeWith(args []string, read func(string, string) (installedScheduleRecord, error), clock func() time.Time, start func(string) (string, string, error), observe func(string) (string, string, error), stop func(string) error, output io.Writer) error {
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
	record, err := read(*environment, *scheduleName)
	if err != nil {
		return err
	}
	if record.LedgerSchema != scheduler.SchemaVersion {
		return errors.New("installed Schedule ledger schema is unsupported")
	}
	if record.DaylightSaving != "wall-clock" || (record.Overlap != "forbid" && record.Overlap != "allow") || record.Retry.MaxAttempts < 1 || record.Retry.MaxAttempts > 10 || record.Failure != "record" {
		return errors.New("installed Schedule policy exceeds the initial runtime contract")
	}
	if _, err := scheduler.Evaluate(scheduler.EvaluationInput{Expression: record.Expression, Timezone: record.Timezone, DaylightSaving: record.DaylightSaving, MissedRun: record.MissedRun, Now: clock().UTC()}); err != nil {
		return fmt.Errorf("installed Schedule timing policy is invalid: %w", err)
	}
	retryDelay, err := time.ParseDuration(record.Retry.Delay)
	if err != nil || retryDelay < time.Second || retryDelay > time.Hour {
		return errors.New("installed Schedule retry delay is invalid")
	}
	return executeSchedule(record, clock, start, observe, stop, output)
}

func stopTaskUnit(instance string) error {
	output, err := exec.Command("systemctl", "stop", instance).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stop completed Task unit %s: %w: %s", instance, err, bytes.TrimSpace(output))
	}
	return nil
}

func startTaskUnit(instance string) (string, string, error) {
	previousInvocation, _ := systemdInvocationID(instance)
	command := exec.Command("systemctl", "start", "--no-block", instance)
	output, err := command.CombinedOutput()
	if err != nil {
		return "failed", "", fmt.Errorf("Task unit %s could not start: %w: %s", instance, err, bytes.TrimSpace(output))
	}
	// Normal short Tasks still complete in this applet run. Long Tasks leave the
	// timer service free to record a subsequent occurrence while they run.
	deadline := time.Now().Add(5 * time.Second)
	for {
		outcome, currentInvocation, err := taskUnitOutcome(instance)
		if err != nil {
			return "running", "", err
		}
		if currentInvocation == "" || currentInvocation == previousInvocation {
			outcome = "running"
		}
		if outcome == "failed" || outcome == "timed-out" {
			return outcome, currentInvocation, fmt.Errorf("Task unit %s finished with %s", instance, outcome)
		}
		if outcome != "running" {
			return outcome, currentInvocation, nil
		}
		if time.Now().After(deadline) {
			if currentInvocation == previousInvocation {
				currentInvocation = ""
			}
			return "running", currentInvocation, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func systemdInvocationID(instance string) (string, error) {
	output, err := exec.Command("systemctl", "show", "--property=InvocationID", "--value", instance).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cannot inspect Task unit %s invocation: %w: %s", instance, err, bytes.TrimSpace(output))
	}
	return strings.TrimSpace(string(output)), nil
}

func taskUnitOutcome(instance string) (string, string, error) {
	output, err := exec.Command("systemctl", "show", "--property=ActiveState", "--property=SubState", "--property=Result", "--property=Job", instance).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("cannot inspect Task unit %s: %w: %s", instance, err, bytes.TrimSpace(output))
	}
	systemdID, err := systemdInvocationID(instance)
	if err != nil {
		return "", "", err
	}
	outcome, err := classifyTaskUnitState(string(output))
	return outcome, systemdID, err
}

func classifyTaskUnitState(output string) (string, error) {
	properties := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			properties[parts[0]] = parts[1]
		}
	}
	if properties["ActiveState"] == "active" && properties["SubState"] == "exited" && properties["Result"] == "success" {
		return "succeeded", nil
	}
	if properties["Job"] != "" || properties["ActiveState"] == "active" || properties["ActiveState"] == "activating" || properties["ActiveState"] == "deactivating" {
		return "running", nil
	}
	if properties["ActiveState"] == "inactive" && properties["Result"] == "success" {
		return "succeeded", nil
	}
	if properties["Result"] == "timeout" {
		return "timed-out", nil
	}
	if properties["ActiveState"] == "failed" {
		return "failed", nil
	}
	return "", fmt.Errorf("Task unit has indeterminate state: %s", strings.TrimSpace(output))
}

// The injected clock and systemd boundary exercise the same runtime path as
// the installed applet without requiring a privileged test host.
func executeSchedule(record installedScheduleRecord, clock func() time.Time, start func(string) (string, string, error), observe func(string) (string, string, error), stop func(string) error, output io.Writer) error {
	store, err := scheduler.Open(record.LedgerPath)
	if err != nil {
		return fmt.Errorf("open occurrence ledger: %w", err)
	}
	defer store.Close()
	now := clock().UTC()
	retryDelay, _ := time.ParseDuration(record.Retry.Delay)
	lastWall, err := store.LatestWallAt(context.Background(), record.Component)
	if err != nil {
		return fmt.Errorf("read latest Schedule occurrence: %w", err)
	}
	decisions, err := scheduler.Evaluate(scheduler.EvaluationInput{
		Expression: record.Expression, Timezone: record.Timezone, DaylightSaving: record.DaylightSaving,
		MissedRun: record.MissedRun, LastWallAt: lastWall, Now: now,
	})
	if err != nil {
		return fmt.Errorf("evaluate Schedule policy: %w", err)
	}
	input := scheduler.DueInput{
		Schedule: record.Component, Task: record.Task, TaskGenerationID: record.TaskGenerationID,
		ApplicationRevision: record.ApplicationRevision, ConfigurationDigest: record.ConfigurationDigest,
		FencingToken: record.FencingToken, TaskUnit: record.TaskUnit,
		InputReferences: record.InputReferences, MaxAttempts: record.Retry.MaxAttempts,
		RetryDelay: retryDelay, Overlap: record.Overlap,
	}
	var occurrence host.ScheduleOccurrenceStatus
	var invocation host.TaskInvocationStatus
	inserted := false
	if len(decisions) == 0 {
		latestOccurrence, latestInvocation, err := store.Latest(context.Background(), record.Component)
		if err != nil {
			return fmt.Errorf("read latest Schedule status: %w", err)
		}
		if latestOccurrence != nil && latestInvocation != nil {
			occurrence = *latestOccurrence
			invocation = *latestInvocation
		}
	} else {
		for _, decision := range decisions {
			input.DueAt = decision.DueAt
			input.WallAt = decision.WallAt
			if decision.Disposition == "recorded" {
				occurrence, invocation, inserted, err = store.RecordDue(context.Background(), input, now)
			} else {
				occurrence, invocation, inserted, err = store.RecordSkipped(context.Background(), input, now, decision.Disposition)
			}
			if err != nil {
				return fmt.Errorf("record %s Schedule occurrence: %w", decision.Disposition, err)
			}
		}
	}
	if !inserted && (invocation.Outcome == "succeeded" || invocation.Outcome == "skipped-overlap") {
		return writeRuntimeResultTo(output, occurrence.ID, invocation.ID, invocation.Outcome)
	}
	pending, err := store.Pending(context.Background(), record.Component)
	if err != nil {
		return fmt.Errorf("read pending Task Invocations: %w", err)
	}
	for _, candidate := range pending {
		if candidate.Outcome == "running" {
			instance, err := taskInstanceUnit(candidate.TaskUnit, candidate.ID)
			if err != nil {
				return err
			}
			observed, systemdID, err := observe(instance)
			if err != nil {
				return err
			}
			if observed != "running" {
				if observed != "succeeded" && observed != "failed" && observed != "timed-out" {
					return errors.New("Task unit observation is unsupported")
				}
				last := candidate.Attempts[len(candidate.Attempts)-1]
				if last.SystemdInvocationID == "" || systemdID != last.SystemdInvocationID {
					observed = "uncertain"
				} else if observed == "succeeded" {
					if _, confirmed := taskConfirmedMessage(record.TaskEvidencePath, candidate.ID); !confirmed {
						observed = "uncertain"
					}
				}
				if err := store.CompleteAttempt(context.Background(), candidate.ID, last.Number, observed, record.TaskEvidencePath, clock().UTC()); err != nil {
					return err
				}
				if observed != "uncertain" {
					if err := stop(instance); err != nil {
						return err
					}
				}
				candidate.Outcome = observed
				if candidate.ID == invocation.ID {
					invocation.Outcome = observed
				}
			}
		}
		if candidate.Outcome == "running" || candidate.Outcome == "succeeded" {
			continue
		}
		instance, err := taskInstanceUnit(candidate.TaskUnit, candidate.ID)
		if err != nil {
			return err
		}
		attempt, err := store.BeginAttempt(context.Background(), candidate.ID, instance, clock().UTC())
		if errors.Is(err, scheduler.ErrOverlap) {
			if candidate.ID == invocation.ID && candidate.Outcome == "pending" {
				if err := store.SkipOverlap(context.Background(), candidate.ID); errors.Is(err, scheduler.ErrOverlap) {
					continue
				} else if err != nil {
					return err
				}
				return writeRuntimeResultTo(output, occurrence.ID, invocation.ID, "skipped-overlap")
			}
			continue
		}
		if errors.Is(err, scheduler.ErrRetryNotDue) || errors.Is(err, scheduler.ErrAttemptsExhausted) || errors.Is(err, scheduler.ErrAttemptRunning) {
			continue
		}
		if err != nil {
			return fmt.Errorf("record Task attempt before launch: %w", err)
		}
		outcome, systemdID, launchErr := start(instance)
		if outcome != "succeeded" && outcome != "failed" && outcome != "timed-out" && outcome != "running" {
			return errors.New("Task launch returned unsupported outcome")
		}
		if systemdID != "" {
			if err := store.MarkLaunched(context.Background(), candidate.ID, attempt, systemdID); err != nil {
				return fmt.Errorf("record Task launch identity: %w", err)
			}
		}
		if outcome != "running" {
			if outcome == "succeeded" {
				if _, confirmed := taskConfirmedMessage(record.TaskEvidencePath, candidate.ID); !confirmed {
					outcome = "uncertain"
				}
			}
			if err := store.CompleteAttempt(context.Background(), candidate.ID, attempt, outcome, record.TaskEvidencePath, clock().UTC()); err != nil {
				return fmt.Errorf("record Task attempt outcome: %w", err)
			}
			if systemdID != "" {
				if err := stop(instance); err != nil {
					return err
				}
			}
		}
		if launchErr != nil {
			return launchErr
		}
		if outcome == "running" && candidate.ID != invocation.ID && record.Overlap == "forbid" && invocation.Outcome == "pending" {
			if err := store.SkipOverlap(context.Background(), invocation.ID); err != nil && !errors.Is(err, scheduler.ErrOverlap) {
				return err
			}
		}
		return writeRuntimeResultTo(output, candidate.OccurrenceID, candidate.ID, outcome)
	}
	return writeRuntimeResultTo(output, occurrence.ID, invocation.ID, invocation.Outcome)
}

func readInstalledSchedule(environment, component string) (installedScheduleRecord, error) {
	return readInstalledSchedulePath(installedSchedulePath(environment, component), environment, component)
}

func readInstalledSchedulePath(path, environment, component string) (installedScheduleRecord, error) {
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
	return writeRuntimeResultTo(os.Stdout, occurrenceID, invocationID, outcome)
}

func writeRuntimeResultTo(output io.Writer, occurrenceID, invocationID, outcome string) error {
	return json.NewEncoder(output).Encode(struct {
		SchemaVersion     string `json:"schemaVersion"`
		OccurrenceID      string `json:"occurrenceId"`
		InvocationID      string `json:"invocationId"`
		Outcome           string `json:"outcome"`
		DeliverySemantics string `json:"deliverySemantics"`
	}{"provision.dev/schedule-runtime-result/v1alpha1", occurrenceID, invocationID, outcome, "at-least-once"})
}
