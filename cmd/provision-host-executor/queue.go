package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planner"
)

const (
	queueGenerationSchema = "provision.dev/host-queue-generation/v1alpha1"
	queueProbeSchema      = "provision.dev/host-queue-probe/v1alpha1"
	qualifiedPodman       = "5.7.0+ds2-3build1"
	qualifiedRabbitMQ     = "4.3.6"
	qualifiedIndex        = "sha256:d0bffe70e755f348625415f32b0a090662e5f06b3ba3f82a4c7aaa18621b1279"
	qualifiedManifest     = "sha256:34fc91a9de04d612a340507b8e7e19c0ee1ec9839e09dc5fc98f54991633ce91"
	qualifiedEvidence     = "sha256:af41714b1aa2270ba6cd151bd24876ac117218e401e1e87515451a7081ac4c6d"
	queueCredentialName   = "rabbitmq-config"
	queueMessageTTL       = int32(24 * 60 * 60 * 1000)
	deadLetterTTL         = int32(7 * 24 * 60 * 60 * 1000)
	queueDeliveryLimit    = int32(3)
	retryDelay            = int32(10 * 1000)
)

var queueSecretPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var rabbitNodePattern = regexp.MustCompile(`rabbit@[A-Za-z0-9._-]+`)

type queueGenerationRecord struct {
	SchemaVersion string                  `json:"schemaVersion"`
	Queue         planner.AsyncQueueInput `json:"queue"`
	Topology      queueTopology           `json:"topology"`
}

type queueTopology struct {
	WorkExchange       string `json:"workExchange"`
	RetryExchange      string `json:"retryExchange"`
	DeadLetterExchange string `json:"deadLetterExchange"`
	RetryQueue         string `json:"retryQueue"`
	DeadLetterQueue    string `json:"deadLetterQueue"`
	MessageTTL         string `json:"messageTtl"`
	DeliveryLimit      int    `json:"deliveryLimit"`
}

type queueProbeRecord struct {
	SchemaVersion      string `json:"schemaVersion"`
	MessageID          string `json:"messageId"`
	PublisherConfirmed bool   `json:"publisherConfirmed"`
	Accepted           int    `json:"accepted"`
	Available          int    `json:"available"`
	Acknowledged       int    `json:"acknowledged"`
	DeadLettered       int    `json:"deadLettered"`
}

func validateQueueOperation(planned planner.Operation, record bootstrapRecord, paths executionPaths) error {
	if planned.Kind != planner.PrepareQueue || len(planned.DependsOn) != 0 || planned.Input.Async == nil || planned.Input.Async.Queue == nil || planned.Input.Artifact != nil || planned.Input.Generation != nil || planned.Input.Systemd != nil || planned.Input.Health != nil || planned.Input.Endpoint != nil || planned.Input.Previous != nil || planned.Input.Drain != nil || planned.Input.Retention != nil || planned.Input.Async.Artifact != nil || planned.Input.Async.Worker != nil || planned.Input.Async.Task != nil || planned.Input.Async.Schedule != nil || planned.Input.Async.Runtime != nil {
		return errors.New("prepareQueue requires only its typed Queue input and no dependency")
	}
	input := planned.Input.Async.Queue
	expectedService := "provision-" + record.Environment + "-rabbitmq"
	expectedData := filepath.Join(paths.environmentHome, "services", "rabbitmq", "data")
	expectedGeneration := "provision-" + record.Environment + "-" + input.Component + "-rabbitmq-4-3-6-" + strings.TrimPrefix(qualifiedManifest, "sha256:")[:12]
	if !deploymentIdentifier.MatchString(input.Component) || input.LogicalID != "provision-"+record.Environment+"-"+input.Component || input.GenerationID != expectedGeneration {
		return errors.New("Queue identity does not match the bootstrapped Environment")
	}
	if input.Implementation != "rabbitmq-quadlet" || input.Lifecycle != "managed" || input.Rollout != "required" || input.RabbitMQVersion != qualifiedRabbitMQ || input.ImageIndex != qualifiedIndex || input.ImageManifest != qualifiedManifest || input.ImageReference != "docker.io/library/rabbitmq@"+qualifiedManifest {
		return errors.New("Queue implementation does not match the qualified RabbitMQ product identity")
	}
	if input.ServiceUnit != expectedService+".service" || input.Container != expectedService || input.Account != record.Account || input.DataPath != expectedData || input.AMQPPort != 25672 {
		return errors.New("Queue service identity or owned path does not match the bootstrapped Environment")
	}
	uid := accountUID(record.Account)
	if uid <= 0 || input.QuadletPath != fmt.Sprintf("/etc/containers/systemd/users/%d/%s.container", uid, expectedService) {
		return errors.New("Queue Quadlet path does not match the bootstrapped Environment account")
	}
	if input.QueueType != "quorum" || input.Members != 1 || input.Contract != (config.QueueContract{Delivery: "at-least-once", Acknowledgement: "manual", PublisherConfirm: "required", Retry: "bounded-redelivery-3", DeadLetter: "required", Retention: "24h0m0s", Ordering: "unqualified", Deduplication: "unqualified"}) {
		return errors.New("Queue contract exceeds or differs from the qualified topology")
	}
	if input.CredentialReference != "secret://"+record.Environment+"/rabbitmq-url" {
		return errors.New("Queue credential Secret Reference does not match the Environment")
	}
	return nil
}

