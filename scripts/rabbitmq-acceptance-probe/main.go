package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const schemaVersion = "provision.dev/rabbitmq-packaging-proof/v1alpha1"

type result struct {
	SchemaVersion          string `json:"schemaVersion"`
	Mode                   string `json:"mode"`
	Queue                  string `json:"queue"`
	QueueType              string `json:"queueType"`
	MessageID              string `json:"messageId"`
	PublisherConfirmed     bool   `json:"publisherConfirmed"`
	ConsumerAcknowledged   bool   `json:"consumerAcknowledged"`
	MessagesAfterOperation int    `json:"messagesAfterOperation"`
	CredentialsSource      string `json:"credentialsSource"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "rabbitmq-acceptance-probe: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	credentialDirectory := os.Getenv("CREDENTIALS_DIRECTORY")
	if credentialDirectory == "" {
		return errors.New("systemd credentials directory is unavailable")
	}
	username, password, err := credentials(filepath.Join(credentialDirectory, "rabbitmq-config"))
	if err != nil {
		return err
	}
	defer func() { password = "" }()

	port := 25672
	if value := os.Getenv("PROVISION_RABBITMQ_PORT"); value != "" {
		port, err = strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("invalid RabbitMQ port")
		}
	}
	queueName := valueOrDefault("PROVISION_RABBITMQ_QUEUE", "provision-issue36")
	messageID := valueOrDefault("PROVISION_RABBITMQ_MESSAGE_ID", "issue36-live-proof")
	mode := valueOrDefault("PROVISION_RABBITMQ_MODE", "roundtrip")
	if err := validateMode(mode); err != nil {
		return err
	}

	config := amqp.Config{
		SASL:       []amqp.Authentication{&amqp.PlainAuth{Username: username, Password: password}},
		Vhost:      "/",
		Heartbeat:  10 * time.Second,
		Locale:     "en_US",
		Dial:       amqp.DefaultDial(5 * time.Second),
		Properties: amqp.Table{"connection_name": "provision-issue36-acceptance"},
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var connection *amqp.Connection
	deadline := time.Now().Add(60 * time.Second)
	for {
		connection, err = amqp.DialConfig("amqp://"+address+"/", config)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("RabbitMQ did not accept an authenticated AMQP connection within 60 seconds")
		}
		time.Sleep(time.Second)
	}
	defer connection.Close()

	channel, err := connection.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer channel.Close()

	queue, err := channel.QueueDeclare(
		queueName,
		true,
		false,
		false,
		false,
		amqp.Table{"x-queue-type": "quorum", "x-quorum-initial-group-size": int32(1)},
	)
	if err != nil {
		return fmt.Errorf("declare quorum queue: %w", err)
	}
	var confirmed bool
	if mode != "consume-existing" {
		if err := channel.Confirm(false); err != nil {
			return fmt.Errorf("enable publisher confirms: %w", err)
		}
		confirmations := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
		returns := channel.NotifyReturn(make(chan amqp.Return, 1))
		publishContext, cancelPublish := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelPublish()
		if err := channel.PublishWithContext(
			publishContext,
			"",
			queue.Name,
			true,
			false,
			amqp.Publishing{
				ContentType:  "text/plain",
				DeliveryMode: amqp.Persistent,
				MessageId:    messageID,
				Timestamp:    time.Now().UTC(),
				Body:         []byte("provision issue 36 live proof"),
			},
		); err != nil {
			return fmt.Errorf("publish message: %w", err)
		}

		select {
		case returned := <-returns:
			return fmt.Errorf("message was returned as unroutable with reply code %d", returned.ReplyCode)
		case confirmation := <-confirmations:
			if !confirmation.Ack {
				return errors.New("broker negatively acknowledged the persistent publish")
			}
			confirmed = true
		case <-publishContext.Done():
			return errors.New("publisher confirm timed out")
		}
	}

	var acknowledged bool
	if mode != "publish-only" {
		if err := channel.Qos(1, 0, false); err != nil {
			return fmt.Errorf("set consumer prefetch: %w", err)
		}
		deliveries, err := channel.Consume(queue.Name, "provision-issue36-acceptance", false, false, false, false, nil)
		if err != nil {
			return fmt.Errorf("start acknowledged consumer: %w", err)
		}
		consumeDeadline := time.NewTimer(15 * time.Second)
		defer consumeDeadline.Stop()
		select {
		case delivery := <-deliveries:
			if delivery.MessageId != messageID {
				return fmt.Errorf("received unexpected stable message identity %q", delivery.MessageId)
			}
			if err := delivery.Ack(false); err != nil {
				return fmt.Errorf("acknowledge delivery: %w", err)
			}
			acknowledged = true
		case <-consumeDeadline.C:
			return errors.New("acknowledged consumer did not receive the confirmed message")
		}
	}

	queue, err = channel.QueueInspect(queue.Name)
	if err != nil {
		return fmt.Errorf("inspect queue after acknowledgement: %w", err)
	}
	expectedMessages := 0
	if mode == "publish-only" {
		expectedMessages = 1
	}
	if queue.Messages != expectedMessages {
		return fmt.Errorf("queue contains %d messages after %s; expected %d", queue.Messages, mode, expectedMessages)
	}

	return json.NewEncoder(os.Stdout).Encode(result{
		SchemaVersion:          schemaVersion,
		Mode:                   mode,
		Queue:                  queue.Name,
		QueueType:              "quorum",
		MessageID:              messageID,
		PublisherConfirmed:     confirmed,
		ConsumerAcknowledged:   acknowledged,
		MessagesAfterOperation: queue.Messages,
		CredentialsSource:      "systemd-encrypted-credential",
	})
}

func validateMode(mode string) error {
	switch mode {
	case "roundtrip", "publish-only", "consume-existing":
		return nil
	default:
		return fmt.Errorf("unsupported proof mode %q", mode)
	}
}

func credentials(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", errors.New("open RabbitMQ systemd credential")
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", errors.New("read RabbitMQ systemd credential")
	}
	if values["default_user"] == "" || values["default_pass"] == "" {
		return "", "", errors.New("RabbitMQ systemd credential lacks default_user or default_pass")
	}
	return values["default_user"], values["default_pass"], nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
