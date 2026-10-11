package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/planner"
	"provision/internal/rollbackwindow"
)

// Store Transition for one managed Valkey Key-Value Store: a candidate
// generation replicates from the active generation, writes are fenced by ACL
// revocation plus client termination, authority switches only after the
// candidate's replication offset equals the active master_repl_offset, and the
// previous generation is retained. Rollback after candidate writes is
// forward-only.

// errKeyValueFenceNotApplied means the ACL revocation itself failed, so nothing
// was mutated and the operation is a plain failure rather than uncertain.
var errKeyValueFenceNotApplied = errors.New("revoke write commands from the application user on the active generation")

// keyValueEqualBudget bounds the re-check made immediately before authority moves.
var keyValueEqualBudget = 5 * time.Second

const (
	keyValueTransitionSchema = "provision.dev/key-value-transition/v1alpha1"
	keyValueRetentionSchema  = "provision.dev/key-value-retention/v1alpha1"
	keyValueFenceBudget      = 30 * time.Second
	keyValueSyncBudget       = 2 * time.Minute

	keyValueSyncPhase   = "synchronizeKeyValueCandidate"
	keyValueFencePhase  = "fenceKeyValueWrites"
	keyValueSwitchPhase = "switchKeyValueAuthority"
	keyValueVerifyPhase = "verifyKeyValueActive"
	keyValueRetainPhase = "retainKeyValuePrevious"
)

type keyValueTransitionMarker struct {
	SchemaVersion       string                           `json:"schemaVersion"`
	Phase               string                           `json:"phase"`
	PlanID              string                           `json:"planId"`
	OperationID         string                           `json:"operationId"`
	OperationDigest     string                           `json:"operationDigest"`
	LogicalID           string                           `json:"logicalId"`
	PreviousGeneration  string                           `json:"previousGeneration"`
	CandidateGeneration string                           `json:"candidateGeneration"`
	Synchronization     string                           `json:"synchronization"`
	WriteFence          string                           `json:"writeFence,omitempty"`
	Replication         *host.KeyValueReplicationOffsets `json:"replication,omitempty"`
	Fence               *host.KeyValueFenceEvidence      `json:"fence,omitempty"`
}

type keyValueReplication struct {
	role         string
	linkUp       bool
	syncing      bool
	masterOffset int64
	slaveOffset  int64
	ok           bool
}

