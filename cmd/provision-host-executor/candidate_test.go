package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

type fakeSystemdController struct {
	active   map[string]bool
	startErr error
}

type fakeCaddyController struct {
	servers    map[string][]byte
	replaceErr error
}

func (controller *fakeCaddyController) Read(_ context.Context, path string) ([]byte, error) {
	const serverPrefix = "/config/apps/http/servers/"
	if strings.HasPrefix(path, serverPrefix) {
		server, ok := controller.servers[strings.TrimPrefix(path, serverPrefix)]
		if !ok {
			return nil, errCaddyPathNotFound
		}
		return append([]byte(nil), server...), nil
	}
	if strings.HasPrefix(path, "/id/") {
		server, ok := controller.servers[strings.TrimPrefix(path, "/id/")]
		if !ok {
			return nil, errCaddyPathNotFound
		}
		return append([]byte(nil), server...), nil
	}
	return nil, errCaddyPathNotFound
}

func (controller *fakeCaddyController) Replace(_ context.Context, path string, body []byte) error {
	if controller.replaceErr != nil {
		return controller.replaceErr
	}
	controller.servers[strings.TrimPrefix(path, "/config/apps/http/servers/")] = append([]byte(nil), body...)
	return nil
}

func (controller *fakeCaddyController) Delete(_ context.Context, path string) error {
	delete(controller.servers, strings.TrimPrefix(path, "/config/apps/http/servers/"))
	return nil
}

func (controller *fakeSystemdController) Run(_ context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "daemon-reload":
		return nil, nil
	case "start":
		if controller.startErr != nil {
			return []byte("candidate crashed"), controller.startErr
		}
		controller.active[args[1]] = true
		return nil, nil
	case "stop":
		controller.active[args[1]] = false
		return nil, nil
	case "is-active":
		if controller.active[args[1]] {
			return []byte("active\n"), nil
		}
	}
	return []byte("inactive\n"), errors.New("inactive")
}

func TestAuthorizedCandidateLifecyclePreservesActiveAndCleansFailedCandidate(t *testing.T) {
	paths, record, signer, publicKey, generation, systemd := authorizedCandidateFixture(t)
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	active := writeActiveRecordFixture(t, activePath, paths.environmentHome)

	install := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}, Input: planner.OperationInput{Generation: &generation}}
	installResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, install, 1)
	if installResult.Outcome != operation.OutcomeSucceeded || !strings.Contains(string(installResult.Observation), `"status":"installed"`) {
		t.Fatalf("authorized install = %+v", installResult)
	}

	start := planner.Operation{ID: "op-03", Kind: planner.StartCandidate, DependsOn: []string{"op-02"}, Input: planner.OperationInput{Systemd: &systemd}}
	startResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, start, 2)
	if startResult.Outcome != operation.OutcomeSucceeded || !strings.Contains(string(startResult.Observation), `"status":"active"`) {
		t.Fatalf("authorized start = %+v", startResult)
	}
	unit, err := os.ReadFile(filepath.Join(paths.systemdUnits, systemd.Unit))
	if err != nil || !strings.Contains(string(unit), "PROVISION_HTTP_LISTEN=127.0.0.1:") || !strings.Contains(string(unit), "PROVISION_REVISION="+generation.Revision) || !strings.Contains(string(unit), "IPAddressDeny=any\nIPAddressAllow=localhost") {
		t.Fatalf("candidate unit is not private and Revision-bound:\n%s\n%v", unit, err)
	}

	reportedRevision := generation.Revision
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", systemd.Port))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live", "/ready":
			response.WriteHeader(http.StatusNoContent)
		case "/verify":
			_ = json.NewEncoder(response).Encode(map[string]string{"revision": reportedRevision})
		default:
			http.NotFound(response, request)
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	health := planner.HealthInput{GenerationReference: generation.GenerationReference, Unit: systemd.Unit, LivenessPath: "/live", ReadinessPath: "/ready", CandidateVerifyPath: "/verify", Port: systemd.Port}
	verify := planner.Operation{ID: "op-04", Kind: planner.VerifyCandidate, DependsOn: []string{"op-03"}, Input: planner.OperationInput{Health: &health}}
	preflight, err := observeCandidateOperation(context.Background(), verify, record, paths)
	if err != nil || preflight.State != "pending" || !strings.Contains(string(preflight.Evidence), `"switchEligible":true`) {
		t.Fatalf("healthy verification preflight = %+v, %v; want pending evidence so signed verification executes", preflight, err)
	}
	verifyResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, verify, 3)
	if verifyResult.Outcome != operation.OutcomeSucceeded || !strings.Contains(string(verifyResult.Observation), `"candidateActive":true`) || !strings.Contains(string(verifyResult.Observation), `"switchEligible":true`) {
		t.Fatalf("authorized candidate verification = %+v", verifyResult)
	}
	assertActiveUnchanged(t, activePath, active)

	reportedRevision = "wrong-revision"
	failedResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, verify, 4)
	if failedResult.Outcome != operation.OutcomeFailed || !strings.Contains(string(failedResult.Observation), `"candidateCleaned":true`) || !strings.Contains(string(failedResult.Observation), "candidateVerification check failed") {
		t.Fatalf("failed candidate verification = %+v", failedResult)
	}
	assertActiveUnchanged(t, activePath, active)
	if _, err := os.Stat(generation.ReleaseDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed candidate Generation was not cleaned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.systemdUnits, systemd.Unit)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed candidate unit was not cleaned: %v", err)
	}
}

