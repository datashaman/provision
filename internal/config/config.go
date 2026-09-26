package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"provision/internal/drain"
	"provision/internal/rollbackwindow"
)

const SchemaVersion = "provision.dev/v1alpha1"

type Root struct {
	SchemaVersion string `yaml:"schemaVersion" json:"schemaVersion"`
	Application   string `yaml:"application" json:"application"`
	Environment   string `yaml:"environment" json:"environment"`
	Revision      string `yaml:"revision" json:"revision"`
}

type Application struct {
	SchemaVersion string               `yaml:"schemaVersion" json:"schemaVersion"`
	Kind          string               `yaml:"kind" json:"kind"`
	Name          string               `yaml:"name" json:"name"`
	Components    map[string]Component `yaml:"components" json:"components"`
}

type Component struct {
	Role     string           `yaml:"role" json:"role"`
	Requires []string         `yaml:"requires,omitempty" json:"requires,omitempty"`
	Health   Health           `yaml:"health,omitempty" json:"health,omitempty,omitzero"`
	Queue    QueueContract    `yaml:"queue,omitempty" json:"queue,omitempty,omitzero"`
	Worker   WorkerContract   `yaml:"worker,omitempty" json:"worker,omitempty,omitzero"`
	Task     TaskContract     `yaml:"task,omitempty" json:"task,omitempty,omitzero"`
	Schedule ScheduleContract `yaml:"schedule,omitempty" json:"schedule,omitempty,omitzero"`
}

type Health struct {
	Liveness              HealthCheck `yaml:"liveness" json:"liveness"`
	Readiness             HealthCheck `yaml:"readiness" json:"readiness"`
	CandidateVerification HealthCheck `yaml:"candidateVerification" json:"candidateVerification"`
}

type HealthCheck struct {
	Path      string `yaml:"path,omitempty" json:"path,omitempty"`
	Condition string `yaml:"condition,omitempty" json:"condition,omitempty"`
}

type QueueContract struct {
	Delivery         string `yaml:"delivery" json:"delivery"`
	Acknowledgement  string `yaml:"acknowledgement" json:"acknowledgement"`
	PublisherConfirm string `yaml:"publisherConfirm" json:"publisherConfirm"`
	Retry            string `yaml:"retry" json:"retry"`
	DeadLetter       string `yaml:"deadLetter" json:"deadLetter"`
	Retention        string `yaml:"retention" json:"retention"`
	Ordering         string `yaml:"ordering" json:"ordering"`
	Deduplication    string `yaml:"deduplication" json:"deduplication"`
}

type WorkerContract struct {
	Queue string `yaml:"queue" json:"queue"`
}

type TaskContract struct {
	Timeout string `yaml:"timeout" json:"timeout"`
}

type ScheduleContract struct {
	Task           string            `yaml:"task" json:"task"`
	Expression     string            `yaml:"expression" json:"expression"`
	Timezone       string            `yaml:"timezone" json:"timezone"`
	DaylightSaving string            `yaml:"daylightSaving" json:"daylightSaving"`
	Overlap        string            `yaml:"overlap" json:"overlap"`
	Retry          ScheduleRetry     `yaml:"retry" json:"retry"`
	MissedRun      ScheduleMissedRun `yaml:"missedRun" json:"missedRun"`
	Failure        string            `yaml:"failure" json:"failure"`
}

type ScheduleRetry struct {
	MaxAttempts int    `yaml:"maxAttempts" json:"maxAttempts"`
	Delay       string `yaml:"delay" json:"delay"`
}

type ScheduleMissedRun struct {
	Mode           string `yaml:"mode" json:"mode"`
	MaxOccurrences int    `yaml:"maxOccurrences" json:"maxOccurrences"`
}

type Environment struct {
	SchemaVersion   string                    `yaml:"schemaVersion" json:"schemaVersion"`
	Kind            string                    `yaml:"kind" json:"kind"`
	Name            string                    `yaml:"name" json:"name"`
	Application     string                    `yaml:"application" json:"application"`
	RollbackWindow  rollbackwindow.Window     `yaml:"rollbackWindow" json:"rollbackWindow"`
	Targets         map[string]Target         `yaml:"targets" json:"targets"`
	Implementations map[string]Implementation `yaml:"implementations" json:"implementations"`
}

