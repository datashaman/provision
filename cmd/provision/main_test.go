package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"provision/internal/host"
	"provision/internal/planner"
)

func TestAuthorizedReleasePreparationIsJournaledAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	configPath := writeLocalHostConfiguration(t, dir)
	writeLocalExecutorSudo(t, dir)
	statePath := filepath.Join(dir, "state.db")
	privateKeyPath := filepath.Join(dir, "authority.key")
	publicKeyPath := filepath.Join(dir, "authority.pub")
	executionLog := filepath.Join(dir, "execution.json")

	keygen := exec.Command("go", "run", ".", "authority", "keygen", "--private-key", privateKeyPath, "--public-key", publicKeyPath)
	keyOutput, err := keygen.CombinedOutput()
	if err != nil {
		t.Fatalf("authority key generation failed: %v\n%s", err, keyOutput)
	}
	var key struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(keyOutput, &key); err != nil || !strings.HasPrefix(key.ID, "sha256:") {
		t.Fatalf("invalid authority key result: %v\n%s", err, keyOutput)
	}

	commandEnv := append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_AUTHORITY_KEY_ID="+key.ID,
		"FAKE_EXECUTION_LOG="+executionLog,
	)
	preview := exec.Command("go", "run", ".", "plan", "preview", "--file", configPath, "--state", statePath)
	preview.Env = commandEnv
	previewOutput, err := preview.CombinedOutput()
	if err != nil {
		t.Fatalf("local Plan preview failed: %v\n%s", err, previewOutput)
	}
	planID := decodePlanID(t, previewOutput)

	approve := exec.Command("go", "run", ".", "plan", "approve",
		"--file", configPath,
		"--plan", planID,
		"--actor", authenticatedActor(t),
		"--state", statePath,
	)
	approve.Env = commandEnv
	if output, err := approve.CombinedOutput(); err != nil {
		t.Fatalf("local Plan approval failed: %v\n%s", err, output)
	}

	execute := exec.Command("go", "run", ".", "deployment", "execute",
		"--plan", planID,
		"--operation", "op-01",
		"--state", statePath,
		"--signing-key", privateKeyPath,
	)
	execute.Env = commandEnv
	executeOutput, err := execute.CombinedOutput()
	if err != nil || !strings.Contains(string(executeOutput), `"outcome": "succeeded"`) {
		t.Fatalf("authorized preparation failed: %v\n%s", err, executeOutput)
	}

	status := exec.Command("go", "run", ".", "deployment", "status", "--plan", planID, "--state", statePath)
	statusOutput, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("journal status after restart failed: %v\n%s", err, statusOutput)
	}
	for _, evidence := range []string{`"kind": "intent"`, `"kind": "outcome"`, `"outcome": "succeeded"`, `"status": "staged"`} {
		if !strings.Contains(string(statusOutput), evidence) {
			t.Fatalf("journal omitted %s:\n%s", evidence, statusOutput)
		}
	}
	envelope, err := os.ReadFile(executionLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envelope), `"kind":"stageArtifact"`) || strings.Contains(string(envelope), `"command"`) {
		t.Fatalf("host received an untyped or command-bearing mutation: %s", envelope)
	}
}

func TestAuthorizedRemoteReleasePreparationIsJournaledAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	configPath := writeRemoteHostConfiguration(t, dir)
	writeRemoteExecutorSSH(t, dir)
	statePath := filepath.Join(dir, "state.db")
	privateKeyPath := filepath.Join(dir, "authority.key")
	publicKeyPath := filepath.Join(dir, "authority.pub")
	executionLog := filepath.Join(dir, "execution.json")
	sshLog := filepath.Join(dir, "ssh.jsonl")
	keyID := generateAuthorityKey(t, privateKeyPath, publicKeyPath)

	commandEnv := append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_AUTHORITY_KEY_ID="+keyID,
		"FAKE_EXECUTION_LOG="+executionLog,
		"FAKE_SSH_LOG="+sshLog,
	)
	planID := previewAndApprove(t, configPath, statePath, commandEnv)

	execute := exec.Command("go", "run", ".", "deployment", "execute",
		"--plan", planID,
		"--operation", "op-01",
		"--state", statePath,
		"--signing-key", privateKeyPath,
	)
	execute.Env = commandEnv
	executeOutput, err := execute.CombinedOutput()
	if err != nil || !strings.Contains(string(executeOutput), `"outcome": "succeeded"`) {
		t.Fatalf("authorized remote preparation failed: %v\n%s", err, executeOutput)
	}

	status := exec.Command("go", "run", ".", "deployment", "status", "--plan", planID, "--state", statePath)
	statusOutput, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("remote journal status after restart failed: %v\n%s", err, statusOutput)
	}
	for _, evidence := range []string{`"kind": "intent"`, `"kind": "outcome"`, `"outcome": "succeeded"`, `"status": "staged"`} {
		if !strings.Contains(string(statusOutput), evidence) {
			t.Fatalf("remote journal omitted %s:\n%s", evidence, statusOutput)
		}
	}
	envelope, err := os.ReadFile(executionLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envelope), `"kind":"stageArtifact"`) || strings.Contains(string(envelope), `"command"`) {
		t.Fatalf("remote host received an untyped or command-bearing mutation: %s", envelope)
	}
	sshCalls, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"StrictHostKeyChecking=yes"`, `"` + authenticatedActor(t) + `@192.0.2.10"`, `"observe-artifact"`, `"execute"`} {
		if !strings.Contains(string(sshCalls), required) {
			t.Fatalf("remote execution omitted trusted SSH evidence %s:\n%s", required, sshCalls)
		}
	}
}

func TestRemoteConnectionLossRecordsExplicitUncertainOutcome(t *testing.T) {
	dir := t.TempDir()
	configPath := writeRemoteHostConfiguration(t, dir)
	writeRemoteExecutorSSH(t, dir)
	statePath := filepath.Join(dir, "state.db")
	privateKeyPath := filepath.Join(dir, "authority.key")
	publicKeyPath := filepath.Join(dir, "authority.pub")
	keyID := generateAuthorityKey(t, privateKeyPath, publicKeyPath)
	commandEnv := append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_AUTHORITY_KEY_ID="+keyID,
		"FAKE_SSH_LOG="+filepath.Join(dir, "ssh.jsonl"),
		"FAKE_EXECUTION_LOG="+filepath.Join(dir, "execution.json"),
		"FAKE_REMOTE_MODE=connection-loss",
		"FAKE_OBSERVE_MARKER="+filepath.Join(dir, "observed-once"),
	)
	planID := previewAndApprove(t, configPath, statePath, commandEnv)

	execute := exec.Command("go", "run", ".", "deployment", "execute",
		"--plan", planID,
		"--operation", "op-01",
		"--state", statePath,
		"--signing-key", privateKeyPath,
	)
	execute.Env = commandEnv
	executeOutput, err := execute.CombinedOutput()
	if err == nil || !strings.Contains(string(executeOutput), "outcome recorded as uncertain") {
		t.Fatalf("connection loss did not produce an explicit uncertain outcome: %v\n%s", err, executeOutput)
	}

	status := exec.Command("go", "run", ".", "deployment", "status", "--plan", planID, "--state", statePath)
	statusOutput, statusErr := status.CombinedOutput()
	if statusErr != nil {
		t.Fatalf("uncertain remote journal status failed: %v\n%s", statusErr, statusOutput)
	}
	for _, evidence := range []string{`"kind": "intent"`, `"kind": "outcome"`, `"outcome": "uncertain"`, `"status": "uncertain"`, `"observed": "unknown"`} {
		if !strings.Contains(string(statusOutput), evidence) {
			t.Fatalf("uncertain remote journal omitted %s:\n%s", evidence, statusOutput)
		}
	}

	resume := exec.Command("go", "run", ".", "deployment", "resume",
		"--plan", planID,
		"--state", statePath,
		"--signing-key", privateKeyPath,
	)
	resume.Env = append(commandEnv, "FAKE_REMOTE_MODE=resume-satisfied")
	resumeOutput, err := resume.CombinedOutput()
	if err != nil || !strings.Contains(string(resumeOutput), `"outcome": "succeeded"`) || !strings.Contains(string(resumeOutput), `"status": "already-present"`) {
		t.Fatalf("interrupted preparation did not resume from fresh Host evidence: %v\n%s", err, resumeOutput)
	}

	status = exec.Command("go", "run", ".", "deployment", "status", "--plan", planID, "--state", statePath)
	statusOutput, statusErr = status.CombinedOutput()
	if statusErr != nil {
		t.Fatalf("resumed remote journal status failed: %v\n%s", statusErr, statusOutput)
	}
	for _, evidence := range []string{`"outcome": "uncertain"`, `"outcome": "succeeded"`, `"resumeOfAttemptId"`, `"status": "already-present"`} {
		if !strings.Contains(string(statusOutput), evidence) {
			t.Fatalf("resumed journal omitted %s:\n%s", evidence, statusOutput)
		}
	}
	sshCalls, readErr := os.ReadFile(filepath.Join(dir, "ssh.jsonl"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if count := strings.Count(string(sshCalls), `"execute"`); count != 1 {
		t.Fatalf("resume replayed the already-satisfied operation; execute calls = %d:\n%s", count, sshCalls)
	}
}

func generateAuthorityKey(t *testing.T, privateKeyPath, publicKeyPath string) string {
	t.Helper()
	keygen := exec.Command("go", "run", ".", "authority", "keygen", "--private-key", privateKeyPath, "--public-key", publicKeyPath)
	keyOutput, err := keygen.CombinedOutput()
	if err != nil {
		t.Fatalf("authority key generation failed: %v\n%s", err, keyOutput)
	}
	var key struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(keyOutput, &key); err != nil || !strings.HasPrefix(key.ID, "sha256:") {
		t.Fatalf("invalid authority key result: %v\n%s", err, keyOutput)
	}
	return key.ID
}

func previewAndApprove(t *testing.T, configPath, statePath string, commandEnv []string) string {
	t.Helper()
	preview := exec.Command("go", "run", ".", "plan", "preview", "--file", configPath, "--state", statePath)
	preview.Env = commandEnv
	previewOutput, err := preview.CombinedOutput()
	if err != nil {
		t.Fatalf("remote Plan preview failed: %v\n%s", err, previewOutput)
	}
	planID := decodePlanID(t, previewOutput)
	approve := exec.Command("go", "run", ".", "plan", "approve",
		"--file", configPath,
		"--plan", planID,
		"--actor", authenticatedActor(t),
		"--state", statePath,
	)
	approve.Env = commandEnv
	if output, err := approve.CombinedOutput(); err != nil {
		t.Fatalf("remote Plan approval failed: %v\n%s", err, output)
	}
	return planID
}

func TestProvisionRemoteSSHHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROVISION_REMOTE_SSH_HELPER") != "1" {
		return
	}
	separator := 0
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index + 1
			break
		}
	}
	arguments := os.Args[separator:]
	if logPath := os.Getenv("FAKE_SSH_LOG"); logPath != "" {
		log, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if log != nil {
			_ = json.NewEncoder(log).Encode(arguments)
			_ = log.Close()
		}
	}
	joined := strings.Join(arguments, " ")
	operator := authenticatedActor(t)
	if strings.Contains(joined, " inspect ") {
		writeReadyBootstrapInspection(t, operator, os.Getenv("FAKE_AUTHORITY_KEY_ID"))
		os.Exit(0)
	}
	if strings.Contains(joined, " observe-artifact ") {
		if os.Getenv("FAKE_REMOTE_MODE") == "resume-satisfied" {
			digest := arguments[len(arguments)-1]
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"status": "already-present", "path": "/var/lib/provision/artifacts/sha256/" + strings.TrimPrefix(digest, "sha256:"), "digest": digest, "size": 123,
			})
			os.Exit(0)
		}
		if os.Getenv("FAKE_REMOTE_MODE") == "connection-loss" {
			marker := os.Getenv("FAKE_OBSERVE_MARKER")
			if _, err := os.Stat(marker); err == nil {
				fmt.Fprintln(os.Stderr, "connection lost before remote state could be observed")
				os.Exit(255)
			}
			_ = os.WriteFile(marker, []byte("observed"), 0600)
		}
		digest := arguments[len(arguments)-1]
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"status": "absent", "path": "/var/lib/provision/artifacts/sha256/" + strings.TrimPrefix(digest, "sha256:"), "digest": digest, "size": 0,
		})
		os.Exit(0)
	}
	if strings.Contains(joined, " execute ") {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			os.Exit(2)
		}
		if path := os.Getenv("FAKE_EXECUTION_LOG"); path != "" {
			_ = os.WriteFile(path, data, 0600)
		}
		if os.Getenv("FAKE_REMOTE_MODE") == "connection-loss" {
			fmt.Fprintln(os.Stderr, "connection lost after dispatch")
			os.Exit(255)
		}
		var envelope struct {
			Authorization struct {
				Claim struct {
					PlanID       string `json:"planId"`
					OperationID  string `json:"operationId"`
					AttemptID    string `json:"attemptId"`
					FencingToken int64  `json:"fencingToken"`
				} `json:"claim"`
			} `json:"authorization"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			os.Exit(2)
		}
		claim := envelope.Authorization.Claim
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"schemaVersion": "provision.dev/host-operation-result/v1alpha1",
			"planId":        claim.PlanID, "operationId": claim.OperationID,
			"attemptId": claim.AttemptID, "fencingToken": claim.FencingToken,
			"outcome": "succeeded",
			"observation": map[string]any{
				"status": "staged",
				"path":   "/var/lib/provision/artifacts/sha256/bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5",
				"digest": "sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5",
				"size":   123,
			},
		})
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "unexpected remote SSH helper invocation", arguments)
	os.Exit(2)
}

func TestProvisionSudoHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROVISION_SUDO_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	args := os.Args[separator:]
	if len(args) >= 3 && args[0] == "-n" && args[2] == "inspect" {
		operator := authenticatedActor(t)
		keyID := os.Getenv("FAKE_AUTHORITY_KEY_ID")
		writeReadyBootstrapInspection(t, operator, keyID)
		os.Exit(0)
	}
	if len(args) >= 3 && args[0] == "-n" && args[2] == "execute" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("FAKE_EXECUTION_LOG"), data, 0600); err != nil {
			os.Exit(2)
		}
		var envelope struct {
			Authorization struct {
				Claim struct {
					PlanID       string `json:"planId"`
					OperationID  string `json:"operationId"`
					AttemptID    string `json:"attemptId"`
					FencingToken int64  `json:"fencingToken"`
				} `json:"claim"`
			} `json:"authorization"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			os.Exit(2)
		}
		claim := envelope.Authorization.Claim
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"schemaVersion": "provision.dev/host-operation-result/v1alpha1",
			"planId":        claim.PlanID, "operationId": claim.OperationID,
			"attemptId": claim.AttemptID, "fencingToken": claim.FencingToken,
			"outcome": "succeeded",
			"observation": map[string]any{
				"status": "staged",
				"path":   "/var/lib/provision/artifacts/sha256/bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5",
				"digest": "sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5",
				"size":   123,
			},
		})
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "unexpected sudo helper invocation", args)
	os.Exit(2)
}

func TestPlanPreviewIsDeterministicAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	writeBootstrapInspectionSSH(t, dir)

	first, err := runPlanPreviewCommand(t, dir, sshLog)
	if err != nil {
		t.Fatalf("plan preview failed: %v\n%s", err, first)
	}
	second, err := runPlanPreviewCommand(t, dir, sshLog)
	if err != nil {
		t.Fatalf("second plan preview failed: %v\n%s", err, second)
	}
	if string(first) != string(second) {
		t.Fatalf("equivalent inputs produced different plans:\n%s\n%s", first, second)
	}
	var plan struct {
		SchemaVersion string              `json:"schemaVersion"`
		ID            string              `json:"id"`
		Operations    []planner.Operation `json:"operations"`
	}
	if err := json.Unmarshal(first, &plan); err != nil {
		t.Fatalf("invalid plan JSON: %v\n%s", err, first)
	}
	if plan.SchemaVersion != "provision.dev/plan/v1alpha1" || !strings.HasPrefix(plan.ID, "sha256:") {
		t.Fatalf("unexpected plan identity: %+v", plan)
	}
	wantOperations := []string{"stageArtifact", "installGeneration", "startCandidate", "verifyCandidate", "switchEndpoint", "verifyActive", "drainPrevious", "retainPrevious"}
	if len(plan.Operations) != len(wantOperations) {
		t.Fatalf("operation count = %d, want %d: %s", len(plan.Operations), len(wantOperations), first)
	}
	for i, want := range wantOperations {
		if string(plan.Operations[i].Kind) != want {
			t.Fatalf("operation %d = %q, want %q", i, plan.Operations[i].Kind, want)
		}
	}
	drain := plan.Operations[6]
	if drain.ID != "op-07" || len(drain.DependsOn) != 1 || drain.DependsOn[0] != "op-06" || drain.Input.Drain == nil {
		t.Fatalf("drain operation is not bound after stable verification: %+v", drain)
	}
	if drain.Input.Drain.Mode != "bounded-http" || drain.Input.Drain.MaxDuration != "2s" || drain.Input.Drain.Endpoint.DrainPolicy != "caddy-graceful-config-reload" {
		t.Fatalf("drain operation omits declared HTTP handoff contract: %+v", drain.Input.Drain)
	}
	if drain.Input.Drain.Previous.ID != "provision-example-http-v0-aaaaaaaaaaaa" || drain.Input.Drain.Previous.SystemdUnit != "provision-lab-web-aaaaaaaaaaaa.service" {
		t.Fatalf("drain operation omits exact previous Generation: %+v", drain.Input.Drain.Previous)
	}
	retained := plan.Operations[7]
	if retained.ID != "op-08" || len(retained.DependsOn) != 1 || retained.DependsOn[0] != "op-07" || retained.Input.Retention == nil {
		t.Fatalf("retention operation is not bound after drain completion: %+v", retained)
	}
	if retained.Input.Retention.Policy != "rollback-window" || retained.Input.Retention.RollbackWindow != "30m0s" {
		t.Fatalf("retention operation omits declared rollback-window policy: %+v", retained.Input.Retention)
	}
	if retained.Input.Retention.Previous.ID != "provision-example-http-v0-aaaaaaaaaaaa" || retained.Input.Retention.Previous.SystemdUnit != "provision-lab-web-aaaaaaaaaaaa.service" || retained.Input.Retention.Previous.ReleaseDirectory != "/var/lib/provision/environments/lab/releases/provision-example-http-v0-aaaaaaaaaaaa" || retained.Input.Retention.Previous.RouteID != "provision-lab-web" {
		t.Fatalf("retention operation omits exact previous Generation: %+v", retained.Input.Retention.Previous)
	}
	if retained.Input.Retention.Endpoint.ID != "provision-example-http-v1-bac304a88517" || retained.Input.Retention.Endpoint.RouteID != "provision-lab-web" {
		t.Fatalf("retention operation omits exact active Endpoint: %+v", retained.Input.Retention.Endpoint)
	}
	for _, concreteInput := range []string{
		`"source": "https://github.com/datashaman/provision-example-http/releases/download/v0.1.0/provision-example-http-linux-amd64.tar.gz"`,
		`"unit": "provision-lab-web-bac304a88517.service"`,
		`"candidateVerificationPath": "/verify"`,
		`"routeId": "provision-lab-web"`,
	} {
		if !strings.Contains(string(first), concreteInput) {
			t.Fatalf("Plan omits typed deployment input %s:\n%s", concreteInput, first)
		}
	}
	commands, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "sudo -n /usr/local/libexec/provision-host-executor inspect --environment lab --operator marlinf") {
		t.Fatalf("preview did not use the restricted inspector:\n%s", commands)
	}
	for _, forbidden := range []string{"--apply", " install ", " start ", " reload ", " execute "} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatalf("preview attempted host mutation %q:\n%s", forbidden, commands)
		}
	}
}

func TestAsyncPlanPreviewIsDeterministicCompleteAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	writeAsyncBootstrapInspectionSSH(t, dir)
	configPath := filepath.Join("..", "..", "examples", "host-async", "root.yaml")

	preview := func(extraEnv ...string) ([]byte, error) {
		command := exec.Command("go", "run", ".", "plan", "preview", "--file", configPath)
		command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
		hasAsyncStateOverride := false
		for _, value := range extraEnv {
			if strings.HasPrefix(value, "FAKE_NO_ACTIVE_ASYNC=") {
				hasAsyncStateOverride = true
			}
		}
		if !hasAsyncStateOverride {
			command.Env = append(command.Env, "FAKE_NO_ACTIVE_ASYNC=1")
		}
		command.Env = append(command.Env, extraEnv...)
		return command.CombinedOutput()
	}
	first, err := preview()
	if err != nil {
		t.Fatalf("async Plan preview failed: %v\n%s", err, first)
	}
	second, err := preview()
	if err != nil {
		t.Fatalf("second async Plan preview failed: %v\n%s", err, second)
	}
	if string(first) != string(second) {
		t.Fatalf("equivalent async inputs produced different Plans:\n%s\n%s", first, second)
	}
	var plan struct {
		ID                       string            `json:"id"`
		ArtifactDigests          map[string]string `json:"artifactDigests"`
		SensitiveValueReferences []string          `json:"sensitiveValueReferences"`
		Operations               []struct {
			Kind string `json:"kind"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(first, &plan); err != nil {
		t.Fatalf("invalid async Plan JSON: %v\n%s", err, first)
	}
	wantOperations := []string{
		"prepareQueue", "stageArtifact", "stageArtifact", "installTaskGeneration", "verifyTaskGeneration",
		"installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate",
		"activateWorkerIntake", "verifyWorkerActive", "installScheduleRuntime", "handoffSchedule", "verifySchedule",
	}
	if len(plan.Operations) != len(wantOperations) {
		t.Fatalf("async operation count = %d, want %d:\n%s", len(plan.Operations), len(wantOperations), first)
	}
	for index, want := range wantOperations {
		if plan.Operations[index].Kind != want {
			t.Fatalf("async operation %d = %q, want %q", index, plan.Operations[index].Kind, want)
		}
	}
	if plan.ArtifactDigests["consumer"] != "sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40" || plan.ArtifactDigests["publish"] != "sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5" {
		t.Fatalf("Plan omitted released Artifact identities: %+v", plan.ArtifactDigests)
	}
	if len(plan.SensitiveValueReferences) != 1 || plan.SensitiveValueReferences[0] != "secret://lab/rabbitmq-url" {
		t.Fatalf("Plan omitted Queue Secret Reference: %+v", plan.SensitiveValueReferences)
	}
	for _, want := range []string{
		`"contract": "host-rabbitmq-systemd-async/v1alpha1"`,
		`"queue": "messages"`,
		`"imageIndex": "sha256:d0bffe70e755f348625415f32b0a090662e5f06b3ba3f82a4c7aaa18621b1279"`,
		`"admission": "gated"`,
		`"maxDuration": "30s"`,
		`"task": "publish"`,
		`"timezone": "Africa/Johannesburg"`,
		`"appletDigest": "sha256:7777777777777777777777777777777777777777777777777777777777777777"`,
		`"recovery": "retain-queue"`,
		`"recovery": "keep-candidate-gated"`,
	} {
		if !strings.Contains(string(first), want) {
			t.Fatalf("async Plan omitted %s:\n%s", want, first)
		}
	}
	if strings.Contains(string(first), "amqp://") || strings.Contains(string(first), "guest:guest") {
		t.Fatalf("async Plan exposed resolved Queue credentials: %s", first)
	}

	changedRuntime, changedErr := preview("FAKE_APPLET_DIGEST=sha256:8888888888888888888888888888888888888888888888888888888888888888")
	if changedErr != nil {
		t.Fatalf("changed runtime asset did not produce a Plan: %v\n%s", changedErr, changedRuntime)
	}
	var changed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(changedRuntime, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.ID == plan.ID {
		t.Fatalf("runtime asset change did not stale Plan %s", plan.ID)
	}
	unsupported, unsupportedErr := preview("FAKE_WORKER_GATE_CAPABILITY=false")
	if unsupportedErr == nil || !strings.Contains(string(unsupported), "required Worker admission-gate capability is not observed") || strings.Contains(string(unsupported), `"operations"`) {
		t.Fatalf("complete asynchronous Application silently narrowed around missing Worker capability: %v\n%s", unsupportedErr, unsupported)
	}
	incomplete, incompleteErr := preview("FAKE_ASYNC_OBSERVATION_COMPLETE=false")
	if incompleteErr == nil || !strings.Contains(string(incomplete), "asynchronous deployment observation is incomplete") || strings.Contains(string(incomplete), `"operations"`) {
		t.Fatalf("explicitly incomplete observation did not fail closed: %v\n%s", incompleteErr, incomplete)
	}
	missingPackaging, missingPackagingErr := preview("FAKE_ASYNC_PACKAGING_COMPLETE=false")
	if missingPackagingErr == nil || !strings.Contains(string(missingPackaging), "rootless Queue account, subordinate IDs, or lingering manager evidence is incomplete") || strings.Contains(string(missingPackaging), `"operations"`) {
		t.Fatalf("missing packaging evidence did not fail closed: %v\n%s", missingPackagingErr, missingPackaging)
	}

	changedConfigDir := copyAsyncExample(t)
	revisionPath := filepath.Join(changedConfigDir, "revision.yaml")
	revision, err := os.ReadFile(revisionPath)
	if err != nil {
		t.Fatal(err)
	}
	revision = []byte(strings.Replace(string(revision),
		"sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40",
		"sha256:3fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40", 1))
	if err := os.WriteFile(revisionPath, revision, 0600); err != nil {
		t.Fatal(err)
	}
	changedConfigCommand := exec.Command("go", "run", ".", "plan", "preview", "--file", filepath.Join(changedConfigDir, "root.yaml"))
	changedConfigCommand.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog, "FAKE_NO_ACTIVE_ASYNC=1")
	changedConfigOutput, changedConfigErr := changedConfigCommand.CombinedOutput()
	if changedConfigErr != nil {
		t.Fatalf("changed Artifact identity did not produce a Plan: %v\n%s", changedConfigErr, changedConfigOutput)
	}
	if err := json.Unmarshal(changedConfigOutput, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.ID == plan.ID {
		t.Fatalf("Artifact identity change did not stale Plan %s", plan.ID)
	}
	existing, existingErr := preview("FAKE_NO_ACTIVE_ASYNC=0")
	if existingErr != nil {
		t.Fatalf("Worker-only replacement Plan failed: %v\n%s", existingErr, existing)
	}
	var replacement planner.Plan
	if err := json.Unmarshal(existing, &replacement); err != nil {
		t.Fatal(err)
	}
	changedLedger, changedLedgerErr := preview(
		"FAKE_NO_ACTIVE_ASYNC=0",
		"FAKE_LEDGER_DIGEST=sha256:abababababababababababababababababababababababababababababababab",
	)
	if changedLedgerErr != nil {
		t.Fatalf("runtime telemetry change prevented Worker-only planning: %v\n%s", changedLedgerErr, changedLedger)
	}
	var changedLedgerPlan planner.Plan
	if err := json.Unmarshal(changedLedger, &changedLedgerPlan); err != nil {
		t.Fatal(err)
	}
	if changedLedgerPlan.ID == replacement.ID {
		t.Fatal("exact capability evidence did not retain changed Schedule ledger telemetry")
	}
	if replacement.Capability.Observed.Async.Deployment.Schedule.LedgerDigest == changedLedgerPlan.Capability.Observed.Async.Deployment.Schedule.LedgerDigest {
		t.Fatal("exact observed Schedule ledger digest was rewritten or omitted")
	}
	if replacement.Capability.DecisionObserved == nil || replacement.Capability.DecisionObserved.Async.Deployment.Schedule.LedgerDigest != "" || changedLedgerPlan.Capability.DecisionObserved == nil || changedLedgerPlan.Capability.DecisionObserved.Async.Deployment.Schedule.LedgerDigest != "" {
		t.Fatal("adapter decision observation did not explicitly exclude volatile ledger content")
	}
	replacementFingerprint, err := planner.ApprovalFingerprint(replacement)
	if err != nil {
		t.Fatal(err)
	}
	changedLedgerFingerprint, err := planner.ApprovalFingerprint(changedLedgerPlan)
	if err != nil {
		t.Fatal(err)
	}
	if changedLedgerFingerprint != replacementFingerprint {
		t.Fatalf("Schedule ledger telemetry made the Worker-only approval stale: %s != %s", changedLedgerFingerprint, replacementFingerprint)
	}
	wantReplacement := []string{"prepareQueue", "stageArtifact", "installWorkerGeneration", "startWorkerCandidate", "verifyWorkerCandidate", "fenceWorkerIntake", "drainWorkerPrevious", "activateWorkerIntake", "verifyWorkerActive", "retainWorkerPrevious"}
	if len(replacement.Operations) != len(wantReplacement) {
		t.Fatalf("Worker-only operation count = %d, want %d:\n%s", len(replacement.Operations), len(wantReplacement), existing)
	}
	for index, want := range wantReplacement {
		if string(replacement.Operations[index].Kind) != want {
			t.Fatalf("Worker-only operation %d = %q, want %q", index, replacement.Operations[index].Kind, want)
		}
	}
	if strings.Contains(string(existing), `"kind": "installTaskGeneration"`) || strings.Contains(string(existing), `"kind": "handoffSchedule"`) {
		t.Fatalf("Worker-only Plan attempted a Task or Schedule transition: %s", existing)
	}
	for index, dependency := range map[int]string{5: "op-07", 6: "op-08", 7: "op-09", 8: "op-10", 9: "op-11"} {
		dependsOn := replacement.Operations[index].DependsOn
		if len(dependsOn) != 1 || dependsOn[0] != dependency {
			t.Fatalf("Worker handoff operation %s dependencies = %v, want [%s]", replacement.Operations[index].Kind, dependsOn, dependency)
		}
	}
	drainDigest, err := planner.OperationDigest(replacement.Operations[6])
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{7, 8, 9} {
		asyncInput := replacement.Operations[index].Input.Async
		if asyncInput == nil || asyncInput.Worker != nil || asyncInput.WorkerHandoff == nil || asyncInput.WorkerHandoff.DrainOperationDigest != drainDigest {
			t.Fatalf("post-drain Worker operation %s is not bound to drain %s: %+v", replacement.Operations[index].Kind, drainDigest, asyncInput)
		}
	}
	for _, index := range []int{5, 6} {
		if got := replacement.Operations[index].Input.Async.WorkerHandoff.DrainOperationDigest; got != "" {
			t.Fatalf("pre-drain Worker operation %s claimed drain digest %s", replacement.Operations[index].Kind, got)
		}
	}

	taskReplacementDir := copyAsyncExample(t)
	taskRevisionPath := filepath.Join(taskReplacementDir, "revision.yaml")
	taskRevision, err := os.ReadFile(taskRevisionPath)
	if err != nil {
		t.Fatal(err)
	}
	taskRevision = []byte(strings.Replace(string(taskRevision),
		"sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5",
		"sha256:"+strings.Repeat("b", 64), 1))
	if err := os.WriteFile(taskRevisionPath, taskRevision, 0600); err != nil {
		t.Fatal(err)
	}
	taskReplacementCommand := exec.Command("go", "run", ".", "plan", "preview", "--file", filepath.Join(taskReplacementDir, "root.yaml"))
	taskReplacementCommand.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_SSH_LOG="+sshLog,
		"FAKE_NO_ACTIVE_ASYNC=0",
		"FAKE_ACTIVE_WORKER_DIGEST=sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40",
	)
	taskReplacementOutput, taskReplacementErr := taskReplacementCommand.CombinedOutput()
	if taskReplacementErr != nil {
		t.Fatalf("Task-only replacement Plan failed: %v\n%s", taskReplacementErr, taskReplacementOutput)
	}
	var taskReplacement planner.Plan
	if err := json.Unmarshal(taskReplacementOutput, &taskReplacement); err != nil {
		t.Fatal(err)
	}
	wantTaskReplacement := []string{"prepareQueue", "stageArtifact", "installTaskGeneration", "verifyTaskGeneration", "handoffSchedule", "verifySchedule"}
	if len(taskReplacement.Operations) != len(wantTaskReplacement) {
		t.Fatalf("Task-only operation count = %d, want %d:\n%s", len(taskReplacement.Operations), len(wantTaskReplacement), taskReplacementOutput)
	}
	for index, want := range wantTaskReplacement {
		if string(taskReplacement.Operations[index].Kind) != want {
			t.Fatalf("Task-only operation %d = %q, want %q", index, taskReplacement.Operations[index].Kind, want)
		}
	}
	if strings.Contains(string(taskReplacementOutput), `"kind":"installWorkerGeneration"`) || strings.Contains(string(taskReplacementOutput), `"kind":"installScheduleRuntime"`) {
		t.Fatalf("Task-only Plan attempted Worker replacement or duplicate Schedule runtime install: %s", taskReplacementOutput)
	}
	handoff := taskReplacement.Operations[4]
	if len(handoff.DependsOn) != 1 || handoff.DependsOn[0] != "op-04-verify" {
		t.Fatalf("Schedule handoff dependencies = %v, want verified Task dependency", handoff.DependsOn)
	}
	schedule := handoff.Input.Async.Schedule
	if schedule == nil || schedule.Previous == nil || schedule.Previous.TaskGenerationID != replacement.Capability.Observed.Async.Deployment.ActiveTask.ID || schedule.TaskGenerationID == schedule.Previous.TaskGenerationID {
		t.Fatalf("Schedule handoff did not expose exact previous and candidate Task generations: %+v", schedule)
	}

	commands, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "provision-host-executor inspect") != 10 {
		t.Fatalf("preview did not perform exactly one read-only inspection per Plan:\n%s", commands)
	}
	for _, forbidden := range []string{"--apply", " install ", " start ", " reload ", " execute "} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatalf("async preview attempted host mutation %q:\n%s", forbidden, commands)
		}
	}
}

