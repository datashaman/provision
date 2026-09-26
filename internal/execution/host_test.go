package execution

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"provision/internal/authority"
	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
	"provision/internal/rollbackwindow"
)

func TestHostObservationCarriesKnownFailedOutcomeForReconciliation(t *testing.T) {
	command := exec.Command("sh", "-c", `cat >/dev/null; printf '%s' '{"state":"satisfied","outcome":"failed","evidence":{"status":"rolled-back","reason":"candidate failed"}}'`)
	observed, err := executeHostObservation(command, planner.Operation{}, "test Host observation")
	if err != nil || observed.State != ObservationSatisfied || observed.Outcome != operation.OutcomeFailed || !strings.Contains(string(observed.Evidence), "rolled-back") {
		t.Fatalf("known failed observation = %+v, %v", observed, err)
	}

	command = exec.Command("sh", "-c", `cat >/dev/null; printf '%s' '{"state":"unknown","outcome":"failed","evidence":{"status":"uncertain"}}'`)
	if _, err := executeHostObservation(command, planner.Operation{}, "test Host observation"); err == nil || !strings.Contains(err.Error(), "non-satisfied") {
		t.Fatalf("non-satisfied outcome was accepted: %v", err)
	}
}

func TestPlannedArtifactInputAcceptsTypedAsyncArtifact(t *testing.T) {
	planned := planner.Operation{Kind: planner.StageArtifact, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Artifact: &planner.AsyncArtifactInput{Component: "consumer", Role: "worker", Source: "https://example.invalid/worker.tar.gz", Digest: "sha256:" + strings.Repeat("a", 64)}}}}
	artifact, err := plannedArtifactInput(planned)
	if err != nil || artifact.Source != "https://example.invalid/worker.tar.gz" || artifact.Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("typed asynchronous Artifact was not normalized: %#v, %v", artifact, err)
	}
}

func TestVerifyEndpointResultBindsActiveAndPreviousGenerations(t *testing.T) {
	reference := planner.GenerationReference{
		ID: "provision-example-http-v2-bbbbbbbbbbbb", Revision: "provision-example-http-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), Account: "provision-lab",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v2-bbbbbbbbbbbb",
	}
	endpoint := planner.EndpointInput{
		GenerationReference: reference, Unit: "provision-lab-web-bbbbbbbbbbbb.service",
		RouteID: "provision-lab-web", ListenPort: 18080,
		Upstream: "127.0.0.1:28082", UpstreamPort: 28082, DrainPolicy: "caddy-graceful-config-reload",
	}
	previous := host.GenerationStatus{
		ID: "provision-example-http-v1-aaaaaaaaaaaa", Revision: "provision-example-http-v1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SystemdUnit: "provision-lab-web-aaaaaaaaaaaa.service",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v1-aaaaaaaaaaaa",
		Port:             28081, RouteID: "provision-lab-web", UnitActive: true, UnitMatches: true,
	}
	planned := planner.Operation{ID: "op-05", Kind: planner.SwitchEndpoint, Input: planner.OperationInput{Endpoint: &endpoint, Previous: &previous}}
	envelope := operation.Envelope{Authorization: authority.Proof{Claim: authority.Claim{PlanID: "sha256:" + strings.Repeat("c", 64), OperationID: "op-05", AttemptID: "attempt-11111111111111111111111111111111", FencingToken: 5}}, Operation: planned}
	observedPrevious := previous
	observed := host.EndpointObservation{
		Status: host.EndpointActive, RouteID: endpoint.RouteID, ListenPort: endpoint.ListenPort, Upstream: endpoint.Upstream,
		Active: host.GenerationStatus{
			ID: endpoint.ID, Revision: endpoint.Revision, ArtifactDigest: endpoint.ArtifactDigest,
			SystemdUnit: endpoint.Unit, ReleaseDirectory: endpoint.ReleaseDirectory, Port: endpoint.UpstreamPort, RouteID: endpoint.RouteID,
			UnitActive: true, UnitMatches: true, RouteObserved: true, RouteUpstream: endpoint.Upstream, RouteMatches: true,
		},
		Previous: &observedPrevious, CandidateVerified: true, GracefulReload: true, PreviousRetained: true, DrainPolicy: endpoint.DrainPolicy,
	}
	evidence, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	result := operation.Result{
		SchemaVersion: operation.ResultSchemaVersion, PlanID: envelope.Authorization.Claim.PlanID,
		OperationID: "op-05", AttemptID: envelope.Authorization.Claim.AttemptID, FencingToken: 5,
		Outcome: operation.OutcomeSucceeded, Observation: evidence,
	}
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("valid Endpoint result rejected: %v", err)
	}
	result.Outcome = operation.OutcomeUncertain
	if err := verifyHostResult(envelope, result); err == nil {
		t.Fatal("active Endpoint observation accepted as uncertain")
	}
	observed.Status = host.EndpointUncertain
	observed.CandidateVerified = false
	observed.GracefulReload = false
	observed.Reason = "route matches neither signed Generation"
	observed.RecoveryAction = "inspect the stable Endpoint"
	result.Observation = mustJSON(t, observed)
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("valid uncertain Endpoint result rejected: %v", err)
	}

	result.Outcome = operation.OutcomeSucceeded
	observed.Status = host.EndpointActive
	observed.CandidateVerified = true
	observed.GracefulReload = true
	observed.Reason = ""
	observed.RecoveryAction = ""
	observed.Previous.ID = "different-generation"
	result.Observation, _ = json.Marshal(observed)
	if err := verifyHostResult(envelope, result); err == nil || !strings.Contains(err.Error(), "previous Generation") {
		t.Fatalf("tampered previous Generation accepted: %v", err)
	}
}

