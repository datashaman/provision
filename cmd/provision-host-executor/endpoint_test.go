package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

func TestAuthorizedEndpointSwitchRequiresVerificationAndRetainsPreviousGeneration(t *testing.T) {
	paths, record, signer, publicKey, firstGeneration, firstSystemd := authorizedCandidateFixture(t)
	firstServer := prepareHealthyCandidate(t, paths, record, signer, publicKey, firstGeneration, firstSystemd, 1)
	defer firstServer.Close()

	firstEndpoint := endpointInput(firstGeneration.GenerationReference, firstSystemd.Unit, firstSystemd.Port)
	firstSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &firstEndpoint}, Recovery: planner.RestorePreviousRoute}
	firstResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, firstSwitch, 4)
	if firstResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("first Endpoint switch = %+v", firstResult)
	}
	var firstObserved host.EndpointObservation
	if err := json.Unmarshal(firstResult.Observation, &firstObserved); err != nil || firstObserved.Status != host.EndpointActive || !firstObserved.CandidateVerified || !firstObserved.GracefulReload || !firstObserved.PreviousRetained || firstObserved.Previous != nil {
		t.Fatalf("first Endpoint observation = %+v, %v", firstObserved, err)
	}

	secondGeneration, secondSystemd := anotherCandidateFixture(t, paths, firstGeneration, firstSystemd)
	secondServer := prepareHealthyCandidate(t, paths, record, signer, publicKey, secondGeneration, secondSystemd, 5)
	defer secondServer.Close()

	previous := firstObserved.Active
	secondEndpoint := endpointInput(secondGeneration.GenerationReference, secondSystemd.Unit, secondSystemd.Port)
	secondSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &secondEndpoint, Previous: &previous}, Recovery: planner.RestorePreviousRoute}
	partiallySwitched, err := caddyServerConfiguration(secondEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.caddy.Replace(context.Background(), "/config/apps/http/servers/"+secondEndpoint.RouteID, partiallySwitched); err != nil {
		t.Fatal(err)
	}
	secondResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, secondSwitch, 8)
	if secondResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("second Endpoint switch = %+v", secondResult)
	}
	var secondObserved host.EndpointObservation
	if err := json.Unmarshal(secondResult.Observation, &secondObserved); err != nil || secondObserved.Status != host.EndpointActive || secondObserved.Previous == nil || secondObserved.Previous.ID != firstGeneration.ID || !secondObserved.Previous.UnitActive || !secondObserved.Previous.UnitMatches || secondObserved.Previous.RouteMatches {
		t.Fatalf("second Endpoint observation = %+v, %v", secondObserved, err)
	}
	controller := paths.systemd.(*fakeSystemdController)
	if !controller.active[firstSystemd.Unit] || !controller.active[secondSystemd.Unit] {
		t.Fatalf("blue-green units not retained: %+v", controller.active)
	}
	if observed := observeGeneration(firstGeneration); observed.Status != host.CandidateInstalled {
		t.Fatalf("previous Generation not retained: %+v", observed)
	}
	recorded, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || recorded == nil || recorded.Active.ID != secondGeneration.ID || recorded.Previous == nil || recorded.Previous.ID != firstGeneration.ID || recorded.DrainPolicy != httpDrainPolicy {
		t.Fatalf("durable active/previous record = %+v, %v", recorded, err)
	}
	upstream, listen, routeOK, err := observeCaddyEndpoint(context.Background(), paths.caddy, secondEndpoint.RouteID)
	if err != nil || !routeOK || upstream != secondEndpoint.Upstream || listen != secondEndpoint.ListenPort {
		t.Fatalf("stable Caddy route = %q, %d, %t, %v", upstream, listen, routeOK, err)
	}
}

