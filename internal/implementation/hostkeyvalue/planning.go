package hostkeyvalue

import (
	"fmt"

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planmodel"
)

const (
	Contract   = "host-valkey-quadlet-key-value-store/v1alpha1"
	listenHost = "127.0.0.1"
	listenPort = 26379
)

type Evaluation struct {
	Contract        string
	Guarantees      []string
	SupportEvidence []string
	Reasons         []string
	// Transition is true when the declared generation differs from a healthy,
	// qualified, same-major active generation, so the Plan is a Store Transition.
	Transition bool
}

// Evaluate fails closed on an unregistered or unqualified Valkey version.
func Evaluate(compiled config.Compiled, observation host.BootstrapStatus) Evaluation {
	name, _, implementation := component(compiled)
	evaluation := Evaluation{
		Contract: Contract,
		Guarantees: []string{
			"explicit-authoritative-store-data-role",
			"digest-pinned-qualified-valkey-image",
			"aof-everysec-persistence",
			"secret-reference-only-plan",
		},
	}
	if !observation.Ready {
		evaluation.Reasons = append(evaluation.Reasons, "restricted Host Target bootstrap is not ready")
	}
	evaluation.Reasons = append(evaluation.Reasons, capabilityReasons(observation)...)
	image, ok := ImageFor(implementation.Version)
	switch {
	case !ok:
		evaluation.Reasons = append(evaluation.Reasons, fmt.Sprintf("Valkey version %s for %q is not a registered image", implementation.Version, name))
	case image.Evidence == "":
		evaluation.Reasons = append(evaluation.Reasons, fmt.Sprintf("Valkey %s has no packaging qualification evidence yet", image.Version))
	}
	if active := activeGeneration(observation); active != nil && ok && active.ID != image.Generation() {
		if reason := transitionReason(active, image); reason != "" {
			evaluation.Reasons = append(evaluation.Reasons, reason)
		} else {
			evaluation.Transition = true
			evaluation.Guarantees = append(evaluation.Guarantees, "separate-candidate-generation", "bounded-acl-write-fence", "replication-offset-equality-evidence", "lossless-forward-cutover-after-bounded-write-fence", "forward-only-store-rollback", "previous-generation-retention")
		}
	}
	if candidate := candidateOf(observation); candidate != nil {
		evaluation.Reasons = append(evaluation.Reasons, unsafeCandidateReasons(candidate, activeGeneration(observation))...)
	}
	if len(evaluation.Reasons) == 0 {
		evaluation.SupportEvidence = []string{"valkey-" + image.Version + "-image-index-and-manifest-pinned", "packaging-qualification-" + image.Evidence}
	}
	return evaluation
}

// transitionReason is non-empty when the observed active generation cannot be
// replaced by the declared image. An unregistered, unhealthy, non-AOF or
// cross-major active generation fails closed.
func transitionReason(active *host.KeyValueGenerationStatus, image Image) string {
	activeImage, registered := ImageByReference(active.ImageReference)
	switch {
	case !registered:
		return "replacing the active Key-Value Store generation is a Store Transition, which is not qualified for an active generation that is not a registered qualified image"
	case activeImage.Major() != image.Major():
		return "Key-Value Store Transition is qualified only within one Valkey major version"
	case !active.Ready || !active.Connectivity || active.Health != "healthy" || active.Role != "authoritative" || active.AppendOnly != "yes" || active.AppendFsync != "everysec":
		return "Key-Value Store Transition requires a healthy authoritative active generation with AOF everysec"
	}
	return ""
}

func activeGeneration(observation host.BootstrapStatus) *host.KeyValueGenerationStatus {
	if observation.KeyValue == nil {
		return nil
	}
	return observation.KeyValue.Active
}

func candidateOf(observation host.BootstrapStatus) *host.KeyValueGenerationStatus {
	if observation.KeyValue == nil {
		return nil
	}
	return observation.KeyValue.Candidate
}

