package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/planner"
)

func applyLocalDatabaseBackup(ctx context.Context, input planner.DatabaseOperationInput, claim authority.Claim, source host.DatabaseGenerationStatus, dir string, uid, gid int, exportDump func(context.Context, io.Writer) error) (host.DatabaseBackupObservation, error) {
	id := stableFilePart(claim.PlanID) + "-" + claim.OperationID
	dump, partial := filepath.Join(dir, id+".dump"), filepath.Join(dir, id+".dump.partial")
	backup := host.DatabaseBackupObservation{
		SchemaVersion: databaseBackupSchema, BackupID: id, SourceGeneration: source.ID,
		DestinationClass: "host-filesystem", DestinationReference: input.Backup.Destination, Path: dump,
		AchievedRecoveryPoint: time.Now().UTC().Format(time.RFC3339Nano), Format: "pg_dump-custom", OutsideGeneration: true,
	}
	if recorded, ok := readDatabaseBackupEvidence(input, claim.PlanID, claim.OperationID, claim.OperationDigest); ok {
		if recorded.ArtifactDigest != "" {
			digest, err := databaseBackupFileDigest(dump)
			if err != nil || digest != recorded.ArtifactDigest {
				return backup, errors.New("recorded Database backup artifact no longer matches its digest")
			}
		}
		return recorded, nil
	}
	if info, err := os.Lstat(dump); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0440 || !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
			return backup, errors.New("existing Database backup file is unsafe")
		}
		// A final name without evidence is a committed dump; adopt it without exporting again.
		backup.ArtifactDigest, err = databaseBackupFileDigest(dump)
		if err != nil {
			return backup, err
		}
		backup.AchievedRecoveryPoint = info.ModTime().UTC().Format(time.RFC3339Nano)
	} else if !errors.Is(err, os.ErrNotExist) {
		return backup, err
	} else {
		if err := os.Remove(partial); err != nil && !errors.Is(err, os.ErrNotExist) {
			return backup, err
		}
		file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return backup, err
		}
		defer os.Remove(partial)
		defer file.Close()
		hash := sha256.New()
		if err := exportDump(ctx, io.MultiWriter(file, hash)); err != nil {
			return backup, err
		}
		if err := file.Sync(); err != nil {
			return backup, err
		}
		if err := file.Chown(uid, gid); err != nil {
			return backup, err
		}
		if err := file.Chmod(0440); err != nil {
			return backup, err
		}
		if err := file.Close(); err != nil {
			return backup, err
		}
		backup.ArtifactDigest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if digest, err := databaseBackupFileDigest(partial); err != nil || digest != backup.ArtifactDigest {
			return backup, errors.New("written Database backup digest differs from the exported dump")
		}
		if err := os.Rename(partial, dump); err != nil {
			return backup, err
		}
	}
	evidence, err := encodeDatabaseBackupEvidence(input, claim, source, backup)
	if err != nil {
		return backup, err
	}
	if err := writeJSONAtomic(filepath.Join(dir, id+".json"), json.RawMessage(evidence), 0440); err != nil {
		return backup, errors.New("record PostgreSQL backup evidence")
	}
	if recorded, ok := readDatabaseBackupEvidence(input, claim.PlanID, claim.OperationID, claim.OperationDigest); !ok || recorded != backup {
		return backup, errors.New("Database backup evidence read back does not match what was written")
	}
	return backup, nil
}

func databaseBackupFileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("Database backup artifact is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
