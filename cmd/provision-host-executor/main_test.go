package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provision/internal/authority"
	"provision/internal/operation"
	"provision/internal/planner"
)

func TestExecutorRejectsDeploymentAndArbitraryCommands(t *testing.T) {
	for _, args := range [][]string{{"apply"}, {"sh", "-c", "touch /tmp/should-not-exist"}} {
		cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "only inspect and authorized typed execution are available") {
			t.Fatalf("command %v accepted: %v\n%s", args, err, output)
		}
	}
}

func TestAuthorizedArtifactPreparationRejectsReplayAndStaleFence(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "authority.key")
	publicPath := filepath.Join(dir, "authority.pub")
	keyInfo, err := authority.GenerateKeyPair(privatePath, publicPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := authority.LoadSigner(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := authority.LoadVerifier(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	authorityState := filepath.Join(dir, "authority-state")
	artifactCache := filepath.Join(dir, "artifacts")
	for _, path := range []string{authorityState, artifactCache} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	artifactBytes := []byte("approved artifact bytes\n")
	artifactSum := sha256.Sum256(artifactBytes)
	artifactDigest := "sha256:" + hex.EncodeToString(artifactSum[:])
	if err := os.WriteFile(filepath.Join(artifactCache, hex.EncodeToString(artifactSum[:])), artifactBytes, 0644); err != nil {
		t.Fatal(err)
	}
	planned := planner.Operation{
		ID: "op-01", Kind: planner.StageArtifact, DependsOn: []string{},
		Input: planner.OperationInput{Artifact: &planner.ArtifactInput{Source: "https://artifacts.example/release.tar.gz", Digest: artifactDigest}},
	}
	operationDigest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	record := bootstrapRecord{
		SchemaVersion: "provision.dev/bootstrap/v2", Environment: "lab", Operator: "operator",
		Account: "provision-lab", ExecutorDigest: "sha256:" + strings.Repeat("e", 64), AuthorityKeyID: keyInfo.ID,
	}
	paths := executionPaths{authorityState: authorityState, artifactCache: artifactCache}
	envelope := signedTestEnvelope(t, signer, planned, operationDigest, record, "attempt-22222222222222222222222222222222", 2, now, now.Add(time.Minute))
	result, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now)
	if err != nil || result.Outcome != operation.OutcomeSucceeded || !strings.Contains(string(result.Observation), `"already-present"`) {
		t.Fatalf("authorized preparation = %+v, %v", result, err)
	}
	if _, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "already been consumed") {
		t.Fatalf("replayed authorization accepted: %v", err)
	}

	stale := signedTestEnvelope(t, signer, planned, operationDigest, record, "attempt-11111111111111111111111111111111", 1, now, now.Add(time.Minute))
	if _, err := executeAuthorized(context.Background(), stale, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "fencing token is stale") {
		t.Fatalf("stale fencing token accepted: %v", err)
	}

	tampered := envelope
	tampered.Operation.Input.Artifact.Source = "https://attacker.example/replacement.tar.gz"
	if _, err := executeAuthorized(context.Background(), tampered, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "digest does not match") {
		t.Fatalf("tampered typed operation accepted: %v", err)
	}

	expired := signedTestEnvelope(t, signer, planned, operationDigest, record, "attempt-33333333333333333333333333333333", 3, now.Add(-2*time.Minute), now.Add(-time.Minute))
	if _, err := executeAuthorized(context.Background(), expired, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired authorization accepted: %v", err)
	}

	failedAttempt := "attempt-44444444444444444444444444444444"
	if err := os.WriteFile(filepath.Join(artifactCache, "."+failedAttempt+".tmp"), []byte("collision"), 0600); err != nil {
		t.Fatal(err)
	}
	missingArtifact := planned
	missingArtifact.Input.Artifact.Digest = "sha256:" + strings.Repeat("4", 64)
	missingDigest, err := planner.OperationDigest(missingArtifact)
	if err != nil {
		t.Fatal(err)
	}
	failed := signedTestEnvelope(t, signer, missingArtifact, missingDigest, record, failedAttempt, 4, now, now.Add(time.Minute))
	failedResult, err := executeAuthorized(context.Background(), failed, record, publicKey, paths, now)
	if err != nil || failedResult.Outcome != operation.OutcomeFailed || !strings.Contains(string(failedResult.Observation), `"status":"failed"`) || !strings.Contains(string(failedResult.Observation), "temporary Artifact cache entry") {
		t.Fatalf("known host failure was not structured: %+v, %v", failedResult, err)
	}
}

func signedTestEnvelope(t *testing.T, signer authority.Signer, planned planner.Operation, operationDigest string, record bootstrapRecord, attempt string, token int64, issuedAt, expiresAt time.Time) operation.Envelope {
	t.Helper()
	proof, err := signer.Sign(authority.Claim{
		PlanID: "sha256:" + strings.Repeat("a", 64), Application: "application", Environment: record.Environment,
		Target:      authority.TargetIdentity{Name: "current", Local: true, Operator: record.Operator, ExecutorDigest: record.ExecutorDigest},
		OperationID: planned.ID, OperationKind: string(planned.Kind), OperationDigest: operationDigest,
		AttemptID: attempt, FencingToken: token, IssuedAt: issuedAt, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return operation.Envelope{SchemaVersion: operation.EnvelopeSchemaVersion, Authorization: proof, Operation: planned}
}

func TestHostOperationResultRemainsStructured(t *testing.T) {
	observation, _ := json.Marshal(artifactObservation{Status: "staged", Digest: "sha256:" + strings.Repeat("a", 64)})
	if !json.Valid(observation) || strings.Contains(string(observation), "command") {
		t.Fatalf("unsafe observation: %s", observation)
	}
}
