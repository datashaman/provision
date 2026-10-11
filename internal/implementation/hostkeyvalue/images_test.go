package hostkeyvalue

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestImageRegistry(t *testing.T) {
	def, ok := ImageFor("")
	if !ok || def.Generation() != "valkey-9-1-2-ac9c858b86dc" || def.Major() != "9" {
		t.Fatalf("default image = %+v, %v", def, ok)
	}
	second, ok := ImageFor("9.1.1")
	if !ok || second.Generation() != "valkey-9-1-1-cc03bc07761a" || second.Major() != def.Major() {
		t.Fatalf("second image = %+v, %v", second, ok)
	}
	if _, ok := ImageFor("9.99.0"); ok {
		t.Fatal("unregistered version resolved")
	}
}

func TestImageByReferenceRejectsTagsAndUnknownDigests(t *testing.T) {
	def, _ := ImageFor("")
	if got, ok := ImageByReference(def.Reference()); !ok || got.Version != "9.1.2" {
		t.Fatalf("registered reference = %+v, %v", got, ok)
	}
	for _, ref := range []string{"docker.io/valkey/valkey:9.1.2", "docker.io/valkey/valkey@sha256:" + strings.Repeat("a", 64)} {
		if _, ok := ImageByReference(ref); ok {
			t.Fatalf("%s resolved", ref)
		}
	}
}

func TestEvidenceDigestsMatchCommittedQualification(t *testing.T) {
	for _, image := range Images {
		path := "../../../docs/evidence/2026-10-10-valkey-" + strings.ReplaceAll(image.Version, ".", "-") + "-packaging-qualification.json"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := "sha256:" + hex.EncodeToString(sum[:]); got != image.Evidence {
			t.Fatalf("%s evidence digest = %s, registry has %s", image.Version, got, image.Evidence)
		}
	}
}