func validateQueueSensitiveValues(envelope map[string]string, input planner.AsyncQueueInput) (string, error) {
	if len(envelope) != 1 {
		return "", errors.New("prepareQueue requires exactly one resolved Secret Reference")
	}
	value, ok := envelope[input.CredentialReference]
	if !ok || value == "" {
		return "", errors.New("prepareQueue lacks its resolved Queue Secret Reference")
	}
	return value, nil
}

func observeQueueOperation(ctx context.Context, planned planner.Operation, record bootstrapRecord, paths executionPaths) (host.OperationObservation, error) {
	if err := validateQueueOperation(planned, record, paths); err != nil {
		return host.OperationObservation{}, err
	}
	observed, state := observeManagedQueue(ctx, *planned.Input.Async.Queue, record, paths)
	evidence, err := json.Marshal(observed)
	if err != nil {
		return host.OperationObservation{}, err
	}
	return host.OperationObservation{State: state, Evidence: evidence}, nil
}

func applyQueueOperation(ctx context.Context, planned planner.Operation, record bootstrapRecord, paths executionPaths, sensitiveValues map[string]string) (json.RawMessage, error) {
	if err := validateQueueOperation(planned, record, paths); err != nil {
		return nil, err
	}
	input := *planned.Input.Async.Queue
	secret, err := validateQueueSensitiveValues(sensitiveValues, input)
	if err != nil {
		return nil, err
	}
	if observed, state := observeManagedQueue(ctx, input, record, paths); state == "satisfied" {
		encoded, err := json.Marshal(observed)
		return encoded, err
	} else if state == "unknown" {
		encoded, _ := json.Marshal(observed)
		return encoded, errors.New("existing Queue resources do not match the approved generation")
	}
	username, password, err := parseQueueSecret(secret, input.AMQPPort)
	secret = ""
	if err != nil {
		return nil, err
	}
	defer func() { password = "" }()
	uid := accountUID(record.Account)
	gid := accountGID(record.Account)
	if uid <= 0 || gid <= 0 {
		return nil, errors.New("Environment account identity is unavailable")
	}
	serviceRoot := filepath.Dir(input.DataPath)
	runtimeHome := "/var/lib/provision/runtime/" + record.Environment
	credentialDir := filepath.Join(runtimeHome, ".config", "credstore.encrypted")
	for _, directory := range []struct {
		path string
		mode os.FileMode
		uid  int
		gid  int
	}{{serviceRoot, 0755, 0, 0}, {credentialDir, 0700, uid, gid}, {filepath.Dir(input.QuadletPath), 0755, 0, 0}} {
		if err := ensureDirectory(directory.path, directory.mode, directory.uid, directory.gid); err != nil {
			return nil, err
		}
	}
	if err := ensureEnvironmentDataDirectory(input.DataPath, record.Account, uid, gid); err != nil {
		return nil, err
	}
	credentialPath := filepath.Join(credentialDir, queueCredentialName)
	credentialConfig := fmt.Sprintf("listeners.tcp.default = 5672\ndefault_user = %s\ndefault_pass = %s\ndefault_queue_type = quorum\n", username, password)
	if err := installEncryptedCredential(ctx, credentialPath, queueCredentialName, uid, gid, credentialConfig); err != nil {
		return nil, err
	}
	entrypointPath := filepath.Join(serviceRoot, "credential-entrypoint")
	if err := installExactFile(entrypointPath, []byte(queueCredentialEntrypoint()), 0755, 0, 0); err != nil {
		return nil, err
	}
	if err := installExactFile(input.QuadletPath, []byte(renderQueueQuadlet(input, entrypointPath)), 0644, 0, 0); err != nil {
		return nil, err
	}
	if _, err := runAsEnvironment(ctx, record, "podman", "pull", input.ImageReference); err != nil {
		return nil, errors.New("pull approved RabbitMQ image")
	}
	if _, err := runAsEnvironment(ctx, record, "podman", "image", "inspect", input.ImageReference); err != nil {
		return nil, errors.New("verify approved RabbitMQ image")
	}
	if _, err := runAsEnvironment(ctx, record, "systemctl", "--user", "daemon-reload"); err != nil {
		return nil, errors.New("reload Environment user units")
	}
	// Quadlet's [Install] section generates the default.target dependency; its
	// generated service is transient and must not be enabled as a conventional
	// unit. Starting after daemon-reload matches the reboot-qualified path.
	if _, err := runAsEnvironment(ctx, record, "systemctl", "--user", "restart", input.ServiceUnit); err != nil {
		return nil, errors.New("start managed RabbitMQ service")
	}
	if err := waitForQueueService(ctx, record, input); err != nil {
		return nil, err
	}
	probe, err := provisionQueueTopology(ctx, input, username, password)
	password = ""
	credentialConfig = ""
	if err != nil {
		return nil, err
	}
	topology := topologyFor(input.LogicalID)
	recordPath := filepath.Join(serviceRoot, "generation.json")
	probePath := filepath.Join(serviceRoot, "probe.json")
	recordedInput := input
	recordedInput.Observed = nil
	if err := writeJSONAtomic(recordPath, queueGenerationRecord{SchemaVersion: queueGenerationSchema, Queue: recordedInput, Topology: topology}, 0444); err != nil {
		return nil, errors.New("record Queue generation")
	}
	if err := writeJSONAtomic(probePath, probe, 0444); err != nil {
		return nil, errors.New("record Queue probe disposition")
	}
	observed, state := observeManagedQueue(ctx, input, record, paths)
	encoded, encodeErr := json.Marshal(observed)
	if encodeErr != nil {
		return nil, encodeErr
	}
	if state != "satisfied" {
		return encoded, errors.New("prepared Queue failed exact verification")
	}
	return encoded, nil
}