// keyValueReplicationInfo reads INFO replication from one container. ok is
// false when the generation could not be queried.
func keyValueReplicationInfo(ctx context.Context, record bootstrapRecord, container string) keyValueReplication {
	out, err := keyValueExec(ctx, record, container, "INFO", "replication")
	if err != nil {
		return keyValueReplication{}
	}
	info := keyValueReplication{ok: true, slaveOffset: -1}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		switch key {
		case "role":
			info.role = value
		case "master_link_status":
			info.linkUp = value == "up"
		case "master_sync_in_progress":
			info.syncing = value == "1"
		case "master_repl_offset":
			info.masterOffset, _ = strconv.ParseInt(value, 10, 64)
		case "slave_repl_offset":
			info.slaveOffset, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if info.slaveOffset < 0 {
		info.slaveOffset = info.masterOffset
	}
	return info
}

func (r keyValueReplication) isReplica() bool { return r.role == "slave" || r.role == "replica" }

func keyValueGenerationRoot(input planner.KeyValuePlan) string { return filepath.Dir(input.DataPath) }

func keyValueMarkerPath(input planner.KeyValuePlan, name string) string {
	return filepath.Join(keyValueGenerationRoot(input), "transition", name)
}

func writeKeyValueMarker(input planner.KeyValuePlan, name string, marker keyValueTransitionMarker) error {
	if err := os.MkdirAll(filepath.Dir(keyValueMarkerPath(input, name)), 0o755); err != nil {
		return err
	}
	marker.SchemaVersion = keyValueTransitionSchema
	return writeJSONAtomic(keyValueMarkerPath(input, name), marker, 0444)
}

// readKeyValueMarker returns the marker only when it belongs to this Plan and
// this exact transition; digest is checked when non-empty.
func readKeyValueMarker(input planner.KeyValuePlan, name, planID, phase, operationDigest string) (keyValueTransitionMarker, bool) {
	var marker keyValueTransitionMarker
	if err := readExactJSON(keyValueMarkerPath(input, name), &marker); err != nil {
		return keyValueTransitionMarker{}, false
	}
	ok := marker.SchemaVersion == keyValueTransitionSchema && marker.Phase == phase && marker.PlanID == planID &&
		marker.LogicalID == input.LogicalID && marker.CandidateGeneration == input.GenerationID && input.Previous != nil && marker.PreviousGeneration == input.Previous.ID &&
		(operationDigest == "" || marker.OperationDigest == operationDigest)
	return marker, ok
}

// keyValueTransitionObservedInput is the identity a transition operation
// observes: the candidate for preparation, synchronization and the fence, the
// stable service afterwards.
func keyValueTransitionObservedInput(kind planner.OperationKind, input planner.KeyValuePlan) planner.KeyValuePlan {
	if kind.ObservesKeyValueCandidate() {
		return input.Candidate()
	}
	return input
}

func keyValueTransitionBase(ctx context.Context, kind planner.OperationKind, phase string, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths) (host.KeyValueOperationObservation, string) {
	observed, state := observeKeyValueGeneration(ctx, keyValueTransitionObservedInput(kind, input), record, paths, false)
	observed.TransitionPhase = phase
	return observed, state
}

func keyValueUncertain(observed host.KeyValueOperationObservation, reason, action string) (json.RawMessage, error) {
	observed.Status, observed.Verified = "uncertain", false
	observed.FailureCategory, observed.Reason, observed.RecoveryAction = "ambiguous-data-bearing-state", reason, action
	observed.KeyValue.Health, observed.KeyValue.Reason, observed.KeyValue.RecoveryAction = "uncertain", reason, action
	encoded, err := json.Marshal(observed)
	if err != nil {
		return nil, err
	}
	return encoded, fmt.Errorf("%w: %s", errUncertainRecovery, reason)
}

// keyValueTransitionUser validates the resolved credential and returns only the
// application user name; the password is never used by the executor here.
func keyValueTransitionUser(values map[string]string, input planner.KeyValuePlan) (string, error) {
	secret, err := validateKeyValueSensitiveValues(values, input)
	if err != nil {
		return "", err
	}
	user, _, err := parseKeyValueSecret(secret, input.Port)
	return user, err
}

// previousKeyValue loads and exactly observes the verified active generation a
// transition replaces, and requires its deterministic binding to equal the
// transition's so the keys can be read back from the new generation.
func previousKeyValue(ctx context.Context, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths) (planner.KeyValuePlan, error) {
	var generation keyValueGenerationRecord
	path := filepath.Join(paths.environmentHome, "services", "valkey", "generations", input.Previous.ID, "generation.json")
	if err := readExactJSON(path, &generation); err != nil || generation.SchemaVersion != keyValueGenerationSchema || !generation.Verified || generation.KeyValue.GenerationID != input.Previous.ID || generation.KeyValue.LogicalID != input.LogicalID {
		return planner.KeyValuePlan{}, errors.New("previous Key-Value Store generation has no verified generation record")
	}
	if !reflect.DeepEqual(generation.KeyValue.Binding, input.Binding) {
		return planner.KeyValuePlan{}, errors.New("transition deterministic keys differ from the active generation's; transition with the revision that prepared them")
	}
	if _, state := observeKeyValueGeneration(ctx, generation.KeyValue, record, paths, false); state != "satisfied" {
		return planner.KeyValuePlan{}, errors.New("previous Key-Value Store generation is not an exact healthy active generation")
	}
	return generation.KeyValue, nil
}

func observeKeyValueTransitionOperation(ctx context.Context, planID string, planned planner.Operation, record bootstrapRecord, paths executionPaths) (host.OperationObservation, error) {
	input := *planned.Input.KeyValue
	digest, err := planner.OperationDigest(planned)
	if err != nil {
		return host.OperationObservation{}, err
	}
	observed, state := keyValueTransitionBase(ctx, planned.Kind, string(planned.Kind), input, record, paths)
	switch planned.Kind {
	case planner.PrepareKeyValueCandidate:
	case planner.SynchronizeKeyValueCandidate:
		if state == "satisfied" {
			replication := keyValueReplicationInfo(ctx, record, input.Candidate().Container)
			observed.Synchronization = "replicaof-initial-sync"
			observed.Replication = &host.KeyValueReplicationOffsets{LinkUp: replication.linkUp, CandidateOffset: replication.slaveOffset}
			if !replication.isReplica() || !replication.linkUp || replication.syncing {
				state = "pending"
			}
		}
	case planner.FenceKeyValueWrites:
		marker, ok := readKeyValueMarker(input, "fence.json", planID, keyValueFencePhase, digest)
		switch {
		case ok && state == "satisfied":
			if rejected, absent := fenceProbe(ctx, record, input.Previous.Container, input.LogicalID+"/fence-probe"); !rejected || !absent {
				state = "unknown"
				break
			}
			observed.WriteFence, observed.Synchronization, observed.Replication, observed.Fence = marker.WriteFence, marker.Synchronization, marker.Replication, marker.Fence
		case state == "satisfied":
			state = "pending"
		}
	case planner.SwitchKeyValueAuthority, planner.VerifyKeyValueActive, planner.RetainKeyValuePrevious:
		observed, state = observeKeyValueAfterSwitch(ctx, planID, planned.Kind, digest, input, record, paths)
	}
	evidence, err := json.Marshal(observed)
	if err != nil {
		return host.OperationObservation{}, err
	}
	return host.OperationObservation{State: state, Evidence: evidence}, nil
}

// observeKeyValueAfterSwitch observes the stable generation for the phases
// after authority moved. A switch interrupted mid-way is "unknown": the data
// bearing state is ambiguous and needs an operator.
func observeKeyValueAfterSwitch(ctx context.Context, planID string, kind planner.OperationKind, digest string, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths) (host.KeyValueOperationObservation, string) {
	observed, state := observeKeyValueGeneration(ctx, input, record, paths, false)
	observed.TransitionPhase = string(kind)
	switchMarker, switched := readKeyValueMarker(input, "authority-switch.json", planID, keyValueSwitchPhase, "")
	if kind == planner.SwitchKeyValueAuthority {
		switchMarker, switched = readKeyValueMarker(input, "authority-switch.json", planID, keyValueSwitchPhase, digest)
	}
	if !switched {
		// Not switched yet: pending only while the previous generation is
		// still the running active one and no mutation has begun, or when the
		// switch completed but its evidence marker was never written.
		if state == "pending" && keyValuePreviousStillActive(ctx, record, input) {
			return observed, "pending"
		}
		if switchedWithoutMarker(ctx, planID, input, record, paths) {
			return observed, "pending"
		}
		return observed, "unknown"
	}
	observed.Replication = switchMarker.Replication
	observed.Synchronization = "authority-switched-after-write-fence"
	if state != "satisfied" {
		return observed, "unknown"
	}
	switch kind {
	case planner.VerifyKeyValueActive:
		observed.Synchronization = "verified-after-authority-switch"
		if _, ok := readKeyValueMarker(input, "active-verification.json", planID, keyValueVerifyPhase, digest); !ok {
			return observed, "pending"
		}
	case planner.RetainKeyValuePrevious:
		observed.Synchronization = "retained-forward-only"
		retained, ok := readKeyValueRetention(input, planID, digest)
		if !ok {
			return observed, "pending"
		}
		previous := *input.Previous
		observed.Previous, observed.RetainedUntil = &previous, retained.RetainedUntil
		observed.StoreRollbackGuarantee, observed.TransitionCleanupPolicy = input.Transition.StoreRollbackGuarantee, input.TransitionCleanupPolicy
	}
	return observed, "satisfied"
}

// switchedWithoutMarker recognizes a switch that finished every mutation (the
// previous record is retired, the candidate resources are gone, the stable
// generation is an exact healthy master) but crashed before recording its
// authority-switch marker. The recorded fence proves the offsets.
func switchedWithoutMarker(ctx context.Context, planID string, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths) bool {
	previousRetired := filepath.Join(paths.environmentHome, "services", "valkey", "generations", input.Previous.ID, "generation.previous.json")
	candidate := input.Candidate()
	if _, err := os.Stat(previousRetired); err != nil {
		return false
	}
	for _, path := range []string{keyValueRecordPath(candidate), candidate.QuadletPath} {
		if _, err := os.Stat(path); err == nil {
			return false
		}
	}
	if fence, ok := readKeyValueMarker(input, "fence.json", planID, keyValueFencePhase, ""); !ok || fence.Replication == nil {
		return false
	}
	_, state := observeKeyValueGeneration(ctx, input, record, paths, true)
	role := keyValueReplicationInfo(ctx, record, input.Container)
	return state == "satisfied" && role.ok && !role.isReplica()
}

// keyValuePreviousStillActive is true only while the stable Quadlet still
// names the previous image and its service is running, i.e. before any
// authority-switch mutation.
func keyValuePreviousStillActive(ctx context.Context, record bootstrapRecord, input planner.KeyValuePlan) bool {
	quadlet, err := os.ReadFile(input.QuadletPath)
	if err != nil || !strings.Contains(string(quadlet), input.Previous.ImageReference) {
		return false
	}
	out, _ := keyValueRun(ctx, record, "systemctl", "--user", "is-active", input.Previous.ServiceUnit)
	return strings.TrimSpace(string(out)) == "active"
}

func applyKeyValueTransitionOperation(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths, sensitiveValues map[string]string) (json.RawMessage, error) {
	if err := validateKeyValueOperation(planned, record, paths); err != nil {
		return nil, err
	}
	input := *planned.Input.KeyValue
	user, err := keyValueTransitionUser(sensitiveValues, input)
	if err != nil {
		return nil, err
	}
	switch planned.Kind {
	case planner.PrepareKeyValueCandidate:
		return applyKeyValueCandidate(ctx, input, record, paths)
	case planner.SynchronizeKeyValueCandidate:
		return applyKeyValueSynchronization(ctx, input, record, paths)
	case planner.FenceKeyValueWrites:
		return applyKeyValueFence(ctx, input, claim, record, paths, user)
	case planner.SwitchKeyValueAuthority:
		return applyKeyValueAuthoritySwitch(ctx, input, claim, record, paths)
	case planner.VerifyKeyValueActive:
		return applyKeyValueActiveVerification(ctx, input, claim, record, paths)
	default:
		return applyKeyValuePreviousRetention(ctx, input, claim, record, paths)
	}
}

func applyKeyValueCandidate(ctx context.Context, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths) (json.RawMessage, error) {
	candidate := input.Candidate()
	if observed, state := keyValueTransitionBase(ctx, planner.PrepareKeyValueCandidate, string(planner.PrepareKeyValueCandidate), input, record, paths); state == "satisfied" {
		return json.Marshal(observed)
	} else if state == "unknown" {
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New("existing candidate Key-Value Store resources do not match the approved generation")
	}
	if _, err := previousKeyValue(ctx, input, record, paths); err != nil {
		return nil, err
	}
	uid, gid := accountUID(record.Account), accountGID(record.Account)
	if uid <= 0 || gid <= 0 {
		return nil, errors.New("Environment account identity is unavailable")
	}
	generationRoot := keyValueGenerationRoot(candidate)
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(generationRoot), 0755}, {generationRoot, 0755}} {
		if err := ensureDirectory(d.path, d.mode, 0, 0); err != nil {
			return nil, err
		}
	}
	if err := ensureEnvironmentDataDirectory(candidate.DataPath, record.Account, uid, gid); err != nil {
		return nil, err
	}
	entrypointPath := keyValueEntrypointPath(candidate)
	if err := installExactFile(entrypointPath, []byte(keyValueCredentialEntrypoint(true)), 0755, 0, 0); err != nil {
		return nil, err
	}
	if err := installExactFile(candidate.QuadletPath, []byte(renderKeyValueQuadlet(candidate, entrypointPath)), 0644, 0, 0); err != nil {
		return nil, err
	}
	for _, step := range []struct {
		message string
		args    []string
	}{
		{"pull approved candidate Valkey image", []string{"podman", "pull", candidate.ImageReference}},
		{"verify approved candidate Valkey image", []string{"podman", "image", "inspect", candidate.ImageReference}},
		{"own candidate Valkey data for the container user", []string{"podman", "unshare", "chown", "-R", "999:999", candidate.DataPath}},
		{"reload Environment user units", []string{"systemctl", "--user", "daemon-reload"}},
		{"start candidate Valkey service", []string{"systemctl", "--user", "restart", candidate.ServiceUnit}},
	} {
		if _, err := keyValueRun(ctx, record, step.args[0], step.args[1:]...); err != nil {
			return nil, errors.New(step.message)
		}
	}
	if err := waitForKeyValueService(ctx, record, candidate); err != nil {
		return nil, err
	}
	if err := writeJSONAtomic(keyValueRecordPath(candidate), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: canonicalKeyValue(candidate)}, 0444); err != nil {
		return nil, errors.New("record candidate Key-Value Store generation")
	}
	observed, state := observeKeyValueGeneration(ctx, candidate, record, paths, true)
	observed.TransitionPhase = string(planner.PrepareKeyValueCandidate)
	encoded, err := json.Marshal(observed)
	if err != nil {
		return nil, err
	}
	if state != "satisfied" {
		return encoded, errors.New("prepared candidate Key-Value Store failed exact verification")
	}
	if err := writeJSONAtomic(keyValueRecordPath(candidate), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: canonicalKeyValue(candidate), Verified: true}, 0444); err != nil {
		return encoded, errors.New("mark candidate Key-Value Store generation verified")
	}
	return encoded, nil
}

