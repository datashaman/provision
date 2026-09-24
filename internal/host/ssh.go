package host

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

const SSHHostPublicKeyPath = "/etc/ssh/ssh_host_ed25519_key.pub"

type SSHHostKeyFingerprint string

func ParseSSHHostKeyFingerprint(value string) (SSHHostKeyFingerprint, error) {
	if !strings.HasPrefix(value, "SHA256:") {
		return "", errors.New("SSH host-key fingerprint must use SHA256")
	}
	digest, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "SHA256:"))
	if err != nil || len(digest) != sha256.Size {
		return "", errors.New("SSH host-key fingerprint is invalid")
	}
	return SSHHostKeyFingerprint(value), nil
}

func (f SSHHostKeyFingerprint) Valid() bool {
	_, err := ParseSSHHostKeyFingerprint(string(f))
	return err == nil
}

func (f *SSHHostKeyFingerprint) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := ParseSSHHostKeyFingerprint(value)
	if err != nil {
		return err
	}
	*f = parsed
	return nil
}

func StrictSSHArguments(target Target, remoteCommand ...string) []string {
	arguments := []string{
		"-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=5",
		"--", target.User + "@" + target.Address,
	}
	return append(arguments, remoteCommand...)
}

func ReadSSHHostKeyFingerprint(path string) (SSHHostKeyFingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0644 || info.Size() > 4096 {
		return "", errors.New("SSH host public key is not a safe regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("SSH host public key cannot be read")
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", errors.New("SSH host public key is not ED25519")
	}
	encoded, err := base64.StdEncoding.DecodeString(fields[1])
	const keyType = "ssh-ed25519"
	if err != nil || len(encoded) != 4+len(keyType)+4+32 || int(binary.BigEndian.Uint32(encoded[:4])) != len(keyType) || string(encoded[4:4+len(keyType)]) != keyType || binary.BigEndian.Uint32(encoded[4+len(keyType):4+len(keyType)+4]) != 32 {
		return "", errors.New("SSH host public key is invalid")
	}
	digest := sha256.Sum256(encoded)
	return SSHHostKeyFingerprint("SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])), nil
}
