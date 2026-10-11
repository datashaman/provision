package planner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/implementation/hostkeyvalue"
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
	return compiled, readyKeyValueHost()
}

func readyKeyValueHost() host.BootstrapStatus {
	return host.BootstrapStatus{
		Ready: true, Environment: "lab", Operator: "marlinf", OS: "ubuntu", OSVersion: "26.04", Architecture: "x86_64",
		AllowedOperations: host.AllowedOperations(),
		KeyValue: &host.KeyValueStatus{
			SchemaVersion: host.KeyValueInspectionSchema, ObservationComplete: true, Findings: []string{},
			Capabilities: host.KeyValueCapabilities{
				Quadlet: true, RootlessEnvironmentAccount: true, SystemdCredentials: true, SubordinateIDs: true, LingeringUserManager: true,
				QuadletPath: "/etc/containers/systemd/users/990/provision-lab-valkey.container", ListenAddress: "127.0.0.1", Port: 26379, AppendOnly: "yes", AppendFsync: "everysec",
			},
		},
	}
}

func TestKeyValuePreviewIsDeterministicAndHoldsNoSecret(t *testing.T) {
	compiled, observation := keyValueFixture(t, "")
	first, err := Build(compiled, observation)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := Build(compiled, observation)
	if first.Plan == nil || !first.Executable || first.Plan.ID != second.Plan.ID || len(first.Plan.Operations) != 1 || first.Plan.Operations[0].Kind != PrepareKeyValue {
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

func TestKeyValueWithoutCapabilityBlockIsNotExecutable(t *testing.T) {
	compiled, observation := keyValueFixture(t, "")
	observation.KeyValue = nil
	preview, err := Build(compiled, observation)
	if err != nil || preview.Executable || preview.Plan != nil || len(preview.Reasons) != 1 || !strings.Contains(preview.Reasons[0], "capability evidence") {
		t.Fatalf("missing capability block was not rejected: %+v %v", preview, err)
	}
	compiled, observation = keyValueFixture(t, "")
	observation.AllowedOperations = []string{"inspect"}
	preview, _ = Build(compiled, observation)
	if preview.Executable || preview.Plan != nil {
		t.Fatalf("executor without prepareKeyValue was accepted: %+v", preview)
	}
}

func TestKeyValuePlanBindsDeterministicKeysByLogicalReference(t *testing.T) {
	compiled, observation := keyValueFixture(t, "")
	preview, _ := Build(compiled, observation)
	binding := preview.Plan.KeyValue.Binding
	if binding.Component != "sessions" || binding.LogicalID != "provision-lab-sessions" || len(binding.Keys) != 2 || !strings.HasPrefix(binding.Keys[0], "provision-lab-sessions/provision-example-key-value-v1/") || preview.Plan.KeyValue.QuadletPath == "" {
		t.Fatalf("binding is not by logical component reference: %+v", binding)
	}
}

func keyValueHTTPFixture(t *testing.T) (config.Compiled, host.BootstrapStatus) {
	t.Helper()
	compiled, err := config.Load(filepath.Join("..", "..", "examples", "host-key-value-http", "root.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	observation := readyKeyValueHost()
	observation.SchemaVersion = "provision.dev/host-inspection/v1alpha1"
	observation.CaddyVersion, observation.CaddyActive, observation.CaddyConfigValid = "2.6.2", true, true
	observation.CaddyAdminReachable, observation.CaddyConfigDurable = true, true
	observation.GenerationStorageReady, observation.JournaldActive = true, true
	observation.ExecutorDigest, observation.AuthorityKeyID, observation.SSHHostKeyFingerprint = "sha256:e", "key", "SHA256:host"
	observation.SystemdVersion, observation.CgroupV2 = "systemd 259", true
	return compiled, observation
}

func TestKeyValueBoundHTTPPlanBindsWorkloadByLogicalReference(t *testing.T) {
	compiled, observation := keyValueHTTPFixture(t)
	preview, err := Build(compiled, observation)
	if err != nil || !preview.Executable || preview.Plan == nil {
		t.Fatalf("Key-Value-bound HTTP preview = %+v, %v", preview, err)
	}
	plan := preview.Plan
	if plan.Operations[0].Kind != PrepareKeyValue || plan.Operations[1].Kind != StageArtifact || len(plan.Operations[1].DependsOn) != 1 || plan.Operations[1].DependsOn[0] != "op-01" {
		t.Fatalf("operation order is wrong: %+v", plan.Operations)
	}
	var bound int
	for _, operation := range plan.Operations {
		in := operation.Input
		for _, bindings := range [][]KeyValueHTTPBinding{
			func() []KeyValueHTTPBinding {
				if in.Systemd != nil {
					return in.Systemd.KeyValueBindings
				}
				return nil
			}(),
			func() []KeyValueHTTPBinding {
				if in.Health != nil {
					return in.Health.KeyValueBindings
				}
				return nil
			}(),
			func() []KeyValueHTTPBinding {
				if in.Endpoint != nil {
					return in.Endpoint.KeyValueBindings
				}
				return nil
			}(),
		} {
			if len(bindings) == 1 {
				bound++
				b := bindings[0]
				if b.Component != "sessions" || b.LogicalID != "provision-lab-sessions" || b.Reference != "secret://lab/valkey-url" || b.EnvironmentVariable != "PROVISION_KEYVALUE_URL_FILE" || len(b.Keys) == 0 {
					t.Fatalf("binding is not by logical reference: %+v", b)
				}
			}
		}
	}
	if bound < 3 {
		t.Fatalf("binding reached only %d of the systemd, health, and endpoint inputs", bound)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "valkey://") {
		t.Fatal("Plan contains a credential-bearing URL")
	}
	second, _ := Build(compiled, observation)
	if second.Plan.ID != plan.ID {
		t.Fatal("Key-Value-bound HTTP Plan is not deterministic")
	}
}

func TestKeyValueBoundHTTPReplanOfExactActiveWorkloadIsIdempotent(t *testing.T) {
	compiled, observation := keyValueHTTPFixture(t)
	first, _ := Build(compiled, observation)
	var stage, health *Operation
	for i := range first.Plan.Operations {
		switch first.Plan.Operations[i].Kind {
		case StageArtifact:
			stage = &first.Plan.Operations[i]
		case SwitchEndpoint:
			health = &first.Plan.Operations[i]
		}
	}
	endpoint := health.Input.Endpoint
	binding := endpoint.KeyValueBindings[0]
	observation.Deployment.Active = &host.GenerationStatus{
		ID: endpoint.ID, Revision: endpoint.Revision, ArtifactDigest: stage.Input.Artifact.Digest, SystemdUnit: endpoint.Unit, RouteID: endpoint.RouteID,
		UnitActive: true, UnitMatches: true, RouteObserved: true, RouteMatches: true,
		KeyValueBinding: &host.HTTPKeyValueBindingStatus{Component: binding.Component, LogicalID: binding.LogicalID, GenerationID: binding.GenerationID, EnvironmentVariable: binding.EnvironmentVariable, BindingState: "key-value-bound"},
	}
	replan, err := Build(compiled, observation)
	if err != nil || !replan.Executable || len(replan.Plan.Operations) != 1 || replan.Plan.Operations[0].Kind != PrepareKeyValue {
		t.Fatalf("exact active workload was not re-planned idempotently: %+v %v", replan, err)
	}
	observation.Deployment.Active.KeyValueBinding.BindingState = "incomplete"
	replan, _ = Build(compiled, observation)
	if replan.Plan != nil && len(replan.Plan.Operations) == 1 {
		t.Fatal("an unbound active workload was treated as exact")
	}
}

func TestKeyValueActiveGenerationDifferingFromPlanFailsClosed(t *testing.T) {
	compiled, observation := keyValueFixture(t, "")
	observation.KeyValue.Active = &host.KeyValueGenerationStatus{ID: "valkey-9-1-1-cc03bc07761a"}
	preview, _ := Build(compiled, observation)
	if preview.Executable || preview.Plan != nil || len(preview.Reasons) != 1 || !strings.Contains(preview.Reasons[0], "Store Transition") {
		t.Fatalf("differing active generation was not rejected: %+v", preview)
	}
}

func TestKeyValueUnsafeCandidateGenerationIsRejectedWithDistinctEvidence(t *testing.T) {
	image, _ := hostkeyvalue.ImageFor("9.1.2")
	active, _ := hostkeyvalue.ImageFor("9.1.1")
	safe := func() *host.KeyValueGenerationStatus {
		return &host.KeyValueGenerationStatus{ID: "candidate", ImageReference: image.Reference(), Role: "authoritative", AppendOnly: "yes", AppendFsync: "everysec", Compatibility: "compatible", ReplicationLinkUp: true}
	}
	cases := map[string]struct {
		mutate func(*host.KeyValueGenerationStatus)
		want   string
	}{
		"unregistered image": {func(c *host.KeyValueGenerationStatus) { c.ImageReference = "docker.io/valkey/valkey:9" }, "unregistered image"},
		"no AOF":             {func(c *host.KeyValueGenerationStatus) { c.AppendOnly = "no" }, "not AOF everysec"},
		"fsync always":       {func(c *host.KeyValueGenerationStatus) { c.AppendFsync = "always" }, "not AOF everysec"},
		"incompatible":       {func(c *host.KeyValueGenerationStatus) { c.Compatibility = "incompatible" }, "failed compatibility"},
		"unknown evidence":   {func(c *host.KeyValueGenerationStatus) { c.Compatibility = "" }, "unknown compatibility evidence"},
		"wrong role":         {func(c *host.KeyValueGenerationStatus) { c.Role = "derived" }, "not authoritative"},
		"no replication":     {func(c *host.KeyValueGenerationStatus) { c.ReplicationLinkUp = false }, "replication from the active generation is unreachable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			compiled, observation := keyValueFixture(t, "")
			observation.KeyValue.Active = &host.KeyValueGenerationStatus{ID: "valkey-9-1-1-cc03bc07761a", ImageReference: active.Reference()}
			observation.KeyValue.Candidate = safe()
			tc.mutate(observation.KeyValue.Candidate)
			preview, err := Build(compiled, observation)
			if err != nil || preview.Executable || preview.Plan != nil {
				t.Fatalf("unsafe candidate was not rejected: %+v %v", preview, err)
			}
			found := false
			for _, reason := range preview.Reasons {
				found = found || strings.Contains(reason, tc.want)
			}
			if !found {
				t.Fatalf("missing distinct evidence %q in %v", tc.want, preview.Reasons)
			}
		})
	}
	compiled, observation := keyValueFixture(t, "")
	observation.KeyValue.Candidate = safe()
	if preview, _ := Build(compiled, observation); len(preview.Reasons) != 0 && strings.Contains(strings.Join(preview.Reasons, ";"), "candidate") {
		t.Fatalf("safe candidate was rejected: %v", preview.Reasons)
	}
}