func TestQueueOnlyPlanContainsOnlyDeclaredQueue(t *testing.T) {
	dir := t.TempDir()
	writeAsyncBootstrapInspectionSSH(t, dir)
	configPath := filepath.Join("..", "..", "examples", "host-queue", "root.yaml")
	command := exec.Command("go", "run", ".", "plan", "preview", "--file", configPath)
	command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+filepath.Join(dir, "ssh.log"), "FAKE_WORKER_GATE_CAPABILITY=false", "FAKE_APPLET_DIGEST=")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Queue-only Plan failed because unrelated capabilities are absent: %v\n%s", err, output)
	}
	var plan struct {
		ArtifactDigests map[string]string `json:"artifactDigests"`
		Operations      []struct {
			Kind string `json:"kind"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(output, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.ArtifactDigests) != 0 || len(plan.Operations) != 1 || plan.Operations[0].Kind != "prepareQueue" {
		t.Fatalf("Queue-only Plan contains undeclared components: %s", output)
	}
}

func TestDatabasePlanPreviewIsDeterministicAndFailsClosedForUnqualifiedTransitions(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	writeDatabaseBootstrapInspectionSSH(t, dir)
	configPath := filepath.Join("..", "..", "examples", "host-database", "root.yaml")

	preview := func(extraEnv ...string) ([]byte, error) {
		command := exec.Command("go", "run", ".", "plan", "preview", "--file", configPath)
		command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
		command.Env = append(command.Env, extraEnv...)
		return command.CombinedOutput()
	}
	first, err := preview()
	if err != nil {
		t.Fatalf("Database Plan preview failed: %v\n%s", err, first)
	}
	second, err := preview()
	if err != nil {
		t.Fatalf("second Database Plan preview failed: %v\n%s", err, second)
	}
	if string(first) != string(second) {
		t.Fatalf("equivalent Database inputs produced different Plans:\n%s\n%s", first, second)
	}
	var plan planner.Plan
	if err := json.Unmarshal(first, &plan); err != nil {
		t.Fatalf("invalid Database Plan JSON: %v\n%s", err, first)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Kind != "prepareDatabase" || plan.Operations[0].Input.Database == nil {
		t.Fatalf("Database Plan did not advertise exactly one typed prepareDatabase operation: %+v\n%s", plan.Operations, first)
	}
	input := plan.Database
	if input == nil || input.GenerationID != "postgresql-17-6-b86568d3e0fe" || input.DataRole != "authoritative" || input.StoreRollbackGuarantee != "forward-only" || input.RollbackWindow != "30m0s" {
		t.Fatalf("Database Plan omitted generation or policy input: %+v", input)
	}
	if input.Consequences.CandidateGeneration != "postgresql-17-6-b86568d3e0fe" || input.Consequences.PreviousGeneration != "none" || input.Consequences.Synchronization != "not-required-without-active-generation" || input.Consequences.TransitionApplicability == "" {
		t.Fatalf("Database Plan omitted explicit Store Generation consequences: %+v", input.Consequences)
	}
	if len(plan.SensitiveValueReferences) != 1 || plan.SensitiveValueReferences[0] != "secret://lab/postgresql-url" {
		t.Fatalf("Plan omitted Database Secret Reference: %+v", plan.SensitiveValueReferences)
	}
	for _, want := range []string{
		`"contract": "host-postgresql-systemd-database/v1alpha1"`,
		`"postgresqlVersion": "17.6"`,
		`"imageIndex": "sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929"`,
		`"imageManifest": "sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409"`,
		`"databaseName": "app"`,
		`"storeRollbackGuarantee": "forward-only"`,
		`"forwardCutoverGuarantee": "not-qualified-by-packaging-proof"`,
		`"transitionMechanism": "none-qualified-by-packaging-proof"`,
		`"transitionApplicability": "initial-generation-only; replacement-store-transition-fails-closed-until-qualified"`,
		`"restoreVerification": "isolated-generation"`,
	} {
		if !strings.Contains(string(first), want) {
			t.Fatalf("Database Plan omitted %s:\n%s", want, first)
		}
	}
	if strings.Contains(string(first), "postgres://") || strings.Contains(string(first), "super-secret") {
		t.Fatalf("Database Plan exposed resolved credentials: %s", first)
	}

	changedCapability, changedErr := preview("FAKE_POSTGRES_MANIFEST=sha256:c86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409")
	if changedErr == nil || !strings.Contains(string(changedCapability), "PostgreSQL packaging identity does not match") || strings.Contains(string(changedCapability), `"operations"`) {
		t.Fatalf("changed PostgreSQL packaging capability did not fail closed: %v\n%s", changedErr, changedCapability)
	}
	changedDigest, changedDigestErr := preview("FAKE_EXECUTOR_DIGEST=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if changedDigestErr != nil {
		t.Fatalf("changed executor digest did not produce a Plan: %v\n%s", changedDigestErr, changedDigest)
	}
	var changed planner.Plan
	if err := json.Unmarshal(changedDigest, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.ID == plan.ID {
		t.Fatalf("observed Database capability change did not stale Plan %s", plan.ID)
	}
	exactActive, exactActiveErr := preview("FAKE_ACTIVE_DATABASE_EXACT=1")
	if exactActiveErr != nil {
		t.Fatalf("exact active Database generation did not preview cleanly: %v\n%s", exactActiveErr, exactActive)
	}
	var exactActivePlan planner.Plan
	if err := json.Unmarshal(exactActive, &exactActivePlan); err != nil {
		t.Fatal(err)
	}
	if exactActivePlan.Database == nil || exactActivePlan.Database.Consequences.ActiveGeneration != "postgresql-17-6-b86568d3e0fe" || exactActivePlan.Database.Consequences.PreviousGeneration != "none" || len(exactActivePlan.Operations) != 0 {
		t.Fatalf("exact active Database generation was misclassified as previous or still executable: %s", exactActive)
	}
	wrongCredential, wrongCredentialErr := preview("FAKE_POSTGRES_CREDENTIAL_REFERENCE=secret://lab/other-postgresql-url")
	if wrongCredentialErr == nil || !strings.Contains(string(wrongCredential), "credential Secret Reference does not match") || strings.Contains(string(wrongCredential), `"operations"`) {
		t.Fatalf("mismatched observed Database credential reference did not fail closed: %v\n%s", wrongCredentialErr, wrongCredential)
	}
	missingEncryptedCredential, missingEncryptedCredentialErr := preview("FAKE_ENCRYPTED_CREDENTIAL=false")
	if missingEncryptedCredentialErr == nil || !strings.Contains(string(missingEncryptedCredential), "encrypted credential delivery is not observed") || strings.Contains(string(missingEncryptedCredential), `"operations"`) {
		t.Fatalf("missing encrypted credential evidence did not fail closed: %v\n%s", missingEncryptedCredentialErr, missingEncryptedCredential)
	}
	wrongPodman, wrongPodmanErr := preview("FAKE_PODMAN_VERSION=9.9.9")
	if wrongPodmanErr == nil || !strings.Contains(string(wrongPodman), "Podman version 9.9.9 has no tested PostgreSQL packaging evidence") || strings.Contains(string(wrongPodman), `"operations"`) {
		t.Fatalf("untested Podman version did not fail closed: %v\n%s", wrongPodmanErr, wrongPodman)
	}
	transition, transitionErr := preview("FAKE_ACTIVE_DATABASE_DIFFERENT=1")
	if transitionErr == nil || !strings.Contains(string(transition), "required Database Store Transition is not qualified") || strings.Contains(string(transition), `"operations"`) {
		t.Fatalf("unqualified Database transition did not fail closed: %v\n%s", transitionErr, transition)
	}

	commands, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "provision-host-executor inspect") != 9 {
		t.Fatalf("Database preview did not perform exactly one read-only inspection per Plan:\n%s", commands)
	}
	for _, forbidden := range []string{"--apply", " install ", " start ", " reload ", " execute "} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatalf("Database preview attempted host mutation %q:\n%s", forbidden, commands)
		}
	}
}

func TestDatabaseBoundHTTPPlanBindsAppToActiveDatabase(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	writeDatabaseBootstrapInspectionSSH(t, dir)
	configPath := filepath.Join("..", "..", "examples", "host-database-http", "root.yaml")

	preview := func(extraEnv ...string) ([]byte, error) {
		command := exec.Command("go", "run", ".", "plan", "preview", "--file", configPath)
		command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
		command.Env = append(command.Env, extraEnv...)
		return command.CombinedOutput()
	}
	first, err := preview()
	if err != nil {
		t.Fatalf("Database-bound HTTP Plan preview failed: %v\n%s", err, first)
	}
	second, err := preview()
	if err != nil {
		t.Fatalf("second Database-bound HTTP Plan preview failed: %v\n%s", err, second)
	}
	if string(first) != string(second) {
		t.Fatalf("equivalent Database-bound HTTP inputs produced different Plans:\n%s\n%s", first, second)
	}
	var plan planner.Plan
	if err := json.Unmarshal(first, &plan); err != nil {
		t.Fatalf("invalid Database-bound HTTP Plan JSON: %v\n%s", err, first)
	}
	wantOperations := []planner.OperationKind{
		planner.PrepareDatabase, planner.StageArtifact, planner.InstallGeneration, planner.StartCandidate,
		planner.VerifyCandidate, planner.SwitchEndpoint, planner.VerifyActive,
	}
	if len(plan.Operations) != len(wantOperations) {
		t.Fatalf("operation count = %d, want %d:\n%s", len(plan.Operations), len(wantOperations), first)
	}
	for index, want := range wantOperations {
		if plan.Operations[index].Kind != want {
			t.Fatalf("operation %d = %q, want %q", index, plan.Operations[index].Kind, want)
		}
	}
	stage := plan.Operations[1]
	start := plan.Operations[3]
	if !slices.Contains(stage.DependsOn, "op-01") || start.Input.Systemd == nil || len(start.Input.Systemd.DatabaseBindings) != 1 {
		t.Fatalf("HTTP component is not gated by the managed Database binding: stage=%+v start=%+v", stage, start)
	}
	binding := start.Input.Systemd.DatabaseBindings[0]
	if binding.Component != "data" || binding.LogicalID != "provision-lab-data" || binding.GenerationID != "postgresql-17-6-b86568d3e0fe" || binding.Reference != "secret://lab/postgresql-url" || binding.EnvironmentVariable != "PROVISION_DATABASE_URL_FILE" {
		t.Fatalf("HTTP component binding does not use the logical Database identity: %+v", binding)
	}
	wantRecordID := "provision-lab-data/provision-example-database-http-v1/record-0001"
	if binding.DeterministicRecordNamespace != "provision-lab-data/provision-example-database-http-v1" || len(binding.DeterministicRecordIDs) != 1 || binding.DeterministicRecordIDs[0] != wantRecordID {
		t.Fatalf("HTTP component binding does not require exact deterministic Database record evidence: %+v", binding)
	}
	if plan.Database == nil || plan.Database.Binding.Reference != "secret://lab/postgresql-url" {
		t.Fatalf("Plan omitted managed Database input: %+v", plan.Database)
	}
	if len(plan.SensitiveValueReferences) != 1 || plan.SensitiveValueReferences[0] != "secret://lab/postgresql-url" {
		t.Fatalf("Plan omitted Database Secret Reference: %+v", plan.SensitiveValueReferences)
	}
	for _, want := range []string{
		`"contract": "host-postgresql-systemd-database+http-component/v1alpha1"`,
		`"applicationHealth": "http-candidate"`,
		`"bindingState": "database-bound"`,
		`"deterministicRecordNamespace": "provision-lab-data/provision-example-database-http-v1"`,
		`"provision-lab-data/provision-example-database-http-v1/record-0001"`,
	} {
		if !strings.Contains(string(first), want) {
			t.Fatalf("Database-bound HTTP Plan omitted %s:\n%s", want, first)
		}
	}
	if strings.Contains(string(first), "postgresql://") || strings.Contains(string(first), "LongRandomPassword") {
		t.Fatalf("Database-bound HTTP Plan exposed resolved Database credentials: %s", first)
	}

	exactActive, exactActiveErr := preview("FAKE_ACTIVE_DATABASE_EXACT=1", "FAKE_ACTIVE_HTTP_COMPONENT_EXACT=1")
	if exactActiveErr != nil {
		t.Fatalf("exact active Database-bound HTTP did not preview cleanly: %v\n%s", exactActiveErr, exactActive)
	}
	var exactActivePlan planner.Plan
	if err := json.Unmarshal(exactActive, &exactActivePlan); err != nil {
		t.Fatal(err)
	}
	if len(exactActivePlan.Operations) != 0 || exactActivePlan.Database == nil {
		t.Fatalf("unchanged Database-bound HTTP was not a no-op Plan: %s", exactActive)
	}
	missingActiveBinding, missingActiveBindingErr := preview("FAKE_ACTIVE_DATABASE_EXACT=1", "FAKE_ACTIVE_HTTP_COMPONENT_EXACT=1", "FAKE_ACTIVE_HTTP_COMPONENT_DATABASE_BINDING=false")
	if missingActiveBindingErr != nil {
		t.Fatalf("active HTTP component without observed Database binding did not preview cleanly: %v\n%s", missingActiveBindingErr, missingActiveBinding)
	}
	var missingActiveBindingPlan planner.Plan
	if err := json.Unmarshal(missingActiveBinding, &missingActiveBindingPlan); err != nil {
		t.Fatal(err)
	}
	if len(missingActiveBindingPlan.Operations) == 0 {
		t.Fatalf("active HTTP component without observed Database binding was treated as a no-op Plan: %s", missingActiveBinding)
	}
}

func TestPlanPreviewRejectsUntestedRequiredBlueGreenCapability(t *testing.T) {
	dir := t.TempDir()
	writeBootstrapInspectionSSH(t, dir)
	output, err := runPlanPreviewCommand(t, dir, filepath.Join(dir, "ssh.log"), "FAKE_CADDY_VERSION=99.0.0")
	if err == nil || !strings.Contains(string(output), "Caddy version 99.0.0 has no tested required blue-green evidence") {
		t.Fatalf("untested capability did not fail closed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), `"operations"`) {
		t.Fatalf("failed planning emitted an executable Plan: %s", output)
	}
	if !strings.Contains(string(output), `"executable": false`) || !strings.Contains(string(output), `"observed"`) || !strings.Contains(string(output), `"decision": "unsupported"`) {
		t.Fatalf("failed planning omitted structured capability evidence: %s", output)
	}
}

func TestPlanPreviewRejectsBusyCandidatePortBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	writeBootstrapInspectionSSH(t, dir)
	output, err := runPlanPreviewCommand(t, dir, filepath.Join(dir, "ssh.log"), "FAKE_CANDIDATE_BUSY=1")
	if err == nil || !strings.Contains(string(output), "candidate port 27811 is already listening") {
		t.Fatalf("busy candidate port did not fail during preview: %v\n%s", err, output)
	}
	if strings.Contains(string(output), `"operations"`) {
		t.Fatalf("busy candidate port emitted an executable Plan: %s", output)
	}
}

func TestPlanApprovalSurvivesProcessRestart(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	statePath := filepath.Join(dir, "state.db")
	writeBootstrapInspectionSSH(t, dir)
	actor := authenticatedActor(t)

	previewCmd := exec.Command("go", "run", ".", "plan", "preview",
		"--file", filepath.Join("..", "..", "examples", "host-http", "root.yaml"),
		"--state", statePath,
	)
	previewCmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
	previewOutput, err := previewCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("preview failed: %v\n%s", err, previewOutput)
	}
	var preview struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(previewOutput, &preview); err != nil || preview.ID == "" {
		t.Fatalf("preview Plan identity missing: %v\n%s", err, previewOutput)
	}
	unapprovedOutput, err := runPlanStatusCommand(t, statePath, preview.ID)
	if err != nil || !strings.Contains(string(unapprovedOutput), `"eligible": false`) || !strings.Contains(string(unapprovedOutput), `"reason": "unapproved"`) {
		t.Fatalf("persisted preview was not inspectably unapproved: %v\n%s", err, unapprovedOutput)
	}

	approve := exec.Command("go", "run", ".", "plan", "approve",
		"--file", filepath.Join("..", "..", "examples", "host-http", "root.yaml"),
		"--plan", preview.ID,
		"--actor", actor,
		"--state", statePath,
		"--expires-after", "15m",
	)
	approve.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
	approvalOutput, err := approve.CombinedOutput()
	if err != nil {
		t.Fatalf("approval failed: %v\n%s", err, approvalOutput)
	}

	status := exec.Command("go", "run", ".", "plan", "status", "--plan", preview.ID, "--state", statePath)
	statusOutput, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("status after process restart failed: %v\n%s", err, statusOutput)
	}
	var result struct {
		PlanID   string `json:"planId"`
		Decision string `json:"decision"`
		Actor    string `json:"actor"`
		Eligible bool   `json:"eligible"`
	}
	if err := json.Unmarshal(statusOutput, &result); err != nil {
		t.Fatalf("invalid status JSON: %v\n%s", err, statusOutput)
	}
	if result.PlanID != preview.ID || result.Decision != "approved" || result.Actor != actor || !result.Eligible {
		t.Fatalf("approval was not durably recoverable: %+v\n%s", result, statusOutput)
	}
	if info, err := os.Stat(statePath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state file permissions = %v, %v; want 0600", info, err)
	}
}

func TestPlanApprovalRejectsChangedObservationBeforeWritingState(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	statePath := filepath.Join(dir, "state.db")
	writeBootstrapInspectionSSH(t, dir)

	previewOutput, err := runPersistedPlanPreviewCommand(t, dir, sshLog, statePath)
	if err != nil {
		t.Fatalf("preview failed: %v\n%s", err, previewOutput)
	}
	var preview struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(previewOutput, &preview); err != nil || preview.ID == "" {
		t.Fatalf("preview Plan identity missing: %v\n%s", err, previewOutput)
	}

	approve := exec.Command("go", "run", ".", "plan", "approve",
		"--file", filepath.Join("..", "..", "examples", "host-http", "root.yaml"),
		"--plan", preview.ID,
		"--actor", authenticatedActor(t),
		"--state", statePath,
	)
	approve.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_SSH_LOG="+sshLog,
		"FAKE_EXECUTOR_DIGEST=sha256:"+strings.Repeat("a", 64),
	)
	output, err := approve.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Plan is stale") {
		t.Fatalf("changed observation inherited old approval: %v\n%s", err, output)
	}
	status, statusErr := runPlanStatusCommand(t, statePath, preview.ID)
	if statusErr != nil || !strings.Contains(string(status), `"reason": "unapproved"`) {
		t.Fatalf("stale approval changed the persisted preview: %v\n%s", statusErr, status)
	}
}

func TestPlanStatusDoesNotCreateMissingStateBackend(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "missing.db")
	cmd := exec.Command("go", "run", ".", "plan", "status", "--plan", "sha256:"+strings.Repeat("0", 64), "--state", statePath)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "State Backend does not exist") {
		t.Fatalf("missing State Backend was not rejected: %v\n%s", err, output)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created a missing State Backend: %v", err)
	}
}

func TestPlanApprovalRejectsAnUnauthenticatedActor(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	statePath := filepath.Join(dir, "state.db")
	writeBootstrapInspectionSSH(t, dir)
	preview, err := runPersistedPlanPreviewCommand(t, dir, sshLog, statePath)
	if err != nil {
		t.Fatalf("preview failed: %v\n%s", err, preview)
	}
	planID := decodePlanID(t, preview)
	claimedActor := authenticatedActor(t) + "-forged"
	output, err := runPlanApprovalCommand(t, dir, sshLog, statePath, planID, claimedActor, "15m")
	if err == nil || !strings.Contains(string(output), "does not match authenticated local OS user") {
		t.Fatalf("unauthenticated actor was accepted: %v\n%s", err, output)
	}
	status, statusErr := runPlanStatusCommand(t, statePath, planID)
	if statusErr != nil || !strings.Contains(string(status), `"reason": "unapproved"`) {
		t.Fatalf("failed actor authentication changed approval state: %v\n%s", statusErr, status)
	}
}

func TestNewApprovalSupersedesOlderPlanWithoutDeletingIt(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	statePath := filepath.Join(dir, "state.db")
	writeBootstrapInspectionSSH(t, dir)

	firstPreview, err := runPersistedPlanPreviewCommand(t, dir, sshLog, statePath)
	if err != nil {
		t.Fatalf("first preview failed: %v\n%s", err, firstPreview)
	}
	firstID := decodePlanID(t, firstPreview)
	actor := authenticatedActor(t)
	if output, err := runPlanApprovalCommand(t, dir, sshLog, statePath, firstID, actor, "15m"); err != nil {
		t.Fatalf("first approval failed: %v\n%s", err, output)
	}

	changedDigest := "sha256:" + strings.Repeat("b", 64)
	secondPreview, err := runPersistedPlanPreviewCommand(t, dir, sshLog, statePath, "FAKE_EXECUTOR_DIGEST="+changedDigest)
	if err != nil {
		t.Fatalf("second preview failed: %v\n%s", err, secondPreview)
	}
	secondID := decodePlanID(t, secondPreview)
	if secondID == firstID {
		t.Fatal("changed observation produced the same Plan identity")
	}
	oldStatus, err := runPlanStatusCommand(t, statePath, firstID)
	if err != nil {
		t.Fatalf("superseded status failed: %v\n%s", err, oldStatus)
	}
	var oldResult struct {
		Eligible bool   `json:"eligible"`
		Reason   string `json:"reason"`
		Plan     struct {
			ID string `json:"id"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(oldStatus, &oldResult); err != nil || oldResult.Eligible || oldResult.Reason != "superseded" || oldResult.Plan.ID != firstID {
		t.Fatalf("old Plan was not retained as superseded: %v %+v\n%s", err, oldResult, oldStatus)
	}
	unapprovedStatus, err := runPlanStatusCommand(t, statePath, secondID)
	if err != nil || !strings.Contains(string(unapprovedStatus), `"reason": "unapproved"`) {
		t.Fatalf("new preview was not persisted as unapproved: %v\n%s", err, unapprovedStatus)
	}
	if output, err := runPlanApprovalCommand(t, dir, sshLog, statePath, secondID, actor, "15m", "FAKE_EXECUTOR_DIGEST="+changedDigest); err != nil {
		t.Fatalf("second approval failed: %v\n%s", err, output)
	}
	newStatus, err := runPlanStatusCommand(t, statePath, secondID)
	if err != nil || !strings.Contains(string(newStatus), `"eligible": true`) {
		t.Fatalf("new Plan did not become eligible: %v\n%s", err, newStatus)
	}
}

func TestExpiredApprovalIsInspectableButIneligible(t *testing.T) {
	dir := t.TempDir()
	sshLog := filepath.Join(dir, "ssh.log")
	statePath := filepath.Join(dir, "state.db")
	writeBootstrapInspectionSSH(t, dir)
	preview, err := runPersistedPlanPreviewCommand(t, dir, sshLog, statePath)
	if err != nil {
		t.Fatalf("preview failed: %v\n%s", err, preview)
	}
	planID := decodePlanID(t, preview)
	if output, err := runPlanApprovalCommand(t, dir, sshLog, statePath, planID, authenticatedActor(t), "1ns"); err != nil {
		t.Fatalf("short approval failed: %v\n%s", err, output)
	}
	status, err := runPlanStatusCommand(t, statePath, planID)
	if err != nil {
		t.Fatalf("expired status failed: %v\n%s", err, status)
	}
	if !strings.Contains(string(status), `"decision": "approved"`) || !strings.Contains(string(status), `"eligible": false`) || !strings.Contains(string(status), `"reason": "expired"`) {
		t.Fatalf("expired approval was not retained and rejected: %s", status)
	}
}

func decodePlanID(t *testing.T, output []byte) string {
	t.Helper()
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &plan); err != nil || plan.ID == "" {
		t.Fatalf("Plan identity missing: %v\n%s", err, output)
	}
	return plan.ID
}