func TestVerifyActiveResultDistinguishesHealthyRollbackAndUncertain(t *testing.T) {
	reference := planner.GenerationReference{
		ID: "provision-example-http-v2-bbbbbbbbbbbb", Revision: "provision-example-http-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), Account: "provision-lab",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v2-bbbbbbbbbbbb",
	}
	endpoint := planner.EndpointInput{
		GenerationReference: reference, Unit: "provision-lab-web-bbbbbbbbbbbb.service",
		RouteID: "provision-lab-web", ListenPort: 18080,
		Upstream: "127.0.0.1:28082", UpstreamPort: 28082, DrainPolicy: "caddy-graceful-config-reload",
	}
	health := planner.HealthInput{GenerationReference: reference, Unit: endpoint.Unit, LivenessPath: "/live", ReadinessPath: "/ready", CandidateVerifyPath: "/verify", Port: endpoint.ListenPort}
	previous := host.GenerationStatus{
		ID: "provision-example-http-v1-aaaaaaaaaaaa", Revision: "provision-example-http-v1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SystemdUnit: "provision-lab-web-aaaaaaaaaaaa.service",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v1-aaaaaaaaaaaa",
		Port:             28081, RouteID: endpoint.RouteID, UnitActive: true, UnitMatches: true,
	}
	candidate := host.GenerationStatus{
		ID: endpoint.ID, Revision: endpoint.Revision, ArtifactDigest: endpoint.ArtifactDigest,
		SystemdUnit: endpoint.Unit, ReleaseDirectory: endpoint.ReleaseDirectory, Port: endpoint.UpstreamPort,
		RouteID: endpoint.RouteID, UnitActive: true, UnitMatches: true, RouteObserved: true, RouteMatches: true, RouteUpstream: endpoint.Upstream,
	}
	planned := planner.Operation{ID: "op-06", Kind: planner.VerifyActive, Input: planner.OperationInput{Health: &health, Endpoint: &endpoint, Previous: &previous}}
	envelope := operation.Envelope{Authorization: authority.Proof{Claim: authority.Claim{PlanID: "sha256:" + strings.Repeat("c", 64), OperationID: "op-06", AttemptID: "attempt-11111111111111111111111111111111", FencingToken: 6}}, Operation: planned}
	healthyChecks := []host.HealthCheckObservation{
		{Name: "liveness", Path: "/live", StatusCode: 204, Healthy: true},
		{Name: "readiness", Path: "/ready", StatusCode: 204, Healthy: true},
		{Name: "candidateVerification", Path: "/verify", StatusCode: 200, Healthy: true},
	}
	result := operation.Result{SchemaVersion: operation.ResultSchemaVersion, PlanID: envelope.Authorization.Claim.PlanID, OperationID: "op-06", AttemptID: envelope.Authorization.Claim.AttemptID, FencingToken: 6}

	healthy := host.ActiveVerificationObservation{Status: host.ActiveVerificationHealthy, Candidate: candidate, Previous: &previous, ActiveChecks: healthyChecks, ObservedUpstream: endpoint.Upstream}
	result.Outcome, result.Observation = operation.OutcomeSucceeded, mustJSON(t, healthy)
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("healthy post-switch result rejected: %v", err)
	}

	failedChecks := []host.HealthCheckObservation{{Name: "liveness", Path: "/live", StatusCode: 503, Reason: "unexpected status 503"}}
	rolledBack := host.ActiveVerificationObservation{
		Status: host.ActiveVerificationRolledBack, Candidate: candidate, Previous: &previous,
		ActiveChecks: failedChecks, PreviousChecks: healthyChecks, RollbackChecks: healthyChecks,
		ObservedUpstream: "127.0.0.1:28081", RollbackAttempted: true, RollbackSucceeded: true,
		Restored: &previous, Reason: "stable health failed",
	}
	result.Outcome, result.Observation = operation.OutcomeFailed, mustJSON(t, rolledBack)
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("proved rollback result rejected: %v", err)
	}

	uncertain := host.ActiveVerificationObservation{
		Status: host.ActiveVerificationUncertain, Candidate: candidate, Previous: &previous,
		ActiveChecks: healthyChecks, ObservedUpstream: endpoint.Upstream,
		Reason: "durable state differs from signed Plan", RecoveryAction: "inspect the stable Endpoint",
	}
	result.Outcome, result.Observation = operation.OutcomeUncertain, mustJSON(t, uncertain)
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("uncertain result rejected: %v", err)
	}
	uncertain.RecoveryAction = ""
	result.Observation = mustJSON(t, uncertain)
	if err := verifyHostResult(envelope, result); err == nil {
		t.Fatal("uncertain result without recovery action accepted")
	}
}

