package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"provision/internal/host"
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
	Connectivity        bool   `json:"connectivity"`
	DurableRestart      bool   `json:"durableRestart"`
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
	active := &host.DatabaseGenerationStatus{
		ID: record.GenerationID, LogicalID: record.LogicalID, PostgreSQLVersion: record.PostgreSQLVersion,
		ImageManifest: record.ImageManifest, ServiceUnit: record.ServiceUnit, Container: record.Container,
		Account: record.Account, DataPath: record.DataPath, QuadletPath: record.QuadletPath,
		Database: record.Database, Connectivity: record.Connectivity, DurableRestart: record.DurableRestart,
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
		active.Reason = "recorded PostgreSQL generation differs from qualified packaging capability"
		active.RecoveryAction = "re-run the packaging qualification on a clean disposable Host"
		findings = append(findings, active.Reason)
		return host.DatabaseDeploymentStatus{Active: active}, findings
	}
	if command("systemctl", "is-active", "user@"+strconv.Itoa(accountUID(record.Account))+".service") != "active" {
		active.Reason = "Environment user manager is not active"
		active.RecoveryAction = "restore or restart the Environment user manager before planning Database lifecycle work"
		findings = append(findings, active.Reason)
		return host.DatabaseDeploymentStatus{Active: active}, findings
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, err := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "systemctl", "--user", "is-active", record.ServiceUnit)
	if err != nil || strings.TrimSpace(string(status)) != "active" {
		active.Reason = "PostgreSQL service is not active"
		active.RecoveryAction = "inspect the Environment user journal and the PostgreSQL container before planning Database lifecycle work"
		findings = append(findings, active.Reason)
		return host.DatabaseDeploymentStatus{Active: active}, findings
	}
	version, err := runAsEnvironment(ctx, bootstrapRecord{Environment: strings.TrimPrefix(record.Account, "provision-"), Account: record.Account}, "podman", "exec", record.Container, "psql", "-U", "provision_issue68", "-d", record.Database, "-Atc", "SHOW server_version;")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(version)), qualifiedPostgreSQL) {
		active.Reason = "PostgreSQL connectivity or product version cannot be verified"
		active.RecoveryAction = "re-run the packaging proof or inspect the credential and container state"
		findings = append(findings, active.Reason)
		return host.DatabaseDeploymentStatus{Active: active}, findings
	}
	active.Ready = active.Connectivity && active.DurableRestart
	if !active.Ready {
		active.Reason = "PostgreSQL generation has not completed connectivity and reboot qualification"
		active.RecoveryAction = "run the post-reboot packaging verification"
		findings = append(findings, active.Reason)
	}
	return host.DatabaseDeploymentStatus{Active: active}, findings
}
