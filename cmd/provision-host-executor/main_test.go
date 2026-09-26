package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
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

func TestEnvironmentAccountIdentityUsesDedicatedRuntimeHome(t *testing.T) {
	fields := strings.Split("provision-lab:x:104:107::/var/lib/provision/runtime/lab:/usr/sbin/nologin", ":")
	if !validEnvironmentAccount(fields, "/var/lib/provision/runtime/lab") {
		t.Fatal("valid non-login system account rejected")
	}

	for _, invalid := range []string{
		"provision-lab:x:1000:107::/var/lib/provision/runtime/lab:/usr/sbin/nologin",
		"provision-lab:x:104:107::/tmp/writable:/usr/sbin/nologin",
		"provision-lab:x:104:107::/var/lib/provision/runtime/lab:/bin/sh",
	} {
		if validEnvironmentAccount(strings.Split(invalid, ":"), "/var/lib/provision/runtime/lab") {
			t.Fatalf("unsafe account identity accepted: %s", invalid)
		}
	}
}

func TestInstalledPodmanVersionUsesQualifiedPackageIdentity(t *testing.T) {
	dir := t.TempDir()
	dpkgQuery := filepath.Join(dir, "dpkg-query")
	if err := os.WriteFile(dpkgQuery, []byte("#!/bin/sh\n[ \"$1|$2|$3|$4\" = '-W|-f=${Version}|podman|' ] || exit 2\nprintf '%s\\n' '5.7.0+ds2-3build1'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if got := installedPodmanPackageVersion(); got != "5.7.0+ds2-3build1" {
		t.Fatalf("package version = %q", got)
	}
}

func TestCaddyServiceMustResumeAutosavedConfiguration(t *testing.T) {
	if !caddyExecStartResumesAutosave(`{ path=/usr/bin/caddy ; argv[]=/usr/bin/caddy run --environ --resume ; }`) {
		t.Fatal("durable Caddy ExecStart rejected")
	}
	for _, unsafe := range []string{
		`{ path=/usr/bin/caddy ; argv[]=/usr/bin/caddy run --environ --config /etc/caddy/Caddyfile ; }`,
		`{ path=/tmp/caddy ; argv[]=/tmp/caddy run --resume ; }`,
	} {
		if caddyExecStartResumesAutosave(unsafe) {
			t.Fatalf("non-durable Caddy ExecStart accepted: %s", unsafe)
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
	sshHostPublicKey := filepath.Join(dir, "ssh_host_ed25519_key.pub")
	keyBlob := binary.BigEndian.AppendUint32(nil, 11)
	keyBlob = append(keyBlob, []byte("ssh-ed25519")...)
	keyBlob = binary.BigEndian.AppendUint32(keyBlob, 32)
	keyBlob = append(keyBlob, bytes.Repeat([]byte{7}, 32)...)
	if err := os.WriteFile(sshHostPublicKey, []byte("ssh-ed25519 "+base64.StdEncoding.EncodeToString(keyBlob)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	hostFingerprint, err := host.ReadSSHHostKeyFingerprint(sshHostPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	paths := executionPaths{authorityState: authorityState, artifactCache: artifactCache, sshHostPublicKey: sshHostPublicKey}
	envelope := signedTestEnvelope(t, signer, planned, operationDigest, record, "attempt-22222222222222222222222222222222", 2, now, now.Add(time.Minute))
	result, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now)
	if err != nil || result.Outcome != operation.OutcomeSucceeded || !strings.Contains(string(result.Observation), `"already-present"`) {
		t.Fatalf("authorized preparation = %+v, %v", result, err)
	}
	if _, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "already been consumed") {
		t.Fatalf("replayed authorization accepted: %v", err)
	}

	stale := signedTestEnvelope(t, signer, planned, operationDigest, record, "attempt-11111111111111111111111111111111", 1, now, now.Add(time.Minute))
	if _, err := executeAuthorized(context.Background(), stale, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "fencing token is stale: submitted=1 current=2") {
		t.Fatalf("stale fencing token accepted: %v", err)
	}

	tampered := envelope
	tampered.Operation.Input.Artifact = &planner.ArtifactInput{Source: "https://attacker.example/replacement.tar.gz", Digest: planned.Input.Artifact.Digest}
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
	missingArtifact.Input.Artifact = &planner.ArtifactInput{Source: planned.Input.Artifact.Source, Digest: "sha256:" + strings.Repeat("4", 64)}
	missingDigest, err := planner.OperationDigest(missingArtifact)
	if err != nil {
		t.Fatal(err)
	}
	failed := signedTestEnvelope(t, signer, missingArtifact, missingDigest, record, failedAttempt, 4, now, now.Add(time.Minute))
	failedResult, err := executeAuthorized(context.Background(), failed, record, publicKey, paths, now)
	if err != nil || failedResult.Outcome != operation.OutcomeFailed || !strings.Contains(string(failedResult.Observation), `"status":"failed"`) || !strings.Contains(string(failedResult.Observation), "temporary Artifact cache entry") {
		t.Fatalf("known host failure was not structured: %+v, %v", failedResult, err)
	}
	consumedPath := filepath.Join(authorityState, failedAttempt+".json")
	consumedData, err := os.ReadFile(consumedPath)
	if err != nil {
		t.Fatal(err)
	}
	var interrupted consumedAuthorization
	if err := json.Unmarshal(consumedData, &interrupted); err != nil {
		t.Fatal(err)
	}
	interrupted.Outcome = "consumed"
	consumedData, err = json.Marshal(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(consumedPath, consumedData, 0600); err != nil {
		t.Fatal(err)
	}
	unrecordedAttempt := "attempt-77777777777777777777777777777777"
	if err := os.WriteFile(filepath.Join(artifactCache, "."+unrecordedAttempt+".tmp"), []byte("not owned by this Environment's authorization state"), 0600); err != nil {
		t.Fatal(err)
	}

	remote := signedTestEnvelopeForTarget(t, signer, planned, operationDigest, record,
		authority.TargetIdentity{Name: "base", Address: "192.168.101.109", Operator: record.Operator, ExecutorDigest: record.ExecutorDigest, SSHHostKeyFingerprint: hostFingerprint},
		"attempt-55555555555555555555555555555555", 5, now, now.Add(time.Minute))
	remoteResult, err := executeAuthorized(context.Background(), remote, record, publicKey, paths, now)
	if err != nil || remoteResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("authorized remote-target preparation = %+v, %v", remoteResult, err)
	}
	if _, err := os.Lstat(filepath.Join(artifactCache, "."+failedAttempt+".tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted Artifact temporary entry was not removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(artifactCache, "."+unrecordedAttempt+".tmp")); err != nil {
		t.Fatalf("unowned Artifact temporary entry was changed: %v", err)
	}
	wrongHost := signedTestEnvelopeForTarget(t, signer, planned, operationDigest, record,
		authority.TargetIdentity{Name: "base", Address: "192.168.101.109", Operator: record.Operator, ExecutorDigest: record.ExecutorDigest, SSHHostKeyFingerprint: host.SSHHostKeyFingerprint("SHA256:" + strings.Repeat("x", 43))},
		"attempt-66666666666666666666666666666666", 6, now, now.Add(time.Minute))
	if _, err := executeAuthorized(context.Background(), wrongHost, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "different SSH host identity") {
		t.Fatalf("authorization for another SSH host accepted: %v", err)
	}
}

func signedTestEnvelope(t *testing.T, signer authority.Signer, planned planner.Operation, operationDigest string, record bootstrapRecord, attempt string, token int64, issuedAt, expiresAt time.Time) operation.Envelope {
	return signedTestEnvelopeForTarget(t, signer, planned, operationDigest, record,
		authority.TargetIdentity{Name: "current", Local: true, Operator: record.Operator, ExecutorDigest: record.ExecutorDigest},
		attempt, token, issuedAt, expiresAt)
}

func signedTestEnvelopeForTarget(t *testing.T, signer authority.Signer, planned planner.Operation, operationDigest string, record bootstrapRecord, target authority.TargetIdentity, attempt string, token int64, issuedAt, expiresAt time.Time) operation.Envelope {
	t.Helper()
	proof, err := signer.Sign(authority.Claim{
		PlanID: "sha256:" + strings.Repeat("a", 64), Application: "application", Environment: record.Environment,
		Target:      target,
		OperationID: planned.ID, OperationKind: string(planned.Kind), OperationDigest: operationDigest,
		AttemptID: attempt, FencingToken: token, IssuedAt: issuedAt, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return operation.Envelope{SchemaVersion: operation.EnvelopeSchemaVersion, Authorization: proof, Operation: planned}
}

func TestArtifactObservationDistinguishesAbsentValidAndInvalidCacheEntries(t *testing.T) {
	cache := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	observed, err := host.ObserveArtifactCache(cache, digest)
	if err != nil || observed.Status != host.ArtifactAbsent || observed.Digest != digest {
		t.Fatalf("absent observation = %+v", observed)
	}
	bytes := []byte("artifact bytes\n")
	sum := sha256.Sum256(bytes)
	digest = "sha256:" + hex.EncodeToString(sum[:])
	path := filepath.Join(cache, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(path, bytes, 0644); err != nil {
		t.Fatal(err)
	}
	observed, err = host.ObserveArtifactCache(cache, digest)
	if err != nil || observed.Status != host.ArtifactAlreadyPresent || observed.Size != int64(len(bytes)) {
		t.Fatalf("valid observation = %+v", observed)
	}
	if err := os.WriteFile(path, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	observed, err = host.ObserveArtifactCache(cache, digest)
	if err != nil || observed.Status != host.ArtifactInvalid || !strings.Contains(observed.Reason, "does not match") {
		t.Fatalf("invalid observation = %+v", observed)
	}
}

func TestHostOperationResultRemainsStructured(t *testing.T) {
	observation, _ := json.Marshal(host.ArtifactObservation{Status: host.ArtifactStaged, Digest: "sha256:" + strings.Repeat("a", 64)})
	if !json.Valid(observation) || strings.Contains(string(observation), "command") {
		t.Fatalf("unsafe observation: %s", observation)
	}
}