func applyKeyValueSynchronization(ctx context.Context, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths) (json.RawMessage, error) {
	candidate := input.Candidate()
	observed, state := keyValueTransitionBase(ctx, planner.SynchronizeKeyValueCandidate, keyValueSyncPhase, input, record, paths)
	if state != "satisfied" {
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New("candidate Key-Value Store generation is not prepared for synchronization")
	}
	if _, err := previousKeyValue(ctx, input, record, paths); err != nil {
		return nil, err
	}
	// The host-network candidate reaches the active generation's loopback listener directly.
	if out, err := keyValueExec(ctx, record, candidate.Container, "REPLICAOF", input.Previous.ListenAddress, strconv.Itoa(input.Previous.Port)); err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "OK") {
		return nil, errors.New("start candidate replication from the active generation")
	}
	deadline := time.Now().Add(keyValueSyncBudget)
	var replication keyValueReplication
	for {
		replication = keyValueReplicationInfo(ctx, record, candidate.Container)
		if replication.ok && replication.isReplica() && replication.linkUp && !replication.syncing {
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("candidate replication link did not come up within the synchronization bound")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	observed, _ = keyValueTransitionBase(ctx, planner.SynchronizeKeyValueCandidate, keyValueSyncPhase, input, record, paths)
	observed.Synchronization = "replicaof-initial-sync"
	observed.Replication = &host.KeyValueReplicationOffsets{LinkUp: true, CandidateOffset: replication.slaveOffset}
	return json.Marshal(observed)
}

// fenceProbe proves the application user cannot write: the probe write must be
// rejected with NOPERM and never stored.
func fenceProbe(ctx context.Context, record bootstrapRecord, container, key string) (rejected, absent bool) {
	out, _ := keyValueExec(ctx, record, container, "SET", key, "fence-probe")
	rejected = strings.Contains(string(out), "NOPERM") && !strings.HasPrefix(strings.TrimSpace(string(out)), "OK")
	exists, err := keyValueExec(ctx, record, container, "EXISTS", key)
	absent = err == nil && strings.TrimSpace(string(exists)) == "0"
	return rejected, absent
}

func parseKeyValueCount(out []byte) (int, error) {
	return strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "(integer)")))
}

