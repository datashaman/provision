package operation

import (
	"encoding/json"
	"errors"

	"provision/internal/authority"
	"provision/internal/planner"
)

const (
	EnvelopeSchemaVersion = "provision.dev/host-operation/v1alpha1"
	ResultSchemaVersion   = "provision.dev/host-operation-result/v1alpha1"
)

type Envelope struct {
	SchemaVersion string            `json:"schemaVersion"`
	Authorization authority.Proof   `json:"authorization"`
	Operation     planner.Operation `json:"operation"`
}

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeUncertain Outcome = "uncertain"
)

type Result struct {
	SchemaVersion string          `json:"schemaVersion"`
	PlanID        string          `json:"planId"`
	OperationID   string          `json:"operationId"`
	AttemptID     string          `json:"attemptId"`
	FencingToken  int64           `json:"fencingToken"`
	Outcome       Outcome         `json:"outcome"`
	Observation   json.RawMessage `json:"observation"`
}

func (r Result) ValidateAgainst(envelope Envelope) error {
	claim := envelope.Authorization.Claim
	if r.SchemaVersion != ResultSchemaVersion || r.PlanID != claim.PlanID || r.OperationID != claim.OperationID || r.AttemptID != claim.AttemptID || r.FencingToken != claim.FencingToken {
		return errors.New("host operation result does not match its authorization")
	}
	if r.Outcome != OutcomeSucceeded && r.Outcome != OutcomeFailed && r.Outcome != OutcomeUncertain {
		return errors.New("host operation result has an invalid outcome")
	}
	if !json.Valid(r.Observation) {
		return errors.New("host operation result observation is invalid")
	}
	return nil
}