func authenticatedActor(t *testing.T) string {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return current.Username
}

func writeReadyBootstrapInspection(t *testing.T, operator, authorityKeyID string) {
	t.Helper()
	status := host.BootstrapStatus{
		SchemaVersion: "provision.dev/host-inspection/v1alpha1", Environment: "lab", Operator: operator, Account: "provision-lab",
		OS: "ubuntu", OSVersion: "26.04", Architecture: "x86_64", SystemdVersion: "systemd 259 (259.5-0ubuntu3.4)",
		SSHServerVersion: "OpenSSH_10.2p1", CaddyVersion: "2.6.2", CaddyActive: true, JournaldActive: true, CgroupV2: true,
		ExecutorDigest: "sha256:e066cdc1a1b8a625dfc32db5ec74c1e4ba7bc459a3a3fc09ccc6488c44d606c5", AuthorityKeyID: authorityKeyID,
		SSHHostKeyFingerprint: "SHA256:ddddddddddddddddddddddddddddddddddddddddddd", GenerationStorageReady: true,
		CaddyConfigValid: true, CaddyAdminReachable: true, CaddyConfigDurable: true, ListeningTCPPorts: []int{18080, 28181},
		Deployment: host.DeploymentStatus{Active: &host.GenerationStatus{
			ID: "provision-example-http-v0-aaaaaaaaaaaa", Revision: "provision-example-http-v0",
			ArtifactDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			SystemdUnit:    "provision-lab-web-aaaaaaaaaaaa.service", ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v0-aaaaaaaaaaaa",
			Port: 28181, RouteID: "provision-lab-web", UnitActive: true, UnitMatches: true,
			RouteObserved: true, RouteUpstream: "127.0.0.1:28181", RouteMatches: true,
		}},
		AllowedOperations: host.AllowedOperations(), Ready: true, Findings: []string{},
	}
	if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
		t.Fatal(err)
	}
}

