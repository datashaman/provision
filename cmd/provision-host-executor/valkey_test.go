package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"provision/internal/implementation/hostkeyvalue"
	"provision/internal/planner"
)

func keyValueOperationFixture(t *testing.T) (planner.Operation, bootstrapRecord, executionPaths) {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(current.Uid)
	image, _ := hostkeyvalue.ImageFor("9.1.2")
	environmentHome := filepath.Join(t.TempDir(), "environments", "lab")
	namespace := "provision-lab-sessions/rev-1"
	input := &planner.KeyValuePlan{
		Component: "sessions", LogicalID: "provision-lab-sessions", GenerationID: image.Generation(), Implementation: "valkey-quadlet", DataRole: "authoritative",
		ValkeyVersion: image.Version, ImageIndex: image.Index, ImageManifest: image.Manifest, ImageReference: image.Reference(), QualificationDigest: image.Evidence,
		ServiceUnit: "provision-lab-valkey.service", Container: "provision-lab-valkey", Account: current.Username,
		DataPath:          filepath.Join(environmentHome, "services", "valkey", "generations", image.Generation(), "data"),
		QuadletDefinition: "provision-lab-valkey.container", QuadletPath: "/etc/containers/systemd/users/" + strconv.Itoa(uid) + "/provision-lab-valkey.container",
		ListenAddress: "127.0.0.1", Port: 26379, CredentialReference: "secret://lab/valkey-url", AppendOnly: "yes", AppendFsync: "everysec",
		Binding: planner.KeyValueBinding{Component: "sessions", LogicalID: "provision-lab-sessions", Namespace: namespace, Keys: []string{namespace + "/key-0001"}, ValuePrefix: "v:"},
	}
	planned := planner.Operation{ID: "op-01", Kind: planner.PrepareKeyValue, DependsOn: []string{}, Input: planner.OperationInput{KeyValue: input}, Recovery: "retain-key-value"}
	return planned, bootstrapRecord{Environment: "lab", Account: current.Username}, executionPaths{environmentHome: environmentHome}
}

func TestPrepareKeyValueValidationPinsIdentityPathsAndListener(t *testing.T) {
	planned, record, paths := keyValueOperationFixture(t)
	if err := validateKeyValueOperation(planned, record, paths); err != nil {
		t.Fatalf("valid prepareKeyValue rejected: %v", err)
	}
	for name, mutate := range map[string]func(*planner.KeyValuePlan){
		"caller-selected data path": func(i *planner.KeyValuePlan) { i.DataPath = filepath.Join(t.TempDir(), "escape") },
		"mutable image":             func(i *planner.KeyValuePlan) { i.ImageReference = "docker.io/valkey/valkey:latest" },
		"derived role":              func(i *planner.KeyValuePlan) { i.DataRole = "derived" },
		"public listener":           func(i *planner.KeyValuePlan) { i.ListenAddress = "0.0.0.0" },
		"weak persistence":          func(i *planner.KeyValuePlan) { i.AppendFsync = "no" },
		"wrong account":             func(i *planner.KeyValuePlan) { i.Account = "root" },
		"foreign key namespace":     func(i *planner.KeyValuePlan) { i.Binding.Keys = []string{"other/key"} },
	} {
		t.Run(name, func(t *testing.T) {
			input := *planned.Input.KeyValue
			mutate(&input)
			tampered := planned
			tampered.Input.KeyValue = &input
			if err := validateKeyValueOperation(tampered, record, paths); err == nil {
				t.Fatal("tampered prepareKeyValue accepted")
			}
		})
	}
}

func TestKeyValueSecretBoundaryAndQuadletDoNotExposeResolvedValue(t *testing.T) {
	planned, _, _ := keyValueOperationFixture(t)
	input := *planned.Input.KeyValue
	secret := "valkey://app_user:LongRandomPassword-1234@127.0.0.1:26379"
	if _, _, err := parseKeyValueSecret(secret, input.Port); err != nil {
		t.Fatal(err)
	}
	if _, err := validateKeyValueSensitiveValues(map[string]string{input.CredentialReference: secret}, input); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"valkey://app_user:LongRandomPassword-1234@example.com:26379",
		"valkey://app_user:LongRandomPassword-1234@127.0.0.1:6379",
		"valkey://app_user:pass'word@127.0.0.1:26379",
		"redis://app_user:LongRandomPassword-1234@127.0.0.1:26379",
	} {
		if _, _, err := parseKeyValueSecret(invalid, input.Port); err == nil || strings.Contains(err.Error(), "LongRandomPassword") {
			t.Fatalf("unsafe secret accepted or disclosed: %v", err)
		}
	}
	if _, err := validateKeyValueSensitiveValues(map[string]string{input.CredentialReference: secret, "secret://lab/other": "x"}, input); err == nil {
		t.Fatal("extra resolved value accepted")
	}
	quadlet := renderKeyValueQuadlet(input, "/x/credential-entrypoint")
	if strings.Contains(quadlet, "LongRandomPassword") || !strings.Contains(quadlet, input.ImageReference) || !strings.Contains(quadlet, "--appendonly yes --appendfsync everysec") || !strings.Contains(quadlet, "PublishPort=127.0.0.1:26379:6379") || !strings.Contains(quadlet, "LoadCredentialEncrypted=valkey-credential") {
		t.Fatalf("Quadlet omitted pinned identity/persistence or exposed a secret:\n%s", quadlet)
	}
}

