package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/planner"
)

const (
	qualifiedPostgreSQL           = "17.6"
	qualifiedPostgreSQLIndex      = "sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929"
	qualifiedPostgreSQLManifest   = "sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409"
	qualifiedPostgreSQLReference  = "docker.io/library/postgres@" + qualifiedPostgreSQLManifest
	qualifiedPostgreSQLGeneration = "postgresql-17-6-b86568d3e0fe"
	qualifiedPostgreSQLEvidence   = "sha256:892fb587ac7323ba4f04d38b1fc165f9e2304219f3de14e7e365cb60b4960eff"
	postgresqlCredentialName      = "postgresql-url"
	postgresqlGenerationSchema    = "provision.dev/host-postgresql-packaging-generation/v1alpha1"
)

type postgresqlPackagingRecord struct {
	SchemaVersion       string `json:"schemaVersion"`
	LogicalID           string `json:"logicalId"`
	GenerationID        string `json:"generationId"`
	PostgreSQLVersion   string `json:"postgresqlVersion"`
	ImageIndex          string `json:"imageIndex"`
	ImageManifest       string `json:"imageManifest"`
	ImageReference      string `json:"imageReference"`
	ServiceUnit         string `json:"serviceUnit"`
	Container           string `json:"container"`
	Account             string `json:"account"`
	DataPath            string `json:"dataPath"`
	QuadletPath         string `json:"quadletPath"`
	Database            string `json:"database"`
	CredentialReference string `json:"credentialReference"`
	ListenAddress       string `json:"listenAddress"`
	Port                int    `json:"port"`
	Connectivity        bool   `json:"connectivity"`
	DurableRestart      bool   `json:"durableRestart"`
	Verified            bool   `json:"verified"`
	PlanID              string `json:"planId,omitempty"`
	OperationDigest     string `json:"operationDigest,omitempty"`
}

func inspectDatabase(environment, account string) *host.DatabaseStatus {
	service := "provision-" + environment + "-postgresql"
	generation := qualifiedPostgreSQLGeneration
	dataPath := filepath.Join("/var/lib/provision/environments", environment, "services", "postgresql", "generations", generation, "data")
	podmanVersion := installedPodmanPackageVersion()
	uid := command("id", "-u", account)
	quadletPath := ""
	if uid != "" {
		quadletPath = "/etc/containers/systemd/users/" + uid + "/" + service + ".container"
	}
	accountUID, _ := strconv.Atoi(uid)
	capabilities := host.DatabaseCapabilities{
		PodmanVersion: podmanVersion, Quadlet: commandSucceeded("test", "-x", "/usr/lib/systemd/system-generators/podman-system-generator"),
		RootlessEnvironmentAccount:    hasAccount(account, "/var/lib/provision/runtime/"+environment),
		SystemdCredentials:            commandSucceeded("systemd-creds", "--version"),
		SubordinateIDs:                fileContainsPrefix("/etc/subuid", account+":") && fileContainsPrefix("/etc/subgid", account+":"),
		LingeringUserManager:          rootOwned("/var/lib/systemd/linger/"+account, 0644) && uid != "" && command("systemctl", "is-active", "user@"+uid+".service") == "active",
		QuadletDefinitionRootOwned:    quadletPath != "" && rootOwned(quadletPath, 0644),
		GenerationDataPathOwned:       environmentDataOwned(dataPath, account, accountUID, accountGID(account)),
		EncryptedCredentialObserved:   ownedBy(filepath.Join("/var/lib/provision/runtime", environment, ".config", "credstore.encrypted", postgresqlCredentialName), accountUID, 0600),
		PostgreSQLQualificationDigest: qualifiedPostgreSQLEvidence,
		PostgreSQLVersion:             qualifiedPostgreSQL,
		PostgreSQLImageIndex:          qualifiedPostgreSQLIndex,
		PostgreSQLImageManifest:       qualifiedPostgreSQLManifest,
		PostgreSQLImageReference:      qualifiedPostgreSQLReference,
		PostgreSQLServiceUnit:         service + ".service",
		PostgreSQLContainer:           service,
		PostgreSQLAccount:             account,
		PostgreSQLGeneration:          generation,
		PostgreSQLGenerationDataPath:  dataPath,
		PostgreSQLQuadletPath:         quadletPath,
		PostgreSQLCredentialReference: "secret://" + environment + "/postgresql-url",
		PostgreSQLListenAddress:       "127.0.0.1",
		PostgreSQLPort:                25432,
		StoreRollbackGuarantee:        "not-qualified-by-packaging-proof",
		ForwardCutoverGuarantee:       "not-qualified-by-packaging-proof",
		SupportedTransitionMechanism:  "none-qualified-by-packaging-proof",
	}
	deployment, findings := inspectPostgreSQLDeployment(context.Background(), capabilities)
	return &host.DatabaseStatus{
		SchemaVersion: "provision.dev/host-database-inspection/v1alpha1", ObservationComplete: true,
		Findings: findings, Capabilities: capabilities, Deployment: deployment,
	}
}

