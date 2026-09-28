package hostdatabase

import (
	"fmt"
	"strings"

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planmodel"
)

const (
	Contract            = "host-postgresql-systemd-database/v1alpha1"
	QualificationDigest = "sha256:892fb587ac7323ba4f04d38b1fc165f9e2304219f3de14e7e365cb60b4960eff"
	PostgreSQLVersion   = "17.6"
	ImageIndex          = "sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929"
	ImageManifest       = "sha256:b86568d3e0fe1dfaeff52714f9da36f206a30e4c49131b82bf96982d78627409"
	ImageReference      = "docker.io/library/postgres@" + ImageManifest
)

type Evaluation struct {
	Contract        string
	Guarantees      []string
	SupportEvidence []string
	Reasons         []string
}

func Evaluate(compiled config.Compiled, observation host.BootstrapStatus, target config.TargetSelection) Evaluation {
	reasons := append([]string(nil), observation.Findings...)
	evaluation := Evaluation{
		Contract: Contract,
		Guarantees: []string{
			"initial-store-generation-plan-preview",
			"host-capability-bound-plan-identity",
			"secret-reference-only-plan",
			"rollback-classification-exposed-separately-from-retention",
		},
	}
	if observation.SchemaVersion != "provision.dev/host-inspection/v1alpha1" {
		reasons = append(reasons, "host inspection schema does not provide Database capability evidence")
	}
	if !observation.Ready {
		reasons = append(reasons, "restricted Host Target bootstrap is not ready")
	}
	if observation.OS != "ubuntu" || observation.OSVersion != "26.04" || observation.Architecture != "x86_64" {
		reasons = append(reasons, "PostgreSQL host packaging is qualified only on Ubuntu 26.04 x86_64")
	}
	if !strings.HasPrefix(observation.SystemdVersion, "systemd 259") {
		reasons = append(reasons, "systemd 259 capability evidence is required for managed PostgreSQL")
	}
	if observation.ExecutorDigest == "" {
		reasons = append(reasons, "restricted host executor identity is not observed")
	}
	if observation.AuthorityKeyID == "" {
		reasons = append(reasons, "host authorization authority is not observed")
	}
	if !target.Target.Local && observation.SSHHostKeyFingerprint == "" {
		reasons = append(reasons, "SSH host key identity is not observed")
	}
	database := observation.Database
	if database == nil {
		reasons = append(reasons, "Database capability observation is absent")
		evaluation.Reasons = unique(reasons)
		return evaluation
	}
	if !database.ObservationComplete {
		reasons = append(reasons, "Database deployment observation is incomplete")
	}
	reasons = append(reasons, database.Findings...)
	capability := database.Capabilities
	if capability.PostgreSQLQualificationDigest != QualificationDigest {
		reasons = append(reasons, fmt.Sprintf("PostgreSQL qualification digest %s is not supported", observedOrUnknown(capability.PostgreSQLQualificationDigest)))
	}
	if capability.PostgreSQLVersion != PostgreSQLVersion || capability.PostgreSQLImageIndex != ImageIndex || capability.PostgreSQLImageManifest != ImageManifest || capability.PostgreSQLImageReference != ImageReference {
		reasons = append(reasons, "PostgreSQL packaging identity does not match the qualified image, version, index, and manifest")
	}
	if capability.PodmanVersion != "5.7.0+ds2-3build1" {
		reasons = append(reasons, fmt.Sprintf("Podman version %s has no tested PostgreSQL packaging evidence", observedOrUnknown(capability.PodmanVersion)))
	}
	if !capability.Quadlet || !capability.RootlessEnvironmentAccount || !capability.SystemdCredentials || !capability.SubordinateIDs || !capability.LingeringUserManager {
		reasons = append(reasons, "rootless PostgreSQL account, Quadlet, systemd credential, subordinate ID, or lingering evidence is incomplete")
	}
	if !capability.EncryptedCredentialObserved {
		reasons = append(reasons, "PostgreSQL encrypted credential delivery is not observed")
	}
	if !capability.QuadletDefinitionRootOwned || !capability.GenerationDataPathOwned {
		reasons = append(reasons, "PostgreSQL Quadlet definition and generation data path ownership are not qualified")
	}
	if capability.PostgreSQLServiceUnit == "" || capability.PostgreSQLContainer == "" || capability.PostgreSQLAccount == "" || capability.PostgreSQLGeneration == "" || capability.PostgreSQLGenerationDataPath == "" || capability.PostgreSQLQuadletPath == "" || capability.PostgreSQLListenAddress == "" || capability.PostgreSQLPort == 0 {
		reasons = append(reasons, "PostgreSQL service, generation, container, account, data path, Quadlet identity, or listener binding is incomplete")
	}
	if capability.StoreRollbackGuarantee != "not-qualified-by-packaging-proof" || capability.ForwardCutoverGuarantee != "not-qualified-by-packaging-proof" || capability.SupportedTransitionMechanism != "none-qualified-by-packaging-proof" {
		reasons = append(reasons, "PostgreSQL Store Transition capability is not honestly classified as unqualified forward-only packaging")
	}
	_, _, implementation := databaseComponent(compiled)
	if implementation.Credential != "" && capability.PostgreSQLCredentialReference != implementation.Credential {
		reasons = append(reasons, "PostgreSQL credential Secret Reference does not match the requested Database implementation")
	}
	evaluation.Reasons = unique(reasons)
	if len(evaluation.Reasons) == 0 {
		evaluation.SupportEvidence = []string{
			"restricted-bootstrap-ready",
			"ubuntu-26.04-x86_64",
			"rootless-podman-quadlet",
			"systemd-credentials",
			"encrypted-credential-observed",
			"postgresql-17.6-image-index-and-manifest-pinned",
			"generation-data-path-owned",
			"durable-reboot-packaging-proof",
			"restricted-executor-identity-matched",
		}
	}
	return evaluation
}

