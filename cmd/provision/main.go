package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"provision/internal/approval"
	"provision/internal/config"
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
	default:
		return usage()
	}
}

func usage() error {
	return errors.New("usage: provision host inspect ... | provision host bootstrap check ... | provision config validate ... | provision plan preview --file ROOT.yaml [--state PATH] | provision plan approve --file ROOT.yaml --plan DIGEST --actor ACTOR --state PATH | provision plan status --plan DIGEST --state PATH")
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
	if preview.Plan.ID != *planID {
		return fmt.Errorf("Plan is stale: requested %s, current %s", *planID, preview.Plan.ID)
	}
	authority, err := approval.AuthenticateLocalActor(*actor, approval.Scope{
		Application: preview.Plan.Application,
		Environment: preview.Plan.Environment,
	})
	if err != nil {
		return err
	}
	backend, err := state.OpenExistingSQLiteForUpdate(*statePath)
	if err != nil {
		return err
	}
	defer backend.Close()
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
	backend, err := state.OpenExistingSQLite(*statePath)
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
	selection, err := compiled.HostSelection()
	if err != nil {
		return planner.Preview{}, err
	}
	observation, err := host.CheckBootstrap(ctx, host.Target{Address: selection.Target.Address, User: selection.Target.User}, compiled.Environment.Name, selection.Target.User)
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
	artifactFile := flags.String("artifact-file", "", "verify local artifact bytes against the Revision digest")
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
	if *artifactFile != "" {
		if err := compiled.VerifyArtifactFile(*artifactFile); err != nil {
			return err
		}
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(struct {
		config.Compiled
		ArtifactVerified bool `json:"artifactVerified"`
	}{compiled, *artifactFile != ""})
}