func inspectPostgreSQLDeployment(ctx context.Context, capability host.DatabaseCapabilities) (host.DatabaseDeploymentStatus, []string) {
	recordPath := filepath.Join(filepath.Dir(capability.PostgreSQLGenerationDataPath), "generation.json")
	data, err := os.ReadFile(recordPath)
	if os.IsNotExist(err) {
		return host.DatabaseDeploymentStatus{}, nil
	}
	if err != nil {
		return host.DatabaseDeploymentStatus{}, []string{"PostgreSQL generation record cannot be read"}
	}
	var record postgresqlPackagingRecord
	if err := json.Unmarshal(data, &record); err != nil || record.SchemaVersion != postgresqlGenerationSchema {
		return host.DatabaseDeploymentStatus{}, []string{"PostgreSQL generation record is invalid"}
	}
	generation := &host.DatabaseGenerationStatus{
		ID: record.GenerationID, LogicalID: record.LogicalID, Role: "candidate", Authority: "none", PostgreSQLVersion: record.PostgreSQLVersion,
		ImageManifest: record.ImageManifest, ServiceUnit: record.ServiceUnit, Container: record.Container,
		Account: record.Account, DataPath: record.DataPath, QuadletPath: record.QuadletPath,
		Database: record.Database, Connectivity: record.Connectivity, DurableRestart: record.DurableRestart,
		SupportedGuarantees: databaseSupportedGuarantees(), OwnedResources: databaseOwnedResources(record),
	}
	findings := []string{}
	if record.GenerationID != capability.PostgreSQLGeneration ||
		record.PostgreSQLVersion != capability.PostgreSQLVersion ||
		record.ImageIndex != capability.PostgreSQLImageIndex ||
		record.ImageManifest != capability.PostgreSQLImageManifest ||
		record.ImageReference != capability.PostgreSQLImageReference ||
		record.ServiceUnit != capability.PostgreSQLServiceUnit ||
		record.Container != capability.PostgreSQLContainer ||
		record.Account != capability.PostgreSQLAccount ||
		record.DataPath != capability.PostgreSQLGenerationDataPath ||
		record.QuadletPath != capability.PostgreSQLQuadletPath ||
		record.CredentialReference != capability.PostgreSQLCredentialReference {
		generation.Reason = "recorded PostgreSQL generation differs from qualified packaging capability"
		generation.RecoveryAction = "re-run the packaging qualification on a clean disposable Host"
		findings = append(findings, generation.Reason)
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	}
	if !record.Verified || !digestPattern.MatchString(record.PlanID) || !digestPattern.MatchString(record.OperationDigest) {
		generation.Health = "degraded"
		generation.Reason = "PostgreSQL generation record is not bound to an approved Plan and operation"
		generation.RecoveryAction = "run the signed Database preparation operation for this exact generation"
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	}
	if command("systemctl", "is-active", "user@"+strconv.Itoa(accountUID(record.Account))+".service") != "active" {
		generation.Health = "unobservable"
		generation.Reason = "Environment user manager is not active"
		generation.RecoveryAction = "restore or restart the Environment user manager before planning Database lifecycle work"
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, err := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "systemctl", "--user", "is-active", record.ServiceUnit)
	if err != nil || strings.TrimSpace(string(status)) != "active" {
		generation.Health = "unhealthy"
		generation.Reason = "PostgreSQL service is not active"
		generation.RecoveryAction = "inspect the Environment user journal and the PostgreSQL container before planning Database lifecycle work"
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	}
	manifest, manifestErr := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "podman", "inspect", "--format", "{{.ImageDigest}}", record.Container)
	binding, bindingErr := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "podman", "port", record.Container, "5432/tcp")
	credentialMount, credentialErr := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "podman", "inspect", "--format", "{{range .}}{{range .Mounts}}{{if eq .Destination \"/run/provision-credential/postgresql-url\"}}{{.RW}}{{end}}{{end}}{{end}}", record.Container)
	if manifestErr != nil || strings.TrimSpace(string(manifest)) != record.ImageManifest || bindingErr != nil || strings.TrimSpace(string(binding)) != "127.0.0.1:25432" || credentialErr != nil || strings.TrimSpace(string(credentialMount)) != "false" {
		generation.Health = "drifted"
		generation.Reason = "PostgreSQL runtime image, loopback binding, or credential boundary differs from the verified generation"
		generation.RecoveryAction = "run the signed Database preparation operation or inspect the PostgreSQL Quadlet and container"
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	}
	version, err := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "podman", "exec", record.Container, "psql", "-U", databaseUserFromRecord(record), "-d", record.Database, "-Atc", "SHOW server_version;")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(version)), qualifiedPostgreSQL) {
		generation.Health = "unobservable"
		generation.Reason = "PostgreSQL connectivity or product version cannot be verified"
		generation.RecoveryAction = "re-run the packaging proof or inspect the credential and container state"
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	}
	identity, identityErr := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "podman", "exec", record.Container, "psql", "-U", databaseUserFromRecord(record), "-d", record.Database, "-Atc", "SELECT current_database() || ':' || current_user;")
	if identityErr != nil || strings.TrimSpace(string(identity)) != record.Database+":"+databaseUserFromRecord(record) {
		generation.Health = "unobservable"
		generation.Reason = "PostgreSQL database identity cannot be verified"
		generation.RecoveryAction = "inspect the PostgreSQL credential, database, and role before planning Database lifecycle work"
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	}
	generation.Ready = generation.Connectivity && record.Verified
	if !generation.Ready {
		generation.Health = "degraded"
		generation.Reason = "PostgreSQL generation has not completed connectivity verification"
		generation.RecoveryAction = "inspect the credential, PostgreSQL service, and SQL identity before planning Database lifecycle work"
		return host.DatabaseDeploymentStatus{Candidate: generation}, findings
	} else {
		generation.Health = "healthy"
		generation.Role = "active"
		generation.Authority = "authoritative"
	}
	deployment := host.DatabaseDeploymentStatus{Active: generation}
	if candidate := observedPostgreSQLCandidateGeneration(capability, generation.ID); candidate != nil {
		deployment.Candidate = candidate
	}
	return deployment, findings
}

