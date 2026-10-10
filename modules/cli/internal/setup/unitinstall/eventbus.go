package unitinstall

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

type eventBusCredential struct {
	Version      int      `yaml:"version"`
	URLs         []string `yaml:"urls"`
	Username     string   `yaml:"username"`
	Token        string   `yaml:"token,omitempty"`
	HostToken    string   `yaml:"eventbus_token,omitempty"`
	MonitorToken string   `yaml:"monitor_eventbus_token,omitempty"`
	CAFile       string   `yaml:"ca_file"`
}

func eventBusRole(id string) string {
	switch id {
	case "eventbus":
		return "internal-admin"
	case "host-agent":
		return "hostagent-publisher"
	case "monitor":
		return "monitor-observability"
	case "collector":
		return "collector-market-fetch-consumer"
	case "storage-primary", "storage-node", "storage-view":
		return "storage-eventbus"
	case "archive":
		return "archive-eventbus"
	case "strategy":
		return "strategy-eventbus"
	case "trade":
		return "trade-eventbus"
	case "factor-engine":
		return "factor-eventbus"
	}
	return ""
}

func eventBusUsername(role string) string {
	switch role {
	case "internal-admin":
		return "eventbus-internal-admin"
	case "monitor-observability":
		return "monitor-observability-consumer"
	default:
		return role
	}
}

// EventBusRoles is shared by the native exporter and the target projection.
func EventBusRoles(components []string) []string {
	roles := []string{}
	for _, id := range components {
		if id == "console-proxy" || id == "web-host" {
			continue
		}
		if role := eventBusRole(id); role != "" {
			roles = append(roles, role)
		}
		if id != "host-agent" {
			roles = append(roles, "metrics-publisher")
		}
	}
	slices.Sort(roles)
	return slices.Compact(roles)
}