func topologyFor(logicalID string) queueTopology {
	return queueTopology{
		WorkExchange: logicalID + ".work", RetryExchange: logicalID + ".retry",
		DeadLetterExchange: logicalID + ".dead-letter", RetryQueue: logicalID + ".retry",
		DeadLetterQueue: logicalID + ".dead-letter", MessageTTL: "24h0m0s", DeliveryLimit: int(queueDeliveryLimit),
	}
}

func parseQueueSecret(value string, port int) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "amqp" || parsed.User == nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/" {
		return "", "", errors.New("resolved Queue Secret Reference must contain a private AMQP URL")
	}
	username := parsed.User.Username()
	password, ok := parsed.User.Password()
	if !ok || !queueSecretPart.MatchString(username) || len(username) > 64 || !queueSecretPart.MatchString(password) || len(password) < 16 || len(password) > 128 {
		return "", "", errors.New("resolved Queue Secret Reference has unsupported credentials")
	}
	hostname := parsed.Hostname()
	observedPort, err := strconv.Atoi(parsed.Port())
	if err != nil || observedPort != port || hostname != "127.0.0.1" && hostname != "localhost" {
		return "", "", errors.New("resolved Queue Secret Reference must target the planned loopback listener")
	}
	return username, password, nil
}

func installEncryptedCredential(ctx context.Context, destination, name string, uid, gid int, plaintext string) error {
	temporary := destination + ".tmp"
	_ = os.Remove(temporary)
	command := exec.CommandContext(ctx, "systemd-creds", "encrypt", "--uid="+strconv.Itoa(uid), "--name="+name, "-", temporary)
	command.Stdin = strings.NewReader(plaintext)
	if output, err := command.CombinedOutput(); err != nil {
		_ = os.Remove(temporary)
		_ = output
		return errors.New("encrypt Queue credential")
	}
	if err := os.Chown(temporary, uid, gid); err != nil {
		_ = os.Remove(temporary)
		return errors.New("own encrypted Queue credential")
	}
	if err := os.Chmod(temporary, 0600); err != nil {
		_ = os.Remove(temporary)
		return errors.New("secure encrypted Queue credential")
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return errors.New("commit encrypted Queue credential")
	}
	return nil
}

func queueCredentialEntrypoint() string {
	return "#!/usr/bin/env bash\nset -euo pipefail\ninstall -d -o rabbitmq -g rabbitmq -m 0700 /run/provision-runtime\ninstall -o rabbitmq -g rabbitmq -m 0400 /run/provision-credential/rabbitmq.conf /run/provision-runtime/rabbitmq.conf\nexport RABBITMQ_CONFIG_FILE=/run/provision-runtime/rabbitmq.conf\nexec /usr/local/bin/docker-entrypoint.sh \"$@\"\n"
}

func renderQueueQuadlet(input planner.AsyncQueueInput, entrypoint string) string {
	return fmt.Sprintf(`[Unit]
Description=Provision managed RabbitMQ Queue generation %s
Wants=network-online.target
After=network-online.target

[Container]
Image=%s
ContainerName=%s
PublishPort=127.0.0.1:%d:5672
Volume=%s:/var/lib/rabbitmq
Volume=%%d/%s:/run/provision-credential/rabbitmq.conf:ro
Volume=%s:/usr/local/bin/provision-rabbitmq-credential-entrypoint:ro
Tmpfs=/run/provision-runtime:rw,mode=0755
Entrypoint=/usr/local/bin/provision-rabbitmq-credential-entrypoint
Exec=rabbitmq-server
Environment=RABBITMQ_NODENAME=rabbit@%s
HealthCmd=rabbitmq-diagnostics -q ping
HealthInterval=5s
HealthRetries=24
NoNewPrivileges=true

[Service]
LoadCredentialEncrypted=%s
Restart=always
TimeoutStartSec=900

[Install]
WantedBy=default.target
`, input.GenerationID, input.ImageReference, input.Container, input.AMQPPort, input.DataPath, queueCredentialName, entrypoint, input.Container, queueCredentialName)
}