func TestEndpointSwitchReportsUncertainForUnreconcilableRoute(t *testing.T) {
	paths, record, signer, publicKey, firstGeneration, firstSystemd := authorizedCandidateFixture(t)
	firstServer := prepareHealthyCandidate(t, paths, record, signer, publicKey, firstGeneration, firstSystemd, 1)
	defer firstServer.Close()
	firstEndpoint := endpointInput(firstGeneration.GenerationReference, firstSystemd.Unit, firstSystemd.Port)
	firstSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &firstEndpoint}, Recovery: planner.RestorePreviousRoute}
	firstResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, firstSwitch, 4)
	var firstObserved host.EndpointObservation
	if err := json.Unmarshal(firstResult.Observation, &firstObserved); err != nil || firstResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("first Endpoint switch = %+v, %+v, %v", firstResult, firstObserved, err)
	}

	secondGeneration, secondSystemd := anotherCandidateFixture(t, paths, firstGeneration, firstSystemd)
	secondServer := prepareHealthyCandidate(t, paths, record, signer, publicKey, secondGeneration, secondSystemd, 5)
	defer secondServer.Close()
	previous := firstObserved.Active
	secondEndpoint := endpointInput(secondGeneration.GenerationReference, secondSystemd.Unit, secondSystemd.Port)
	driftedEndpoint := secondEndpoint
	driftedEndpoint.Upstream = "127.0.0.1:29999"
	drifted, err := caddyServerConfiguration(driftedEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.caddy.Replace(context.Background(), "/config/apps/http/servers/"+secondEndpoint.RouteID, drifted); err != nil {
		t.Fatal(err)
	}

	secondSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &secondEndpoint, Previous: &previous}, Recovery: planner.RestorePreviousRoute}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, secondSwitch, 8)
	var observed host.EndpointObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeUncertain || observed.Status != host.EndpointUncertain || observed.Reason == "" || observed.RecoveryAction == "" {
		t.Fatalf("drifted Endpoint result = %+v, observation=%+v, error=%v", result, observed, err)
	}
	recorded, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || recorded == nil || recorded.Active.ID != firstGeneration.ID {
		t.Fatalf("ambiguous route changed durable active Generation: %+v, %v", recorded, err)
	}
}

func TestAuthorizedEndpointSwitchRejectsCandidateWithoutExactHostVerification(t *testing.T) {
	paths, record, signer, publicKey, generation, systemd := authorizedCandidateFixture(t)
	install := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}, Input: planner.OperationInput{Generation: &generation}}
	executeCandidateOperation(t, paths, record, signer, publicKey, install, 1)
	start := planner.Operation{ID: "op-03", Kind: planner.StartCandidate, DependsOn: []string{"op-02"}, Input: planner.OperationInput{Systemd: &systemd}}
	executeCandidateOperation(t, paths, record, signer, publicKey, start, 2)

	endpoint := endpointInput(generation.GenerationReference, systemd.Unit, systemd.Port)
	switchOperation := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &endpoint}, Recovery: planner.RestorePreviousRoute}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, switchOperation, 3)
	if result.Outcome != operation.OutcomeFailed || !strings.Contains(string(result.Observation), "no successful exact candidate verification") {
		t.Fatalf("unverified candidate switch = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(paths.environmentHome, "active-generation.json")); !os.IsNotExist(err) {
		t.Fatalf("unverified switch wrote active record: %v", err)
	}
	if len(paths.caddy.(*fakeCaddyController).servers) != 0 {
		t.Fatalf("unverified switch changed Caddy: %+v", paths.caddy.(*fakeCaddyController).servers)
	}
}

func TestAuthorizedEndpointSwitchFailsClosedWhenCaddyRejectsLoad(t *testing.T) {
	paths, record, signer, publicKey, generation, systemd := authorizedCandidateFixture(t)
	server := prepareHealthyCandidate(t, paths, record, signer, publicKey, generation, systemd, 1)
	defer server.Close()
	paths.caddy.(*fakeCaddyController).replaceErr = fmt.Errorf("invalid Caddy configuration")

	endpoint := endpointInput(generation.GenerationReference, systemd.Unit, systemd.Port)
	switchOperation := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &endpoint}, Recovery: planner.RestorePreviousRoute}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, switchOperation, 4)
	if result.Outcome != operation.OutcomeFailed || !strings.Contains(string(result.Observation), "atomic Caddy route load failed") {
		t.Fatalf("rejected Caddy load = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(paths.environmentHome, "active-generation.json")); !os.IsNotExist(err) {
		t.Fatalf("failed switch wrote active record: %v", err)
	}
	if !paths.systemd.(*fakeSystemdController).active[systemd.Unit] {
		t.Fatal("failed switch stopped the verified candidate")
	}
}

