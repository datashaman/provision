package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"provision/internal/host"
	"provision/internal/implementation/hostkeyvalue"
	"provision/internal/planner"
)

const (
	keyValueGenerationSchema = "provision.dev/host-key-value-generation/v1alpha1"
	keyValueCredentialName   = "valkey-credential"
	keyValueListenAddress    = "127.0.0.1"
	keyValuePort             = 26379
)

var keyValueSecretPart = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
var keyValueUserPart = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type keyValueGenerationRecord struct {
	SchemaVersion string               `json:"schemaVersion"`
	KeyValue      planner.KeyValuePlan `json:"keyValue"`
	// Verified is set only after the generation passed exact verification, so a
	// half-prepared generation is never bindable or reported active.
	Verified bool `json:"verified"`
}

func validateKeyValueOperation(planned planner.Operation, record bootstrapRecord, paths executionPaths) error {
	in := planned.Input
	if planned.Kind != planner.PrepareKeyValue || len(planned.DependsOn) != 0 || in.KeyValue == nil || in.Artifact != nil || in.Generation != nil || in.Systemd != nil || in.Health != nil || in.Endpoint != nil || in.Previous != nil || in.Drain != nil || in.Retention != nil || in.Async != nil || in.Database != nil {
		return errors.New("prepareKeyValue requires only its typed Key-Value Store input and no dependency")
	}
	input := in.KeyValue
	image, ok := hostkeyvalue.ImageByReference(input.ImageReference)
	if !ok || image.Version != input.ValkeyVersion || image.Index != input.ImageIndex || image.Manifest != input.ImageManifest || image.Evidence != input.QualificationDigest || image.Generation() != input.GenerationID {
		return errors.New("Key-Value Store implementation does not match a qualified Valkey product identity")
	}
	service := "provision-" + record.Environment + "-valkey"
	if !deploymentIdentifier.MatchString(input.Component) || input.LogicalID != "provision-"+record.Environment+"-"+input.Component || input.Implementation != "valkey-quadlet" || input.DataRole != "authoritative" {
		return errors.New("Key-Value Store identity does not match the bootstrapped Environment")
	}
	expectedData := filepath.Join(paths.environmentHome, "services", "valkey", "generations", input.GenerationID, "data")
	if input.ServiceUnit != service+".service" || input.Container != service || input.Account != record.Account || input.DataPath != expectedData || input.QuadletDefinition != service+".container" || input.ListenAddress != keyValueListenAddress || input.Port != keyValuePort || input.AppendOnly != "yes" || input.AppendFsync != "everysec" {
		return errors.New("Key-Value Store service identity, owned path, listener, or persistence does not match the bootstrapped Environment")
	}
	uid := accountUID(record.Account)
	if uid <= 0 || input.QuadletPath != fmt.Sprintf("/etc/containers/systemd/users/%d/%s.container", uid, service) {
		return errors.New("Key-Value Store Quadlet path does not match the bootstrapped Environment account")
	}
	if input.CredentialReference != "secret://"+record.Environment+"/valkey-url" {
		return errors.New("Key-Value Store credential Secret Reference does not match the Environment")
	}
	b := input.Binding
	if b.Component != input.Component || b.LogicalID != input.LogicalID || len(b.Keys) == 0 || b.ValuePrefix == "" {
		return errors.New("Key-Value Store binding is not by the logical component reference")
	}
	for _, key := range b.Keys {
		if !strings.HasPrefix(key, b.Namespace+"/") || !strings.HasPrefix(b.Namespace, input.LogicalID+"/") {
			return errors.New("Key-Value Store deterministic key is outside the planned namespace")
		}
	}
	return nil
}

func validateKeyValueSensitiveValues(envelope map[string]string, input planner.KeyValuePlan) (string, error) {
	value, ok := envelope[input.CredentialReference]
	if len(envelope) != 1 || !ok || value == "" {
		return "", errors.New("prepareKeyValue requires exactly its resolved Key-Value Store Secret Reference")
	}
	return value, nil
}

func parseKeyValueSecret(value string, port int) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "valkey" || parsed.User == nil || parsed.Host != fmt.Sprintf("%s:%d", keyValueListenAddress, port) || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", "", errors.New("resolved Key-Value Store Secret Reference must contain a private loopback valkey URL")
	}
	password, _ := parsed.User.Password()
	if !keyValueUserPart.MatchString(parsed.User.Username()) || !keyValueSecretPart.MatchString(password) {
		return "", "", errors.New("resolved Key-Value Store credential has unsupported characters")
	}
	return parsed.User.Username(), password, nil
}

