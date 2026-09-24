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

	observed.Previous.ID = "different-generation"
	result.Observation, _ = json.Marshal(observed)
	if err := verifyHostResult(envelope, result); err == nil || !strings.Contains(err.Error(), "previous Generation") {
		t.Fatalf("tampered previous Generation accepted: %v", err)
	}
}
