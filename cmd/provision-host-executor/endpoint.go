package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/planner"
)

const (
	activeGenerationSchema = "provision.dev/active-generation/v1alpha1"
	httpDrainPolicy        = "caddy-graceful-config-reload"
)

var errCaddyPathNotFound = errors.New("Caddy configuration path is absent")

type caddyController interface {
	Read(context.Context, string) ([]byte, error)
	Replace(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

type adminCaddyController struct {
	client  *http.Client
	baseURL string
}

func newAdminCaddyController() caddyController {
	return adminCaddyController{
		client: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("Caddy admin redirects are not allowed")
			},
		},
		baseURL: "http://127.0.0.1:2019",
	}
}

func (controller adminCaddyController) Read(ctx context.Context, path string) ([]byte, error) {
	return controller.request(ctx, http.MethodGet, path, nil)
}

func (controller adminCaddyController) Replace(ctx context.Context, path string, body []byte) error {
	_, err := controller.request(ctx, http.MethodPost, path, body)
	return err
}

func (controller adminCaddyController) Delete(ctx context.Context, path string) error {
	_, err := controller.request(ctx, http.MethodDelete, path, nil)
	if errors.Is(err, errCaddyPathNotFound) {
		return nil
	}
	return err
}

func (controller adminCaddyController) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("Caddy admin path is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, method, controller.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create Caddy admin request")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := controller.client.Do(request)
	if err != nil {
		return nil, errors.New("contact Caddy admin endpoint")
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if readErr != nil || len(data) > 1<<20 {
		return nil, errors.New("Caddy admin response is invalid")
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, errCaddyPathNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Caddy admin request failed with status %d", response.StatusCode)
	}
	return data, nil
}

func validateEndpointInput(input planner.EndpointInput, previous *host.GenerationStatus, record bootstrapRecord, paths executionPaths) error {
	if err := validateSystemdInput(planner.SystemdInput{GenerationReference: input.GenerationReference, Unit: input.Unit, Port: input.UpstreamPort}, record, paths); err != nil {
		return errors.New("Endpoint candidate identity is invalid")
	}
	if !deploymentIdentifier.MatchString(input.RouteID) || !strings.HasPrefix(input.RouteID, "provision-"+record.Environment+"-") || input.ListenPort < 1024 || input.ListenPort > 65535 || input.UpstreamPort == input.ListenPort || input.Upstream != fmt.Sprintf("127.0.0.1:%d", input.UpstreamPort) || input.DrainPolicy != httpDrainPolicy {
		return errors.New("Endpoint input does not match the fixed Caddy route contract")
	}
	if previous != nil {
		if err := validatePlannedPrevious(*previous, record, paths, input.RouteID); err != nil {
			return err
		}
	}
	return nil
}

func validatePlannedPrevious(previous host.GenerationStatus, record bootstrapRecord, paths executionPaths, routeID string) error {
	reference := planner.GenerationReference{ID: previous.ID, Revision: previous.Revision, ArtifactDigest: previous.ArtifactDigest, Account: record.Account, ReleaseDirectory: previous.ReleaseDirectory}
	if err := validateSystemdInput(planner.SystemdInput{GenerationReference: reference, Unit: previous.SystemdUnit, Port: previous.Port}, record, paths); err != nil || previous.RouteID != routeID || !previous.UnitActive || !previous.UnitMatches || !previous.RouteObserved || !previous.RouteMatches || previous.RouteUpstream != fmt.Sprintf("127.0.0.1:%d", previous.Port) {
		return errors.New("planned previous Generation is not a healthy active route")
	}
	return nil
}

