package host

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestSSHHostKeyFingerprintMatchesOpenSSHFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh_host_ed25519_key.pub")
	keyBlob := make([]byte, 0, 51)
	keyBlob = binary.BigEndian.AppendUint32(keyBlob, 11)
	keyBlob = append(keyBlob, []byte("ssh-ed25519")...)
	keyBlob = binary.BigEndian.AppendUint32(keyBlob, 32)
	keyBlob = append(keyBlob, bytes.Repeat([]byte{7}, 32)...)
	encoded := base64.StdEncoding.EncodeToString(keyBlob)
	if err := os.WriteFile(path, []byte("ssh-ed25519 "+encoded+" host\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ReadSSHHostKeyFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != "SHA256:gNSIRW+2Iyiuvsdp/bgjy38bvWHw6wQm3tuoXrl3WjQ" {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSSHHostKeyFingerprint(path); err == nil {
		t.Fatal("unsafe SSH host public-key permissions accepted")
	}
}