func ensureDirectory(path string, mode os.FileMode, uid, gid int) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil {
			return errors.New("create Queue-owned directory")
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return errors.New("own Queue-owned directory")
		}
		return os.Chmod(path, mode)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return errors.New("Queue-owned directory is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
		return errors.New("Queue-owned directory ownership differs")
	}
	return nil
}

func ensureEnvironmentDataDirectory(path, account string, uid, gid int) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return ensureDirectory(path, 0700, uid, gid)
	}
	if !environmentDataOwned(path, account, uid, gid) {
		return errors.New("Queue data directory is not owned by the Environment account or its subordinate IDs")
	}
	return nil
}

func environmentDataOwned(path, account string, uid, gid int) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	uidStart, uidCount, uidOK := subordinateIDRange("/etc/subuid", account)
	gidStart, gidCount, gidOK := subordinateIDRange("/etc/subgid", account)
	uidOwned := int(stat.Uid) == uid || uidOK && idInRange(int(stat.Uid), uidStart, uidCount)
	gidOwned := int(stat.Gid) == gid || gidOK && idInRange(int(stat.Gid), gidStart, gidCount)
	return uidOwned && gidOwned
}

func subordinateIDRange(path, account string) (int, int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 3 || fields[0] != account {
			continue
		}
		start, startErr := strconv.Atoi(fields[1])
		count, countErr := strconv.Atoi(fields[2])
		if startErr == nil && countErr == nil && start > 0 && count > 0 {
			return start, count, true
		}
	}
	return 0, 0, false
}

func idInRange(id, start, count int) bool {
	return id >= start && id-start < count
}

func installExactFile(path string, data []byte, mode os.FileMode, uid, gid int) error {
	if existing, err := os.ReadFile(path); err == nil {
		info, statErr := os.Lstat(path)
		stat, ok := info.Sys().(*syscall.Stat_t)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode || !ok || int(stat.Uid) != uid || int(stat.Gid) != gid || !bytes.Equal(existing, data) {
			return errors.New("existing Queue-owned file differs from the approved generation")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect Queue-owned file")
	}
	temporary := path + ".tmp"
	_ = os.Remove(temporary)
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return errors.New("write Queue-owned file")
	}
	if err := os.Chown(temporary, uid, gid); err != nil {
		_ = os.Remove(temporary)
		return errors.New("own Queue-owned file")
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return errors.New("commit Queue-owned file")
	}
	return nil
}

func waitForQueueService(ctx context.Context, record bootstrapRecord, input planner.AsyncQueueInput) error {
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
	return errors.New("managed RabbitMQ service did not become healthy")
}

