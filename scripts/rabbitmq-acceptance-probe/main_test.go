package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsReadRabbitMQConfigWithoutLeakingUnrelatedValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rabbitmq.conf")
	contents := "listeners.tcp.default = 5672\ndefault_user = provision-issue36\ndefault_pass = correct-horse-battery-staple\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	username, password, err := credentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if username != "provision-issue36" {
		t.Fatalf("username = %q", username)
	}
	if password != "correct-horse-battery-staple" {
		t.Fatalf("password = %q", password)
	}
}

func TestCredentialsRejectMissingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rabbitmq.conf")
	if err := os.WriteFile(path, []byte("default_user = provision-issue36\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := credentials(path); err == nil {
		t.Fatal("missing password was accepted")
	}
}

func TestValidateMode(t *testing.T) {
	for _, mode := range []string{"roundtrip", "publish-only", "consume-existing"} {
		if err := validateMode(mode); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	if err := validateMode("exactly-once"); err == nil {
		t.Fatal("unsupported exactly-once mode was accepted")
	}
}
