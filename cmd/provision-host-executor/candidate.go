package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"provision/internal/host"
	"provision/internal/planner"
)

const generationManifestName = ".provision-generation.json"

var bundleExecutable = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type systemdController interface {
	Run(context.Context, ...string) ([]byte, error)
}

type commandSystemdController struct{}

func (commandSystemdController) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
}

func validateCandidateOperation(planned planner.Operation, record bootstrapRecord, paths executionPaths) error {
	if planned.Input.Artifact != nil || planned.Input.Retention != nil {
		return errors.New("candidate operation contains an unrelated typed input")
	}
	switch planned.Kind {
	case planner.InstallGeneration:
		if len(planned.DependsOn) != 1 || planned.Input.Generation == nil || planned.Input.Systemd != nil || planned.Input.Health != nil || planned.Input.Endpoint != nil || planned.Input.Previous != nil {
			return errors.New("installGeneration requires only its typed Generation input and one dependency")
		}
		return validateGenerationInput(*planned.Input.Generation, record, paths)
	case planner.StartCandidate:
		if len(planned.DependsOn) != 1 || planned.Input.Systemd == nil || planned.Input.Generation != nil || planned.Input.Health != nil || planned.Input.Endpoint != nil || planned.Input.Previous != nil {
			return errors.New("startCandidate requires only its typed systemd input and one dependency")
		}
		return validateSystemdInput(*planned.Input.Systemd, record, paths)
	case planner.VerifyCandidate:
		if len(planned.DependsOn) != 1 || planned.Input.Health == nil || planned.Input.Generation != nil || planned.Input.Systemd != nil || planned.Input.Endpoint != nil || planned.Input.Previous != nil {
			return errors.New("verifyCandidate requires only its typed Health Contract input and one dependency")
		}
		return validateHealthInput(*planned.Input.Health, record, paths)
	case planner.SwitchEndpoint:
		if len(planned.DependsOn) != 1 || planned.DependsOn[0] != "op-04" || planned.Input.Endpoint == nil || planned.Input.Generation != nil || planned.Input.Systemd != nil || planned.Input.Health != nil {
			return errors.New("switchEndpoint requires only its typed Endpoint input, optional planned previous Generation, and candidate-verification dependency")
		}
		return validateEndpointInput(*planned.Input.Endpoint, planned.Input.Previous, record, paths)
	default:
		return errors.New("host executor does not allow this operation kind")
	}
}

func validateGenerationReference(input planner.GenerationReference, record bootstrapRecord, paths executionPaths) error {
	expected := filepath.Join(paths.environmentHome, "releases", input.ID)
	if !deploymentIdentifier.MatchString(input.ID) || !deploymentIdentifier.MatchString(input.Revision) || !digestPattern.MatchString(input.ArtifactDigest) || input.Account != record.Account || input.ReleaseDirectory != expected {
		return errors.New("Generation input does not match the bootstrapped Environment or fixed release path")
	}
	return nil
}

func validateGenerationInput(input planner.GenerationInput, record bootstrapRecord, paths executionPaths) error {
	return validateGenerationReference(input.GenerationReference, record, paths)
}

func validateSystemdInput(input planner.SystemdInput, record bootstrapRecord, paths executionPaths) error {
	if err := validateGenerationReference(input.GenerationReference, record, paths); err != nil || !systemdUnit.MatchString(input.Unit) || !strings.HasPrefix(input.Unit, "provision-"+record.Environment+"-") || input.Port < 1024 || input.Port > 65535 {
		return errors.New("systemd candidate input does not match the bootstrapped Environment or fixed release path")
	}
	return nil
}

func validateHealthInput(input planner.HealthInput, record bootstrapRecord, paths executionPaths) error {
	if err := validateSystemdInput(systemdInputFromHealth(input), record, paths); err != nil {
		return errors.New("candidate Health Contract identity is invalid")
	}
	for _, path := range []string{input.LivenessPath, input.ReadinessPath, input.CandidateVerifyPath} {
		if !validProbePath(path) {
			return errors.New("candidate Health Contract path is invalid")
		}
	}
	return nil
}

