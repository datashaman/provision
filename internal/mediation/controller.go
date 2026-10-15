package mediation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"provision/internal/approval"
	"provision/internal/authority"
	"provision/internal/config"
	"provision/internal/execution"
	"provision/internal/host"
	"provision/internal/planner"
	"provision/internal/state"
)

func validateRequest(data []byte) (Request, error) {
	var request Request
	if err := decode(data, &request); err != nil {
		return request, err
	}
	for _, name := range []string{request.ID, request.Application, request.Environment, request.Revision, request.Component} {
		if !namePattern.MatchString(name) {
			return request, errors.New("request references must be registered identifiers")
		}
	}
	if request.SchemaVersion != RequestSchema || request.Operation != "stageArtifact" {
		return request, errors.New("unsupported request schema or operation")
	}
	return request, nil
}

func (c *Controller) Import(ctx context.Context, requester, filename string, now time.Time) (state.RequestSnapshot, error) {
	registry, err := c.registry()
	if err != nil {
		return state.RequestSnapshot{}, err
	}
	account, ok := registry.Requesters[requester]
	if !ok {
		return state.RequestSnapshot{}, errors.New("unknown registered requester")
	}
	data, err := CaptureSubmission(account.Inbox, filename, account.UID, account.GID)
	if err != nil {
		return state.RequestSnapshot{}, err
	}
	request, validationErr := validateRequest(data)
	rawDigest := digest(data)
	canonical, _ := json.Marshal(request)
	identityDigest := digest(canonical)
	if validationErr != nil {
		identityDigest = rawDigest
	}
	identity, _ := json.Marshal([]string{requester, strconv.FormatUint(uint64(account.UID), 10), identityDigest})
	capture := state.RequestCapture{SchemaVersion: state.RequestCaptureSchema, ID: digest(identity), Requester: requester, UID: account.UID, SubmissionID: request.ID, SubmissionDigest: rawDigest, Submission: data, Application: request.Application, Environment: request.Environment, Revision: request.Revision, Component: request.Component, Operation: planner.OperationKind(request.Operation), CapturedAt: now.UTC()}
	if validationErr != nil {
		capture.Denial = "invalid-request"
		capture.SubmissionID = rawDigest
	} else if compiled, err := resolve(registry, requester, request); err != nil {
		capture.Denial = "scope-or-registration-denied"
	} else {
		capture.Configuration = compiled
	}
	snapshot, err := c.Backend.CaptureRequest(ctx, capture)
	if err != nil {
		return state.RequestSnapshot{}, err
	}
	if err := c.Publish(ctx, snapshot.Capture.ID, now); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func resolve(registry Registry, requester string, request Request) (config.Compiled, error) {
	account, ok := registry.Requesters[requester]
	if !ok {
		return config.Compiled{}, errors.New("requester registration was revoked")
	}
	allowed := false
	for _, grant := range account.Grants {
		if grant.Application == request.Application && grant.Environment == request.Environment {
			for _, op := range grant.Operations {
				if op == request.Operation {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return config.Compiled{}, errors.New("requester scope does not grant the requested operation")
	}
	for _, compiled := range registry.Configurations {
		if compiled.Application.Name == request.Application && compiled.Environment.Name == request.Environment && compiled.Revision.Name == request.Revision {
			selection, err := compiled.HostSelection()
			if err != nil {
				return config.Compiled{}, err
			}
			if selection.Target.Local || selection.Component != request.Component || request.Operation != "stageArtifact" {
				return config.Compiled{}, errors.New("request does not select the registered remote native HTTP Artifact")
			}
			return compiled, nil
		}
	}
	return config.Compiled{}, errors.New("unknown registered Application, Environment or Revision")
}

func (c *Controller) allowed(snapshot state.RequestSnapshot) error {
	registry, err := c.registry()
	if err != nil {
		return err
	}
	capture := snapshot.Capture
	r, ok := registry.Requesters[capture.Requester]
	if !ok || r.UID != capture.UID || capture.Denial != "" {
		return errors.New("captured request is denied or its authenticated account registration changed")
	}
	request, err := validateRequest(capture.Submission)
	if err != nil {
		return err
	}
	compiled, err := resolve(registry, capture.Requester, request)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(capture.Configuration)
	current, _ := json.Marshal(compiled)
	if string(expected) != string(current) {
		return errors.New("registered input identities changed; submit a new request and obtain fresh approval")
	}
	return nil
}

func currentPlan(ctx context.Context, compiled config.Compiled) (planner.Plan, error) {
	selection, err := compiled.HostSelection()
	if err != nil {
		return planner.Plan{}, err
	}
	observation, err := host.CheckBootstrap(ctx, host.Target{Address: selection.Target.Address, User: selection.Target.User}, compiled.Environment.Name, selection.Target.User)
	if err != nil {
		return planner.Plan{}, err
	}
	preview, err := planner.Build(compiled, observation)
	if err != nil {
		return planner.Plan{}, err
	}
	if !preview.Executable || preview.Plan == nil {
		return planner.Plan{}, fmt.Errorf("trusted Plan is not executable: %v", preview.Reasons)
	}
	return *preview.Plan, nil
}

func (c *Controller) Plan(ctx context.Context, id string, now time.Time) (state.RequestSnapshot, error) {
	snapshot, err := c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return snapshot, err
	}
	if err := c.allowed(snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.PlanID != "" {
		return snapshot, c.Publish(ctx, id, now)
	}
	plan, err := currentPlan(ctx, snapshot.Capture.Configuration)
	if err != nil {
		return snapshot, err
	}
	var selected string
	for _, op := range plan.Operations {
		if op.Kind == snapshot.Capture.Operation && op.Input.Artifact != nil {
			if selected != "" {
				return snapshot, errors.New("ambiguous requested operation")
			}
			selected = op.ID
		}
	}
	if selected == "" {
		return snapshot, errors.New("Plan has no requested stageArtifact operation")
	}
	if err := c.Backend.StoreRequestPlan(ctx, id, plan, selected, now.UTC()); err != nil {
		return snapshot, err
	}
	snapshot, err = c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return snapshot, err
	}
	return snapshot, c.Publish(ctx, id, now)
}

func (c *Controller) fresh(ctx context.Context, snapshot state.RequestSnapshot) (planner.Plan, error) {
	if err := c.allowed(snapshot); err != nil {
		return planner.Plan{}, err
	}
	stored, err := c.Backend.LoadPlanSnapshot(ctx, snapshot.PlanID)
	if err != nil {
		return planner.Plan{}, err
	}
	if stored.CurrentPlanID != snapshot.PlanID {
		return planner.Plan{}, errors.New("mediated Plan is superseded")
	}
	current, err := currentPlan(ctx, snapshot.Capture.Configuration)
	if err != nil {
		return planner.Plan{}, err
	}
	a, err := planner.ApprovalFingerprint(stored.Plan)
	if err != nil {
		return planner.Plan{}, err
	}
	b, err := planner.ApprovalFingerprint(current)
	if err != nil {
		return planner.Plan{}, err
	}
	if a != b {
		return planner.Plan{}, errors.New("mediated Plan is stale; submit a new request and obtain fresh approval")
	}
	return stored.Plan, nil
}

// Decide uses OS authentication, never an actor asserted by the submission.
func (c *Controller) Decide(ctx context.Context, id, planID, operationID, actor string, expires time.Duration, deny bool, now time.Time) (state.RequestSnapshot, error) {
	snapshot, err := c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return snapshot, err
	}
	if planID != snapshot.PlanID || operationID != snapshot.OperationID || expires <= 0 {
		return snapshot, errors.New("decision must confirm the reviewed exact Plan and operation, with a positive expiry")
	}
	auth, err := approval.AuthenticateLocalActor(actor, approval.Scope{Application: snapshot.Capture.Application, Environment: snapshot.Capture.Environment})
	if err != nil {
		return snapshot, err
	}
	current, err := user.Current()
	if err != nil {
		return snapshot, err
	}
	if current.Uid == strconv.FormatUint(uint64(snapshot.Capture.UID), 10) {
		return snapshot, errors.New("requester account cannot approve or deny its own mediated request")
	}
	plan, err := c.fresh(ctx, snapshot)
	if err != nil {
		return snapshot, err
	}
	decision := "approved"
	if deny {
		decision = "denied"
	}
	err = c.Backend.RecordRequestApproval(ctx, state.RequestApproval{SchemaVersion: state.RequestApprovalSchema, RequestID: id, PlanID: planID, OperationID: operationID, OperationDigest: snapshot.OperationDigest, SigningKeyID: plan.Capability.Observed.AuthorityKeyID, Actor: auth.Actor, DecidedAt: now.UTC(), ExpiresAt: now.UTC().Add(expires), Decision: decision})
	if err != nil {
		return snapshot, err
	}
	snapshot, err = c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return snapshot, err
	}
	return snapshot, c.Publish(ctx, id, now)
}

func (c *Controller) Authorize(ctx context.Context, id, operationID string, now time.Time) error {
	snapshot, err := c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return err
	}
	if err := c.allowed(snapshot); err != nil {
		return err
	}
	a := snapshot.Approval
	if snapshot.OperationID != operationID || a == nil || a.Decision != "approved" || a.RequestID != id || a.PlanID != snapshot.PlanID || a.OperationID != operationID || a.OperationDigest != snapshot.OperationDigest || !a.ExpiresAt.After(now) || a.DecidedAt.After(now) {
		return errors.New("mediated approval is absent, expired, denied or bound to another operation")
	}
	return nil
}

