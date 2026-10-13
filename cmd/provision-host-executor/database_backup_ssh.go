package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"provision/internal/authority"
	"provision/internal/host"
	"provision/internal/planner"
)

// Off-host ssh:// Database backup transport (ADR 0078). ponytail: the dump is buffered in memory; stream pg_dump to ssh if Databases outgrow RAM. It shells out to the system ssh with a
// temp known_hosts holding only the key whose fingerprint matches the Plan's pin.

type offHostError struct {
	category  string
	message   string
	uncertain bool
}

func (e *offHostError) Error() string { return e.category + ": " + e.message }
func (e *offHostError) Is(target error) bool {
	return e.uncertain && target == errUncertainRecovery
}

func offHostFailure(category, message string) *offHostError {
	return &offHostError{category: "backup-destination-" + category, message: message}
}

type offHostSession struct {
	user, host, port, dir string // dir is the remote backup directory for this logical Database
	temp, key, knownHosts string
	fingerprint           string
}

func (s *offHostSession) close() { _ = os.RemoveAll(s.temp) }

func (s *offHostSession) hostIdentity() string {
	if s.port == "" {
		return s.host
	}
	return s.host + ":" + s.port
}

func (s *offHostSession) remote(backupID, suffix string) string {
	return path.Join(s.dir, backupID+suffix)
}

// openOffHostSession pins the destination host key before any credential is presented to it.
func openOffHostSession(ctx context.Context, input planner.DatabaseOperationInput, privateKey string) (*offHostSession, error) {
	parsed, err := url.Parse(input.Backup.Destination)
	if err != nil || databaseBackupDestinationClass(input.Backup.Destination) != "off-host-ssh" {
		return nil, offHostFailure("invalid", "Database backup destination is not a valid ssh URL")
	}
	if !strings.HasPrefix(privateKey, "-----BEGIN ") || !strings.Contains(privateKey, "PRIVATE KEY-----") {
		return nil, offHostFailure("credential", "resolved backup credential is not a private key")
	}
	session := &offHostSession{user: input.Backup.SSHUser, host: parsed.Hostname(), port: parsed.Port(), dir: path.Join(parsed.Path, input.LogicalID)}
	if session.temp, err = os.MkdirTemp("", "provision-backup-ssh-"); err != nil {
		return nil, offHostFailure("local", "create backup transport scratch directory")
	}
	fail := func(e error) (*offHostSession, error) { session.close(); return nil, e }
	session.key = path.Join(session.temp, "id")
	session.knownHosts = path.Join(session.temp, "known_hosts")
	if err := os.WriteFile(session.key, []byte(privateKey+"\n"), 0600); err != nil {
		return fail(offHostFailure("local", "write backup transport key"))
	}
	scanArgs := []string{"-T", "10"}
	if session.port != "" {
		scanArgs = append(scanArgs, "-p", session.port)
	}
	scanned, err := exec.CommandContext(ctx, "ssh-keyscan", append(scanArgs, "--", session.host)...).Output()
	if err != nil || len(bytes.TrimSpace(scanned)) == 0 {
		return fail(offHostFailure("unreachable", "backup destination host key could not be retrieved"))
	}
	for _, line := range strings.Split(string(scanned), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		candidate := path.Join(session.temp, "candidate")
		if os.WriteFile(candidate, []byte(line+"\n"), 0600) != nil {
			continue
		}
		fields := strings.Fields(command("ssh-keygen", "-lf", candidate))
		if len(fields) >= 2 && fields[1] == input.Backup.HostKey {
			session.fingerprint = fields[1]
			if err := os.WriteFile(session.knownHosts, []byte(line+"\n"), 0600); err != nil {
				return fail(offHostFailure("local", "write backup transport known_hosts"))
			}
			return session, nil
		}
	}
	return fail(offHostFailure("host-key-mismatch", "backup destination presented no host key matching the pinned fingerprint"))
}

