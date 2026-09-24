package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"provision/internal/config"
	"provision/internal/host"
)

const (
	SchemaVersion        = "provision.dev/plan/v1alpha1"
	PreviewSchemaVersion = "provision.dev/plan-preview/v1alpha1"
)

type Preview struct {
	SchemaVersion string             `json:"schemaVersion"`
	Executable    bool               `json:"executable"`
	Plan          *Plan              `json:"plan,omitempty"`
	Capability    CapabilityEvidence `json:"capability"`
	Reasons       []string           `json:"reasons"`
}

type Plan struct {
	SchemaVersion            string                `json:"schemaVersion"`
	ID                       string                `json:"id"`
	Application              string                `json:"application"`
	Environment              string                `json:"environment"`
	Revision                 string                `json:"revision"`
	ConfigurationDigest      string                `json:"configurationDigest"`
	ArtifactDigests          map[string]string     `json:"artifactDigests"`
	Target                   Target                `json:"target"`
	ObservationDigest        string                `json:"observationDigest"`
	Capability               CapabilityEvidence    `json:"capability"`
	ApprovalRequirements     []ApprovalRequirement `json:"approvalRequirements"`
	SensitiveValueReferences []string              `json:"sensitiveValueReferences"`
	Operations               []Operation           `json:"operations"`
}

type Target struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Local   bool   `json:"local,omitempty"`
	Address string `json:"address"`
	User    string `json:"user"`
}

type CapabilityEvidence struct {
	Contract        string               `json:"contract"`
	RequiredMode    string               `json:"requiredMode"`
	Guarantees      []string             `json:"guarantees"`
	SupportEvidence []string             `json:"supportEvidence"`
	Observed        host.BootstrapStatus `json:"observed"`
	Decision        string               `json:"decision"`
}

