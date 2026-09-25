package main

import (
	"context"
	"crypto/ed25519"
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
	"time"

	"provision/internal/authority"
	"provision/internal/drain"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
	"provision/internal/rollbackwindow"
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

func TestAuthorizedHTTPDrainStopsOnlyExactPreviousUnitAndRetainsRollbackAssets(t *testing.T) {
	paths, record, signer, publicKey, firstGeneration, firstSystemd := authorizedCandidateFixture(t)
	stable, stablePort := startStableEndpoint(t, paths, "provision-lab-web")
	defer stable.Close()
	firstCandidate := prepareHealthyCandidate(t, paths, record, signer, publicKey, firstGeneration, firstSystemd, 1)
	defer firstCandidate.Close()
	firstEndpoint := endpointInput(firstGeneration.GenerationReference, firstSystemd.Unit, firstSystemd.Port)
	firstEndpoint.ListenPort = stablePort
	firstSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &firstEndpoint}, Recovery: planner.RestorePreviousRoute}
	firstResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, firstSwitch, 4)
	var firstObserved host.EndpointObservation
	if err := json.Unmarshal(firstResult.Observation, &firstObserved); err != nil || firstResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("first Endpoint switch = %+v, %+v, %v", firstResult, firstObserved, err)
	}

	secondGeneration, secondSystemd := anotherCandidateFixture(t, paths, firstGeneration, firstSystemd)
	secondCandidate := prepareHealthyCandidate(t, paths, record, signer, publicKey, secondGeneration, secondSystemd, 5)
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
	verified, _ := executeCandidateOperation(t, paths, record, signer, publicKey, verify, 9)
	if verified.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("stable verification = %+v", verified)
	}
	ageStableVerification(t, paths, 10*time.Second)

	drainInput := planner.DrainInput{Endpoint: secondEndpoint, Previous: previous, Mode: "bounded-http", MaxDuration: "2s"}
	drain := planner.Operation{ID: "op-07", Kind: planner.DrainPrevious, DependsOn: []string{"op-06"}, Input: planner.OperationInput{Drain: &drainInput}, Recovery: planner.RestorePreviousRoute}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, drain, 10)
	var observed host.DrainObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeSucceeded || observed.Status != host.DrainCompleted || !observed.BoundElapsed || !observed.StableRouteVerified || observed.PreviousUnitActive || !observed.PreviousUnitRetained || !observed.PreviousReleaseRetained {
		t.Fatalf("HTTP drain = %+v, observation=%+v, error=%v", result, observed, err)
	}
	controller := paths.systemd.(*fakeSystemdController)
	if controller.active[firstSystemd.Unit] || !controller.active[secondSystemd.Unit] || len(controller.stopped) != 1 || controller.stopped[0] != firstSystemd.Unit {
		t.Fatalf("drain stopped the wrong units: active=%+v stopped=%+v", controller.active, controller.stopped)
	}
	if _, err := os.Stat(filepath.Join(paths.systemdUnits, firstSystemd.Unit)); err != nil {
		t.Fatalf("previous unit definition was not retained: %v", err)
	}
	if observed := observeGeneration(firstGeneration); observed.Status != host.CandidateInstalled {
		t.Fatalf("previous release was not retained: %+v", observed)
	}
	resumed, err := observeCandidateOperation(context.Background(), drain, record, paths)
	if err != nil || resumed.State != "satisfied" {
		t.Fatalf("completed drain resumption = %+v, %v", resumed, err)
	}
}

