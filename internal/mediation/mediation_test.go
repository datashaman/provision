package mediation_test

import (
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"provision/internal/mediation"
	"provision/internal/state"
)

func fixture(t *testing.T) (*mediation.Controller, string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Tests use the real importer and ownership checks. Separate UID isolation is exercised live.
	inbox := filepath.Join(dir, "inbox")
	results := filepath.Join(dir, "results")
	for _, p := range []string{inbox, results} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.ParseUint(current.Uid, 10, 32)
	gid, _ := strconv.ParseUint(current.Gid, 10, 32)
	registry := mediation.Registry{SchemaVersion: mediation.RegistrySchema, Operator: current.Username, Requesters: map[string]mediation.Requester{"agent": {Account: current.Username, UID: uint32(uid), GID: uint32(gid), Inbox: inbox, Results: results, Grants: []mediation.Grant{{Application: "provision-example-http", Environment: "lab", Operations: []string{"stageArtifact"}}}}}}
	data, _ := json.Marshal(registry)
	path := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := state.OpenSQLite(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backend.Close() })
	controller, err := mediation.Open(path, backend.(state.RequestBackend))
	if err != nil {
		t.Fatal(err)
	}
	return controller, inbox, path
}

func TestImporterRejectsUnsafeInputsBeforePlanning(t *testing.T) {
	c, inbox, _ := fixture(t)
	data := []byte(`{"schemaVersion":"provision.dev/operation-request/v1alpha1","id":"one","application":"provision-example-http","environment":"lab","revision":"provision-example-http-v1","component":"web","operation":"stageArtifact"}`)
	for _, tc := range []struct {
		name  string
		setup func(string)
	}{
		{"fifo", func(p string) {
			if err := unix.Mkfifo(p, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(p string) {
			if err := os.Symlink("target", p); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(p string) {
			target := filepath.Join(inbox, "target")
			if err := os.WriteFile(target, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, p); err != nil {
				t.Fatal(err)
			}
		}},
		{"writable", func(p string) {
			if err := os.WriteFile(p, data, 0666); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, 0666); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversize", func(p string) {
			if err := os.WriteFile(p, []byte(strings.Repeat("x", 65537)), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(inbox, tc.name)
			tc.setup(p)
			if _, err := c.Import(context.Background(), "agent", tc.name, time.Now()); err == nil {
				t.Fatal("unsafe submission accepted")
			}
		})
	}
	if _, err := c.Import(context.Background(), "agent", "../registry.json", time.Now()); err == nil {
		t.Fatal("traversal accepted")
	}
}

func registerExample(t *testing.T, c *mediation.Controller) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root.yaml", "application.yaml", "environment.yaml", "revision.yaml"} {
		data, err := os.ReadFile(filepath.Join("../../examples/host-http", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Register(filepath.Join(dir, "root.yaml")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImporterCapturesImmutableInputsAndDeduplicates(t *testing.T) {
	c, inbox, _ := fixture(t)
	dir := registerExample(t, c)
	data := []byte(`{
 "schemaVersion":"provision.dev/operation-request/v1alpha1", "id":"capture-one",
 "application":"provision-example-http", "environment":"lab", "revision":"provision-example-http-v1",
 "component":"web", "operation":"stageArtifact"}`)
	path := filepath.Join(inbox, "untrusted-filename.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := c.Import(context.Background(), "agent", filepath.Base(path), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.Capture.Denial != "" || first.Capture.Configuration.Revision.Artifacts["web"].Digest != "sha256:bac304a885179a21fc889fef25cf09f12d8af19e30aa49c1c05e719d10f031b5" {
		t.Fatalf("capture=%+v", first)
	}
	second, err := c.Import(context.Background(), "agent", filepath.Base(path), time.Now().Add(time.Minute))
	if err != nil || !second.Capture.CapturedAt.Equal(first.Capture.CapturedAt) {
		t.Fatalf("duplicate capture changed: %+v %v", second, err)
	}
	var semantic mediation.Request
	if err := json.Unmarshal(data, &semantic); err != nil {
		t.Fatal(err)
	}
	reformatted, _ := json.Marshal(semantic)
	if err := os.WriteFile(path, reformatted, 0600); err != nil {
		t.Fatal(err)
	}
	third, err := c.Import(context.Background(), "agent", filepath.Base(path), time.Now())
	if err != nil || third.Capture.ID != first.Capture.ID || string(third.Capture.Submission) != string(data) {
		t.Fatalf("equivalent duplicate changed capture: %+v %v", third, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "revision.yaml"), []byte("changed original configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), `"stageArtifact"`, `"installGeneration"`)), 0600); err != nil {
		t.Fatal(err)
	}
	stored, err := c.Backend.LoadRequest(context.Background(), first.Capture.ID)
	if err != nil || stored.Capture.Configuration.Digest != first.Capture.Configuration.Digest || string(stored.Capture.Submission) != string(data) {
		t.Fatalf("capture changed: %+v %v", stored, err)
	}
}

func TestImporterDeniesScopeAndMalformedAuthorityFields(t *testing.T) {
	for _, change := range []string{"scope", "actor", "duplicate", "path", "kind"} {
		t.Run(change, func(t *testing.T) {
			c, inbox, _ := fixture(t)
			registerExample(t, c)
			data := `{"schemaVersion":"provision.dev/operation-request/v1alpha1","id":"deny-one","application":"provision-example-http","environment":"lab","revision":"provision-example-http-v1","component":"web","operation":"stageArtifact"}`
			switch change {
			case "scope":
				data = strings.ReplaceAll(data, `"lab"`, `"other"`)
			case "actor":
				data = strings.TrimSuffix(data, "}") + `,"actor":"operator"}`
			case "duplicate":
				data = strings.TrimSuffix(data, "}") + `,"operation":"installGeneration"}`
			case "path":
				data = strings.ReplaceAll(data, `"provision-example-http-v1"`, `"../../secret"`)
			case "kind":
				data = strings.ReplaceAll(data, `"stageArtifact"`, `"installGeneration"`)
			}
			if err := os.WriteFile(filepath.Join(inbox, "claim.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := c.Import(context.Background(), "agent", "claim.json", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Capture.Denial == "" || snapshot.PlanID != "" {
				t.Fatalf("request not denied before planning: %+v", snapshot)
			}
			if _, err := c.Plan(context.Background(), snapshot.Capture.ID, time.Now()); err == nil {
				t.Fatal("denied request reached planning")
			}
			receipt, err := c.Receipt(context.Background(), snapshot.Capture.ID, time.Now())
			if err != nil || receipt.Requester != "agent" || receipt.Status == "captured" {
				t.Fatalf("attribution=%+v %v", receipt, err)
			}
		})
	}
}

func TestImporterRejectsOwnershipAndUntrustedRegistration(t *testing.T) {
	c, inbox, registryPath := fixture(t)
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("OS has no nobody account")
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registry mediation.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	account := registry.Requesters["agent"]
	account.Account = nobody.Username
	uid, _ := strconv.ParseUint(nobody.Uid, 10, 32)
	gid, _ := strconv.ParseUint(nobody.Gid, 10, 32)
	account.UID = uint32(uid)
	account.GID = uint32(gid)
	registry.Requesters["agent"] = account
	data, _ = json.Marshal(registry)
	if err := os.WriteFile(registryPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "foreign.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Import(context.Background(), "agent", "foreign.json", time.Now()); err == nil {
		t.Fatal("foreign ownership accepted")
	}
	if err := os.Chmod(registryPath, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Import(context.Background(), "agent", "foreign.json", time.Now()); err == nil {
		t.Fatal("writable registry accepted")
	}
}

func TestRegistryRejectsSharedRequesterResults(t *testing.T) {
	c, _, path := fixture(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var registry mediation.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	registry.Requesters["other-agent"] = registry.Requesters["agent"]
	data, _ = json.Marshal(registry)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := mediation.Open(path, c.Backend); err == nil {
		t.Fatal("two requesters share an account and published results")
	}
}

func TestRegisteredRevisionCannotSilentlyChangeInputs(t *testing.T) {
	c, inbox, registryPath := fixture(t)
	directory := registerExample(t, c)
	revisionPath := filepath.Join(directory, "revision.yaml")
	original, err := os.ReadFile(revisionPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(string(original), "v0.1.0/", "v0.2.0/")
	if changed == string(original) {
		t.Fatal("test did not change Artifact source")
	}
	if err := os.WriteFile(revisionPath, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(filepath.Join(directory, "root.yaml")); err == nil {
		t.Fatal("same Revision name accepted changed input identity")
	}
	data := []byte(`{"schemaVersion":"provision.dev/operation-request/v1alpha1","id":"revoked","application":"provision-example-http","environment":"lab","revision":"provision-example-http-v1","component":"web","operation":"stageArtifact"}`)
	if err := os.WriteFile(filepath.Join(inbox, "request.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.Import(context.Background(), "agent", "request.json", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registry mediation.Registry
	if err := json.Unmarshal(contents, &registry); err != nil {
		t.Fatal(err)
	}
	account := registry.Requesters["agent"]
	account.Grants = nil
	registry.Requesters["agent"] = account
	contents, _ = json.Marshal(registry)
	if err := os.WriteFile(registryPath, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Plan(context.Background(), snapshot.Capture.ID, time.Now()); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("revoked grant reached live planning: %v", err)
	}
	receipt, err := c.Receipt(context.Background(), snapshot.Capture.ID, time.Now())
	if err != nil || receipt.Eligible || receipt.ApprovalStatus != "scope-denied" {
		t.Fatalf("revoked receipt=%+v %v", receipt, err)
	}
}