func provisionQueueTopology(ctx context.Context, input planner.AsyncQueueInput, username, password string) (queueProbeRecord, error) {
	config := amqp.Config{SASL: []amqp.Authentication{&amqp.PlainAuth{Username: username, Password: password}}, Heartbeat: 10 * time.Second, Locale: "en_US", Dial: amqp.DefaultDial(5 * time.Second), Properties: amqp.Table{"connection_name": "provision-queue-preparation"}}
	address := fmt.Sprintf("amqp://127.0.0.1:%d/", input.AMQPPort)
	var connection *amqp.Connection
	deadline := time.Now().Add(time.Minute)
	for {
		var err error
		connection, err = amqp.DialConfig(address, config)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return queueProbeRecord{}, errors.New("managed RabbitMQ did not accept an authenticated AMQP connection within 60 seconds")
		}
		select {
		case <-ctx.Done():
			return queueProbeRecord{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		return queueProbeRecord{}, errors.New("open RabbitMQ topology channel")
	}
	defer channel.Close()
	topology := topologyFor(input.LogicalID)
	for _, exchange := range []string{topology.WorkExchange, topology.RetryExchange, topology.DeadLetterExchange} {
		if err := channel.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
			return queueProbeRecord{}, errors.New("declare Queue exchange topology")
		}
	}
	mainArgs := amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": queueDeliveryLimit, "x-dead-letter-exchange": topology.DeadLetterExchange, "x-dead-letter-routing-key": topology.DeadLetterQueue, "x-message-ttl": queueMessageTTL}
	if _, err := channel.QueueDeclare(input.LogicalID, true, false, false, false, mainArgs); err != nil {
		return queueProbeRecord{}, errors.New("declare managed quorum Queue")
	}
	retryArgs := amqp.Table{"x-queue-type": "quorum", "x-message-ttl": retryDelay, "x-dead-letter-exchange": topology.WorkExchange, "x-dead-letter-routing-key": input.LogicalID}
	if _, err := channel.QueueDeclare(topology.RetryQueue, true, false, false, false, retryArgs); err != nil {
		return queueProbeRecord{}, errors.New("declare managed retry Queue")
	}
	deadArgs := amqp.Table{"x-queue-type": "quorum", "x-message-ttl": deadLetterTTL}
	if _, err := channel.QueueDeclare(topology.DeadLetterQueue, true, false, false, false, deadArgs); err != nil {
		return queueProbeRecord{}, errors.New("declare managed dead-letter Queue")
	}
	for _, binding := range []struct{ queue, key, exchange string }{{input.LogicalID, input.LogicalID, topology.WorkExchange}, {topology.RetryQueue, topology.RetryQueue, topology.RetryExchange}, {topology.DeadLetterQueue, topology.DeadLetterQueue, topology.DeadLetterExchange}} {
		if err := channel.QueueBind(binding.queue, binding.key, binding.exchange, false, nil); err != nil {
			return queueProbeRecord{}, errors.New("bind managed Queue topology")
		}
	}
	if err := channel.Confirm(false); err != nil {
		return queueProbeRecord{}, errors.New("enable Queue publisher confirms")
	}
	confirmations := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	returns := channel.NotifyReturn(make(chan amqp.Return, 1))
	messageID := "provision-queue-probe-" + strings.TrimPrefix(input.ImageManifest, "sha256:")[:12]
	if err := channel.PublishWithContext(ctx, topology.WorkExchange, input.LogicalID, true, false, amqp.Publishing{ContentType: "text/plain", DeliveryMode: amqp.Persistent, MessageId: messageID, Timestamp: time.Now().UTC(), Body: []byte("provision managed Queue acceptance probe")}); err != nil {
		return queueProbeRecord{}, errors.New("publish Queue acceptance probe")
	}
	select {
	case <-returns:
		return queueProbeRecord{}, errors.New("Queue acceptance probe was returned")
	case confirmation := <-confirmations:
		if !confirmation.Ack {
			return queueProbeRecord{}, errors.New("Queue acceptance probe was negatively acknowledged")
		}
	case <-ctx.Done():
		return queueProbeRecord{}, ctx.Err()
	case <-time.After(15 * time.Second):
		return queueProbeRecord{}, errors.New("Queue acceptance probe confirmation timed out")
	}
	delivery, ok, err := channel.Get(input.LogicalID, false)
	if err != nil || !ok || delivery.MessageId != messageID {
		return queueProbeRecord{}, errors.New("Queue acceptance probe was not available with its stable identity")
	}
	if err := delivery.Ack(false); err != nil {
		return queueProbeRecord{}, errors.New("acknowledge Queue acceptance probe")
	}
	main, err := channel.QueueInspect(input.LogicalID)
	if err != nil || main.Messages != 0 {
		return queueProbeRecord{}, errors.New("Queue acceptance probe disposition is ambiguous")
	}
	dead, err := channel.QueueInspect(topology.DeadLetterQueue)
	if err != nil {
		return queueProbeRecord{}, errors.New("inspect managed dead-letter Queue")
	}
	return queueProbeRecord{SchemaVersion: queueProbeSchema, MessageID: messageID, PublisherConfirmed: true, Accepted: 1, Available: main.Messages, Acknowledged: 1, DeadLettered: dead.Messages}, nil
}

