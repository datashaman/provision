package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/implementation/hostkeyvalue"
	"provision/internal/planner"
)

func keyValueTransitionFixture(t *testing.T) (planner.Operation, bootstrapRecord, executionPaths) {
	t.Helper()
	planned, record, paths := keyValueOperationFixture(t)
	input := *planned.Input.KeyValue
	image, _ := hostkeyvalue.ImageFor("9.1.1")
	input.Previous = &host.KeyValueGenerationStatus{
		ID: image.Generation(), LogicalID: input.LogicalID, Role: "authoritative", Ready: true, Connectivity: true, Health: "healthy", ImageReference: image.Reference(), ImageManifest: image.Manifest,
		ServiceUnit: input.ServiceUnit, Container: input.Container, Account: input.Account, QuadletPath: input.QuadletPath, ListenAddress: input.ListenAddress, Port: input.Port,
		DataPath: paths.environmentHome + "/services/valkey/generations/" + image.Generation() + "/data",
	}
	input.Transition = planner.KeyValueCandidateTransition(input.ServiceUnit, input.Container, input.QuadletPath)
	planned.Kind = planner.PrepareKeyValueCandidate
	planned.Input.KeyValue = &input
	return planned, record, paths
}

func TestKeyValueTransitionValidationPinsCandidateAndPreviousIdentity(t *testing.T) {
	planned, record, paths := keyValueTransitionFixture(t)
	for _, kind := range []planner.OperationKind{planner.PrepareKeyValueCandidate, planner.SynchronizeKeyValueCandidate, planner.FenceKeyValueWrites, planner.SwitchKeyValueAuthority, planner.VerifyKeyValueActive, planner.RetainKeyValuePrevious} {
		op := planned
		op.Kind = kind
		if kind != planner.PrepareKeyValueCandidate {
			op.DependsOn = []string{"op-01"}
		}
		if err := validateKeyValueOperation(op, record, paths); err != nil {
			t.Fatalf("%s rejected: %v", kind, err)
		}
	}
	for name, mutate := range map[string]func(*planner.KeyValuePlan){
		"caller-selected candidate port":      func(i *planner.KeyValuePlan) { i.Transition.CandidatePort = 6379 },
		"caller-selected candidate container": func(i *planner.KeyValuePlan) { i.Transition.CandidateContainer = "other" },
		"derived guarantee":                   func(i *planner.KeyValuePlan) { i.Transition.StoreRollbackGuarantee = "zero-loss" },
		"missing previous":                    func(i *planner.KeyValuePlan) { i.Previous = nil },
		"unregistered previous image":         func(i *planner.KeyValuePlan) { i.Previous.ImageReference = "docker.io/valkey/valkey:8" },
		"previous is the candidate":           func(i *planner.KeyValuePlan) { i.Previous.ID = i.GenerationID },
		"previous data outside environment":   func(i *planner.KeyValuePlan) { i.Previous.DataPath = "/var/lib/other/data" },
		"previous different service":          func(i *planner.KeyValuePlan) { i.Previous.ServiceUnit = "other.service" },
	} {
		t.Run(name, func(t *testing.T) {
			input := *planned.Input.KeyValue
			previous, transition := *input.Previous, *input.Transition
			input.Previous, input.Transition = &previous, &transition
			mutate(&input)
			tampered := planned
			tampered.Input.KeyValue = &input
			if err := validateKeyValueOperation(tampered, record, paths); err == nil {
				t.Fatal("tampered Store Transition accepted")
			}
		})
	}
	first := planned
	first.Kind = planner.PrepareKeyValue
	if err := validateKeyValueOperation(first, record, paths); err == nil {
		t.Fatal("prepareKeyValue accepted Store Transition input")
	}
	dependent := planned
	dependent.Kind, dependent.DependsOn = planner.SwitchKeyValueAuthority, nil
	if err := validateKeyValueOperation(dependent, record, paths); err == nil {
		t.Fatal("transition step without its dependency accepted")
	}
}