func validProbePath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || len(path) > 256 || strings.ContainsAny(path, "?#\\") {
		return false
	}
	for index := 0; index < len(path); index++ {
		if path[index] <= ' ' || path[index] == 0x7f {
			return false
		}
	}
	return true
}

func observeCandidateOperation(ctx context.Context, planned planner.Operation, record bootstrapRecord, paths executionPaths) (host.OperationObservation, error) {
	if err := validateCandidateOperation(planned, record, paths); err != nil {
		return host.OperationObservation{}, err
	}
	var evidence any
	var state string
	switch planned.Kind {
	case planner.InstallGeneration:
		observed := observeGeneration(*planned.Input.Generation)
		evidence = observed
		switch observed.Status {
		case host.CandidateInstalled:
			state = "satisfied"
		case host.CandidateAbsent:
			state = "pending"
		default:
			state = "unknown"
		}
	case planner.StartCandidate:
		observed := observeSystemdCandidate(ctx, paths, *planned.Input.Systemd)
		evidence = observed
		switch observed.Status {
		case host.CandidateActive:
			state = "satisfied"
		case host.CandidateAbsent, host.CandidateInstalled:
			state = "pending"
		default:
			state = "unknown"
		}
	case planner.VerifyCandidate:
		observed := checkCandidateHealth(ctx, paths, *planned.Input.Health)
		evidence = observed
		// A healthy preflight is evidence, not completion. The signed verification
		// must execute so the host can durably authorize the later Endpoint switch.
		state = "pending"
	case planner.SwitchEndpoint:
		observed := observeEndpoint(ctx, paths, *planned.Input.Endpoint)
		evidence = observed
		if observed.Status == host.EndpointActive {
			state = "satisfied"
		} else if observed.Status == host.EndpointPending {
			state = "pending"
		} else {
			state = "unknown"
		}
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return host.OperationObservation{}, err
	}
	return host.OperationObservation{State: state, Evidence: encoded}, nil
}

func applyCandidateOperation(ctx context.Context, planned planner.Operation, record bootstrapRecord, paths executionPaths, attempt string) (json.RawMessage, error) {
	if err := validateCandidateOperation(planned, record, paths); err != nil {
		return nil, err
	}
	switch planned.Kind {
	case planner.InstallGeneration:
		observed, err := installGeneration(paths, *planned.Input.Generation, attempt)
		encoded, encodeErr := json.Marshal(observed)
		if encodeErr != nil {
			return nil, encodeErr
		}
		return encoded, err
	case planner.StartCandidate:
		observed, err := startCandidate(ctx, paths, *planned.Input.Systemd)
		encoded, encodeErr := json.Marshal(observed)
		if encodeErr != nil {
			return nil, encodeErr
		}
		return encoded, err
	case planner.VerifyCandidate:
		observed := waitForCandidateHealth(ctx, paths, *planned.Input.Health)
		encoded, err := json.Marshal(observed)
		if err != nil {
			return nil, err
		}
		if observed.Status != host.CandidateHealthy || !observed.SwitchEligible {
			if cleanupErr := cleanupCandidate(ctx, paths, *planned.Input.Health); cleanupErr != nil {
				observed.Reason += "; candidate cleanup: " + cleanupErr.Error()
			} else {
				observed.CandidateCleaned = true
			}
			encoded, err = json.Marshal(observed)
			if err != nil {
				return nil, err
			}
			return encoded, errors.New(observed.Reason)
		}
		return encoded, nil
	case planner.SwitchEndpoint:
		return nil, errors.New("switchEndpoint requires its signed Plan claim")
	default:
		return nil, errors.New("host executor does not allow this operation kind")
	}
}

