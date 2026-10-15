package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"time"

	"provision/internal/mediation"
	"provision/internal/state"
)

func runRequest(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: provision request submit|register|import|plan|review|approve|deny|execute|resume|status|observe ...")
	}
	command := args[0]
	flags := flag.NewFlagSet("request "+command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	registry := flags.String("registry", "", "private trusted registration file")
	statePath := flags.String("state", "", "private SQLite State Backend")
	file := flags.String("file", "", "submission or trusted root configuration path")
	inbox := flags.String("inbox", "", "registered requester inbox (submit only)")
	requester := flags.String("requester", "", "trusted requester registration name (import only)")
	name := flags.String("name", "", "single inbox filename (import only)")
	id := flags.String("request", "", "captured request identity")
	plan := flags.String("plan", "", "reviewed exact Plan digest (decision only)")
	op := flags.String("operation", "", "reviewed exact operation identity (decision only)")
	actor := flags.String("actor", "", "authenticated trusted operator username")
	expires := flags.Duration("expires-after", 15*time.Minute, "mediated approval lifetime")
	key := flags.String("signing-key", "", "private authority signing key (execute/resume only)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected request arguments")
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	if command == "submit" {
		if *inbox == "" || *file == "" {
			return errors.New("request submit requires --inbox and --file")
		}
		name, err := mediation.Submit(*inbox, *file)
		if err != nil {
			return err
		}
		return output.Encode(struct {
			Filename string `json:"filename"`
		}{name})
	}
	if *registry == "" || *statePath == "" {
		return errors.New("trusted request commands require --registry and --state")
	}
	if err := mediation.CheckPrivateState(*registry, *statePath); err != nil {
		return err
	}
	var backend state.Backend
	var err error
	if command == "review" || command == "observe" {
		backend, err = state.OpenExistingSQLite(*statePath)
	} else {
		backend, err = state.OpenSQLite(*statePath)
	}
	if err != nil {
		return err
	}
	defer backend.Close()
	controller, err := mediation.Open(*registry, backend.(state.RequestBackend))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	var result any
	switch command {
	case "register":
		if *file == "" {
			return errors.New("request register requires --file")
		}
		result, err = controller.Register(*file)
	case "import":
		if *requester == "" || *name == "" {
			return errors.New("request import requires --requester and --name")
		}
		result, err = controller.Import(ctx, *requester, *name, now)
	default:
		if *id == "" {
			return errors.New("trusted request command requires --request")
		}
		switch command {
		case "plan":
			result, err = controller.Plan(ctx, *id, now)
		case "review":
			snapshot, loadErr := backend.(state.RequestBackend).LoadRequest(ctx, *id)
			if loadErr != nil {
				return loadErr
			}
			stored, loadErr := backend.LoadPlanSnapshot(ctx, snapshot.PlanID)
			if loadErr != nil {
				return loadErr
			}
			result = struct {
				Request state.RequestSnapshot `json:"request"`
				Plan    any                   `json:"plan"`
			}{snapshot, stored.Plan}
		case "approve", "deny":
			if *plan == "" || *op == "" || *actor == "" {
				return errors.New("request decision requires --plan, --operation and --actor")
			}
			result, err = controller.Decide(ctx, *id, *plan, *op, *actor, *expires, command == "deny", now)
		case "execute", "resume":
			if *key == "" {
				return errors.New("request execution requires --signing-key")
			}
			result, err = controller.Execute(ctx, *id, *key, command == "resume", now)
		case "status":
			err = controller.Publish(ctx, *id, now)
			if err == nil {
				result, err = controller.Receipt(ctx, *id, now)
			}
		case "observe":
			observed, observeErr := controller.Observe(ctx, *id)
			err = observeErr
			result = struct {
				SchemaVersion string `json:"schemaVersion"`
				RequestID     string `json:"requestId"`
				Observation   any    `json:"observation"`
			}{"provision.dev/request-observation/v1alpha1", *id, observed}
		default:
			return errors.New("unknown request command")
		}
	}
	if receipt, ok := result.(mediation.Receipt); ok && receipt.RequestID == "" {
		result = nil
	}
	if result != nil && (err == nil || command == "execute" || command == "resume") {
		if encodeErr := output.Encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}