func (c *Controller) Execute(ctx context.Context, id, key string, resume bool, now time.Time) (receipt Receipt, err error) {
	defer c.auditFailure(ctx, id, "dispatch", &err)
	snapshot, err := c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	receipt, err = c.Receipt(ctx, id, now)
	if err != nil {
		return receipt, err
	}
	if receipt.Status == "succeeded" {
		return receipt, c.Publish(ctx, id, now)
	}
	if err := c.Authorize(ctx, id, snapshot.OperationID, now); err != nil {
		return receipt, err
	}
	if _, err := c.fresh(ctx, snapshot); err != nil {
		return receipt, err
	}
	signer, err := authority.LoadSigner(key)
	if err != nil {
		return receipt, err
	}
	stored, err := c.Backend.LoadPlanSnapshot(ctx, snapshot.PlanID)
	if err != nil {
		return receipt, err
	}
	handler, err := execution.NewHostHandler(stored.Plan.Target, stored.Plan.Environment)
	if err != nil {
		return receipt, err
	}
	holder, err := execution.NewLocalHolder()
	if err != nil {
		return receipt, err
	}
	engine := execution.Engine{Backend: c.Backend, Signer: signer, Handler: handler, Authorize: c.Authorize, Now: time.Now}
	if resume {
		_, err = engine.Resume(ctx, execution.ResumeRequest{RequestID: id, PlanID: snapshot.PlanID, Holder: holder, LeaseDuration: 2 * time.Minute})
	} else {
		_, err = engine.Execute(ctx, execution.Request{RequestID: id, PlanID: snapshot.PlanID, OperationID: snapshot.OperationID, Holder: holder, LeaseDuration: 2 * time.Minute})
	}
	// A caller cancellation must not prevent publishing the sanitized journal outcome.
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	publishErr := c.Publish(publishCtx, id, time.Now())
	receipt, receiptErr := c.Receipt(publishCtx, id, time.Now())
	return receipt, errors.Join(err, publishErr, receiptErr)
}