func TestPostSwitchVerificationAcceptsHealthyStableEndpoint(t *testing.T) {
	paths, record, signer, publicKey, generation, systemd := authorizedCandidateFixture(t)
	stable, stablePort := startStableEndpoint(t, paths, "provision-lab-web")
	defer stable.Close()
	candidate := prepareHealthyCandidate(t, paths, record, signer, publicKey, generation, systemd, 1)
	defer candidate.Close()

	endpoint := endpointInput(generation.GenerationReference, systemd.Unit, systemd.Port)
	endpoint.ListenPort = stablePort
	switchOperation := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &endpoint}, Recovery: planner.RestorePreviousRoute}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, switchOperation, 4)
	if result.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("Endpoint switch = %+v", result)
	}

	verify := activeVerificationOperation(endpoint, nil)
	result, _ = executeCandidateOperation(t, paths, record, signer, publicKey, verify, 5)
	var observed host.ActiveVerificationObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeSucceeded || observed.Status != host.ActiveVerificationHealthy || len(observed.ActiveChecks) != 3 || observed.RollbackAttempted {
		t.Fatalf("post-switch verification = %+v, observation=%+v, error=%v", result, observed, err)
	}
}

func TestPostSwitchVerificationRollsBackToHealthyPreviousGeneration(t *testing.T) {
	paths, record, signer, publicKey, firstGeneration, firstSystemd := authorizedCandidateFixture(t)
	stable, stablePort := startStableEndpoint(t, paths, "provision-lab-web")
	defer stable.Close()
	firstCandidate := prepareHealthyCandidate(t, paths, record, signer, publicKey, firstGeneration, firstSystemd, 1)
	defer firstCandidate.Close()
	firstEndpoint := endpointInput(firstGeneration.GenerationReference, firstSystemd.Unit, firstSystemd.Port)
	firstEndpoint.ListenPort = stablePort
	firstSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &firstEndpoint}, Recovery: planner.RestorePreviousRoute}
	firstResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, firstSwitch, 4)
	if firstResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("first Endpoint switch = %+v", firstResult)
	}
	var firstObserved host.EndpointObservation
	if err := json.Unmarshal(firstResult.Observation, &firstObserved); err != nil {
		t.Fatal(err)
	}

	secondGeneration, secondSystemd := anotherCandidateFixture(t, paths, firstGeneration, firstSystemd)
	secondCandidate := prepareCandidate(t, paths, record, signer, publicKey, secondGeneration, secondSystemd, 5, true)
	defer secondCandidate.Close()
	previous := firstObserved.Active
	secondEndpoint := endpointInput(secondGeneration.GenerationReference, secondSystemd.Unit, secondSystemd.Port)
	secondEndpoint.ListenPort = stablePort
	secondSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &secondEndpoint, Previous: &previous}, Recovery: planner.RestorePreviousRoute}
	secondResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, secondSwitch, 8)
	if secondResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("second Endpoint switch = %+v", secondResult)
	}

	verify := activeVerificationOperation(secondEndpoint, &previous)
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, verify, 9)
	var observed host.ActiveVerificationObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeFailed || observed.Status != host.ActiveVerificationRolledBack || !observed.RollbackAttempted || !observed.RollbackSucceeded || observed.Restored == nil || observed.Restored.ID != previous.ID || len(observed.PreviousChecks) != 3 || len(observed.RollbackChecks) != 3 {
		t.Fatalf("post-switch rollback = %+v, observation=%+v, error=%v", result, observed, err)
	}
	recorded, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || recorded == nil || recorded.Active.ID != previous.ID || recorded.Previous == nil || recorded.Previous.ID != secondGeneration.ID {
		t.Fatalf("rolled-back active record = %+v, %v", recorded, err)
	}
	upstream, _, routeOK, err := observeCaddyEndpoint(context.Background(), paths.caddy, secondEndpoint.RouteID)
	if err != nil || !routeOK || upstream != fmt.Sprintf("127.0.0.1:%d", previous.Port) {
		t.Fatalf("rolled-back stable route = %q, %t, %v", upstream, routeOK, err)
	}
	resumedResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, verify, 10)
	var resumed host.ActiveVerificationObservation
	if err := json.Unmarshal(resumedResult.Observation, &resumed); err != nil || resumedResult.Outcome != operation.OutcomeFailed || resumed.Status != host.ActiveVerificationRolledBack || !resumed.RollbackSucceeded || resumed.Restored == nil || resumed.Restored.ID != previous.ID || len(resumed.PreviousChecks) != 3 || len(resumed.RollbackChecks) != 3 || !strings.Contains(resumed.Reason, "interrupted attempt") {
		t.Fatalf("resumed rollback = %+v, observation=%+v, error=%v", resumedResult, resumed, err)
	}
}