func TestVerifyDrainResultBindsBoundPolicyAndExactGenerations(t *testing.T) {
	reference := planner.GenerationReference{
		ID: "provision-example-http-v2-bbbbbbbbbbbb", Revision: "provision-example-http-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), Account: "provision-lab",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v2-bbbbbbbbbbbb",
	}
	endpoint := planner.EndpointInput{
		GenerationReference: reference, Unit: "provision-lab-web-bbbbbbbbbbbb.service",
		RouteID: "provision-lab-web", ListenPort: 18080,
		Upstream: "127.0.0.1:28082", UpstreamPort: 28082, DrainPolicy: "caddy-graceful-config-reload",
	}
	previous := host.GenerationStatus{
		ID: "provision-example-http-v1-aaaaaaaaaaaa", Revision: "provision-example-http-v1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SystemdUnit: "provision-lab-web-aaaaaaaaaaaa.service",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v1-aaaaaaaaaaaa",
		Port:             28081, RouteID: endpoint.RouteID, UnitActive: true, UnitMatches: true,
	}
	drain := planner.DrainInput{Endpoint: endpoint, Previous: previous, Mode: "bounded-http", MaxDuration: "2s"}
	planned := planner.Operation{ID: "op-07", Kind: planner.DrainPrevious, Input: planner.OperationInput{Drain: &drain}}
	drainDigest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	envelope := operation.Envelope{Authorization: authority.Proof{Claim: authority.Claim{PlanID: "sha256:" + strings.Repeat("c", 64), OperationID: "op-07", AttemptID: "attempt-11111111111111111111111111111111", FencingToken: 7}}, Operation: planned}
	switchedAt := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	deadline := time.Date(2026, 9, 25, 12, 0, 2, 0, time.UTC)
	observed := host.DrainObservation{
		Status: host.DrainCompleted, Active: host.GenerationStatus{
			ID: endpoint.ID, Revision: endpoint.Revision, ArtifactDigest: endpoint.ArtifactDigest,
			SystemdUnit: endpoint.Unit, ReleaseDirectory: endpoint.ReleaseDirectory, Port: endpoint.UpstreamPort, RouteID: endpoint.RouteID,
			UnitActive: true, UnitMatches: true, RouteObserved: true, RouteUpstream: endpoint.Upstream, RouteMatches: true,
		},
		Previous: previous, Mode: drain.Mode, HandoffPolicy: endpoint.DrainPolicy, MaxDuration: drain.MaxDuration, OperationDigest: drainDigest,
		SwitchedAt: &switchedAt, StableVerifiedAt: &switchedAt, Deadline: &deadline,
		BoundElapsed: true, StableRouteVerified: true, PreviousUnitRetained: true, PreviousReleaseRetained: true,
	}
	result := operation.Result{
		SchemaVersion: operation.ResultSchemaVersion, PlanID: envelope.Authorization.Claim.PlanID,
		OperationID: "op-07", AttemptID: envelope.Authorization.Claim.AttemptID, FencingToken: 7,
		Outcome: operation.OutcomeSucceeded, Observation: mustJSON(t, observed),
	}
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("valid HTTP drain result rejected: %v", err)
	}
	observed.MaxDuration = "3s"
	result.Observation = mustJSON(t, observed)
	if err := verifyHostResult(envelope, result); err == nil || !strings.Contains(err.Error(), "does not match the Plan") {
		t.Fatalf("tampered HTTP drain bound accepted: %v", err)
	}
	observed.MaxDuration = drain.MaxDuration
	observed.Status = host.DrainUncertain
	observed.Reason = "stable route cannot be proved"
	observed.RecoveryAction = "inspect the stable Endpoint"
	result.Outcome = operation.OutcomeUncertain
	result.Observation = mustJSON(t, observed)
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("valid uncertain HTTP drain result rejected: %v", err)
	}
}