func observeEndpoint(ctx context.Context, paths executionPaths, input planner.EndpointInput) host.EndpointObservation {
	result := host.EndpointObservation{
		Status: host.EndpointPending, RouteID: input.RouteID, ListenPort: input.ListenPort, Upstream: input.Upstream,
		Active: generationStatusFromEndpoint(input), DrainPolicy: input.DrainPolicy,
	}
	record, err := readActiveGenerationRecord(filepath.Join(paths.environmentHome, "active-generation.json"))
	if err != nil {
		result.Status = host.EndpointFailed
		result.Reason = err.Error()
		return result
	}
	if record == nil || !sameGenerationIdentity(record.Active, result.Active) || record.ListenPort != input.ListenPort || record.DrainPolicy != input.DrainPolicy {
		return result
	}
	result.Previous = copyGenerationStatus(record.Previous)
	result.CandidateVerified = digestPattern.MatchString(record.PlanID) && digestPattern.MatchString(record.CandidateVerificationOperationDigest)
	observed := observeSystemdCandidate(ctx, paths, planner.SystemdInput{GenerationReference: input.GenerationReference, Unit: input.Unit, Port: input.UpstreamPort})
	if observed.Status != host.CandidateActive {
		result.Status = host.EndpointFailed
		result.Reason = "recorded active Generation is not running as planned"
		return result
	}
	upstream, listen, routeOK, routeErr := observeCaddyEndpoint(ctx, paths.caddy, input.RouteID)
	if routeErr != nil && !errors.Is(routeErr, errCaddyPathNotFound) {
		result.Status = host.EndpointFailed
		result.Reason = routeErr.Error()
		return result
	}
	result.Active.UnitActive = true
	result.Active.UnitMatches = true
	result.Active.RouteObserved = routeOK
	result.Active.RouteUpstream = upstream
	result.Active.RouteMatches = routeOK && upstream == input.Upstream && listen == input.ListenPort
	result.PreviousRetained = previousGenerationRetained(ctx, paths, input.Account, result.Previous)
	if result.CandidateVerified && result.Active.RouteMatches && result.PreviousRetained {
		result.Status = host.EndpointActive
		result.GracefulReload = true
	}
	return result
}