func TestPostSwitchVerificationReportsUncertainWithoutPreviousGeneration(t *testing.T) {
	paths, record, signer, publicKey, generation, systemd := authorizedCandidateFixture(t)
	stable, stablePort := startStableEndpoint(t, paths, "provision-lab-web")
	defer stable.Close()
	candidate := prepareCandidate(t, paths, record, signer, publicKey, generation, systemd, 1, true)
	defer candidate.Close()
	endpoint := endpointInput(generation.GenerationReference, systemd.Unit, systemd.Port)
	endpoint.ListenPort = stablePort
	switchOperation := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &endpoint}, Recovery: planner.RestorePreviousRoute}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, switchOperation, 4)
	if result.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("Endpoint switch = %+v", result)
	}

	verify := activeVerificationOperation(endpoint, nil)
	result, _ = executeCandidateOperation(t, paths, record, signer, publicKey, verify, 5)
	var observed host.ActiveVerificationObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeUncertain || observed.Status != host.ActiveVerificationUncertain || observed.RollbackAttempted || observed.RollbackSucceeded || observed.Reason == "" || observed.RecoveryAction == "" {
		t.Fatalf("uncertain post-switch verification = %+v, observation=%+v, error=%v", result, observed, err)
	}
	recorded, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || recorded == nil || recorded.Active.ID != generation.ID || recorded.Previous != nil {
		t.Fatalf("uncertain active record = %+v, %v", recorded, err)
	}
}

func TestAdminCaddyControllerTreatsNullConfigurationAsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte("null\n"))
	}))
	defer server.Close()
	controller := adminCaddyController{client: server.Client(), baseURL: server.URL}

	data, err := controller.Read(context.Background(), "/config/apps/http/servers/provision-lab-web")
	if !errors.Is(err, errCaddyPathNotFound) || data != nil {
		t.Fatalf("Caddy null configuration = %q, %v; want absent path", data, err)
	}
}

func prepareHealthyCandidate(t *testing.T, paths executionPaths, record bootstrapRecord, signer authority.Signer, publicKey []byte, generation planner.GenerationInput, systemd planner.SystemdInput, firstToken int64) *httptest.Server {
	return prepareCandidate(t, paths, record, signer, publicKey, generation, systemd, firstToken, false)
}