func observedPostgreSQLCandidateGeneration(capability host.DatabaseCapabilities, activeID string) *host.DatabaseGenerationStatus {
	generationsRoot := filepath.Dir(filepath.Dir(capability.PostgreSQLGenerationDataPath))
	entries, err := os.ReadDir(generationsRoot)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == activeID {
			continue
		}
		recordPath := filepath.Join(generationsRoot, entry.Name(), "generation.json")
		data, err := os.ReadFile(recordPath)
		if err != nil {
			continue
		}
		var record postgresqlPackagingRecord
		if json.Unmarshal(data, &record) != nil || record.SchemaVersion != postgresqlGenerationSchema || record.GenerationID == "" || record.LogicalID == "" {
			return &host.DatabaseGenerationStatus{
				ID: entry.Name(), Role: "candidate", Authority: "none", Health: "unobservable",
				CompatibilityGate: "generation-record",
				Reason:            "candidate PostgreSQL generation record is invalid",
				RecoveryAction:    "discard the candidate generation or recreate it from a Plan with validated transition evidence",
			}
		}
		return candidateDatabaseGenerationStatus(record)
	}
	return nil
}

func candidateDatabaseGenerationStatus(record postgresqlPackagingRecord) *host.DatabaseGenerationStatus {
	gate := "transition-compatibility-unqualified"
	if record.PostgreSQLVersion != qualifiedPostgreSQL {
		gate = "physical-replication-version"
	}
	return &host.DatabaseGenerationStatus{
		ID: record.GenerationID, LogicalID: record.LogicalID, Role: "candidate", Authority: "none", Ready: false,
		PostgreSQLVersion: record.PostgreSQLVersion, ImageManifest: record.ImageManifest, ServiceUnit: record.ServiceUnit,
		Container: record.Container, Account: record.Account, DataPath: record.DataPath, QuadletPath: record.QuadletPath,
		Database: record.Database, Connectivity: record.Connectivity, DurableRestart: record.DurableRestart, Health: "failed",
		Reason:              "candidate PostgreSQL generation has not proven physical/logical replication compatibility with the active generation",
		RecoveryAction:      "discard the candidate generation or re-plan with validated PostgreSQL transition evidence",
		CompatibilityGate:   gate,
		SupportedGuarantees: databaseSupportedGuarantees(),
		OwnedResources:      databaseOwnedResources(record),
	}
}