type Receipt struct {
	SchemaVersion       string                 `json:"schemaVersion"`
	RequestID           string                 `json:"requestId"`
	Requester           string                 `json:"requester"`
	UID                 uint32                 `json:"uid"`
	SubmissionDigest    string                 `json:"submissionDigest"`
	Application         string                 `json:"application"`
	Environment         string                 `json:"environment"`
	Revision            string                 `json:"revision"`
	Component           string                 `json:"component"`
	ConfigurationDigest string                 `json:"configurationDigest,omitempty"`
	ArtifactDigest      string                 `json:"artifactDigest,omitempty"`
	PlanID              string                 `json:"planId,omitempty"`
	OperationID         string                 `json:"operationId,omitempty"`
	OperationKind       string                 `json:"operationKind"`
	OperationDigest     string                 `json:"operationDigest,omitempty"`
	Status              string                 `json:"status"`
	ApprovalStatus      string                 `json:"approvalStatus"`
	Eligible            bool                   `json:"eligible"`
	LastEvent           *state.RequestEvent    `json:"lastEvent,omitempty"`
	Approval            *state.RequestApproval `json:"approval,omitempty"`
	Attempts            []AttemptReceipt       `json:"attempts,omitempty"`
}

type AttemptReceipt struct {
	AttemptID        string                 `json:"attemptId"`
	FencingToken     int64                  `json:"fencingToken"`
	ApprovalSequence int64                  `json:"approvalSequence"`
	SigningKeyID     string                 `json:"signingKeyId"`
	Outcome          state.ExecutionOutcome `json:"outcome,omitempty"`
	OccurredAt       time.Time              `json:"occurredAt"`
}

