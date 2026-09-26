package host

import (
	"encoding/json"
	"time"

	"provision/internal/drain"
	"provision/internal/rollbackwindow"
)

type CandidateStatus string

const (
	CandidateAbsent    CandidateStatus = "absent"
	CandidateInstalled CandidateStatus = "installed"
	CandidateActive    CandidateStatus = "active"
	CandidateHealthy   CandidateStatus = "healthy"
	CandidateInvalid   CandidateStatus = "invalid"
	CandidateFailed    CandidateStatus = "failed"
)

type GenerationManifest struct {
	SchemaVersion    string `json:"schemaVersion"`
	ID               string `json:"id"`
	Revision         string `json:"revision"`
	ArtifactDigest   string `json:"artifactDigest"`
	Account          string `json:"account"`
	ReleaseDirectory string `json:"releaseDirectory"`
	Executable       string `json:"executable"`
}

type GenerationObservation struct {
	Status           CandidateStatus `json:"status"`
	ID               string          `json:"id"`
	Revision         string          `json:"revision"`
	ArtifactDigest   string          `json:"artifactDigest"`
	ReleaseDirectory string          `json:"releaseDirectory"`
	Executable       string          `json:"executable,omitempty"`
	Reason           string          `json:"reason,omitempty"`
}

type SystemdObservation struct {
	Status           CandidateStatus `json:"status"`
	GenerationID     string          `json:"generationId"`
	Revision         string          `json:"revision"`
	Unit             string          `json:"unit"`
	Port             int             `json:"port"`
	ReleaseDirectory string          `json:"releaseDirectory"`
	Reason           string          `json:"reason,omitempty"`
}

type HealthCheckObservation struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	StatusCode int    `json:"statusCode,omitempty"`
	Healthy    bool   `json:"healthy"`
	Reason     string `json:"reason,omitempty"`
}

type HealthObservation struct {
	Status           CandidateStatus          `json:"status"`
	GenerationID     string                   `json:"generationId"`
	Revision         string                   `json:"revision"`
	ArtifactDigest   string                   `json:"artifactDigest"`
	ReleaseDirectory string                   `json:"releaseDirectory"`
	Unit             string                   `json:"unit"`
	Port             int                      `json:"port"`
	CandidateActive  bool                     `json:"candidateActive"`
	CandidateCleaned bool                     `json:"candidateCleaned,omitempty"`
	Checks           []HealthCheckObservation `json:"checks"`
	SwitchEligible   bool                     `json:"switchEligible"`
	Reason           string                   `json:"reason,omitempty"`
}

type EndpointStatus string

const (
	EndpointPending   EndpointStatus = "pending"
	EndpointActive    EndpointStatus = "active"
	EndpointFailed    EndpointStatus = "failed"
	EndpointUncertain EndpointStatus = "uncertain"
)

type EndpointObservation struct {
	Status            EndpointStatus    `json:"status"`
	RouteID           string            `json:"routeId"`
	ListenPort        int               `json:"listenPort"`
	Upstream          string            `json:"upstream"`
	Active            GenerationStatus  `json:"active"`
	Previous          *GenerationStatus `json:"previous,omitempty"`
	CandidateVerified bool              `json:"candidateVerified"`
	GracefulReload    bool              `json:"gracefulReload"`
	PreviousRetained  bool              `json:"previousRetained"`
	DrainPolicy       string            `json:"drainPolicy"`
	Reason            string            `json:"reason,omitempty"`
	RecoveryAction    string            `json:"recoveryAction,omitempty"`
}

type ActiveVerificationStatus string

const (
	ActiveVerificationHealthy    ActiveVerificationStatus = "healthy"
	ActiveVerificationRolledBack ActiveVerificationStatus = "rolled-back"
	ActiveVerificationUncertain  ActiveVerificationStatus = "uncertain"
)