// run executes a POSIX shell script on the destination. Transport failures are classified without echoing ssh output.
func (s *offHostSession) run(ctx context.Context, stdin io.Reader, stdout io.Writer, script string) error {
	args := []string{"-F", "/dev/null", "-o", "ForwardAgent=no", "-i", s.key, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + s.knownHosts,
		"-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=15"}
	if s.port != "" {
		args = append(args, "-p", s.port)
	}
	cmd := exec.CommandContext(ctx, "ssh", append(args, s.user+"@"+s.host, script)...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	switch {
	case !errors.As(err, &exit) || exit.ExitCode() != 255:
		return offHostFailure("command-failed", "remote backup command failed")
	case strings.Contains(stderr.String(), "Permission denied"):
		return offHostFailure("auth-failed", "backup destination rejected the backup credential")
	case strings.Contains(stderr.String(), "Host key verification failed"):
		return offHostFailure("host-key-mismatch", "backup destination host key changed during the session")
	default:
		return offHostFailure("unreachable", "backup destination connection failed")
	}
}

func (s *offHostSession) output(ctx context.Context, stdin io.Reader, script string) (string, error) {
	var out bytes.Buffer
	err := s.run(ctx, stdin, &out, script)
	return strings.TrimSpace(out.String()), err
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

// transportLoss marks an error raised after the dump may have reached the destination as uncertain.
func transportLoss(err error) error {
	var failure *offHostError
	if errors.As(err, &failure) && (failure.category == "backup-destination-unreachable" || failure.category == "backup-destination-command-failed") {
		failure.uncertain = true
	}
	return err
}

func offHostEvidence(input planner.DatabaseOperationInput, claim authority.Claim, source host.DatabaseGenerationStatus, backup host.DatabaseBackupObservation) ([]byte, error) {
	return json.Marshal(map[string]any{
		"schemaVersion": databaseBackupSchema, "planId": claim.PlanID, "operationId": claim.OperationID, "operationDigest": claim.OperationDigest,
		"logicalId": input.LogicalID, "sourceGeneration": source.ID, "backup": backup,
	})
}

// applyOffHostDatabaseBackup observes before acting (ADR 0047): recorded evidence, then a finished upload, then upload.
func applyOffHostDatabaseBackup(ctx context.Context, input planner.DatabaseOperationInput, claim authority.Claim, source host.DatabaseGenerationStatus, privateKey string, exportDump func(context.Context) ([]byte, error)) (host.DatabaseBackupObservation, error) {
	session, err := openOffHostSession(ctx, input, privateKey)
	if err != nil {
		return host.DatabaseBackupObservation{}, err
	}
	defer session.close()
	backupID := stableFilePart(claim.PlanID) + "-" + claim.OperationID
	dump, evidence, partial := session.remote(backupID, ".dump"), session.remote(backupID, ".json"), session.remote(backupID, ".dump.partial")
	backup := host.DatabaseBackupObservation{
		SchemaVersion: databaseBackupSchema, BackupID: backupID, SourceGeneration: source.ID,
		DestinationClass: "off-host-ssh", DestinationReference: input.Backup.Destination, Path: dump,
		Format: "pg_dump-custom", OutsideGeneration: true, DestinationHost: session.hostIdentity(), HostKeyFingerprint: session.fingerprint,
	}

	existing, err := session.output(ctx, nil, "if [ -f "+shellQuote(evidence)+" ]; then cat "+shellQuote(evidence)+"; fi")
	if err != nil {
		return backup, err
	}
	if recorded, ok := parseDatabaseBackupEvidence([]byte(existing), input, claim.PlanID, claim.OperationID, claim.OperationDigest); ok {
		if recorded.DestinationHost != backup.DestinationHost || recorded.HostKeyFingerprint != backup.HostKeyFingerprint {
			return backup, offHostFailure("host-key-mismatch", "recorded backup evidence names a different destination host identity")
		}
		digest, err := remoteDigest(ctx, session, dump)
		if err != nil {
			return backup, err
		}
		if digest != recorded.ArtifactDigest {
			return backup, offHostFailure("digest-mismatch", "recorded backup artifact no longer matches its digest")
		}
		return recorded, nil
	}

	// A final name without evidence means the rename landed and the response (or the evidence write) was lost.
	// ponytail: recovery point is the dump's mtime, which trails the true backup start by the upload duration.
	if finished, err := session.output(ctx, nil, "if [ -f "+shellQuote(dump)+" ]; then sha256sum "+shellQuote(dump)+" | cut -d' ' -f1; stat -c %Y "+shellQuote(dump)+"; fi"); err != nil {
		return backup, err
	} else if lines := strings.Fields(finished); len(lines) == 2 {
		seconds, _ := strconv.ParseInt(lines[1], 10, 64)
		backup.ArtifactDigest = "sha256:" + lines[0]
		backup.AchievedRecoveryPoint = time.Unix(seconds, 0).UTC().Format(time.RFC3339Nano)
		return backup, writeOffHostEvidence(ctx, session, input, claim, source, backup, evidence)
	}

	started := time.Now().UTC()
	data, err := exportDump(ctx)
	if err != nil {
		return backup, err
	}
	sum := sha256.Sum256(data)
	local := hex.EncodeToString(sum[:])
	uploaded, err := session.output(ctx, bytes.NewReader(data), "umask 077 && mkdir -p "+shellQuote(session.dir)+" && rm -f "+shellQuote(partial)+" && cat > "+shellQuote(partial)+" && sha256sum "+shellQuote(partial)+" | cut -d' ' -f1")
	if err != nil || uploaded != local {
		_ = session.run(ctx, nil, io.Discard, "rm -f "+shellQuote(partial))
		if err == nil {
			err = offHostFailure("digest-mismatch", "uploaded backup digest differs from the local dump")
		}
		return backup, err
	}
	if err := session.run(ctx, nil, io.Discard, "mv "+shellQuote(partial)+" "+shellQuote(dump)); err != nil {
		return backup, transportLoss(err)
	}
	backup.ArtifactDigest = "sha256:" + local
	backup.AchievedRecoveryPoint = started.Format(time.RFC3339Nano)
	return backup, writeOffHostEvidence(ctx, session, input, claim, source, backup, evidence)
}

func remoteDigest(ctx context.Context, session *offHostSession, file string) (string, error) {
	out, err := session.output(ctx, nil, "sha256sum "+shellQuote(file)+" | cut -d' ' -f1")
	if err != nil {
		return "", err
	}
	return "sha256:" + out, nil
}

// writeOffHostEvidence writes <id>.json beside the dump, renames it into place, and reads it back.
func writeOffHostEvidence(ctx context.Context, session *offHostSession, input planner.DatabaseOperationInput, claim authority.Claim, source host.DatabaseGenerationStatus, backup host.DatabaseBackupObservation, evidence string) error {
	data, err := offHostEvidence(input, claim, source, backup)
	if err != nil {
		return err
	}
	if err := session.run(ctx, bytes.NewReader(data), io.Discard, "cat > "+shellQuote(evidence+".partial")+" && mv "+shellQuote(evidence+".partial")+" "+shellQuote(evidence)); err != nil {
		return transportLoss(err)
	}
	back, err := session.output(ctx, nil, "cat "+shellQuote(evidence))
	if err != nil {
		return transportLoss(err)
	}
	if recorded, ok := parseDatabaseBackupEvidence([]byte(back), input, claim.PlanID, claim.OperationID, claim.OperationDigest); !ok || recorded != backup {
		return &offHostError{category: "backup-destination-evidence-unverified", message: "backup evidence read back from the destination does not match what was written", uncertain: true}
	}
	return nil
}

type fetchedOffHostBackup struct {
	path, digest, host, fingerprint, backupID string
	cleanup                                   func()
}

// fetchOffHostBackup copies the dump to a scratch path owned by the Environment account and refuses on any digest mismatch.
func fetchOffHostBackup(ctx context.Context, input planner.DatabaseOperationInput, claim authority.Claim, backupOperationID, privateKey, scratchParent string, uid, gid int) (*fetchedOffHostBackup, error) {
	session, err := openOffHostSession(ctx, input, privateKey)
	if err != nil {
		return nil, err
	}
	defer session.close()
	backupID := stableFilePart(claim.PlanID) + "-" + backupOperationID
	evidence, err := session.output(ctx, nil, "if [ -f "+shellQuote(session.remote(backupID, ".json"))+" ]; then cat "+shellQuote(session.remote(backupID, ".json"))+"; fi")
	if err != nil {
		return nil, err
	}
	var record struct {
		PlanID      string                         `json:"planId"`
		OperationID string                         `json:"operationId"`
		LogicalID   string                         `json:"logicalId"`
		Backup      host.DatabaseBackupObservation `json:"backup"`
	}
	if json.Unmarshal([]byte(evidence), &record) != nil || record.PlanID != claim.PlanID || record.OperationID != backupOperationID || record.LogicalID != input.LogicalID ||
		record.Backup.BackupID != backupID || record.Backup.HostKeyFingerprint != session.fingerprint || record.Backup.DestinationHost != session.hostIdentity() ||
		!strings.HasPrefix(record.Backup.ArtifactDigest, "sha256:") {
		return nil, errors.New("Database restore verification requires backup evidence")
	}
	scratch, err := os.MkdirTemp(scratchParent, "fetch-")
	if err != nil || os.Chown(scratch, uid, gid) != nil || os.Chmod(scratch, 0750) != nil {
		return nil, offHostFailure("local", "create restore fetch scratch directory")
	}
	fetched := &fetchedOffHostBackup{path: path.Join(scratch, backupID+".dump"), digest: record.Backup.ArtifactDigest, host: session.hostIdentity(), fingerprint: session.fingerprint, backupID: backupID, cleanup: func() { _ = os.RemoveAll(scratch) }}
	file, err := os.OpenFile(fetched.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if err != nil || file.Chown(uid, gid) != nil {
		if file != nil {
			_ = file.Close()
		}
		fetched.cleanup()
		return nil, offHostFailure("local", "create restore fetch file")
	}
	hash := sha256.New()
	err = session.run(ctx, nil, io.MultiWriter(file, hash), "cat "+shellQuote(session.remote(backupID, ".dump")))
	_ = file.Close()
	if err != nil {
		fetched.cleanup()
		return nil, err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != fetched.digest {
		fetched.cleanup()
		return nil, offHostFailure("digest-mismatch", "fetched backup does not match its recorded digest; restore refused")
	}
	return fetched, nil
}
