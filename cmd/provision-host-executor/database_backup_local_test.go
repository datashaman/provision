package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"provision/internal/authority"
	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planner"
)

func localBackupFixture(t *testing.T) (planner.DatabaseOperationInput, authority.Claim, host.DatabaseGenerationStatus, string) {
	t.Helper()
	root := t.TempDir()
	input := planner.DatabaseOperationInput{
		LogicalID: "provision-lab-data", GenerationID: "g1", DataPath: filepath.Join(root, "active", "data"),
		Backup: config.DatabaseBackupPolicy{Destination: "file://" + root},
	}
	claim := authority.Claim{PlanID: "plan", OperationID: "op-02", OperationDigest: "digest"}
	source, _ := databaseBackupSource(input)
	dir := filepath.Join(root, input.LogicalID)
	if err := os.Mkdir(dir, 0750); err != nil {
		t.Fatal(err)
	}
	return input, claim, source, dir
}

func TestLocalBackupStreamsBeyondMemoryBudgetAndReusesEvidence(t *testing.T) {
	input, claim, source, dir := localBackupFixture(t)
	chunk := make([]byte, 64<<10)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	backup, err := applyLocalDatabaseBackup(context.Background(), input, claim, source, dir, os.Getuid(), os.Getgid(), func(_ context.Context, destination io.Writer) error {
		for range 512 {
			if _, err := destination.Write(chunk); err != nil {
				return err
			}
		}
		return nil
	})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("32 MiB backup allocated %d bytes; budget is 8 MiB", allocated)
	}
	if backup.ArtifactDigest != "sha256:83ee47245398adee79bd9c0a8bc57b821e92aba10f5f9ade8a5d1fae4d8c4302" {
		t.Fatalf("unexpected digest: %s", backup.ArtifactDigest)
	}
	again, err := applyLocalDatabaseBackup(context.Background(), input, claim, source, dir, os.Getuid(), os.Getgid(), func(context.Context, io.Writer) error {
		t.Fatal("recorded backup must not be exported again")
		return nil
	})
	if err != nil || again != backup {
		t.Fatalf("recorded evidence not reused: %+v, %v", again, err)
	}
	if _, err := os.Stat(filepath.Join(dir, backup.BackupID+".dump.partial")); !os.IsNotExist(err) {
		t.Fatalf("partial still exists: %v", err)
	}
}

func TestLocalBackupFailedExportNeverCommitsAndRetryReplacesPartial(t *testing.T) {
	input, claim, source, dir := localBackupFixture(t)
	partial := filepath.Join(dir, "plan-op-02.dump.partial")
	if err := os.WriteFile(partial, []byte("interrupted attempt"), 0600); err != nil {
		t.Fatal(err)
	}
	exportErr := errors.New("pg_dump failed")
	_, err := applyLocalDatabaseBackup(context.Background(), input, claim, source, dir, os.Getuid(), os.Getgid(), func(_ context.Context, destination io.Writer) error {
		_, _ = io.WriteString(destination, "incomplete dump")
		return exportErr
	})
	if !errors.Is(err, exportErr) {
		t.Fatalf("export failure = %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("failed export left destination state: %v, %v", entries, err)
	}
	backup, err := applyLocalDatabaseBackup(context.Background(), input, claim, source, dir, os.Getuid(), os.Getgid(), func(_ context.Context, destination io.Writer) error {
		_, err := io.WriteString(destination, "complete dump")
		return err
	})
	if err != nil || backup.ArtifactDigest != digestOf("complete dump") {
		t.Fatalf("retry = %+v, %v", backup, err)
	}
}

func TestLocalBackupRecoversAfterEvidenceWriteFailureAndRefusesTampering(t *testing.T) {
	input, claim, source, dir := localBackupFixture(t)
	evidence := filepath.Join(dir, "plan-op-02.json")
	if err := os.Mkdir(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := applyLocalDatabaseBackup(context.Background(), input, claim, source, dir, os.Getuid(), os.Getgid(), func(_ context.Context, destination io.Writer) error {
		_, err := io.WriteString(destination, "committed dump")
		return err
	})
	if err == nil {
		t.Fatal("expected evidence write failure")
	}
	if err := os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	noExport := func(context.Context, io.Writer) error {
		t.Fatal("committed backup must not be exported again")
		return nil
	}
	backup, err := applyLocalDatabaseBackup(context.Background(), input, claim, source, dir, os.Getuid(), os.Getgid(), noExport)
	if err != nil || backup.ArtifactDigest != digestOf("committed dump") {
		t.Fatalf("recovery = %+v, %v", backup, err)
	}
	if err := os.Chmod(backup.Path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup.Path, []byte("tampered"), 0440); err != nil {
		t.Fatal(err)
	}
	if _, err := applyLocalDatabaseBackup(context.Background(), input, claim, source, dir, os.Getuid(), os.Getgid(), noExport); err == nil {
		t.Fatal("tampered dump accepted")
	}
}
