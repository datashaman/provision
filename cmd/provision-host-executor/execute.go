package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/operation"
	"provision/internal/planner"
)

var (
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	operationID    = regexp.MustCompile(`^op-[0-9]{2}$`)
	attemptID      = regexp.MustCompile(`^attempt-[0-9a-f]{32}$`)
	journalOutcome = regexp.MustCompile(`^(consumed|succeeded|failed|uncertain)$`)
)

type executionPaths struct {
	executor         string
	bootstrap        string
	publicKey        string
	authorityState   string
	artifactCache    string
	environmentHome  string
	systemdUnits     string
	systemd          systemdController
	caddy            caddyController
	healthTimeout    time.Duration
	sshHostPublicKey string
}

type hostFence struct {
	FencingToken int64  `json:"fencingToken"`
	AttemptID    string `json:"attemptId"`
}

type consumedAuthorization struct {
	SchemaVersion string            `json:"schemaVersion"`
	Claim         authority.Claim   `json:"claim"`
	Operation     planner.Operation `json:"operation"`
	Observation   json.RawMessage   `json:"observation,omitempty"`
	Outcome       string            `json:"outcome"`
	RecordedAt    time.Time         `json:"recordedAt"`
}

func runExecute(args []string) error {
	flags := flag.NewFlagSet("execute", flag.ContinueOnError)
	environment := flags.String("environment", "", "Environment identity")
	operator := flags.String("operator", "", "bootstrap operator")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !identifier.MatchString(*environment) || !username.MatchString(*operator) {
		return errors.New("execute requires valid --environment and --operator")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("operation envelope exceeds 1 MiB")
	}
	var envelope operation.Envelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return errors.New("operation envelope is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("operation envelope must contain exactly one JSON value")
	}
	paths := systemExecutionPaths(*environment)
	record, publicKey, err := loadExecutionAuthority(paths, *environment, *operator)
	if err != nil {
		return err
	}
	result, err := executeAuthorized(context.Background(), envelope, record, publicKey, paths, time.Now().UTC())
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(result)
}

func runObserveArtifact(args []string) error {
	flags := flag.NewFlagSet("observe-artifact", flag.ContinueOnError)
	environment := flags.String("environment", "", "Environment identity")
	operator := flags.String("operator", "", "bootstrap operator")
	digest := flags.String("digest", "", "planned Artifact digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !identifier.MatchString(*environment) || !username.MatchString(*operator) || !digestPattern.MatchString(*digest) {
		return errors.New("observe-artifact requires valid --environment, --operator, and --digest")
	}
	paths := systemExecutionPaths(*environment)
	if _, _, err := loadExecutionAuthority(paths, *environment, *operator); err != nil {
		return err
	}
	observed, err := host.ObserveArtifactCache(paths.artifactCache, *digest)
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(observed)
}

func runObserveOperation(args []string) error {
	flags := flag.NewFlagSet("observe-operation", flag.ContinueOnError)
	environment := flags.String("environment", "", "Environment identity")
	operator := flags.String("operator", "", "bootstrap operator")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !identifier.MatchString(*environment) || !username.MatchString(*operator) {
		return errors.New("observe-operation requires valid --environment and --operator")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("operation observation input exceeds 1 MiB")
	}
	var planned planner.Operation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&planned); err != nil {
		return errors.New("operation observation input is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("operation observation input must contain exactly one JSON value")
	}
	paths := systemExecutionPaths(*environment)
	record, _, err := loadExecutionAuthority(paths, *environment, *operator)
	if err != nil {
		return err
	}
	observed, err := observeCandidateOperation(context.Background(), planned, record, paths)
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(observed)
}