type Target struct {
	Kind    string `yaml:"kind" json:"kind"`
	Local   bool   `yaml:"local,omitempty" json:"local,omitempty"`
	Address string `yaml:"address" json:"address"`
	User    string `yaml:"user" json:"user"`
}

type Implementation struct {
	Kind       string                 `yaml:"kind" json:"kind"`
	Target     string                 `yaml:"target" json:"target"`
	Lifecycle  string                 `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	Credential string                 `yaml:"credential,omitempty" json:"credential,omitempty"`
	Rollout    string                 `yaml:"rollout,omitempty" json:"rollout,omitempty"`
	Endpoint   Endpoint               `yaml:"endpoint,omitempty" json:"endpoint,omitempty,omitzero"`
	Worker     WorkerImplementation   `yaml:"worker,omitempty" json:"worker,omitempty,omitzero"`
	Schedule   ScheduleImplementation `yaml:"schedule,omitempty" json:"schedule,omitempty,omitzero"`
}

type WorkerImplementation struct {
	Admission string      `yaml:"admission" json:"admission"`
	Drain     WorkerDrain `yaml:"drain" json:"drain"`
}

type WorkerDrain struct {
	Mode        string `yaml:"mode" json:"mode"`
	MaxDuration string `yaml:"maxDuration" json:"maxDuration"`
}

type ScheduleImplementation struct {
	LedgerSchema string `yaml:"ledgerSchema" json:"ledgerSchema"`
}

type Endpoint struct {
	Port  int   `yaml:"port" json:"port"`
	Drain Drain `yaml:"drain" json:"drain"`
}

type Drain struct {
	Mode        drain.Mode  `yaml:"mode" json:"mode"`
	MaxDuration drain.Bound `yaml:"maxDuration" json:"maxDuration"`
}

type Revision struct {
	SchemaVersion string              `yaml:"schemaVersion" json:"schemaVersion"`
	Kind          string              `yaml:"kind" json:"kind"`
	Name          string              `yaml:"name" json:"name"`
	Application   string              `yaml:"application" json:"application"`
	Artifacts     map[string]Artifact `yaml:"artifacts" json:"artifacts"`
}

type Artifact struct {
	Source string `yaml:"source" json:"source"`
	Digest string `yaml:"digest" json:"digest"`
}

type Compiled struct {
	Application Application `json:"application"`
	Environment Environment `json:"environment"`
	Revision    Revision    `json:"revision"`
	Digest      string      `json:"digest"`
}

type HostSelection struct {
	Component      string
	Implementation Implementation
	Artifact       Artifact
	TargetName     string
	Target         Target
}

// HostSelection returns the single, validated native Host Target slice used by
// the initial HTTP tracer.
func (c Compiled) HostSelection() (HostSelection, error) {
	for component, implementation := range c.Environment.Implementations {
		artifact, ok := c.Revision.Artifacts[component]
		if !ok {
			return HostSelection{}, errors.New("Revision has no Artifact for planned component")
		}
		target, ok := c.Environment.Targets[implementation.Target]
		if !ok {
			return HostSelection{}, errors.New("Implementation has no configured Host Target")
		}
		return HostSelection{
			Component:      component,
			Implementation: implementation,
			Artifact:       artifact,
			TargetName:     implementation.Target,
			Target:         target,
		}, nil
	}
	return HostSelection{}, errors.New("configuration has no implementation to plan")
}

type TargetSelection struct {
	Name   string
	Target Target
}

// PlanningTarget returns the one Host Target shared by every implementation in
// the current tracer. Iteration is sorted so map order cannot influence errors.
func (c Compiled) PlanningTarget() (TargetSelection, error) {
	names := make([]string, 0, len(c.Environment.Implementations))
	for name := range c.Environment.Implementations {
		names = append(names, name)
	}
	sort.Strings(names)
	var selected TargetSelection
	for _, name := range names {
		implementation := c.Environment.Implementations[name]
		target, ok := c.Environment.Targets[implementation.Target]
		if !ok {
			return TargetSelection{}, fmt.Errorf("component %q has no configured Host Target", name)
		}
		if selected.Name == "" {
			selected = TargetSelection{Name: implementation.Target, Target: target}
			continue
		}
		if selected.Name != implementation.Target {
			return TargetSelection{}, errors.New("current tracer requires one shared Host Target")
		}
	}
	if selected.Name == "" {
		return TargetSelection{}, errors.New("configuration has no implementation to plan")
	}
	return selected, nil
}

func (c Compiled) IsAsync() bool {
	if len(c.Application.Components) != 4 {
		return false
	}
	roles := map[string]bool{}
	for _, component := range c.Application.Components {
		roles[component.Role] = true
	}
	return roles["queue"] && roles["worker"] && roles["task"] && roles["schedule"]
}

// VerifyArtifactFile checks supplied local bytes against the immutable digest
// declared for the initial single-component Revision. It never contacts or
// changes the Host Target.
func (c Compiled) VerifyArtifactFile(path string) error {
	if len(c.Revision.Artifacts) != 1 {
		return errors.New("configuration has multiple Artifacts; name each Artifact file")
	}
	for name := range c.Revision.Artifacts {
		return c.verifyArtifactFile(name, path, "artifact")
	}
	return errors.New("Revision has no Artifact to verify")
}

// VerifyArtifactFiles checks one explicitly named local file for every
// Artifact in the Revision. Component names prevent same-shaped archives from
// being accidentally swapped.
func (c Compiled) VerifyArtifactFiles(paths map[string]string) error {
	if len(paths) != len(c.Revision.Artifacts) {
		return errors.New("one named Artifact file is required for every Revision Artifact")
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := c.Revision.Artifacts[name]; !ok {
			return fmt.Errorf("Artifact %q is not declared by the Revision", name)
		}
		if err := c.verifyArtifactFile(name, paths[name], "Artifact "+name); err != nil {
			return err
		}
	}
	return nil
}

func (c Compiled) verifyArtifactFile(name, path, label string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", label, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", label)
	}
	artifact := c.Revision.Artifacts[name]
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	actual := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if actual != artifact.Digest {
		return fmt.Errorf("%s digest mismatch: expected %s, got %s", label, artifact.Digest, actual)
	}
	return nil
}

var (
	namePattern      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hostPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	userPattern      = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	cronFieldPattern = regexp.MustCompile(`^[0-9*/,-]+$`)
)

// Load compiles an explicit root document and its three local references. This
// initial subset handles exactly one HTTP component; it is not a general schema.
func Load(rootPath string) (Compiled, error) {
	rootReal, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return Compiled{}, fmt.Errorf("root configuration: %w", err)
	}
	base := filepath.Dir(rootReal)
	var root Root
	if err := readDocument(rootReal, &root); err != nil {
		return Compiled{}, err
	}
	if root.SchemaVersion != SchemaVersion {
		return Compiled{}, errors.New("root schemaVersion is unsupported")
	}
	paths := []string{root.Application, root.Environment, root.Revision}
	resolved := make([]string, len(paths))
	for i, ref := range paths {
		if !filepath.IsLocal(ref) {
			return Compiled{}, fmt.Errorf("document reference %q must be a local relative path", ref)
		}
		path, err := filepath.EvalSymlinks(filepath.Join(base, ref))
		if err != nil {
			return Compiled{}, fmt.Errorf("document reference %q: %w", ref, err)
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || !filepath.IsLocal(rel) {
			return Compiled{}, fmt.Errorf("document reference %q escapes the configuration directory", ref)
		}
		resolved[i] = path
	}
	var result Compiled
	if err := readDocument(resolved[0], &result.Application); err != nil {
		return Compiled{}, err
	}
	if err := readDocument(resolved[1], &result.Environment); err != nil {
		return Compiled{}, err
	}
	if err := readDocument(resolved[2], &result.Revision); err != nil {
		return Compiled{}, err
	}
	if err := result.validate(); err != nil {
		return Compiled{}, err
	}
	canonical, err := json.Marshal(struct {
		Application Application `json:"application"`
		Environment Environment `json:"environment"`
		Revision    Revision    `json:"revision"`
	}{result.Application, result.Environment, result.Revision})
	if err != nil {
		return Compiled{}, err
	}
	sum := sha256.Sum256(canonical)
	result.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return result, nil
}

func readDocument(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("%s: document exceeds 1 MiB", path)
	}
	var node yaml.Node
	parser := yaml.NewDecoder(bytes.NewReader(data))
	if err := parser.Decode(&node); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := inspectNode(&node); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var extra yaml.Node
	if err := parser.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%s: multiple YAML documents are not supported", path)
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func inspectNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("YAML aliases and anchors are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" {
				return errors.New("YAML mapping keys must be ordinary strings")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate YAML key %q", key.Value)
			}
			seen[key.Value] = true
		}
	}
	if node.Kind == yaml.ScalarNode {
		switch node.Tag {
		case "!!str", "!!int", "!!bool", "!!null":
		default:
			return fmt.Errorf("unsupported YAML scalar tag %q", node.Tag)
		}
	}
	for _, child := range node.Content {
		if err := inspectNode(child); err != nil {
			return err
		}
	}
	return nil
}

func (c Compiled) validate() error {
	if c.Application.SchemaVersion != SchemaVersion || c.Environment.SchemaVersion != SchemaVersion || c.Revision.SchemaVersion != SchemaVersion {
		return errors.New("document schemaVersion is unsupported")
	}
	if c.Application.Kind != "Application" || c.Environment.Kind != "Environment" || c.Revision.Kind != "Revision" {
		return errors.New("document kind does not match its root reference")
	}
	if !validName(c.Application.Name) || !validName(c.Environment.Name) || !validName(c.Revision.Name) {
		return errors.New("application, environment, and revision names must be lowercase identifiers")
	}
	if c.Environment.Application != c.Application.Name || c.Revision.Application != c.Application.Name {
		return errors.New("environment and revision must reference the same application")
	}
	if _, err := rollbackwindow.ParseWindow(string(c.Environment.RollbackWindow)); err != nil && strings.Contains(err.Error(), "canonical") {
		return errors.New("Environment requires a valid canonical rollbackWindow")
	} else if err != nil {
		return errors.New("Environment requires a supported rollbackWindow from 1m0s through 720h0m0s")
	}
	if len(c.Application.Components) != 1 {
		return c.validateAsync()
	}
	if len(c.Application.Components) != 1 || len(c.Environment.Implementations) != 1 || len(c.Revision.Artifacts) != 1 {
		return errors.New("initial host tracer supports exactly one HTTP component")
	}
	for name, component := range c.Application.Components {
		if !validName(name) || component.Role != "http" {
			return fmt.Errorf("component %q must be an HTTP service", name)
		}
		for _, check := range []struct{ name, path string }{
			{"liveness", component.Health.Liveness.Path},
			{"readiness", component.Health.Readiness.Path},
			{"candidateVerification", component.Health.CandidateVerification.Path},
		} {
			if !validHealthPath(check.path) {
				return fmt.Errorf("component %q requires a valid %s health path", name, check.name)
			}
		}
		implementation, ok := c.Environment.Implementations[name]
		if !ok || implementation.Kind != "systemd" || !validName(implementation.Target) || implementation.Endpoint.Port < 1024 || implementation.Endpoint.Port > 65535 {
			return fmt.Errorf("component %q requires a systemd implementation and unprivileged endpoint port", name)
		}
		if implementation.Endpoint.Drain.Mode != drain.ModeBoundedHTTP {
			return fmt.Errorf("component %q requires the bounded-http drain mode", name)
		}
		_, err := drain.ParseBound(string(implementation.Endpoint.Drain.MaxDuration))
		if err != nil && strings.Contains(err.Error(), "canonical") {
			return fmt.Errorf("component %q requires a valid canonical drain maxDuration", name)
		}
		if err != nil {
			return fmt.Errorf("component %q requires a supported drain maxDuration from 1s through 5m", name)
		}
		switch implementation.Rollout {
		case "required", "preferred", "replace":
		default:
			return fmt.Errorf("component %q has unsupported rollout requirement", name)
		}
		if len(c.Environment.Targets) != 1 {
			return errors.New("initial host tracer supports exactly one host target")
		}
		target, ok := c.Environment.Targets[implementation.Target]
		if !ok || target.Kind != "host" || !userPattern.MatchString(target.User) {
			return fmt.Errorf("implementation target %q must name an existing host and operator", implementation.Target)
		}
		if target.Local {
			if target.Address != "" {
				return fmt.Errorf("local implementation target %q cannot declare an address", implementation.Target)
			}
		} else if !hostPattern.MatchString(target.Address) || strings.HasSuffix(target.Address, ".") {
			return fmt.Errorf("implementation target %q must name an existing remote host", implementation.Target)
		}
		artifact, ok := c.Revision.Artifacts[name]
		if !ok || !digestPattern.MatchString(artifact.Digest) || !validArtifactSource(artifact.Source) {
			return fmt.Errorf("component %q requires an immutable artifact source and sha256 digest", name)
		}
	}
	return nil
}

func (c Compiled) validateAsync() error {
	if len(c.Application.Components) != 4 || len(c.Environment.Implementations) != 4 || len(c.Revision.Artifacts) != 2 {
		return errors.New("asynchronous host tracer requires exactly one Queue, Worker, Task, and Schedule with Worker and Task Artifacts")
	}
	roles := make(map[string]string, 4)
	for name, component := range c.Application.Components {
		if !validName(name) {
			return fmt.Errorf("component %q must be a lowercase identifier", name)
		}
		if component.Role == "scheduler" {
			return fmt.Errorf("component %q uses unsupported role scheduler; model a Schedule instead", name)
		}
		switch component.Role {
		case "queue", "worker", "task", "schedule":
		default:
			return fmt.Errorf("component %q has unsupported asynchronous role %q", name, component.Role)
		}
		if previous := roles[component.Role]; previous != "" {
			return fmt.Errorf("asynchronous host tracer requires exactly one %s component", component.Role)
		}
		if err := validateAsyncComponentShape(name, component); err != nil {
			return err
		}
		roles[component.Role] = name
	}
	for _, role := range []string{"queue", "worker", "task", "schedule"} {
		if roles[role] == "" {
			return fmt.Errorf("asynchronous host tracer requires exactly one %s component", role)
		}
	}

	queueName := roles["queue"]
	queue := c.Application.Components[queueName].Queue
	wantQueue := QueueContract{
		Delivery: "at-least-once", Acknowledgement: "manual", PublisherConfirm: "required",
		Retry: "bounded-redelivery-3", DeadLetter: "required", Retention: "24h0m0s",
		Ordering: "unqualified", Deduplication: "unqualified",
	}
	if queue != wantQueue {
		return fmt.Errorf("Queue %q requests unsupported delivery, acknowledgement, retry, dead-letter, retention, ordering, or deduplication semantics", queueName)
	}

	workerName := roles["worker"]
	worker := c.Application.Components[workerName]
	if target, ok := c.Application.Components[worker.Worker.Queue]; !ok {
		return fmt.Errorf("Worker %q references missing Queue %q", workerName, worker.Worker.Queue)
	} else if target.Role != "queue" {
		return fmt.Errorf("Worker %q may reference only a Queue", workerName)
	}
	checks := map[string]HealthCheck{
		"liveness": worker.Health.Liveness, "readiness": worker.Health.Readiness,
		"candidateVerification": worker.Health.CandidateVerification,
	}
	wantConditions := map[string]string{
		"liveness": "process-active", "readiness": "queue-connected",
		"candidateVerification": "gated-queue-connected",
	}
	for name, check := range checks {
		if check.Path != "" || check.Condition != wantConditions[name] {
			return fmt.Errorf("Worker %q requires %s condition %q", workerName, name, wantConditions[name])
		}
	}

	taskName := roles["task"]
	if err := validateCanonicalDuration(c.Application.Components[taskName].Task.Timeout, time.Second, time.Hour); err != nil {
		return fmt.Errorf("Task %q requires a canonical timeout from 1s through 1h0m0s", taskName)
	}

	scheduleName := roles["schedule"]
	schedule := c.Application.Components[scheduleName].Schedule
	if target, ok := c.Application.Components[schedule.Task]; !ok {
		return fmt.Errorf("Schedule %q references missing Task %q", scheduleName, schedule.Task)
	} else if target.Role != "task" {
		return fmt.Errorf("Schedule %q may reference only a Task", scheduleName)
	}
	if !validCronExpression(schedule.Expression) {
		return fmt.Errorf("Schedule %q requires a five-field expression", scheduleName)
	}
	if schedule.Timezone == "" {
		return fmt.Errorf("Schedule %q requires an explicit timezone", scheduleName)
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("Schedule %q has unknown timezone %q", scheduleName, schedule.Timezone)
	}
	if schedule.DaylightSaving != "wall-clock" || schedule.Overlap != "forbid" || schedule.Failure != "record" {
		return fmt.Errorf("Schedule %q has incomplete daylight-saving, overlap, or failure policy", scheduleName)
	}
	if schedule.Retry.MaxAttempts < 1 || schedule.Retry.MaxAttempts > 10 || validateCanonicalDuration(schedule.Retry.Delay, time.Second, time.Hour) != nil {
		return fmt.Errorf("Schedule %q requires a bounded retry policy", scheduleName)
	}
	switch schedule.MissedRun.Mode {
	case "skip":
		if schedule.MissedRun.MaxOccurrences != 0 {
			return fmt.Errorf("Schedule %q skip policy cannot declare catch-up occurrences", scheduleName)
		}
	case "bounded-catch-up":
		if schedule.MissedRun.MaxOccurrences < 1 || schedule.MissedRun.MaxOccurrences > 100 {
			return fmt.Errorf("Schedule %q requires a bounded catch-up policy", scheduleName)
		}
	default:
		return fmt.Errorf("Schedule %q requires an explicit missed-run policy", scheduleName)
	}

	if err := validateComponentGraph(c.Application.Components); err != nil {
		return err
	}
	if len(c.Environment.Targets) != 1 {
		return errors.New("asynchronous host tracer supports exactly one Host Target")
	}
	for name, implementation := range c.Environment.Implementations {
		component, ok := c.Application.Components[name]
		if !ok {
			return fmt.Errorf("implementation %q has no Application component", name)
		}
		target, ok := c.Environment.Targets[implementation.Target]
		if !ok || target.Kind != "host" || !validName(implementation.Target) || !userPattern.MatchString(target.User) {
			return fmt.Errorf("implementation target %q must name an existing Host Target and operator", implementation.Target)
		}
		if target.Local {
			if target.Address != "" {
				return fmt.Errorf("local implementation target %q cannot declare an address", implementation.Target)
			}
		} else if !hostPattern.MatchString(target.Address) || strings.HasSuffix(target.Address, ".") {
			return fmt.Errorf("implementation target %q must name an existing remote Host Target", implementation.Target)
		}
		switch component.Role {
		case "queue":
			if implementation.Endpoint != (Endpoint{}) || implementation.Worker != (WorkerImplementation{}) || implementation.Schedule != (ScheduleImplementation{}) {
				return fmt.Errorf("Queue %q contains fields for another implementation type", name)
			}
			if implementation.Kind != "rabbitmq-quadlet" || implementation.Lifecycle != "managed" || implementation.Rollout != "required" || !validSecretReference(implementation.Credential) {
				return fmt.Errorf("Queue %q requires a managed rabbitmq-quadlet implementation and Queue credential Secret Reference", name)
			}
		case "worker":
			if implementation.Lifecycle != "" || implementation.Credential != "" || implementation.Endpoint != (Endpoint{}) || implementation.Schedule != (ScheduleImplementation{}) {
				return fmt.Errorf("Worker %q contains fields for another implementation type", name)
			}
			if implementation.Kind != "systemd-worker" || implementation.Rollout != "required" || implementation.Worker.Admission != "gated" || implementation.Worker.Drain.Mode != "bounded-in-flight" || validateCanonicalDuration(implementation.Worker.Drain.MaxDuration, time.Second, 5*time.Minute) != nil {
				return fmt.Errorf("Worker %q requires gated systemd-worker blue-green with a bounded drain from 1s through 5m0s", name)
			}
		case "task":
			if implementation.Kind != "systemd-task" || implementation.Lifecycle != "" || implementation.Credential != "" || implementation.Rollout != "required" || implementation.Endpoint != (Endpoint{}) || implementation.Worker != (WorkerImplementation{}) || implementation.Schedule != (ScheduleImplementation{}) {
				return fmt.Errorf("Task %q requires a systemd-task implementation", name)
			}
		case "schedule":
			if implementation.Kind != "systemd-schedule" || implementation.Lifecycle != "" || implementation.Credential != "" || implementation.Rollout != "required" || implementation.Endpoint != (Endpoint{}) || implementation.Worker != (WorkerImplementation{}) || implementation.Schedule.LedgerSchema != "provision.dev/schedule-ledger/v1alpha1" {
				return fmt.Errorf("Schedule %q requires a systemd-schedule implementation and supported occurrence-ledger schema", name)
			}
		}
	}
	for name, artifact := range c.Revision.Artifacts {
		component, ok := c.Application.Components[name]
		if !ok || component.Role != "worker" && component.Role != "task" {
			return fmt.Errorf("Artifact %q must belong to the Worker or Task", name)
		}
		if !digestPattern.MatchString(artifact.Digest) || !validArtifactSource(artifact.Source) {
			return fmt.Errorf("component %q requires an immutable Artifact source and sha256 digest", name)
		}
	}
	for _, name := range []string{workerName, taskName} {
		if _, ok := c.Revision.Artifacts[name]; !ok {
			return fmt.Errorf("component %q requires an immutable Artifact", name)
		}
	}
	return nil
}

func validateAsyncComponentShape(name string, component Component) error {
	zeroHealth := Health{}
	zeroQueue := QueueContract{}
	zeroWorker := WorkerContract{}
	zeroTask := TaskContract{}
	zeroSchedule := ScheduleContract{}
	switch component.Role {
	case "queue":
		if component.Health != zeroHealth || component.Worker != zeroWorker || component.Task != zeroTask || component.Schedule != zeroSchedule {
			return fmt.Errorf("Queue %q contains fields for another component type", name)
		}
	case "worker":
		if component.Queue != zeroQueue || component.Task != zeroTask || component.Schedule != zeroSchedule {
			return fmt.Errorf("Worker %q contains fields for another component type", name)
		}
	case "task":
		if component.Health != zeroHealth || component.Queue != zeroQueue || component.Worker != zeroWorker || component.Schedule != zeroSchedule {
			return fmt.Errorf("Task %q contains fields for another component type", name)
		}
	case "schedule":
		if component.Health != zeroHealth || component.Queue != zeroQueue || component.Worker != zeroWorker || component.Task != zeroTask {
			return fmt.Errorf("Schedule %q contains fields for another component type", name)
		}
	}
	return nil
}

func validateCanonicalDuration(value string, minimum, maximum time.Duration) error {
	duration, err := time.ParseDuration(value)
	if err != nil || duration.String() != value || duration < minimum || duration > maximum {
		return errors.New("invalid duration")
	}
	return nil
}

func validCronExpression(value string) bool {
	fields := strings.Fields(value)
	if len(fields) != 5 || strings.Join(fields, " ") != value {
		return false
	}
	for _, field := range fields {
		if !cronFieldPattern.MatchString(field) {
			return false
		}
	}
	return true
}

func validSecretReference(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "secret" && u.User == nil && u.Host != "" && u.Path != "" && u.RawQuery == "" && u.Fragment == ""
}

func validateComponentGraph(components map[string]Component) error {
	edges := make(map[string][]string, len(components))
	for name, component := range components {
		edges[name] = append(edges[name], component.Requires...)
		if component.Role == "worker" {
			edges[name] = append(edges[name], component.Worker.Queue)
		}
		if component.Role == "schedule" {
			edges[name] = append(edges[name], component.Schedule.Task)
		}
		for _, reference := range edges[name] {
			if _, ok := components[reference]; !ok {
				return fmt.Errorf("component %q references missing component %q", name, reference)
			}
		}
	}
	state := make(map[string]uint8, len(components))
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return fmt.Errorf("component references contain a cycle at %q", name)
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, reference := range edges[name] {
			if err := visit(reference); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for name := range components {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func validName(value string) bool { return namePattern.MatchString(value) }

func validHealthPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "?#\\") {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] <= ' ' || value[i] == 0x7f {
			return false
		}
	}
	return true
}

func validArtifactSource(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "oci"
}
