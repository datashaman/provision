package main

import (
	"context"
	"encoding/json"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"provision/internal/config"
	"provision/internal/planner"
)

func TestEnvironmentCommandRunsFromAccessibleRuntimeHome(t *testing.T) {
	command := environmentCommand(context.Background(), bootstrapRecord{Environment: "lab", Account: "provision-lab"}, 102, "podman", "pull", "example.invalid/image@sha256:abc")
	if command.Dir != "/var/lib/provision/runtime/lab" {
		t.Fatalf("working directory = %q", command.Dir)
	}
	if got := strings.Join(command.Args, " "); !strings.Contains(got, "HOME=/var/lib/provision/runtime/lab") || !strings.Contains(got, "XDG_RUNTIME_DIR=/run/user/102") {
		t.Fatalf("environment command = %q", got)
	}
}

func TestSubordinateIdentityRangeIsBounded(t *testing.T) {
	for _, test := range []struct {
		id   int
		want bool
	}{{165535, false}, {165536, true}, {166534, true}, {231071, true}, {231072, false}} {
		if got := idInRange(test.id, 165536, 65536); got != test.want {
			t.Fatalf("idInRange(%d) = %t", test.id, got)
		}
	}
}

func TestPrepareQueueValidationPinsIdentityPathsAndContract(t *testing.T) {
	planned, record, paths := queueOperationFixture(t)
	if err := validateQueueOperation(planned, record, paths); err != nil {
		t.Fatalf("valid prepareQueue rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*planner.AsyncQueueInput)
	}{
		{"caller-selected data path", func(input *planner.AsyncQueueInput) { input.DataPath = filepath.Join(t.TempDir(), "escape") }},
		{"mutable image", func(input *planner.AsyncQueueInput) { input.ImageReference = "docker.io/library/rabbitmq:latest" }},
		{"unqualified retention", func(input *planner.AsyncQueueInput) { input.Contract.Retention = "unqualified" }},
		{"wrong account", func(input *planner.AsyncQueueInput) { input.Account = "root" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := planned
			input := *planned.Input.Async.Queue
			test.mutate(&input)
			tampered.Input.Async = &planner.AsyncOperationInput{Queue: &input}
			if err := validateQueueOperation(tampered, record, paths); err == nil {
				t.Fatal("tampered prepareQueue accepted")
			}
		})
	}
}

func TestQueueSecretBoundaryAndQuadletDoNotExposeResolvedValue(t *testing.T) {
	planned, _, _ := queueOperationFixture(t)
	input := *planned.Input.Async.Queue
	secret := "amqp://queue_user:LongRandomPassword_1234@127.0.0.1:25672/"
	username, password, err := parseQueueSecret(secret, input.AMQPPort)
	if err != nil || username != "queue_user" || password != "LongRandomPassword_1234" {
		t.Fatalf("valid resolved secret rejected: %q %q %v", username, password, err)
	}
	if _, err := validateQueueSensitiveValues(map[string]string{input.CredentialReference: secret}, input); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"amqp://queue_user:short@127.0.0.1:25672/",
		"amqp://queue_user:LongRandomPassword_1234@example.com:25672/",
		"amqp://queue_user:LongRandomPassword_1234@127.0.0.1:5672/",
	} {
		if _, _, err := parseQueueSecret(invalid, input.AMQPPort); err == nil || strings.Contains(err.Error(), "LongRandomPassword") {
			t.Fatalf("unsafe secret accepted or disclosed: %v", err)
		}
	}
	quadlet := renderQueueQuadlet(input, filepath.Join(filepath.Dir(input.DataPath), "credential-entrypoint"))
	if strings.Contains(quadlet, password) || !strings.Contains(quadlet, input.ImageReference) || !strings.Contains(quadlet, "LoadCredentialEncrypted=rabbitmq-config") {
		t.Fatalf("Quadlet omitted pinned identity or exposed a secret:\n%s", quadlet)
	}
}

