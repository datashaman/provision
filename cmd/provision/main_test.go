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
	"strings"
	"testing"
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
		fmt.Fprintf(os.Stdout, `{"schemaVersion":"provision.dev/host-inspection/v1alpha1","environment":"lab","operator":%q,"account":"provision-lab","os":"ubuntu","osVersion":"26.04","architecture":"x86_64","systemdVersion":"systemd 259 (259.5-0ubuntu3.4)","sshServerVersion":"OpenSSH_10.2p1","caddyVersion":"2.6.2","caddyActive":true,"journaldActive":true,"cgroupV2":true,"executorDigest":"sha256:e066cdc1a1b8a625dfc32db5ec74c1e4ba7bc459a3a3fc09ccc6488c44d606c5","authorityKeyId":%q,"generationStorageReady":true,"caddyConfigValid":true,"caddyAdminReachable":true,"listeningTcpPorts":[18080,28181],"deployment":{"active":{"id":"provision-example-http-v0-aaaaaaaaaaaa","revision":"provision-example-http-v0","systemdUnit":"provision-lab-web-aaaaaaaaaaaa.service","releaseDirectory":"/var/lib/provision/environments/lab/releases/provision-example-http-v0-aaaaaaaaaaaa","port":28181,"routeId":"provision-lab-web","unitActive":true,"unitMatches":true,"routeObserved":true,"routeUpstream":"127.0.0.1:28181","routeMatches":true}},"allowedOperations":["inspect","stageArtifact"],"ready":true,"findings":[]}`+"\n", operator, keyID)
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
		SchemaVersion string `json:"schemaVersion"`
		ID            string `json:"id"`
		Operations    []struct {
			Kind string `json:"kind"`
		} `json:"operations"`
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
		if plan.Operations[i].Kind != want {
			t.Fatalf("operation %d = %q, want %q", i, plan.Operations[i].Kind, want)
		}
	}
	for _, concreteInput := range []string{
		`"source": "https://github.com/datashaman/provision-example-http/releases/download/v0.1.0/provision-example-http-linux-amd64.tar.gz"`,
		`"unit": "provision-lab-web-bac304a88517.service"`,
		`"path": "/verify"`,
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

func writeLocalHostConfiguration(t *testing.T, dir string) string {
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
targets:
  current:
    kind: host
    local: true
    user: %s
implementations:
  web:
    kind: systemd
    target: current
    rollout: required
    endpoint:
      port: 18080
`, authenticatedActor(t))
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
    printf '{"schemaVersion":"provision.dev/host-inspection/v1alpha1","environment":"lab","operator":"marlinf","account":"provision-lab","os":"ubuntu","osVersion":"26.04","architecture":"x86_64","systemdVersion":"systemd 259 (259.5-0ubuntu3.4)","sshServerVersion":"OpenSSH_10.2p1","caddyVersion":"%s","caddyActive":true,"journaldActive":true,"cgroupV2":true,"executorDigest":"%s","authorityKeyId":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","generationStorageReady":true,"caddyConfigValid":true,"caddyAdminReachable":true,"listeningTcpPorts":[%s],"deployment":{"active":{"id":"provision-example-http-v0-aaaaaaaaaaaa","revision":"provision-example-http-v0","systemdUnit":"provision-lab-web-aaaaaaaaaaaa.service","releaseDirectory":"/var/lib/provision/environments/lab/releases/provision-example-http-v0-aaaaaaaaaaaa","port":28181,"routeId":"provision-lab-web","unitActive":true,"unitMatches":true,"routeObserved":true,"routeUpstream":"127.0.0.1:28181","routeMatches":true}},"allowedOperations":["inspect","stageArtifact"],"ready":true,"findings":[]}\n' "${FAKE_CADDY_VERSION:-2.6.2}" "$executor_digest" "$ports"
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
	if err == nil || !strings.Contains(string(output), "unexpected host executor operation") {
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
