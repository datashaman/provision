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

// Plan derives the deterministic preview. The caller must have checked Evaluate.
func Plan(compiled config.Compiled) *planmodel.KeyValuePlan {
	name, comp, implementation := component(compiled)
	image, _ := ImageFor(implementation.Version)
	env := compiled.Environment.Name
	service := "provision-" + env + "-valkey"
	return &planmodel.KeyValuePlan{
		Component:               name,
		LogicalID:               "provision-" + env + "-" + name,
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