func (c *Controller) Receipt(ctx context.Context, id string, now time.Time) (Receipt, error) {
	snapshot, err := c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	cap := snapshot.Capture
	receipt := Receipt{SchemaVersion: "provision.dev/request-receipt/v1alpha1", RequestID: id, Requester: cap.Requester, UID: cap.UID, SubmissionDigest: cap.SubmissionDigest, Application: cap.Application, Environment: cap.Environment, Revision: cap.Revision, Component: cap.Component, ConfigurationDigest: cap.Configuration.Digest, ArtifactDigest: cap.Configuration.Revision.Artifacts[cap.Component].Digest, PlanID: snapshot.PlanID, OperationID: snapshot.OperationID, OperationKind: string(cap.Operation), OperationDigest: snapshot.OperationDigest, Approval: snapshot.Approval, Status: "captured"}
	receipt.ApprovalStatus = "unapproved"
	if a := snapshot.Approval; a != nil {
		receipt.ApprovalStatus = a.Decision
		if a.Decision == "approved" && !a.ExpiresAt.After(now) {
			receipt.ApprovalStatus = "expired"
		}
	}
	if err := c.allowed(snapshot); err != nil {
		receipt.ApprovalStatus = "scope-denied"
	}
	receipt.Eligible = receipt.ApprovalStatus == "approved"
	audit, err := c.Backend.LoadRequestEvents(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	if len(audit) > 0 {
		receipt.LastEvent = &audit[len(audit)-1]
	}
	if cap.Denial != "" {
		receipt.Status = cap.Denial
		return receipt, nil
	}
	if snapshot.PlanID == "" {
		return receipt, nil
	}
	stored, err := c.Backend.LoadPlanSnapshot(ctx, snapshot.PlanID)
	if err != nil {
		return Receipt{}, err
	}
	if stored.CurrentPlanID != snapshot.PlanID {
		receipt.ApprovalStatus = "superseded"
		receipt.Eligible = false
	}
	receipt.Status = "awaiting-approval"
	if a := snapshot.Approval; a != nil {
		receipt.Status = a.Decision
		if a.Decision == "approved" && !a.ExpiresAt.After(now) {
			receipt.Status = "expired"
		}
	}
	events, err := c.Backend.LoadJournal(ctx, snapshot.PlanID)
	if err != nil {
		return Receipt{}, err
	}
	positions := map[string]int{}
	for _, event := range events {
		if event.OperationID != snapshot.OperationID {
			continue
		}
		if event.Kind == state.JournalIntent {
			var binding struct {
				RequestID        string `json:"requestId"`
				ApprovalSequence int64  `json:"approvalSequence"`
				SigningKeyID     string `json:"signingKeyId"`
			}
			if err := json.Unmarshal(event.Observation, &binding); err != nil {
				return Receipt{}, err
			}
			if binding.RequestID != id {
				continue
			}
			positions[event.AttemptID] = len(receipt.Attempts)
			receipt.Attempts = append(receipt.Attempts, AttemptReceipt{AttemptID: event.AttemptID, FencingToken: event.FencingToken, ApprovalSequence: binding.ApprovalSequence, SigningKeyID: binding.SigningKeyID, OccurredAt: event.OccurredAt})
			receipt.Status = "interrupted"
		} else if position, ok := positions[event.AttemptID]; ok {
			receipt.Attempts[position].Outcome = event.Outcome
			receipt.Attempts[position].OccurredAt = event.OccurredAt
			receipt.Status = string(event.Outcome)
		}
	}
	if receipt.Status == "succeeded" {
		receipt.Eligible = false
	}
	if len(receipt.Attempts) > 0 {
		last := receipt.Attempts[len(receipt.Attempts)-1]
		if last.Outcome == state.ExecutionFailed && (snapshot.Approval == nil || !snapshot.Approval.DecidedAt.After(last.OccurredAt)) {
			receipt.Eligible = false
			receipt.ApprovalStatus = "fresh-decision-required"
		}
	}
	return receipt, nil
}

func (c *Controller) Publish(ctx context.Context, id string, now time.Time) error {
	receipt, err := c.Receipt(ctx, id, now)
	if err != nil {
		return err
	}
	registry, err := c.registry()
	if err != nil {
		return err
	}
	account, ok := registry.Requesters[receipt.Requester]
	if !ok {
		return errors.New("cannot publish without requester registration")
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(account.Results, id[len("sha256:"):]+".json"), data, 0640)
}

// Submit is usable by the requester with no registry, State Backend or Host access.
func Submit(inbox, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64<<10 {
		return "", errors.New("request exceeds 64 KiB")
	}
	request, err := validateRequest(data)
	if err != nil {
		return "", err
	}
	filename := request.ID + ".json"
	output, err := os.OpenFile(filepath.Join(inbox, filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return "", err
	}
	if _, err := output.Write(data); err != nil {
		output.Close()
		return "", err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return "", err
	}
	return filename, output.Close()
}

// Observe is read-only, including after approval expiry. Evidence stays in private state.
func (c *Controller) Observe(ctx context.Context, id string) (execution.HandlerObservation, error) {
	snapshot, err := c.Backend.LoadRequest(ctx, id)
	if err != nil {
		return execution.HandlerObservation{}, err
	}
	plan, err := c.Backend.LoadPlanSnapshot(ctx, snapshot.PlanID)
	if err != nil {
		return execution.HandlerObservation{}, err
	}
	handler, err := execution.NewHostHandler(plan.Plan.Target, plan.Plan.Environment)
	if err != nil {
		return execution.HandlerObservation{}, err
	}
	for _, op := range plan.Plan.Operations {
		if op.ID == snapshot.OperationID {
			return handler.Observe(ctx, snapshot.PlanID, op)
		}
	}
	return execution.HandlerObservation{}, errors.New("bound operation missing")
}

// Keep denial evidence attributable without publishing transport diagnostics or private paths.
func (c *Controller) auditFailure(ctx context.Context, id, action string, cause *error) {
	if *cause == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current, err := user.Current()
	if err != nil {
		*cause = errors.Join(*cause, err)
		return
	}
	event := state.RequestEvent{SchemaVersion: state.RequestEventSchema, RequestID: id, Action: action, Reason: "rejected-or-failed", Actor: current.Username, OccurredAt: time.Now().UTC()}
	*cause = errors.Join(*cause, c.Backend.RecordRequestEvent(auditCtx, event))
	*cause = errors.Join(*cause, c.Publish(auditCtx, id, time.Now().UTC()))
}
