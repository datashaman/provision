package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

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
	if err := verifyHostResult(envelope, result); err == nil || !strings.Contains(err.Error(), "cannot return an uncertain") {
		t.Fatalf("uncertain Endpoint switch result accepted: %v", err)
	}

	result.Outcome = operation.OutcomeSucceeded
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

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