func TestAuthorizedRollbackWindowRetentionRecordsDeadlineAndKeepsRestartableAssets(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drain := httpDrainOperation(endpoint, previous, "2s")
	drained, _ := executeCandidateOperation(t, paths, record, signer, publicKey, drain, 10)
	if drained.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("HTTP drain = %+v", drained)
	}

	retained := rollbackRetentionOperation(endpoint, previous, "30m0s")
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, retained, 11)
	var observed host.RetentionObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeSucceeded || observed.Status != host.RetentionCompleted || observed.Policy != rollbackwindow.RuleRollbackWindow || observed.RollbackWindow != "30m0s" || !observed.StableRouteVerified || observed.PreviousUnitActive || !observed.PreviousUnitRetained || !observed.PreviousGenerationDirectoryRetained || !observed.PreviousManifestRetained || !observed.PreviousArtifactRetained || !observed.Restartable || observed.CleanupPerformed || observed.RetainedAt == nil || observed.RetainUntil == nil {
		t.Fatalf("rollback-window retention = %+v, observation=%+v, error=%v", result, observed, err)
	}
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	current, err := readActiveGenerationRecord(activePath)
	if err != nil || current == nil || current.PreviousRollback == nil || current.PreviousRollback.OperationDigest == "" || current.PreviousRollback.Window != "30m0s" {
		t.Fatalf("durable retention marker = %+v, %v", current, err)
	}
	wantDeadline := current.SwitchedAt.Add(30 * time.Minute)
	if !observed.RetainUntil.Equal(wantDeadline) || !current.PreviousRollback.RetainUntil.Equal(wantDeadline) {
		t.Fatalf("retention deadline = %v / %v, want switch-derived %v", observed.RetainUntil, current.PreviousRollback.RetainUntil, wantDeadline)
	}
	if current.PreviousRollback.RecordedAt.Before(*current.PreviousDrainedAt) {
		t.Fatalf("retention recorded before drain: %+v", current)
	}
	if paths.systemd.(*fakeSystemdController).active[previous.SystemdUnit] {
		t.Fatal("retention restarted the drained previous unit")
	}
	resumed, err := observeCandidateOperation(context.Background(), retained, record, paths)
	if err != nil || resumed.State != "satisfied" {
		t.Fatalf("completed retention resumption = %+v, %v", resumed, err)
	}
}

func TestAuthorizedRollbackWindowRetentionRequiresExactDrainCompletion(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	retained := rollbackRetentionOperation(endpoint, previous, "30m0s")
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, retained, 10)
	var observed host.RetentionObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeFailed || observed.Status != host.RetentionFailed || !strings.Contains(observed.Reason, "no successful exact drain completion") {
		t.Fatalf("retention without drain = %+v, observation=%+v, error=%v", result, observed, err)
	}
	current, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || current == nil || current.PreviousRollback != nil {
		t.Fatalf("unverified retention changed durable state: %+v, %v", current, err)
	}
	if !paths.systemd.(*fakeSystemdController).active[previous.SystemdUnit] {
		t.Fatal("unverified retention changed the previous Generation")
	}
}

func TestAuthorizedRollbackWindowRetentionFailsWhenArtifactIsMissing(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drained, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	if drained.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("HTTP drain = %+v", drained)
	}
	artifactPath, err := host.ArtifactCachePath(paths.artifactCache, previous.ArtifactDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifactPath); err != nil {
		t.Fatal(err)
	}

	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, rollbackRetentionOperation(endpoint, previous, "30m0s"), 11)
	var observed host.RetentionObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeFailed || observed.Status != host.RetentionFailed || observed.PreviousArtifactRetained || observed.Restartable || observed.CleanupPerformed || observed.Reason == "" {
		t.Fatalf("retention with missing Artifact = %+v, observation=%+v, error=%v", result, observed, err)
	}
	current, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || current == nil || current.PreviousRollback != nil {
		t.Fatalf("failed retention changed durable state: %+v, %v", current, err)
	}
	if _, err := os.Stat(previous.ReleaseDirectory); err != nil {
		t.Fatalf("failed rollback-window recording removed the previous Generation directory: %v", err)
	}
}