func keyValueURLCredentialPath(environment string) string {
	return filepath.Join("/var/lib/provision/environments", environment, "credentials", "valkey-url")
}

func keyValueCredentialEntrypoint() string {
	return `#!/usr/bin/env bash
set -euo pipefail
url="$(cat /run/provision-credential/valkey-credential)"
case "$url" in
  valkey://*@127.0.0.1:*) ;;
  *) echo "unsupported credential URL" >&2; exit 1 ;;
esac
credential_pair="${url#valkey://}"
credential_pair="${credential_pair%%@*}"
user="${credential_pair%%:*}"
password="${credential_pair#*:}"
[[ -n "$user" && -n "$password" ]] || { echo "incomplete Valkey credential URL" >&2; exit 1; }
hash="$(printf '%s' "$password" | sha256sum | cut -d' ' -f1)"
printf 'user default off\nuser %s on #%s ~* &* +@all\n' "$user" "$hash" > /tmp/users.acl
chmod 0644 /tmp/users.acl
cat > /tmp/provision-cli.sh <<CLI
#!/bin/sh
REDISCLI_AUTH='$password' exec valkey-cli --user '$user' "\$@"
CLI
cat > /tmp/provision-health.sh <<HEALTH
#!/bin/sh
[ "\$(/tmp/provision-cli.sh ping 2>/dev/null)" = PONG ]
HEALTH
chmod 0700 /tmp/provision-cli.sh /tmp/provision-health.sh
exec docker-entrypoint.sh "$@"
`
}

func renderKeyValueQuadlet(input planner.KeyValuePlan, entrypoint string) string {
	return fmt.Sprintf(`[Unit]
Description=Provision managed Valkey Key-Value Store generation %s
Wants=network-online.target
After=network-online.target

[Container]
Image=%s
ContainerName=%s
PublishPort=127.0.0.1:%d:6379
Volume=%s:/data
Volume=%%d/%s:/run/provision-credential/valkey-credential:ro
Volume=%s:/usr/local/bin/provision-valkey-credential-entrypoint:ro
Entrypoint=/usr/local/bin/provision-valkey-credential-entrypoint
Exec=valkey-server --aclfile /tmp/users.acl --appendonly %s --appendfsync %s --dir /data --bind 0.0.0.0
HealthCmd=/tmp/provision-health.sh
HealthInterval=5s
HealthRetries=24
NoNewPrivileges=true

[Service]
LoadCredentialEncrypted=%s
Restart=always
TimeoutStartSec=900

[Install]
WantedBy=default.target
`, input.GenerationID, input.ImageReference, input.Container, input.Port, input.DataPath, keyValueCredentialName, entrypoint, input.AppendOnly, input.AppendFsync, keyValueCredentialName)
}

func keyValueServiceRoot(input planner.KeyValuePlan) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(input.DataPath)))
}

func keyValueRecordPath(input planner.KeyValuePlan) string {
	return filepath.Join(filepath.Dir(input.DataPath), "generation.json")
}

func observeKeyValueOperation(ctx context.Context, planned planner.Operation, record bootstrapRecord, paths executionPaths) (host.OperationObservation, error) {
	if err := validateKeyValueOperation(planned, record, paths); err != nil {
		return host.OperationObservation{}, err
	}
	observed, state := observeManagedKeyValue(ctx, *planned.Input.KeyValue, record, paths)
	evidence, err := json.Marshal(observed)
	if err != nil {
		return host.OperationObservation{}, err
	}
	return host.OperationObservation{State: state, Evidence: evidence}, nil
}