type ApprovalRequirement struct {
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type OperationKind string

const (
	StageArtifact     OperationKind = "stageArtifact"
	InstallGeneration OperationKind = "installGeneration"
	StartCandidate    OperationKind = "startCandidate"
	VerifyCandidate   OperationKind = "verifyCandidate"
	SwitchEndpoint    OperationKind = "switchEndpoint"
	VerifyActive      OperationKind = "verifyActive"
	DrainPrevious     OperationKind = "drainPrevious"
	RetainPrevious    OperationKind = "retainPrevious"
)

type RecoveryMode string

const (
	DiscardStaged          RecoveryMode = "discard-staged"
	RemoveCandidate        RecoveryMode = "remove-candidate"
	StopCandidate          RecoveryMode = "stop-candidate"
	LeaveEndpointUnchanged RecoveryMode = "leave-endpoint-unchanged"
	RestorePreviousRoute   RecoveryMode = "restore-previous-route"
	RetainBothGenerations  RecoveryMode = "retain-both-generations"
)

type Operation struct {
	ID                   string           `json:"id"`
	Kind                 OperationKind    `json:"kind"`
	DependsOn            []string         `json:"dependsOn"`
	Input                OperationInput   `json:"input"`
	Preconditions        []TypedCondition `json:"preconditions"`
	ExpectedObservations []TypedCondition `json:"expectedObservations"`
	Recovery             RecoveryMode     `json:"recovery"`
}

type TypedCondition struct {
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	Expected string `json:"expected"`
}

type OperationInput struct {
	Artifact   *ArtifactInput         `json:"artifact,omitempty"`
	Generation *GenerationInput       `json:"generation,omitempty"`
	Systemd    *SystemdInput          `json:"systemd,omitempty"`
	Health     *HealthInput           `json:"health,omitempty"`
	Endpoint   *EndpointInput         `json:"endpoint,omitempty"`
	Previous   *host.GenerationStatus `json:"previous,omitempty"`
	Retention  *RetentionInput        `json:"retention,omitempty"`
}

type ArtifactInput struct {
	Source string `json:"source"`
	Digest string `json:"digest"`
}

type GenerationInput struct {
	ID               string `json:"id"`
	Revision         string `json:"revision"`
	ArtifactDigest   string `json:"artifactDigest"`
	Account          string `json:"account"`
	ReleaseDirectory string `json:"releaseDirectory"`
}

type SystemdInput struct {
	GenerationID     string `json:"generationId"`
	Revision         string `json:"revision"`
	ArtifactDigest   string `json:"artifactDigest"`
	Account          string `json:"account"`
	ReleaseDirectory string `json:"releaseDirectory"`
	Unit             string `json:"unit"`
	Port             int    `json:"port"`
}

type HealthInput struct {
	GenerationID        string `json:"generationId"`
	Revision            string `json:"revision"`
	LivenessPath        string `json:"livenessPath"`
	ReadinessPath       string `json:"readinessPath"`
	CandidateVerifyPath string `json:"candidateVerificationPath"`
	Port                int    `json:"port"`
}

type EndpointInput struct {
	RouteID      string `json:"routeId"`
	ListenPort   int    `json:"listenPort"`
	Upstream     string `json:"upstream"`
	UpstreamPort int    `json:"upstreamPort"`
}

type RetentionInput struct {
	GenerationID     string `json:"generationId"`
	SystemdUnit      string `json:"systemdUnit"`
	ReleaseDirectory string `json:"releaseDirectory"`
	RouteID          string `json:"routeId"`
	Until            string `json:"until"`
}

// Build converts validated configuration and a restricted Host Target
// inspection into canonical preview data. It never changes or persists state.
func Build(compiled config.Compiled, observation host.BootstrapStatus) (Preview, error) {
	selection, err := compiled.HostSelection()
	if err != nil {
		return Preview{}, err
	}
	if observation.Environment != compiled.Environment.Name || observation.Operator != selection.Target.User {
		return Preview{}, errors.New("host observation does not identify the configured Environment and operator")
	}

	evidence := CapabilityEvidence{
		Contract:     "host-systemd-caddy-http/v1alpha1",
		RequiredMode: selection.Implementation.Rollout,
		Guarantees: []string{
			"separate-candidate-generation",
			"candidate-health-gate",
			"atomic-caddy-route-load",
			"previous-generation-retention",
		},
		SupportEvidence: []string{
			"restricted-bootstrap-ready",
			"tested-product-version-match",
			"caddy-active",
			"caddy-config-valid",
			"caddy-admin-reachable",
			"generation-storage-ready",
			"candidate-port-free",
			"journald-active",
			"restricted-executor-identity-matched",
		},
		Observed: observation,
		Decision: "required-blue-green-supported",
	}
	reasons := capabilityIssues(observation, selection)
	preview := Preview{
		SchemaVersion: PreviewSchemaVersion,
		Executable:    len(reasons) == 0,
		Capability:    evidence,
		Reasons:       reasons,
	}
	if len(reasons) != 0 {
		preview.Capability.Decision = "unsupported"
		return preview, nil
	}

	observationDigest, err := digest(observation)
	if err != nil {
		return Preview{}, err
	}
	plan := Plan{
		SchemaVersion:       SchemaVersion,
		Application:         compiled.Application.Name,
		Environment:         compiled.Environment.Name,
		Revision:            compiled.Revision.Name,
		ConfigurationDigest: compiled.Digest,
		ArtifactDigests:     map[string]string{selection.Component: selection.Artifact.Digest},
		Target: Target{
			Name:    selection.TargetName,
			Kind:    selection.Target.Kind,
			Local:   selection.Target.Local,
			Address: selection.Target.Address,
			User:    selection.Target.User,
		},
		ObservationDigest: observationDigest,
		Capability:        evidence,
		ApprovalRequirements: []ApprovalRequirement{{
			Capability: "approve",
			Reason:     "environment deployment policy requires approval of this exact Plan",
		}},
		SensitiveValueReferences: []string{},
		Operations:               httpOperations(compiled, selection, observation, observationDigest),
	}
	plan.ID, err = digest(plan)
	if err != nil {
		return Preview{}, err
	}
	preview.Plan = &plan
	return preview, nil
}

func capabilityIssues(observation host.BootstrapStatus, selection config.HostSelection) []string {
	issues := append([]string{}, observation.Findings...)
	if observation.SchemaVersion != "provision.dev/host-inspection/v1alpha1" {
		issues = append(issues, "host inspection schema does not provide deployment capability evidence")
	}
	if !observation.Ready {
		issues = append(issues, "restricted Host Target bootstrap is not ready")
	}
	if observation.OS != "ubuntu" || observation.OSVersion != "26.04" {
		issues = append(issues, fmt.Sprintf("Ubuntu version %s has no tested required blue-green evidence", observedOrUnknown(observation.OSVersion)))
	}
	if observation.Architecture != "x86_64" {
		issues = append(issues, fmt.Sprintf("architecture %s has no tested required blue-green evidence", observedOrUnknown(observation.Architecture)))
	}
	if !strings.HasPrefix(observation.SystemdVersion, "systemd 259") {
		issues = append(issues, fmt.Sprintf("systemd version %s has no tested required blue-green evidence", observedOrUnknown(observation.SystemdVersion)))
	}
	if observation.CaddyVersion != "2.6.2" {
		issues = append(issues, fmt.Sprintf("Caddy version %s has no tested required blue-green evidence", observedOrUnknown(observation.CaddyVersion)))
	}
	if !observation.CaddyActive {
		issues = append(issues, "Caddy is not active")
	}
	if !observation.CaddyConfigValid {
		issues = append(issues, "Caddy configuration is not valid")
	}
	if !observation.CaddyAdminReachable {
		issues = append(issues, "Caddy admin endpoint is not reachable for atomic configuration loads")
	}
	if !observation.GenerationStorageReady {
		issues = append(issues, "generation storage is not ready")
	}
	if !observation.JournaldActive {
		issues = append(issues, "journald is not active")
	}
	if observation.ExecutorDigest == "" {
		issues = append(issues, "restricted host executor identity is not observed")
	}
	if observation.AuthorityKeyID == "" {
		issues = append(issues, "host authorization authority is not observed")
	}
	if !selection.Target.Local && observation.SSHHostKeyFingerprint == "" {
		issues = append(issues, "SSH host key identity is not observed")
	}
	for _, operation := range []string{"stageArtifact", "installGeneration", "startCandidate", "verifyCandidate"} {
		if !slices.Contains(observation.AllowedOperations, operation) {
			issues = append(issues, fmt.Sprintf("host executor does not allow typed %s operations", operation))
		}
	}
	if active := observation.Deployment.Active; active != nil && (!active.UnitActive || !active.UnitMatches || !active.RouteObserved || !active.RouteMatches) {
		issues = append(issues, "active generation and stable Caddy route do not match observed deployment state")
	}
	candidatePort := generationPort(selection.Artifact.Digest)
	if candidatePort == selection.Implementation.Endpoint.Port {
		issues = append(issues, "candidate port collides with the stable Endpoint port")
	}
	if slices.Contains(observation.ListeningTCPPorts, candidatePort) {
		issues = append(issues, fmt.Sprintf("candidate port %d is already listening", candidatePort))
	}
	return unique(issues)
}

func httpOperations(compiled config.Compiled, selection config.HostSelection, observation host.BootstrapStatus, observationDigest string) []Operation {
	digestID := strings.TrimPrefix(selection.Artifact.Digest, "sha256:")[:12]
	generationID := compiled.Revision.Name + "-" + digestID
	account := "provision-" + compiled.Environment.Name
	releaseDirectory := filepath.Join("/var/lib/provision/environments", compiled.Environment.Name, "releases", generationID)
	unit := fmt.Sprintf("provision-%s-%s-%s.service", compiled.Environment.Name, selection.Component, digestID)
	routeID := fmt.Sprintf("provision-%s-%s", compiled.Environment.Name, selection.Component)
	listenPort := selection.Implementation.Endpoint.Port
	candidatePort := generationPort(selection.Artifact.Digest)
	component := compiled.Application.Components[selection.Component]

	operations := []Operation{
		{ID: "op-01", Kind: StageArtifact, DependsOn: []string{}, Input: OperationInput{Artifact: &ArtifactInput{Source: selection.Artifact.Source, Digest: selection.Artifact.Digest}}, Preconditions: conditions("artifact-digest", selection.Artifact.Source, selection.Artifact.Digest), ExpectedObservations: conditions("artifact-cache", selection.Artifact.Digest, "verified"), Recovery: DiscardStaged},
		{ID: "op-02", Kind: InstallGeneration, DependsOn: []string{"op-01"}, Input: OperationInput{Generation: &GenerationInput{ID: generationID, Revision: compiled.Revision.Name, ArtifactDigest: selection.Artifact.Digest, Account: account, ReleaseDirectory: releaseDirectory}}, Preconditions: conditions("artifact-cache", selection.Artifact.Digest, "verified"), ExpectedObservations: conditions("generation-directory", releaseDirectory, generationID), Recovery: RemoveCandidate},
		{ID: "op-03", Kind: StartCandidate, DependsOn: []string{"op-02"}, Input: OperationInput{Systemd: &SystemdInput{GenerationID: generationID, Revision: compiled.Revision.Name, ArtifactDigest: selection.Artifact.Digest, Account: account, ReleaseDirectory: releaseDirectory, Unit: unit, Port: candidatePort}}, Preconditions: conditions("tcp-port", fmt.Sprintf("127.0.0.1:%d", candidatePort), "available"), ExpectedObservations: conditions("systemd-unit", unit, "active"), Recovery: StopCandidate},
		{ID: "op-04", Kind: VerifyCandidate, DependsOn: []string{"op-03"}, Input: OperationInput{Health: &HealthInput{GenerationID: generationID, Revision: compiled.Revision.Name, LivenessPath: component.Health.Liveness.Path, ReadinessPath: component.Health.Readiness.Path, CandidateVerifyPath: component.Health.CandidateVerification.Path, Port: candidatePort}}, Preconditions: conditions("health-check", component.Health.Readiness.Path, "healthy"), ExpectedObservations: conditions("health-check", component.Health.CandidateVerification.Path, "healthy"), Recovery: LeaveEndpointUnchanged},
		{ID: "op-05", Kind: SwitchEndpoint, DependsOn: []string{"op-04"}, Input: OperationInput{Endpoint: &EndpointInput{RouteID: routeID, ListenPort: listenPort, Upstream: fmt.Sprintf("127.0.0.1:%d", candidatePort), UpstreamPort: candidatePort}}, Preconditions: conditions("candidate-verification", generationID, "passed"), ExpectedObservations: conditions("caddy-route", routeID, fmt.Sprintf("127.0.0.1:%d", candidatePort)), Recovery: RestorePreviousRoute},
		{ID: "op-06", Kind: VerifyActive, DependsOn: []string{"op-05"}, Input: OperationInput{Health: &HealthInput{GenerationID: generationID, Revision: compiled.Revision.Name, LivenessPath: component.Health.Liveness.Path, ReadinessPath: component.Health.Readiness.Path, CandidateVerifyPath: component.Health.CandidateVerification.Path, Port: listenPort}}, Preconditions: conditions("caddy-route", routeID, fmt.Sprintf("127.0.0.1:%d", candidatePort)), ExpectedObservations: conditions("stable-endpoint-health", component.Health.Readiness.Path, "healthy"), Recovery: RestorePreviousRoute},
	}
	if previous := observation.Deployment.Active; previous != nil {
		operations = append(operations,
			Operation{ID: "op-07", Kind: DrainPrevious, DependsOn: []string{"op-06"}, Input: OperationInput{Previous: previous}, Preconditions: conditions("post-switch-verification", generationID, "passed"), ExpectedObservations: conditions("systemd-unit", previous.SystemdUnit, "drained"), Recovery: RestorePreviousRoute},
			Operation{ID: "op-08", Kind: RetainPrevious, DependsOn: []string{"op-07"}, Input: OperationInput{Retention: &RetentionInput{GenerationID: previous.ID, SystemdUnit: previous.SystemdUnit, ReleaseDirectory: previous.ReleaseDirectory, RouteID: previous.RouteID, Until: "environment-policy-rollback-window"}}, Preconditions: conditions("systemd-unit", previous.SystemdUnit, "drained"), ExpectedObservations: conditions("rollback-generation", previous.ID+"@"+observationDigest, "retained"), Recovery: RetainBothGenerations},
		)
	}
	return operations
}

func conditions(kind, subject, expected string) []TypedCondition {
	return []TypedCondition{{Kind: kind, Subject: subject, Expected: expected}}
}

func generationPort(digest string) int {
	bytes, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")[:4])
	if err != nil || len(bytes) != 2 {
		return 0
	}
	return 20000 + (int(bytes[0])<<8|int(bytes[1]))%20000
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

func digest(value any) (string, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize Plan input: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func OperationDigest(operation Operation) (string, error) {
	return digest(operation)
}

func (p Plan) VerifyIdentity() error {
	want := p.ID
	copy := p
	copy.ID = ""
	actual, err := digest(copy)
	if err != nil {
		return err
	}
	if want == "" || actual != want {
		return errors.New("Plan identity does not match its canonical contents")
	}
	return nil
}