func validateDatabaseOperation(planned planner.Operation, record bootstrapRecord, paths executionPaths) error {
	if planned.Kind != planner.PrepareDatabase || len(planned.DependsOn) != 0 || planned.Input.Database == nil || planned.Input.Artifact != nil || planned.Input.Generation != nil || planned.Input.Systemd != nil || planned.Input.Health != nil || planned.Input.Endpoint != nil || planned.Input.Previous != nil || planned.Input.Drain != nil || planned.Input.Retention != nil || planned.Input.Async != nil {
		return errors.New("prepareDatabase requires only its typed Database input and no dependency")
	}
	input := planned.Input.Database
	expectedService := "provision-" + record.Environment + "-postgresql"
	expectedData := filepath.Join(paths.environmentHome, "services", "postgresql", "generations", qualifiedPostgreSQLGeneration, "data")
	if !deploymentIdentifier.MatchString(input.Component) || input.LogicalID != "provision-"+record.Environment+"-"+input.Component || input.GenerationID != qualifiedPostgreSQLGeneration {
		return errors.New("Database identity does not match the bootstrapped Environment")
	}
	if input.Implementation != "postgresql-quadlet" || input.Lifecycle != "managed" || input.Rollout != "required" || input.DataRole != "authoritative" {
		return errors.New("Database lifecycle does not match the qualified managed PostgreSQL contract")
	}
	if input.PostgreSQLVersion != qualifiedPostgreSQL || input.ImageIndex != qualifiedPostgreSQLIndex || input.ImageManifest != qualifiedPostgreSQLManifest || input.ImageReference != qualifiedPostgreSQLReference {
		return errors.New("Database implementation does not match the qualified PostgreSQL product identity")
	}
	if input.ServiceUnit != expectedService+".service" || input.Container != expectedService || input.Account != record.Account || input.DataPath != expectedData || input.ListenAddress != "127.0.0.1" || input.Port != 25432 {
		return errors.New("Database service identity, listener, or owned path does not match the bootstrapped Environment")
	}
	uid := accountUID(record.Account)
	if uid <= 0 || input.QuadletPath != fmt.Sprintf("/etc/containers/systemd/users/%d/%s.container", uid, expectedService) {
		return errors.New("Database Quadlet path does not match the bootstrapped Environment account")
	}
	if input.CredentialReference != "secret://"+record.Environment+"/postgresql-url" || input.Binding.Reference != input.CredentialReference || input.Binding.Protocol != "postgresql" || input.Binding.Host != "127.0.0.1" || input.Binding.Port != input.Port || input.Binding.Database != input.DatabaseName {
		return errors.New("Database credential Secret Reference or binding does not match the Environment")
	}
	if !databaseNamePattern(input.DatabaseName) {
		return errors.New("Database name is outside the supported PostgreSQL identity subset")
	}
	if input.StoreRollbackGuarantee != "forward-only" || input.ForwardCutoverGuarantee != "not-qualified-by-packaging-proof" || input.TransitionMechanism != "none-qualified-by-packaging-proof" || input.TransitionCleanupPolicy != "retain-previous-generation" {
		return errors.New("Database transition policy exceeds the qualified initial-generation contract")
	}
	if input.Backup.Mode != "required" || input.Recovery.RestoreVerification != "isolated-generation" || input.Recovery.HostLoss != "off-host-backup-required" {
		return errors.New("Database safety policy does not require backups and isolated restore verification")
	}
	if !databaseTransitionValidationMatches(input.TransitionValidation, input.StoreRollbackGuarantee) {
		return errors.New("Database Plan lacks PostgreSQL physical/logical replication compatibility requirements")
	}
	if input.Observed.Active != nil && input.Observed.Active.ID != input.GenerationID {
		return errors.New("prepareDatabase cannot replace an active Database generation without qualified Store Transition compatibility evidence")
	}
	if input.Observed.Candidate != nil {
		return errors.New("prepareDatabase cannot proceed while an unqualified candidate Database generation is observed")
	}
	if candidate := observedPostgreSQLCandidateGeneration(host.DatabaseCapabilities{PostgreSQLGenerationDataPath: input.DataPath}, input.GenerationID); candidate != nil {
		return fmt.Errorf("prepareDatabase cannot proceed while candidate Database generation %s failed PostgreSQL compatibility gate %q: %s", candidate.ID, candidate.CompatibilityGate, candidate.Reason)
	}
	return nil
}

func databaseTransitionValidationMatches(validation planner.DatabaseTransitionValidation, rollbackGuarantee string) bool {
	return validation.RequiredStoreRollbackGuarantee == rollbackGuarantee &&
		slices.Equal(validation.PhysicalReplicationRequired, []string{
			"same PostgreSQL major version family",
			"compatible server parameters and extensions",
			"base backup or streaming replication source remains the active generation",
			"candidate reaches verified replay position before any authority change",
		}) &&
		slices.Equal(validation.LogicalReplicationRequired, []string{
			"schema compatibility is validated before subscription",
			"DDL changes are outside the replication window or explicitly coordinated",
			"sequence semantics and gaps are explicitly accepted",
			"extension and workload restrictions are validated",
			"bounded final write fence is declared and within policy",
		}) &&
		validation.ForwardCutoverRequirement == "no acknowledged writes may be lost before candidate authority" &&
		validation.RollbackClassificationRequired == rollbackGuarantee &&
		validation.UnsupportedCandidateFailure == "fail-closed-before-authority-change"
}

func databaseNamePattern(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for index, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || index > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	first := value[0]
	return first == '_' || first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z'
}

func validateDatabaseSensitiveValues(envelope map[string]string, input planner.DatabaseOperationInput) (string, string, error) {
	if len(envelope) != 1 {
		return "", "", errors.New("prepareDatabase requires exactly one resolved Secret Reference")
	}
	value, ok := envelope[input.CredentialReference]
	if !ok || value == "" {
		return "", "", errors.New("prepareDatabase lacks its resolved Database Secret Reference")
	}
	username, password, database, port, err := parseDatabaseSecret(value)
	if err != nil {
		return "", "", err
	}
	if database != input.DatabaseName || port != input.Port {
		return "", "", errors.New("resolved Database Secret Reference does not match the approved binding")
	}
	if username != input.DatabaseName {
		return "", "", errors.New("resolved Database Secret Reference user must match the approved database identity")
	}
	return username, password, nil
}

func parseDatabaseSecret(value string) (string, string, string, int, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "postgresql" || parsed.User == nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", 0, errors.New("resolved Database Secret Reference must contain a private PostgreSQL URL")
	}
	username := parsed.User.Username()
	password, ok := parsed.User.Password()
	port, portErr := strconv.Atoi(parsed.Port())
	if !ok || portErr != nil || parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" || parsed.Path == "" || strings.Contains(parsed.Path[1:], "/") {
		return "", "", "", 0, errors.New("resolved Database Secret Reference must target the planned loopback listener and database")
	}
	if !queueSecretPart.MatchString(username) || !queueSecretPart.MatchString(password) || len(password) < 16 || len(password) > 128 {
		return "", "", "", 0, errors.New("resolved Database Secret Reference has unsupported credentials")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if !databaseNamePattern(database) {
		return "", "", "", 0, errors.New("resolved Database Secret Reference has unsupported database identity")
	}
	return username, password, database, port, nil
}