func applySwitchEndpoint(ctx context.Context, planned planner.Operation, claim authority.Claim, record bootstrapRecord, paths executionPaths, now time.Time) (json.RawMessage, error) {
	input := *planned.Input.Endpoint
	verificationDigest, err := requireSuccessfulCandidateVerification(paths, claim, planned)
	if err != nil {
		return encodeEndpointFailure(input, planned.Input.Previous, err.Error())
	}
	if observed := observeSystemdCandidate(ctx, paths, planner.SystemdInput{GenerationReference: input.GenerationReference, Unit: input.Unit, Port: input.UpstreamPort}); observed.Status != host.CandidateActive {
		return encodeEndpointFailure(input, planned.Input.Previous, "verified candidate is no longer active")
	}

	activePath := filepath.Join(paths.environmentHome, "active-generation.json")
	current, err := readActiveGenerationRecord(activePath)
	if err != nil {
		return encodeEndpointFailure(input, planned.Input.Previous, err.Error())
	}
	if current != nil && sameGenerationIdentity(current.Active, generationStatusFromEndpoint(input)) {
		observed := observeEndpoint(ctx, paths, input)
		encoded, encodeErr := json.Marshal(observed)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if observed.Status == host.EndpointActive {
			return encoded, nil
		}
	}
	if err := activeMatchesPlannedPrevious(ctx, paths, record, current, planned.Input.Previous, input.RouteID); err != nil {
		return encodeEndpointFailure(input, planned.Input.Previous, err.Error())
	}

	serverPath := "/config/apps/http/servers/" + url.PathEscape(input.RouteID)
	oldServer, readErr := paths.caddy.Read(ctx, serverPath)
	if readErr != nil && !errors.Is(readErr, errCaddyPathNotFound) {
		return encodeEndpointFailure(input, planned.Input.Previous, readErr.Error())
	}
	if planned.Input.Previous == nil && readErr == nil {
		return encodeEndpointFailure(input, nil, "Provision Caddy server already exists without a recorded active Generation")
	}
	if planned.Input.Previous != nil {
		upstream, listen, routeOK, routeErr := observeCaddyEndpoint(ctx, paths.caddy, input.RouteID)
		if routeErr != nil || !routeOK || upstream != fmt.Sprintf("127.0.0.1:%d", planned.Input.Previous.Port) || listen != input.ListenPort {
			return encodeEndpointFailure(input, planned.Input.Previous, "current Caddy route does not match the planned previous Generation")
		}
	}

	server, err := caddyServerConfiguration(input)
	if err != nil {
		return nil, err
	}
	if err := paths.caddy.Replace(ctx, serverPath, server); err != nil {
		return encodeEndpointFailure(input, planned.Input.Previous, "atomic Caddy route load failed: "+err.Error())
	}
	upstream, listen, routeOK, routeErr := observeCaddyEndpoint(ctx, paths.caddy, input.RouteID)
	if routeErr != nil || !routeOK || upstream != input.Upstream || listen != input.ListenPort {
		_ = restoreCaddyServer(context.Background(), paths.caddy, serverPath, oldServer, readErr)
		return encodeEndpointFailure(input, planned.Input.Previous, "Caddy did not expose the planned stable route")
	}

	active := generationStatusFromEndpoint(input)
	active.UnitActive, active.UnitMatches, active.RouteObserved, active.RouteMatches = true, true, true, true
	active.RouteUpstream = input.Upstream
	previous := retainedPreviousStatus(planned.Input.Previous)
	recordValue := host.ActiveGenerationRecord{
		SchemaVersion: activeGenerationSchema, PlanID: claim.PlanID,
		CandidateVerificationOperationDigest: verificationDigest,
		Active:                               active, Previous: previous, ListenPort: input.ListenPort,
		DrainPolicy: input.DrainPolicy, SwitchedAt: now.UTC(),
	}
	if err := writeJSONAtomic(activePath, recordValue, 0644); err != nil {
		rollbackErr := restoreCaddyServer(context.Background(), paths.caddy, serverPath, oldServer, readErr)
		reason := "record active Generation after Caddy switch"
		if rollbackErr != nil {
			reason += "; restore previous route: " + rollbackErr.Error()
		}
		return encodeEndpointFailure(input, planned.Input.Previous, reason)
	}
	observed := observeEndpoint(ctx, paths, input)
	encoded, err := json.Marshal(observed)
	if err != nil {
		return nil, err
	}
	if observed.Status != host.EndpointActive {
		return encoded, errors.New("switched Endpoint failed exact post-load observation")
	}
	return encoded, nil
}

func requireSuccessfulCandidateVerification(paths executionPaths, claim authority.Claim, planned planner.Operation) (string, error) {
	entries, err := os.ReadDir(paths.authorityState)
	if err != nil {
		return "", errors.New("read host authorization history")
	}
	for _, entry := range entries {
		if entry.IsDir() || !attemptID.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		path := filepath.Join(paths.authorityState, entry.Name())
		info, infoErr := os.Lstat(path)
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ownedByExecutor(info) || info.Size() > 1<<20 {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var consumed consumedAuthorization
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&consumed)
		var extra any
		trailingErr := decoder.Decode(&extra)
		if decodeErr != nil || trailingErr != io.EOF || consumed.SchemaVersion != "provision.dev/consumed-authorization/v1alpha2" || consumed.Outcome != "succeeded" || consumed.Claim.PlanID != claim.PlanID || consumed.Claim.Application != claim.Application || consumed.Claim.Environment != claim.Environment || consumed.Operation.ID != planned.DependsOn[0] || consumed.Operation.Kind != planner.VerifyCandidate || consumed.Claim.OperationID != consumed.Operation.ID || consumed.Claim.OperationKind != string(consumed.Operation.Kind) {
			continue
		}
		digest, digestErr := planner.OperationDigest(consumed.Operation)
		if digestErr != nil || digest != consumed.Claim.OperationDigest || consumed.Operation.Input.Health == nil || !healthMatchesEndpoint(*consumed.Operation.Input.Health, *planned.Input.Endpoint) {
			continue
		}
		var observed host.HealthObservation
		if decodeExactJSON(consumed.Observation, &observed) != nil || observed.Status != host.CandidateHealthy || !observed.CandidateActive || !observed.SwitchEligible || observed.GenerationID != planned.Input.Endpoint.ID || observed.Revision != planned.Input.Endpoint.Revision || observed.ArtifactDigest != planned.Input.Endpoint.ArtifactDigest || observed.Unit != planned.Input.Endpoint.Unit || observed.Port != planned.Input.Endpoint.UpstreamPort {
			continue
		}
		return digest, nil
	}
	return "", errors.New("host has no successful exact candidate verification for this Plan")
}

