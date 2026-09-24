package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxArtifactBytes int64 = 512 << 20
const ArtifactCacheRoot = "/var/lib/provision/artifacts/sha256"

type ArtifactStatus string

const (
	ArtifactAbsent         ArtifactStatus = "absent"
	ArtifactUnknown        ArtifactStatus = "unknown"
	ArtifactInvalid        ArtifactStatus = "invalid"
	ArtifactStaged         ArtifactStatus = "staged"
	ArtifactAlreadyPresent ArtifactStatus = "already-present"
	ArtifactFailed         ArtifactStatus = "failed"
)

type ArtifactObservation struct {
	Status ArtifactStatus `json:"status"`
	Path   string         `json:"path"`
	Digest string         `json:"digest"`
	Size   int64          `json:"size"`
	Reason string         `json:"reason,omitempty"`
}

func ArtifactCachePath(cacheRoot, digest string) (string, error) {
	encoded := strings.TrimPrefix(digest, "sha256:")
	decoded, err := hex.DecodeString(encoded)
	if !strings.HasPrefix(digest, "sha256:") || len(decoded) != sha256.Size || err != nil || strings.ToLower(digest) != digest {
		return "", errors.New("planned Artifact digest is invalid")
	}
	return filepath.Join(cacheRoot, encoded), nil
}

func ObserveArtifactCache(cacheRoot, expected string) (ArtifactObservation, error) {
	path, err := ArtifactCachePath(cacheRoot, expected)
	if err != nil {
		return ArtifactObservation{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ArtifactObservation{Status: ArtifactAbsent, Path: path, Digest: expected}, nil
	}
	if err != nil {
		return ArtifactObservation{Status: ArtifactUnknown, Path: path, Digest: expected, Reason: "Artifact cache entry cannot be inspected"}, nil
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0644 || info.Size() > MaxArtifactBytes {
		return ArtifactObservation{Status: ArtifactInvalid, Path: path, Digest: expected, Size: info.Size(), Reason: "Artifact cache entry is not a safe bounded regular file"}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return ArtifactObservation{Status: ArtifactUnknown, Path: path, Digest: expected, Size: info.Size(), Reason: "Artifact cache entry cannot be read"}, nil
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, MaxArtifactBytes+1)); err != nil {
		return ArtifactObservation{Status: ArtifactUnknown, Path: path, Digest: expected, Size: info.Size(), Reason: "Artifact cache entry cannot be hashed"}, nil
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return ArtifactObservation{Status: ArtifactInvalid, Path: path, Digest: actual, Size: info.Size(), Reason: "Artifact cache entry digest does not match the Plan"}, nil
	}
	return ArtifactObservation{Status: ArtifactAlreadyPresent, Path: path, Digest: actual, Size: info.Size()}, nil
}
