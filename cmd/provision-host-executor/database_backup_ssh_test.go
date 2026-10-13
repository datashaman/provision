package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"provision/internal/authority"
	"provision/internal/config"
	"provision/internal/host"
	"provision/internal/planner"
)

// offHostFixture fakes ssh and ssh-keyscan on PATH. The fake ssh runs the remote script locally, so the
// "destination" is a directory; PROVISION_TEST_FAULT injects unreachable, auth, partial and response-loss faults.
type offHostFixture struct {
	input      planner.DatabaseOperationInput
	claim      authority.Claim
	source     host.DatabaseGenerationStatus
	key        string
	destRoot   string
	fault      string
	faultFile  string
	dumpCalled int
}

func newOffHostFixture(t *testing.T) *offHostFixture {
	t.Helper()
	bin, work := t.TempDir(), t.TempDir()
	keyPath := filepath.Join(work, "host")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v %s", err, out)
	}
	clientKey := filepath.Join(work, "client")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", clientKey).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(keyPath + ".pub")
	private, _ := os.ReadFile(clientKey)
	fields := strings.Fields(string(pub))
	fingerprint := strings.Fields(command("ssh-keygen", "-lf", keyPath+".pub"))[1]
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write("ssh-keyscan", `echo "localhost `+fields[0]+" "+fields[1]+`"`)
	write("ssh", `
script=""; for arg; do script="$arg"; done
fault="$(cat "$PROVISION_TEST_FAULT_FILE" 2>/dev/null || true)"
case "$fault" in
  unreachable) echo "ssh: connect to host localhost port 22: Connection refused" >&2; exit 255;;
  auth) echo "backup@localhost: Permission denied (publickey)." >&2; exit 255;;
  partial) case "$script" in *"cat >"*.dump.partial*) head -c 40 | sh -c "$script" >/dev/null 2>&1; exit 255;; esac;;
  lose-response) case "$script" in "mv "*.dump.partial*) sh -c "$script"; exit 255;; esac;;
esac
exec sh -c "$script"
`)
	if _, err := exec.LookPath("sha256sum"); err != nil {
		write("sha256sum", `exec shasum -a 256 "$@"`)
	}
	if exec.Command("stat", "-c", "%Y", "/").Run() != nil {
		write("stat", `[ "$1" = -c ] && exec /usr/bin/stat -f %m "$3"; exec /usr/bin/stat "$@"`)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	faultFile := filepath.Join(work, "fault")
	t.Setenv("PROVISION_TEST_FAULT_FILE", faultFile)
	dest := filepath.Join(work, "dest")
	input := planner.DatabaseOperationInput{
		LogicalID: "provision-lab-data", GenerationID: "g1",
		Backup: config.DatabaseBackupPolicy{Mode: "required", Destination: "ssh://localhost" + dest, SSHUser: "backup", HostKey: fingerprint, CredentialReference: "secret://lab/backup-ssh-key"},
	}
	return &offHostFixture{
		input: input, claim: authority.Claim{PlanID: "sha256:" + strings.Repeat("a", 64), OperationID: "op-02", OperationDigest: "sha256:" + strings.Repeat("b", 64)},
		source: host.DatabaseGenerationStatus{ID: "g1", LogicalID: input.LogicalID}, key: strings.TrimSpace(string(private)), destRoot: dest, faultFile: faultFile,
	}
}

func (f *offHostFixture) setFault(t *testing.T, fault string) {
	t.Helper()
	if err := os.WriteFile(f.faultFile, []byte(fault), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *offHostFixture) backup(t *testing.T, dump string) (host.DatabaseBackupObservation, error) {
	t.Helper()
	return applyOffHostDatabaseBackup(context.Background(), f.input, f.claim, f.source, f.key, func(context.Context) ([]byte, error) {
		f.dumpCalled++
		return []byte(dump), nil
	})
}

func (f *offHostFixture) remoteDir() string { return filepath.Join(f.destRoot, f.input.LogicalID) }

func digestOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func categoryOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a categorized failure")
	}
	category, _, _ := strings.Cut(err.Error(), ": ")
	return category
}

func TestOffHostBackupUploadsPinsHostAndRecordsEvidence(t *testing.T) {
	f := newOffHostFixture(t)
	dump := strings.Repeat("row\n", 200)
	backup, err := f.backup(t, dump)
	if err != nil {
		t.Fatal(err)
	}
	if backup.ArtifactDigest != digestOf(dump) || backup.HostKeyFingerprint != f.input.Backup.HostKey || backup.DestinationHost != "localhost" || backup.DestinationClass != "off-host-ssh" {
		t.Fatalf("evidence lacks digest or host identity: %+v", backup)
	}
	entries, _ := os.ReadDir(f.remoteDir())
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	id := stableFilePart(f.claim.PlanID) + "-op-02"
	if strings.Join(names, ",") != id+".dump,"+id+".json" {
		t.Fatalf("destination holds %v; want the renamed dump and evidence only", names)
	}
	evidence, _ := os.ReadFile(filepath.Join(f.remoteDir(), id+".json"))
	for _, secret := range []string{f.key[:30], "backup@", f.input.Backup.CredentialReference} {
		if strings.Contains(string(evidence), secret) {
			t.Fatalf("evidence leaks %q", secret)
		}
	}
}