func writeLocalHostConfiguration(t *testing.T, dir string) string {
	t.Helper()
	return writeHostConfiguration(t, dir, fmt.Sprintf("    local: true\n    user: %s", authenticatedActor(t)))
}

func writeRemoteHostConfiguration(t *testing.T, dir string) string {
	t.Helper()
	return writeHostConfiguration(t, dir, fmt.Sprintf("    address: 192.0.2.10\n    user: %s", authenticatedActor(t)))
}

func writeHostConfiguration(t *testing.T, dir, targetFields string) string {
	t.Helper()
	root := filepath.Join("..", "..", "examples", "host-http")
	for _, name := range []string{"root.yaml", "application.yaml", "revision.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	environment := fmt.Sprintf(`schemaVersion: provision.dev/v1alpha1
kind: Environment
name: lab
application: provision-example-http
rollbackWindow: 30m0s
targets:
  current:
    kind: host
%s
implementations:
  web:
    kind: systemd
    target: current
    rollout: required
    endpoint:
      port: 18080
      drain:
        mode: bounded-http
        maxDuration: 2s
`, targetFields)
	if err := os.WriteFile(filepath.Join(dir, "environment.yaml"), []byte(environment), 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "root.yaml")
}

func writeLocalExecutorSudo(t *testing.T, dir string) {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nGO_WANT_PROVISION_SUDO_HELPER=1 exec %q -test.run=TestProvisionSudoHelper -- \"$@\"\n", testBinary)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeRemoteExecutorSSH(t *testing.T, dir string) {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nGO_WANT_PROVISION_REMOTE_SSH_HELPER=1 exec %q -test.run=TestProvisionRemoteSSHHelper -- \"$@\"\n", testBinary)
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func runPlanApprovalCommand(t *testing.T, dir, sshLog, statePath, planID, actor, expiresAfter string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "run", ".", "plan", "approve",
		"--file", filepath.Join("..", "..", "examples", "host-http", "root.yaml"),
		"--plan", planID,
		"--actor", actor,
		"--state", statePath,
		"--expires-after", expiresAfter,
	)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd.CombinedOutput()
}

func runPlanStatusCommand(t *testing.T, statePath, planID string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "run", ".", "plan", "status", "--plan", planID, "--state", statePath)
	return cmd.CombinedOutput()
}

func runPlanPreviewCommand(t *testing.T, dir, sshLog string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "run", ".", "plan", "preview", "--file", filepath.Join("..", "..", "examples", "host-http", "root.yaml"))
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd.CombinedOutput()
}

func runPersistedPlanPreviewCommand(t *testing.T, dir, sshLog, statePath string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "run", ".", "plan", "preview",
		"--file", filepath.Join("..", "..", "examples", "host-http", "root.yaml"),
		"--state", statePath,
	)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+sshLog)
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd.CombinedOutput()
}

func writeBootstrapInspectionSSH(t *testing.T, dir string) {
	t.Helper()
	ssh := filepath.Join(dir, "ssh")
	data := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SSH_LOG"
case "$*" in
  *"/usr/local/libexec/provision-host-executor inspect --environment lab --operator marlinf")
    ports='18080,28181'
    if [ "${FAKE_CANDIDATE_BUSY:-0}" = 1 ]; then ports='18080,27811,28181'; fi
    executor_digest="${FAKE_EXECUTOR_DIGEST:-sha256:e066cdc1a1b8a625dfc32db5ec74c1e4ba7bc459a3a3fc09ccc6488c44d606c5}"
    printf '{"schemaVersion":"provision.dev/host-inspection/v1alpha1","environment":"lab","operator":"marlinf","account":"provision-lab","os":"ubuntu","osVersion":"26.04","architecture":"x86_64","systemdVersion":"systemd 259 (259.5-0ubuntu3.4)","sshServerVersion":"OpenSSH_10.2p1","caddyVersion":"%s","caddyActive":true,"journaldActive":true,"cgroupV2":true,"executorDigest":"%s","authorityKeyId":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","sshHostKeyFingerprint":"SHA256:ddddddddddddddddddddddddddddddddddddddddddd","generationStorageReady":true,"caddyConfigValid":true,"caddyAdminReachable":true,"caddyConfigDurable":true,"listeningTcpPorts":[%s],"deployment":{"active":{"id":"provision-example-http-v0-aaaaaaaaaaaa","revision":"provision-example-http-v0","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","systemdUnit":"provision-lab-web-aaaaaaaaaaaa.service","releaseDirectory":"/var/lib/provision/environments/lab/releases/provision-example-http-v0-aaaaaaaaaaaa","port":28181,"routeId":"provision-lab-web","unitActive":true,"unitMatches":true,"routeObserved":true,"routeUpstream":"127.0.0.1:28181","routeMatches":true}},"allowedOperations":["inspect","stageArtifact","installGeneration","startCandidate","verifyCandidate","switchEndpoint","verifyActive","drainPrevious","retainPrevious","prepareQueue","installTaskGeneration","verifyTaskGeneration","installWorkerGeneration","startWorkerCandidate","verifyWorkerCandidate","fenceWorkerIntake","drainWorkerPrevious","activateWorkerIntake","verifyWorkerActive","installScheduleRuntime","handoffSchedule","verifySchedule","retainWorkerPrevious","prepareDatabase"],"ready":true,"findings":[]}\n' "${FAKE_CADDY_VERSION:-2.6.2}" "$executor_digest" "$ports"
    ;;
  *) exit 23 ;;
esac
`
	if err := os.WriteFile(ssh, []byte(data), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeAsyncBootstrapInspectionSSH(t *testing.T, dir string) {
	t.Helper()
	ssh := filepath.Join(dir, "ssh")
	data := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SSH_LOG"
printf() {
  value="$(command printf "$@")"
	if [ "${FAKE_NO_ACTIVE_ASYNC:-0}" = 1 ]; then
		value="$(command printf '%s' "$value" | sed -E 's/,"activeWorker":\{[^}]*\}//; s/,"activeTask":\{[^}]*\}//; s/,"schedule":.*$/}}}/')"
	fi
	  command printf '%s\n' "$value" | sed 's#"rabbitmqImageManifest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91"#"rabbitmqImageManifest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91","rabbitmqServiceUnit":"provision-lab-rabbitmq.service","rabbitmqContainer":"provision-lab-rabbitmq","rabbitmqAccount":"provision-lab","rabbitmqDataPath":"/var/lib/provision/environments/lab/services/rabbitmq/data","rabbitmqQuadletPath":"/etc/containers/systemd/users/999/provision-lab-rabbitmq.container"#g' | sed 's#"imageManifest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91"#"generationId":"provision-lab-messages-rabbitmq-4-3-6-34fc91a9de04","imageManifest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91","serviceUnit":"provision-lab-rabbitmq.service","container":"provision-lab-rabbitmq","account":"provision-lab","dataPath":"/var/lib/provision/environments/lab/services/rabbitmq/data","quadletPath":"/etc/containers/systemd/users/999/provision-lab-rabbitmq.container","rabbitmqVersion":"4.3.6","health":"healthy","retryQueue":"provision-lab-messages.retry","deadLetterQueue":"provision-lab-messages.dead-letter","workExchange":"provision-lab-messages.work","retryExchange":"provision-lab-messages.retry","deadLetterExchange":"provision-lab-messages.dead-letter","bindings":[{"source":"provision-lab-messages.work","destination":"provision-lab-messages","routingKey":"provision-lab-messages"},{"source":"provision-lab-messages.retry","destination":"provision-lab-messages.retry","routingKey":"provision-lab-messages.retry"},{"source":"provision-lab-messages.dead-letter","destination":"provision-lab-messages.dead-letter","routingKey":"provision-lab-messages.dead-letter"}],"messageTtl":"24h0m0s","retryDelay":"10s","deadLetterTtl":"168h0m0s","deliveryLimit":3,"accepted":1,"available":0,"acknowledged":1,"deadLettered":0,"probeMessageId":"probe","supportedGuarantees":["publisher-confirms","manual-acknowledgement","at-least-once"],"ownedResources":["rabbitmq-queue:provision-lab-messages"]#g'
}
case "$*" in
  *"/usr/local/libexec/provision-host-executor inspect --environment lab --operator marlinf")
		applet_digest="${FAKE_APPLET_DIGEST:-sha256:7777777777777777777777777777777777777777777777777777777777777777}"
		worker_gate="${FAKE_WORKER_GATE_CAPABILITY:-true}"
		active_worker_digest="${FAKE_ACTIVE_WORKER_DIGEST:-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
		active_worker_id="provision-example-async-v0-$(command printf '%s' "$active_worker_digest" | sed 's/^sha256://' | cut -c1-12)"
		active_worker_unit="provision-lab-consumer-$(command printf '%s' "$active_worker_digest" | sed 's/^sha256://' | cut -c1-12).service"
		observation_complete="${FAKE_ASYNC_OBSERVATION_COMPLETE:-true}"
		packaging_complete="${FAKE_ASYNC_PACKAGING_COMPLETE:-true}"
		ledger_digest="${FAKE_LEDGER_DIGEST:-sha256:9999999999999999999999999999999999999999999999999999999999999999}"
		printf '{"schemaVersion":"provision.dev/host-inspection/v1alpha1","environment":"lab","operator":"marlinf","account":"provision-lab","os":"ubuntu","osVersion":"26.04","architecture":"x86_64","systemdVersion":"systemd 259 (259.5-0ubuntu3.4)","sshServerVersion":"OpenSSH_10.2p1","caddyVersion":"2.6.2","caddyActive":true,"journaldActive":true,"cgroupV2":true,"executorDigest":"sha256:e066cdc1a1b8a625dfc32db5ec74c1e4ba7bc459a3a3fc09ccc6488c44d606c5","authorityKeyId":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","sshHostKeyFingerprint":"SHA256:ddddddddddddddddddddddddddddddddddddddddddd","generationStorageReady":true,"caddyConfigValid":true,"caddyAdminReachable":true,"caddyConfigDurable":true,"allowedOperations":["inspect","stageArtifact","installGeneration","startCandidate","verifyCandidate","switchEndpoint","verifyActive","drainPrevious","retainPrevious","prepareQueue","installTaskGeneration","verifyTaskGeneration","installWorkerGeneration","startWorkerCandidate","verifyWorkerCandidate","fenceWorkerIntake","drainWorkerPrevious","activateWorkerIntake","verifyWorkerActive","installScheduleRuntime","handoffSchedule","verifySchedule","retainWorkerPrevious","prepareDatabase"],"ready":true,"findings":[],"async":{"schemaVersion":"provision.dev/host-async-inspection/v1alpha1","observationComplete":%s,"capabilities":{"podmanVersion":"5.7.0+ds2-3build1","quadlet":true,"rootlessEnvironmentAccount":true,"systemdCredentials":true,"subordinateIds":%s,"lingeringUserManager":%s,"quadletDefinitionRootOwned":%s,"dataPathEnvironmentOwned":%s,"encryptedCredentialObserved":%s,"workerAdmissionGate":%s,"rabbitmqQualificationDigest":"sha256:af41714b1aa2270ba6cd151bd24876ac117218e401e1e87515451a7081ac4c6d","rabbitmqVersion":"4.3.6","rabbitmqImageIndex":"sha256:d0bffe70e755f348625415f32b0a090662e5f06b3ba3f82a4c7aaa18621b1279","rabbitmqImageManifest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91","scheduleAppletDigest":"%s","scheduleLedgerSchema":"provision.dev/schedule-ledger/v1alpha1"},"deployment":{"queue":{"id":"provision-lab-messages","exists":true,"ready":true,"queueType":"quorum","members":1,"durable":true,"imageManifest":"sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91"},"activeWorker":{"id":"%s","revision":"provision-example-async-v0","artifactDigest":"%s","systemdUnit":"%s","active":true,"gate":"open","unitActive":true,"queueConnected":true,"inFlight":0},"activeTask":{"id":"provision-example-async-v0-ce1dc7e13900","revision":"provision-example-async-v0","artifactDigest":"sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5","systemdUnit":"provision-lab-publish-ce1dc7e13900@.service","queue":"provision-lab-messages","configurationDigest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","timeout":"1m0s"},"schedule":{"component":"every-minute","timerUnit":"provision-lab-every-minute.timer","taskGenerationId":"provision-example-async-v0-ce1dc7e13900","appletDigest":"%s","ledgerSchema":"provision.dev/schedule-ledger/v1alpha1","ledgerDigest":"%s","fencingToken":7,"timezone":"Africa/Johannesburg","expression":"* * * * *","daylightSaving":"wall-clock","overlap":"forbid","retry":{"maxAttempts":1,"delay":"10s"},"missedRun":{"mode":"skip","maxOccurrences":0},"failure":"record","active":true}}}}\n' "$observation_complete" "$packaging_complete" "$packaging_complete" "$packaging_complete" "$packaging_complete" "$packaging_complete" "$worker_gate" "$applet_digest" "$active_worker_id" "$active_worker_digest" "$active_worker_unit" "$applet_digest" "$ledger_digest"
    ;;
  *) exit 23 ;;
esac
`
	if err := os.WriteFile(ssh, []byte(data), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeDatabaseBootstrapInspectionSSH(t *testing.T, dir string) {
	t.Helper()
	ssh := filepath.Join(dir, "ssh")
	data := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SSH_LOG"
case "$*" in
  *"/usr/local/libexec/provision-host-executor inspect --environment lab --operator marlinf")
    executor_digest="${FAKE_EXECUTOR_DIGEST:-sha256:e066cdc1a1b8a625dfc32db5ec74c1e4ba7bc459a3a3fc09ccc6488c44d606c5}"
    postgres_manifest="${FAKE_POSTGRES_MANIFEST:-sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409}"
    podman_version="${FAKE_PODMAN_VERSION:-5.7.0+ds2-3build1}"
    credential_reference="${FAKE_POSTGRES_CREDENTIAL_REFERENCE:-secret://lab/postgresql-url}"
    encrypted_credential="${FAKE_ENCRYPTED_CREDENTIAL:-true}"
    host_deployment='{}'
    if [ "${FAKE_ACTIVE_HTTP_COMPONENT_EXACT:-0}" = 1 ]; then
      http_database_binding=''
      if [ "${FAKE_ACTIVE_HTTP_COMPONENT_DATABASE_BINDING:-true}" = true ]; then
        http_database_binding=',"databaseBinding":{"component":"data","logicalId":"provision-lab-data","generationId":"postgresql-17-6-b86568d3e0fe","database":"app","environmentVariable":"PROVISION_DATABASE_URL_FILE","bindingState":"database-bound"}'
      fi
      host_deployment='{"active":{"id":"provision-example-database-http-v1-4ac304a88517","revision":"provision-example-database-http-v1","artifactDigest":"sha256:4ac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5","systemdUnit":"provision-lab-web-4ac304a88517.service","releaseDirectory":"/var/lib/provision/environments/lab/releases/provision-example-database-http-v1-4ac304a88517","port":39139,"routeId":"provision-lab-web"'"$http_database_binding"',"unitActive":true,"unitMatches":true,"routeObserved":true,"routeUpstream":"127.0.0.1:39139","routeMatches":true}}'
    fi
    deployment='{}'
    if [ "${FAKE_ACTIVE_DATABASE_DIFFERENT:-0}" = 1 ]; then
      deployment='{"active":{"id":"postgresql-16-0-aaaaaaaaaaaa","logicalId":"provision-lab-data","ready":true,"postgresqlVersion":"16.0","imageManifest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","serviceUnit":"provision-lab-postgresql.service","container":"provision-lab-postgresql","account":"provision-lab","dataPath":"/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-16-0-aaaaaaaaaaaa/data","quadletPath":"/etc/containers/systemd/users/999/provision-lab-postgresql.container","database":"app","connectivity":true,"durableRestart":true}}'
    fi
    if [ "${FAKE_ACTIVE_DATABASE_EXACT:-0}" = 1 ]; then
      deployment='{"active":{"id":"postgresql-17-6-b86568d3e0fe","logicalId":"provision-lab-data","ready":true,"postgresqlVersion":"17.6","imageManifest":"sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409","serviceUnit":"provision-lab-postgresql.service","container":"provision-lab-postgresql","account":"provision-lab","dataPath":"/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-17-6-b86568d3e0fe/data","quadletPath":"/etc/containers/systemd/users/999/provision-lab-postgresql.container","database":"app","connectivity":true,"durableRestart":true}}'
    fi
    printf '{"schemaVersion":"provision.dev/host-inspection/v1alpha1","environment":"lab","operator":"marlinf","account":"provision-lab","os":"ubuntu","osVersion":"26.04","architecture":"x86_64","systemdVersion":"systemd 259 (259.5-0ubuntu3.4)","sshServerVersion":"OpenSSH_10.2p1","caddyVersion":"2.6.2","caddyActive":true,"journaldActive":true,"cgroupV2":true,"executorDigest":"%s","authorityKeyId":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","sshHostKeyFingerprint":"SHA256:ddddddddddddddddddddddddddddddddddddddddddd","generationStorageReady":true,"caddyConfigValid":true,"caddyAdminReachable":true,"caddyConfigDurable":true,"deployment":%s,"allowedOperations":["inspect","stageArtifact","installGeneration","startCandidate","verifyCandidate","switchEndpoint","verifyActive","drainPrevious","retainPrevious","prepareQueue","installTaskGeneration","verifyTaskGeneration","installWorkerGeneration","startWorkerCandidate","verifyWorkerCandidate","fenceWorkerIntake","drainWorkerPrevious","activateWorkerIntake","verifyWorkerActive","installScheduleRuntime","handoffSchedule","verifySchedule","retainWorkerPrevious","prepareDatabase"],"ready":true,"findings":[],"database":{"schemaVersion":"provision.dev/host-database-inspection/v1alpha1","observationComplete":true,"findings":[],"capabilities":{"podmanVersion":"%s","quadlet":true,"rootlessEnvironmentAccount":true,"systemdCredentials":true,"subordinateIds":true,"lingeringUserManager":true,"quadletDefinitionRootOwned":true,"generationDataPathOwned":true,"encryptedCredentialObserved":%s,"postgresqlQualificationDigest":"sha256:892fb587ac7323ba4f04d38b1fc165f9e2304219f3de14e7e365cb60b4960eff","postgresqlVersion":"17.6","postgresqlImageIndex":"sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929","postgresqlImageManifest":"%s","postgresqlImageReference":"docker.io/library/postgres@%s","postgresqlServiceUnit":"provision-lab-postgresql.service","postgresqlContainer":"provision-lab-postgresql","postgresqlAccount":"provision-lab","postgresqlGeneration":"postgresql-17-6-b86568d3e0fe","postgresqlGenerationDataPath":"/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-17-6-b86568d3e0fe/data","postgresqlQuadletPath":"/etc/containers/systemd/users/999/provision-lab-postgresql.container","postgresqlCredentialReference":"%s","postgresqlListenAddress":"127.0.0.1","postgresqlPort":25432,"storeRollbackGuarantee":"not-qualified-by-packaging-proof","forwardCutoverGuarantee":"not-qualified-by-packaging-proof","supportedTransitionMechanism":"none-qualified-by-packaging-proof"},"deployment":%s}}\n' "$executor_digest" "$host_deployment" "$podman_version" "$encrypted_credential" "$postgres_manifest" "$postgres_manifest" "$credential_reference" "$deployment"
    ;;
  *) exit 23 ;;
esac
`
	if err := os.WriteFile(ssh, []byte(data), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidateVerifiesArtifactBytesWithoutChangingHost(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "host-http")
	dir := t.TempDir()
	for _, name := range []string{"root.yaml", "application.yaml", "environment.yaml", "revision.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	artifact := filepath.Join(dir, "provision-example-http.tar.gz")
	content := []byte("fixture artifact bytes\n")
	if err := os.WriteFile(artifact, content, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	revision := filepath.Join(dir, "revision.yaml")
	data, err := os.ReadFile(revision)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5", digest, 1))
	if err := os.WriteFile(revision, data, 0600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".", "config", "validate", "--file", filepath.Join(dir, "root.yaml"), "--artifact-file", artifact)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), `"artifactVerified": true`) {
		t.Fatalf("validation failed: %v\n%s", err, output)
	}
	if err := os.WriteFile(artifact, []byte("tampered bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "run", ".", "config", "validate", "--file", filepath.Join(dir, "root.yaml"), "--artifact-file", artifact)
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "artifact digest mismatch") {
		t.Fatalf("tampered artifact accepted: %v\n%s", err, output)
	}
}

func TestConfigValidateVerifiesEveryAsyncArtifactByComponent(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join("..", "..", "examples", "host-async")
	for _, name := range []string{"root.yaml", "application.yaml", "environment.yaml", "revision.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	workerPath := filepath.Join(dir, "worker.tar.gz")
	taskPath := filepath.Join(dir, "task.tar.gz")
	workerBytes := []byte("worker release fixture\n")
	taskBytes := []byte("task release fixture\n")
	if err := os.WriteFile(workerPath, workerBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, taskBytes, 0600); err != nil {
		t.Fatal(err)
	}
	workerSum := sha256.Sum256(workerBytes)
	taskSum := sha256.Sum256(taskBytes)
	revisionPath := filepath.Join(dir, "revision.yaml")
	revision, err := os.ReadFile(revisionPath)
	if err != nil {
		t.Fatal(err)
	}
	revision = []byte(strings.ReplaceAll(string(revision),
		"sha256:2fcb2cec1d3d899e53737b9c25579ec1b2a271b93945a604bb43d337d207df40",
		"sha256:"+hex.EncodeToString(workerSum[:])))
	revision = []byte(strings.ReplaceAll(string(revision),
		"sha256:ce1dc7e13900742b3139beb521e9bcd30005470370462b9aa01383f078c999e5",
		"sha256:"+hex.EncodeToString(taskSum[:])))
	if err := os.WriteFile(revisionPath, revision, 0600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("go", "run", ".", "config", "validate",
		"--file", filepath.Join(dir, "root.yaml"),
		"--artifact-file", "consumer="+workerPath,
		"--artifact-file", "publish="+taskPath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("async artifact validation failed: %v\n%s", err, output)
	}
	for _, want := range []string{`"artifactVerified": true`, `"verifiedArtifacts": [`, `"consumer"`, `"publish"`} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("validation omitted %s:\n%s", want, output)
		}
	}

	swapped := exec.Command("go", "run", ".", "config", "validate",
		"--file", filepath.Join(dir, "root.yaml"),
		"--artifact-file", "consumer="+taskPath,
		"--artifact-file", "publish="+workerPath,
	)
	swappedOutput, swappedErr := swapped.CombinedOutput()
	if swappedErr == nil || !strings.Contains(string(swappedOutput), "Artifact consumer digest mismatch") {
		t.Fatalf("component-swapped artifacts were accepted: %v\n%s", swappedErr, swappedOutput)
	}
}

func TestConfigValidateRejectsInvalidAsyncContracts(t *testing.T) {
	tests := []struct {
		name string
		file string
		old  string
		new  string
		want string
	}{
		{"missing Queue reference", "application.yaml", "queue: messages", "queue: missing", "references missing Queue"},
		{"Worker references Task", "application.yaml", "queue: messages", "queue: publish", "may reference only a Queue"},
		{"Schedule references Worker", "application.yaml", "task: publish", "task: consumer", "may reference only a Task"},
		{"scheduler component", "application.yaml", "role: schedule", "role: scheduler", "model a Schedule instead"},
		{"unsupported Queue ordering", "application.yaml", "ordering: unqualified", "ordering: fifo", "unsupported delivery"},
		{"noncanonical Worker drain", "environment.yaml", "maxDuration: 30s", "maxDuration: 30000ms", "bounded drain"},
		{"preferred Worker rollout", "environment.yaml", "kind: systemd-worker\n    target: base\n    rollout: required", "kind: systemd-worker\n    target: base\n    rollout: preferred", "requires gated systemd-worker blue-green"},
		{"unknown Schedule timezone", "application.yaml", "timezone: Africa/Johannesburg", "timezone: Mars/Olympus", "unknown timezone"},
		{"unbounded catch-up", "application.yaml", "mode: skip\n        maxOccurrences: 0", "mode: bounded-catch-up\n        maxOccurrences: 101", "bounded catch-up"},
		{"resolved Queue credential", "environment.yaml", "secret://lab/rabbitmq-url", "amqp://guest:guest@localhost", "Secret Reference"},
		{"component cycle", "application.yaml", "role: queue\n    queue:", "role: queue\n    requires: [consumer]\n    queue:", "contain a cycle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := copyAsyncExample(t)
			path := filepath.Join(dir, test.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(data), test.old, test.new, 1)
			if changed == string(data) {
				t.Fatalf("test substitution did not match %q", test.old)
			}
			if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "run", ".", "config", "validate", "--file", filepath.Join(dir, "root.yaml"))
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("invalid async contract accepted or wrong error: %v\n%s", err, output)
			}
			if strings.Contains(string(output), "guest:guest") {
				t.Fatalf("validation error exposed resolved credential: %s", output)
			}
		})
	}
}