func TestCandidateQuadletIsSeparateAndReplicaAuthenticated(t *testing.T) {
	planned, _, _ := keyValueTransitionFixture(t)
	input := *planned.Input.KeyValue
	candidate := input.Candidate()
	quadlet := renderKeyValueQuadlet(candidate, keyValueEntrypointPath(candidate))
	for _, want := range []string{"ContainerName=" + input.Container + "-candidate", "PublishPort=127.0.0.1:26380:6379", "credential-entrypoint-candidate", "--appendonly yes --appendfsync everysec"} {
		if !strings.Contains(quadlet, want) {
			t.Fatalf("candidate Quadlet lacks %q:\n%s", want, quadlet)
		}
	}
	if keyValueRecordPath(candidate) == keyValueRecordPath(input) || keyValueEntrypointPath(candidate) == keyValueEntrypointPath(input) || candidate.DataPath != input.DataPath {
		t.Fatal("candidate must have its own record and entrypoint but the target generation's data path")
	}
	if !strings.Contains(keyValueCredentialEntrypoint(true), `--masteruser "$user" --masterauth "$password"`) || strings.Contains(keyValueCredentialEntrypoint(false), "masterauth") {
		t.Fatal("only the candidate entrypoint authenticates replication")
	}
	if canonicalKeyValue(input).Previous != nil || canonicalKeyValue(input).Transition != nil {
		t.Fatal("generation records must not carry transition context")
	}
}

type fakeValkey struct {
	fenced           bool
	probeAcks        bool
	activeOffset     int64
	candidateOffset  int64
	linkUp           bool
	calls            []string
	activeContainer  string
	candidateContain string
}

func (f *fakeValkey) run(_ context.Context, _ bootstrapRecord, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "podman" || len(args) < 4 || args[0] != "exec" {
		return nil, nil
	}
	container, cmd := args[1], strings.Join(args[3:], " ")
	switch {
	case strings.HasPrefix(cmd, "ACL SETUSER"):
		f.fenced = true
		return []byte("OK\n"), nil
	case strings.HasPrefix(cmd, "CLIENT KILL USER"):
		if strings.HasSuffix(cmd, "TYPE normal") {
			return []byte("2\n"), nil
		}
		return []byte("0\n"), nil
	case strings.HasPrefix(cmd, "SET "):
		if f.fenced && !f.probeAcks {
			return []byte("NOPERM User app_user has no permissions to run the 'set' command\n"), nil
		}
		return []byte("OK\n"), nil
	case strings.HasPrefix(cmd, "EXISTS "):
		return []byte("0\n"), nil
	case cmd == "INFO replication" && container == f.activeContainer:
		return []byte(fmt.Sprintf("role:master\nmaster_repl_offset:%d\n", f.activeOffset)), nil
	case cmd == "INFO replication" && container == f.candidateContain:
		status := "down"
		if f.linkUp {
			status = "up"
		}
		return []byte(fmt.Sprintf("role:slave\nmaster_link_status:%s\nmaster_sync_in_progress:0\nslave_repl_offset:%d\n", status, f.candidateOffset)), nil
	}
	return nil, fmt.Errorf("unexpected command %q", cmd)
}

func useFakeValkey(t *testing.T, f *fakeValkey) {
	t.Helper()
	old := keyValueRun
	keyValueRun = f.run
	t.Cleanup(func() { keyValueRun = old })
}