func TestOffHostBackupFailuresAreCategorizedAndNeverFallBack(t *testing.T) {
	for _, test := range []struct{ name, fault, category string }{
		{"unreachable", "unreachable", "backup-destination-unreachable"},
		{"auth failure", "auth", "backup-destination-auth-failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOffHostFixture(t)
			f.setFault(t, test.fault)
			_, err := f.backup(t, "dump")
			if got := categoryOf(t, err); got != test.category || errors.Is(err, errUncertainRecovery) {
				t.Fatalf("category = %q uncertain=%v, want failed %q", got, errors.Is(err, errUncertainRecovery), test.category)
			}
			if _, statErr := os.Stat(f.remoteDir()); statErr == nil {
				t.Fatal("a failed upload left destination state")
			}
		})
	}
	t.Run("host key mismatch", func(t *testing.T) {
		f := newOffHostFixture(t)
		f.input.Backup.HostKey = "SHA256:" + strings.Repeat("A", 43)
		_, err := f.backup(t, "dump")
		if categoryOf(t, err) != "backup-destination-host-key-mismatch" || f.dumpCalled != 0 {
			t.Fatalf("err=%v dumpCalled=%d; a mismatched pin must fail before any dump leaves the host", err, f.dumpCalled)
		}
	})
}

func TestOffHostBackupPartialUploadIsFailedAndRetryRemovesPartial(t *testing.T) {
	f := newOffHostFixture(t)
	f.setFault(t, "partial")
	_, err := f.backup(t, strings.Repeat("row\n", 200))
	if categoryOf(t, err) != "backup-destination-unreachable" || errors.Is(err, errUncertainRecovery) {
		t.Fatalf("partial upload err = %v, want failed", err)
	}
	f.setFault(t, "")
	dump := strings.Repeat("row\n", 200)
	backup, err := f.backup(t, dump)
	if err != nil || backup.ArtifactDigest != digestOf(dump) {
		t.Fatalf("retry = %+v, %v", backup, err)
	}
	if _, err := os.Stat(filepath.Join(f.remoteDir(), stableFilePart(f.claim.PlanID)+"-op-02.dump.partial")); err == nil {
		t.Fatal("retry left a .partial behind")
	}
}

func TestOffHostBackupResponseLossAfterUploadIsUncertainAndRecoveryObservesFirst(t *testing.T) {
	f := newOffHostFixture(t)
	f.setFault(t, "lose-response")
	dump := strings.Repeat("row\n", 200)
	_, err := f.backup(t, dump)
	if !errors.Is(err, errUncertainRecovery) {
		t.Fatalf("response loss err = %v, want uncertain", err)
	}
	f.setFault(t, "")
	f.dumpCalled = 0
	backup, err := f.backup(t, "a different dump that must not be uploaded")
	if err != nil || f.dumpCalled != 0 || backup.ArtifactDigest != digestOf(dump) {
		t.Fatalf("recovery = %+v, %v, dumpCalled=%d; want evidence recorded for the landed upload without re-uploading", backup, err, f.dumpCalled)
	}
	again, err := f.backup(t, "ignored")
	if err != nil || again != backup || f.dumpCalled != 0 {
		t.Fatalf("recorded evidence not reused: %+v, %v", again, err)
	}
}

func TestOffHostRestoreFetchChecksDigestAndRefusesTampering(t *testing.T) {
	f := newOffHostFixture(t)
	dump := strings.Repeat("row\n", 200)
	if _, err := f.backup(t, dump); err != nil {
		t.Fatal(err)
	}
	current, _ := user.Current()
	uid, _ := strconv.Atoi(current.Uid)
	gid, _ := strconv.Atoi(current.Gid)
	fetched, err := fetchOffHostBackup(context.Background(), f.input, f.claim, "op-02", f.key, t.TempDir(), uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(fetched.path); string(body) != dump || fetched.digest != digestOf(dump) || fetched.host != "localhost" || fetched.fingerprint != f.input.Backup.HostKey {
		t.Fatalf("fetched %+v", fetched)
	}
	fetched.cleanup()
	if err := os.WriteFile(filepath.Join(f.remoteDir(), stableFilePart(f.claim.PlanID)+"-op-02.dump"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchOffHostBackup(context.Background(), f.input, f.claim, "op-02", f.key, t.TempDir(), uid, gid); categoryOf(t, err) != "backup-destination-digest-mismatch" {
		t.Fatalf("tampered dump err = %v, want digest mismatch refusal", err)
	}
}
