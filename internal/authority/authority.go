package authority

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SchemaVersion = "provision.dev/host-authorization/v1alpha1"

type TargetIdentity struct {
	Name           string `json:"name"`
	Local          bool   `json:"local"`
	Address        string `json:"address,omitempty"`
	Operator       string `json:"operator"`
	ExecutorDigest string `json:"executorDigest"`
}

type Claim struct {
	SchemaVersion   string         `json:"schemaVersion"`
	KeyID           string         `json:"keyId"`
	PlanID          string         `json:"planId"`
	Application     string         `json:"application"`
	Environment     string         `json:"environment"`
	Target          TargetIdentity `json:"target"`
	OperationID     string         `json:"operationId"`
	OperationKind   string         `json:"operationKind"`
	OperationDigest string         `json:"operationDigest"`
	AttemptID       string         `json:"attemptId"`
	FencingToken    int64          `json:"fencingToken"`
	IssuedAt        time.Time      `json:"issuedAt"`
	ExpiresAt       time.Time      `json:"expiresAt"`
}

type Proof struct {
	Claim     Claim  `json:"claim"`
	Signature string `json:"signature"`
}

type KeyInfo struct {
	ID         string `json:"id"`
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

type Signer struct {
	id  string
	key ed25519.PrivateKey
}

func GenerateKeyPair(privatePath, publicPath string) (KeyInfo, error) {
	if privatePath == "" || publicPath == "" || privatePath == publicPath {
		return KeyInfo{}, errors.New("distinct private and public key paths are required")
	}
	for _, path := range []string{privatePath, publicPath} {
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil || !parent.IsDir() {
			return KeyInfo{}, fmt.Errorf("authority key parent directory does not exist: %s", filepath.Dir(path))
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return KeyInfo{}, fmt.Errorf("authority key path already exists: %s", path)
			}
			return KeyInfo{}, fmt.Errorf("inspect authority key path: %w", err)
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("generate authority key: %w", err)
	}
	if err := writeExclusive(privatePath, []byte(hex.EncodeToString(privateKey)+"\n"), 0600); err != nil {
		return KeyInfo{}, err
	}
	if err := writeExclusive(publicPath, []byte(hex.EncodeToString(publicKey)+"\n"), 0644); err != nil {
		_ = os.Remove(privatePath)
		return KeyInfo{}, err
	}
	return KeyInfo{ID: keyID(publicKey), PrivateKey: privatePath, PublicKey: publicPath}, nil
}

func LoadSigner(path string) (Signer, error) {
	data, err := readKey(path, 0600)
	if err != nil {
		return Signer{}, err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return Signer{}, errors.New("authority private key is invalid")
	}
	key := ed25519.PrivateKey(decoded)
	publicKey := key.Public().(ed25519.PublicKey)
	return Signer{id: keyID(publicKey), key: key}, nil
}

func LoadVerifier(path string) (ed25519.PublicKey, string, error) {
	data, err := readKey(path, 0644)
	if err != nil {
		return nil, "", err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, "", errors.New("authority public key is invalid")
	}
	key := ed25519.PublicKey(decoded)
	return key, keyID(key), nil
}

func (s Signer) ID() string { return s.id }

func (s Signer) Sign(claim Claim) (Proof, error) {
	if len(s.key) != ed25519.PrivateKeySize || s.id == "" {
		return Proof{}, errors.New("authority signer is not initialized")
	}
	claim.SchemaVersion = SchemaVersion
	claim.KeyID = s.id
	claim.IssuedAt = claim.IssuedAt.UTC()
	claim.ExpiresAt = claim.ExpiresAt.UTC()
	encoded, err := json.Marshal(claim)
	if err != nil {
		return Proof{}, fmt.Errorf("encode authorization claim: %w", err)
	}
	signature := ed25519.Sign(s.key, encoded)
	return Proof{Claim: claim, Signature: base64.RawURLEncoding.EncodeToString(signature)}, nil
}

func Verify(proof Proof, publicKey ed25519.PublicKey, now time.Time) error {
	if proof.Claim.SchemaVersion != SchemaVersion || proof.Claim.KeyID != keyID(publicKey) {
		return errors.New("authorization key or schema does not match")
	}
	now = now.UTC()
	if proof.Claim.IssuedAt.After(now.Add(5*time.Second)) || !proof.Claim.ExpiresAt.After(now) || !proof.Claim.ExpiresAt.After(proof.Claim.IssuedAt) || proof.Claim.ExpiresAt.Sub(proof.Claim.IssuedAt) > 5*time.Minute {
		return errors.New("authorization is expired or has an invalid lifetime")
	}
	signature, err := base64.RawURLEncoding.DecodeString(proof.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("authorization signature is invalid")
	}
	encoded, err := json.Marshal(proof.Claim)
	if err != nil || !ed25519.Verify(publicKey, encoded, signature) {
		return errors.New("authorization signature is invalid")
	}
	return nil
}

func keyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256([]byte(hex.EncodeToString(publicKey)))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func readKey(path string, mode os.FileMode) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read authority key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode || info.Size() > 1024 {
		return nil, errors.New("authority key must be a safe regular file with expected permissions")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authority key: %w", err)
	}
	return data, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create authority key: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write authority key: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close authority key: %w", err)
	}
	return nil
}