type PlanningOutput struct {
	SensitiveValueReferences []string
	Database                 *planmodel.DatabaseOperationInput
}

func Plan(compiled config.Compiled, observation host.BootstrapStatus) PlanningOutput {
	componentName, component, implementation := databaseComponent(compiled)
	capability := observation.Database.Capabilities
	logicalID := "provision-" + compiled.Environment.Name + "-" + componentName
	input := &planmodel.DatabaseOperationInput{
		Component: componentName, LogicalID: logicalID, GenerationID: capability.PostgreSQLGeneration,
		Implementation: implementation.Kind, Lifecycle: implementation.Lifecycle, Rollout: implementation.Rollout,
		CredentialReference:     implementation.Credential,
		DatabaseName:            component.Database.DatabaseName,
		DataRole:                component.Database.DataRole,
		PostgreSQLVersion:       capability.PostgreSQLVersion,
		ImageIndex:              capability.PostgreSQLImageIndex,
		ImageManifest:           capability.PostgreSQLImageManifest,
		ImageReference:          capability.PostgreSQLImageReference,
		ServiceUnit:             capability.PostgreSQLServiceUnit,
		Container:               capability.PostgreSQLContainer,
		Account:                 capability.PostgreSQLAccount,
		DataPath:                capability.PostgreSQLGenerationDataPath,
		QuadletPath:             capability.PostgreSQLQuadletPath,
		ListenAddress:           capability.PostgreSQLListenAddress,
		Port:                    capability.PostgreSQLPort,
		StoreRollbackGuarantee:  component.Database.StoreRollbackGuarantee,
		ForwardCutoverGuarantee: capability.ForwardCutoverGuarantee,
		TransitionMechanism:     capability.SupportedTransitionMechanism,
		TransitionCleanupPolicy: component.Database.TransitionCleanupPolicy,
		RollbackWindow:          compiled.Environment.RollbackWindow,
		Backup:                  component.Database.Backup,
		Recovery:                component.Database.Recovery,
		Binding: planmodel.DatabaseBindingInput{
			Reference: implementation.Credential, Protocol: "postgresql", Host: capability.PostgreSQLListenAddress, Port: capability.PostgreSQLPort, Database: component.Database.DatabaseName,
		},
		Consequences: databaseConsequences(compiled, component, capability, observation.Database.Deployment),
		Observed:     observation.Database.Deployment,
	}
	return PlanningOutput{SensitiveValueReferences: []string{implementation.Credential}, Database: input}
}

func databaseConsequences(compiled config.Compiled, component config.Component, capability host.DatabaseCapabilities, deployment host.DatabaseDeploymentStatus) planmodel.DatabaseStoreConsequences {
	previous := "none"
	if deployment.Active != nil {
		previous = deployment.Active.ID
		if deployment.Active.ID == capability.PostgreSQLGeneration {
			previous = "none"
		}
	}
	retained := make([]string, 0, len(deployment.Retained))
	for _, generation := range deployment.Retained {
		retained = append(retained, generation.ID)
	}
	return planmodel.DatabaseStoreConsequences{
		ActiveGeneration:        capability.PostgreSQLGeneration,
		CandidateGeneration:     capability.PostgreSQLGeneration,
		PreviousGeneration:      previous,
		RetainedGenerations:     retained,
		Synchronization:         "not-required-without-active-generation",
		Cutover:                 "initial-authority-after-verification",
		Retention:               component.Database.TransitionCleanupPolicy,
		StoreRollbackGuarantee:  component.Database.StoreRollbackGuarantee,
		RollbackWindow:          compiled.Environment.RollbackWindow,
		TransitionApplicability: "initial-generation-only; replacement-store-transition-fails-closed-until-qualified",
	}
}

func ReplacementReason(compiled config.Compiled, database host.DatabaseStatus) string {
	_, _, implementation := databaseComponent(compiled)
	if implementation.Rollout != "required" || database.Deployment.Active == nil {
		return ""
	}
	active := database.Deployment.Active
	if active.ID == database.Capabilities.PostgreSQLGeneration && active.Ready && active.Connectivity && active.ImageManifest == database.Capabilities.PostgreSQLImageManifest {
		return ""
	}
	if database.Capabilities.SupportedTransitionMechanism != "none-qualified-by-packaging-proof" {
		return ""
	}
	return "required Database Store Transition is not qualified for isolated candidate generation, synchronization, verification, cutover, and retention"
}

func DecisionObservation(observation host.BootstrapStatus) host.BootstrapStatus {
	return observation
}

func databaseComponent(compiled config.Compiled) (string, config.Component, config.Implementation) {
	for name, component := range compiled.Application.Components {
		if component.Role == "database" {
			return name, component, compiled.Environment.Implementations[name]
		}
	}
	return "", config.Component{}, config.Implementation{}
}

func conditions(kind, subject, expected string) []planmodel.TypedCondition {
	return []planmodel.TypedCondition{{Kind: kind, Subject: subject, Expected: expected}}
}

func observedOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