func TestAuthorizedCandidateOperationRejectsPlanTampering(t *testing.T) {
	paths, record, signer, publicKey, generation, _ := authorizedCandidateFixture(t)
	planned := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}, Input: planner.OperationInput{Generation: &generation}}
	digest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	envelope := signedTestEnvelope(t, signer, planned, digest, record, "attempt-44444444444444444444444444444444", 1, now, now.Add(time.Minute))
	tampered := envelope
	tampered.Operation.Input.Generation = &planner.GenerationInput{GenerationReference: generation.GenerationReference}
	tampered.Operation.Input.Generation.ReleaseDirectory = filepath.Join(paths.environmentHome, "elsewhere", generation.ID)
	if _, err := executeAuthorized(context.Background(), tampered, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "digest does not match") {
		t.Fatalf("tampered candidate operation accepted: %v", err)
	}
}

func TestAuthorizedUnsafeBundleFailsWithoutChangingActiveGeneration(t *testing.T) {
	paths, record, signer, publicKey, generation, _ := authorizedCandidateFixture(t)
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	active := writeActiveRecordFixture(t, activePath, paths.environmentHome)
	unsafe := nativeBundle(t, "../escape", []byte("bad\n"), 0755)
	digest := sha256.Sum256(unsafe)
	generation.ArtifactDigest = "sha256:" + hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(paths.artifactCache, hex.EncodeToString(digest[:])), unsafe, 0644); err != nil {
		t.Fatal(err)
	}
	planned := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}, Input: planner.OperationInput{Generation: &generation}}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, planned, 1)
	if result.Outcome != operation.OutcomeFailed || !strings.Contains(string(result.Observation), `"status":"failed"`) {
		t.Fatalf("unsafe bundle result = %+v", result)
	}
	assertActiveUnchanged(t, activePath, active)
	if _, err := os.Stat(generation.ReleaseDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed installation left a Generation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.environmentHome, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe bundle escaped release directory: %v", err)
	}
}