func observeGeneration(input planner.GenerationInput) host.GenerationObservation {
	result := host.GenerationObservation{Status: host.CandidateAbsent, ID: input.ID, Revision: input.Revision, ArtifactDigest: input.ArtifactDigest, ReleaseDirectory: input.ReleaseDirectory}
	info, err := os.Lstat(input.ReleaseDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return result
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0755 || !ownedByExecutor(info) {
		result.Status = host.CandidateInvalid
		result.Reason = "Generation directory is not a safe directory"
		return result
	}
	manifestPath := filepath.Join(input.ReleaseDirectory, generationManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 || manifestInfo.Mode().Perm() != 0444 || !ownedByExecutor(manifestInfo) {
		result.Status = host.CandidateInvalid
		result.Reason = "Generation manifest is missing or unsafe"
		return result
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil || len(data) > 64<<10 {
		result.Status = host.CandidateInvalid
		result.Reason = "Generation manifest cannot be read"
		return result
	}
	var manifest host.GenerationManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&manifest)
	var extra any
	trailingErr := decoder.Decode(&extra)
	if decodeErr != nil || trailingErr != io.EOF || manifest.SchemaVersion != "provision.dev/generation/v1alpha1" || manifest.ID != input.ID || manifest.Revision != input.Revision || manifest.ArtifactDigest != input.ArtifactDigest || manifest.Account != input.Account || manifest.ReleaseDirectory != input.ReleaseDirectory || !bundleExecutable.MatchString(manifest.Executable) {
		result.Status = host.CandidateInvalid
		result.Reason = "Generation manifest does not match the approved Plan"
		return result
	}
	executable := filepath.Join(input.ReleaseDirectory, manifest.Executable)
	executableInfo, err := os.Lstat(executable)
	if err != nil || !executableInfo.Mode().IsRegular() || executableInfo.Mode()&os.ModeSymlink != 0 || executableInfo.Mode().Perm() != 0555 || !ownedByExecutor(executableInfo) {
		result.Status = host.CandidateInvalid
		result.Reason = "Generation executable is missing or unsafe"
		return result
	}
	result.Status = host.CandidateInstalled
	result.Executable = manifest.Executable
	return result
}

func installGeneration(paths executionPaths, input planner.GenerationInput, attempt string) (host.GenerationObservation, error) {
	if observed := observeGeneration(input); observed.Status == host.CandidateInstalled {
		return observed, nil
	} else if observed.Status != host.CandidateAbsent {
		return observed, errors.New(observed.Reason)
	}
	artifact, err := host.ObserveArtifactCache(paths.artifactCache, input.ArtifactDigest)
	if err != nil || artifact.Status != host.ArtifactAlreadyPresent {
		return failedGeneration(input, "approved Artifact is not present and verified in the host cache"), errors.New("approved Artifact is not present and verified in the host cache")
	}
	releaseRoot := filepath.Dir(input.ReleaseDirectory)
	if err := ensureReleaseRoot(releaseRoot); err != nil {
		return failedGeneration(input, "create release storage"), errors.New("create release storage")
	}
	temporary := filepath.Join(releaseRoot, "."+input.ID+"-"+attempt)
	if err := os.Mkdir(temporary, 0755); err != nil {
		return failedGeneration(input, "create temporary Generation directory"), errors.New("create temporary Generation directory")
	}
	if err := os.Chmod(temporary, 0755); err != nil {
		_ = os.Remove(temporary)
		return failedGeneration(input, "secure temporary Generation directory"), errors.New("secure temporary Generation directory")
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	executable, err := extractNativeBundle(artifact.Path, temporary)
	if err != nil {
		return failedGeneration(input, err.Error()), err
	}
	manifest := host.GenerationManifest{SchemaVersion: "provision.dev/generation/v1alpha1", ID: input.ID, Revision: input.Revision, ArtifactDigest: input.ArtifactDigest, Account: input.Account, ReleaseDirectory: input.ReleaseDirectory, Executable: executable}
	if err := writeJSONAtomic(filepath.Join(temporary, generationManifestName), manifest, 0444); err != nil {
		return failedGeneration(input, "write Generation manifest"), errors.New("write Generation manifest")
	}
	if err := os.Rename(temporary, input.ReleaseDirectory); err != nil {
		if observed := observeGeneration(input); observed.Status == host.CandidateInstalled {
			return observed, nil
		}
		return failedGeneration(input, "commit immutable Generation"), errors.New("commit immutable Generation")
	}
	committed = true
	observed := observeGeneration(input)
	if observed.Status != host.CandidateInstalled {
		return observed, errors.New("installed Generation failed verification")
	}
	return observed, nil
}

func failedGeneration(input planner.GenerationInput, reason string) host.GenerationObservation {
	return host.GenerationObservation{Status: host.CandidateFailed, ID: input.ID, Revision: input.Revision, ArtifactDigest: input.ArtifactDigest, ReleaseDirectory: input.ReleaseDirectory, Reason: reason}
}

func extractNativeBundle(artifactPath, destination string) (string, error) {
	file, err := os.Open(artifactPath)
	if err != nil {
		return "", errors.New("open staged Artifact")
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", errors.New("Artifact is not a gzip-compressed native bundle")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(io.LimitReader(gzipReader, host.MaxArtifactBytes+1))
	executable := ""
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("read native bundle")
		}
		if header.Typeflag != tar.TypeReg || !bundleExecutable.MatchString(header.Name) || header.Name == generationManifestName || header.Size < 1 || header.Size > host.MaxArtifactBytes || header.Mode&0111 == 0 || executable != "" {
			return "", errors.New("native bundle must contain exactly one executable regular file at its root")
		}
		destinationPath := filepath.Join(destination, header.Name)
		output, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
		if err != nil {
			return "", errors.New("create Generation executable")
		}
		written, copyErr := io.Copy(output, io.LimitReader(reader, header.Size+1))
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil || written != header.Size {
			return "", errors.New("extract Generation executable")
		}
		if err := os.Chmod(destinationPath, 0555); err != nil {
			return "", errors.New("secure Generation executable")
		}
		executable = header.Name
	}
	if executable == "" {
		return "", errors.New("native bundle has no executable")
	}
	return executable, nil
}

func observeSystemdCandidate(ctx context.Context, paths executionPaths, input planner.SystemdInput) host.SystemdObservation {
	result := host.SystemdObservation{Status: host.CandidateAbsent, GenerationID: input.ID, Revision: input.Revision, Unit: input.Unit, Port: input.Port, ReleaseDirectory: input.ReleaseDirectory}
	generation := observeGeneration(planner.GenerationInput{GenerationReference: input.GenerationReference})
	if generation.Status != host.CandidateInstalled {
		if generation.Status != host.CandidateAbsent {
			result.Status = host.CandidateInvalid
			result.Reason = generation.Reason
		}
		return result
	}
	result.Status = host.CandidateInstalled
	wanted := systemdCandidateUnit(input, generation.Executable)
	unitPath := filepath.Join(paths.systemdUnits, input.Unit)
	data, err := os.ReadFile(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return result
	}
	unitInfo, infoErr := os.Lstat(unitPath)
	if err != nil || infoErr != nil || !unitInfo.Mode().IsRegular() || unitInfo.Mode()&os.ModeSymlink != 0 || unitInfo.Mode().Perm() != 0644 || !ownedByExecutor(unitInfo) || string(data) != wanted {
		result.Status = host.CandidateInvalid
		result.Reason = "candidate systemd unit does not match the approved Plan"
		return result
	}
	output, err := paths.systemd.Run(ctx, "is-active", input.Unit)
	if err == nil && strings.TrimSpace(string(output)) == "active" {
		result.Status = host.CandidateActive
	}
	return result
}

func startCandidate(ctx context.Context, paths executionPaths, input planner.SystemdInput) (host.SystemdObservation, error) {
	if observed := observeSystemdCandidate(ctx, paths, input); observed.Status == host.CandidateActive {
		return observed, nil
	} else if observed.Status == host.CandidateInvalid || observed.Status == host.CandidateAbsent {
		reason := observed.Reason
		if reason == "" {
			reason = "candidate Generation is not installed"
		}
		return failedSystemd(input, reason), errors.New(reason)
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", input.Port))
	if err != nil {
		return failedSystemd(input, "candidate private port is already in use"), errors.New("candidate private port is already in use")
	}
	_ = listener.Close()
	generation := observeGeneration(planner.GenerationInput{GenerationReference: input.GenerationReference})
	unitPath := filepath.Join(paths.systemdUnits, input.Unit)
	unit := systemdCandidateUnit(input, generation.Executable)
	created := false
	if data, err := os.ReadFile(unitPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
			return failedSystemd(input, "install candidate systemd unit"), errors.New("install candidate systemd unit")
		}
		if err := os.Chmod(unitPath, 0644); err != nil {
			_ = os.Remove(unitPath)
			return failedSystemd(input, "secure candidate systemd unit"), errors.New("secure candidate systemd unit")
		}
		created = true
	} else if err != nil || string(data) != unit {
		return failedSystemd(input, "candidate systemd unit differs from the approved Plan"), errors.New("candidate systemd unit differs from the approved Plan")
	}
	if output, err := paths.systemd.Run(ctx, "daemon-reload"); err != nil {
		if created {
			_ = cleanupCandidateUnit(context.Background(), paths, input)
		}
		return failedSystemd(input, "reload systemd after candidate install: "+strings.TrimSpace(string(output))), errors.New("reload systemd after candidate install")
	}
	if output, err := paths.systemd.Run(ctx, "start", input.Unit); err != nil {
		if created {
			_ = cleanupCandidateUnit(context.Background(), paths, input)
		}
		return failedSystemd(input, "start candidate systemd unit: "+strings.TrimSpace(string(output))), errors.New("start candidate systemd unit")
	}
	observed := observeSystemdCandidate(ctx, paths, input)
	if observed.Status != host.CandidateActive {
		return failedSystemd(input, "candidate systemd unit did not become active"), errors.New("candidate systemd unit did not become active")
	}
	return observed, nil
}

func failedSystemd(input planner.SystemdInput, reason string) host.SystemdObservation {
	return host.SystemdObservation{Status: host.CandidateFailed, GenerationID: input.ID, Revision: input.Revision, Unit: input.Unit, Port: input.Port, ReleaseDirectory: input.ReleaseDirectory, Reason: reason}
}

func systemdCandidateUnit(input planner.SystemdInput, executable string) string {
	return fmt.Sprintf(`[Unit]
Description=Provision candidate %s
After=network.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
Environment=PROVISION_HTTP_LISTEN=127.0.0.1:%d
Environment=PROVISION_REVISION=%s
ExecStart=%s
Restart=on-failure
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
IPAddressDeny=any
IPAddressAllow=localhost

`, input.ID, input.Account, input.Account, input.ReleaseDirectory, input.Port, input.Revision, filepath.Join(input.ReleaseDirectory, executable))
}

func cleanupCandidateUnit(ctx context.Context, paths executionPaths, input planner.SystemdInput) error {
	active, err := activeCandidate(paths.environmentHome, input.ID, input.Unit)
	if err != nil {
		return err
	}
	if active {
		return errors.New("refusing to clean up the recorded active generation")
	}
	_, _ = paths.systemd.Run(ctx, "stop", input.Unit)
	unitPath := filepath.Join(paths.systemdUnits, input.Unit)
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("remove candidate systemd unit")
	}
	_, err = paths.systemd.Run(ctx, "daemon-reload")
	return err
}

func cleanupCandidate(ctx context.Context, paths executionPaths, input planner.HealthInput) error {
	if err := cleanupCandidateUnit(ctx, paths, systemdInputFromHealth(input)); err != nil {
		return err
	}
	observed := observeGeneration(planner.GenerationInput{GenerationReference: input.GenerationReference})
	if observed.Status == host.CandidateAbsent {
		return nil
	}
	if observed.Status != host.CandidateInstalled {
		return errors.New("candidate Generation is unsafe; refusing cleanup")
	}
	if err := os.RemoveAll(input.ReleaseDirectory); err != nil {
		return errors.New("remove failed candidate Generation")
	}
	return nil
}

func activeCandidate(environmentHome, generationID, unit string) (bool, error) {
	path := filepath.Join(environmentHome, "active-generation.json")
	record, err := readActiveGenerationRecord(path)
	if err != nil {
		return false, errors.New(err.Error() + "; refusing candidate cleanup")
	}
	if record == nil {
		return false, nil
	}
	return record.Active.ID == generationID || record.Active.SystemdUnit == unit, nil
}

func ensureReleaseRoot(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0755); err != nil {
			return err
		}
		if err := os.Chmod(path, 0755); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0755 || !ownedByExecutor(info) {
		return errors.New("release storage is not an executor-owned safe directory")
	}
	return nil
}

