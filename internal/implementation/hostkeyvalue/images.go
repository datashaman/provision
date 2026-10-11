// Package hostkeyvalue holds the managed host-local Valkey packaging identity.
// Planning and execution build on it in later slices.
package hostkeyvalue

import "strings"

// Image is one digest-pinned Valkey product identity. The first entry is the
// default an implementation gets when it declares no version.
type Image struct {
	Version  string
	Index    string
	Manifest string
	// Evidence is the packaging qualification digest. Empty means the image
	// is registered but not yet qualified.
	Evidence string
}

var Images = []Image{
	// Evidence is the digest of docs/evidence/2026-10-10-valkey-9-1-2-packaging-qualification.json.
	{Version: "9.1.2", Index: "sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd", Manifest: "sha256:ac9c858b86dc81f3482d53a9d854547379a5f41e012500f839ab257c399063e0", Evidence: "sha256:76f79d213d3cc181a6868a7879fa3133f2bdd3affa01542a54beb43c2fff3769"},
	// Evidence is the digest of docs/evidence/2026-10-10-valkey-9-1-1-packaging-qualification.json.
	{Version: "9.1.1", Index: "sha256:64e361b630ecf18dff7ca4df6a88e6eafc193687eb48cff2c7e0a293ab67d29a", Manifest: "sha256:cc03bc07761acc552c960bfbcc66196dcf02e949dadb92d941d970ed6c782fea", Evidence: "sha256:01112f9103a64f8b4a2b2e30835552895f27d29e9841111d2b0808ef660ba9bd"},
}

func (i Image) Reference() string { return "docker.io/valkey/valkey@" + i.Manifest }

func (i Image) Generation() string {
	return "valkey-" + strings.ReplaceAll(i.Version, ".", "-") + "-" + strings.TrimPrefix(i.Manifest, "sha256:")[:12]
}

// Major is the Valkey major version; transitions are qualified within one major.
func (i Image) Major() string { return strings.SplitN(i.Version, ".", 2)[0] }

// ImageFor resolves a declared version; the empty version selects the default image.
func ImageFor(version string) (Image, bool) {
	for _, image := range Images {
		if version == "" || image.Version == version {
			return image, true
		}
	}
	return Image{}, false
}

// ImageByReference resolves a fully qualified digest reference to a registered,
// qualified image. Tag-only references never resolve.
func ImageByReference(reference string) (Image, bool) {
	for _, image := range Images {
		if image.Evidence != "" && reference == image.Reference() {
			return image, true
		}
	}
	return Image{}, false
}
