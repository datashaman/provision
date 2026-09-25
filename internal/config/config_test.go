package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func examplePath() string {
	return filepath.Join("..", "..", "examples", "host-http", "root.yaml")
}

func TestLoadExampleDeterministically(t *testing.T) {
	first, err := Load(examplePath())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(examplePath())
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || !strings.HasPrefix(first.Digest, "sha256:") {
		t.Fatalf("non-deterministic digest: %q / %q", first.Digest, second.Digest)
	}
	if first.Application.Name != "provision-example-http" || first.Environment.Name != "lab" || first.Revision.Name != "provision-example-http-v1" {
		t.Fatalf("unexpected compiled configuration: %+v", first)
	}
	implementation := first.Environment.Implementations["web"]
	if implementation.Endpoint.Drain.Mode != "bounded-http" || implementation.Endpoint.Drain.MaxDuration != "2s" {
		t.Fatalf("unexpected compiled drain contract: %+v", implementation.Endpoint.Drain)
	}
}

func TestEquivalentJSONRootHasSameDigest(t *testing.T) {
	dir := copyExample(t)
	jsonRoot := filepath.Join(dir, "root.json")
	data := []byte(`{"schemaVersion":"provision.dev/v1alpha1","application":"application.yaml","environment":"environment.yaml","revision":"revision.yaml"}`)
	if err := os.WriteFile(jsonRoot, data, 0600); err != nil {
		t.Fatal(err)
	}
	fromYAML, err := Load(filepath.Join(dir, "root.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := Load(jsonRoot)
	if err != nil {
		t.Fatal(err)
	}
	if fromJSON.Digest != fromYAML.Digest {
		t.Fatalf("JSON digest %s != YAML digest %s", fromJSON.Digest, fromYAML.Digest)
	}
}

func TestRejectsUnsafeOrInvalidDocuments(t *testing.T) {
	tests := []struct {
		name string
		file string
		old  string
		new  string
		want string
	}{
		{"unknown field", "application.yaml", "kind: Application", "kind: Application\nunknown: true", "field unknown not found"},
		{"unknown nested field", "application.yaml", "role: http", "role: http\n    command: arbitrary", "field command not found"},
		{"duplicate key", "application.yaml", "name: provision-example-http", "name: provision-example-http\nname: second", "duplicate YAML key"},
		{"alias", "application.yaml", "name: provision-example-http", "name: &app provision-example-http", "aliases and anchors"},
		{"multiple documents", "revision.yaml", "kind: Revision", "kind: Revision\n---\nkind: Revision", "multiple YAML documents"},
		{"wrong application", "environment.yaml", "application: provision-example-http", "application: other", "same application"},
		{"unsupported component", "application.yaml", "role: http", "role: worker", "must be an HTTP service"},
		{"unsafe health path", "application.yaml", "path: /live", "path: /live check", "liveness health path"},
		{"unsafe host", "environment.yaml", "address: base.local", "address: -oProxyCommand=evil", "existing remote host"},
		{"missing drain mode", "environment.yaml", "mode: bounded-http", "mode: ''", "bounded-http drain mode"},
		{"unsupported drain mode", "environment.yaml", "mode: bounded-http", "mode: websocket", "bounded-http drain mode"},
		{"missing drain duration", "environment.yaml", "maxDuration: 2s", "maxDuration: ''", "valid canonical drain maxDuration"},
		{"invalid drain duration", "environment.yaml", "maxDuration: 2s", "maxDuration: eventually", "valid canonical drain maxDuration"},
		{"noncanonical drain duration", "environment.yaml", "maxDuration: 2s", "maxDuration: 2000ms", "valid canonical drain maxDuration"},
		{"too short drain duration", "environment.yaml", "maxDuration: 2s", "maxDuration: 500ms", "supported drain maxDuration"},
		{"too long drain duration", "environment.yaml", "maxDuration: 2s", "maxDuration: 10m0s", "supported drain maxDuration"},
		{"artifact credential", "revision.yaml", "https://github.com", "https://user:secret@github.com", "immutable artifact source"},
		{"artifact query", "revision.yaml", "provision-example-http-linux-amd64.tar.gz", "provision-example-http-linux-amd64.tar.gz?token=secret", "immutable artifact source"},
		{"bad digest", "revision.yaml", "sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5", "sha256:bad", "sha256 digest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := copyExample(t)
			path := filepath.Join(dir, test.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(data), test.old, test.new, 1)
			if changed == string(data) {
				t.Fatalf("test substitution did not match %q", test.old)
			}
			if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = Load(filepath.Join(dir, "root.yaml"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestRejectsReferenceTraversalAndSymlinkEscape(t *testing.T) {
	dir := copyExample(t)
	root := filepath.Join(dir, "root.yaml")
	data, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(data), "application: application.yaml", "application: ../outside.yaml", 1)
	if err := os.WriteFile(root, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "local relative") {
		t.Fatalf("traversal error = %v", err)
	}
	if err := os.WriteFile(root, data, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("kind: Application"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "application.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "application.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("symlink error = %v", err)
	}
}

func copyExample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"root.yaml", "application.yaml", "environment.yaml", "revision.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "examples", "host-http", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
