package host

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Target identifies one existing machine. Inspect never creates or changes it.
type Target struct {
	Local   bool   `json:"local"`
	Address string `json:"address,omitempty"`
	User    string `json:"user,omitempty"`
}

type Observation struct {
	Target         Target   `json:"target"`
	Kernel         string   `json:"kernel"`
	Architecture   string   `json:"architecture"`
	OS             string   `json:"os,omitempty"`
	OSVersion      string   `json:"osVersion,omitempty"`
	SystemdVersion string   `json:"systemdVersion,omitempty"`
	CgroupV2       bool     `json:"cgroupV2"`
	PodmanVersion  string   `json:"podmanVersion,omitempty"`
	CaddyVersion   string   `json:"caddyVersion,omitempty"`
	Findings       []string `json:"findings"`
}

type runner func(context.Context, string, ...string) ([]byte, error)

var (
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
)

func (t Target) Validate() error {
	if t.Local {
		if t.Address != "" || t.User != "" {
			return errors.New("local target cannot specify an address or user")
		}
		return nil
	}
	if !hostPattern.MatchString(t.Address) || strings.HasSuffix(t.Address, ".") {
		return errors.New("remote target address is invalid")
	}
	if !userPattern.MatchString(t.User) {
		return errors.New("remote target user is invalid")
	}
	return nil
}

// Inspect performs only fixed read-only commands. SSH requires an already trusted
// host key and never prompts for credentials or modifies known_hosts.
func Inspect(ctx context.Context, target Target) (Observation, error) {
	return inspect(ctx, target, runCommand)
}

func inspect(ctx context.Context, target Target, run runner) (Observation, error) {
	if err := target.Validate(); err != nil {
		return Observation{}, err
	}
	call := func(args ...string) (string, error) {
		name := args[0]
		commandArgs := args[1:]
		if !target.Local {
			commandArgs = StrictSSHArguments(target, append([]string{name}, commandArgs...)...)
			name = "ssh"
		}
		out, err := run(ctx, name, commandArgs...)
		return strings.TrimSpace(string(out)), err
	}

	obs := Observation{Target: target, Findings: []string{}}
	var err error
	if obs.Kernel, err = call("uname", "-s"); err != nil {
		return Observation{}, fmt.Errorf("inspect kernel: %w", err)
	}
	if obs.Architecture, err = call("uname", "-m"); err != nil {
		return Observation{}, fmt.Errorf("inspect architecture: %w", err)
	}
	if obs.Kernel != "Linux" {
		obs.Findings = append(obs.Findings, "Linux host implementation is unavailable")
		return obs, nil
	}
	osRelease, err := call("cat", "/etc/os-release")
	if err != nil {
		return Observation{}, fmt.Errorf("inspect operating system: %w", err)
	}
	fields := parseOSRelease(osRelease)
	obs.OS = fields["ID"]
	obs.OSVersion = fields["VERSION_ID"]
	if obs.OS != "ubuntu" {
		obs.Findings = append(obs.Findings, "Ubuntu host implementation is unavailable")
	}
	systemd, err := call("systemctl", "--version")
	if err == nil {
		fields := strings.Fields(strings.SplitN(systemd, "\n", 2)[0])
		if len(fields) >= 2 && fields[0] == "systemd" {
			obs.SystemdVersion = fields[1]
		}
	}
	if obs.SystemdVersion == "" {
		obs.Findings = append(obs.Findings, "systemd is not identified")
	}
	_, err = call("test", "-f", "/sys/fs/cgroup/cgroup.controllers")
	obs.CgroupV2 = err == nil
	if !obs.CgroupV2 {
		obs.Findings = append(obs.Findings, "cgroup v2 is not observed; the curated OCI path requires it")
	}
	if obs.PodmanVersion, err = call("podman", "--version"); err != nil {
		obs.PodmanVersion = ""
		obs.Findings = append(obs.Findings, "Podman is not observed; the curated OCI path requires it")
	}
	if obs.CaddyVersion, err = call("caddy", "version"); err != nil {
		obs.CaddyVersion = ""
		obs.Findings = append(obs.Findings, "Caddy is not observed for host endpoint routing")
	}
	return obs, nil
}

func parseOSRelease(data string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		fields[key] = value
	}
	return fields
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