func systemExecutionPaths(environment string) executionPaths {
	return executionPaths{
		executor:         host.ExecutorPath,
		bootstrap:        "/etc/provision/bootstrap/" + environment + ".json",
		publicKey:        "/etc/provision/authority/" + environment + ".pub",
		authorityState:   "/var/lib/provision/authority/" + environment,
		artifactCache:    host.ArtifactCacheRoot,
		environmentHome:  "/var/lib/provision/environments/" + environment,
		systemdUnits:     "/etc/systemd/system",
		systemd:          commandSystemdController{},
		caddy:            newAdminCaddyController(),
		healthTimeout:    10 * time.Second,
		sshHostPublicKey: host.SSHHostPublicKeyPath,
	}
}

func loadExecutionAuthority(paths executionPaths, environment, operator string) (bootstrapRecord, ed25519.PublicKey, error) {
	if !rootOwned(paths.executor, 0755) || !rootOwned(paths.bootstrap, 0644) || !rootOwned(paths.publicKey, 0644) || !rootOwned(paths.authorityState, 0700) || !rootOwned(paths.artifactCache, 0755) {
		return bootstrapRecord{}, nil, errors.New("restricted executor authority files or directories are unsafe")
	}
	executorBytes, err := os.ReadFile(paths.executor)
	if err != nil {
		return bootstrapRecord{}, nil, errors.New("restricted executor identity cannot be read")
	}
	executorSum := sha256.Sum256(executorBytes)
	executorDigest := "sha256:" + hex.EncodeToString(executorSum[:])
	data, err := os.ReadFile(paths.bootstrap)
	if err != nil {
		return bootstrapRecord{}, nil, errors.New("bootstrap authority record cannot be read")
	}
	var record bootstrapRecord
	if json.Unmarshal(data, &record) != nil || record.SchemaVersion != "provision.dev/bootstrap/v2" || record.Environment != environment || record.Operator != operator || record.Account != "provision-"+environment || record.ExecutorDigest != executorDigest {
		return bootstrapRecord{}, nil, errors.New("bootstrap authority record does not match this executor and identity")
	}
	if !candidateStorageReady(record.Account, paths.environmentHome) || !rootOwned(paths.systemdUnits, 0755) {
		return bootstrapRecord{}, nil, errors.New("candidate execution directories or Environment account are unsafe")
	}
	publicKey, keyID, err := authority.LoadVerifier(paths.publicKey)
	if err != nil || keyID != record.AuthorityKeyID {
		return bootstrapRecord{}, nil, errors.New("authorization verifier does not match the bootstrap record")
	}
	return record, publicKey, nil
}