func healthMatchesEndpoint(health planner.HealthInput, endpoint planner.EndpointInput) bool {
	return health.GenerationReference == endpoint.GenerationReference && health.Unit == endpoint.Unit && health.Port == endpoint.UpstreamPort
}

func activeMatchesPlannedPrevious(ctx context.Context, paths executionPaths, record bootstrapRecord, current *host.ActiveGenerationRecord, planned *host.GenerationStatus, routeID string) error {
	if planned == nil {
		if current != nil {
			return errors.New("observed active Generation was not present in the approved Plan")
		}
		return nil
	}
	if current == nil || !sameGenerationIdentity(current.Active, *planned) || current.Active.RouteID != routeID {
		return errors.New("recorded active Generation differs from the approved Plan")
	}
	if !previousGenerationRetained(ctx, paths, record.Account, planned) {
		return errors.New("planned previous Generation is no longer runnable")
	}
	return nil
}

func previousGenerationRetained(ctx context.Context, paths executionPaths, account string, previous *host.GenerationStatus) bool {
	if previous == nil {
		return true
	}
	reference := planner.GenerationReference{ID: previous.ID, Revision: previous.Revision, ArtifactDigest: previous.ArtifactDigest, Account: account, ReleaseDirectory: previous.ReleaseDirectory}
	observed := observeSystemdCandidate(ctx, paths, planner.SystemdInput{GenerationReference: reference, Unit: previous.SystemdUnit, Port: previous.Port})
	return observed.Status == host.CandidateActive
}

func retainedPreviousStatus(previous *host.GenerationStatus) *host.GenerationStatus {
	if previous == nil {
		return nil
	}
	result := *previous
	result.UnitActive = true
	result.UnitMatches = true
	result.RouteObserved = false
	result.RouteUpstream = ""
	result.RouteMatches = false
	return &result
}

func generationStatusFromEndpoint(input planner.EndpointInput) host.GenerationStatus {
	return host.GenerationStatus{
		ID: input.ID, Revision: input.Revision, ArtifactDigest: input.ArtifactDigest,
		SystemdUnit: input.Unit, ReleaseDirectory: input.ReleaseDirectory,
		Port: input.UpstreamPort, RouteID: input.RouteID,
	}
}

func sameGenerationIdentity(left, right host.GenerationStatus) bool {
	return left.ID == right.ID && left.Revision == right.Revision && left.ArtifactDigest == right.ArtifactDigest && left.SystemdUnit == right.SystemdUnit && left.ReleaseDirectory == right.ReleaseDirectory && left.Port == right.Port && left.RouteID == right.RouteID
}

