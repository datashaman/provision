package execution

import (
	"strings"
	"testing"
	"time"

	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planner"
)

func TestResolvedQueueSecretIsSelectedOnlyForTheTypedOperation(t *testing.T) {
	queue := planner.Operation{
		ID: "op-01", Kind: planner.PrepareQueue,
		Input: planner.OperationInput{Async: &planner.AsyncOperationInput{Queue: &planner.AsyncQueueInput{CredentialReference: "secret://lab/rabbitmq-url"}}},
	}
	http := planner.Operation{ID: "op-02", Kind: planner.StartCandidate}
	plan := planner.Plan{Operations: []planner.Operation{queue, http}}
	secret := "amqp://queue_user:LongRandomPassword_1234@127.0.0.1:25672/"

	selected, err := resolvedValuesForOperation(plan, "op-01", map[string]string{
		"secret://lab/rabbitmq-url": secret,
		"secret://lab/unrelated":    "must-not-cross-boundary",
	})
	if err != nil || len(selected) != 1 || selected["secret://lab/rabbitmq-url"] != secret {
		t.Fatalf("resolved selection = %#v, %v", selected, err)
	}
	if selected, err := resolvedValuesForOperation(plan, "op-02", map[string]string{"secret://lab/rabbitmq-url": secret}); err != nil || len(selected) != 0 {
		t.Fatalf("non-secret operation received resolved values: %#v, %v", selected, err)
	}
	if _, err := resolvedValuesForOperation(plan, "op-01", nil); err == nil || strings.Contains(err.Error(), "LongRandomPassword") {
		t.Fatalf("missing secret was accepted or disclosed: %v", err)
	}
}

func TestResolvedKeyValueSecretIsSelectedOnlyForTheTypedOperation(t *testing.T) {
	op := planner.Operation{ID: "op-01", Kind: planner.PrepareKeyValue, Input: planner.OperationInput{KeyValue: &planner.KeyValuePlan{CredentialReference: "secret://lab/valkey-url"}}}
	plan := planner.Plan{Operations: []planner.Operation{op}}
	selected, err := resolvedValuesForOperation(plan, "op-01", map[string]string{"secret://lab/valkey-url": "valkey://u:p@127.0.0.1:26379", "secret://lab/other": "no"})
	if err != nil || len(selected) != 1 {
		t.Fatalf("resolved selection = %#v, %v", selected, err)
	}
	if _, err := resolvedValuesForOperation(plan, "op-01", nil); err == nil || strings.Contains(err.Error(), "valkey://") {
		t.Fatalf("missing secret accepted or disclosed: %v", err)
	}
}

func TestOffHostBackupKeyReachesOnlyBackupAndRestoreOperations(t *testing.T) {
	database := &planner.DatabaseOperationInput{CredentialReference: "secret://lab/postgresql-url", Backup: config.DatabaseBackupPolicy{CredentialReference: "secret://lab/backup-ssh-key"}}
	plan := planner.Plan{Operations: []planner.Operation{
		{ID: "op-01", Kind: planner.PrepareDatabase, Input: planner.OperationInput{Database: database}},
		{ID: "op-02", Kind: planner.BackupDatabase, Input: planner.OperationInput{Database: database}},
		{ID: "op-03", Kind: planner.VerifyDatabaseRestore, Input: planner.OperationInput{Database: database}},
	}}
	available := map[string]string{"secret://lab/postgresql-url": "db", "secret://lab/backup-ssh-key": "key"}
	for id, want := range map[string]int{"op-01": 1, "op-02": 2, "op-03": 2} {
		selected, err := resolvedValuesForOperation(plan, id, available)
		if err != nil || len(selected) != want {
			t.Fatalf("%s selected %v, %v; want %d values", id, selected, err, want)
		}
	}
	if _, err := resolvedValuesForOperation(plan, "op-02", map[string]string{"secret://lab/postgresql-url": "db"}); err == nil {
		t.Fatal("backup without its resolved key was accepted")
	}
}

func TestOffHostBackupSuccessRequiresDigestAndPinnedHostIdentity(t *testing.T) {
	pin := "SHA256:" + strings.Repeat("A", 43)
	input := &planner.DatabaseOperationInput{Backup: config.DatabaseBackupPolicy{Destination: "ssh://backup.example/b", HostKey: pin, CredentialReference: "secret://lab/k"}, Recovery: config.DatabaseRecoveryPolicy{PointObjective: "1h0m0s"}}
	planned := planner.Operation{Kind: planner.BackupDatabase, Input: planner.OperationInput{Database: input}}
	observed := host.DatabaseOperationObservation{TransitionPhase: "backupDatabase", Backup: &host.DatabaseBackupObservation{
		SchemaVersion: "provision.dev/database-backup/v1alpha1", DestinationClass: "off-host-ssh", DestinationReference: input.Backup.Destination, OutsideGeneration: true,
		AchievedRecoveryPoint: time.Now().UTC().Format(time.RFC3339Nano), ArtifactDigest: "sha256:" + strings.Repeat("a", 64), DestinationHost: "backup.example", HostKeyFingerprint: pin,
	}}
	if err := verifyDatabaseTransitionSuccess(planned, observed); err != nil {
		t.Fatalf("complete off-host evidence rejected: %v", err)
	}
	for name, mutate := range map[string]func(*host.DatabaseBackupObservation){
		"no digest":      func(b *host.DatabaseBackupObservation) { b.ArtifactDigest = "" },
		"no host":        func(b *host.DatabaseBackupObservation) { b.DestinationHost = "" },
		"other host key": func(b *host.DatabaseBackupObservation) { b.HostKeyFingerprint = "SHA256:" + strings.Repeat("B", 43) },
		"no host key":    func(b *host.DatabaseBackupObservation) { b.HostKeyFingerprint = "" },
	} {
		tampered := observed
		backup := *observed.Backup
		mutate(&backup)
		tampered.Backup = &backup
		if verifyDatabaseTransitionSuccess(planned, tampered) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