func observeManagedQueue(ctx context.Context, input planner.AsyncQueueInput, record bootstrapRecord, paths executionPaths) (host.QueueStatus, string) {
	status := queueStatusIdentity(input)
	serviceRoot := filepath.Dir(input.DataPath)
	recordPath := filepath.Join(serviceRoot, "generation.json")
	data, err := os.ReadFile(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return status, "pending"
	}
	if err != nil {
		status.Health = "unknown"
		status.Reason = "read Queue generation record"
		return status, "unknown"
	}
	var generation queueGenerationRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&generation) != nil || generation.SchemaVersion != queueGenerationSchema || !queueInputsEqual(generation.Queue, input) || generation.Topology != topologyFor(input.LogicalID) {
		status.Health = "drifted"
		status.Reason = "Queue generation record differs from the approved generation"
		return status, "unknown"
	}
	if !rootOwned(recordPath, 0444) || !rootOwned(input.QuadletPath, 0644) || !rootOwned(filepath.Join(serviceRoot, "credential-entrypoint"), 0755) {
		status.Health = "unsafe-owned-resource"
		status.Reason = "Queue generation files have unsafe ownership or modes"
		return status, "unknown"
	}
	quadlet, quadletErr := os.ReadFile(input.QuadletPath)
	entrypointPath := filepath.Join(serviceRoot, "credential-entrypoint")
	entrypoint, entrypointErr := os.ReadFile(entrypointPath)
	uid := accountUID(record.Account)
	credentialPath := filepath.Join("/var/lib/provision/runtime", record.Environment, ".config", "credstore.encrypted", queueCredentialName)
	if quadletErr != nil || string(quadlet) != renderQueueQuadlet(input, entrypointPath) || entrypointErr != nil || string(entrypoint) != queueCredentialEntrypoint() || uid <= 0 || !ownedBy(credentialPath, uid, 0600) || !environmentDataOwned(input.DataPath, record.Account, uid, accountGID(record.Account)) {
		status.Health = "drifted-owned-resource"
		status.Reason = "Queue runtime files or data ownership differ from the approved generation"
		return status, "unknown"
	}
	active, activeErr := runAsEnvironment(ctx, record, "systemctl", "--user", "is-active", input.ServiceUnit)
	health, healthErr := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.State.Health.Status}}", input.Container)
	manifest, manifestErr := runAsEnvironment(ctx, record, "podman", "inspect", "--format", "{{.ImageDigest}}", input.Container)
	version, versionErr := runAsEnvironment(ctx, record, "podman", "exec", input.Container, "rabbitmqctl", "version")
	queues, queuesErr := runAsEnvironment(ctx, record, "podman", "exec", input.Container, "rabbitmqctl", "list_queues", "-q", "name", "type", "durable", "arguments", "messages", "--formatter", "json")
	exchanges, exchangesErr := runAsEnvironment(ctx, record, "podman", "exec", input.Container, "rabbitmqctl", "list_exchanges", "-q", "name", "type", "durable", "--formatter", "json")
	bindings, bindingsErr := runAsEnvironment(ctx, record, "podman", "exec", input.Container, "rabbitmqctl", "list_bindings", "-q", "source_name", "destination_name", "routing_key", "--formatter", "json")
	quorum, quorumErr := runAsEnvironment(ctx, record, "podman", "exec", input.Container, "rabbitmq-queues", "quorum_status", "--vhost", "/", input.LogicalID)
	if activeErr != nil {
		status.Health = "unobservable"
		status.Reason = "query managed Queue service state"
		return status, "unknown"
	}
	if healthErr != nil || manifestErr != nil {
		status.Health = "unobservable"
		status.Reason = "query managed Queue container identity or health"
		return status, "unknown"
	}
	if versionErr != nil {
		status.Health = "unobservable"
		status.Reason = "query RabbitMQ product version"
		return status, "unknown"
	}
	if queuesErr != nil || exchangesErr != nil || bindingsErr != nil {
		status.Health = "unobservable"
		status.Reason = "query RabbitMQ Queue topology"
		return status, "unknown"
	}
	if quorumErr != nil {
		status.Health = "unobservable"
		status.Reason = "query RabbitMQ quorum membership"
		return status, "unknown"
	}
	if strings.TrimSpace(string(active)) != "active" {
		status.Health = "drifted"
		status.Reason = "managed Queue service is not active"
		return status, "unknown"
	}
	if strings.TrimSpace(string(health)) != "healthy" {
		status.Health = "drifted"
		status.Reason = "managed Queue container is not healthy"
		return status, "unknown"
	}
	if strings.TrimSpace(string(manifest)) != input.ImageManifest {
		status.Health = "drifted"
		status.Reason = "managed Queue container image manifest differs"
		return status, "unknown"
	}
	if strings.TrimSpace(string(version)) != input.RabbitMQVersion {
		status.Health = "drifted"
		status.Reason = "RabbitMQ product version differs"
		return status, "unknown"
	}
	if topologyReason := queueTopologyDriftReason(queues, exchanges, bindings, input); topologyReason != "" {
		status.Health = "drifted"
		status.Reason = topologyReason
		return status, "unknown"
	}
	if !exactQuorumMember(quorum, input.Container) {
		status.Health = "drifted"
		status.Reason = "RabbitMQ quorum membership differs from the single approved node"
		return status, "unknown"
	}
	probeData, err := os.ReadFile(filepath.Join(serviceRoot, "probe.json"))
	var probe queueProbeRecord
	if err != nil || json.Unmarshal(probeData, &probe) != nil || probe.SchemaVersion != queueProbeSchema || !probe.PublisherConfirmed || probe.Accepted != probe.Available+probe.Acknowledged+probe.DeadLettered || probe.Accepted < 1 {
		status.Health = "probe-unaccounted"
		status.Reason = "Queue acceptance probe disposition is not fully accounted"
		return status, "unknown"
	}
	status.Exists = true
	status.Ready = true
	status.Durable = true
	status.RabbitMQVersion = strings.TrimSpace(string(version))
	status.Health = "healthy"
	status.Accepted, status.Available, status.Acknowledged, status.DeadLettered = probe.Accepted, probe.Available, probe.Acknowledged, probe.DeadLettered
	status.ProbeMessageID = probe.MessageID
	return status, "satisfied"
}