// equalOffsets waits, inside the fence bound, for the candidate to reach the
// active generation's master_repl_offset over an up replication link.
func equalOffsets(ctx context.Context, record bootstrapRecord, active, candidate string) (host.KeyValueReplicationOffsets, error) {
	for {
		a := keyValueReplicationInfo(ctx, record, active)
		c := keyValueReplicationInfo(ctx, record, candidate)
		if a.ok && c.ok && c.isReplica() && c.linkUp && !c.syncing && a.masterOffset > 0 && a.masterOffset == c.slaveOffset {
			return host.KeyValueReplicationOffsets{LinkUp: true, ActiveOffset: a.masterOffset, CandidateOffset: c.slaveOffset}, nil
		}
		select {
		case <-ctx.Done():
			return host.KeyValueReplicationOffsets{}, errors.New("candidate replication offset did not equal the active master_repl_offset within the write-fence bound")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// fenceActiveWrites closes the active generation to application writes and
// proves it. The fence is bounded: everything must finish inside
// keyValueFenceBudget. CLIENT PAUSE is deliberately not used: it blocks writers
// instead of rejecting them and expires on its own, so a blocked write could be
// acknowledged on the old generation after authority switched.
func fenceActiveWrites(ctx context.Context, record bootstrapRecord, input planner.KeyValuePlan, user string) (*host.KeyValueFenceEvidence, host.KeyValueReplicationOffsets, error) {
	started := time.Now()
	fenceCtx, cancel := context.WithTimeout(ctx, keyValueFenceBudget)
	defer cancel()
	active, candidate := input.Previous.Container, input.Candidate().Container
	if out, err := keyValueExec(fenceCtx, record, active, "ACL", "SETUSER", user, "-@write"); err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "OK") {
		return nil, host.KeyValueReplicationOffsets{}, errKeyValueFenceNotApplied
	}
	// TYPE filters leave the candidate's replication link (a replica client) alone.
	killed := 0
	for _, clientType := range []string{"normal", "pubsub"} {
		out, err := keyValueExec(fenceCtx, record, active, "CLIENT", "KILL", "USER", user, "TYPE", clientType)
		count, parseErr := parseKeyValueCount(out)
		if err != nil || parseErr != nil {
			return nil, host.KeyValueReplicationOffsets{}, errors.New("revoked application writes but could not prove client termination")
		}
		killed += count
	}
	rejected, absent := fenceProbe(fenceCtx, record, active, input.LogicalID+"/fence-probe")
	offsets, offsetErr := equalOffsets(fenceCtx, record, active, candidate)
	evidence := &host.KeyValueFenceEvidence{WriteRevoked: rejected, ClientsKilled: killed, ProbeRejected: rejected, ProbeAbsent: absent, ElapsedMillis: int(time.Since(started) / time.Millisecond)}
	if !rejected || !absent {
		return evidence, offsets, errors.New("fenced active Key-Value Store writes but the probe write was not rejected or left a key")
	}
	if offsetErr != nil {
		return evidence, offsets, fmt.Errorf("fenced active Key-Value Store writes but lossless offset evidence could not be proven: %w", offsetErr)
	}
	return evidence, offsets, nil
}

// provenLosslessState re-proves, immediately before authority moves, that the
// active generation still rejects writes and the candidate offset equals its
// master_repl_offset over an up replication link.
func provenLosslessState(ctx context.Context, record bootstrapRecord, input planner.KeyValuePlan) (host.KeyValueReplicationOffsets, error) {
	rejected, absent := fenceProbe(ctx, record, input.Previous.Container, input.LogicalID+"/fence-probe")
	if !rejected || !absent {
		return host.KeyValueReplicationOffsets{}, errors.New("active Key-Value Store writes are no longer fenced; refusing to switch authority")
	}
	equalCtx, cancel := context.WithTimeout(ctx, keyValueEqualBudget)
	defer cancel()
	offsets, err := equalOffsets(equalCtx, record, input.Previous.Container, input.Candidate().Container)
	if err != nil {
		return host.KeyValueReplicationOffsets{}, errors.New("authority switch requires the candidate offset to equal the active master_repl_offset over an up link")
	}
	return offsets, nil
}

func applyKeyValueFence(ctx context.Context, input planner.KeyValuePlan, claim authority.Claim, record bootstrapRecord, paths executionPaths, user string) (json.RawMessage, error) {
	candidate := input.Candidate()
	digest := claim.OperationDigest
	if marker, ok := readKeyValueMarker(input, "fence.json", claim.PlanID, keyValueFencePhase, digest); ok {
		recorded, state := keyValueTransitionBase(ctx, planner.FenceKeyValueWrites, keyValueFencePhase, input, record, paths)
		// The ACL revocation is in memory: an active-service restart reopens
		// writes, so a recorded fence is only reused while it still holds.
		if rejected, absent := fenceProbe(ctx, record, input.Previous.Container, input.LogicalID+"/fence-probe"); state == "satisfied" && rejected && absent {
			recorded.WriteFence, recorded.Synchronization, recorded.Replication, recorded.Fence = marker.WriteFence, marker.Synchronization, marker.Replication, marker.Fence
			return json.Marshal(recorded)
		}
	}
	base, state := keyValueTransitionBase(ctx, planner.FenceKeyValueWrites, keyValueFencePhase, input, record, paths)
	if state != "satisfied" {
		encoded, _ := json.Marshal(base)
		return encoded, errors.New("candidate Key-Value Store generation is not prepared for the write fence")
	}
	if replication := keyValueReplicationInfo(ctx, record, candidate.Container); !replication.ok || !replication.isReplica() || !replication.linkUp {
		return nil, errors.New("candidate replication link is not up; synchronize before the write fence")
	}
	evidence, offsets, fenceErr := fenceActiveWrites(ctx, record, input, user)
	if errors.Is(fenceErr, errKeyValueFenceNotApplied) {
		return nil, fenceErr
	}
	if fenceErr != nil {
		base.Fence = evidence
		return keyValueUncertain(base, fenceErr.Error(), "inspect the active and candidate Valkey generations, the application ACL user and replication offsets, then resume or explicitly recover the approved transition")
	}
	marker := keyValueTransitionMarker{
		Phase: keyValueFencePhase, PlanID: claim.PlanID, OperationID: claim.OperationID, OperationDigest: digest, LogicalID: input.LogicalID,
		PreviousGeneration: input.Previous.ID, CandidateGeneration: input.GenerationID, Synchronization: "offset-equal-after-write-fence",
		WriteFence: input.Transition.WriteFence, Replication: &offsets, Fence: evidence,
	}
	base, _ = keyValueTransitionBase(ctx, planner.FenceKeyValueWrites, keyValueFencePhase, input, record, paths)
	base.WriteFence, base.Synchronization, base.Replication, base.Fence = marker.WriteFence, marker.Synchronization, &offsets, evidence
	if err := writeKeyValueMarker(input, "fence.json", marker); err != nil {
		return keyValueUncertain(base, "fenced active Key-Value Store writes but the fence evidence could not be recorded: "+err.Error(), "inspect the active and candidate Valkey generations, then resume or explicitly recover the approved transition")
	}
	return json.Marshal(base)
}

func applyKeyValueAuthoritySwitch(ctx context.Context, input planner.KeyValuePlan, claim authority.Claim, record bootstrapRecord, paths executionPaths) (json.RawMessage, error) {
	candidate := input.Candidate()
	digest := claim.OperationDigest
	if observed, state := observeKeyValueAfterSwitch(ctx, claim.PlanID, planner.SwitchKeyValueAuthority, digest, input, record, paths); state == "satisfied" {
		return json.Marshal(observed)
	}
	if switchedWithoutMarker(ctx, claim.PlanID, input, record, paths) {
		fence, _ := readKeyValueMarker(input, "fence.json", claim.PlanID, keyValueFencePhase, "")
		observed, _ := observeKeyValueGeneration(ctx, input, record, paths, true)
		observed.TransitionPhase, observed.Synchronization, observed.Replication = keyValueSwitchPhase, "authority-switched-after-write-fence", fence.Replication
		if err := writeJSONAtomic(keyValueRecordPath(input), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: canonicalKeyValue(input), Verified: true}, 0444); err != nil {
			return keyValueUncertain(observed, "mark the authoritative generation verified: "+err.Error(), "inspect the authoritative Valkey generation and its records before resuming the authority switch")
		}
		marker := keyValueTransitionMarker{
			Phase: keyValueSwitchPhase, PlanID: claim.PlanID, OperationID: claim.OperationID, OperationDigest: digest, LogicalID: input.LogicalID,
			PreviousGeneration: input.Previous.ID, CandidateGeneration: input.GenerationID, Synchronization: observed.Synchronization, Replication: fence.Replication,
		}
		if err := writeKeyValueMarker(input, "authority-switch.json", marker); err != nil {
			return keyValueUncertain(observed, "record authority-switch evidence: "+err.Error(), "inspect the authoritative Valkey generation and its records before resuming the authority switch")
		}
		return json.Marshal(observed)
	}
	failBase := func() host.KeyValueOperationObservation {
		base, _ := observeKeyValueGeneration(ctx, input, record, paths, false)
		base.TransitionPhase = keyValueSwitchPhase
		return base
	}
	// Preconditions: nothing is mutated until the fence, the candidate and the
	// lossless offset evidence are all re-proven.
	fence, ok := readKeyValueMarker(input, "fence.json", claim.PlanID, keyValueFencePhase, "")
	if !ok || fence.Fence == nil || fence.Replication == nil {
		encoded, _ := json.Marshal(failBase())
		return encoded, errors.New("authority switch requires this Plan's recorded write fence")
	}
	if _, state := observeKeyValueGeneration(ctx, candidate, record, paths, false); state != "satisfied" {
		encoded, _ := json.Marshal(failBase())
		return encoded, errors.New("authority switch cannot verify the prepared candidate generation")
	}
	offsets, err := provenLosslessState(ctx, record, input)
	if err != nil {
		encoded, _ := json.Marshal(failBase())
		return encoded, err
	}

	uncertain := func(reason string) (json.RawMessage, error) {
		base, _ := observeKeyValueGeneration(ctx, input, record, paths, false)
		base.TransitionPhase, base.Synchronization, base.Replication = keyValueSwitchPhase, "authority-switch-post-mutation", &offsets
		return keyValueUncertain(base, reason, "inspect the previous and candidate Valkey services, the authoritative Quadlet, generation records and data paths before resuming or explicitly recovering the authority switch")
	}
	if _, err := keyValueRun(ctx, record, "systemctl", "--user", "stop", input.Previous.ServiceUnit); err != nil {
		return uncertain("stop the previous active Key-Value Store service: " + errText(err))
	}
	if out, err := keyValueExec(ctx, record, candidate.Container, "REPLICAOF", "NO", "ONE"); err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "OK") {
		return uncertain("promote the candidate generation to master")
	}
	if replication := keyValueReplicationInfo(ctx, record, candidate.Container); !replication.ok || replication.isReplica() || replication.masterOffset < offsets.ActiveOffset {
		return uncertain("candidate promotion did not preserve the replication offset")
	}
	if _, err := keyValueRun(ctx, record, "systemctl", "--user", "stop", candidate.ServiceUnit); err != nil {
		return uncertain("stop the promoted candidate service: " + errText(err))
	}
	if err := replaceManagedFile(input.QuadletPath, []byte(renderKeyValueQuadlet(input, keyValueEntrypointPath(input))), 0644, 0, 0); err != nil {
		return uncertain("write the authoritative Key-Value Store Quadlet: " + err.Error())
	}
	for _, step := range []struct {
		message string
		args    []string
	}{
		{"reload Environment user units", []string{"systemctl", "--user", "daemon-reload"}},
		{"start the authoritative Key-Value Store service", []string{"systemctl", "--user", "restart", input.ServiceUnit}},
	} {
		if _, err := keyValueRun(ctx, record, step.args[0], step.args[1:]...); err != nil {
			return uncertain(step.message + ": " + errText(err))
		}
	}
	if err := waitForKeyValueService(ctx, record, input); err != nil {
		return uncertain("wait for the authoritative Key-Value Store service: " + err.Error())
	}
	// The previous record leaves the active-generation glob; the candidate
	// record, Quadlet and entrypoint go away so the candidate unit cannot
	// restart on the now authoritative data directory.
	previousRecord := filepath.Join(paths.environmentHome, "services", "valkey", "generations", input.Previous.ID, "generation.json")
	if err := os.Rename(previousRecord, strings.TrimSuffix(previousRecord, ".json")+".previous.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return uncertain("retire the previous generation record: " + err.Error())
	}
	if err := writeJSONAtomic(keyValueRecordPath(input), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: canonicalKeyValue(input)}, 0444); err != nil {
		return uncertain("record the authoritative generation: " + err.Error())
	}
	for _, path := range []string{keyValueRecordPath(candidate), candidate.QuadletPath, keyValueEntrypointPath(candidate)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return uncertain("remove candidate transition resources: " + err.Error())
		}
	}
	if _, err := keyValueRun(ctx, record, "systemctl", "--user", "daemon-reload"); err != nil {
		return uncertain("reload Environment user units after candidate cleanup: " + errText(err))
	}
	observed, state := observeKeyValueGeneration(ctx, input, record, paths, true)
	observed.TransitionPhase, observed.Synchronization, observed.Replication = keyValueSwitchPhase, "authority-switched-after-write-fence", &offsets
	if role := keyValueReplicationInfo(ctx, record, input.Container); state != "satisfied" || !role.ok || role.isReplica() {
		return uncertain("authoritative Key-Value Store generation failed exact verification")
	}
	if err := writeJSONAtomic(keyValueRecordPath(input), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: canonicalKeyValue(input), Verified: true}, 0444); err != nil {
		return uncertain("mark the authoritative generation verified: " + err.Error())
	}
	marker := keyValueTransitionMarker{
		Phase: keyValueSwitchPhase, PlanID: claim.PlanID, OperationID: claim.OperationID, OperationDigest: digest, LogicalID: input.LogicalID,
		PreviousGeneration: input.Previous.ID, CandidateGeneration: input.GenerationID, Synchronization: observed.Synchronization, Replication: &offsets,
	}
	if err := writeKeyValueMarker(input, "authority-switch.json", marker); err != nil {
		return uncertain("record authority-switch evidence: " + err.Error())
	}
	return json.Marshal(observed)
}

