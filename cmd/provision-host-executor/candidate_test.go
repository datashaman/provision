package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

func TestAuthorizedCandidateInstallIsPlanBoundAndFenced(t *testing.T) {
	paths, record, generation, _ := candidateFixture(t)
	paths.authorityState = filepath.Join(t.TempDir(), "authority-state")
	if err := os.Mkdir(paths.authorityState, 0700); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(t.TempDir(), "authority.key")
	publicPath := filepath.Join(filepath.Dir(privatePath), "authority.pub")
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
	record.SchemaVersion = "provision.dev/bootstrap/v2"
	record.Operator = "operator"
	record.ExecutorDigest = "sha256:" + strings.Repeat("e", 64)
	record.AuthorityKeyID = keyInfo.ID
	planned := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}, Input: planner.OperationInput{Generation: &generation}}
	digest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	envelope := signedTestEnvelope(t, signer, planned, digest, record, "attempt-44444444444444444444444444444444", 1, now, now.Add(time.Minute))
	result, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now)
	if err != nil || result.Outcome != operation.OutcomeSucceeded || !strings.Contains(string(result.Observation), `"status":"installed"`) {
		t.Fatalf("authorized candidate install = %+v, %v", result, err)
	}
	tampered := envelope
	tampered.Operation.Input.Generation = &planner.GenerationInput{ID: generation.ID, Revision: generation.Revision, ArtifactDigest: generation.ArtifactDigest, Account: generation.Account, ReleaseDirectory: filepath.Join(paths.environmentHome, "elsewhere", generation.ID)}
	if _, err := executeAuthorized(context.Background(), tampered, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "digest does not match") {
		t.Fatalf("tampered candidate operation accepted: %v", err)
	}
}