func inspectRecordedQueue(ctx context.Context, environment, account string, paths executionPaths) (*host.QueueStatus, []string) {
	path := filepath.Join(paths.environmentHome, "services", "rabbitmq", "generation.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{"managed Queue generation record cannot be read"}
	}
	var generation queueGenerationRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&generation) != nil || generation.SchemaVersion != queueGenerationSchema {
		return nil, []string{"managed Queue generation record is invalid"}
	}
	status, state := observeManagedQueue(ctx, generation.Queue, bootstrapRecord{Environment: environment, Account: account}, paths)
	if state != "satisfied" {
		return &status, []string{"managed Queue generation is not an exact healthy observation"}
	}
	return &status, nil
}

func queueInputsEqual(left, right planner.AsyncQueueInput) bool {
	left.Observed = nil
	right.Observed = nil
	return reflect.DeepEqual(left, right)
}

func queueStatusIdentity(input planner.AsyncQueueInput) host.QueueStatus {
	topology := topologyFor(input.LogicalID)
	return host.QueueStatus{ID: input.LogicalID, GenerationID: input.GenerationID, QueueType: input.QueueType, Members: input.Members, ImageManifest: input.ImageManifest, ServiceUnit: input.ServiceUnit, Container: input.Container, Account: input.Account, DataPath: input.DataPath, QuadletPath: input.QuadletPath, RetryQueue: topology.RetryQueue, DeadLetterQueue: topology.DeadLetterQueue, MessageTTL: topology.MessageTTL, DeliveryLimit: topology.DeliveryLimit, SupportedGuarantees: []string{"publisher-confirms", "manual-acknowledgement", "at-least-once", "bounded-redelivery-3", "dead-lettering", "24-hour-message-retention"}, OwnedResources: []string{
		"systemd-unit:" + input.ServiceUnit,
		"container:" + input.Container,
		"data-path:" + input.DataPath,
		"quadlet:" + input.QuadletPath,
		"rabbitmq-queue:" + input.LogicalID,
		"rabbitmq-queue:" + topology.RetryQueue,
		"rabbitmq-queue:" + topology.DeadLetterQueue,
		"rabbitmq-exchange:" + topology.WorkExchange,
		"rabbitmq-exchange:" + topology.RetryExchange,
		"rabbitmq-exchange:" + topology.DeadLetterExchange,
	}}
}

func exactQueueTopology(queueData, exchangeData, bindingData []byte, input planner.AsyncQueueInput) bool {
	return queueTopologyDriftReason(queueData, exchangeData, bindingData, input) == ""
}