func executeAuthorized(ctx context.Context, envelope operation.Envelope, record bootstrapRecord, publicKey ed25519.PublicKey, paths executionPaths, now time.Time) (operation.Result, error) {
	claim := envelope.Authorization.Claim
	if envelope.SchemaVersion != operation.EnvelopeSchemaVersion {
		return operation.Result{}, errors.New("operation envelope schema is unsupported")
	}
	if err := authority.Verify(envelope.Authorization, publicKey, now); err != nil {
		return operation.Result{}, err
	}
	if !digestPattern.MatchString(claim.PlanID) || !deploymentIdentifier.MatchString(claim.Application) || claim.Environment != record.Environment || !operationID.MatchString(claim.OperationID) || !attemptID.MatchString(claim.AttemptID) || claim.FencingToken <= 0 {
		return operation.Result{}, errors.New("authorization contains invalid operation identity")
	}
	validTargetLocation := claim.Target.Local && claim.Target.Address == ""
	if !claim.Target.Local {
		validTargetLocation = (host.Target{Address: claim.Target.Address, User: claim.Target.Operator}).Validate() == nil
	}
	if !deploymentIdentifier.MatchString(claim.Target.Name) || !validTargetLocation || claim.Target.Operator != record.Operator || claim.Target.ExecutorDigest != record.ExecutorDigest {
		return operation.Result{}, errors.New("authorization targets a different Host Target")
	}
	if !claim.Target.Local {
		fingerprint, err := host.ReadSSHHostKeyFingerprint(paths.sshHostPublicKey)
		if err != nil || claim.Target.SSHHostKeyFingerprint != fingerprint {
			return operation.Result{}, errors.New("authorization targets a different SSH host identity")
		}
	}
	if claim.OperationID != envelope.Operation.ID || claim.OperationKind != string(envelope.Operation.Kind) {
		return operation.Result{}, errors.New("authorization does not identify the supplied operation")
	}
	digest, err := planner.OperationDigest(envelope.Operation)
	if err != nil || digest != claim.OperationDigest {
		return operation.Result{}, errors.New("authorized operation digest does not match its payload")
	}
	if envelope.Operation.Kind == planner.StageArtifact {
		if err := validateStageArtifact(envelope.Operation); err != nil {
			return operation.Result{}, err
		}
	} else if err := validateCandidateOperation(envelope.Operation, record, paths); err != nil {
		return operation.Result{}, err
	}

	var result operation.Result
	var consumedObservation json.RawMessage
	actionErr, err := withHostFence(paths, claim, envelope.Operation, now, &consumedObservation, func() error {
		var encoded json.RawMessage
		var actionErr error
		if envelope.Operation.Kind == planner.StageArtifact {
			if err := cleanupInterruptedArtifactStages(paths.authorityState, paths.artifactCache, claim.AttemptID); err != nil {
				actionErr = err
			} else {
				observation, err := stageArtifact(ctx, paths.artifactCache, claim.AttemptID, *envelope.Operation.Input.Artifact)
				if err != nil {
					actionErr = err
				} else {
					encoded, err = json.Marshal(observation)
					actionErr = err
				}
			}
		} else if envelope.Operation.Kind == planner.SwitchEndpoint {
			encoded, actionErr = applySwitchEndpoint(ctx, envelope.Operation, claim, record, paths, now)
		} else if envelope.Operation.Kind == planner.VerifyActive {
			encoded, actionErr = applyVerifyActive(ctx, envelope.Operation, claim, record, paths, now)
		} else {
			encoded, actionErr = applyCandidateOperation(ctx, envelope.Operation, record, paths, claim.AttemptID)
		}
		if len(encoded) != 0 {
			consumedObservation = append(consumedObservation[:0], encoded...)
			result = operation.Result{
				SchemaVersion: operation.ResultSchemaVersion,
				PlanID:        claim.PlanID,
				OperationID:   claim.OperationID,
				AttemptID:     claim.AttemptID,
				FencingToken:  claim.FencingToken,
				Outcome:       operation.OutcomeSucceeded,
				Observation:   encoded,
			}
		}
		if actionErr != nil {
			return actionErr
		}
		if len(encoded) == 0 {
			return errors.New("host operation returned no observation")
		}
		return nil
	})
	if err != nil {
		return operation.Result{}, err
	}
	if actionErr != nil {
		encoded := result.Observation
		if envelope.Operation.Kind == planner.StageArtifact {
			artifact := *envelope.Operation.Input.Artifact
			path, _ := host.ArtifactCachePath(paths.artifactCache, artifact.Digest)
			var encodeErr error
			encoded, encodeErr = json.Marshal(host.ArtifactObservation{
				Status: host.ArtifactFailed, Path: path, Digest: artifact.Digest, Reason: actionErr.Error(),
			})
			if encodeErr != nil {
				return operation.Result{}, encodeErr
			}
		}
		if len(encoded) == 0 {
			encoded = json.RawMessage(`{"status":"failed","reason":"host operation failed before producing a typed observation"}`)
		}
		outcome := operation.OutcomeFailed
		if errors.Is(actionErr, errUncertainRecovery) {
			outcome = operation.OutcomeUncertain
		}
		return operation.Result{
			SchemaVersion: operation.ResultSchemaVersion,
			PlanID:        claim.PlanID,
			OperationID:   claim.OperationID,
			AttemptID:     claim.AttemptID,
			FencingToken:  claim.FencingToken,
			Outcome:       outcome,
			Observation:   encoded,
		}, nil
	}
	return result, nil
}

