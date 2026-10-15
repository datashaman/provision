package mediation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
	"provision/internal/config"
	"provision/internal/state"
)

const RegistrySchema = "provision.dev/request-registry/v1alpha1"
const RequestSchema = "provision.dev/operation-request/v1alpha1"

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type Request struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Application   string `json:"application"`
	Environment   string `json:"environment"`
	Revision      string `json:"revision"`
	Component     string `json:"component"`
	Operation     string `json:"operation"`
}

type Registry struct {
	SchemaVersion  string               `json:"schemaVersion"`
	Operator       string               `json:"operator"`
	Requesters     map[string]Requester `json:"requesters"`
	Configurations []config.Compiled    `json:"configurations"`
}

type Requester struct {
	Account string  `json:"account"`
	UID     uint32  `json:"uid"`
	GID     uint32  `json:"gid"`
	Inbox   string  `json:"inbox"`
	Results string  `json:"results"`
	Grants  []Grant `json:"grants"`
}

type Grant struct {
	Application string   `json:"application"`
	Environment string   `json:"environment"`
	Operations  []string `json:"operations"`
}

type Controller struct {
	RegistryPath string
	Backend      state.RequestBackend
}

func Open(path string, backend state.RequestBackend) (*Controller, error) {
	c := &Controller{RegistryPath: path, Backend: backend}
	_, err := c.registry()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Controller) registry() (Registry, error) {
	var registry Registry
	data, err := trustedRead(c.RegistryPath, 4<<20)
	if err != nil {
		return registry, err
	}
	if err := decode(data, &registry); err != nil {
		return registry, err
	}
	current, err := user.Current()
	if err != nil {
		return registry, err
	}
	if registry.SchemaVersion != RegistrySchema || registry.Operator != current.Username {
		return registry, errors.New("registry requires its authenticated trusted operator account")
	}
	seenUID := map[uint32]bool{}
	seenGID := map[uint32]bool{}
	seenDirectory := map[string]bool{}
	for name, r := range registry.Requesters {
		if seenUID[r.UID] || seenGID[r.GID] || seenDirectory[r.Inbox] || seenDirectory[r.Results] {
			return registry, errors.New("requesters must have distinct OS accounts, inboxes and result directories")
		}
		seenUID[r.UID] = true
		seenGID[r.GID] = true
		seenDirectory[r.Inbox] = true
		seenDirectory[r.Results] = true
		account, err := user.Lookup(r.Account)
		if err != nil || !namePattern.MatchString(name) || account.Uid != strconv.FormatUint(uint64(r.UID), 10) || account.Gid != strconv.FormatUint(uint64(r.GID), 10) {
			return registry, errors.New("requester registration does not match the OS account")
		}
		for _, p := range []struct {
			path  string
			inbox bool
		}{{r.Inbox, true}, {r.Results, false}} {
			fd, err := trustedDirectory(p.path, p.inbox, r.GID)
			if err != nil {
				return registry, err
			}
			var st unix.Stat_t
			err = unix.Fstat(fd, &st)
			unix.Close(fd)
			if err != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0007 != 0 || (!p.inbox && st.Mode&0020 != 0) {
				return registry, errors.New("inbox and results must be operator-owned and private to the registered requester group")
			}
			if st.Mode&0070 != 0 && st.Gid != r.GID {
				return registry, errors.New("inbox or result group differs from requester registration")
			}
		}
		if r.Inbox == r.Results {
			return registry, errors.New("inbox and results must be distinct")
		}
	}
	for _, compiled := range registry.Configurations {
		if err := compiled.VerifyIdentity(); err != nil {
			return registry, err
		}
	}
	return registry, nil
}

func decode(data []byte, out any) error {
	// encoding/json otherwise accepts duplicate keys, making reviewed and interpreted input ambiguous.
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate JSON field")
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("input must contain one JSON document")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Register captures all YAML documents before compilation; no later command opens their original paths.
func (c *Controller) Register(path string) (config.Compiled, error) {
	registry, err := c.registry()
	if err != nil {
		return config.Compiled{}, err
	}
	rootBytes, err := trustedRead(path, 1<<20)
	if err != nil {
		return config.Compiled{}, err
	}
	var root config.Root
	if err := yaml.Unmarshal(rootBytes, &root); err != nil {
		return config.Compiled{}, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(c.RegistryPath), ".capture-*")
	if err != nil {
		return config.Compiled{}, err
	}
	defer os.RemoveAll(dir)
	for name, data := range map[string][]byte{"root.yaml": rootBytes} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return config.Compiled{}, err
		}
	}
	for _, ref := range []string{root.Application, root.Environment, root.Revision} {
		if !filepath.IsLocal(ref) || ref == "root.yaml" {
			return config.Compiled{}, errors.New("configuration references must stay within the registered directory")
		}
		data, err := trustedRead(filepath.Join(filepath.Dir(path), ref), 1<<20)
		if err != nil {
			return config.Compiled{}, err
		}
		destination := filepath.Join(dir, ref)
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return config.Compiled{}, err
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			return config.Compiled{}, err
		}
	}
	compiled, err := config.Load(filepath.Join(dir, "root.yaml"))
	if err != nil {
		return config.Compiled{}, err
	}
	selection, err := compiled.HostSelection()
	if err != nil {
		return config.Compiled{}, err
	}
	if selection.Target.Local {
		return config.Compiled{}, errors.New("mediated stageArtifact currently requires a remote native HTTP Host Target")
	}
	for _, prior := range registry.Configurations {
		if prior.Application.Name == compiled.Application.Name && prior.Environment.Name == compiled.Environment.Name && prior.Revision.Name == compiled.Revision.Name {
			if prior.Digest != compiled.Digest {
				return config.Compiled{}, errors.New("registered revision inputs changed; register a new Revision identity")
			}
			return prior, nil
		}
	}
	registry.Configurations = append(registry.Configurations, compiled)
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return config.Compiled{}, err
	}
	return compiled, writeJSON(c.RegistryPath, data, 0600)
}