func queueTopologyDriftReason(queueData, exchangeData, bindingData []byte, input planner.AsyncQueueInput) string {
	var queues []struct {
		Name      string          `json:"name"`
		Type      string          `json:"type"`
		Durable   bool            `json:"durable"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(queueData, &queues) != nil {
		return "RabbitMQ Queue inventory is not supported JSON"
	}
	topology := topologyFor(input.LogicalID)
	wanted := []string{input.LogicalID, topology.RetryQueue, topology.DeadLetterQueue}
	sort.Strings(wanted)
	observed := make([]string, 0, len(queues))
	for _, queue := range queues {
		if queue.Type != "quorum" || !queue.Durable {
			return "RabbitMQ Queue inventory contains a non-durable or non-quorum Queue"
		}
		arguments, ok := decodeQueueArguments(queue.Arguments)
		if !ok {
			return "RabbitMQ Queue arguments use an unsupported JSON representation"
		}
		switch queue.Name {
		case input.LogicalID:
			if !argumentNumber(arguments, "x-delivery-limit", int64(queueDeliveryLimit)) || !argumentNumber(arguments, "x-message-ttl", int64(queueMessageTTL)) || arguments["x-dead-letter-exchange"] != topology.DeadLetterExchange || arguments["x-dead-letter-routing-key"] != topology.DeadLetterQueue {
				return "managed Queue delivery-limit, retention, or dead-letter arguments differ"
			}
		case topology.RetryQueue:
			if !argumentNumber(arguments, "x-message-ttl", int64(retryDelay)) || arguments["x-dead-letter-exchange"] != topology.WorkExchange || arguments["x-dead-letter-routing-key"] != input.LogicalID {
				return "managed retry Queue delay or dead-letter routing arguments differ"
			}
		case topology.DeadLetterQueue:
			if !argumentNumber(arguments, "x-message-ttl", int64(deadLetterTTL)) {
				return "managed dead-letter Queue retention arguments differ"
			}
		default:
			return "RabbitMQ Queue inventory contains an unrelated Queue"
		}
		observed = append(observed, queue.Name)
	}
	sort.Strings(observed)
	if strings.Join(observed, "\x00") != strings.Join(wanted, "\x00") {
		return "RabbitMQ Queue inventory is missing an exact managed Queue"
	}
	var exchanges []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Durable bool   `json:"durable"`
	}
	if json.Unmarshal(exchangeData, &exchanges) != nil {
		return "RabbitMQ exchange inventory is not supported JSON"
	}
	wantedExchanges := map[string]bool{topology.WorkExchange: false, topology.RetryExchange: false, topology.DeadLetterExchange: false}
	for _, exchange := range exchanges {
		if _, managed := wantedExchanges[exchange.Name]; managed {
			if exchange.Type != "direct" || !exchange.Durable {
				return "managed RabbitMQ exchange type or durability differs"
			}
			wantedExchanges[exchange.Name] = true
		} else if strings.HasPrefix(exchange.Name, input.LogicalID+".") {
			return "RabbitMQ exchange inventory contains an unrelated managed-prefix exchange"
		}
	}
	for _, found := range wantedExchanges {
		if !found {
			return "RabbitMQ exchange inventory is missing an exact managed exchange"
		}
	}
	var bindings []struct {
		Source      string `json:"source_name"`
		Destination string `json:"destination_name"`
		RoutingKey  string `json:"routing_key"`
	}
	if json.Unmarshal(bindingData, &bindings) != nil {
		return "RabbitMQ binding inventory is not supported JSON"
	}
	wantedBindings := map[string]bool{
		"\x00" + input.LogicalID + "\x00" + input.LogicalID:                                                 false,
		"\x00" + topology.RetryQueue + "\x00" + topology.RetryQueue:                                         false,
		"\x00" + topology.DeadLetterQueue + "\x00" + topology.DeadLetterQueue:                               false,
		topology.WorkExchange + "\x00" + input.LogicalID + "\x00" + input.LogicalID:                         false,
		topology.RetryExchange + "\x00" + topology.RetryQueue + "\x00" + topology.RetryQueue:                false,
		topology.DeadLetterExchange + "\x00" + topology.DeadLetterQueue + "\x00" + topology.DeadLetterQueue: false,
	}
	for _, binding := range bindings {
		key := binding.Source + "\x00" + binding.Destination + "\x00" + binding.RoutingKey
		if _, managed := wantedBindings[key]; managed {
			wantedBindings[key] = true
		} else if strings.HasPrefix(binding.Source, input.LogicalID+".") || strings.HasPrefix(binding.Destination, input.LogicalID+".") {
			return "RabbitMQ binding inventory contains an unrelated managed-prefix binding"
		}
	}
	for _, found := range wantedBindings {
		if !found {
			return "RabbitMQ binding inventory is missing an exact managed or default binding"
		}
	}
	return ""
}

func decodeQueueArguments(data json.RawMessage) (map[string]any, bool) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&object) == nil && object != nil {
		return object, true
	}
	var encodedEntries []json.RawMessage
	if json.Unmarshal(data, &encodedEntries) != nil {
		return nil, false
	}
	arguments := make(map[string]any, len(encodedEntries))
	for _, encodedEntry := range encodedEntries {
		var entry []json.RawMessage
		if json.Unmarshal(encodedEntry, &entry) != nil || len(entry) != 3 {
			return nil, false
		}
		var name, valueType string
		if json.Unmarshal(entry[0], &name) != nil || name == "" || json.Unmarshal(entry[1], &valueType) != nil || valueType == "" {
			return nil, false
		}
		if _, duplicate := arguments[name]; duplicate {
			return nil, false
		}
		var value any
		valueDecoder := json.NewDecoder(bytes.NewReader(entry[2]))
		valueDecoder.UseNumber()
		if valueDecoder.Decode(&value) != nil {
			return nil, false
		}
		arguments[name] = value
	}
	return arguments, true
}

func argumentNumber(arguments map[string]any, name string, want int64) bool {
	value, ok := arguments[name]
	if !ok {
		return false
	}
	switch number := value.(type) {
	case float64:
		return int64(number) == want && float64(int64(number)) == number
	case json.Number:
		observed, err := number.Int64()
		return err == nil && observed == want
	default:
		return false
	}
}

func exactQuorumMember(data []byte, container string) bool {
	members := map[string]bool{}
	for _, member := range rabbitNodePattern.FindAllString(string(data), -1) {
		members[member] = true
	}
	return len(members) == 1 && members["rabbit@"+container]
}

func runAsEnvironment(ctx context.Context, record bootstrapRecord, name string, args ...string) ([]byte, error) {
	uid := accountUID(record.Account)
	if uid <= 0 {
		return nil, errors.New("Environment account is unavailable")
	}
	return environmentCommand(ctx, record, uid, name, args...).CombinedOutput()
}

func environmentCommand(ctx context.Context, record bootstrapRecord, uid int, name string, args ...string) *exec.Cmd {
	runtimeHome := "/var/lib/provision/runtime/" + record.Environment
	commandArgs := []string{"-u", record.Account, "--", "env", "HOME=" + runtimeHome, "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(uid), "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + strconv.Itoa(uid) + "/bus", name}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, "runuser", commandArgs...)
	command.Dir = runtimeHome
	return command
}

func accountUID(account string) int { return accountID(account, "-u") }
func accountGID(account string) int { return accountID(account, "-g") }

func accountID(account, flag string) int {
	value := command("id", flag, account)
	id, _ := strconv.Atoi(value)
	return id
}