func prepareCandidate(t *testing.T, paths executionPaths, record bootstrapRecord, signer authority.Signer, publicKey []byte, generation planner.GenerationInput, systemd planner.SystemdInput, firstToken int64, failThroughStableEndpoint bool) *httptest.Server {
	t.Helper()
	install := planner.Operation{ID: "op-02", Kind: planner.InstallGeneration, DependsOn: []string{"op-01"}, Input: planner.OperationInput{Generation: &generation}}
	installResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, install, firstToken)
	if installResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("install candidate = %+v", installResult)
	}
	start := planner.Operation{ID: "op-03", Kind: planner.StartCandidate, DependsOn: []string{"op-02"}, Input: planner.OperationInput{Systemd: &systemd}}
	startResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, start, firstToken+1)
	if startResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("start candidate = %+v", startResult)
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", systemd.Port))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if failThroughStableEndpoint && request.Host != fmt.Sprintf("127.0.0.1:%d", systemd.Port) {
			http.Error(response, "post-switch failure", http.StatusServiceUnavailable)
			return
		}
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
	health := planner.HealthInput{GenerationReference: generation.GenerationReference, Unit: systemd.Unit, LivenessPath: "/live", ReadinessPath: "/ready", CandidateVerifyPath: "/verify", Port: systemd.Port}
	verify := planner.Operation{ID: "op-04", Kind: planner.VerifyCandidate, DependsOn: []string{"op-03"}, Input: planner.OperationInput{Health: &health}}
	verifyResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, verify, firstToken+2)
	if verifyResult.Outcome != operation.OutcomeSucceeded {
		server.Close()
		t.Fatalf("verify candidate = %+v", verifyResult)
	}
	return server
}

func startStableEndpoint(t *testing.T, paths executionPaths, routeID string) (*httptest.Server, int) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstream, _, ok, observeErr := observeCaddyEndpoint(request.Context(), paths.caddy, routeID)
		if observeErr != nil || !ok {
			http.Error(response, "stable route unavailable", http.StatusServiceUnavailable)
			return
		}
		forward, requestErr := http.NewRequestWithContext(request.Context(), request.Method, "http://"+upstream+request.URL.RequestURI(), nil)
		if requestErr != nil {
			http.Error(response, "stable route invalid", http.StatusInternalServerError)
			return
		}
		forward.Host = request.Host
		result, requestErr := http.DefaultClient.Do(forward)
		if requestErr != nil {
			http.Error(response, "stable upstream unavailable", http.StatusBadGateway)
			return
		}
		defer result.Body.Close()
		for key, values := range result.Header {
			for _, value := range values {
				response.Header().Add(key, value)
			}
		}
		response.WriteHeader(result.StatusCode)
		_, _ = io.Copy(response, result.Body)
	}))
	server.Listener = listener
	server.Start()
	return server, listener.Addr().(*net.TCPAddr).Port
}

func activeVerificationOperation(endpoint planner.EndpointInput, previous *host.GenerationStatus) planner.Operation {
	health := planner.HealthInput{
		GenerationReference: endpoint.GenerationReference, Unit: endpoint.Unit,
		LivenessPath: "/live", ReadinessPath: "/ready", CandidateVerifyPath: "/verify", Port: endpoint.ListenPort,
	}
	return planner.Operation{ID: "op-06", Kind: planner.VerifyActive, DependsOn: []string{"op-05"}, Input: planner.OperationInput{Health: &health, Endpoint: &endpoint, Previous: previous}, Recovery: planner.RestorePreviousRoute}
}

func anotherCandidateFixture(t *testing.T, paths executionPaths, first planner.GenerationInput, firstSystemd planner.SystemdInput) (planner.GenerationInput, planner.SystemdInput) {
	t.Helper()
	bundle := nativeBundle(t, "provision-example-http", []byte("second candidate executable\n"), 0755)
	digest := sha256.Sum256(bundle)
	digestHex := hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(paths.artifactCache, digestHex), bundle, 0644); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	id := "provision-example-http-v2-" + digestHex[:12]
	reference := planner.GenerationReference{
		ID: id, Revision: "provision-example-http-v2", ArtifactDigest: "sha256:" + digestHex,
		Account: first.Account, ReleaseDirectory: filepath.Join(paths.environmentHome, "releases", id),
	}
	return planner.GenerationInput{GenerationReference: reference}, planner.SystemdInput{
		GenerationReference: reference, Unit: "provision-lab-web-" + digestHex[:12] + ".service", Port: port,
	}
}

func endpointInput(reference planner.GenerationReference, unit string, port int) planner.EndpointInput {
	return planner.EndpointInput{
		GenerationReference: reference, Unit: unit, RouteID: "provision-lab-web",
		ListenPort: 18080, Upstream: fmt.Sprintf("127.0.0.1:%d", port), UpstreamPort: port,
		DrainPolicy: httpDrainPolicy,
	}
}
