package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"provision/internal/retention"
)

const ExecutorPath = "/usr/local/libexec/provision-host-executor"

func AllowedOperations() []string {
	return []string{"inspect", "stageArtifact", "installGeneration", "startCandidate", "verifyCandidate", "switchEndpoint", "verifyActive", "drainPrevious", "retainPrevious"}
}

var environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

type BootstrapStatus struct {
	SchemaVersion          string                `json:"schemaVersion,omitempty"`
	Environment            string                `json:"environment"`
	Operator               string                `json:"operator"`
	Account                string                `json:"account"`
	OS                     string                `json:"os"`
	OSVersion              string                `json:"osVersion"`
	Architecture           string                `json:"architecture"`
	SystemdVersion         string                `json:"systemdVersion,omitempty"`
	SSHServerVersion       string                `json:"sshServerVersion,omitempty"`
	CaddyVersion           string                `json:"caddyVersion,omitempty"`
	CaddyActive            bool                  `json:"caddyActive"`
	JournaldActive         bool                  `json:"journaldActive"`
	CgroupV2               bool                  `json:"cgroupV2"`
	ExecutorDigest         string                `json:"executorDigest,omitempty"`
	AuthorityKeyID         string                `json:"authorityKeyId,omitempty"`
	SSHHostKeyFingerprint  SSHHostKeyFingerprint `json:"sshHostKeyFingerprint,omitempty"`
	GenerationStorageReady bool                  `json:"generationStorageReady,omitempty"`
	CaddyConfigValid       bool                  `json:"caddyConfigValid,omitempty"`
	CaddyAdminReachable    bool                  `json:"caddyAdminReachable,omitempty"`
	CaddyConfigDurable     bool                  `json:"caddyConfigDurable,omitempty"`
	ListeningTCPPorts      []int                 `json:"listeningTcpPorts,omitempty"`
	Deployment             DeploymentStatus      `json:"deployment,omitempty"`
	AllowedOperations      []string              `json:"allowedOperations"`
	Ready                  bool                  `json:"ready"`
	Findings               []string              `json:"findings"`
}

type DeploymentStatus struct {
	Active   *GenerationStatus `json:"active,omitempty"`
	Previous *GenerationStatus `json:"previous,omitempty"`
}

type GenerationStatus struct {
	ID               string `json:"id"`
	Revision         string `json:"revision"`
	ArtifactDigest   string `json:"artifactDigest"`
	SystemdUnit      string `json:"systemdUnit"`
	ReleaseDirectory string `json:"releaseDirectory"`
	Port             int    `json:"port"`
	RouteID          string `json:"routeId"`
	UnitActive       bool   `json:"unitActive"`
	UnitMatches      bool   `json:"unitMatches"`
	RouteObserved    bool   `json:"routeObserved"`
	RouteUpstream    string `json:"routeUpstream,omitempty"`
	RouteMatches     bool   `json:"routeMatches"`
}

type ActiveGenerationRecord struct {
	SchemaVersion                        string            `json:"schemaVersion"`
	PlanID                               string            `json:"planId"`
	CandidateVerificationOperationDigest string            `json:"candidateVerificationOperationDigest"`
	Active                               GenerationStatus  `json:"active"`
	Previous                             *GenerationStatus `json:"previous,omitempty"`
	ListenPort                           int               `json:"listenPort"`
	DrainPolicy                          string            `json:"drainPolicy"`
	SwitchedAt                           time.Time         `json:"switchedAt"`
	StableVerificationOperationDigest    string            `json:"stableVerificationOperationDigest,omitempty"`
	StableVerifiedAt                     *time.Time        `json:"stableVerifiedAt,omitempty"`
	PreviousDrainOperationDigest         string            `json:"previousDrainOperationDigest,omitempty"`
	PreviousDrainedAt                    *time.Time        `json:"previousDrainedAt,omitempty"`
	PreviousRetentionOperationDigest     string            `json:"previousRetentionOperationDigest,omitempty"`
	PreviousRollbackWindow               retention.Window  `json:"previousRollbackWindow,omitempty"`
	PreviousRetainedAt                   *time.Time        `json:"previousRetainedAt,omitempty"`
	PreviousRetainUntil                  *time.Time        `json:"previousRetainUntil,omitempty"`
}

// CheckBootstrap invokes only the root-owned inspector. It cannot request a
// deployment operation, and remote connections require a trusted host key.
func CheckBootstrap(ctx context.Context, target Target, environment, operator string) (BootstrapStatus, error) {
	if err := target.Validate(); err != nil {
		return BootstrapStatus{}, err
	}
	if !environmentPattern.MatchString(environment) {
		return BootstrapStatus{}, errors.New("invalid environment identifier")
	}
	if !userPattern.MatchString(operator) || operator == "root" {
		return BootstrapStatus{}, errors.New("invalid operator user")
	}
	command := []string{"sudo", "-n", ExecutorPath, "inspect", "--environment", environment, "--operator", operator}
	name := command[0]
	args := command[1:]
	if !target.Local {
		name = "ssh"
		args = StrictSSHArguments(target, command...)
	}
	output, err := runCommand(ctx, name, args...)
	if err != nil {
		return BootstrapStatus{}, fmt.Errorf("host bootstrap check: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var status BootstrapStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return BootstrapStatus{}, fmt.Errorf("host bootstrap check returned invalid status: %w", err)
	}
	if status.Environment != environment || status.Operator != operator || status.Account != "provision-"+environment {
		return BootstrapStatus{}, errors.New("host bootstrap check returned mismatched identity")
	}
	wantedOperations := AllowedOperations()
	if len(status.AllowedOperations) != len(wantedOperations) {
		return BootstrapStatus{}, errors.New("unexpected host executor operation is enabled")
	}
	for index := range wantedOperations {
		if status.AllowedOperations[index] != wantedOperations[index] {
			return BootstrapStatus{}, errors.New("unexpected host executor operation is enabled")
		}
	}
	if status.Ready && (status.OS != "ubuntu" || status.Architecture == "" || !strings.HasPrefix(status.SystemdVersion, "systemd ") || status.SSHServerVersion == "" || status.CaddyVersion == "" || !status.CaddyActive || !status.CaddyConfigValid || !status.CaddyAdminReachable || !status.CaddyConfigDurable || !status.GenerationStorageReady || !status.JournaldActive || !strings.HasPrefix(status.ExecutorDigest, "sha256:") || !strings.HasPrefix(status.AuthorityKeyID, "sha256:") || !target.Local && !status.SSHHostKeyFingerprint.Valid() || len(status.Findings) != 0) {
		return BootstrapStatus{}, errors.New("host bootstrap check returned incomplete readiness evidence")
	}
	return status, nil
}