func cleanupInterruptedArtifactStages(authorityState, cacheRoot, currentAttempt string) error {
	entries, err := os.ReadDir(authorityState)
	if err != nil {
		return errors.New("inspect prior host authorizations")
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		previousAttempt := strings.TrimSuffix(name, ".json")
		if previousAttempt == currentAttempt || !attemptID.MatchString(previousAttempt) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(authorityState, name))
		if err != nil {
			return errors.New("read prior host authorization")
		}
		var consumed consumedAuthorization
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&consumed); err != nil {
			return errors.New("prior host authorization is invalid")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || consumed.SchemaVersion != "provision.dev/consumed-authorization/v1alpha2" || consumed.Claim.AttemptID != previousAttempt || !journalOutcome.MatchString(consumed.Outcome) {
			return errors.New("prior host authorization is invalid")
		}
		if consumed.Claim.OperationKind != string(consumed.Operation.Kind) || consumed.Claim.OperationID != consumed.Operation.ID {
			return errors.New("prior host authorization is invalid")
		}
		if consumed.Operation.Kind != planner.StageArtifact {
			continue
		}
		temporary := filepath.Join(cacheRoot, "."+previousAttempt+".tmp")
		info, err := os.Lstat(temporary)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("inspect interrupted Artifact temporary entry")
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("interrupted Artifact temporary entry is unsafe")
		}
		if err := os.Remove(temporary); err != nil {
			return errors.New("remove interrupted Artifact temporary entry")
		}
	}
	return nil
}

func validateStageArtifact(planned planner.Operation) error {
	if planned.Kind != planner.StageArtifact || len(planned.DependsOn) != 0 || planned.Input.Artifact == nil || planned.Input.Generation != nil || planned.Input.Systemd != nil || planned.Input.Health != nil || planned.Input.Endpoint != nil || planned.Input.Previous != nil || planned.Input.Retention != nil {
		return errors.New("only the typed stageArtifact operation is enabled")
	}
	artifact := planned.Input.Artifact
	if !digestPattern.MatchString(artifact.Digest) {
		return errors.New("Artifact digest is invalid")
	}
	source, err := url.Parse(artifact.Source)
	if err != nil || source.Scheme != "https" || source.Host == "" || source.User != nil || source.RawQuery != "" || source.Fragment != "" {
		return errors.New("Artifact source must be an immutable HTTPS reference without credentials or query data")
	}
	return nil
}

