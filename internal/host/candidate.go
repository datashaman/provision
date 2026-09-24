package host

import "encoding/json"

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
	EndpointPending EndpointStatus = "pending"
	EndpointActive  EndpointStatus = "active"
	EndpointFailed  EndpointStatus = "failed"
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
}

type OperationObservation struct {
	State    string          `json:"state"`
	Evidence json.RawMessage `json:"evidence"`
}
