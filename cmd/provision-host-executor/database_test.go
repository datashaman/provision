package main

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"provision/internal/config"
	"provision/internal/host"
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
		{"missing transition validation", func(input *planner.DatabaseOperationInput) {
			input.TransitionValidation.PhysicalReplicationRequired = nil
		}},
		{"weaker transition validation wording", func(input *planner.DatabaseOperationInput) {
			input.TransitionValidation.PhysicalReplicationRequired = []string{"trust me"}
		}},
		{"active replacement without compatibility proof", func(input *planner.DatabaseOperationInput) {
			input.Observed.Active = &host.DatabaseGenerationStatus{ID: "postgresql-16-0-aaaaaaaaaaaa", LogicalID: input.LogicalID, Ready: true, Connectivity: true}
		}},
		{"unsafe candidate observed", func(input *planner.DatabaseOperationInput) {
			input.Observed.Candidate = &host.DatabaseGenerationStatus{ID: "postgresql-18-0-bbbbbbbbbbbb", LogicalID: input.LogicalID, Health: "failed", CompatibilityGate: "physical-replication-version"}
		}},
		{"unverified ready candidate observed", func(input *planner.DatabaseOperationInput) {
			input.Observed.Candidate = &host.DatabaseGenerationStatus{ID: "postgresql-17-6-bbbbbbbbbbbb", LogicalID: input.LogicalID, Ready: true, Connectivity: true}
		}},
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

func TestCandidateGenerationInspectionReportsCompatibilityGate(t *testing.T) {
	root := t.TempDir()
	generationsRoot := filepath.Join(root, "services", "postgresql", "generations")
	candidateRoot := filepath.Join(generationsRoot, "postgresql-18-0-bbbbbbbbbbbb")
	if err := os.MkdirAll(candidateRoot, 0755); err != nil {
		t.Fatal(err)
	}
	record := postgresqlPackagingRecord{
		SchemaVersion: postgresqlGenerationSchema, LogicalID: "provision-lab-data", GenerationID: "postgresql-18-0-bbbbbbbbbbbb",
		PostgreSQLVersion: "18.0", ImageManifest: "sha256:" + strings.Repeat("b", 64),
		ServiceUnit: "provision-lab-postgresql-candidate.service", Container: "provision-lab-postgresql-candidate",
		Account: "provision-lab", DataPath: filepath.Join(candidateRoot, "data"), QuadletPath: "/etc/containers/systemd/users/999/provision-lab-postgresql-candidate.container",
		Database: "app", CredentialReference: "secret://lab/postgresql-url", Connectivity: true,
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateRoot, "generation.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	candidate := observedPostgreSQLCandidateGeneration(host.DatabaseCapabilities{
		PostgreSQLGenerationDataPath: filepath.Join(generationsRoot, qualifiedPostgreSQLGeneration, "data"),
	}, qualifiedPostgreSQLGeneration)
	if candidate == nil || candidate.ID != record.GenerationID || candidate.Role != "candidate" || candidate.Authority != "none" || candidate.CompatibilityGate != "physical-replication-version" || candidate.Ready || !strings.Contains(candidate.Reason, "physical/logical replication compatibility") {
		t.Fatalf("candidate compatibility evidence was not surfaced: %+v", candidate)
	}
}

