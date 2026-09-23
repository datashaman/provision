package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"provision/internal/config"
	"provision/internal/host"
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
	case args[0] == "config" && args[1] == "validate":
		return runConfigValidate(args[2:])
	default:
		return usage()
	}
}

func usage() error {
	return errors.New("usage: provision host inspect (--local | --address HOST --user USER) | provision config validate --file ROOT.yaml")
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
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	return output.Encode(compiled)
}