// unsafeCandidateReasons returns one distinct reason per unsafe condition of an
// observed candidate generation. Unknown compatibility evidence is unsafe.
func unsafeCandidateReasons(candidate, active *host.KeyValueGenerationStatus) []string {
	prefix := fmt.Sprintf("candidate Key-Value Store generation %s ", candidate.ID)
	var reasons []string
	image, registered := ImageByReference(candidate.ImageReference)
	if !registered {
		reasons = append(reasons, prefix+"uses an unregistered image")
	}
	if candidate.AppendOnly != "yes" || candidate.AppendFsync != "everysec" {
		reasons = append(reasons, prefix+"persistence is not AOF everysec")
	}
	if candidate.Role != "authoritative" {
		reasons = append(reasons, fmt.Sprintf("%shas Store Data Role %q, not authoritative", prefix, candidate.Role))
	}
	switch {
	case candidate.Compatibility == "incompatible":
		reasons = append(reasons, prefix+"failed compatibility with the active generation")
	case candidate.Compatibility != "compatible":
		reasons = append(reasons, prefix+"has unknown compatibility evidence")
	case active != nil && registered:
		if activeImage, ok := ImageByReference(active.ImageReference); !ok || activeImage.Major() != image.Major() {
			reasons = append(reasons, prefix+"is not the same Valkey major as the active generation")
		}
	}
	if !candidate.ReplicationLinkUp {
		reasons = append(reasons, prefix+"replication from the active generation is unreachable")
	}
	return reasons
}

func capabilityReasons(observation host.BootstrapStatus) []string {
	if observation.OS != "ubuntu" || observation.OSVersion != "26.04" || observation.Architecture != "x86_64" {
		return []string{"managed Valkey packaging is qualified only on Ubuntu 26.04 x86_64"}
	}
	status := observation.KeyValue
	if status == nil || status.SchemaVersion != host.KeyValueInspectionSchema || !status.ObservationComplete {
		return []string{"host inspection does not provide Key-Value Store capability evidence"}
	}
	c := status.Capabilities
	var reasons []string
	if !c.Quadlet || !c.RootlessEnvironmentAccount || !c.SystemdCredentials || !c.SubordinateIDs || !c.LingeringUserManager {
		reasons = append(reasons, "host lacks the rootless Podman/Quadlet and systemd credential capabilities managed Valkey needs")
	}
	if c.QuadletPath == "" || c.ListenAddress != listenHost || c.Port != listenPort || c.AppendOnly != "yes" || c.AppendFsync != "everysec" {
		reasons = append(reasons, "host Key-Value Store capability does not report the qualified listener and AOF everysec persistence")
	}
	return append(reasons, status.Findings...)
}

// Operation is the single typed prepareKeyValue operation for a first generation.
func Operation(plan *planmodel.KeyValuePlan) planmodel.Operation {
	return planmodel.Operation{
		ID: "op-01", Kind: planmodel.PrepareKeyValue, DependsOn: []string{},
		Input:                planmodel.OperationInput{KeyValue: plan},
		Preconditions:        []planmodel.TypedCondition{{Kind: "valkey-qualification", Subject: plan.QualificationDigest, Expected: "matched"}},
		ExpectedObservations: []planmodel.TypedCondition{{Kind: "key-value-generation", Subject: plan.LogicalID, Expected: "client-round-trip-verified"}, {Kind: "deterministic-keys", Subject: plan.Binding.Namespace, Expected: "present"}},
		Recovery:             planmodel.RetainKeyValue,
	}
}

