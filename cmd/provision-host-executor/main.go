// provision-host-executor is a nonresident, root-owned host entrypoint. This
// first increment exposes inspection only; Plan-bound mutations arrive in #6.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"provision/internal/host"
)

const executorPath = "/usr/local/libexec/provision-host-executor"

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)
var username = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
var deploymentIdentifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,127}$`)
var systemdUnit = regexp.MustCompile(`^provision-[a-z0-9-]+\.service$`)

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
	result := host.BootstrapStatus{SchemaVersion: "provision.dev/host-inspection/v1alpha1", Environment: environment, Operator: operator, Account: account, AllowedOperations: []string{"inspect"}, Findings: []string{}}
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
	result.CaddyConfigValid = commandSucceeded("caddy", "validate", "--config", "/etc/caddy/Caddyfile")
	if !result.CaddyConfigValid {
		result.Findings = append(result.Findings, "Caddy configuration does not validate")
	}
	result.CaddyAdminReachable = caddyAdminOK("/config/")
	if !result.CaddyAdminReachable {
		result.Findings = append(result.Findings, "Caddy admin endpoint is not reachable")
	}
	var portErr error
	result.ListeningTCPPorts, portErr = listeningTCPPorts()
	if portErr != nil {
		result.Findings = append(result.Findings, "listening TCP ports cannot be observed")
	}
	result.JournaldActive = command("systemctl", "is-active", "systemd-journald") == "active"
	if !result.JournaldActive {
		result.Findings = append(result.Findings, "journald is not active")
	}
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	result.CgroupV2 = err == nil
	result.GenerationStorageReady = hasAccount(account, "/var/lib/provision/environments/"+environment)
	if !result.GenerationStorageReady {
		result.Findings = append(result.Findings, "dedicated Environment account is missing or changed")
	}
	active, err := readActiveGeneration(environment)
	if err != nil {
		result.Findings = append(result.Findings, err.Error())
	} else if active != nil {
		active.UnitActive = command("systemctl", "is-active", active.SystemdUnit) == "active"
		workingDirectory := command("systemctl", "show", active.SystemdUnit, "--property=WorkingDirectory", "--value")
		environment := strings.Fields(command("systemctl", "show", active.SystemdUnit, "--property=Environment", "--value"))
		active.UnitMatches = workingDirectory == active.ReleaseDirectory && contains(environment, fmt.Sprintf("PORT=%d", active.Port))
		route, routeOK := caddyAdminRead("/id/" + url.PathEscape(active.RouteID))
		active.RouteObserved = routeOK
		active.RouteUpstream = firstJSONValue(route, "dial")
		active.RouteMatches = active.RouteUpstream == fmt.Sprintf("127.0.0.1:%d", active.Port)
		if !active.UnitActive {
			result.Findings = append(result.Findings, "recorded active generation systemd unit is not active")
		}
		if !active.UnitMatches {
			result.Findings = append(result.Findings, "active systemd unit does not match the recorded release directory and port")
		}
		if !active.RouteObserved {
			result.Findings = append(result.Findings, "recorded active generation Caddy route is not observed")
		} else if !active.RouteMatches {
			result.Findings = append(result.Findings, "active Caddy route does not match the recorded generation port")
		}
		result.Deployment.Active = active
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

func commandSucceeded(name string, args ...string) bool {
	return exec.Command(name, args...).Run() == nil
}

func caddyAdminOK(path string) bool {
	_, ok := caddyAdminRead(path)
	return ok
}

func caddyAdminRead(path string) ([]byte, bool) {
	client := http.Client{Timeout: 500 * time.Millisecond}
	response, err := client.Get("http://127.0.0.1:2019" + path)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return data, err == nil && response.StatusCode >= 200 && response.StatusCode < 300
}

func firstJSONValue(data []byte, key string) string {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	var find func(any) string
	find = func(current any) string {
		switch current := current.(type) {
		case map[string]any:
			if value, ok := current[key].(string); ok {
				return value
			}
			for _, child := range current {
				if value := find(child); value != "" {
					return value
				}
			}
		case []any:
			for _, child := range current {
				if value := find(child); value != "" {
					return value
				}
			}
		}
		return ""
	}
	return find(value)
}

func listeningTCPPorts() ([]int, error) {
	ports := map[int]bool{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			_, encodedPort, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			port, err := strconv.ParseUint(encodedPort, 16, 16)
			if err == nil {
				ports[int(port)] = true
			}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	result := make([]int, 0, len(ports))
	for port := range ports {
		result = append(result, port)
	}
	sort.Ints(result)
	return result, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func readActiveGeneration(environment string) (*host.GenerationStatus, error) {
	path := filepath.Join("/var/lib/provision/environments", environment, "active-generation.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("active generation observation cannot be read")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
		return nil, errors.New("active generation observation is not a safe regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("active generation observation cannot be read")
	}
	var active host.GenerationStatus
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&active); err != nil {
		return nil, errors.New("active generation observation is invalid")
	}
	releaseRoot := filepath.Join("/var/lib/provision/environments", environment, "releases")
	if !deploymentIdentifier.MatchString(active.ID) || !deploymentIdentifier.MatchString(active.Revision) || !systemdUnit.MatchString(active.SystemdUnit) || !deploymentIdentifier.MatchString(active.RouteID) || active.Port < 1024 || active.Port > 65535 || !filepath.IsAbs(active.ReleaseDirectory) || !strings.HasPrefix(filepath.Clean(active.ReleaseDirectory), releaseRoot+string(os.PathSeparator)) {
		return nil, errors.New("active generation observation contains unsafe identity or path data")
	}
	return &active, nil
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