func TestAuthorizedFailedStartCleansOnlyCandidateUnit(t *testing.T) {
	paths, record, signer, publicKey, generation, systemd := authorizedCandidateFixture(t)
	controller := paths.systemd.(*fakeSystemdController)
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	active := writeActiveRecordFixture(t, activePath, paths.environmentHome)
	previousRelease := filepath.Join(paths.environmentHome, "releases", "previous-generation")
	if err := os.MkdirAll(previousRelease, 0755); err != nil {
		t.Fatal(err)
	}
	previousUnit := filepath.Join(paths.systemdUnits, "provision-lab-web-previous.service")
	if err := os.WriteFile(previousUnit, []byte("previous unit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	install := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}, Input: planner.OperationInput{Generation: &generation}}
	executeCandidateOperation(t, paths, record, signer, publicKey, install, 1)
	controller.startErr = errors.New("start failed")
	start := planner.Operation{ID: "op-03", Kind: planner.StartCandidate, DependsOn: []string{"op-02"}, Input: planner.OperationInput{Systemd: &systemd}}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, start, 2)
	if result.Outcome != operation.OutcomeFailed || !strings.Contains(string(result.Observation), "start candidate systemd unit") {
		t.Fatalf("failed start result = %+v", result)
	}
	assertActiveUnchanged(t, activePath, active)
	if _, err := os.Stat(previousRelease); err != nil {
		t.Fatalf("previous release removed: %v", err)
	}
	if data, err := os.ReadFile(previousUnit); err != nil || string(data) != "previous unit\n" {
		t.Fatalf("previous unit changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(paths.systemdUnits, systemd.Unit)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed candidate unit was not cleaned: %v", err)
	}
}

func executeCandidateOperation(t *testing.T, paths executionPaths, record bootstrapRecord, signer authority.Signer, publicKey ed25519.PublicKey, planned planner.Operation, token int64) (operation.Result, operation.Envelope) {
	t.Helper()
	digest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC).Add(time.Duration(token) * time.Minute)
	attempt := fmt.Sprintf("attempt-%032x", token)
	envelope := signedTestEnvelope(t, signer, planned, digest, record, attempt, token, now, now.Add(time.Minute))
	result, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now)
	if err != nil {
		t.Fatalf("execute %s: %v", planned.Kind, err)
	}
	return result, envelope
}

func authorizedCandidateFixture(t *testing.T) (executionPaths, bootstrapRecord, authority.Signer, ed25519.PublicKey, planner.GenerationInput, planner.SystemdInput) {
	t.Helper()
	root := t.TempDir()
	controller := &fakeSystemdController{active: map[string]bool{}}
	paths := executionPaths{
		authorityState: filepath.Join(root, "authority-state"), artifactCache: filepath.Join(root, "artifacts"),
		environmentHome: filepath.Join(root, "environments", "lab"), systemdUnits: filepath.Join(root, "systemd"),
		systemd: controller, caddy: &fakeCaddyController{servers: map[string][]byte{}}, healthTimeout: 20 * time.Millisecond,
	}
	for _, directory := range []string{paths.authorityState, paths.artifactCache, paths.environmentHome, paths.systemdUnits} {
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
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	generationID := "provision-example-http-v1-" + hex.EncodeToString(digest[:])[:12]
	reference := planner.GenerationReference{ID: generationID, Revision: "provision-example-http-v1", ArtifactDigest: digestString, Account: "provision-lab", ReleaseDirectory: filepath.Join(paths.environmentHome, "releases", generationID)}
	generation := planner.GenerationInput{GenerationReference: reference}
	systemd := planner.SystemdInput{GenerationReference: reference, Unit: "provision-lab-web-" + hex.EncodeToString(digest[:])[:12] + ".service", Port: port}
	privatePath := filepath.Join(root, "authority.key")
	publicPath := filepath.Join(root, "authority.pub")
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
	record := bootstrapRecord{SchemaVersion: "provision.dev/bootstrap/v2", Environment: "lab", Operator: "operator", Account: "provision-lab", ExecutorDigest: "sha256:" + strings.Repeat("e", 64), AuthorityKeyID: keyInfo.ID}
	return paths, record, signer, publicKey, generation, systemd
}

func assertActiveUnchanged(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(expected) {
		t.Fatalf("active generation changed: %q, %v", actual, err)
	}
}

func writeActiveRecordFixture(t *testing.T, path, environmentHome string) []byte {
	t.Helper()
	record := host.ActiveGenerationRecord{
		SchemaVersion:                        activeGenerationSchema,
		PlanID:                               "sha256:" + strings.Repeat("a", 64),
		CandidateVerificationOperationDigest: "sha256:" + strings.Repeat("b", 64),
		Active: host.GenerationStatus{
			ID: "previous-generation", Revision: "previous-revision",
			ArtifactDigest:   "sha256:" + strings.Repeat("c", 64),
			SystemdUnit:      "provision-lab-web-previous.service",
			ReleaseDirectory: filepath.Join(environmentHome, "releases", "previous-generation"),
			Port:             28080, RouteID: "provision-lab-web",
		},
		ListenPort: 18080, DrainPolicy: httpDrainPolicy,
		SwitchedAt: time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}
	return encoded
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