func TestVerifyRetentionResultBindsDeadlinePolicyAndExactGenerations(t *testing.T) {
	reference := planner.GenerationReference{
		ID: "provision-example-http-v2-bbbbbbbbbbbb", Revision: "provision-example-http-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), Account: "provision-lab",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v2-bbbbbbbbbbbb",
	}
	endpoint := planner.EndpointInput{
		GenerationReference: reference, Unit: "provision-lab-web-bbbbbbbbbbbb.service",
		RouteID: "provision-lab-web", ListenPort: 18080,
		Upstream: "127.0.0.1:28082", UpstreamPort: 28082, DrainPolicy: "caddy-graceful-config-reload",
	}
	previous := host.GenerationStatus{
		ID: "provision-example-http-v1-aaaaaaaaaaaa", Revision: "provision-example-http-v1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SystemdUnit: "provision-lab-web-aaaaaaaaaaaa.service",
		ReleaseDirectory: "/var/lib/provision/environments/lab/releases/provision-example-http-v1-aaaaaaaaaaaa",
		Port:             28081, RouteID: endpoint.RouteID, UnitMatches: true,
	}
	input := planner.RetentionInput{Endpoint: endpoint, Previous: previous, Policy: rollbackwindow.RuleRollbackWindow, RollbackWindow: "30m0s"}
	planned := planner.Operation{ID: "op-08", Kind: planner.RetainPrevious, Input: planner.OperationInput{Retention: &input}}
	digest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	envelope := operation.Envelope{Authorization: authority.Proof{Claim: authority.Claim{PlanID: "sha256:" + strings.Repeat("c", 64), OperationID: "op-08", AttemptID: "attempt-11111111111111111111111111111111", FencingToken: 8}}, Operation: planned}
	switchedAt := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	drainedAt := switchedAt.Add(3 * time.Second)
	retainedAt := switchedAt.Add(4 * time.Second)
	deadline := switchedAt.Add(30 * time.Minute)
	observed := host.RetentionObservation{
		Status: host.RetentionCompleted,
		Active: host.GenerationStatus{
			ID: endpoint.ID, Revision: endpoint.Revision, ArtifactDigest: endpoint.ArtifactDigest,
			SystemdUnit: endpoint.Unit, ReleaseDirectory: endpoint.ReleaseDirectory, Port: endpoint.UpstreamPort, RouteID: endpoint.RouteID,
			UnitActive: true, UnitMatches: true, RouteObserved: true, RouteUpstream: endpoint.Upstream, RouteMatches: true,
		},
		Previous: previous, Policy: input.Policy, RollbackWindow: input.RollbackWindow, OperationDigest: digest,
		DrainOperationDigest: "sha256:" + strings.Repeat("d", 64), SwitchedAt: &switchedAt, DrainedAt: &drainedAt, RetainedAt: &retainedAt, RetainUntil: &deadline,
		StableRouteVerified: true, PreviousUnitRetained: true, PreviousGenerationDirectoryRetained: true, PreviousManifestRetained: true, PreviousArtifactRetained: true, Restartable: true,
	}
	result := operation.Result{SchemaVersion: operation.ResultSchemaVersion, PlanID: envelope.Authorization.Claim.PlanID, OperationID: "op-08", AttemptID: envelope.Authorization.Claim.AttemptID, FencingToken: 8, Outcome: operation.OutcomeSucceeded, Observation: mustJSON(t, observed)}
	if err := verifyHostResult(envelope, result); err != nil {
		t.Fatalf("valid retention result rejected: %v", err)
	}
	observed.RollbackWindow = "10m0s"
	result.Observation = mustJSON(t, observed)
	if err := verifyHostResult(envelope, result); err == nil || !strings.Contains(err.Error(), "does not match the Plan") {
		t.Fatalf("tampered rollback window accepted: %v", err)
	}
	observed.RollbackWindow = input.RollbackWindow
	observed.RetainUntil = &retainedAt
	result.Observation = mustJSON(t, observed)
	if err := verifyHostResult(envelope, result); err == nil {
		t.Fatal("non-switch-derived retention deadline accepted")
	}
	observed.RetainUntil = &deadline
	observed.CleanupPerformed = true
	result.Observation = mustJSON(t, observed)
	if err := verifyHostResult(envelope, result); err == nil {
		t.Fatal("retention result claiming cleanup accepted")
	}
}