func TestPrepareDatabaseValidationRescansHostForLateCandidate(t *testing.T) {
	planned, record, paths := databaseOperationFixture(t)
	candidateRoot := filepath.Join(paths.environmentHome, "services", "postgresql", "generations", "postgresql-18-0-bbbbbbbbbbbb")
	if err := os.MkdirAll(candidateRoot, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(postgresqlPackagingRecord{
		SchemaVersion: postgresqlGenerationSchema, LogicalID: "provision-lab-data", GenerationID: "postgresql-18-0-bbbbbbbbbbbb",
		PostgreSQLVersion: "18.0", ImageManifest: "sha256:" + strings.Repeat("b", 64),
		ServiceUnit: "provision-lab-postgresql-candidate.service", Container: "provision-lab-postgresql-candidate",
		Account: record.Account, DataPath: filepath.Join(candidateRoot, "data"), QuadletPath: "/etc/containers/systemd/users/999/provision-lab-postgresql-candidate.container",
		Database: "app", CredentialReference: "secret://lab/postgresql-url",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateRoot, "generation.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateDatabaseOperation(planned, record, paths); err == nil || !strings.Contains(err.Error(), "physical-replication-version") {
		t.Fatalf("late unsafe candidate was not rejected with compatibility evidence: %v", err)
	}
}

func TestDatabaseTransitionValidationAllowsCandidateIdentityOnlyBeforeAuthority(t *testing.T) {
	planned, record, paths := databaseOperationFixture(t)
	active := host.DatabaseGenerationStatus{
		ID: "postgresql-17-6-aaaaaaaaaaaa", LogicalID: planned.Input.Database.LogicalID, Role: "active", Authority: "authoritative",
		Ready: true, PostgreSQLVersion: planned.Input.Database.PostgreSQLVersion, ImageManifest: planned.Input.Database.ImageManifest,
		ServiceUnit: "provision-lab-postgresql-old.service", Container: "provision-lab-postgresql-old", Account: record.Account,
		DataPath:    filepath.Join(paths.environmentHome, "services", "postgresql", "generations", "postgresql-17-6-aaaaaaaaaaaa", "data"),
		QuadletPath: "/etc/containers/systemd/users/999/provision-lab-postgresql-old.container", Database: planned.Input.Database.DatabaseName, Connectivity: true,
	}
	stable := *planned.Input.Database
	stable.Observed.Active = &active
	stable.ForwardCutoverGuarantee = "lossless-after-bounded-write-fence"
	stable.TransitionMechanism = "offline-logical-snapshot-with-bounded-write-fence"
	candidate := candidateDatabaseInput(stable)

	candidateStep := planned
	candidateStep.Kind = planner.PrepareDatabaseCandidate
	candidateStep.Input.Database = &candidate
	if err := validateDatabaseOperation(candidateStep, record, paths); err != nil {
		t.Fatalf("candidate transition Database operation rejected: %v", err)
	}

	stableStep := planned
	stableStep.Kind = planner.SwitchDatabaseAuthority
	stableStep.DependsOn = []string{"op-03"}
	stableStep.Input.Database = &stable
	if err := validateDatabaseOperation(stableStep, record, paths); err != nil {
		t.Fatalf("stable authority switch Database operation rejected: %v", err)
	}

	tampered := candidateStep
	tampered.Input.Database = &stable
	if err := validateDatabaseOperation(tampered, record, paths); err == nil {
		t.Fatal("candidate preparation accepted stable service identity")
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
		ForwardCutoverGuarantee: "lossless-after-bounded-write-fence", TransitionMechanism: "offline-logical-snapshot-with-bounded-write-fence",
		TransitionCleanupPolicy: "retain-previous-generation",
		Backup:                  config.DatabaseBackupPolicy{Mode: "required", Frequency: "1h0m0s", Retention: "168h0m0s", Destination: "file:///var/lib/provision/backups/lab/postgresql"},
		Recovery:                config.DatabaseRecoveryPolicy{PointObjective: "1h0m0s", TimeObjective: "4h0m0s", RestoreVerification: "isolated-generation", HostLoss: "not-declared"},
		RestoreCandidate: planner.DatabaseRestoreCandidateInput{
			GenerationID: qualifiedPostgreSQLGeneration + "-restore-check",
			ServiceUnit:  "provision-lab-postgresql-restore.service", Container: "provision-lab-postgresql-restore",
			DataPath:      filepath.Join(environmentHome, "services", "postgresql", "restore-candidates", qualifiedPostgreSQLGeneration+"-restore-check", "data"),
			QuadletPath:   "/etc/containers/systemd/users/" + strconv.Itoa(uid) + "/provision-lab-postgresql-restore.container",
			ListenAddress: "127.0.0.1", Port: 25434, Isolated: true,
		},
		TransitionValidation: planner.DatabaseTransitionValidation{
			RequiredStoreRollbackGuarantee: "forward-only",
			PhysicalReplicationRequired: []string{
				"same PostgreSQL major version family",
				"compatible server parameters and extensions",
				"base backup or streaming replication source remains the active generation",
				"candidate reaches verified replay position before any authority change",
			},
			LogicalReplicationRequired: []string{
				"schema compatibility is validated before subscription",
				"DDL changes are outside the replication window or explicitly coordinated",
				"sequence semantics and gaps are explicitly accepted",
				"extension and workload restrictions are validated",
				"bounded final write fence is declared and within policy",
			},
			ForwardCutoverRequirement:      "no acknowledged writes may be lost before candidate authority",
			WriteFenceRequirement:          "active-database-read-only-with-session-termination",
			WriteFenceMaximum:              "30s",
			RollbackClassificationRequired: "forward-only",
			UnsupportedCandidateFailure:    "fail-closed-before-authority-change",
		},
		Binding: planner.DatabaseBindingInput{Reference: "secret://lab/postgresql-url", Protocol: "postgresql", Host: "127.0.0.1", Port: 25432, Database: "app"},
	}
	planned := planner.Operation{ID: "op-01", Kind: planner.PrepareDatabase, DependsOn: []string{}, Input: planner.OperationInput{Database: input}, Recovery: planner.RetainDatabase}
	return planned, bootstrapRecord{Environment: "lab", Account: current.Username}, executionPaths{environmentHome: environmentHome}
}
