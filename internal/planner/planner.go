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
	hostasync "provision/internal/implementation/hostasync"
	"provision/internal/planmodel"
	"provision/internal/rollbackwindow"
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
	Contract         string                `json:"contract"`
	RequiredMode     string                `json:"requiredMode"`
	Guarantees       []string              `json:"guarantees"`
	SupportEvidence  []string              `json:"supportEvidence"`
	Observed         host.BootstrapStatus  `json:"observed"`
	DecisionObserved *host.BootstrapStatus `json:"decisionObserved,omitempty"`
	Decision         string                `json:"decision"`
}

type ApprovalRequirement struct {
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type OperationKind = planmodel.OperationKind

const (
	StageArtifact           = planmodel.StageArtifact
	InstallGeneration       = planmodel.InstallGeneration
	StartCandidate          = planmodel.StartCandidate
	VerifyCandidate         = planmodel.VerifyCandidate
	SwitchEndpoint          = planmodel.SwitchEndpoint
	VerifyActive            = planmodel.VerifyActive
	DrainPrevious           = planmodel.DrainPrevious
	RetainPrevious          = planmodel.RetainPrevious
	PrepareQueue            = planmodel.PrepareQueue
	InstallTaskGeneration   = planmodel.InstallTaskGeneration
	VerifyTaskGeneration    = planmodel.VerifyTaskGeneration
	InstallWorkerGeneration = planmodel.InstallWorkerGeneration
	StartWorkerCandidate    = planmodel.StartWorkerCandidate
	VerifyWorkerCandidate   = planmodel.VerifyWorkerCandidate
	FenceWorkerIntake       = planmodel.FenceWorkerIntake
	DrainWorkerPrevious     = planmodel.DrainWorkerPrevious
	ActivateWorkerIntake    = planmodel.ActivateWorkerIntake
	VerifyWorkerActive      = planmodel.VerifyWorkerActive
	InstallScheduleRuntime  = planmodel.InstallScheduleRuntime
	HandoffSchedule         = planmodel.HandoffSchedule
	VerifySchedule          = planmodel.VerifySchedule
	RetainWorkerPrevious    = planmodel.RetainWorkerPrevious
)

type RecoveryMode = planmodel.RecoveryMode

const (
	DiscardStaged                = planmodel.DiscardStaged
	RemoveCandidate              = planmodel.RemoveCandidate
	StopCandidate                = planmodel.StopCandidate
	LeaveEndpointUnchanged       = planmodel.LeaveEndpointUnchanged
	RestorePreviousRoute         = planmodel.RestorePreviousRoute
	RetainBothGenerations        = planmodel.RetainBothGenerations
	RetainQueue                  = planmodel.RetainQueue
	DiscardAsyncArtifact         = planmodel.DiscardAsyncArtifact
	RemoveTaskCandidate          = planmodel.RemoveTaskCandidate
	RemoveWorkerCandidate        = planmodel.RemoveWorkerCandidate
	KeepCandidateGated           = planmodel.KeepCandidateGated
	RestorePreviousWorkerIntake  = planmodel.RestorePreviousWorkerIntake
	ReleaseInflight              = planmodel.ReleaseInflight
	RestorePreviousScheduleFence = planmodel.RestorePreviousScheduleFence
	RetainBothWorkerGenerations  = planmodel.RetainBothWorkerGenerations
)

type Operation = planmodel.Operation
type TypedCondition = planmodel.TypedCondition
type OperationInput = planmodel.OperationInput
type AsyncOperationInput = planmodel.AsyncOperationInput
type AsyncQueueInput = planmodel.AsyncQueueInput
type AsyncArtifactInput = planmodel.AsyncArtifactInput
type AsyncWorkerInput = planmodel.AsyncWorkerInput
type AsyncTaskInput = planmodel.AsyncTaskInput
type AsyncScheduleInput = planmodel.AsyncScheduleInput
type AsyncRuntimeInput = planmodel.AsyncRuntimeInput

type ArtifactInput = planmodel.ArtifactInput
type GenerationReference = planmodel.GenerationReference
type GenerationInput = planmodel.GenerationInput
type SystemdInput = planmodel.SystemdInput
type HealthInput = planmodel.HealthInput
type EndpointInput = planmodel.EndpointInput
type DrainInput = planmodel.DrainInput
type RetentionInput = planmodel.RetentionInput

// Build converts validated configuration and a restricted Host Target
// inspection into canonical preview data. It never changes or persists state.
func Build(compiled config.Compiled, observation host.BootstrapStatus) (Preview, error) {
	if compiled.IsAsync() {
		return buildAsync(compiled, observation)
	}
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
			"in-flight-http-request-drain",
			"previous-generation-retention",
			"post-switch-health-verification",
			"automatic-previous-route-rollback",
		},
		SupportEvidence: []string{
			"restricted-bootstrap-ready",
			"tested-product-version-match",
			"caddy-active",
			"caddy-config-valid",
			"caddy-admin-reachable",
			"caddy-graceful-config-reload",
			"caddy-autosave-resume",
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

func buildAsync(compiled config.Compiled, observation host.BootstrapStatus) (Preview, error) {
	target, err := compiled.PlanningTarget()
	if err != nil {
		return Preview{}, err
	}
	if observation.Environment != compiled.Environment.Name || observation.Operator != target.Target.User {
		return Preview{}, errors.New("host observation does not identify the configured Environment and operator")
	}
	evaluation := hostasync.Evaluate(observation, target)
	evidence := CapabilityEvidence{
		Contract:        evaluation.Contract,
		RequiredMode:    "required",
		Guarantees:      evaluation.Guarantees,
		SupportEvidence: evaluation.SupportEvidence,
		Observed:        observation,
		Decision:        "required-blue-green-supported",
	}
	reasons := append([]string(nil), evaluation.Reasons...)
	if !compiled.IsQueueOnly() && observation.Async != nil {
		if reason := hostasync.ReplacementReason(compiled, *observation.Async); reason != "" {
			reasons = append(reasons, reason)
		}
		capability := observation.Async.Capabilities
		if !capability.WorkerAdmissionGate {
			reasons = append(reasons, "required Worker admission-gate capability is not observed")
		}
		if capability.ScheduleAppletDigest == "" || capability.ScheduleLedgerSchema == "" {
			reasons = append(reasons, "required Schedule runtime capability is not observed")
		}
		for _, component := range compiled.Application.Components {
			if component.Role == "schedule" {
				if reason := hostasync.InitialSchedulePolicyReason(component.Schedule); reason != "" {
					reasons = append(reasons, reason)
				}
			}
		}
	}
	preview := Preview{SchemaVersion: PreviewSchemaVersion, Executable: len(reasons) == 0, Capability: evidence, Reasons: reasons}
	if len(reasons) != 0 {
		preview.Capability.Decision = "unsupported"
		return preview, nil
	}

	planObservation := hostasync.DecisionObservation(observation)
	evidence.DecisionObserved = &planObservation
	preview.Capability = evidence
	observationDigest, err := digest(planObservation)
	if err != nil {
		return Preview{}, err
	}
	artifactDigests := make(map[string]string, len(compiled.Revision.Artifacts))
	for name, artifact := range compiled.Revision.Artifacts {
		artifactDigests[name] = artifact.Digest
	}
	adapterPlan := hostasync.Plan(compiled, planObservation)
	operations := composeAsyncOperations(adapterPlan)
	for _, operation := range operations {
		if !slices.Contains(observation.AllowedOperations, string(operation.Kind)) {
			reasons = append(reasons, fmt.Sprintf("host executor does not allow typed %s operations", operation.Kind))
		}
	}
	if len(reasons) != 0 {
		preview.Executable = false
		preview.Reasons = unique(reasons)
		preview.Capability.Decision = "unsupported"
		return preview, nil
	}
	plan := Plan{
		SchemaVersion:       SchemaVersion,
		Application:         compiled.Application.Name,
		Environment:         compiled.Environment.Name,
		Revision:            compiled.Revision.Name,
		ConfigurationDigest: compiled.Digest,
		ArtifactDigests:     artifactDigests,
		Target:              Target{Name: target.Name, Kind: target.Target.Kind, Local: target.Target.Local, Address: target.Target.Address, User: target.Target.User},
		ObservationDigest:   observationDigest,
		Capability:          evidence,
		ApprovalRequirements: []ApprovalRequirement{{
			Capability: "approve", Reason: "environment deployment policy requires approval of this exact Plan",
		}},
		SensitiveValueReferences: adapterPlan.SensitiveValueReferences,
		Operations:               operations,
	}
	plan.ID, err = digest(plan)
	if err != nil {
		return Preview{}, err
	}
	preview.Plan = &plan
	return preview, nil
}

func composeAsyncOperations(adapterPlan hostasync.PlanningOutput) []Operation {
	transitions := adapterPlan.Transitions
	if adapterPlan.QueueOnly {
		return []Operation{transitions.QueuePreparation}
	}
	workerArtifact := withDependencies(transitions.WorkerArtifact, transitions.QueuePreparation.ID)
	workerOperations := withExternalDependencies(transitions.Worker.Operations, transitions.Worker.EntryID, workerArtifact.ID, transitions.QueuePreparation.ID)
	if adapterPlan.WorkerOnly {
		operations := []Operation{transitions.QueuePreparation, workerArtifact}
		operations = append(operations, workerOperations...)
		return operations
	}
	taskArtifact := withDependencies(transitions.TaskArtifact, transitions.QueuePreparation.ID)
	taskOperations := withExternalDependencies(transitions.Task.Operations, transitions.Task.EntryID, taskArtifact.ID)
	scheduleOperations := withExternalDependencies(transitions.Schedule.Operations, transitions.Schedule.RuntimeID, transitions.Task.ReadyID)
	scheduleOperations = withExternalDependencies(scheduleOperations, transitions.Schedule.ActivationID, transitions.Worker.ReadyID, transitions.Task.ReadyID)

	operations := []Operation{transitions.QueuePreparation, workerArtifact, taskArtifact}
	operations = append(operations, taskOperations...)
	operations = append(operations, workerOperations...)
	operations = append(operations, scheduleOperations...)
	return operations
}

func withExternalDependencies(operations []Operation, targetID string, dependencies ...string) []Operation {
	result := append([]Operation(nil), operations...)
	for index := range result {
		if result[index].ID == targetID {
			result[index] = withDependencies(result[index], dependencies...)
			break
		}
	}
	return result
}

func withDependencies(operation Operation, dependencies ...string) Operation {
	for _, dependency := range dependencies {
		if !slices.Contains(operation.DependsOn, dependency) {
			operation.DependsOn = append(operation.DependsOn, dependency)
		}
	}
	return operation
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
	if !observation.CaddyConfigDurable {
		issues = append(issues, "Caddy is not configured to resume its autosaved active configuration")
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
	for _, operation := range []string{"stageArtifact", "installGeneration", "startCandidate", "verifyCandidate", "switchEndpoint", "verifyActive", "drainPrevious", "retainPrevious"} {
		if !slices.Contains(observation.AllowedOperations, operation) {
			issues = append(issues, fmt.Sprintf("host executor does not allow typed %s operations", operation))
		}
	}
	if active := observation.Deployment.Active; active != nil && (active.ArtifactDigest == "" || !active.UnitActive || !active.UnitMatches || !active.RouteObserved || !active.RouteMatches) {
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
	generation := GenerationReference{ID: generationID, Revision: compiled.Revision.Name, ArtifactDigest: selection.Artifact.Digest, Account: account, ReleaseDirectory: releaseDirectory}
	endpoint := EndpointInput{GenerationReference: generation, Unit: unit, RouteID: routeID, ListenPort: listenPort, Upstream: fmt.Sprintf("127.0.0.1:%d", candidatePort), UpstreamPort: candidatePort, DrainPolicy: "caddy-graceful-config-reload"}

	operations := []Operation{
		{ID: "op-01", Kind: StageArtifact, DependsOn: []string{}, Input: OperationInput{Artifact: &ArtifactInput{Source: selection.Artifact.Source, Digest: selection.Artifact.Digest}}, Preconditions: conditions("artifact-digest", selection.Artifact.Source, selection.Artifact.Digest), ExpectedObservations: conditions("artifact-cache", selection.Artifact.Digest, "verified"), Recovery: DiscardStaged},
		{ID: "op-02", Kind: InstallGeneration, DependsOn: []string{"op-01"}, Input: OperationInput{Generation: &GenerationInput{GenerationReference: generation}}, Preconditions: conditions("artifact-cache", selection.Artifact.Digest, "verified"), ExpectedObservations: conditions("generation-directory", releaseDirectory, generationID), Recovery: RemoveCandidate},
		{ID: "op-03", Kind: StartCandidate, DependsOn: []string{"op-02"}, Input: OperationInput{Systemd: &SystemdInput{GenerationReference: generation, Unit: unit, Port: candidatePort}}, Preconditions: conditions("tcp-port", fmt.Sprintf("127.0.0.1:%d", candidatePort), "available"), ExpectedObservations: conditions("systemd-unit", unit, "active"), Recovery: StopCandidate},
		{ID: "op-04", Kind: VerifyCandidate, DependsOn: []string{"op-03"}, Input: OperationInput{Health: &HealthInput{GenerationReference: generation, Unit: unit, LivenessPath: component.Health.Liveness.Path, ReadinessPath: component.Health.Readiness.Path, CandidateVerifyPath: component.Health.CandidateVerification.Path, Port: candidatePort}}, Preconditions: conditions("health-check", component.Health.Readiness.Path, "healthy"), ExpectedObservations: conditions("health-check", component.Health.CandidateVerification.Path, "healthy"), Recovery: LeaveEndpointUnchanged},
		{ID: "op-05", Kind: SwitchEndpoint, DependsOn: []string{"op-04"}, Input: OperationInput{Endpoint: &endpoint, Previous: observation.Deployment.Active}, Preconditions: conditions("candidate-verification", generationID, "passed"), ExpectedObservations: conditions("caddy-route", routeID, fmt.Sprintf("127.0.0.1:%d", candidatePort)), Recovery: RestorePreviousRoute},
		{ID: "op-06", Kind: VerifyActive, DependsOn: []string{"op-05"}, Input: OperationInput{Health: &HealthInput{GenerationReference: generation, Unit: unit, LivenessPath: component.Health.Liveness.Path, ReadinessPath: component.Health.Readiness.Path, CandidateVerifyPath: component.Health.CandidateVerification.Path, Port: listenPort}, Endpoint: &endpoint, Previous: observation.Deployment.Active}, Preconditions: conditions("caddy-route", routeID, fmt.Sprintf("127.0.0.1:%d", candidatePort)), ExpectedObservations: conditions("stable-endpoint-health", component.Health.Readiness.Path, "healthy"), Recovery: RestorePreviousRoute},
	}
	if previous := observation.Deployment.Active; previous != nil {
		operations = append(operations,
			Operation{ID: "op-07", Kind: DrainPrevious, DependsOn: []string{"op-06"}, Input: OperationInput{Drain: &DrainInput{Endpoint: endpoint, Previous: *previous, Mode: selection.Implementation.Endpoint.Drain.Mode, MaxDuration: selection.Implementation.Endpoint.Drain.MaxDuration}}, Preconditions: conditions("post-switch-verification", generationID, "passed"), ExpectedObservations: conditions("systemd-unit", previous.SystemdUnit, "drained"), Recovery: RestorePreviousRoute},
			Operation{ID: "op-08", Kind: RetainPrevious, DependsOn: []string{"op-07"}, Input: OperationInput{Retention: &RetentionInput{Endpoint: endpoint, Previous: *previous, Policy: rollbackwindow.RuleRollbackWindow, RollbackWindow: compiled.Environment.RollbackWindow}}, Preconditions: conditions("systemd-unit", previous.SystemdUnit, "drained"), ExpectedObservations: conditions("rollback-generation", previous.ID+"@"+observationDigest, "retained"), Recovery: RetainBothGenerations},
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

// ApprovalFingerprint binds configuration, typed operations, capabilities,
// and the adapter's explicitly relevant observation while retaining the exact
// full observation in the Plan as audit evidence. Runtime telemetry excluded
// by the adapter cannot race a human approval; every decision-relevant change
// still makes the stored Plan stale.
func ApprovalFingerprint(plan Plan) (string, error) {
	comparable := plan
	comparable.ID = ""
	if comparable.Capability.DecisionObserved != nil {
		comparable.Capability.Observed = *comparable.Capability.DecisionObserved
	}
	return digest(comparable)
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
