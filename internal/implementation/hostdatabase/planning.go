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
			"candidate-store-generation-inspection",
			"postgresql-transition-compatibility-gates",
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
	if capability.StoreRollbackGuarantee != "forward-only" ||
		capability.ForwardCutoverGuarantee != "lossless-after-bounded-write-fence" ||
		capability.SupportedTransitionMechanism != "offline-logical-snapshot-with-bounded-write-fence" {
		reasons = append(reasons, "PostgreSQL Store Transition capability is not the qualified offline logical snapshot with bounded write fence")
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
	Candidate                *planmodel.DatabaseOperationInput
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
		TransitionValidation:    databaseTransitionValidation(component, observation.Database.Deployment),
		Binding: planmodel.DatabaseBindingInput{
			Reference: implementation.Credential, Protocol: "postgresql", Host: capability.PostgreSQLListenAddress, Port: capability.PostgreSQLPort, Database: component.Database.DatabaseName,
		},
		Consequences: databaseConsequences(compiled, component, capability, observation.Database.Deployment),
		Observed:     observation.Database.Deployment,
	}
	return PlanningOutput{SensitiveValueReferences: []string{implementation.Credential}, Database: input, Candidate: databaseCandidateInput(input)}
}

func databaseCandidateInput(input *planmodel.DatabaseOperationInput) *planmodel.DatabaseOperationInput {
	candidate := *input
	candidate.ServiceUnit = strings.TrimSuffix(input.ServiceUnit, ".service") + "-candidate.service"
	candidate.Container = input.Container + "-candidate"
	candidate.Port = input.Port + 1
	candidate.QuadletPath = strings.TrimSuffix(input.QuadletPath, ".container") + "-candidate.container"
	candidate.Binding.Port = candidate.Port
	return &candidate
}

func databaseTransitionValidation(component config.Component, deployment host.DatabaseDeploymentStatus) planmodel.DatabaseTransitionValidation {
	return planmodel.DatabaseTransitionValidation{
		RequiredStoreRollbackGuarantee: component.Database.StoreRollbackGuarantee,
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
		RollbackClassificationRequired: component.Database.StoreRollbackGuarantee,
		UnsupportedCandidateFailure:    unsupportedCandidateSummary(deployment.Candidate),
	}
}

func databaseConsequences(compiled config.Compiled, component config.Component, capability host.DatabaseCapabilities, deployment host.DatabaseDeploymentStatus) planmodel.DatabaseStoreConsequences {
	previous := "none"
	synchronization := "not-required-without-active-generation"
	cutover := "initial-authority-after-verification"
	applicability := "initial-generation-or-qualified-forward-only-transition"
	if deployment.Active != nil {
		previous = deployment.Active.ID
		if deployment.Active.ID == capability.PostgreSQLGeneration {
			previous = "none"
		} else if compatibleActiveGeneration(deployment.Active, capability) && capability.SupportedTransitionMechanism == "offline-logical-snapshot-with-bounded-write-fence" {
			synchronization = "offline-logical-snapshot-after-bounded-write-fence"
			cutover = "lossless-forward-authority-switch-after-write-fence"
			applicability = "qualified-forward-only-store-transition"
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
		Synchronization:         synchronization,
		Cutover:                 cutover,
		Retention:               component.Database.TransitionCleanupPolicy,
		StoreRollbackGuarantee:  component.Database.StoreRollbackGuarantee,
		RollbackWindow:          compiled.Environment.RollbackWindow,
		TransitionApplicability: applicability,
	}
}

func ReplacementReason(compiled config.Compiled, database host.DatabaseStatus) string {
	_, _, implementation := databaseComponent(compiled)
	if implementation.Rollout != "required" || database.Deployment.Active == nil {
		if reason := unsafeCandidateReason(database.Deployment.Candidate); reason != "" {
			return reason
		}
		return ""
	}
	if reason := unsafeCandidateReason(database.Deployment.Candidate); reason != "" {
		return reason
	}
	active := database.Deployment.Active
	if active.ID == database.Capabilities.PostgreSQLGeneration && active.Ready && active.Connectivity && active.ImageManifest == database.Capabilities.PostgreSQLImageManifest {
		return ""
	}
	if compatibleActiveGeneration(active, database.Capabilities) && database.Capabilities.SupportedTransitionMechanism == "offline-logical-snapshot-with-bounded-write-fence" {
		return ""
	}
	return "required Database Store Transition is not qualified for isolated candidate generation, synchronization, verification, cutover, and retention"
}

func compatibleActiveGeneration(active *host.DatabaseGenerationStatus, capability host.DatabaseCapabilities) bool {
	return active != nil &&
		active.Ready &&
		active.Connectivity &&
		active.PostgreSQLVersion == capability.PostgreSQLVersion &&
		active.ImageManifest == capability.PostgreSQLImageManifest &&
		active.Database != "" &&
		active.LogicalID != ""
}

func unsafeCandidateReason(candidate *host.DatabaseGenerationStatus) string {
	if candidate == nil {
		return ""
	}
	if candidate.CompatibilityGate != "" {
		return fmt.Sprintf("candidate Database generation %s failed PostgreSQL compatibility gate %q: %s", candidate.ID, candidate.CompatibilityGate, candidateReason(candidate))
	}
	return fmt.Sprintf("candidate Database generation %s is not verified safe for required Store Transition: %s", candidate.ID, candidateReason(candidate))
}

func unsupportedCandidateSummary(candidate *host.DatabaseGenerationStatus) string {
	if candidate == nil {
		return "fail-closed-before-authority-change"
	}
	if candidate.CompatibilityGate != "" {
		return "fail-closed-before-authority-change:" + candidate.CompatibilityGate
	}
	return "fail-closed-before-authority-change"
}

func candidateReason(candidate *host.DatabaseGenerationStatus) string {
	if candidate == nil {
		return "no candidate observed"
	}
	if candidate.Reason != "" {
		return candidate.Reason
	}
	if candidate.Health != "" {
		return "candidate health is " + candidate.Health
	}
	return "candidate compatibility is unverified"
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