func TestKeyValueStatusIdentityListsOwnedResourcesWithoutCredentialValues(t *testing.T) {
	planned, _, _ := keyValueOperationFixture(t)
	status := keyValueStatusIdentity(*planned.Input.KeyValue)
	if status.ID != planned.Input.KeyValue.GenerationID || status.Role != "authoritative" || len(status.OwnedResources) < 6 || status.Ready {
		t.Fatalf("unexpected identity: %+v", status)
	}
}

func TestConfigValueParsesCLIOutput(t *testing.T) {
	if configValue([]byte("appendfsync\neverysec\n")) != "everysec" || configValue([]byte("")) != "" {
		t.Fatal("CONFIG GET output misparsed")
	}
}

func keyValueBindingFixture() planner.KeyValueHTTPBinding {
	ns := "provision-lab-sessions/rev-1"
	return planner.KeyValueHTTPBinding{
		Component: "sessions", LogicalID: "provision-lab-sessions", GenerationID: "valkey-9-1-2-ac9c858b86dc", Reference: "secret://lab/valkey-url",
		Host: "127.0.0.1", Port: 26379, EnvironmentVariable: "PROVISION_KEYVALUE_URL_FILE", Namespace: ns, Keys: []string{ns + "/key-0001"}, ValuePrefix: "v:",
	}
}

func TestKeyValueBindingValidationRejectsDriftAndUnpreparedStore(t *testing.T) {
	record := bootstrapRecord{Environment: "lab", Account: "provision-lab"}
	paths := executionPaths{environmentHome: t.TempDir()}
	good := keyValueBindingFixture()
	// A binding with no verified prepared generation record is rejected.
	if err := validateKeyValueBindings([]planner.KeyValueHTTPBinding{good}, record, paths); err == nil {
		t.Fatal("binding to an unprepared Key-Value Store accepted")
	}
	for name, mutate := range map[string]func(*planner.KeyValueHTTPBinding){
		"foreign logical id": func(b *planner.KeyValueHTTPBinding) { b.LogicalID = "other" },
		"foreign secret":     func(b *planner.KeyValueHTTPBinding) { b.Reference = "secret://prod/valkey-url" },
		"public host":        func(b *planner.KeyValueHTTPBinding) { b.Host = "0.0.0.0" },
		"key outside space":  func(b *planner.KeyValueHTTPBinding) { b.Keys = []string{"other/key"} },
		"comma key":          func(b *planner.KeyValueHTTPBinding) { b.Keys = []string{b.Namespace + "/a,b"} },
	} {
		b := good
		mutate(&b)
		if err := validateKeyValueBindings([]planner.KeyValueHTTPBinding{b}, record, paths); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := validateKeyValueBindings([]planner.KeyValueHTTPBinding{good, good}, record, paths); err == nil {
		t.Fatal("two bindings accepted")
	}
	if err := validateKeyValueBindings(nil, record, paths); err != nil {
		t.Fatal(err)
	}
}

func TestKeyValueUnitBindingDeliversCredentialByReferenceOnly(t *testing.T) {
	unit := keyValueUnitBinding(planner.SystemdInput{GenerationReference: planner.GenerationReference{Account: "provision-lab", KeyValueBindings: []planner.KeyValueHTTPBinding{keyValueBindingFixture()}}})
	for _, want := range []string{"LoadCredentialEncrypted=valkey-url:/var/lib/provision/environments/lab/credentials/valkey-url", "PROVISION_KEYVALUE_LOGICAL_ID=provision-lab-sessions", "PROVISION_KEYVALUE_URL_FILE=%d/valkey-url"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "valkey://") || keyValueUnitBinding(planner.SystemdInput{}) != "" {
		t.Fatal("credential value rendered or empty binding not empty")
	}
}

func TestKeyValueBindingRequiresVerifiedGeneration(t *testing.T) {
	planned, record, paths := keyValueOperationFixture(t)
	input := *planned.Input.KeyValue
	binding := keyValueBindingFixture()
	binding.GenerationID = input.GenerationID
	binding.Namespace = input.Binding.Namespace
	binding.Keys = input.Binding.Keys
	binding.ValuePrefix = input.Binding.ValuePrefix
	write := func(verified bool) {
		if err := os.MkdirAll(filepath.Dir(keyValueRecordPath(input)), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(keyValueRecordPath(input))
		if err := writeJSONAtomic(keyValueRecordPath(input), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: input, Verified: verified}, 0444); err != nil {
			t.Fatal(err)
		}
	}
	bindings := []planner.KeyValueHTTPBinding{binding}
	write(false)
	if err := validateKeyValueBindings(bindings, record, paths); err == nil {
		t.Fatal("binding to an unverified generation accepted")
	}
	write(true)
	if err := validateKeyValueBindings(bindings, record, paths); err != nil {
		t.Fatalf("binding to a verified generation rejected: %v", err)
	}
}

func TestParseLoopbackListenerAcceptsOnlyOnePublishedAddress(t *testing.T) {
	if a, p, ok := parseLoopbackListener("127.0.0.1:26379\n"); !ok || a != "127.0.0.1" || p != 26379 {
		t.Fatalf("exact listener misparsed: %s %d %t", a, p, ok)
	}
	for _, bad := range []string{"", "0.0.0.0:26379\n127.0.0.1:26379\n", "127.0.0.1:x"} {
		if a, p, ok := parseLoopbackListener(bad); ok && a == "127.0.0.1" && p == 26379 {
			t.Fatalf("%q accepted", bad)
		}
	}
}