func containerPostgreSQLURL(value string) (string, error) {
	username, password, database, _, err := parseDatabaseSecret(value)
	if err != nil {
		return "", err
	}
	translated := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(username, password),
		Host:   "127.0.0.1:5432",
		Path:   "/" + database,
	}
	return translated.String(), nil
}

func observeDatabaseOperation(ctx context.Context, planID string, planned planner.Operation, record bootstrapRecord, paths executionPaths) (host.OperationObservation, error) {
	if err := validateDatabaseOperation(planned, record, paths); err != nil {
		return host.OperationObservation{}, err
	}
	operationDigest, err := planner.OperationDigest(planned)
	if err != nil {
		return host.OperationObservation{}, err
	}
	observed, state := observeManagedDatabase(ctx, *planned.Input.Database, record, paths, planID, operationDigest, "")
	evidence, err := json.Marshal(observed)
	if err != nil {
		return host.OperationObservation{}, err
	}
	return host.OperationObservation{State: state, Evidence: evidence}, nil
}

func applyDatabaseOperation(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths, sensitiveValues map[string]string) (json.RawMessage, error) {
	if err := validateDatabaseOperation(planned, record, paths); err != nil {
		return nil, err
	}
	operationDigest, err := planner.OperationDigest(planned)
	if err != nil {
		return nil, err
	}
	input := *planned.Input.Database
	secret, username, err := databaseSecretForOperation(sensitiveValues, input)
	if err != nil {
		return nil, err
	}
	if observed, state := observeManagedDatabase(ctx, input, record, paths, claim.PlanID, operationDigest, secret); state == "satisfied" {
		encoded, err := json.Marshal(observed)
		return encoded, err
	} else if state == "unknown" {
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New("existing Database resources do not match the approved generation")
	}
	uid, gid := accountUID(record.Account), accountGID(record.Account)
	if uid <= 0 || gid <= 0 {
		return nil, errors.New("Environment account identity is unavailable")
	}
	serviceRoot := filepath.Dir(filepath.Dir(filepath.Dir(input.DataPath)))
	generationRoot := filepath.Dir(input.DataPath)
	runtimeHome := "/var/lib/provision/runtime/" + record.Environment
	credentialDir := filepath.Join(runtimeHome, ".config", "credstore.encrypted")
	for _, directory := range []struct {
		path string
		mode os.FileMode
		uid  int
		gid  int
	}{{serviceRoot, 0755, 0, 0}, {generationRoot, 0755, 0, 0}, {credentialDir, 0700, uid, gid}, {filepath.Dir(input.QuadletPath), 0755, 0, 0}} {
		if err := ensureDirectory(directory.path, directory.mode, directory.uid, directory.gid); err != nil {
			return nil, err
		}
	}
	if err := ensureEnvironmentDataDirectory(input.DataPath, record.Account, uid, gid); err != nil {
		return nil, err
	}
	credentialPath := filepath.Join(credentialDir, postgresqlCredentialName)
	if err := installUserEncryptedCredential(ctx, credentialPath, postgresqlCredentialName, uid, gid, secret+"\n"); err != nil {
		return nil, errors.New("encrypt Database credential")
	}
	entrypointPath := filepath.Join(serviceRoot, "credential-entrypoint")
	if err := installExactFile(entrypointPath, []byte(databaseCredentialEntrypoint()), 0755, 0, 0); err != nil {
		return nil, err
	}
	if err := installExactFile(input.QuadletPath, []byte(renderDatabaseQuadlet(input, entrypointPath, username)), 0644, 0, 0); err != nil {
		return nil, err
	}
	if _, err := runAsEnvironment(ctx, record, "podman", "pull", input.ImageReference); err != nil {
		return nil, errors.New("pull approved PostgreSQL image")
	}
	if _, err := runAsEnvironment(ctx, record, "podman", "image", "inspect", input.ImageReference); err != nil {
		return nil, errors.New("verify approved PostgreSQL image")
	}
	if _, err := runAsEnvironment(ctx, record, "podman", "unshare", "chown", "-R", "999:999", input.DataPath); err != nil {
		return nil, errors.New("own PostgreSQL data path inside the rootless user namespace")
	}
	if _, err := runAsEnvironment(ctx, record, "podman", "unshare", "chmod", "0700", input.DataPath); err != nil {
		return nil, errors.New("secure PostgreSQL data path inside the rootless user namespace")
	}
	if _, err := runAsEnvironment(ctx, record, "systemctl", "--user", "daemon-reload"); err != nil {
		return nil, errors.New("reload Environment user units")
	}
	if _, err := runAsEnvironment(ctx, record, "systemctl", "--user", "restart", input.ServiceUnit); err != nil {
		return nil, errors.New("start managed PostgreSQL service")
	}
	if err := waitForDatabaseService(ctx, record, input); err != nil {
		return nil, err
	}
	observed, actionErr := verifyDatabaseGeneration(ctx, input, record, paths, username, secret)
	if actionErr != nil {
		encoded, _ := json.Marshal(observed)
		return encoded, actionErr
	}
	recordPath := filepath.Join(generationRoot, "generation.json")
	packaging := postgresqlPackagingRecord{
		SchemaVersion: postgresqlGenerationSchema, LogicalID: input.LogicalID, GenerationID: input.GenerationID,
		PostgreSQLVersion: input.PostgreSQLVersion, ImageIndex: input.ImageIndex, ImageManifest: input.ImageManifest,
		ImageReference: input.ImageReference, ServiceUnit: input.ServiceUnit, Container: input.Container, Account: input.Account,
		DataPath: input.DataPath, QuadletPath: input.QuadletPath, Database: input.DatabaseName, CredentialReference: input.CredentialReference,
		ListenAddress: input.ListenAddress, Port: input.Port,
		Connectivity: true, DurableRestart: false, Verified: true, PlanID: claim.PlanID, OperationDigest: operationDigest,
	}
	if err := writeJSONAtomic(recordPath, packaging, 0444); err != nil {
		return nil, errors.New("record Database generation")
	}
	observed, state := observeManagedDatabase(ctx, input, record, paths, claim.PlanID, operationDigest, secret)
	encoded, encodeErr := json.Marshal(observed)
	if encodeErr != nil {
		return nil, encodeErr
	}
	if state != "satisfied" {
		return encoded, errors.New("prepared Database failed exact verification")
	}
	secret = ""
	return encoded, nil
}