func applyKeyValueOperation(ctx context.Context, planned planner.Operation, record bootstrapRecord, paths executionPaths, sensitiveValues map[string]string) (json.RawMessage, error) {
	if err := validateKeyValueOperation(planned, record, paths); err != nil {
		return nil, err
	}
	input := *planned.Input.KeyValue
	secret, err := validateKeyValueSensitiveValues(sensitiveValues, input)
	if err != nil {
		return nil, err
	}
	if observed, state := observeManagedKeyValue(ctx, input, record, paths); state == "satisfied" {
		return json.Marshal(observed)
	} else if state == "unknown" {
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New("existing Key-Value Store resources do not match the approved generation")
	}
	if _, _, err := parseKeyValueSecret(secret, input.Port); err != nil {
		return nil, err
	}
	credentialURL := secret
	secret = ""
	defer func() { credentialURL = "" }()
	uid, gid := accountUID(record.Account), accountGID(record.Account)
	if uid <= 0 || gid <= 0 {
		return nil, errors.New("Environment account identity is unavailable")
	}
	serviceRoot := keyValueServiceRoot(input)
	generationRoot := filepath.Dir(input.DataPath)
	runtimeHome := "/var/lib/provision/runtime/" + record.Environment
	credentialDir := filepath.Join(runtimeHome, ".config", "credstore.encrypted")
	for _, d := range []struct {
		path string
		mode os.FileMode
		uid  int
		gid  int
	}{{serviceRoot, 0755, 0, 0}, {filepath.Dir(generationRoot), 0755, 0, 0}, {generationRoot, 0755, 0, 0}, {credentialDir, 0700, uid, gid}, {filepath.Dir(keyValueURLCredentialPath(record.Environment)), 0700, 0, 0}, {filepath.Dir(input.QuadletPath), 0755, 0, 0}} {
		if err := ensureDirectory(d.path, d.mode, d.uid, d.gid); err != nil {
			return nil, err
		}
	}
	if err := ensureEnvironmentDataDirectory(input.DataPath, record.Account, uid, gid); err != nil {
		return nil, err
	}
	if err := installUserEncryptedCredential(ctx, filepath.Join(credentialDir, keyValueCredentialName), keyValueCredentialName, uid, gid, credentialURL+"\n"); err != nil {
		return nil, err
	}
	if err := installSystemEncryptedCredential(ctx, keyValueURLCredentialPath(record.Environment), "valkey-url", credentialURL+"\n"); err != nil {
		return nil, err
	}
	credentialURL = ""
	entrypointPath := filepath.Join(serviceRoot, "credential-entrypoint")
	if err := installExactFile(entrypointPath, []byte(keyValueCredentialEntrypoint()), 0755, 0, 0); err != nil {
		return nil, err
	}
	if err := installExactFile(input.QuadletPath, []byte(renderKeyValueQuadlet(input, entrypointPath)), 0644, 0, 0); err != nil {
		return nil, err
	}
	for _, step := range []struct {
		message string
		args    []string
	}{
		{"pull approved Valkey image", []string{"podman", "pull", input.ImageReference}},
		{"verify approved Valkey image", []string{"podman", "image", "inspect", input.ImageReference}},
		{"own Valkey data for the container user", []string{"podman", "unshare", "chown", "-R", "999:999", input.DataPath}},
		{"reload Environment user units", []string{"systemctl", "--user", "daemon-reload"}},
		{"start managed Valkey service", []string{"systemctl", "--user", "restart", input.ServiceUnit}},
	} {
		if _, err := runAsEnvironment(ctx, record, step.args[0], step.args[1:]...); err != nil {
			return nil, errors.New(step.message)
		}
	}
	if err := waitForKeyValueService(ctx, record, input); err != nil {
		return nil, err
	}
	for _, key := range input.Binding.Keys {
		if out, err := keyValueCLI(ctx, record, input, "SET", key, input.Binding.DeterministicValue(key)); err != nil || !strings.HasPrefix(string(out), "OK") {
			return nil, errors.New("write deterministic workload key")
		}
	}
	if err := writeJSONAtomic(keyValueRecordPath(input), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: input}, 0444); err != nil {
		return nil, errors.New("record Key-Value Store generation")
	}
	observed, state := observeKeyValueGeneration(ctx, input, record, paths, true)
	encoded, encodeErr := json.Marshal(observed)
	if encodeErr != nil {
		return nil, encodeErr
	}
	if state != "satisfied" {
		return encoded, errors.New("prepared Key-Value Store failed exact verification")
	}
	// Only now is the generation verified; the record is replaced atomically.
	if err := writeJSONAtomic(keyValueRecordPath(input), keyValueGenerationRecord{SchemaVersion: keyValueGenerationSchema, KeyValue: input, Verified: true}, 0444); err != nil {
		return encoded, errors.New("mark Key-Value Store generation verified")
	}
	return encoded, nil
}