type ActiveVerificationObservation struct {
	Status            ActiveVerificationStatus `json:"status"`
	Candidate         GenerationStatus         `json:"candidate"`
	Previous          *GenerationStatus        `json:"previous,omitempty"`
	ActiveChecks      []HealthCheckObservation `json:"activeChecks"`
	PreviousChecks    []HealthCheckObservation `json:"previousChecks,omitempty"`
	RollbackChecks    []HealthCheckObservation `json:"rollbackChecks,omitempty"`
	ObservedUpstream  string                   `json:"observedUpstream,omitempty"`
	RollbackAttempted bool                     `json:"rollbackAttempted"`
	RollbackSucceeded bool                     `json:"rollbackSucceeded"`
	Restored          *GenerationStatus        `json:"restored,omitempty"`
	Reason            string                   `json:"reason,omitempty"`
	RecoveryAction    string                   `json:"recoveryAction,omitempty"`
}

type DrainStatus string

const (
	DrainPending   DrainStatus = "pending"
	DrainCompleted DrainStatus = "drained"
	DrainFailed    DrainStatus = "failed"
	DrainUncertain DrainStatus = "uncertain"
)

type DrainObservation struct {
	Status                  DrainStatus      `json:"status"`
	Active                  GenerationStatus `json:"active"`
	Previous                GenerationStatus `json:"previous"`
	Mode                    drain.Mode       `json:"mode"`
	HandoffPolicy           string           `json:"handoffPolicy"`
	MaxDuration             drain.Bound      `json:"maxDuration"`
	OperationDigest         string           `json:"operationDigest"`
	SwitchedAt              *time.Time       `json:"switchedAt,omitempty"`
	StableVerifiedAt        *time.Time       `json:"stableVerifiedAt,omitempty"`
	Deadline                *time.Time       `json:"deadline,omitempty"`
	BoundElapsed            bool             `json:"boundElapsed"`
	StableRouteVerified     bool             `json:"stableRouteVerified"`
	PreviousUnitActive      bool             `json:"previousUnitActive"`
	PreviousUnitRetained    bool             `json:"previousUnitRetained"`
	PreviousReleaseRetained bool             `json:"previousReleaseRetained"`
	Reason                  string           `json:"reason,omitempty"`
	RecoveryAction          string           `json:"recoveryAction,omitempty"`
}

type RetentionStatus string

const (
	RetentionPending   RetentionStatus = "pending"
	RetentionCompleted RetentionStatus = "retained"
	RetentionFailed    RetentionStatus = "failed"
	RetentionUncertain RetentionStatus = "uncertain"
)

type RetentionObservation struct {
	Status                              RetentionStatus       `json:"status"`
	Active                              GenerationStatus      `json:"active"`
	Previous                            GenerationStatus      `json:"previous"`
	Policy                              rollbackwindow.Rule   `json:"policy"`
	RollbackWindow                      rollbackwindow.Window `json:"rollbackWindow"`
	OperationDigest                     string                `json:"operationDigest"`
	DrainOperationDigest                string                `json:"drainOperationDigest,omitempty"`
	SwitchedAt                          *time.Time            `json:"switchedAt,omitempty"`
	DrainedAt                           *time.Time            `json:"drainedAt,omitempty"`
	RetainedAt                          *time.Time            `json:"retainedAt,omitempty"`
	RetainUntil                         *time.Time            `json:"retainUntil,omitempty"`
	StableRouteVerified                 bool                  `json:"stableRouteVerified"`
	PreviousUnitActive                  bool                  `json:"previousUnitActive"`
	PreviousUnitRetained                bool                  `json:"previousUnitRetained"`
	PreviousGenerationDirectoryRetained bool                  `json:"previousGenerationDirectoryRetained"`
	PreviousManifestRetained            bool                  `json:"previousManifestRetained"`
	PreviousArtifactRetained            bool                  `json:"previousArtifactRetained"`
	Restartable                         bool                  `json:"restartable"`
	CleanupPerformed                    bool                  `json:"cleanupPerformed"`
	Reason                              string                `json:"reason,omitempty"`
	RecoveryAction                      string                `json:"recoveryAction,omitempty"`
}

type OperationObservation struct {
	State    string          `json:"state"`
	Outcome  string          `json:"outcome,omitempty"`
	Evidence json.RawMessage `json:"evidence"`
}