func databaseSecretForOperation(values map[string]string, input planner.DatabaseOperationInput) (string, string, error) {
	username, _, err := validateDatabaseSensitiveValues(values, input)
	if err != nil {
		return "", "", err
	}
	secret := values[input.CredentialReference]
	return secret, username, nil
}

func waitForDatabaseService(ctx context.Context, record bootstrapRecord, input planner.DatabaseOperationInput) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		active, _ := runAsEnvironment(ctx, record, "systemctl", "--user", "is-active", input.ServiceUnit)
		health, _ := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.State.Health.Status}}", input.Container)
		if strings.TrimSpace(string(active)) == "active" && strings.TrimSpace(string(health)) == "healthy" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("managed PostgreSQL service did not become healthy")
}

func verifyDatabaseGeneration(ctx context.Context, input planner.DatabaseOperationInput, record bootstrapRecord, paths executionPaths, username, credentialURL string) (host.DatabaseOperationObservation, error) {
	observed := databaseOperationIdentity(input)
	status, statusErr := runAsEnvironment(ctx, record, "systemctl", "--user", "is-active", input.ServiceUnit)
	health, healthErr := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.State.Health.Status}}", input.Container)
	manifest, manifestErr := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.ImageDigest}}", input.Container)
	binding, bindingErr := runAsEnvironment(ctx, record, "podman", "port", input.Container, "5432/tcp")
	credentialMount, credentialErr := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{range .}}{{range .Mounts}}{{if eq .Destination \"/run/provision-credential/postgresql-url\"}}{{.RW}}{{end}}{{end}}{{end}}", input.Container)
	containerURL, urlErr := containerPostgreSQLURL(credentialURL)
	version, versionErr := runAsEnvironment(ctx, record, "podman", "exec", input.Container, "psql", containerURL, "-Atc", "SHOW server_version;")
	identity, identityErr := runAsEnvironment(ctx, record, "podman", "exec", input.Container, "psql", containerURL, "-Atc", "SELECT current_database() || ':' || current_user;")
	observed.Checks.ServiceHealth = statusErr == nil && healthErr == nil && strings.TrimSpace(string(status)) == "active" && strings.TrimSpace(string(health)) == "healthy"
	observed.Checks.SQLConnectivity = urlErr == nil && versionErr == nil && strings.HasPrefix(strings.TrimSpace(string(version)), input.PostgreSQLVersion)
	observed.Checks.DatabaseIdentity = urlErr == nil && identityErr == nil && strings.TrimSpace(string(identity)) == input.DatabaseName+":"+username
	observed.Checks.GenerationIdentity = manifestErr == nil && strings.TrimSpace(string(manifest)) == input.ImageManifest && bindingErr == nil && strings.TrimSpace(string(binding)) == fmt.Sprintf("127.0.0.1:%d", input.Port)
	observed.Checks.CredentialBoundary = credentialErr == nil && strings.TrimSpace(string(credentialMount)) == "false"
	observed.Verified = observed.Checks.ServiceHealth && observed.Checks.SQLConnectivity && observed.Checks.DatabaseIdentity && observed.Checks.GenerationIdentity && observed.Checks.CredentialBoundary
	if observed.Verified {
		observed.Status = "verified"
		observed.Database.Ready = true
		observed.Database.Connectivity = true
		observed.Database.Health = "healthy"
		return observed, nil
	}
	observed.Status = "failed"
	observed.Database.Health = "failed"
	observed.FailureCategory, observed.Reason = databaseVerificationFailure(observed.Checks)
	observed.Database.Reason = observed.Reason
	observed.RecoveryAction = "inspect the Environment user journal, PostgreSQL container health, credential boundary, and SQL identity before retrying"
	observed.Database.RecoveryAction = observed.RecoveryAction
	return observed, errors.New(observed.Reason)
}