func TestVerifyWorkerHandoffResultsBindBothGenerationsAndReleasedMessage(t *testing.T) {
	candidate := planner.AsyncWorkerInput{
		GenerationID: "provision-example-async-v2-bbbbbbbbbbbb", Revision: "provision-example-async-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), SystemdUnit: "provision-lab-consumer-bbbbbbbbbbbb.service",
		Drain: config.WorkerDrain{Mode: "bounded-in-flight", MaxDuration: "30s"},
		Previous: &host.WorkerGenerationStatus{
			ID: "provision-example-async-v1-aaaaaaaaaaaa", Revision: "provision-example-async-v1",
			ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SystemdUnit: "provision-lab-consumer-aaaaaaaaaaaa.service",
		},
	}
	handoff := planner.AsyncWorkerHandoffInput{Worker: candidate, QueueGenerationID: "queue-generation-a", RollbackWindow: rollbackwindow.Window("30m0s")}
	planned := planner.Operation{ID: "op-09", Kind: planner.DrainWorkerPrevious, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{WorkerHandoff: &handoff}}}
	digest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	planID := "sha256:" + strings.Repeat("c", 64)
	envelope := operation.Envelope{Authorization: authority.Proof{Claim: authority.Claim{PlanID: planID}}, Operation: planned}
	startedAt := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	completionDeadline := startedAt.Add(20 * time.Second)
	deadline := startedAt.Add(30 * time.Second)
	releaseStartedAt := completionDeadline
	completedAt := completionDeadline.Add(time.Second)
	observed := host.AsyncWorkerHandoffObservation{
		Status: "previous-released", PlanID: planID, OperationDigest: digest, QueueGenerationID: handoff.QueueGenerationID,
		InFlightMessageID: "msg-stable", ReleasedMessageID: "msg-stable", BoundElapsed: true, DrainStartedAt: &startedAt, CompletionDeadline: &completionDeadline, DrainDeadline: &deadline, ReleaseStartedAt: &releaseStartedAt, DrainCompletedAt: &completedAt,
		Candidate: host.WorkerGenerationStatus{ID: candidate.GenerationID, ArtifactDigest: candidate.ArtifactDigest, Gate: "closed", UnitActive: true},
		Previous:  host.WorkerGenerationStatus{ID: candidate.Previous.ID, ArtifactDigest: candidate.Previous.ArtifactDigest, Gate: "closed"},
	}
	result := operation.Result{Outcome: operation.OutcomeSucceeded, Observation: mustJSON(t, observed)}
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err != nil {
		t.Fatalf("valid bounded-release result rejected: %v", err)
	}
	lateCompletion := deadline.Add(time.Nanosecond)
	observed.DrainCompletedAt = &lateCompletion
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err == nil {
		t.Fatal("Worker settlement completed after the declared deadline accepted")
	}
	observed.DrainCompletedAt = &completedAt
	observed.ReleasedMessageID = ""
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err == nil {
		t.Fatal("released Worker delivery without a stable message identity accepted")
	}
	observed.ReleasedMessageID = "msg-stable"
	observed.BoundElapsed = false
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err == nil {
		t.Fatal("released Worker delivery without elapsed bound accepted")
	}
	observed.BoundElapsed = true
	observed.ReleasedMessageID = "msg-different"
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err == nil {
		t.Fatal("released Worker delivery changed its stable message identity")
	}
	observed.ReleasedMessageID = "msg-stable"
	observed.QueueGenerationID = "queue-generation-b"
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err == nil || !strings.Contains(err.Error(), "does not match the Plan") {
		t.Fatalf("handoff result for a different Queue generation accepted: %v", err)
	}
}