func ownedByExecutor(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func waitForCandidateHealth(ctx context.Context, paths executionPaths, input planner.HealthInput) host.HealthObservation {
	deadline := time.Now().Add(paths.healthTimeout)
	for {
		observed := checkCandidateHealth(ctx, paths, input)
		if observed.Status == host.CandidateHealthy || time.Now().After(deadline) || ctx.Err() != nil {
			return observed
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return observed
		case <-timer.C:
		}
	}
}

func checkCandidateHealth(ctx context.Context, paths executionPaths, input planner.HealthInput) host.HealthObservation {
	result := host.HealthObservation{Status: host.CandidateFailed, GenerationID: input.ID, Revision: input.Revision, ArtifactDigest: input.ArtifactDigest, ReleaseDirectory: input.ReleaseDirectory, Unit: input.Unit, Port: input.Port, Checks: []host.HealthCheckObservation{}}
	if observed := observeSystemdCandidate(ctx, paths, systemdInputFromHealth(input)); observed.Status != host.CandidateActive {
		result.Reason = "planned systemd candidate is not active: " + observed.Reason
		return result
	}
	result.CandidateActive = true
	checks := []struct {
		name           string
		path           string
		verifyRevision bool
	}{
		{"liveness", input.LivenessPath, false},
		{"readiness", input.ReadinessPath, false},
		{"candidateVerification", input.CandidateVerifyPath, true},
	}
	client := http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("candidate health redirects are not allowed")
		},
	}
	for _, check := range checks {
		observed := host.HealthCheckObservation{Name: check.name, Path: check.path}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", input.Port, check.path), nil)
		if err != nil {
			observed.Reason = "create health request"
			result.Checks = append(result.Checks, observed)
			result.Reason = check.name + " check could not be created"
			return result
		}
		response, err := client.Do(request)
		if err != nil {
			observed.Reason = "private candidate endpoint is unavailable"
			result.Checks = append(result.Checks, observed)
			result.Reason = check.name + " check failed"
			return result
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
		_ = response.Body.Close()
		observed.StatusCode = response.StatusCode
		if readErr != nil || len(data) > 64<<10 || response.StatusCode < 200 || response.StatusCode >= 300 {
			observed.Reason = "health response is not successful and bounded"
			result.Checks = append(result.Checks, observed)
			result.Reason = check.name + " check failed"
			return result
		}
		if check.verifyRevision {
			var body struct {
				Revision string `json:"revision"`
			}
			if json.Unmarshal(data, &body) != nil || body.Revision != input.Revision {
				observed.Reason = "candidate verification did not report the planned Revision"
				result.Checks = append(result.Checks, observed)
				result.Reason = check.name + " check failed"
				return result
			}
		}
		observed.Healthy = true
		result.Checks = append(result.Checks, observed)
	}
	result.Status = host.CandidateHealthy
	result.SwitchEligible = true
	return result
}

func systemdInputFromHealth(input planner.HealthInput) planner.SystemdInput {
	return planner.SystemdInput{GenerationReference: input.GenerationReference, Unit: input.Unit, Port: input.Port}
}