// projectEventBus copies only the selected roles. Runtime configs reference
// the immutable release copies, never the export directory or another unit.
func projectEventBus(root *os.Root, options *PrepareOptions, components []string, destination string) error {
	if options.EventBusDirectory == "" && options.EventBusURL == "" {
		return nil
	}
	endpoint, err := url.Parse(options.EventBusURL)
	catalog, catalogErr := servicecatalog.LoadEmbedded()
	if catalogErr != nil {
		return catalogErr
	}
	component, found := catalog.Component("eventbus")
	if !found || len(component.Ports) != 1 {
		return errors.New("EventBus catalog endpoint is invalid")
	}
	port := component.Ports[0]
	if err != nil || endpoint.Scheme != "tls" || endpoint.Hostname() == "" || endpoint.Port() != strconv.Itoa(port) || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("issued EventBus material requires its catalog TLS endpoint")
	}
	source, err := fsutil.OpenPhysicalRoot(options.EventBusDirectory, true)
	if err != nil {
		return err
	}
	defer source.Close()
	ca, err := fsutil.ReadPrivate(source, "ca.pem", 64<<10)
	if err != nil {
		return err
	}
	trust := x509.NewCertPool()
	if !trust.AppendCertsFromPEM(ca) {
		return errors.New("issued EventBus CA is invalid")
	}
	for _, id := range components {
		if id == "console-proxy" || id == "web-host" {
			continue
		}
		values := options.Environment[id]
		if values == nil {
			values = map[string]string{}
			options.Environment[id] = values
		}
		for key := range values {
			if strings.HasPrefix(key, "MOOX_EVENTBUS_NATS_") || slices.Contains([]string{"MOOX_EVENTBUS_USERNAME", "MOOX_EVENTBUS_PASSWORD", "MOOX_EVENTBUS_CREDENTIALS", "MOOX_EVENTBUS_TLS_CA", "MOOX_EVENTBUS_TLS_CERT", "MOOX_EVENTBUS_TLS_KEY", "MOOX_EVENTBUS_HOST", "MOOX_EVENTBUS_PORT"}, key) {
				return errors.New("runtime EventBus overrides conflict with issued material")
			}
		}
		relative := id + "/secrets/eventbus/"
		absolute := filepath.Join(destination, filepath.FromSlash(relative))
		if err := fsutil.WritePrivate(root, relative+"ca.pem", ca, false); err != nil {
			return err
		}
		writeRole := func(role string) (string, error) {
			raw, err := fsutil.ReadPrivate(source, role+".yaml", 16<<10)
			if err != nil {
				return "", err
			}
			if _, err := yamlDocument(raw); err != nil {
				return "", err
			}
			var credential eventBusCredential
			decoder := yaml.NewDecoder(bytes.NewReader(raw))
			decoder.KnownFields(true)
			if decoder.Decode(&credential) != nil || credential.Version != 1 || credential.Username != eventBusUsername(role) || credential.CAFile != "ca.pem" || len(credential.URLs) != 1 {
				return "", errors.New("issued EventBus role configuration is invalid")
			}
			tokens := []string{credential.Token, credential.HostToken, credential.MonitorToken}
			if len(slices.DeleteFunc(tokens, func(s string) bool { return s == "" })) != 1 || len(credential.Token+credential.HostToken+credential.MonitorToken) < 32 {
				return "", errors.New("issued EventBus role requires one nonempty token")
			}
			if role == "hostagent-publisher" && credential.HostToken == "" || role == "monitor-observability" && credential.MonitorToken == "" || role != "hostagent-publisher" && role != "monitor-observability" && credential.Token == "" {
				return "", errors.New("issued EventBus token field does not match its role")
			}
			credential.URLs, credential.CAFile = []string{options.EventBusURL}, filepath.Join(absolute, "ca.pem")
			raw, err = yaml.Marshal(credential)
			if err != nil {
				return "", errors.New("issued EventBus role cannot be encoded")
			}
			name := relative + role + ".yaml"
			if id == "host-agent" && role == "hostagent-publisher" {
				name = id + "/config/eventbus.yaml"
			}
			if err := fsutil.WritePrivate(root, name, raw, id == "host-agent" && role == "hostagent-publisher"); err != nil {
				return "", err
			}
			return filepath.Join(destination, filepath.FromSlash(name)), nil
		}
		if id != "host-agent" {
			values["MOOX_METRICS_EVENTBUS_URL"] = options.EventBusURL
			metrics, err := writeRole("metrics-publisher")
			if err != nil {
				return err
			}
			values["MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE"] = metrics
		}
		role := eventBusRole(id)
		if role == "" {
			continue
		}
		credential, err := writeRole(role)
		if err != nil {
			return err
		}
		values["MOOX_EVENTBUS_URL"], values["MOOX_EVENTBUS_CREDENTIAL_FILE"] = options.EventBusURL, credential
		if id == "host-agent" {
			continue
		}
		if slices.Contains([]string{"storage-primary", "storage-node", "storage-view"}, id) {
			values["MOOX_STORAGE_EVENTBUS_URL"], values["MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE"] = options.EventBusURL, credential
			continue
		}
		if id == "monitor" {
			values["MOOX_OBSERVABILITY_EVENTBUS_URL"], values["MOOX_OBSERVABILITY_CREDENTIAL_FILE"] = options.EventBusURL, credential
		}
		filename := id + "/config/app.yaml"
		if id == "factor-engine" {
			filename = id + "/config/engine.yaml"
		}
		raw, err := root.ReadFile(filename)
		if err != nil {
			return err
		}
		document, err := yamlDocument(raw)
		if err != nil {
			return err
		}
		if id == "eventbus" {
			cert, err := fsutil.ReadPrivate(source, "server.pem", 64<<10)
			if err != nil {
				return err
			}
			key, err := fsutil.ReadPrivate(source, "server-key.pem", 64<<10)
			if err != nil {
				return err
			}
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				return errors.New("issued EventBus TLS server identity is invalid")
			}
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				return errors.New("issued EventBus TLS server certificate is invalid")
			}
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: trust, DNSName: endpoint.Hostname()}); err != nil {
				return errors.New("issued EventBus TLS server certificate does not verify for its endpoint")
			}
			for name, content := range map[string][]byte{"server.pem": cert, "server-key.pem": key} {
				if err := fsutil.WritePrivate(root, relative+name, content, false); err != nil {
					return err
				}
			}
			users, err := fsutil.ReadPrivate(source, "users.yaml", 128<<10)
			if err != nil {
				return err
			}
			if _, err := yamlDocument(users); err != nil {
				return err
			}
			if err := fsutil.WritePrivate(root, relative+"users.yaml", users, false); err != nil {
				return err
			}
			broker := member(document, "broker")
			for key, value := range map[string]any{"host": "0.0.0.0", "port": port, "client_advertise": endpoint.Host, "server_name": "eventbus-" + options.MaterialOptions.HostID, "auth": map[string]any{"enabled": true, "users_file": filepath.Join(absolute, "users.yaml")}, "tls": map[string]any{"enabled": true, "cert_file": filepath.Join(absolute, "server.pem"), "key_file": filepath.Join(absolute, "server-key.pem"), "ca_file": filepath.Join(absolute, "ca.pem")}} {
				if err := setValue(broker, key, value); err != nil {
					return err
				}
			}
			if err := setValue(document, "internal_client", map[string]string{"credential_file": credential, "tls_ca_file": filepath.Join(absolute, "ca.pem")}); err != nil {
				return err
			}
		} else if id == "monitor" {
			observability := member(document, "observability")
			if err := setValue(observability, "eventbus_urls", []string{options.EventBusURL}); err != nil {
				return err
			}
			if err := setValue(observability, "credential_file", credential); err != nil {
				return err
			}
		} else if id == "strategy" || id == "trade" || id == "factor-engine" {
			bus := member(document, "eventbus")
			if err := setValue(bus, "urls", []string{options.EventBusURL}); err != nil {
				return err
			}
			if err := setValue(bus, "credential_file", credential); err != nil {
				return err
			}
		}
		raw, err = yaml.Marshal(document)
		if err != nil {
			return errors.New("issued EventBus configuration cannot be encoded")
		}
		if err := fsutil.WritePrivate(root, filename, raw, true); err != nil {
			return err
		}
	}
	return nil
}