func waitForKeyValueService(ctx context.Context, record bootstrapRecord, input planner.KeyValuePlan) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		active, _ := runAsEnvironment(ctx, record, "systemctl", "--user", "is-active", input.ServiceUnit)
		health, _ := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.State.Health.Status}}", input.Container)
		if strings.TrimSpace(string(active)) == "active" && strings.TrimSpace(string(health)) == "healthy" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("managed Valkey service did not become healthy")
}

// keyValueCLI is a real authenticated client round trip: the in-container
// valkey-cli carries the credential, so the executor never holds it again.
func keyValueCLI(ctx context.Context, record bootstrapRecord, input planner.KeyValuePlan, args ...string) ([]byte, error) {
	return runAsEnvironment(ctx, record, "podman", append([]string{"exec", input.Container, "/tmp/provision-cli.sh"}, args...)...)
}

func observeManagedKeyValue(ctx context.Context, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths) (host.KeyValueOperationObservation, string) {
	return observeKeyValueGeneration(ctx, input, record, paths, false)
}

// observeKeyValueGeneration checks the exact generation. An unverified record
// means preparation was interrupted before verification: it is "pending" (safe
// to re-apply idempotently) unless the caller is the final verification itself.
func observeKeyValueGeneration(ctx context.Context, input planner.KeyValuePlan, record bootstrapRecord, paths executionPaths, finalVerification bool) (host.KeyValueOperationObservation, string) {
	status := keyValueStatusIdentity(input)
	observation := host.KeyValueOperationObservation{SchemaVersion: host.KeyValueOperationSchema, Status: "pending", KeyValue: status}
	fail := func(health, reason string) (host.KeyValueOperationObservation, string) {
		observation.Status = "failed"
		observation.KeyValue.Health, observation.KeyValue.Reason = health, reason
		return observation, "unknown"
	}
	recordPath := keyValueRecordPath(input)
	data, err := os.ReadFile(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return observation, "pending"
	}
	if err != nil {
		return fail("unknown", "read Key-Value Store generation record")
	}
	var generation keyValueGenerationRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&generation) != nil || generation.SchemaVersion != keyValueGenerationSchema || !reflect.DeepEqual(generation.KeyValue, input) {
		return fail("drifted", "Key-Value Store generation record differs from the approved generation")
	}
	if !generation.Verified && !finalVerification {
		return observation, "pending"
	}
	serviceRoot := keyValueServiceRoot(input)
	entrypointPath := filepath.Join(serviceRoot, "credential-entrypoint")
	uid := accountUID(record.Account)
	if !rootOwned(recordPath, 0444) || !rootOwned(input.QuadletPath, 0644) || !rootOwned(entrypointPath, 0755) || uid <= 0 {
		return fail("unsafe-owned-resource", "Key-Value Store generation files have unsafe ownership or modes")
	}
	quadlet, quadletErr := os.ReadFile(input.QuadletPath)
	entrypoint, entrypointErr := os.ReadFile(entrypointPath)
	credentialPath := filepath.Join("/var/lib/provision/runtime", record.Environment, ".config", "credstore.encrypted", keyValueCredentialName)
	if quadletErr != nil || string(quadlet) != renderKeyValueQuadlet(input, entrypointPath) || entrypointErr != nil || string(entrypoint) != keyValueCredentialEntrypoint() || !ownedBy(credentialPath, uid, 0600) || !rootOwned(keyValueURLCredentialPath(record.Environment), 0600) || !environmentDataOwned(input.DataPath, record.Account, uid, accountGID(record.Account)) {
		return fail("drifted-owned-resource", "Key-Value Store runtime files or data ownership differ from the approved generation")
	}
	active, activeErr := runAsEnvironment(ctx, record, "systemctl", "--user", "is-active", input.ServiceUnit)
	health, healthErr := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.State.Health.Status}}", input.Container)
	manifest, manifestErr := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.ImageDigest}}", input.Container)
	port, portErr := runAsEnvironment(ctx, record, "podman", "port", input.Container, "6379/tcp")
	ping, pingErr := keyValueCLI(ctx, record, input, "PING")
	appendonly, aofErr := keyValueCLI(ctx, record, input, "CONFIG", "GET", "appendonly")
	appendfsync, fsyncErr := keyValueCLI(ctx, record, input, "CONFIG", "GET", "appendfsync")
	info, infoErr := keyValueCLI(ctx, record, input, "INFO", "server")
	if activeErr != nil || healthErr != nil || manifestErr != nil || portErr != nil || pingErr != nil || aofErr != nil || fsyncErr != nil || infoErr != nil {
		return fail("unobservable", "query managed Key-Value Store service, packaging, persistence, or listener")
	}
	version := ""
	for _, line := range strings.Split(string(info), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "valkey_version:"); ok {
			version = value
		}
	}
	observation.KeyValue.ValkeyVersion = version
	observation.KeyValue.ImageManifest = strings.TrimSpace(string(manifest))
	if listenAddress, listenPort, ok := parseLoopbackListener(string(port)); ok {
		observation.KeyValue.ListenAddress, observation.KeyValue.Port = listenAddress, listenPort
	}
	observation.KeyValue.AppendOnly = configValue(appendonly)
	observation.KeyValue.AppendFsync = configValue(appendfsync)
	switch {
	case strings.TrimSpace(string(active)) != "active":
		return fail("drifted", "managed Key-Value Store service is not active")
	case strings.TrimSpace(string(health)) != "healthy":
		return fail("drifted", "managed Key-Value Store container is not healthy")
	case strings.TrimSpace(string(manifest)) != input.ImageManifest:
		return fail("drifted", "managed Key-Value Store container image manifest differs")
	case version != input.ValkeyVersion:
		return fail("drifted", "Valkey product version differs")
	case observation.KeyValue.AppendOnly != input.AppendOnly || observation.KeyValue.AppendFsync != input.AppendFsync:
		return fail("drifted", "Valkey persistence differs from AOF everysec")
	case observation.KeyValue.ListenAddress != input.ListenAddress || observation.KeyValue.Port != input.Port:
		return fail("drifted", "Valkey listener differs from the approved loopback listener")
	case strings.TrimSpace(string(ping)) != "PONG":
		return fail("unobservable", "Valkey did not answer an authenticated PING")
	}
	for _, key := range input.Binding.Keys {
		value, err := keyValueCLI(ctx, record, input, "GET", key)
		if err != nil || strings.TrimSpace(string(value)) != input.Binding.DeterministicValue(key) {
			return fail("drifted", "deterministic workload keys are not readable from the generation")
		}
		observation.Keys = append(observation.Keys, host.DeterministicKeyStatus{Key: key, Value: input.Binding.DeterministicValue(key)})
	}
	observation.Status, observation.Verified = "verified", true
	observation.KeyValue.Ready, observation.KeyValue.Connectivity, observation.KeyValue.Health = true, true, "healthy"
	observation.Checks = host.KeyValueVerificationChecks{ServiceHealth: true, ClientRoundTrip: true, GenerationIdentity: true, Persistence: true, CredentialBoundary: true}
	return observation, "satisfied"
}

