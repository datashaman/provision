package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"provision/internal/mediation"
	"provision/internal/state"
)

func TestRequestCommandsAuthenticateImporterAndRejectSelfApprovalAndGenericBypass(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "provision")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.ParseUint(current.Uid, 10, 32)
	gid, _ := strconv.ParseUint(current.Gid, 10, 32)
	private := filepath.Join(dir, "private")
	inbox := filepath.Join(dir, "inbox")
	results := filepath.Join(dir, "results")
	configuration := filepath.Join(dir, "configuration")
	for _, path := range []string{private, inbox, results, configuration} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := writeRemoteHostConfiguration(t, configuration)
	writeRemoteExecutorSSH(t, dir)
	registryPath := filepath.Join(private, "registry.json")
	statePath := filepath.Join(private, "state.db")
	keyPath := filepath.Join(private, "authority.key")
	registry := mediation.Registry{SchemaVersion: mediation.RegistrySchema, Operator: current.Username, Requesters: map[string]mediation.Requester{"agent": {Account: current.Username, UID: uint32(uid), GID: uint32(gid), Inbox: inbox, Results: results, Grants: []mediation.Grant{{Application: "provision-example-http", Environment: "lab", Operations: []string{"stageArtifact"}}}}}}
	data, _ := json.Marshal(registry)
	if err := os.WriteFile(registryPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_AUTHORITY_KEY_ID=sha256:"+strings.Repeat("c", 64), "FAKE_EXECUTION_LOG="+filepath.Join(dir, "unexpected-execution.json"))
	command := func(wantError bool, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if (err != nil) != wantError {
			t.Fatalf("command %v: %v\n%s", args, err, out)
		}
		return out
	}
	trusted := func(action string, extra ...string) []string {
		return append([]string{"request", action, "--registry", registryPath, "--state", statePath}, extra...)
	}
	var key struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(command(false, "authority", "keygen", "--private-key", keyPath, "--public-key", filepath.Join(private, "authority.pub")), &key); err != nil {
		t.Fatal(err)
	}
	env = append(env, "FAKE_AUTHORITY_KEY_ID="+key.ID)
	command(false, trusted("register", "--file", configPath)...)
	submission := filepath.Join(dir, "one.json")
	if err := os.WriteFile(submission, []byte(`{"schemaVersion":"provision.dev/operation-request/v1alpha1","id":"one","application":"provision-example-http","environment":"lab","revision":"provision-example-http-v1","component":"web","operation":"stageArtifact"}`), 0600); err != nil {
		t.Fatal(err)
	}
	command(false, "request", "submit", "--inbox", inbox, "--file", submission)
	var captured state.RequestSnapshot
	if err := json.Unmarshal(command(false, trusted("import", "--requester", "agent", "--name", "one.json")...), &captured); err != nil {
		t.Fatal(err)
	}
	var planned state.RequestSnapshot
	if err := json.Unmarshal(command(false, trusted("plan", "--request", captured.Capture.ID)...), &planned); err != nil {
		t.Fatal(err)
	}
	review := command(false, trusted("review", "--request", captured.Capture.ID)...)
	if !strings.Contains(string(review), planned.PlanID) || !strings.Contains(string(review), planned.OperationDigest) {
		t.Fatalf("review omits exact binding: %s", review)
	}
	out := command(true, trusted("approve", "--request", captured.Capture.ID, "--plan", planned.PlanID, "--operation", planned.OperationID, "--actor", current.Username)...)
	if !strings.Contains(string(out), "cannot approve") {
		t.Fatalf("self approval: %s", out)
	}
	out = command(true, trusted("approve", "--request", captured.Capture.ID, "--plan", planned.PlanID, "--operation", planned.OperationID, "--actor", "someone-else")...)
	if !strings.Contains(string(out), "authenticated local OS user") {
		t.Fatalf("impersonation: %s", out)
	}
	command(false, "plan", "approve", "--file", configPath, "--plan", planned.PlanID, "--state", statePath, "--actor", current.Username)
	for _, operation := range []string{"op-01", "op-02"} {
		out = command(true, "deployment", "execute", "--plan", planned.PlanID, "--operation", operation, "--state", statePath, "--signing-key", keyPath)
		if !strings.Contains(string(out), "mediated Plan requires its exact request approval") {
			t.Fatalf("Plan-wide approval bypassed request binding: %s", out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "unexpected-execution.json")); !os.IsNotExist(err) {
		t.Fatal("requester commands mutated Host")
	}
}