func databaseVerificationFailure(checks host.DatabaseVerificationChecks) (string, string) {
	switch {
	case !checks.ServiceHealth:
		return "service", "PostgreSQL service health verification failed"
	case !checks.SQLConnectivity:
		return "connectivity", "PostgreSQL SQL connectivity or product version verification failed"
	case !checks.DatabaseIdentity:
		return "verification", "PostgreSQL database identity verification failed"
	case !checks.GenerationIdentity:
		return "packaging", "PostgreSQL generation identity or loopback binding verification failed"
	case !checks.CredentialBoundary:
		return "credential", "PostgreSQL credential boundary verification failed"
	default:
		return "verification", "PostgreSQL verification failed"
	}
}

func observeManagedDatabase(ctx context.Context, input planner.DatabaseOperationInput, record bootstrapRecord, paths executionPaths, planID, operationDigest, credentialURL string) (host.DatabaseOperationObservation, string) {
	observed := databaseOperationIdentity(input)
	recordPath := filepath.Join(filepath.Dir(input.DataPath), "generation.json")
	data, err := os.ReadFile(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		observed.Status = "absent"
		return observed, "pending"
	}
	if err != nil {
		observed.Status = "unknown"
		observed.FailureCategory = "observation"
		observed.Reason = "read Database generation record"
		observed.Database.Reason = observed.Reason
		return observed, "unknown"
	}
	var generation postgresqlPackagingRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&generation) != nil || generation.SchemaVersion != postgresqlGenerationSchema || !databaseRecordMatchesInput(generation, input) {
		observed.Status = "drifted"
		observed.FailureCategory = "observation"
		observed.Reason = "Database generation record differs from the approved generation"
		observed.Database.Reason = observed.Reason
		return observed, "unknown"
	}
	if (planID != "" && generation.PlanID != planID) || (operationDigest != "" && generation.OperationDigest != operationDigest) {
		observed.Status = "drifted"
		observed.FailureCategory = "authorization"
		observed.Reason = "Database generation record is not bound to the approved Plan and operation"
		observed.Database.Reason = observed.Reason
		return observed, "unknown"
	}
	serviceRoot := filepath.Dir(filepath.Dir(filepath.Dir(input.DataPath)))
	entrypointPath := filepath.Join(serviceRoot, "credential-entrypoint")
	entrypoint, entrypointErr := os.ReadFile(entrypointPath)
	quadlet, quadletErr := os.ReadFile(input.QuadletPath)
	uid := accountUID(record.Account)
	credentialPath := filepath.Join("/var/lib/provision/runtime", record.Environment, ".config", "credstore.encrypted", postgresqlCredentialName)
	if !rootOwned(recordPath, 0444) || entrypointErr != nil || string(entrypoint) != databaseCredentialEntrypoint() || quadletErr != nil || string(quadlet) != renderDatabaseQuadlet(input, entrypointPath, databaseUserFromRecord(generation)) || uid <= 0 || !ownedBy(credentialPath, uid, 0600) || !environmentDataOwned(input.DataPath, record.Account, uid, accountGID(record.Account)) {
		observed.Status = "drifted"
		observed.FailureCategory = "observation"
		observed.Reason = "Database runtime files, credentials, or data ownership differ from the approved generation"
		observed.Database.Reason = observed.Reason
		return observed, "unknown"
	}
	username := databaseUserFromRecord(generation)
	verified, err := verifyDatabaseGeneration(ctx, input, record, paths, username, credentialURL)
	if err != nil {
		return verified, "unknown"
	}
	if !generation.Connectivity {
		verified.Status = "degraded"
		verified.Database.Health = "degraded"
		verified.FailureCategory = "verification"
		verified.Reason = "Database generation record has not recorded successful connectivity"
		verified.Database.Reason = verified.Reason
		return verified, "unknown"
	}
	verified.Database.DurableRestart = generation.DurableRestart
	if !generation.DurableRestart {
		verified.Database.Ready = true
		verified.Database.Health = "healthy"
	}
	return verified, "satisfied"
}

func databaseFailureCategory(reason string) string {
	switch {
	case strings.Contains(reason, "Secret Reference"), strings.Contains(reason, "credential"):
		return "credential"
	case strings.Contains(reason, "image"), strings.Contains(reason, "packaging"), strings.Contains(reason, "data path inside the rootless user namespace"):
		return "packaging"
	case strings.Contains(reason, "service"), strings.Contains(reason, "user units"):
		return "service"
	case strings.Contains(reason, "connectivity"), strings.Contains(reason, "SQL"):
		return "connectivity"
	case strings.Contains(reason, "authorization"), strings.Contains(reason, "approved Plan"):
		return "authorization"
	case strings.Contains(reason, "observe"), strings.Contains(reason, "record"), strings.Contains(reason, "resources"):
		return "observation"
	default:
		return "verification"
	}
}