// parseLoopbackListener returns the single published listener, or false when
// the container publishes anything other than exactly one address:port.
func parseLoopbackListener(output string) (string, int, bool) {
	lines := strings.Fields(output)
	if len(lines) != 1 {
		return "", 0, false
	}
	address, portText, ok := strings.Cut(lines[0], ":")
	port, err := strconv.Atoi(portText)
	return address, port, ok && err == nil
}

func configValue(output []byte) string {
	lines := strings.Fields(string(output))
	if len(lines) == 2 {
		return lines[1]
	}
	return ""
}

func keyValueStatusIdentity(input planner.KeyValuePlan) host.KeyValueGenerationStatus {
	serviceRoot := keyValueServiceRoot(input)
	environment := strings.TrimPrefix(input.CredentialReference, "secret://")
	if separator := strings.IndexByte(environment, '/'); separator >= 0 {
		environment = environment[:separator]
	}
	return host.KeyValueGenerationStatus{
		ID: input.GenerationID, LogicalID: input.LogicalID, Role: input.DataRole, ImageReference: input.ImageReference, ImageManifest: input.ImageManifest, QualificationDigest: input.QualificationDigest,
		ServiceUnit: input.ServiceUnit, Container: input.Container, Account: input.Account, DataPath: input.DataPath, QuadletPath: input.QuadletPath, ListenAddress: input.ListenAddress, Port: input.Port,
		SupportedGuarantees: []string{"authoritative-store-data-role", "aof-everysec-persistence", "loopback-only-listener", "secret-reference-only-plan"},
		OwnedResources: []string{
			"systemd-unit:" + input.ServiceUnit, "container:" + input.Container, "data-path:" + input.DataPath, "quadlet:" + input.QuadletPath,
			"generation-record:" + keyValueRecordPath(input), "credential-entrypoint:" + filepath.Join(serviceRoot, "credential-entrypoint"),
			"encrypted-credential:" + filepath.Join("/var/lib/provision/runtime", environment, ".config", "credstore.encrypted", keyValueCredentialName),
			"encrypted-credential:" + keyValueURLCredentialPath(environment),
		},
	}
}