func TestInterruptedRollbackWindowRetentionResumesWithoutShorteningDeadline(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drained, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	if drained.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("HTTP drain = %+v", drained)
	}
	retained := rollbackRetentionOperation(endpoint, previous, "30m0s")
	digest, err := planner.OperationDigest(retained)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 11, 0, 0, time.UTC)
	envelope := signedTestEnvelope(t, signer, retained, digest, record, "attempt-0000000000000000000000000000000b", 11, now, now.Add(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := executeAuthorized(ctx, envelope, record, publicKey, paths, now)
	if err != nil {
		t.Fatal(err)
	}
	var interrupted host.RetentionObservation
	if err := json.Unmarshal(result.Observation, &interrupted); err != nil || result.Outcome != operation.OutcomeUncertain || interrupted.Status != host.RetentionUncertain || !strings.Contains(interrupted.Reason, "interrupted") || interrupted.RecoveryAction == "" {
		t.Fatalf("interrupted retention = %+v, observation=%+v, error=%v", result, interrupted, err)
	}
	current, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || current == nil || current.PreviousRollback != nil {
		t.Fatalf("interrupted retention changed durable state: %+v, %v", current, err)
	}
	wantDeadline := current.SwitchedAt.Add(30 * time.Minute)

	resumed, _ := executeCandidateOperation(t, paths, record, signer, publicKey, retained, 12)
	var observed host.RetentionObservation
	if err := json.Unmarshal(resumed.Observation, &observed); err != nil || resumed.Outcome != operation.OutcomeSucceeded || observed.Status != host.RetentionCompleted || observed.RetainUntil == nil || !observed.RetainUntil.Equal(wantDeadline) {
		t.Fatalf("resumed retention = %+v, observation=%+v, error=%v", resumed, observed, err)
	}
}

func TestRollbackWindowRetentionCannotBeShortenedByAnotherSignedOperation(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drained, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	if drained.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("HTTP drain = %+v", drained)
	}
	initial := rollbackRetentionOperation(endpoint, previous, "30m0s")
	first, _ := executeCandidateOperation(t, paths, record, signer, publicKey, initial, 11)
	if first.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("initial retention = %+v", first)
	}
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	before, err := readActiveGenerationRecord(activePath)
	if err != nil || before == nil || before.PreviousRollback == nil {
		t.Fatalf("initial retention marker = %+v, %v", before, err)
	}
	wantDeadline := before.PreviousRollback.RetainUntil
	wantDigest := before.PreviousRollback.OperationDigest

	shorter := rollbackRetentionOperation(endpoint, previous, "10m0s")
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, shorter, 12)
	var observed host.RetentionObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeUncertain || observed.Status != host.RetentionUncertain || observed.RecoveryAction == "" {
		t.Fatalf("shortened retention = %+v, observation=%+v, error=%v", result, observed, err)
	}
	after, err := readActiveGenerationRecord(activePath)
	if err != nil || after == nil || after.PreviousRollback == nil || after.PreviousRollback.OperationDigest != wantDigest || after.PreviousRollback.Window != "30m0s" || !after.PreviousRollback.RetainUntil.Equal(wantDeadline) {
		t.Fatalf("shortened retention changed durable window: before=%+v after=%+v error=%v", before, after, err)
	}
}

func TestRollbackWindowRetentionFailsClosedOnStableRouteDrift(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drained, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	if drained.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("HTTP drain = %+v", drained)
	}
	drifted := endpoint
	drifted.Upstream = "127.0.0.1:29999"
	server, err := caddyServerConfiguration(drifted)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.caddy.Replace(context.Background(), "/config/apps/http/servers/"+endpoint.RouteID, server); err != nil {
		t.Fatal(err)
	}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, rollbackRetentionOperation(endpoint, previous, "30m0s"), 11)
	var observed host.RetentionObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeUncertain || observed.Status != host.RetentionUncertain || observed.RecoveryAction == "" || observed.CleanupPerformed {
		t.Fatalf("route-drift retention = %+v, observation=%+v, error=%v", result, observed, err)
	}
	current, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil || current == nil || current.PreviousRollback != nil {
		t.Fatalf("route drift changed durable retention state: %+v, %v", current, err)
	}
}

func TestRollbackWindowRetentionRejectsSelectingActiveGeneration(t *testing.T) {
	paths, record, signer, publicKey, endpoint, _ := prepareSwitchedTwoGenerationEndpoint(t, true)
	active := generationStatusFromEndpoint(endpoint)
	active.UnitActive, active.UnitMatches, active.RouteObserved, active.RouteMatches = true, true, true, true
	active.RouteUpstream = endpoint.Upstream
	retained := rollbackRetentionOperation(endpoint, active, "30m0s")
	digest, err := planner.OperationDigest(retained)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 10, 0, 0, time.UTC)
	envelope := signedTestEnvelope(t, signer, retained, digest, record, "attempt-0000000000000000000000000000000a", 10, now, now.Add(time.Minute))
	if _, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "cannot select the active Generation") {
		t.Fatalf("active Generation retention error = %v", err)
	}
}