func databaseRecordMatchesInput(record postgresqlPackagingRecord, input planner.DatabaseOperationInput) bool {
	return record.LogicalID == input.LogicalID && record.GenerationID == input.GenerationID && record.PostgreSQLVersion == input.PostgreSQLVersion && record.ImageIndex == input.ImageIndex && record.ImageManifest == input.ImageManifest && record.ImageReference == input.ImageReference && record.ServiceUnit == input.ServiceUnit && record.Container == input.Container && record.Account == input.Account && record.DataPath == input.DataPath && record.QuadletPath == input.QuadletPath && record.Database == input.DatabaseName && record.CredentialReference == input.CredentialReference && record.ListenAddress == input.ListenAddress && record.Port == input.Port
}

func databaseOperationIdentity(input planner.DatabaseOperationInput) host.DatabaseOperationObservation {
	return host.DatabaseOperationObservation{Status: "pending", Database: host.DatabaseGenerationStatus{
		ID: input.GenerationID, LogicalID: input.LogicalID, Role: "candidate", Authority: "pending", PostgreSQLVersion: input.PostgreSQLVersion, ImageManifest: input.ImageManifest,
		ServiceUnit: input.ServiceUnit, Container: input.Container, Account: input.Account, DataPath: input.DataPath, QuadletPath: input.QuadletPath,
		Database: input.DatabaseName, SupportedGuarantees: databaseSupportedGuarantees(), OwnedResources: databaseOwnedResources(postgresqlPackagingRecord{
			LogicalID: input.LogicalID, GenerationID: input.GenerationID, ServiceUnit: input.ServiceUnit, Container: input.Container, Account: input.Account,
			DataPath: input.DataPath, QuadletPath: input.QuadletPath, Database: input.DatabaseName, CredentialReference: input.CredentialReference,
		}),
	}}
}

func databaseSupportedGuarantees() []string {
	return []string{"pinned-postgresql-image", "encrypted-systemd-credential", "loopback-only-listener", "sql-identity-verification"}
}

func databaseOwnedResources(record postgresqlPackagingRecord) []string {
	environment := strings.TrimPrefix(record.CredentialReference, "secret://")
	if separator := strings.IndexByte(environment, '/'); separator >= 0 {
		environment = environment[:separator]
	}
	serviceRoot := filepath.Dir(filepath.Dir(filepath.Dir(record.DataPath)))
	return []string{
		"systemd-unit:" + record.ServiceUnit,
		"container:" + record.Container,
		"data-path:" + record.DataPath,
		"quadlet:" + record.QuadletPath,
		"generation-record:" + filepath.Join(filepath.Dir(record.DataPath), "generation.json"),
		"credential-entrypoint:" + filepath.Join(serviceRoot, "credential-entrypoint"),
		"encrypted-credential:" + filepath.Join("/var/lib/provision/runtime", environment, ".config", "credstore.encrypted", postgresqlCredentialName),
		"postgresql-database:" + record.Database,
	}
}

func databaseCredentialEntrypoint() string {
	return "#!/usr/bin/env bash\nset -euo pipefail\nurl=\"$(cat /run/provision-credential/postgresql-url)\"\ncase \"$url\" in\n  postgresql://*@127.0.0.1:*/*|postgresql://*@localhost:*/*) ;;\n  *) echo \"unsupported credential URL\" >&2; exit 1 ;;\nesac\ncredential_rest=\"${url#postgresql://}\"\ncredential_pair=\"${credential_rest%%@*}\"\nhost_and_path=\"${credential_rest#*@}\"\ndatabase_name=\"${host_and_path#*/}\"\nexport POSTGRES_USER=\"${credential_pair%%:*}\"\nexport POSTGRES_PASSWORD=\"${credential_pair#*:}\"\nexport POSTGRES_DB=\"${database_name%%\\?*}\"\n[[ -n \"$POSTGRES_USER\" && -n \"$POSTGRES_PASSWORD\" && -n \"$POSTGRES_DB\" ]] || { echo \"incomplete PostgreSQL credential URL\" >&2; exit 1; }\nexec docker-entrypoint.sh \"$@\"\n"
}

func renderDatabaseQuadlet(input planner.DatabaseOperationInput, entrypoint, username string) string {
	return fmt.Sprintf(`[Unit]
Description=Provision managed PostgreSQL Database generation %s
Wants=network-online.target
After=network-online.target

[Container]
Image=%s
ContainerName=%s
PublishPort=127.0.0.1:%d:5432
Volume=%s:/var/lib/postgresql/data
Volume=%%d/%s:/run/provision-credential/postgresql-url:ro
Volume=%s:/usr/local/bin/provision-postgresql-credential-entrypoint:ro
Entrypoint=/usr/local/bin/provision-postgresql-credential-entrypoint
Exec=postgres
Environment=PGDATA=/var/lib/postgresql/data/pgdata
HealthCmd=pg_isready -U %s -d %s
HealthInterval=5s
HealthRetries=24
NoNewPrivileges=true

[Service]
LoadCredentialEncrypted=%s
Restart=always
TimeoutStartSec=900

[Install]
WantedBy=default.target
`, input.GenerationID, input.ImageReference, input.Container, input.Port, input.DataPath, postgresqlCredentialName, entrypoint, username, input.DatabaseName, postgresqlCredentialName)
}

func databaseUserFromRecord(record postgresqlPackagingRecord) string {
	return record.Database
}
