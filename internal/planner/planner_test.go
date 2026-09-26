package planner

import (
	"strings"
	"testing"

	"provision/internal/host"
)

func TestApprovalFingerprintPreservesExactEvidenceWithoutStalingOnExcludedTelemetry(t *testing.T) {
	decision := host.BootstrapStatus{SchemaVersion: "provision.dev/host-inspection/v1alpha1", Environment: "lab", ExecutorDigest: "sha256:" + strings.Repeat("a", 64)}
	firstObserved := decision
	firstObserved.Async = &host.AsyncStatus{Deployment: host.AsyncDeploymentStatus{Messages: []host.QueueMessageStatus{{ID: "msg-1"}}}}
	secondObserved := decision
	secondObserved.Async = &host.AsyncStatus{Deployment: host.AsyncDeploymentStatus{Messages: []host.QueueMessageStatus{{ID: "msg-2"}}}}

	first := Plan{SchemaVersion: SchemaVersion, Application: "app", Environment: "lab", Revision: "revision-a", Capability: CapabilityEvidence{Observed: firstObserved, DecisionObserved: &decision}}
	second := first
	second.Capability.Observed = secondObserved
	firstFingerprint, err := ApprovalFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := ApprovalFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("excluded telemetry changed approval fingerprint: %s != %s", firstFingerprint, secondFingerprint)
	}

	changedDecision := decision
	changedDecision.ExecutorDigest = "sha256:" + strings.Repeat("b", 64)
	second.Capability.DecisionObserved = &changedDecision
	changedFingerprint, err := ApprovalFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if changedFingerprint == firstFingerprint {
		t.Fatal("decision-relevant capability change did not stale approval fingerprint")
	}
}
