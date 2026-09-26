package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"provision/internal/approval"
	"provision/internal/authority"
	"provision/internal/config"
	"provision/internal/execution"
	"provision/internal/host"
	"provision/internal/planner"
	"provision/internal/state"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "provision:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return usage()
	}
	switch {
	case args[0] == "host" && args[1] == "inspect":
		return runHostInspect(args[2:])
	case args[0] == "host" && args[1] == "bootstrap" && len(args) >= 3 && args[2] == "check":
		return runHostBootstrapCheck(args[3:])
	case args[0] == "config" && args[1] == "validate":
		return runConfigValidate(args[2:])
	case args[0] == "plan" && args[1] == "preview":
		return runPlanPreview(args[2:])
	case args[0] == "plan" && args[1] == "approve":
		return runPlanApprove(args[2:])
	case args[0] == "plan" && args[1] == "status":
		return runPlanStatus(args[2:])
	case args[0] == "authority" && args[1] == "keygen":
		return runAuthorityKeygen(args[2:])
	case args[0] == "deployment" && args[1] == "execute":
		return runDeploymentExecute(args[2:])
	case args[0] == "deployment" && args[1] == "resume":
		return runDeploymentResume(args[2:])
	case args[0] == "deployment" && args[1] == "status":
		return runDeploymentStatus(args[2:])
	default:
		return usage()
	}
}

func usage() error {
	return errors.New("usage: provision host inspect ... | provision host bootstrap check ... | provision config validate ... | provision plan ... | provision authority keygen ... | provision deployment execute ... | provision deployment resume ... | provision deployment status ...")
}