func TestRollbackWindowRetentionAllowsDistinctGenerationsToShareArtifact(t *testing.T) {
	paths, record, _, _, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	previous.ArtifactDigest = endpoint.ArtifactDigest
	input := *rollbackRetentionOperation(endpoint, previous, "30m0s").Input.Retention
	if err := validateRetentionInput(input, record, paths); err != nil {
		t.Fatalf("distinct Generations sharing one content-addressed Artifact were rejected: %v", err)
	}
}

func TestRollbackWindowRetentionRejectsMalformedSignedPolicy(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	tests := []struct {
		name   string
		mutate func(*planner.RetentionInput)
	}{
		{name: "unknown policy", mutate: func(input *planner.RetentionInput) { input.Policy = "forever" }},
		{name: "noncanonical window", mutate: func(input *planner.RetentionInput) { input.RollbackWindow = "30m" }},
		{name: "unsupported window", mutate: func(input *planner.RetentionInput) { input.RollbackWindow = "30s" }},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planned := rollbackRetentionOperation(endpoint, previous, "30m0s")
			test.mutate(planned.Input.Retention)
			digest, err := planner.OperationDigest(planned)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 24, 12, 10, 0, 0, time.UTC)
			envelope := signedTestEnvelope(t, signer, planned, digest, record, fmt.Sprintf("attempt-%032x", index+20), int64(index+20), now, now.Add(time.Minute))
			if _, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now); err == nil {
				t.Fatal("malformed signed retention operation unexpectedly passed validation")
			}
		})
	}
}

func TestStaleRollbackWindowRetentionCannotChangeRecordedWindow(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drained, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	if drained.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("HTTP drain = %+v", drained)
	}
	retained := rollbackRetentionOperation(endpoint, previous, "30m0s")
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, retained, 11)
	if result.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("retention = %+v", result)
	}
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	before, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := planner.OperationDigest(retained)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 12, 0, 0, time.UTC)
	stale := signedTestEnvelope(t, signer, retained, digest, record, "attempt-0000000000000000000000000000000c", 10, now, now.Add(time.Minute))
	if _, err := executeAuthorized(context.Background(), stale, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale retention error = %v", err)
	}
	after, err := os.ReadFile(activePath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("stale retention changed durable record: %v", err)
	}
}

func TestAuthorizedHTTPDrainRequiresExactStableVerification(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, false)
	drain := httpDrainOperation(endpoint, previous, "2s")
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, drain, 9)
	var observed host.DrainObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeFailed || observed.Status != host.DrainFailed || !strings.Contains(observed.Reason, "no successful exact stable Endpoint verification") {
		t.Fatalf("unverified HTTP drain = %+v, observation=%+v, error=%v", result, observed, err)
	}
	controller := paths.systemd.(*fakeSystemdController)
	if !controller.active[previous.SystemdUnit] || len(controller.stopped) != 0 {
		t.Fatalf("unverified drain changed previous unit: active=%+v stopped=%+v", controller.active, controller.stopped)
	}
}

func TestAuthorizedHTTPDrainFailsClosedOnRouteDrift(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drifted := endpoint
	drifted.Upstream = "127.0.0.1:29999"
	server, err := caddyServerConfiguration(drifted)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.caddy.Replace(context.Background(), "/config/apps/http/servers/"+endpoint.RouteID, server); err != nil {
		t.Fatal(err)
	}
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	var observed host.DrainObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeUncertain || observed.Status != host.DrainUncertain || observed.RecoveryAction == "" {
		t.Fatalf("route-drift HTTP drain = %+v, observation=%+v, error=%v", result, observed, err)
	}
	controller := paths.systemd.(*fakeSystemdController)
	if !controller.active[previous.SystemdUnit] || len(controller.stopped) != 0 {
		t.Fatalf("route-drift drain changed previous unit: active=%+v stopped=%+v", controller.active, controller.stopped)
	}
}

func TestAuthorizedHTTPDrainReportsKnownStopFailure(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	controller := paths.systemd.(*fakeSystemdController)
	controller.stopErr = errors.New("systemd refused stop")
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	var observed host.DrainObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeFailed || observed.Status != host.DrainFailed || observed.Reason == "" {
		t.Fatalf("failed HTTP drain = %+v, observation=%+v, error=%v", result, observed, err)
	}
	if !controller.active[previous.SystemdUnit] {
		t.Fatal("known stop failure was reported after previous unit became inactive")
	}
}