// errText never includes command output, which could carry credential-bearing text.
func errText(err error) string {
	if err != nil {
		return err.Error()
	}
	return "unexpected output"
}

func applyKeyValueActiveVerification(ctx context.Context, input planner.KeyValuePlan, claim authority.Claim, record bootstrapRecord, paths executionPaths) (json.RawMessage, error) {
	switchMarker, switched := readKeyValueMarker(input, "authority-switch.json", claim.PlanID, keyValueSwitchPhase, "")
	observed, state := observeKeyValueGeneration(ctx, input, record, paths, false)
	observed.TransitionPhase, observed.Synchronization, observed.Replication = keyValueVerifyPhase, "verified-after-authority-switch", switchMarker.Replication
	role := keyValueReplicationInfo(ctx, record, input.Container)
	if !switched || state != "satisfied" || !role.ok || role.isReplica() {
		observed.Status, observed.Verified = "failed", false
		observed.FailureCategory, observed.Reason = "deterministic-keys", "active Key-Value Store authority did not prove a verified master with the planned deterministic keys after the switch"
		observed.RecoveryAction = "inspect the active Valkey generation and deterministic workload keys before resuming or explicitly recovering the transition"
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New(observed.Reason)
	}
	marker := keyValueTransitionMarker{
		Phase: keyValueVerifyPhase, PlanID: claim.PlanID, OperationID: claim.OperationID, OperationDigest: claim.OperationDigest, LogicalID: input.LogicalID,
		PreviousGeneration: input.Previous.ID, CandidateGeneration: input.GenerationID, Synchronization: observed.Synchronization,
	}
	if err := writeKeyValueMarker(input, "active-verification.json", marker); err != nil {
		return keyValueUncertain(observed, "record active Key-Value Store verification evidence after switch: "+err.Error(), "inspect the active Valkey generation and deterministic workload keys before resuming or explicitly recovering the transition")
	}
	return json.Marshal(observed)
}

