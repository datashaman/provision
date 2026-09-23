// provision-host-executor is a nonresident, root-owned host entrypoint. This
// first increment exposes inspection only; Plan-bound mutations arrive in #6.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"provision/internal/host"
)

const executorPath = "/usr/local/libexec/provision-host-executor"

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)
var username = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

type bootstrapRecord struct {
	SchemaVersion  string `json:"schemaVersion"`
	Environment    string `json:"environment"`
	Operator       string `json:"operator"`
	Account        string `json:"account"`
	ExecutorDigest string `json:"executorDigest"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "provision-host-executor:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "inspect" {
		return errors.New("no deployment operations are enabled; only inspect is available")
	}
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	environment := flags.String("environment", "", "Environment identity")
	operator := flags.String("operator", "", "bootstrap operator")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || !identifier.MatchString(*environment) || !username.MatchString(*operator) {
		return errors.New("inspect requires valid --environment and --operator")
	}
	result := inspect(*environment, *operator)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func inspect(environment, operator string) host.BootstrapStatus {
	account := "provision-" + environment
	result := host.BootstrapStatus{Environment: environment, Operator: operator, Account: account, AllowedOperations: []string{"inspect"}, Findings: []string{}}
	result.Architecture = strings.TrimSpace(command("uname", "-m"))
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			value = strings.Trim(value, `"`)
			switch key {
			case "ID":
				result.OS = value
			case "VERSION_ID":
				result.OSVersion = value
			}
		}
	}
	if result.OS != "ubuntu" {
		result.Findings = append(result.Findings, "Ubuntu host is required for this bootstrap")
	}
	result.SystemdVersion = firstLine(command("systemctl", "--version"))
	if !strings.HasPrefix(result.SystemdVersion, "systemd ") {
		result.Findings = append(result.Findings, "systemd is not available")
	}
	result.CaddyVersion = firstLine(command("caddy", "version"))
	result.SSHServerVersion = firstLine(commandCombined("/usr/sbin/sshd", "-V"))
	if result.SSHServerVersion == "" {
		result.Findings = append(result.Findings, "OpenSSH server version is not available")
	}
	if result.CaddyVersion == "" {
		result.Findings = append(result.Findings, "Caddy is not available")
	}
	result.CaddyActive = command("systemctl", "is-active", "caddy") == "active"
	if !result.CaddyActive {
		result.Findings = append(result.Findings, "Caddy service is not active")
	}
	result.JournaldActive = command("systemctl", "is-active", "systemd-journald") == "active"
	if !result.JournaldActive {
		result.Findings = append(result.Findings, "journald is not active")
	}
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	result.CgroupV2 = err == nil
	if !hasAccount(account, "/var/lib/provision/environments/"+environment) {
		result.Findings = append(result.Findings, "dedicated Environment account is missing or changed")
	}
	if !rootOwned(executorPath, 0755) || !rootOwned("/usr/local/libexec", 0755) {
		result.Findings = append(result.Findings, "executor path is not root-owned with safe permissions")
	}
	if !rootOwned("/etc/provision/bootstrap/"+environment+".json", 0644) {
		result.Findings = append(result.Findings, "bootstrap record permissions or ownership have changed")
	}
	if !rootOwned("/etc/sudoers.d/provision-"+environment, 0440) {
		result.Findings = append(result.Findings, "executor sudoers permissions or ownership have changed")
	}
	if !rootOwned("/var/lib/provision", 0755) || !rootOwned("/var/lib/provision/environments", 0755) {
		result.Findings = append(result.Findings, "Provision state directory permissions or ownership have changed")
	}
	if data, err := os.ReadFile("/etc/sudoers.d/provision-" + environment); err != nil || string(data) != fmt.Sprintf("%s ALL=(root) NOPASSWD: %s\n", operator, executorPath) {
		result.Findings = append(result.Findings, "executor sudoers rule does not match the declared operator")
	}
	if output, err := exec.Command("visudo", "-cf", "/etc/sudoers.d/provision-"+environment).CombinedOutput(); err != nil {
		result.Findings = append(result.Findings, "executor sudoers rule is invalid: "+strings.TrimSpace(string(output)))
	}
	if data, err := os.ReadFile(executorPath); err == nil {
		sum := sha256.Sum256(data)
		result.ExecutorDigest = "sha256:" + hex.EncodeToString(sum[:])
	} else {
		result.Findings = append(result.Findings, "executor binary is missing")
	}
	var record bootstrapRecord
	if data, err := os.ReadFile("/etc/provision/bootstrap/" + environment + ".json"); err != nil || json.Unmarshal(data, &record) != nil || record.SchemaVersion != "provision.dev/bootstrap/v1" || record.Environment != environment || record.Operator != operator || record.Account != account || record.ExecutorDigest != result.ExecutorDigest {
		result.Findings = append(result.Findings, "bootstrap record differs from installed executor or identities")
	}
	result.Ready = len(result.Findings) == 0
	return result
}

func command(name string, args ...string) string {
	output, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func commandCombined(name string, args ...string) string {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func firstLine(value string) string { return strings.SplitN(value, "\n", 2)[0] }

func hasAccount(name, home string) bool {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[0] == name {
			uid, uidErr := strconv.Atoi(fields[2])
			gid, gidErr := strconv.Atoi(fields[3])
			if uidErr != nil || gidErr != nil || uid <= 0 || uid >= 1000 || fields[5] != home || fields[6] != "/usr/sbin/nologin" {
				return false
			}
			info, err := os.Lstat(home)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0750 {
				return false
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			return ok && stat.Uid == uint32(uid) && stat.Gid == uint32(gid)
		}
	}
	return false
}

func rootOwned(path string, mode os.FileMode) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && stat.Gid == 0 && safeRootParents(path)
}

func safeRootParents(path string) bool {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || stat.Gid != 0 {
			return false
		}
		if parent == "/" {
			return true
		}
	}
}