func TestFenceRevokesWritesKillsClientsAndProvesOffsetEquality(t *testing.T) {
	planned, record, _ := keyValueTransitionFixture(t)
	input := *planned.Input.KeyValue
	f := &fakeValkey{activeOffset: 4096, candidateOffset: 4096, linkUp: true, activeContainer: input.Previous.Container, candidateContain: input.Candidate().Container}
	useFakeValkey(t, f)
	evidence, offsets, err := fenceActiveWrites(context.Background(), record, input, "app_user")
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.WriteRevoked || !evidence.ProbeRejected || !evidence.ProbeAbsent || evidence.ClientsKilled != 2 || offsets.ActiveOffset != 4096 || offsets.CandidateOffset != 4096 || !offsets.LinkUp {
		t.Fatalf("incomplete fence evidence: %+v %+v", evidence, offsets)
	}
	joined := strings.Join(f.calls, "\n")
	if strings.Contains(strings.ToUpper(joined), "CLIENT PAUSE") || !strings.Contains(joined, "ACL SETUSER app_user -@write") || !strings.Contains(joined, "CLIENT KILL USER app_user TYPE normal") {
		t.Fatalf("fence did not use ACL revocation plus client kill:\n%s", joined)
	}
	if strings.Index(joined, "ACL SETUSER") > strings.Index(joined, "CLIENT KILL") || strings.Index(joined, "CLIENT KILL") > strings.Index(joined, "SET ") {
		t.Fatalf("fence order is wrong:\n%s", joined)
	}
}

func TestFenceWithAcknowledgedProbeWriteIsNotAccepted(t *testing.T) {
	planned, record, _ := keyValueTransitionFixture(t)
	input := *planned.Input.KeyValue
	f := &fakeValkey{probeAcks: true, activeOffset: 10, candidateOffset: 10, linkUp: true, activeContainer: input.Previous.Container, candidateContain: input.Candidate().Container}
	useFakeValkey(t, f)
	if _, _, err := fenceActiveWrites(context.Background(), record, input, "app_user"); err == nil {
		t.Fatal("fence accepted although a write was acknowledged")
	}
	if _, err := provenLosslessState(context.Background(), record, input); err == nil {
		t.Fatal("authority switch gate accepted an unfenced active generation")
	}
}

func TestLosslessGateRequiresLinkUpAndEqualOffsets(t *testing.T) {
	planned, record, _ := keyValueTransitionFixture(t)
	input := *planned.Input.KeyValue
	base := func() *fakeValkey {
		return &fakeValkey{fenced: true, activeOffset: 500, candidateOffset: 500, linkUp: true, activeContainer: input.Previous.Container, candidateContain: input.Candidate().Container}
	}
	f := base()
	useFakeValkey(t, f)
	offsets, err := provenLosslessState(context.Background(), record, input)
	if err != nil || offsets.ActiveOffset != 500 || offsets.CandidateOffset != 500 {
		t.Fatalf("equal offsets over an up link rejected: %+v %v", offsets, err)
	}
	for name, mutate := range map[string]func(*fakeValkey){
		"candidate behind": func(f *fakeValkey) { f.candidateOffset = 499 },
		"link down":        func(f *fakeValkey) { f.linkUp = false },
		"zero offsets":     func(f *fakeValkey) { f.activeOffset, f.candidateOffset = 0, 0 },
	} {
		t.Run(name, func(t *testing.T) {
			f := base()
			mutate(f)
			useFakeValkey(t, f)
			old := keyValueEqualBudget
			keyValueEqualBudget = 300 * time.Millisecond
			defer func() { keyValueEqualBudget = old }()
			started := time.Now()
			if _, err := provenLosslessState(context.Background(), record, input); err == nil {
				t.Fatal("authority gate passed without lossless evidence")
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("lossless gate is not bounded")
			}
		})
	}
}

func TestAuthoritySwitchMutatesNothingWithoutThePlansFenceRecord(t *testing.T) {
	planned, record, paths := keyValueTransitionFixture(t)
	input := *planned.Input.KeyValue
	f := &fakeValkey{fenced: true, activeOffset: 1, candidateOffset: 1, linkUp: true, activeContainer: input.Previous.Container, candidateContain: input.Candidate().Container}
	useFakeValkey(t, f)
	claim := authority.Claim{PlanID: "sha256:plan", OperationID: "op-04", OperationDigest: "sha256:op"}
	if _, err := applyKeyValueAuthoritySwitch(context.Background(), input, claim, record, paths); err == nil {
		t.Fatal("authority switched without a recorded write fence")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "stop") || strings.Contains(call, "REPLICAOF") {
			t.Fatalf("authority switch mutated before the fence record was proven: %s", call)
		}
	}
}