// TransitionOperations is the Store Transition: prepare a separate candidate,
// synchronize it by replication, fence writes, switch authority, verify the new
// active generation, and retain the previous one. Rollback is forward-only.
func TransitionOperations(plan *planmodel.KeyValuePlan) []planmodel.Operation {
	previous := plan.Previous.ID
	steps := []struct {
		kind          planmodel.OperationKind
		preconditions []planmodel.TypedCondition
		expected      planmodel.TypedCondition
	}{
		{planmodel.PrepareKeyValueCandidate,
			[]planmodel.TypedCondition{{Kind: "key-value-secret-reference", Subject: plan.CredentialReference, Expected: "resolved-at-execution"}, {Kind: "active-key-value-generation", Subject: previous, Expected: "verified-compatible"}, {Kind: "valkey-qualification", Subject: plan.QualificationDigest, Expected: "matched"}},
			planmodel.TypedCondition{Kind: "candidate-key-value-generation", Subject: plan.GenerationID, Expected: "prepared"}},
		{planmodel.SynchronizeKeyValueCandidate,
			[]planmodel.TypedCondition{{Kind: "candidate-key-value-generation", Subject: plan.GenerationID, Expected: "prepared"}, {Kind: "synchronization-method", Subject: plan.LogicalID, Expected: plan.Transition.Synchronization}},
			planmodel.TypedCondition{Kind: "candidate-key-value-generation", Subject: plan.GenerationID, Expected: "synchronized"}},
		{planmodel.FenceKeyValueWrites,
			[]planmodel.TypedCondition{{Kind: "candidate-key-value-generation", Subject: plan.GenerationID, Expected: "synchronized"}, {Kind: "write-fence", Subject: plan.LogicalID, Expected: "bounded-" + plan.Transition.WriteFenceMaximum}},
			planmodel.TypedCondition{Kind: "write-fence", Subject: plan.LogicalID, Expected: "offset-equal-after-fence"}},
		{planmodel.SwitchKeyValueAuthority,
			[]planmodel.TypedCondition{{Kind: "write-fence", Subject: plan.LogicalID, Expected: "offset-equal-after-fence"}, {Kind: "forward-cutover", Subject: plan.LogicalID, Expected: plan.Transition.ForwardCutoverGuarantee}},
			planmodel.TypedCondition{Kind: "key-value-authority", Subject: plan.LogicalID, Expected: plan.GenerationID}},
		{planmodel.VerifyKeyValueActive,
			[]planmodel.TypedCondition{{Kind: "key-value-authority", Subject: plan.LogicalID, Expected: plan.GenerationID}, {Kind: "deterministic-keys", Subject: plan.Binding.Namespace, Expected: "present"}},
			planmodel.TypedCondition{Kind: "key-value-generation", Subject: plan.LogicalID, Expected: "client-round-trip-verified-active"}},
		{planmodel.RetainKeyValuePrevious,
			[]planmodel.TypedCondition{{Kind: "previous-key-value-generation", Subject: previous, Expected: "exact-identity-retained"}, {Kind: "store-rollback-guarantee", Subject: plan.LogicalID, Expected: plan.Transition.StoreRollbackGuarantee}},
			planmodel.TypedCondition{Kind: "previous-key-value-generation", Subject: previous, Expected: "retained-through-rollback-window"}},
	}
	operations := make([]planmodel.Operation, 0, len(steps))
	for index, step := range steps {
		dependsOn := []string{}
		if index > 0 {
			dependsOn = []string{fmt.Sprintf("op-%02d", index)}
		}
		operations = append(operations, planmodel.Operation{
			ID: fmt.Sprintf("op-%02d", index+1), Kind: step.kind, DependsOn: dependsOn,
			Input:                planmodel.OperationInput{KeyValue: plan},
			Preconditions:        step.preconditions,
			ExpectedObservations: []planmodel.TypedCondition{step.expected},
			Recovery:             planmodel.RetainKeyValue,
		})
	}
	return operations
}

// Plan derives the deterministic preview. The caller must have checked Evaluate.
func Plan(compiled config.Compiled, observation host.BootstrapStatus) *planmodel.KeyValuePlan {
	name, comp, implementation := component(compiled)
	image, _ := ImageFor(implementation.Version)
	env := compiled.Environment.Name
	service := "provision-" + env + "-valkey"
	logicalID := "provision-" + env + "-" + name
	namespace := logicalID + "/" + compiled.Revision.Name
	quadletPath := ""
	if observation.KeyValue != nil {
		quadletPath = observation.KeyValue.Capabilities.QuadletPath
	}
	plan := &planmodel.KeyValuePlan{
		Component:               name,
		LogicalID:               logicalID,
		GenerationID:            image.Generation(),
		Implementation:          implementation.Kind,
		DataRole:                comp.KeyValue.DataRole,
		ValkeyVersion:           image.Version,
		ImageIndex:              image.Index,
		ImageManifest:           image.Manifest,
		ImageReference:          image.Reference(),
		QualificationDigest:     image.Evidence,
		ServiceUnit:             service + ".service",
		Container:               service,
		Account:                 "provision-" + env,
		DataPath:                "/var/lib/provision/environments/" + env + "/services/valkey/generations/" + image.Generation() + "/data",
		QuadletDefinition:       service + ".container",
		ListenAddress:           listenHost,
		Port:                    listenPort,
		CredentialReference:     implementation.Credential,
		AppendOnly:              "yes",
		AppendFsync:             "everysec",
		TransitionCleanupPolicy: comp.KeyValue.TransitionCleanupPolicy,
		RollbackWindow:          compiled.Environment.RollbackWindow,
		QuadletPath:             quadletPath,
		Binding: planmodel.KeyValueBinding{
			Component: name, LogicalID: logicalID, Namespace: namespace,
			Keys:        []string{namespace + "/key-0001", namespace + "/key-0002"},
			ValuePrefix: "provision-deterministic-v1:",
		},
	}
	if active := activeGeneration(observation); active != nil && active.ID != image.Generation() {
		previous := *active
		plan.Previous = &previous
		plan.Transition = planmodel.KeyValueCandidateTransition(plan.ServiceUnit, plan.Container, quadletPath)
	}
	return plan
}

func component(compiled config.Compiled) (string, config.Component, config.Implementation) {
	for name, c := range compiled.Application.Components {
		if c.Role == "key-value-store" {
			return name, c, compiled.Environment.Implementations[name]
		}
	}
	return "", config.Component{}, config.Implementation{}
}