func TestVerifyWorkerActiveRequiresVerifiedDurableActiveRecord(t *testing.T) {
	worker := planner.AsyncWorkerInput{
		GenerationID: "provision-example-async-v2-bbbbbbbbbbbb", Revision: "provision-example-async-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), SystemdUnit: "provision-lab-consumer-bbbbbbbbbbbb.service",
	}
	planned := planner.Operation{ID: "op-11", Kind: planner.VerifyWorkerActive, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Worker: &worker}}}
	envelope := operation.Envelope{Operation: planned}
	observed := host.AsyncWorkerOperationObservation{
		Status: "active-open",
		Worker: host.WorkerGenerationStatus{
			ID: worker.GenerationID, Revision: worker.Revision, ArtifactDigest: worker.ArtifactDigest, SystemdUnit: worker.SystemdUnit,
			Gate: "open", UnitActive: true, QueueConnected: true,
		},
	}
	result := operation.Result{Outcome: operation.OutcomeSucceeded, Observation: mustJSON(t, observed)}
	if err := verifyAsyncWorkerResult(envelope, result); err == nil {
		t.Fatal("active Worker verification accepted without verified durable active identity")
	}
	observed.Verified = true
	observed.Worker.Active = true
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerResult(envelope, result); err != nil {
		t.Fatalf("verified durable active Worker rejected: %v", err)
	}
}

func TestVerifyWorkerActiveHandoffDistinguishesHealthyRollbackAndUncertain(t *testing.T) {
	candidate := planner.AsyncWorkerInput{
		GenerationID: "provision-example-async-v2-bbbbbbbbbbbb", Revision: "provision-example-async-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), SystemdUnit: "provision-lab-consumer-bbbbbbbbbbbb.service",
		Previous: &host.WorkerGenerationStatus{
			ID: "provision-example-async-v1-aaaaaaaaaaaa", Revision: "provision-example-async-v1",
			ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SystemdUnit: "provision-lab-consumer-aaaaaaaaaaaa.service",
		},
	}
	handoff := planner.AsyncWorkerHandoffInput{Worker: candidate, QueueGenerationID: "queue-generation-a", DrainOperationDigest: "sha256:" + strings.Repeat("d", 64)}
	planned := planner.Operation{ID: "op-11", Kind: planner.VerifyWorkerActive, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{WorkerHandoff: &handoff}}}
	planID := "sha256:" + strings.Repeat("c", 64)
	digest, _ := planner.OperationDigest(planned)
	envelope := operation.Envelope{Authorization: authority.Proof{Claim: authority.Claim{PlanID: planID}}, Operation: planned}
	observed := host.AsyncWorkerActiveVerificationObservation{
		Status: host.WorkerActiveVerificationHealthy, PlanID: planID, OperationDigest: digest, QueueGenerationID: handoff.QueueGenerationID,
		MessageID: "msg-stable", PublisherConfirmed: true, CandidateProcessed: true, CandidateAcknowledged: true,
		Candidate: host.WorkerGenerationStatus{ID: candidate.GenerationID, Revision: candidate.Revision, ArtifactDigest: candidate.ArtifactDigest, Gate: "open", Active: true, UnitActive: true, QueueConnected: true},
		Previous:  *candidate.Previous,
	}
	result := operation.Result{Outcome: operation.OutcomeSucceeded, Observation: mustJSON(t, observed)}
	if err := verifyAsyncWorkerActiveVerificationResult(envelope, result); err != nil {
		t.Fatalf("healthy message-level Worker verification rejected: %v", err)
	}
	observed.CandidateAcknowledged = false
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerActiveVerificationResult(envelope, result); err == nil {
		t.Fatal("healthy Worker verification without candidate acknowledgement accepted")
	}

	observed.Status = host.WorkerActiveVerificationRolledBack
	observed.Candidate.Gate, observed.Candidate.Active, observed.Candidate.UnitActive = "closed", false, false
	observed.CandidateAcknowledged = false
	observed.PreviousProcessed, observed.PreviousAcknowledged = true, true
	observed.RollbackAttempted, observed.RollbackSucceeded = true, true
	restored := *candidate.Previous
	restored.Gate, restored.Active, restored.UnitActive, restored.QueueConnected = "open", true, true, true
	observed.Restored = &restored
	observed.Reason = "candidate stopped before acknowledging the verification message"
	result.Outcome, result.Observation = operation.OutcomeFailed, mustJSON(t, observed)
	if err := verifyAsyncWorkerActiveVerificationResult(envelope, result); err != nil {
		t.Fatalf("proved Worker rollback rejected: %v", err)
	}

	observed.Status = host.WorkerActiveVerificationUncertain
	observed.RollbackSucceeded, observed.Restored = false, nil
	observed.RecoveryAction = "inspect exact Worker and Queue evidence"
	result.Outcome, result.Observation = operation.OutcomeUncertain, mustJSON(t, observed)
	if err := verifyAsyncWorkerActiveVerificationResult(envelope, result); err != nil {
		t.Fatalf("explicit uncertain Worker recovery rejected: %v", err)
	}
}

