package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func runPlanPreviewCommand(t *testing.T, dir, sshLog string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "run", ".", "plan", "preview", "--file", filepath.Join("..", "..", "examples", "host-http", "root.yaml"))
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
    printf '{"schemaVersion":"provision.dev/host-inspection/v1alpha1","environment":"lab","operator":"marlinf","account":"provision-lab","os":"ubuntu","osVersion":"26.04","architecture":"x86_64","systemdVersion":"systemd 259 (259.5-0ubuntu3.4)","sshServerVersion":"OpenSSH_10.2p1","caddyVersion":"%s","caddyActive":true,"journaldActive":true,"cgroupV2":true,"executorDigest":"sha256:e066cdc1a1b8a625dfc32db5ec74c1e4ba7bc459a3a3fc09ccc6488c44d606c5","generationStorageReady":true,"caddyConfigValid":true,"caddyAdminReachable":true,"listeningTcpPorts":[%s],"deployment":{"active":{"id":"provision-example-http-v0-aaaaaaaaaaaa","revision":"provision-example-http-v0","systemdUnit":"provision-lab-web-aaaaaaaaaaaa.service","releaseDirectory":"/var/lib/provision/environments/lab/releases/provision-example-http-v0-aaaaaaaaaaaa","port":28181,"routeId":"provision-lab-web","unitActive":true,"unitMatches":true,"routeObserved":true,"routeUpstream":"127.0.0.1:28181","routeMatches":true}},"allowedOperations":["inspect"],"ready":true,"findings":[]}\n' "${FAKE_CADDY_VERSION:-2.6.2}" "$ports"
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
	bin := filepath.Join(t.TempDir(), "provision-host-executor")
	if err := os.WriteFile(bin, []byte("test binary"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../../scripts/bootstrap-host.sh", "--dry-run", "--environment", "lab", "--operator", "marlinf", "--binary", bin)
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