type keyValueRetentionRecord struct {
	SchemaVersion           string                        `json:"schemaVersion"`
	PlanID                  string                        `json:"planId"`
	OperationID             string                        `json:"operationId"`
	OperationDigest         string                        `json:"operationDigest"`
	ActiveGeneration        string                        `json:"activeGeneration"`
	PreviousGeneration      string                        `json:"previousGeneration"`
	Previous                host.KeyValueGenerationStatus `json:"previous"`
	StoreRollbackGuarantee  string                        `json:"storeRollbackGuarantee"`
	RollbackClaim           string                        `json:"rollbackClaim"`
	RollbackWindow          rollbackwindow.Window         `json:"rollbackWindow"`
	TransitionCleanupPolicy string                        `json:"transitionCleanupPolicy"`
	RetainedUntil           string                        `json:"retainedUntil"`
}

func keyValueRetentionPath(input planner.KeyValuePlan) string {
	return filepath.Join(keyValueServiceRoot(input), "retained", input.Previous.ID+".json")
}

func readKeyValueRetention(input planner.KeyValuePlan, planID, digest string) (keyValueRetentionRecord, bool) {
	var retained keyValueRetentionRecord
	if err := readExactJSON(keyValueRetentionPath(input), &retained); err != nil {
		return keyValueRetentionRecord{}, false
	}
	ok := retained.SchemaVersion == keyValueRetentionSchema && retained.PlanID == planID && (digest == "" || retained.OperationDigest == digest) &&
		retained.ActiveGeneration == input.GenerationID && retained.PreviousGeneration == input.Previous.ID && reflect.DeepEqual(retained.Previous, *input.Previous) &&
		retained.StoreRollbackGuarantee == input.Transition.StoreRollbackGuarantee && retained.TransitionCleanupPolicy == input.TransitionCleanupPolicy && retained.RetainedUntil != ""
	if _, err := os.Stat(input.Previous.DataPath); err != nil {
		return keyValueRetentionRecord{}, false
	}
	return retained, ok
}