func TestAuthorizedHTTPDrainReconcilesStoppedUnitWithoutBlindReplay(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	controller := paths.systemd.(*fakeSystemdController)
	controller.active[previous.SystemdUnit] = false
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "2s"), 10)
	var observed host.DrainObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeSucceeded || observed.Status != host.DrainCompleted {
		t.Fatalf("reconciled HTTP drain = %+v, observation=%+v, error=%v", result, observed, err)
	}
	if len(controller.stopped) != 0 {
		t.Fatalf("reconciliation blindly replayed systemd stop: %+v", controller.stopped)
	}
}

func TestAuthorizedHTTPDrainWaitsOnlyRemainingBound(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	current, err := readActiveGenerationRecord(activePath)
	if err != nil || current == nil {
		t.Fatalf("read active record: %+v, %v", current, err)
	}
	drainNow := time.Now().UTC()
	stableVerifiedAt := drainNow.Add(-800 * time.Millisecond)
	current.StableVerifiedAt = &stableVerifiedAt
	if err := writeJSONAtomic(activePath, *current, 0644); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, _ := executeCandidateOperation(t, paths, record, signer, publicKey, httpDrainOperation(endpoint, previous, "1s"), 10)
	elapsed := time.Since(started)
	if result.Outcome != operation.OutcomeSucceeded || elapsed < 150*time.Millisecond || elapsed > 800*time.Millisecond {
		t.Fatalf("remaining drain wait = %s, result=%+v", elapsed, result)
	}
}