func copyGenerationStatus(value *host.GenerationStatus) *host.GenerationStatus {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func caddyServerConfiguration(input planner.EndpointInput) ([]byte, error) {
	server := map[string]any{
		"listen": []string{fmt.Sprintf(":%d", input.ListenPort)},
		"routes": []any{map[string]any{
			"@id": input.RouteID,
			"handle": []any{map[string]any{
				"handler":   "reverse_proxy",
				"upstreams": []any{map[string]any{"dial": input.Upstream}},
			}},
		}},
	}
	return json.Marshal(server)
}

func observeCaddyEndpoint(ctx context.Context, controller caddyController, routeID string) (string, int, bool, error) {
	serverPath := "/config/apps/http/servers/" + url.PathEscape(routeID)
	server, err := controller.Read(ctx, serverPath)
	if err != nil {
		return "", 0, false, err
	}
	var serverConfig struct {
		Listen []string `json:"listen"`
	}
	if json.Unmarshal(server, &serverConfig) != nil || len(serverConfig.Listen) != 1 {
		return "", 0, false, errors.New("Provision Caddy server has invalid listen configuration")
	}
	var listenPort int
	if _, scanErr := fmt.Sscanf(serverConfig.Listen[0], ":%d", &listenPort); scanErr != nil || listenPort < 1024 || listenPort > 65535 {
		return "", 0, false, errors.New("Provision Caddy server has an invalid stable port")
	}
	route, err := controller.Read(ctx, "/id/"+url.PathEscape(routeID))
	if err != nil {
		return "", listenPort, false, err
	}
	upstream := firstJSONValue(route, "dial")
	if upstream == "" {
		return "", listenPort, false, errors.New("Provision Caddy route has no upstream")
	}
	return upstream, listenPort, true, nil
}

func restoreCaddyServer(ctx context.Context, controller caddyController, path string, previous []byte, previousErr error) error {
	if errors.Is(previousErr, errCaddyPathNotFound) {
		return controller.Delete(ctx, path)
	}
	if previousErr != nil {
		return previousErr
	}
	return controller.Replace(ctx, path, previous)
}

func readActiveGenerationRecord(path string) (*host.ActiveGenerationRecord, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0644 || !ownedByExecutor(info) || info.Size() > 64<<10 {
		return nil, errors.New("active Generation record is unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("active Generation record cannot be read")
	}
	var record host.ActiveGenerationRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&record)
	var extra any
	trailingErr := decoder.Decode(&extra)
	releaseRoot := filepath.Join(filepath.Dir(path), "releases")
	if decodeErr != nil || trailingErr != io.EOF || record.SchemaVersion != activeGenerationSchema || !digestPattern.MatchString(record.PlanID) || !digestPattern.MatchString(record.CandidateVerificationOperationDigest) || record.ListenPort < 1024 || record.ListenPort > 65535 || record.DrainPolicy != httpDrainPolicy || record.SwitchedAt.IsZero() || validateRecordedGeneration(record.Active, releaseRoot) != nil || record.Previous != nil && validateRecordedGeneration(*record.Previous, releaseRoot) != nil {
		return nil, errors.New("active Generation record is invalid")
	}
	return &record, nil
}

func validateRecordedGeneration(generation host.GenerationStatus, releaseRoot string) error {
	if !deploymentIdentifier.MatchString(generation.ID) || !deploymentIdentifier.MatchString(generation.Revision) || !digestPattern.MatchString(generation.ArtifactDigest) || !systemdUnit.MatchString(generation.SystemdUnit) || !deploymentIdentifier.MatchString(generation.RouteID) || generation.Port < 1024 || generation.Port > 65535 || generation.ReleaseDirectory != filepath.Join(releaseRoot, generation.ID) {
		return errors.New("Generation identity is invalid")
	}
	return nil
}

func encodeEndpointFailure(input planner.EndpointInput, previous *host.GenerationStatus, reason string) (json.RawMessage, error) {
	observed := host.EndpointObservation{
		Status: host.EndpointFailed, RouteID: input.RouteID, ListenPort: input.ListenPort,
		Upstream: input.Upstream, Active: generationStatusFromEndpoint(input),
		Previous: copyGenerationStatus(previous), DrainPolicy: input.DrainPolicy, Reason: reason,
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		return nil, err
	}
	return encoded, errors.New(reason)
}

func decodeExactJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("JSON value has trailing data")
	}
	return nil
}