func withHostFence(paths executionPaths, claim authority.Claim, planned planner.Operation, now time.Time, observation *json.RawMessage, action func() error) (error, error) {
	lock, err := os.OpenFile(filepath.Join(paths.authorityState, "executor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("open host authorization fence lock")
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.New("lock host authorization fence")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	fencePath := filepath.Join(paths.authorityState, "fence.json")
	var fence hostFence
	if data, err := os.ReadFile(fencePath); err == nil {
		if json.Unmarshal(data, &fence) != nil || fence.FencingToken <= 0 || !attemptID.MatchString(fence.AttemptID) {
			return nil, errors.New("host authorization fence is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("read host authorization fence")
	}
	if claim.FencingToken < fence.FencingToken || claim.FencingToken == fence.FencingToken && claim.AttemptID != fence.AttemptID {
		return nil, errors.New("authorization fencing token is stale")
	}
	if claim.FencingToken > fence.FencingToken {
		if err := writeJSONAtomic(fencePath, hostFence{FencingToken: claim.FencingToken, AttemptID: claim.AttemptID}, 0600); err != nil {
			return nil, err
		}
	}
	consumedPath := filepath.Join(paths.authorityState, claim.AttemptID+".json")
	consumed := consumedAuthorization{SchemaVersion: "provision.dev/consumed-authorization/v1alpha2", Claim: claim, Operation: planned, Outcome: "consumed", RecordedAt: now.UTC()}
	encoded, err := json.Marshal(consumed)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(consumedPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("authorization has already been consumed")
	}
	if err != nil {
		return nil, errors.New("record consumed authorization")
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return nil, errors.New("record consumed authorization")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, errors.New("sync consumed authorization")
	}
	if err := file.Close(); err != nil {
		return nil, errors.New("close consumed authorization")
	}

	actionErr := action()
	consumed.RecordedAt = time.Now().UTC()
	if observation != nil {
		consumed.Observation = append(consumed.Observation[:0], (*observation)...)
	}
	if actionErr != nil {
		if errors.Is(actionErr, errUncertainRecovery) {
			consumed.Outcome = "uncertain"
		} else {
			consumed.Outcome = "failed"
		}
	} else {
		consumed.Outcome = "succeeded"
	}
	if !journalOutcome.MatchString(consumed.Outcome) {
		return actionErr, errors.New("authorization outcome is invalid")
	}
	if err := writeJSONAtomic(consumedPath, consumed, 0600); err != nil {
		return actionErr, err
	}
	return actionErr, nil
}

func stageArtifact(ctx context.Context, cacheRoot, attempt string, artifact planner.ArtifactInput) (host.ArtifactObservation, error) {
	destination, err := host.ArtifactCachePath(cacheRoot, artifact.Digest)
	if err != nil {
		return host.ArtifactObservation{}, err
	}
	if observation, ok := verifyCachedArtifact(cacheRoot, artifact.Digest); ok {
		return observation, nil
	}
	temporary := filepath.Join(cacheRoot, "."+attempt+".tmp")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return host.ArtifactObservation{}, errors.New("create temporary Artifact cache entry")
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.Source, nil)
	if err != nil {
		return host.ArtifactObservation{}, errors.New("create Artifact request")
	}
	client := &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 || request.URL.Scheme != "https" || request.URL.User != nil {
				return errors.New("unsafe Artifact redirect")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return host.ArtifactObservation{}, errors.New("download Artifact")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > host.MaxArtifactBytes {
		return host.ArtifactObservation{}, errors.New("Artifact source returned an unsupported response")
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, host.MaxArtifactBytes+1))
	if err != nil || written > host.MaxArtifactBytes {
		return host.ArtifactObservation{}, errors.New("downloaded Artifact exceeds its safe limit")
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != artifact.Digest {
		return host.ArtifactObservation{}, errors.New("downloaded Artifact digest does not match the approved Plan")
	}
	if err := file.Sync(); err != nil {
		return host.ArtifactObservation{}, errors.New("sync staged Artifact")
	}
	if err := file.Close(); err != nil {
		return host.ArtifactObservation{}, errors.New("close staged Artifact")
	}
	if err := os.Chmod(temporary, 0644); err != nil {
		return host.ArtifactObservation{}, errors.New("secure staged Artifact permissions")
	}
	if err := os.Link(temporary, destination); err != nil {
		if observation, ok := verifyCachedArtifact(cacheRoot, artifact.Digest); ok {
			_ = os.Remove(temporary)
			removeTemporary = false
			return observation, nil
		}
		return host.ArtifactObservation{}, errors.New("commit staged Artifact to cache")
	}
	if err := os.Remove(temporary); err != nil {
		return host.ArtifactObservation{}, errors.New("remove temporary Artifact cache entry")
	}
	removeTemporary = false
	return host.ArtifactObservation{Status: host.ArtifactStaged, Path: destination, Digest: artifact.Digest, Size: written}, nil
}

func verifyCachedArtifact(cacheRoot, expected string) (host.ArtifactObservation, bool) {
	observed, err := host.ObserveArtifactCache(cacheRoot, expected)
	return observed, err == nil && observed.Status == host.ArtifactAlreadyPresent
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if errors.Is(err, os.ErrExist) {
		_ = os.Remove(temporary)
		file, err = os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	}
	if err != nil {
		return errors.New("create durable authorization record")
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return errors.New("write durable authorization record")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return errors.New("sync durable authorization record")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return errors.New("close durable authorization record")
	}
	if err := os.Chmod(temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return errors.New("secure durable authorization record")
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return errors.New("commit durable authorization record")
	}
	return nil
}