func TestTransitionMarkerBindsPlanAndGenerations(t *testing.T) {
	planned, _, _ := keyValueTransitionFixture(t)
	input := *planned.Input.KeyValue
	marker := keyValueTransitionMarker{Phase: keyValueFencePhase, PlanID: "sha256:plan", OperationDigest: "sha256:op", LogicalID: input.LogicalID, PreviousGeneration: input.Previous.ID, CandidateGeneration: input.GenerationID}
	if err := writeKeyValueMarker(input, "fence.json", marker); err != nil {
		t.Fatal(err)
	}
	if _, ok := readKeyValueMarker(input, "fence.json", "sha256:plan", keyValueFencePhase, "sha256:op"); !ok {
		t.Fatal("matching marker rejected")
	}
	for name, args := range map[string][3]string{"other plan": {"sha256:other", keyValueFencePhase, "sha256:op"}, "other phase": {"sha256:plan", keyValueSwitchPhase, "sha256:op"}, "other op": {"sha256:plan", keyValueFencePhase, "sha256:x"}} {
		if _, ok := readKeyValueMarker(input, "fence.json", args[0], args[1], args[2]); ok {
			t.Fatalf("%s marker accepted", name)
		}
	}
}

func TestReplicationInfoParsesOffsetsAndLink(t *testing.T) {
	f := &fakeValkey{candidateOffset: 77, linkUp: true, candidateContain: "c"}
	useFakeValkey(t, f)
	info := keyValueReplicationInfo(context.Background(), bootstrapRecord{}, "c")
	if !info.ok || !info.isReplica() || !info.linkUp || info.slaveOffset != 77 || info.syncing {
		t.Fatalf("misparsed: %+v", info)
	}
}

func TestStaleExecutorAttemptCannotCommitKeyValueAuthority(t *testing.T) {
	planned, record, paths := keyValueTransitionFixture(t)
	planned.Kind, planned.ID, planned.DependsOn = planner.SwitchKeyValueAuthority, "op-04", []string{"op-03"}
	input := *planned.Input.KeyValue
	record.SchemaVersion, record.Operator, record.ExecutorDigest = "provision.dev/bootstrap/v2", "operator", "sha256:"+strings.Repeat("e", 64)
	dir := t.TempDir()
	signer, publicKey := testAuthority(t, dir, &record)
	paths.authorityState = dir
	if err := writeJSONAtomic(filepath.Join(dir, "fence.json"), hostFence{FencingToken: 5, AttemptID: "attempt-55555555555555555555555555555555"}, 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeValkey{fenced: true, activeOffset: 1, candidateOffset: 1, linkUp: true, activeContainer: input.Previous.Container, candidateContain: input.Candidate().Container}
	useFakeValkey(t, f)
	digest, err := planner.OperationDigest(planned)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	envelope := signedTestEnvelope(t, signer, planned, digest, record, "attempt-11111111111111111111111111111111", 1, now, now.Add(time.Minute))
	envelope.SensitiveValues = map[string]string{input.CredentialReference: "valkey://app_user:LongRandomPassword-1234@127.0.0.1:26379"}
	if _, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, now); err == nil || !strings.Contains(err.Error(), "fencing token is stale") {
		t.Fatalf("stale attempt was not rejected: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("stale attempt reached the Key-Value Store: %v", f.calls)
	}
	if _, err := os.Stat(keyValueMarkerPath(input, "authority-switch.json")); err == nil {
		t.Fatal("stale attempt committed authority-switch evidence")
	}
}

func testAuthority(t *testing.T, dir string, record *bootstrapRecord) (authority.Signer, ed25519.PublicKey) {
	t.Helper()
	privatePath, publicPath := filepath.Join(dir, "authority.key"), filepath.Join(dir, "authority.pub")
	info, err := authority.GenerateKeyPair(privatePath, publicPath)
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
	record.AuthorityKeyID = info.ID
	return signer, publicKey
}
