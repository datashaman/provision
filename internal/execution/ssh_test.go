package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

func TestSSHHostHandlerUsesTrustedIdentityAndTypedExecutor(t *testing.T) {
	dir := t.TempDir()
	installSSHTestCommand(t, dir)
	logPath := filepath.Join(dir, "ssh-arguments.json")
	envelopePath := filepath.Join(dir, "envelope.json")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PROVISION_SSH_TEST_LOG", logPath)
	t.Setenv("PROVISION_SSH_TEST_ENVELOPE", envelopePath)

	target := planner.Target{Name: "base", Kind: "host", Address: "192.168.101.109", User: "operator"}
	handlerValue, err := NewHostHandler(target, "lab")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerValue.(SSHHostHandler)
	planned := planner.Operation{
		ID: "op-01", Kind: planner.StageArtifact, DependsOn: []string{}, Recovery: planner.DiscardStaged,
		Input: planner.OperationInput{Artifact: &planner.ArtifactInput{Source: "https://artifacts.example/release.tar.gz", Digest: "sha256:" + strings.Repeat("3", 64)}},
	}
	observed, err := handler.Observe(t.Context(), "sha256:"+strings.Repeat("a", 64), planned)
	if err != nil || observed.State != ObservationPending {
		t.Fatalf("remote observation = %+v, %v", observed, err)
	}
	arguments := readStringSlice(t, logPath)
	wantPrefix := []string{"-o", "BatchMode=yes", "-o", "PasswordAuthentication=no", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=5", "--", "operator@192.168.101.109"}
	if len(arguments) < len(wantPrefix) || strings.Join(arguments[:len(wantPrefix)], "\x00") != strings.Join(wantPrefix, "\x00") || !strings.Contains(strings.Join(arguments, " "), "observe-artifact --environment lab --operator operator") {
		t.Fatalf("unsafe SSH arguments: %q", arguments)
	}

	envelope := operation.Envelope{
		SchemaVersion: operation.EnvelopeSchemaVersion,
		Authorization: authority.Proof{Claim: authority.Claim{
			PlanID: "sha256:" + strings.Repeat("a", 64), Environment: "lab",
			Target:      authority.TargetIdentity{Name: "base", Address: target.Address, Operator: target.User},
			OperationID: "op-01", AttemptID: "attempt-" + strings.Repeat("1", 32), FencingToken: 1,
		}},
		Operation: planned,
	}
	result, err := handler.ApplyOrResume(t.Context(), envelope)
	if err != nil || result.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("remote apply = %+v, %v", result, err)
	}
	data, err := os.ReadFile(envelopePath)
	if err != nil || !strings.Contains(string(data), `"kind":"stageArtifact"`) || strings.Contains(string(data), `"command"`) {
		t.Fatalf("remote envelope = %s, %v", data, err)
	}

	envelope.Authorization.Claim.Target.Address = "other.example"
	if _, err := handler.ApplyOrResume(t.Context(), envelope); err == nil || !strings.Contains(err.Error(), "different Host Target") {
		t.Fatalf("mismatched remote target accepted: %v", err)
	}
}

func TestSSHHostHandlerFailsClosedOnHostIdentityError(t *testing.T) {
	dir := t.TempDir()
	installSSHTestCommand(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PROVISION_SSH_TEST_IDENTITY_FAILURE", "1")
	handlerValue, err := NewHostHandler(planner.Target{Name: "base", Kind: "host", Address: "base.local", User: "operator"}, "lab")
	if err != nil {
		t.Fatal(err)
	}
	planned := planner.Operation{ID: "op-01", Kind: planner.StageArtifact, Input: planner.OperationInput{Artifact: &planner.ArtifactInput{Digest: "sha256:" + strings.Repeat("3", 64)}}}
	if _, err := handlerValue.Observe(t.Context(), "sha256:"+strings.Repeat("a", 64), planned); err == nil || !strings.Contains(err.Error(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("changed host identity did not fail closed: %v", err)
	}
}

func TestProvisionSSHHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROVISION_SSH_HELPER") != "1" {
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
	if logPath := os.Getenv("PROVISION_SSH_TEST_LOG"); logPath != "" {
		data, _ := json.Marshal(arguments)
		_ = os.WriteFile(logPath, data, 0600)
	}
	if os.Getenv("PROVISION_SSH_TEST_IDENTITY_FAILURE") == "1" {
		fmt.Fprintln(os.Stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED")
		os.Exit(255)
	}
	joined := strings.Join(arguments, " ")
	if strings.Contains(joined, " observe-artifact ") {
		digest := arguments[len(arguments)-1]
		_ = json.NewEncoder(os.Stdout).Encode(host.ArtifactObservation{
			Status: host.ArtifactAbsent, Path: host.ArtifactCacheRoot + "/" + strings.TrimPrefix(digest, "sha256:"), Digest: digest,
		})
		os.Exit(0)
	}
	if strings.Contains(joined, " execute ") {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("PROVISION_SSH_TEST_ENVELOPE"), data, 0600)
		var envelope operation.Envelope
		if json.Unmarshal(data, &envelope) != nil {
			os.Exit(2)
		}
		claim := envelope.Authorization.Claim
		_ = json.NewEncoder(os.Stdout).Encode(operation.Result{
			SchemaVersion: operation.ResultSchemaVersion, PlanID: claim.PlanID, OperationID: claim.OperationID,
			AttemptID: claim.AttemptID, FencingToken: claim.FencingToken, Outcome: operation.OutcomeSucceeded,
			Observation: json.RawMessage(`{"status":"staged","path":"/var/lib/provision/artifacts/sha256/` + strings.Repeat("3", 64) + `","digest":"sha256:` + strings.Repeat("3", 64) + `","size":123}`),
		})
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "unexpected SSH invocation", arguments)
	os.Exit(2)
}

func installSSHTestCommand(t *testing.T, dir string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nGO_WANT_PROVISION_SSH_HELPER=1 exec " + shellQuote(executable) + " -test.run=TestProvisionSSHHelper -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func readStringSlice(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	return values
}
