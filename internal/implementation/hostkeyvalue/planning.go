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
	if len(evaluation.Reasons) == 0 {
		evaluation.SupportEvidence = []string{"valkey-" + image.Version + "-image-index-and-manifest-pinned", "packaging-qualification-" + image.Evidence}
	}
	return evaluation
}

func capabilityReasons(observation host.BootstrapStatus) []string {
	if observation.OS != "ubuntu" || observation.OSVersion != "26.04" || observation.Architecture != "x86_64" {
		return []string{"managed Valkey packaging is qualified only on Ubuntu 26.04 x86_64"}
	}
	status := observation.KeyValue
	if status == nil || status.SchemaVersion != "provision.dev/host-key-value-inspection/v1alpha1" || !status.ObservationComplete {
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
	return &planmodel.KeyValuePlan{
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
}

func component(compiled config.Compiled) (string, config.Component, config.Implementation) {
	for name, c := range compiled.Application.Components {
		if c.Role == "key-value-store" {
			return name, c, compiled.Environment.Implementations[name]
		}
	}
	return "", config.Component{}, config.Implementation{}
}
