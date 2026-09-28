package main

import (
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"provision/internal/config"
	"provision/internal/planner"
)

func TestPrepareDatabaseValidationPinsIdentityPathsAndPolicy(t *testing.T) {
	planned, record, paths := databaseOperationFixture(t)
	if err := validateDatabaseOperation(planned, record, paths); err != nil {
		t.Fatalf("valid prepareDatabase rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*planner.DatabaseOperationInput)
	}{
		{"caller-selected data path", func(input *planner.DatabaseOperationInput) { input.DataPath = filepath.Join(t.TempDir(), "escape") }},
		{"mutable image", func(input *planner.DatabaseOperationInput) {
			input.ImageReference = "docker.io/library/postgres:latest"
		}},
		{"wrong account", func(input *planner.DatabaseOperationInput) { input.Account = "root" }},
		{"overclaimed rollback", func(input *planner.DatabaseOperationInput) { input.StoreRollbackGuarantee = "automatic" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := planned
			input := *planned.Input.Database
			test.mutate(&input)
			tampered.Input.Database = &input
			if err := validateDatabaseOperation(tampered, record, paths); err == nil {
				t.Fatal("tampered prepareDatabase accepted")
			}
		})
	}
}

func TestDatabaseSecretBoundaryAndQuadletDoNotExposeResolvedValue(t *testing.T) {
	planned, _, _ := databaseOperationFixture(t)
	input := *planned.Input.Database
	secret := "postgresql://app:LongRandomPassword_1234@127.0.0.1:25432/app"
	username, password, database, port, err := parseDatabaseSecret(secret)
	if err != nil || username != "app" || password != "LongRandomPassword_1234" || database != "app" || port != 25432 {
		t.Fatalf("valid resolved secret rejected: %q %q %q %d %v", username, password, database, port, err)
	}
	if _, _, err := validateDatabaseSensitiveValues(map[string]string{input.CredentialReference: secret}, input); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"postgresql://app:short@127.0.0.1:25432/app",
		"postgresql://app:LongRandomPassword_1234@example.com:25432/app",
	} {
		if _, _, _, _, err := parseDatabaseSecret(invalid); err == nil || strings.Contains(err.Error(), "LongRandomPassword") {
			t.Fatalf("unsafe secret accepted or disclosed: %v", err)
		}
	}
	if _, _, err := validateDatabaseSensitiveValues(map[string]string{input.CredentialReference: "postgresql://other:LongRandomPassword_1234@127.0.0.1:25432/app"}, input); err == nil || strings.Contains(err.Error(), "LongRandomPassword") {
		t.Fatalf("mismatched Database user accepted or disclosed: %v", err)
	}
	if _, _, err := validateDatabaseSensitiveValues(map[string]string{input.CredentialReference: "postgresql://app:LongRandomPassword_1234@127.0.0.1:5432/app"}, input); err == nil || strings.Contains(err.Error(), "LongRandomPassword") {
		t.Fatalf("mismatched Database port accepted or disclosed: %v", err)
	}
	quadlet := renderDatabaseQuadlet(input, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(input.DataPath))), "credential-entrypoint"), username)
	if strings.Contains(quadlet, password) || !strings.Contains(quadlet, input.ImageReference) || !strings.Contains(quadlet, "LoadCredentialEncrypted=postgresql-url") || !strings.Contains(quadlet, "PublishPort=127.0.0.1:25432:5432") {
		t.Fatalf("Quadlet omitted pinned identity or exposed a secret:\n%s", quadlet)
	}
}

func TestDatabaseStatusIdentifiesOwnedResourcesByKind(t *testing.T) {
	planned, _, _ := databaseOperationFixture(t)
	input := *planned.Input.Database
	status := databaseOperationIdentity(input).Database
	want := []string{
		"systemd-unit:" + input.ServiceUnit,
		"container:" + input.Container,
		"data-path:" + input.DataPath,
		"quadlet:" + input.QuadletPath,
		"generation-record:" + filepath.Join(filepath.Dir(input.DataPath), "generation.json"),
		"credential-entrypoint:" + filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(input.DataPath))), "credential-entrypoint"),
		"encrypted-credential:/var/lib/provision/runtime/lab/.config/credstore.encrypted/postgresql-url",
		"postgresql-database:" + input.DatabaseName,
	}
	if !slices.Equal(status.OwnedResources, want) {
		t.Fatalf("owned resources = %#v, want %#v", status.OwnedResources, want)
	}
	if !slices.Equal(status.SupportedGuarantees, []string{"pinned-postgresql-image", "encrypted-systemd-credential", "loopback-only-listener", "sql-identity-verification"}) {
		t.Fatalf("Database status overclaims or omits guarantees: %+v", status)
	}
}

func databaseOperationFixture(t *testing.T) (planner.Operation, bootstrapRecord, executionPaths) {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.Atoi(current.Uid)
	if err != nil || uid <= 0 {
		t.Fatalf("test requires a non-root user identity, uid=%q err=%v", current.Uid, err)
	}
	environmentHome := filepath.Join(t.TempDir(), "environments", "lab")
	input := &planner.DatabaseOperationInput{
		Component: "data", LogicalID: "provision-lab-data", GenerationID: qualifiedPostgreSQLGeneration,
		Implementation: "postgresql-quadlet", Lifecycle: "managed", Rollout: "required", CredentialReference: "secret://lab/postgresql-url",
		DatabaseName: "app", DataRole: "authoritative", PostgreSQLVersion: qualifiedPostgreSQL, ImageIndex: qualifiedPostgreSQLIndex,
		ImageManifest: qualifiedPostgreSQLManifest, ImageReference: qualifiedPostgreSQLReference,
		ServiceUnit: "provision-lab-postgresql.service", Container: "provision-lab-postgresql", Account: current.Username,
		DataPath:      filepath.Join(environmentHome, "services", "postgresql", "generations", qualifiedPostgreSQLGeneration, "data"),
		QuadletPath:   "/etc/containers/systemd/users/" + strconv.Itoa(uid) + "/provision-lab-postgresql.container",
		ListenAddress: "127.0.0.1", Port: 25432, StoreRollbackGuarantee: "forward-only",
		ForwardCutoverGuarantee: "not-qualified-by-packaging-proof", TransitionMechanism: "none-qualified-by-packaging-proof",
		TransitionCleanupPolicy: "retain-previous-generation",
		Backup:                  config.DatabaseBackupPolicy{Mode: "required", Frequency: "1h0m0s", Retention: "168h0m0s"},
		Recovery:                config.DatabaseRecoveryPolicy{PointObjective: "1h0m0s", TimeObjective: "4h0m0s", RestoreVerification: "isolated-generation", HostLoss: "off-host-backup-required"},
		Binding:                 planner.DatabaseBindingInput{Reference: "secret://lab/postgresql-url", Protocol: "postgresql", Host: "127.0.0.1", Port: 25432, Database: "app"},
	}
	planned := planner.Operation{ID: "op-01", Kind: planner.PrepareDatabase, DependsOn: []string{}, Input: planner.OperationInput{Database: input}, Recovery: planner.RetainDatabase}
	return planned, bootstrapRecord{Environment: "lab", Account: current.Username}, executionPaths{environmentHome: environmentHome}
}
