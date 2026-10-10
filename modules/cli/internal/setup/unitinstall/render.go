package unitinstall

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

func applyOverrides(root *os.Root, options PrepareOptions, components []string) error {
	if len(options.Overrides) > 128 {
		return errors.New("too many rendered configuration overrides")
	}
	var total int64
	for name, source := range options.Overrides {
		parts := strings.Split(name, "/")
		if !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00\r\n") || len(parts) != 3 || !slices.Contains(components, parts[0]) || parts[1] != "config" || name == "host-gateway/config/app.yaml" || (path.Ext(name) != ".yaml" && path.Ext(name) != ".json") {
			return errors.New("rendered overrides must belong to the selected component config directory")
		}
		parent, err := fsutil.OpenPhysicalRoot(filepath.Dir(source), false)
		if err != nil {
			return err
		}
		raw, err := fsutil.ReadPrivate(parent, filepath.Base(source), 1<<20)
		parent.Close()
		if err != nil {
			return err
		}
		total += int64(len(raw))
		if total > 16<<20 {
			return errors.New("rendered configuration overrides exceed the private input limit")
		}
		if err := fsutil.WritePrivate(root, name, raw, true); err != nil {
			return err
		}
	}
	return nil
}

func yamlDocument(raw []byte) (*yaml.Node, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("configuration override is not valid YAML")
	}
	if decoder.Decode(new(any)) != io.EOF || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration requires exactly one YAML mapping")
	}
	if err := uniqueMappings(&document); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}
func uniqueMappings(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("configuration aliases and anchors are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] || key.Value == "<<" {
				return errors.New("configuration contains a duplicate or invalid mapping key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := uniqueMappings(child); err != nil {
			return err
		}
	}
	return nil
}
func member(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func setValue(node *yaml.Node, key string, value any) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return errors.New("configuration field must be a mapping")
	}
	var encoded yaml.Node
	if err := encoded.Encode(value); err != nil {
		return errors.New("configuration field cannot be encoded")
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = &encoded
			return nil
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &encoded)
	return nil
}

func renderConfigurations(root *os.Root, options PrepareOptions, components []string, projection unitbundle.Projection, destination string) error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	keys := map[string]string{}
	for _, credential := range projection.Credentials {
		keys[credential.Caller] = credential.KeyID
	}
	for _, id := range components {
		component, _ := catalog.Component(id)
		if id == "host-gateway" || id == "web-host" {
			continue
		}
		directory := id + "/config"
		if err := fs.WalkDir(root.FS(), directory, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || path.Ext(name) != ".yaml" {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || !fsutil.Owned(info) || info.Size() > 1<<20 {
				return errors.New("software configuration template is not a bounded regular file")
			}
			raw, err := root.ReadFile(name)
			if err != nil {
				return err
			}
			document, err := yamlDocument(raw)
			if err != nil {
				return err
			}
			if client := member(document, "gateway_client"); client != nil {
				if keys[id] == "" {
					return errors.New("gateway configuration lacks its assigned component identity")
				}
				if err := setValue(document, "gateway_client", map[string]string{"caller": id, "key_id": keys[id], "key_file": "../../secrets/caller-" + id + ".key"}); err != nil {
					return err
				}
			}
			bind := "0.0.0.0"
			if component.Health.Loopback {
				bind = "127.0.0.1"
			}
			if health := member(document, "health"); health != nil {
				for _, key := range []string{"addr", "listen"} {
					if member(health, key) != nil {
						if err := setValue(health, key, net.JoinHostPort(bind, strconv.Itoa(component.Health.Port))); err != nil {
							return err
						}
					}
				}
			}
			if services := member(member(document, "server"), "service"); services != nil && services.Kind == yaml.SequenceNode {
				for _, service := range services.Content {
					if port := member(service, "port"); port != nil && port.Value == strconv.Itoa(component.Health.Port) {
						if err := setValue(service, "ip", bind); err != nil {
							return err
						}
					}
				}
			}
			if id == "host-agent" && name == "host-agent/config/app.yaml" {
				for key, value := range map[string]string{"host_id": projection.HostID, "identity_path": filepath.Join(destination, "host-agent", "data", "identity.yaml"), "eventbus_config": filepath.Join(destination, "host-agent", "config", "eventbus.yaml"), "health_addr": net.JoinHostPort(bind, strconv.Itoa(component.Health.Port))} {
					if err := setValue(document, key, value); err != nil {
						return err
					}
				}
			}
			var output bytes.Buffer
			encoder := yaml.NewEncoder(&output)
			encoder.SetIndent(2)
			if err := encoder.Encode(document); err != nil {
				return errors.New("cannot encode rendered component configuration")
			}
			if err := encoder.Close(); err != nil {
				return err
			}
			return fsutil.WritePrivate(root, name, output.Bytes(), true)
		}); err != nil {
			return err
		}
	}
	if slices.Contains(components, "host-agent") {
		if _, err := fsutil.ReadPrivate(root, "host-agent/config/eventbus.yaml", 1<<20); err != nil {
			return errors.New("host installation requires its rendered private EventBus configuration")
		}
	}
	return nil
}
