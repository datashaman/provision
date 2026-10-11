package main

import (
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