func TestAuthorizedHTTPDrainInterruptionResumesFromRecordedSwitchTime(t *testing.T) {
	paths, record, signer, publicKey, endpoint, previous := prepareSwitchedTwoGenerationEndpoint(t, true)
	drain := httpDrainOperation(endpoint, previous, "1s")
	digest, err := planner.OperationDigest(drain)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 10, 0, 0, time.UTC)
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	current, err := readActiveGenerationRecord(activePath)
	if err != nil || current == nil {
		t.Fatalf("read active record: %+v, %v", current, err)
	}
	stableVerifiedAt := time.Now().UTC()
	current.StableVerifiedAt = &stableVerifiedAt
	if err := writeJSONAtomic(activePath, *current, 0644); err != nil {
		t.Fatal(err)
	}

	interrupted := signedTestEnvelope(t, signer, drain, digest, record, "attempt-0000000000000000000000000000000a", 10, now, now.Add(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	result, err := executeAuthorized(ctx, interrupted, record, publicKey, paths, now)
	if err != nil {
		t.Fatal(err)
	}
	var observed host.DrainObservation
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeUncertain || observed.Status != host.DrainUncertain || !strings.Contains(observed.Reason, "interrupted") {
		t.Fatalf("interrupted HTTP drain = %+v, observation=%+v, error=%v", result, observed, err)
	}
	controller := paths.systemd.(*fakeSystemdController)
	if !controller.active[previous.SystemdUnit] || len(controller.stopped) != 0 {
		t.Fatalf("interrupted wait changed previous unit: active=%+v stopped=%+v", controller.active, controller.stopped)
	}

	current, err = readActiveGenerationRecord(activePath)
	if err != nil || current == nil {
		t.Fatalf("reread active record: %+v, %v", current, err)
	}
	stableVerifiedAt = time.Now().UTC().Add(-2 * time.Second)
	current.StableVerifiedAt = &stableVerifiedAt
	if err := writeJSONAtomic(activePath, *current, 0644); err != nil {
		t.Fatal(err)
	}
	resumedAt := now.Add(2 * time.Second)
	resumed := signedTestEnvelope(t, signer, drain, digest, record, "attempt-0000000000000000000000000000000b", 11, resumedAt, resumedAt.Add(time.Minute))
	result, err = executeAuthorized(context.Background(), resumed, record, publicKey, paths, resumedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.Observation, &observed); err != nil || result.Outcome != operation.OutcomeSucceeded || observed.Status != host.DrainCompleted || len(controller.stopped) != 1 || controller.stopped[0] != previous.SystemdUnit {
		t.Fatalf("resumed HTTP drain = %+v, observation=%+v, stopped=%+v, error=%v", result, observed, controller.stopped, err)
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

func httpDrainOperation(endpoint planner.EndpointInput, previous host.GenerationStatus, maxDuration string) planner.Operation {
	plannedDrain := planner.DrainInput{Endpoint: endpoint, Previous: previous, Mode: drain.ModeBoundedHTTP, MaxDuration: drain.Bound(maxDuration)}
	return planner.Operation{ID: "op-07", Kind: planner.DrainPrevious, DependsOn: []string{"op-06"}, Input: planner.OperationInput{Drain: &plannedDrain}, Recovery: planner.RestorePreviousRoute}
}

func rollbackRetentionOperation(endpoint planner.EndpointInput, previous host.GenerationStatus, window string) planner.Operation {
	plannedRetention := planner.RetentionInput{Endpoint: endpoint, Previous: previous, Policy: rollbackwindow.RuleRollbackWindow, RollbackWindow: rollbackwindow.Window(window)}
	return planner.Operation{ID: "op-08", Kind: planner.RetainPrevious, DependsOn: []string{"op-07"}, Input: planner.OperationInput{Retention: &plannedRetention}, Recovery: planner.RetainBothGenerations}
}

func prepareSwitchedTwoGenerationEndpoint(t *testing.T, verifyActive bool) (executionPaths, bootstrapRecord, authority.Signer, ed25519.PublicKey, planner.EndpointInput, host.GenerationStatus) {
	t.Helper()
	paths, record, signer, publicKey, firstGeneration, firstSystemd := authorizedCandidateFixture(t)
	stable, stablePort := startStableEndpoint(t, paths, "provision-lab-web")
	t.Cleanup(stable.Close)
	firstCandidate := prepareHealthyCandidate(t, paths, record, signer, publicKey, firstGeneration, firstSystemd, 1)
	t.Cleanup(firstCandidate.Close)
	firstEndpoint := endpointInput(firstGeneration.GenerationReference, firstSystemd.Unit, firstSystemd.Port)
	firstEndpoint.ListenPort = stablePort
	firstSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &firstEndpoint}, Recovery: planner.RestorePreviousRoute}
	firstResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, firstSwitch, 4)
	var firstObserved host.EndpointObservation
	if err := json.Unmarshal(firstResult.Observation, &firstObserved); err != nil || firstResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("first Endpoint switch = %+v, %+v, %v", firstResult, firstObserved, err)
	}

	secondGeneration, secondSystemd := anotherCandidateFixture(t, paths, firstGeneration, firstSystemd)
	secondCandidate := prepareHealthyCandidate(t, paths, record, signer, publicKey, secondGeneration, secondSystemd, 5)
	t.Cleanup(secondCandidate.Close)
	previous := firstObserved.Active
	secondEndpoint := endpointInput(secondGeneration.GenerationReference, secondSystemd.Unit, secondSystemd.Port)
	secondEndpoint.ListenPort = stablePort
	secondSwitch := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, DependsOn: []string{"op-04"}, Input: planner.OperationInput{Endpoint: &secondEndpoint, Previous: &previous}, Recovery: planner.RestorePreviousRoute}
	secondResult, _ := executeCandidateOperation(t, paths, record, signer, publicKey, secondSwitch, 8)
	if secondResult.Outcome != operation.OutcomeSucceeded {
		t.Fatalf("second Endpoint switch = %+v", secondResult)
	}
	if verifyActive {
		verified, _ := executeCandidateOperation(t, paths, record, signer, publicKey, activeVerificationOperation(secondEndpoint, &previous), 9)
		if verified.Outcome != operation.OutcomeSucceeded {
			t.Fatalf("stable verification = %+v", verified)
		}
		ageStableVerification(t, paths, 10*time.Second)
	}
	return paths, record, signer, publicKey, secondEndpoint, previous
}

func ageStableVerification(t *testing.T, paths executionPaths, age time.Duration) {
	t.Helper()
	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	current, err := readActiveGenerationRecord(activePath)
	if err != nil || current == nil || current.StableVerifiedAt == nil {
		t.Fatalf("read stable verification marker: %+v, %v", current, err)
	}
	verifiedAt := time.Now().UTC().Add(-age)
	if verifiedAt.Before(current.SwitchedAt) {
		verifiedAt = current.SwitchedAt
	}
	current.StableVerifiedAt = &verifiedAt
	if err := writeJSONAtomic(activePath, *current, 0644); err != nil {
		t.Fatal(err)
	}
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