func applyKeyValuePreviousRetention(ctx context.Context, input planner.KeyValuePlan, claim authority.Claim, record bootstrapRecord, paths executionPaths) (json.RawMessage, error) {
	observed, state := observeKeyValueGeneration(ctx, input, record, paths, false)
	observed.TransitionPhase, observed.Synchronization = keyValueRetainPhase, "retained-forward-only"
	if _, ok := readKeyValueMarker(input, "active-verification.json", claim.PlanID, keyValueVerifyPhase, ""); !ok || state != "satisfied" {
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New("previous Key-Value Store generation retention requires the verified active generation")
	}
	if _, err := os.Stat(input.Previous.DataPath); err != nil {
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New("previous Key-Value Store generation data is not retained")
	}
	retained, exists := readKeyValueRetention(input, claim.PlanID, "")
	if !exists {
		duration, err := input.RollbackWindow.Duration()
		if err != nil {
			encoded, _ := json.Marshal(observed)
			return encoded, err
		}
		retained = keyValueRetentionRecord{
			SchemaVersion: keyValueRetentionSchema, PlanID: claim.PlanID, OperationID: claim.OperationID, OperationDigest: claim.OperationDigest,
			ActiveGeneration: input.GenerationID, PreviousGeneration: input.Previous.ID, Previous: *input.Previous,
			StoreRollbackGuarantee: input.Transition.StoreRollbackGuarantee, RollbackClaim: "none-retention-is-not-a-rollback",
			RollbackWindow: input.RollbackWindow, TransitionCleanupPolicy: input.TransitionCleanupPolicy,
			RetainedUntil: time.Now().UTC().Add(duration).Format(time.RFC3339Nano),
		}
		if err := os.MkdirAll(filepath.Dir(keyValueRetentionPath(input)), 0o755); err != nil {
			encoded, _ := json.Marshal(observed)
			return encoded, err
		}
		if err := writeJSONAtomic(keyValueRetentionPath(input), retained, 0444); err != nil {
			encoded, _ := json.Marshal(observed)
			return encoded, err
		}
	}
	previous := *input.Previous
	observed.Previous, observed.RetainedUntil = &previous, retained.RetainedUntil
	observed.StoreRollbackGuarantee, observed.TransitionCleanupPolicy = input.Transition.StoreRollbackGuarantee, input.TransitionCleanupPolicy
	return json.Marshal(observed)
}