// inspectKeyValue reports the first-class key-value capability block: the
// qualified packaging identity plus the exact observed Valkey generation.
func inspectKeyValue(environment, account string, paths executionPaths) *host.KeyValueStatus {
	uid := command("id", "-u", account)
	quadletPath := ""
	if uid != "" {
		quadletPath = "/etc/containers/systemd/users/" + uid + "/provision-" + environment + "-valkey.container"
	}
	accountUIDValue := accountUID(account)
	status := &host.KeyValueStatus{
		SchemaVersion: host.KeyValueInspectionSchema, ObservationComplete: true, Findings: []string{},
		Capabilities: host.KeyValueCapabilities{
			PodmanVersion: installedPodmanPackageVersion(), Quadlet: commandSucceeded("test", "-x", "/usr/lib/systemd/system-generators/podman-system-generator"),
			RootlessEnvironmentAccount: hasAccount(account, "/var/lib/provision/runtime/"+environment), SystemdCredentials: commandSucceeded("systemd-creds", "--version"),
			SubordinateIDs:              fileContainsPrefix("/etc/subuid", account+":") && fileContainsPrefix("/etc/subgid", account+":"),
			LingeringUserManager:        rootOwned("/var/lib/systemd/linger/"+account, 0644) && uid != "" && command("systemctl", "is-active", "user@"+uid+".service") == "active",
			QuadletDefinitionRootOwned:  quadletPath != "" && rootOwned(quadletPath, 0644),
			EncryptedCredentialObserved: ownedBy(filepath.Join("/var/lib/provision/runtime", environment, ".config", "credstore.encrypted", keyValueCredentialName), accountUIDValue, 0600),
			Contract:                    hostkeyvalue.Contract, QuadletPath: quadletPath, ListenAddress: keyValueListenAddress, Port: keyValuePort, AppendOnly: "yes", AppendFsync: "everysec",
		},
	}
	matches, _ := filepath.Glob(filepath.Join(paths.environmentHome, "services", "valkey", "generations", "*", "generation.json"))
	switch len(matches) {
	case 0:
	case 1:
		data, err := os.ReadFile(matches[0])
		var generation keyValueGenerationRecord
		if err != nil || json.Unmarshal(data, &generation) != nil || generation.SchemaVersion != keyValueGenerationSchema {
			status.Findings = append(status.Findings, "managed Key-Value Store generation record is invalid")
			return status
		}
		observed, state := observeManagedKeyValue(context.Background(), generation.KeyValue, bootstrapRecord{Environment: environment, Account: account}, paths)
		status.Active = &observed.KeyValue
		if state != "satisfied" {
			status.Findings = append(status.Findings, "managed Key-Value Store generation is not an exact healthy observation")
		}
	default:
		status.Findings = append(status.Findings, "multiple managed Key-Value Store generations are not supported by this slice")
	}
	return status
}

func keyValueHTTPKeysMatch(binding planner.KeyValueHTTPBinding, observed []host.DeterministicKeyStatus) bool {
	if len(binding.Keys) == 0 || len(observed) != len(binding.Keys) {
		return false
	}
	for i, key := range binding.Keys {
		if observed[i].Key != key || observed[i].Value != binding.ValuePrefix+key {
			return false
		}
	}
	return true
}