func TestVerifyWorkerRetentionBindsRollbackWindowAndDrainOperation(t *testing.T) {
	candidate := planner.AsyncWorkerInput{
		GenerationID: "provision-example-async-v2-bbbbbbbbbbbb", Revision: "provision-example-async-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), SystemdUnit: "provision-lab-consumer-bbbbbbbbbbbb.service",
		Drain:    config.WorkerDrain{Mode: "bounded-in-flight", MaxDuration: "30s"},
		Previous: &host.WorkerGenerationStatus{ID: "provision-example-async-v1-aaaaaaaaaaaa", ArtifactDigest: "sha256:" + strings.Repeat("a", 64)},
	}
	planID := "sha256:" + strings.Repeat("c", 64)
	drainDigest := "sha256:" + strings.Repeat("d", 64)
	handoff := planner.AsyncWorkerHandoffInput{Worker: candidate, QueueGenerationID: "queue-generation-a", RollbackWindow: "30m0s", DrainOperationDigest: drainDigest}
	planned := planner.Operation{ID: "op-15", Kind: planner.RetainWorkerPrevious, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{WorkerHandoff: &handoff}}}
	digest, _ := planner.OperationDigest(planned)
	envelope := operation.Envelope{Authorization: authority.Proof{Claim: authority.Claim{PlanID: planID}}, Operation: planned}
	retainedAt := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	retainUntil := retainedAt.Add(30 * time.Minute)
	observed := host.AsyncWorkerHandoffObservation{
		Status: "previous-retained", PlanID: planID, OperationDigest: digest, DrainOperationDigest: drainDigest,
		QueueGenerationID: handoff.QueueGenerationID, RollbackWindow: handoff.RollbackWindow,
		Candidate:  host.WorkerGenerationStatus{ID: candidate.GenerationID, ArtifactDigest: candidate.ArtifactDigest},
		Previous:   host.WorkerGenerationStatus{ID: candidate.Previous.ID, ArtifactDigest: candidate.Previous.ArtifactDigest, Restartable: true},
		RetainedAt: &retainedAt, RetainUntil: &retainUntil,
	}
	result := operation.Result{Outcome: operation.OutcomeSucceeded, Observation: mustJSON(t, observed)}
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err != nil {
		t.Fatalf("exact Worker retention rejected: %v", err)
	}
	observed.DrainOperationDigest = "sha256:" + strings.Repeat("e", 64)
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err == nil {
		t.Fatal("Worker retention for another drain operation accepted")
	}
	observed.DrainOperationDigest = drainDigest
	short := retainedAt.Add(10 * time.Minute)
	observed.RetainUntil = &short
	result.Observation = mustJSON(t, observed)
	if err := verifyAsyncWorkerHandoffResult(envelope, result); err == nil {
		t.Fatal("Worker retention with a shortened rollback window accepted")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
