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
	"strings"

	"go.yaml.in/yaml/v3"
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
	Role   string `yaml:"role" json:"role"`
	Health Health `yaml:"health" json:"health"`
}

type Health struct {
	Path string `yaml:"path" json:"path"`
}

type Environment struct {
	SchemaVersion   string                    `yaml:"schemaVersion" json:"schemaVersion"`
	Kind            string                    `yaml:"kind" json:"kind"`
	Name            string                    `yaml:"name" json:"name"`
	Application     string                    `yaml:"application" json:"application"`
	Targets         map[string]Target         `yaml:"targets" json:"targets"`
	Implementations map[string]Implementation `yaml:"implementations" json:"implementations"`
}

type Target struct {
	Kind    string `yaml:"kind" json:"kind"`
	Address string `yaml:"address" json:"address"`
	User    string `yaml:"user" json:"user"`
}

type Implementation struct {
	Kind     string   `yaml:"kind" json:"kind"`
	Target   string   `yaml:"target" json:"target"`
	Rollout  string   `yaml:"rollout" json:"rollout"`
	Endpoint Endpoint `yaml:"endpoint" json:"endpoint"`
}

type Endpoint struct {
	Port int `yaml:"port" json:"port"`
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

var (
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hostPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	userPattern   = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
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
	if len(c.Application.Components) != 1 || len(c.Environment.Implementations) != 1 || len(c.Revision.Artifacts) != 1 {
		return errors.New("initial host tracer supports exactly one HTTP component")
	}
	for name, component := range c.Application.Components {
		if !validName(name) || component.Role != "http" || !validHealthPath(component.Health.Path) {
			return fmt.Errorf("component %q must be an HTTP service with a health path", name)
		}
		implementation, ok := c.Environment.Implementations[name]
		if !ok || implementation.Kind != "systemd" || !validName(implementation.Target) || implementation.Endpoint.Port < 1024 || implementation.Endpoint.Port > 65535 {
			return fmt.Errorf("component %q requires a systemd implementation and unprivileged endpoint port", name)
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
		if !ok || target.Kind != "host" || !hostPattern.MatchString(target.Address) || strings.HasSuffix(target.Address, ".") || !userPattern.MatchString(target.User) {
			return fmt.Errorf("implementation target %q must name an existing remote host", implementation.Target)
		}
		artifact, ok := c.Revision.Artifacts[name]
		if !ok || !digestPattern.MatchString(artifact.Digest) || !validArtifactSource(artifact.Source) {
			return fmt.Errorf("component %q requires an immutable artifact source and sha256 digest", name)
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