// validateKeyValueBindings checks that an HTTP Generation binds only a verified
// prepared Key-Value Store generation, by logical reference, with its
// credential delivered as an encrypted credential file.
func validateKeyValueBindings(bindings []planner.KeyValueHTTPBinding, record bootstrapRecord, paths executionPaths) error {
	if len(bindings) > 1 {
		return errors.New("only one Key-Value Store binding is qualified for HTTP components")
	}
	for _, b := range bindings {
		if !deploymentIdentifier.MatchString(b.Component) || b.LogicalID != "provision-"+record.Environment+"-"+b.Component ||
			b.Reference != "secret://"+record.Environment+"/valkey-url" || b.Host != keyValueListenAddress || b.Port != keyValuePort ||
			b.EnvironmentVariable != "PROVISION_KEYVALUE_URL_FILE" || !strings.HasPrefix(b.Namespace, b.LogicalID+"/") || len(b.Keys) == 0 || b.ValuePrefix == "" {
			return errors.New("Key-Value Store binding does not match the bootstrapped Environment or logical Key-Value Store")
		}
		for _, key := range b.Keys {
			if !strings.HasPrefix(key, b.Namespace+"/") || strings.ContainsAny(key, ", \n") {
				return errors.New("Key-Value Store binding deterministic key is outside the planned namespace")
			}
		}
		var generation keyValueGenerationRecord
		path := filepath.Join(paths.environmentHome, "services", "valkey", "generations", b.GenerationID, "generation.json")
		if err := readExactJSON(path, &generation); err != nil || generation.SchemaVersion != keyValueGenerationSchema ||
			generation.KeyValue.LogicalID != b.LogicalID || generation.KeyValue.GenerationID != b.GenerationID ||
			!generation.Verified || generation.KeyValue.Account != record.Account || generation.KeyValue.CredentialReference != b.Reference || generation.KeyValue.Port != b.Port {
			return errors.New("Key-Value Store binding does not match a verified prepared Key-Value Store generation")
		}
	}
	return nil
}

// keyValueUnitBinding renders the workload's systemd credential and logical
// binding environment. The credential value is never in the unit text.
func keyValueUnitBinding(input planner.SystemdInput) string {
	if len(input.KeyValueBindings) != 1 {
		return ""
	}
	b := input.KeyValueBindings[0]
	environment := strings.TrimPrefix(input.Account, "provision-")
	return fmt.Sprintf(`LoadCredentialEncrypted=valkey-url:%s
Environment=PROVISION_KEYVALUE_COMPONENT=%s
Environment=PROVISION_KEYVALUE_LOGICAL_ID=%s
Environment=PROVISION_KEYVALUE_GENERATION=%s
Environment=PROVISION_KEYVALUE_NAMESPACE=%s
Environment=PROVISION_KEYVALUE_KEYS=%s
Environment=PROVISION_KEYVALUE_VALUE_PREFIX=%s
Environment=%s=%%d/valkey-url
`, keyValueURLCredentialPath(environment), b.Component, b.LogicalID, b.GenerationID, b.Namespace, strings.Join(b.Keys, ","), b.ValuePrefix, b.EnvironmentVariable)
}

// observedHTTPKeyValueBinding reads the logical binding a running HTTP unit
// declares, and whether its encrypted credential is delivered. It never reads
// the credential value.
func observedHTTPKeyValueBinding(environment string, unitEnvironment, unitCredentials []string) *host.HTTPKeyValueBindingStatus {
	values := map[string]string{}
	for _, entry := range unitEnvironment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	if values["PROVISION_KEYVALUE_LOGICAL_ID"] == "" && values["PROVISION_KEYVALUE_GENERATION"] == "" && values["PROVISION_KEYVALUE_URL_FILE"] == "" {
		return nil
	}
	status := &host.HTTPKeyValueBindingStatus{
		Component: values["PROVISION_KEYVALUE_COMPONENT"], LogicalID: values["PROVISION_KEYVALUE_LOGICAL_ID"],
		GenerationID: values["PROVISION_KEYVALUE_GENERATION"], EnvironmentVariable: "PROVISION_KEYVALUE_URL_FILE", BindingState: "declared",
	}
	credentialPath := keyValueURLCredentialPath(environment)
	if status.Component == "" || status.LogicalID == "" || status.GenerationID == "" || values["PROVISION_KEYVALUE_URL_FILE"] == "" {
		status.BindingState = "incomplete"
	} else if contains(unitCredentials, "valkey-url:"+credentialPath) && commandSucceeded("test", "-s", credentialPath) {
		status.BindingState = "key-value-bound"
	}
	return status
}
