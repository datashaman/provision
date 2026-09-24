package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