func TestCandidateLifecycleInstallsStartsAndVerifiesWithoutChangingActiveGeneration(t *testing.T) {
	paths, _, generation, systemd := candidateFixture(t)
	active := []byte(`{"id":"previous-generation","systemdUnit":"provision-lab-web-previous.service"}`)
	if err := os.WriteFile(filepath.Join(paths.environmentHome, "active-generation.json"), active, 0644); err != nil {
		t.Fatal(err)
	}

	installed, err := installGeneration(paths, generation, "attempt-11111111111111111111111111111111")
	if err != nil || installed.Status != host.CandidateInstalled || installed.ArtifactDigest != generation.ArtifactDigest || installed.Revision != generation.Revision {
		t.Fatalf("installed Generation = %+v, %v", installed, err)
	}
	executable, err := os.ReadFile(filepath.Join(generation.ReleaseDirectory, installed.Executable))
	if err != nil || string(executable) != "candidate executable\n" {
		t.Fatalf("installed executable = %q, %v", executable, err)
	}

	originalSystemctl := runSystemctl
	t.Cleanup(func() { runSystemctl = originalSystemctl })
	activeUnits := map[string]bool{}
	runSystemctl = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "daemon-reload", "stop":
			return nil, nil
		case "start":
			activeUnits[args[1]] = true
			return nil, nil
		case "is-active":
			if activeUnits[args[1]] {
				return []byte("active\n"), nil
			}
		}
		return []byte("inactive\n"), errors.New("inactive")
	}
	started, err := startCandidate(context.Background(), paths, systemd)
	if err != nil || started.Status != host.CandidateActive {
		t.Fatalf("started candidate = %+v, %v", started, err)
	}
	unchanged, err := os.ReadFile(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || string(unchanged) != string(active) {
		t.Fatalf("active generation changed: %s, %v", unchanged, err)
	}
	unit, err := os.ReadFile(filepath.Join(paths.systemdUnits, systemd.Unit))
	if err != nil || !strings.Contains(string(unit), "PROVISION_HTTP_LISTEN=127.0.0.1:") || !strings.Contains(string(unit), "PROVISION_REVISION="+generation.Revision) || !strings.Contains(string(unit), "IPAddressDeny=any\nIPAddressAllow=localhost") {
		t.Fatalf("candidate unit is not private and Revision-bound:\n%s\n%v", unit, err)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live", "/ready":
			response.WriteHeader(http.StatusNoContent)
		case "/verify":
			_ = json.NewEncoder(response).Encode(map[string]string{"revision": generation.Revision})
		default:
			http.NotFound(response, request)
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	systemd.Port = listener.Addr().(*net.TCPAddr).Port
	health := planner.HealthInput{GenerationID: generation.ID, Revision: generation.Revision, LivenessPath: "/live", ReadinessPath: "/ready", CandidateVerifyPath: "/verify", Port: systemd.Port}
	verified := checkCandidateHealth(context.Background(), health)
	if verified.Status != host.CandidateHealthy || !verified.SwitchEligible || len(verified.Checks) != 3 {
		t.Fatalf("candidate Health Contract = %+v", verified)
	}
	wrongRevision := health
	wrongRevision.Revision = "provision-example-http-v2"
	failedHealth := checkCandidateHealth(context.Background(), wrongRevision)
	if failedHealth.Status != host.CandidateFailed || failedHealth.SwitchEligible || !strings.Contains(failedHealth.Reason, "candidateVerification") {
		t.Fatalf("failed candidate Health Contract = %+v", failedHealth)
	}
	unchanged, err = os.ReadFile(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || string(unchanged) != string(active) {
		t.Fatalf("failed pre-switch health changed active generation: %s, %v", unchanged, err)
	}
}

func TestFailedCandidateStartCleansOnlyItsUnitAndPreservesPreviousGeneration(t *testing.T) {
	paths, _, generation, systemd := candidateFixture(t)
	if _, err := installGeneration(paths, generation, "attempt-22222222222222222222222222222222"); err != nil {
		t.Fatal(err)
	}
	previousRelease := filepath.Join(paths.environmentHome, "releases", "previous-generation")
	if err := os.Mkdir(previousRelease, 0755); err != nil {
		t.Fatal(err)
	}
	previousUnit := filepath.Join(paths.systemdUnits, "provision-lab-web-previous.service")
	if err := os.WriteFile(previousUnit, []byte("previous unit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	active := []byte(`{"id":"previous-generation","systemdUnit":"provision-lab-web-previous.service"}`)
	if err := os.WriteFile(activePath, active, 0644); err != nil {
		t.Fatal(err)
	}

	originalSystemctl := runSystemctl
	t.Cleanup(func() { runSystemctl = originalSystemctl })
	runSystemctl = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "start" {
			return []byte("candidate crashed"), errors.New("start failed")
		}
		if args[0] == "is-active" {
			return []byte("inactive"), errors.New("inactive")
		}
		return nil, nil
	}
	failed, err := startCandidate(context.Background(), paths, systemd)
	if err == nil || failed.Status != host.CandidateFailed {
		t.Fatalf("failed start = %+v, %v", failed, err)
	}
	if _, err := os.Stat(filepath.Join(paths.systemdUnits, systemd.Unit)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed candidate unit was not cleaned up: %v", err)
	}
	if data, err := os.ReadFile(previousUnit); err != nil || string(data) != "previous unit\n" {
		t.Fatalf("previous unit changed: %q, %v", data, err)
	}
	if _, err := os.Stat(previousRelease); err != nil {
		t.Fatalf("previous release removed: %v", err)
	}
	if data, err := os.ReadFile(activePath); err != nil || string(data) != string(active) {
		t.Fatalf("active generation changed: %q, %v", data, err)
	}
	if err := cleanupCandidateUnit(context.Background(), paths, planner.SystemdInput{GenerationID: "previous-generation", Unit: "provision-lab-web-previous.service"}); err == nil || !strings.Contains(err.Error(), "recorded active") {
		t.Fatalf("active generation cleanup accepted: %v", err)
	}
}

func TestCandidateInstallRejectsUnsafeBundleAndLeavesNoGeneration(t *testing.T) {
	paths, _, generation, _ := candidateFixture(t)
	unsafe := nativeBundle(t, "../escape", []byte("bad\n"), 0755)
	digest := sha256.Sum256(unsafe)
	generation.ArtifactDigest = "sha256:" + hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(paths.artifactCache, hex.EncodeToString(digest[:])), unsafe, 0644); err != nil {
		t.Fatal(err)
	}
	observed, err := installGeneration(paths, generation, "attempt-33333333333333333333333333333333")
	if err == nil || observed.Status != host.CandidateFailed {
		t.Fatalf("unsafe bundle installed: %+v, %v", observed, err)
	}
	if _, err := os.Stat(generation.ReleaseDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed installation left a Generation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.environmentHome, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe bundle escaped release directory: %v", err)
	}
}

func candidateFixture(t *testing.T) (executionPaths, bootstrapRecord, planner.GenerationInput, planner.SystemdInput) {
	t.Helper()
	root := t.TempDir()
	paths := executionPaths{
		artifactCache:   filepath.Join(root, "artifacts"),
		environmentHome: filepath.Join(root, "environments", "lab"),
		systemdUnits:    filepath.Join(root, "systemd"),
	}
	for _, directory := range []string{paths.artifactCache, paths.environmentHome, paths.systemdUnits} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	bundle := nativeBundle(t, "provision-example-http", []byte("candidate executable\n"), 0755)
	digest := sha256.Sum256(bundle)
	digestString := "sha256:" + hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(paths.artifactCache, hex.EncodeToString(digest[:])), bundle, 0644); err != nil {
		t.Fatal(err)
	}
	generationID := "provision-example-http-v1-" + hex.EncodeToString(digest[:])[:12]
	generation := planner.GenerationInput{
		ID: generationID, Revision: "provision-example-http-v1", ArtifactDigest: digestString, Account: "provision-lab",
		ReleaseDirectory: filepath.Join(paths.environmentHome, "releases", generationID),
	}
	systemd := planner.SystemdInput{
		GenerationID: generation.ID, Revision: generation.Revision, ArtifactDigest: generation.ArtifactDigest, Account: generation.Account,
		ReleaseDirectory: generation.ReleaseDirectory, Unit: "provision-lab-web-" + hex.EncodeToString(digest[:])[:12] + ".service", Port: 27811,
	}
	return paths, bootstrapRecord{Environment: "lab", Account: "provision-lab"}, generation, systemd
}

func nativeBundle(t *testing.T, name string, content []byte, mode int64) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