func runAuthorityKeygen(args []string) error {
	flags := flag.NewFlagSet("authority keygen", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	privatePath := flags.String("private-key", "", "new private signing key path")
	publicPath := flags.String("public-key", "", "new public verification key path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *privatePath == "" || *publicPath == "" {
		return errors.New("usage: provision authority keygen --private-key PATH --public-key PATH")
	}
	info, err := authority.GenerateKeyPair(*privatePath, *publicPath)
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(info)
}

func runDeploymentExecute(args []string) error {
	flags := flag.NewFlagSet("deployment execute", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	planID := flags.String("plan", "", "approved current Plan identity")
	operationID := flags.String("operation", "", "exact operation identity from the Plan")
	statePath := flags.String("state", "", "SQLite State Backend path")
	signingKey := flags.String("signing-key", "", "local authority private key path")
	leaseDuration := flags.Duration("lease-duration", 2*time.Minute, "fenced execution lease lifetime")
	var secretFiles repeatedFlag
	flags.Var(&secretFiles, "secret-file", "resolve a Secret Reference from REFERENCE=PATH for this execution")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *planID == "" || *operationID == "" || *statePath == "" || *signingKey == "" || *leaseDuration <= 0 {
		return errors.New("usage: provision deployment execute --plan DIGEST --operation ID --state PATH --signing-key PATH [--secret-file REFERENCE=PATH] [--lease-duration 2m]")
	}
	resolvedSecrets, err := loadSecretFiles(secretFiles)
	if err != nil {
		return err
	}
	signer, err := authority.LoadSigner(*signingKey)
	if err != nil {
		return err
	}
	backend, err := state.OpenExistingSQLiteForUpdate(*statePath)
	if err != nil {
		return err
	}
	defer backend.Close()
	holder, err := execution.NewLocalHolder()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	snapshot, err := backend.LoadPlanSnapshot(ctx, *planID)
	if err != nil {
		return err
	}
	handler, err := execution.NewHostHandler(snapshot.Plan.Target, snapshot.Plan.Environment)
	if err != nil {
		return err
	}
	engine := execution.Engine{Backend: backend, Signer: signer, Handler: handler, ResolvedSecrets: resolvedSecrets, Now: time.Now}
	result, err := engine.Execute(ctx, execution.Request{
		PlanID: *planID, OperationID: *operationID, Holder: holder, LeaseDuration: *leaseDuration,
	})
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	if result.SchemaVersion != "" {
		if encodeErr := output.Encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

func runDeploymentResume(args []string) error {
	flags := flag.NewFlagSet("deployment resume", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	planID := flags.String("plan", "", "approved current Plan identity")
	statePath := flags.String("state", "", "SQLite State Backend path")
	signingKey := flags.String("signing-key", "", "local authority private key path")
	leaseDuration := flags.Duration("lease-duration", 2*time.Minute, "fenced execution lease lifetime")
	var secretFiles repeatedFlag
	flags.Var(&secretFiles, "secret-file", "resolve a Secret Reference from REFERENCE=PATH for this execution")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *planID == "" || *statePath == "" || *signingKey == "" || *leaseDuration <= 0 {
		return errors.New("usage: provision deployment resume --plan DIGEST --state PATH --signing-key PATH [--secret-file REFERENCE=PATH] [--lease-duration 2m]")
	}
	resolvedSecrets, err := loadSecretFiles(secretFiles)
	if err != nil {
		return err
	}
	signer, err := authority.LoadSigner(*signingKey)
	if err != nil {
		return err
	}
	backend, err := state.OpenExistingSQLiteForUpdate(*statePath)
	if err != nil {
		return err
	}
	defer backend.Close()
	holder, err := execution.NewLocalHolder()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	snapshot, err := backend.LoadPlanSnapshot(ctx, *planID)
	if err != nil {
		return err
	}
	handler, err := execution.NewHostHandler(snapshot.Plan.Target, snapshot.Plan.Environment)
	if err != nil {
		return err
	}
	engine := execution.Engine{Backend: backend, Signer: signer, Handler: handler, ResolvedSecrets: resolvedSecrets, Now: time.Now}
	result, err := engine.Resume(ctx, execution.ResumeRequest{
		PlanID: *planID, Holder: holder, LeaseDuration: *leaseDuration,
	})
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	if result.SchemaVersion != "" {
		if encodeErr := output.Encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

func runDeploymentStatus(args []string) error {
	flags := flag.NewFlagSet("deployment status", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	planID := flags.String("plan", "", "Plan identity")
	statePath := flags.String("state", "", "SQLite State Backend path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *planID == "" || *statePath == "" {
		return errors.New("usage: provision deployment status --plan DIGEST --state PATH")
	}
	backend, err := state.OpenExistingSQLiteForUpdate(*statePath)
	if err != nil {
		return err
	}
	defer backend.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, err := backend.LoadJournal(ctx, *planID)
	if err != nil {
		return err
	}
	rejected, err := backend.LoadRejectedResults(ctx, *planID)
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(struct {
		SchemaVersion   string                 `json:"schemaVersion"`
		PlanID          string                 `json:"planId"`
		Events          []state.JournalEvent   `json:"events"`
		RejectedResults []state.RejectedResult `json:"rejectedResults"`
	}{"provision.dev/deployment-status/v1alpha2", *planID, events, rejected})
}

func runPlanPreview(args []string) error {
	flags := flag.NewFlagSet("plan preview", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	path := flags.String("file", "", "root configuration document")
	statePath := flags.String("state", "", "SQLite State Backend path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("usage: provision plan preview --file ROOT.yaml [--state PATH]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	preview, err := buildCurrentPlan(ctx, *path)
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	if !preview.Executable {
		if err := output.Encode(preview); err != nil {
			return err
		}
		return fmt.Errorf("Plan is not executable: %s", strings.Join(preview.Reasons, "; "))
	}
	if *statePath != "" {
		backend, err := state.OpenSQLite(*statePath)
		if err != nil {
			return err
		}
		defer backend.Close()
		if err := backend.StoreCurrentPlan(ctx, *preview.Plan, time.Now().UTC()); err != nil {
			return err
		}
	}
	return output.Encode(preview.Plan)
}

func runPlanApprove(args []string) error {
	flags := flag.NewFlagSet("plan approve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	path := flags.String("file", "", "root configuration document")
	planID := flags.String("plan", "", "exact Plan identity to approve")
	actor := flags.String("actor", "", "external actor identity")
	statePath := flags.String("state", "", "SQLite State Backend path")
	expiresAfter := flags.Duration("expires-after", 15*time.Minute, "approval lifetime")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || *planID == "" || *actor == "" || *statePath == "" || *expiresAfter <= 0 {
		return errors.New("usage: provision plan approve --file ROOT.yaml --plan DIGEST --actor ACTOR --state PATH [--expires-after 15m]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	preview, err := buildCurrentPlan(ctx, *path)
	if err != nil {
		return err
	}
	if !preview.Executable || preview.Plan == nil {
		return fmt.Errorf("current Plan is not executable: %s", strings.Join(preview.Reasons, "; "))
	}
	backend, err := state.OpenExistingSQLiteForUpdate(*statePath)
	if err != nil {
		return err
	}
	defer backend.Close()
	stored, err := backend.LoadPlanSnapshot(ctx, *planID)
	if err != nil {
		return err
	}
	requestedFingerprint, err := planner.ApprovalFingerprint(stored.Plan)
	if err != nil {
		return err
	}
	currentFingerprint, err := planner.ApprovalFingerprint(*preview.Plan)
	if err != nil {
		return err
	}
	if requestedFingerprint != currentFingerprint {
		return fmt.Errorf("Plan is stale: requested %s, current %s", *planID, preview.Plan.ID)
	}
	authority, err := approval.AuthenticateLocalActor(*actor, approval.Scope{
		Application: stored.Plan.Application,
		Environment: stored.Plan.Environment,
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	status, err := approval.Approve(ctx, backend, *planID, authority, now, now.Add(*expiresAfter))
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(status)
}

func runPlanStatus(args []string) error {
	flags := flag.NewFlagSet("plan status", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	planID := flags.String("plan", "", "Plan identity")
	statePath := flags.String("state", "", "SQLite State Backend path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *planID == "" || *statePath == "" {
		return errors.New("usage: provision plan status --plan DIGEST --state PATH")
	}
	backend, err := state.OpenExistingSQLiteForUpdate(*statePath)
	if err != nil {
		return err
	}
	defer backend.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := approval.Inspect(ctx, backend, *planID, time.Now().UTC())
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(status)
}

func buildCurrentPlan(ctx context.Context, path string) (planner.Preview, error) {
	compiled, err := config.Load(path)
	if err != nil {
		return planner.Preview{}, err
	}
	selection, err := compiled.PlanningTarget()
	if err != nil {
		return planner.Preview{}, err
	}
	target := host.Target{Local: selection.Target.Local, Address: selection.Target.Address, User: selection.Target.User}
	if target.Local {
		target.User = ""
	}
	observation, err := host.CheckBootstrap(ctx, target, compiled.Environment.Name, selection.Target.User)
	if err != nil {
		return planner.Preview{}, err
	}
	return planner.Build(compiled, observation)
}

func runHostBootstrapCheck(args []string) error {
	flags := flag.NewFlagSet("host bootstrap check", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	local := flags.Bool("local", false, "check the current machine")
	address := flags.String("address", "", "existing remote host")
	user := flags.String("user", "", "SSH user for the remote host")
	environment := flags.String("environment", "", "Environment identity")
	operator := flags.String("operator", "", "user allowed to call the restricted executor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	status, err := host.CheckBootstrap(ctx, host.Target{Local: *local, Address: *address, User: *user}, *environment, *operator)
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	if err := output.Encode(status); err != nil {
		return err
	}
	if !status.Ready {
		return errors.New("host bootstrap is not ready; inspect findings above")
	}
	return nil
}

func runHostInspect(args []string) error {
	flags := flag.NewFlagSet("host inspect", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	local := flags.Bool("local", false, "inspect the current machine")
	address := flags.String("address", "", "existing remote host")
	user := flags.String("user", "", "SSH user for the remote host")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	observation, err := host.Inspect(ctx, host.Target{Local: *local, Address: *address, User: *user})
	if err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(observation)
}

func runConfigValidate(args []string) error {
	flags := flag.NewFlagSet("config validate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	path := flags.String("file", "", "root configuration document")
	var artifactFiles repeatedFlag
	flags.Var(&artifactFiles, "artifact-file", "verify local Artifact bytes; use COMPONENT=PATH when the Revision has multiple Artifacts")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("usage: provision config validate --file ROOT.yaml")
	}
	compiled, err := config.Load(*path)
	if err != nil {
		return err
	}
	verifiedArtifacts := []string{}
	if len(artifactFiles) == 1 && !strings.Contains(artifactFiles[0], "=") {
		if err := compiled.VerifyArtifactFile(artifactFiles[0]); err != nil {
			return err
		}
		for name := range compiled.Revision.Artifacts {
			verifiedArtifacts = append(verifiedArtifacts, name)
		}
	} else if len(artifactFiles) != 0 {
		paths := make(map[string]string, len(artifactFiles))
		for _, value := range artifactFiles {
			name, file, ok := strings.Cut(value, "=")
			if !ok || !configName(name) || file == "" {
				return errors.New("--artifact-file must use COMPONENT=PATH for a multi-Artifact Revision")
			}
			if _, duplicate := paths[name]; duplicate {
				return fmt.Errorf("duplicate --artifact-file for component %q", name)
			}
			paths[name] = file
		}
		if err := compiled.VerifyArtifactFiles(paths); err != nil {
			return err
		}
		for name := range paths {
			verifiedArtifacts = append(verifiedArtifacts, name)
		}
	}
	sort.Strings(verifiedArtifacts)
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(struct {
		config.Compiled
		ArtifactVerified  bool     `json:"artifactVerified"`
		VerifiedArtifacts []string `json:"verifiedArtifacts"`
	}{compiled, len(artifactFiles) != 0, verifiedArtifacts})
}

type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func loadSecretFiles(values []string) (map[string]string, error) {
	resolved := make(map[string]string, len(values))
	for _, value := range values {
		reference, path, ok := strings.Cut(value, "=")
		if !ok || !strings.HasPrefix(reference, "secret://") || path == "" {
			return nil, errors.New("--secret-file must use SECRET_REFERENCE=PATH")
		}
		if _, duplicate := resolved[reference]; duplicate {
			return nil, fmt.Errorf("duplicate --secret-file for %s", reference)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > 8192 {
			return nil, fmt.Errorf("Secret Reference %s requires a private regular file", reference)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read resolved value for %s", reference)
		}
		secret := strings.TrimSpace(string(data))
		if secret == "" {
			return nil, fmt.Errorf("resolved value for %s is empty", reference)
		}
		resolved[reference] = secret
	}
	return resolved, nil
}

func configName(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value {
		letter := character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		if !letter && !digit && character != '-' {
			return false
		}
	}
	return true
}
