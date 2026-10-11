package planner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"provision/internal/config"
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

func TestPlanIdentityRejectsUnsupportedSchemaVersion(t *testing.T) {
	plan := Plan{SchemaVersion: "provision.dev/plan/v1alpha1", Application: "app", Environment: "lab", Revision: "revision-a"}
	id, err := digest(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.ID = id
	if err := plan.VerifyIdentity(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("old Plan schema was accepted: %v", err)
	}
}

func keyValueFixture(t *testing.T, version string) (config.Compiled, host.BootstrapStatus) {
	t.Helper()
	compiled, err := config.Load(filepath.Join("..", "..", "examples", "host-key-value", "root.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if version != "" {
		implementation := compiled.Environment.Implementations["sessions"]
		implementation.Version = version
		compiled.Environment.Implementations["sessions"] = implementation
	}
	return compiled, host.BootstrapStatus{Ready: true, Environment: "lab", Operator: "marlinf"}
}

func TestKeyValuePreviewIsDeterministicAndHoldsNoSecret(t *testing.T) {
	compiled, observation := keyValueFixture(t, "")
	first, err := Build(compiled, observation)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := Build(compiled, observation)
	if first.Plan == nil || first.Plan.ID != second.Plan.ID {
		t.Fatalf("preview is missing or not deterministic: %+v", first)
	}
	kv := first.Plan.KeyValue
	if kv.DataRole != "authoritative" || kv.AppendOnly != "yes" || kv.AppendFsync != "everysec" || kv.CredentialReference != "secret://lab/valkey-url" ||
		kv.ValkeyVersion != "9.1.2" || kv.Port != 26379 || !strings.HasSuffix(kv.DataPath, "/generations/"+kv.GenerationID+"/data") || kv.TransitionCleanupPolicy != "retain-previous-generation" {
		t.Fatalf("preview missing required facts: %+v", kv)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "redis://") {
		t.Fatal("preview contains a resolved credential")
	}
}

func TestKeyValueUnregisteredVersionFailsGuarantees(t *testing.T) {
	compiled, observation := keyValueFixture(t, "8.0.0")
	preview, err := Build(compiled, observation)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Executable || preview.Plan != nil || len(preview.Reasons) != 1 || !strings.Contains(preview.Reasons[0], "not a registered image") {
		t.Fatalf("unregistered version was not rejected: %+v", preview)
	}
}