func TestExactQueueTopologyRejectsMissingOrUnrelatedQueues(t *testing.T) {
	planned, _, _ := queueOperationFixture(t)
	input := *planned.Input.Async.Queue
	topology := topologyFor(input.LogicalID)
	encode := func(names ...string) []byte {
		queues := make([]map[string]any, 0, len(names))
		for _, name := range names {
			arguments := map[string]any{}
			switch name {
			case input.LogicalID:
				arguments = map[string]any{"x-delivery-limit": float64(queueDeliveryLimit), "x-message-ttl": float64(queueMessageTTL), "x-dead-letter-exchange": topology.DeadLetterExchange, "x-dead-letter-routing-key": topology.DeadLetterQueue}
			case topology.RetryQueue:
				arguments = map[string]any{"x-message-ttl": float64(retryDelay), "x-dead-letter-exchange": topology.WorkExchange, "x-dead-letter-routing-key": input.LogicalID}
			case topology.DeadLetterQueue:
				arguments = map[string]any{"x-message-ttl": float64(deadLetterTTL)}
			}
			queues = append(queues, map[string]any{"name": name, "type": "quorum", "durable": true, "arguments": arguments})
		}
		data, _ := json.Marshal(queues)
		return data
	}
	exchanges, _ := json.Marshal([]map[string]any{{"name": topology.WorkExchange, "type": "direct", "durable": true}, {"name": topology.RetryExchange, "type": "direct", "durable": true}, {"name": topology.DeadLetterExchange, "type": "direct", "durable": true}})
	bindings, _ := json.Marshal([]map[string]any{
		{"source_name": "", "destination_name": input.LogicalID, "routing_key": input.LogicalID},
		{"source_name": "", "destination_name": topology.RetryQueue, "routing_key": topology.RetryQueue},
		{"source_name": "", "destination_name": topology.DeadLetterQueue, "routing_key": topology.DeadLetterQueue},
		{"source_name": topology.WorkExchange, "destination_name": input.LogicalID, "routing_key": input.LogicalID},
		{"source_name": topology.RetryExchange, "destination_name": topology.RetryQueue, "routing_key": topology.RetryQueue},
		{"source_name": topology.DeadLetterExchange, "destination_name": topology.DeadLetterQueue, "routing_key": topology.DeadLetterQueue},
	})
	if !exactQueueTopology(encode(input.LogicalID, topology.RetryQueue, topology.DeadLetterQueue), exchanges, bindings, input) {
		t.Fatal("exact managed Queue topology rejected")
	}
	if exactQueueTopology(encode(input.LogicalID, topology.DeadLetterQueue), exchanges, bindings, input) || exactQueueTopology(encode(input.LogicalID, topology.RetryQueue, topology.DeadLetterQueue, "unrelated"), exchanges, bindings, input) {
		t.Fatal("incomplete or unrelated Queue topology accepted")
	}
}

func TestDecodeQueueArgumentsAcceptsRabbitMQ43AMQPTableJSON(t *testing.T) {
	arguments, ok := decodeQueueArguments(json.RawMessage(`[["x-queue-type","longstr","quorum"],["x-message-ttl","signedint",86400000],["x-dead-letter-exchange","longstr","managed.dead-letter"]]`))
	if !ok || arguments["x-queue-type"] != "quorum" || arguments["x-dead-letter-exchange"] != "managed.dead-letter" || !argumentNumber(arguments, "x-message-ttl", 86400000) {
		t.Fatalf("decoded arguments = %#v, %t", arguments, ok)
	}
	if _, ok := decodeQueueArguments(json.RawMessage(`[["x-message-ttl","signedint",1],["x-message-ttl","signedint",2]]`)); ok {
		t.Fatal("duplicate RabbitMQ Queue argument accepted")
	}
}

func TestQueueStatusIdentifiesOwnedResourcesByKind(t *testing.T) {
	planned, _, _ := queueOperationFixture(t)
	input := *planned.Input.Async.Queue
	topology := topologyFor(input.LogicalID)
	want := []string{
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
	}
	if got := queueStatusIdentity(input).OwnedResources; !slices.Equal(got, want) {
		t.Fatalf("owned resources = %#v, want %#v", got, want)
	}
}

func queueOperationFixture(t *testing.T) (planner.Operation, bootstrapRecord, executionPaths) {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.Atoi(current.Uid)
	if err != nil {
		t.Fatal(err)
	}
	environmentHome := filepath.Join(t.TempDir(), "environments", "lab")
	input := &planner.AsyncQueueInput{
		Component: "messages", LogicalID: "provision-lab-messages",
		GenerationID:   "provision-lab-messages-rabbitmq-4-3-6-" + strings.TrimPrefix(qualifiedManifest, "sha256:")[:12],
		Implementation: "rabbitmq-quadlet", Lifecycle: "managed", Rollout: "required",
		CredentialReference: "secret://lab/rabbitmq-url", RabbitMQVersion: qualifiedRabbitMQ,
		ImageIndex: qualifiedIndex, ImageManifest: qualifiedManifest, ImageReference: "docker.io/library/rabbitmq@" + qualifiedManifest,
		ServiceUnit: "provision-lab-rabbitmq.service", Container: "provision-lab-rabbitmq", Account: current.Username,
		DataPath:    filepath.Join(environmentHome, "services", "rabbitmq", "data"),
		QuadletPath: "/etc/containers/systemd/users/" + strconv.Itoa(uid) + "/provision-lab-rabbitmq.container",
		AMQPPort:    25672, QueueType: "quorum", Members: 1,
		Contract: config.QueueContract{Delivery: "at-least-once", Acknowledgement: "manual", PublisherConfirm: "required", Retry: "bounded-redelivery-3", DeadLetter: "required", Retention: "24h0m0s", Ordering: "unqualified", Deduplication: "unqualified"},
	}
	planned := planner.Operation{ID: "op-01", Kind: planner.PrepareQueue, DependsOn: []string{}, Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Queue: input}}, Recovery: planner.RetainQueue}
	return planned, bootstrapRecord{Environment: "lab", Account: current.Username}, executionPaths{environmentHome: environmentHome}
}
