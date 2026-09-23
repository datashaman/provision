package host

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRejectsUnsafeRemoteTargetBeforeRunningCommands(t *testing.T) {
	targets := []Target{
		{Address: "-oProxyCommand=evil", User: "operator"},
		{Address: "base.local;evil", User: "operator"},
		{Address: "base.local", User: "bad user"},
		{Local: true, Address: "base.local"},
	}
	for _, target := range targets {
		called := false
		_, err := inspect(context.Background(), target, func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Fatalf("target %+v: error=%v called=%v", target, err, called)
		}
	}
}

func TestRemoteInspectionUsesFixedReadOnlySSHCommands(t *testing.T) {
	commands := []string{}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ssh" {
			t.Fatalf("unexpected executable %q", name)
		}
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		if !strings.Contains(joined, "StrictHostKeyChecking=yes") || !strings.Contains(joined, "BatchMode=yes") {
			t.Fatalf("SSH safety options missing from %q", joined)
		}
		switch {
		case strings.HasSuffix(joined, " uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, " uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, " cat /etc/os-release"):
			return []byte("ID=ubuntu\nVERSION_ID=\"26.04\"\n"), nil
		case strings.HasSuffix(joined, " systemctl --version"):
			return []byte("systemd 259\n+PAM\n"), nil
		case strings.HasSuffix(joined, " test -f /sys/fs/cgroup/cgroup.controllers"):
			return nil, nil
		case strings.HasSuffix(joined, " podman --version"):
			return nil, errors.New("not installed")
		case strings.HasSuffix(joined, " caddy version"):
			return []byte("2.6.2\n"), nil
		default:
			t.Fatalf("unexpected remote command: %q", joined)
			return nil, nil
		}
	}
	obs, err := inspect(context.Background(), Target{Address: "base.local", User: "marlinf"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 7 || obs.OS != "ubuntu" || obs.OSVersion != "26.04" || obs.SystemdVersion != "259" || !obs.CgroupV2 {
		t.Fatalf("unexpected observation: %+v (%d commands)", obs, len(commands))
	}
	if obs.PodmanVersion != "" || obs.CaddyVersion != "2.6.2" || len(obs.Findings) != 1 {
		t.Fatalf("unexpected optional capabilities: %+v", obs)
	}
}

func TestMalformedSystemdOutputIsAFindingNotAPanic(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch strings.Join(append([]string{name}, args...), " ") {
		case "uname -s":
			return []byte("Linux"), nil
		case "uname -m":
			return []byte("aarch64"), nil
		case "cat /etc/os-release":
			return []byte("ID=ubuntu\nVERSION_ID=26.04"), nil
		case "systemctl --version":
			return []byte("broken"), nil
		default:
			return nil, errors.New("missing")
		}
	}
	obs, err := inspect(context.Background(), Target{Local: true}, run)
	if err != nil {
		t.Fatal(err)
	}
	if obs.SystemdVersion != "" || len(obs.Findings) != 4 {
		t.Fatalf("unexpected findings: %+v", obs)
	}
}

func TestNonLinuxLocalMachineIsReportedWithoutLinuxProbes(t *testing.T) {
	commands := 0
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		commands++
		if commands == 1 {
			return []byte("Darwin"), nil
		}
		return []byte("arm64"), nil
	}
	obs, err := inspect(context.Background(), Target{Local: true}, run)
	if err != nil {
		t.Fatal(err)
	}
	if commands != 2 || obs.Kernel != "Darwin" || len(obs.Findings) != 1 {
		t.Fatalf("unexpected observation: %+v (%d commands)", obs, commands)
	}
}