func copyAsyncExample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join("..", "..", "examples", "host-async")
	for _, name := range []string{"root.yaml", "application.yaml", "environment.yaml", "revision.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestConfigValidateRequiresCompleteHTTPHealthContract(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "host-http")
	dir := t.TempDir()
	for _, name := range []string{"root.yaml", "application.yaml", "environment.yaml", "revision.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "run", ".", "config", "validate", "--file", filepath.Join(dir, "root.yaml"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("complete health contract rejected: %v\n%s", err, output)
	}
	application := filepath.Join(dir, "application.yaml")
	data, err := os.ReadFile(application)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "      candidateVerification:\n        path: /verify\n", "", 1))
	if err := os.WriteFile(application, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "run", ".", "config", "validate", "--file", filepath.Join(dir, "root.yaml"))
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "candidateVerification") {
		t.Fatalf("missing candidate verification accepted: %v\n%s", err, output)
	}
}

func TestHostBootstrapCheckRejectsUnsafeEnvironment(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "host", "bootstrap", "check", "--local", "--environment", "../../etc", "--operator", "marlinf")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "invalid environment identifier") {
		t.Fatalf("unsafe environment accepted: %v\n%s", err, output)
	}
}

func TestHostBootstrapDryRunChangesNothing(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "provision-host-executor")
	publicKey := filepath.Join(dir, "authority.pub")
	if err := os.WriteFile(bin, []byte("test binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicKey, []byte(strings.Repeat("0", 64)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../../scripts/bootstrap-host.sh", "--dry-run", "--environment", "lab", "--operator", "marlinf", "--binary", bin, "--authority-public-key", publicKey)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "No changes made") || !strings.Contains(string(output), "provision-lab") {
		t.Fatalf("bootstrap dry-run failed: %v\n%s", err, output)
	}
}

func TestHostBootstrapCheckRejectsUnexpectedExecutorCapability(t *testing.T) {
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	log := filepath.Join(dir, "ssh-arguments")
	fake := `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_SSH_LOG"
printf '%s\n' '{"environment":"lab","operator":"operator","account":"provision-lab","allowedOperations":["inspect","shell"],"ready":true,"findings":[]}'
`
	if err := os.WriteFile(ssh, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", ".", "host", "bootstrap", "check", "--address", "lab.example", "--user", "operator", "--environment", "lab", "--operator", "operator")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_SSH_LOG="+log)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "host executor operation capabilities differ: missing=[stageArtifact") || !strings.Contains(string(output), "unexpected=[shell]") {
		t.Fatalf("unexpected executor capability accepted: %v\n%s", err, output)
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "StrictHostKeyChecking=yes") || !strings.Contains(string(args), "/usr/local/libexec/provision-host-executor") {
		t.Fatalf("unsafe SSH invocation: %s", args)
	}
}
