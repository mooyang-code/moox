package jetstream

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type CredentialFile struct {
	URLs                 []string `yaml:"urls"`
	Version              int      `yaml:"version"`
	Username             string   `yaml:"username"`
	Password             string   `yaml:"password"`
	Token                string   `yaml:"token"`
	EventBusToken        string   `yaml:"eventbus_token"`
	MonitorEventBusToken string   `yaml:"monitor_eventbus_token"`
	CAFile               string   `yaml:"ca_file"`
}

// ExpandCredentialPath resolves environment variables and a leading ~/ so
// role credential files can be configured consistently across deployments.
func ExpandCredentialPath(path string) string {
	path = os.ExpandEnv(path)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func LoadCredentialFile(path string) (CredentialFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return CredentialFile{}, fmt.Errorf("stat credential file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return CredentialFile{}, fmt.Errorf("credential file must be a regular 0600 file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return CredentialFile{}, fmt.Errorf("read credential file: %w", err)
	}
	var file CredentialFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return file, fmt.Errorf("parse credential file: %w", err)
	}
	if file.Password == "" {
		file.Password = file.Token
	}
	if file.Password == "" {
		file.Password = file.EventBusToken
	}
	if file.Password == "" {
		file.Password = file.MonitorEventBusToken
	}
	if strings.TrimSpace(file.Username) == "" || file.Password == "" {
		return file, fmt.Errorf("credential file requires username and token/password")
	}
	return file, nil
}

func (c *Config) ApplyCredentialFile(path string) error {
	file, err := LoadCredentialFile(path)
	if err != nil {
		return err
	}
	caFile := file.CAFile
	if caFile != "" && !filepath.IsAbs(caFile) {
		caFile = filepath.Join(filepath.Dir(path), caFile)
	}
	c.Username, c.Password = file.Username, file.Password
	// Keep a deployment-wide CA supplied by the environment when a legacy or
	// externally managed role file omits ca_file. A credential file with an
	// explicit CA remains authoritative and is resolved relative to itself.
	if caFile != "" {
		c.TLSCAFile = caFile
	}
	// The deployment endpoint is supplied by the module config or the
	// deployment-wide environment. Credential exports on the control host may
	// deliberately contain its loopback URL, which must not override a remote
	// service endpoint. Example YAML loopback URLs must not hide a non-local
	// role file, which is how intranet engine clients reach the public EventBus.
	if len(file.URLs) > 0 && (len(c.URLs) == 0 || (allEventBusURLsLoopback(c.URLs) && anyEventBusURLRoutable(file.URLs))) {
		c.URLs = append([]string(nil), file.URLs...)
	}
	return nil
}

func eventBusURLHost(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		return parsed.Hostname()
	}
	value = strings.TrimPrefix(strings.TrimPrefix(value, "tls://"), "nats://")
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return value
	}
	return host
}

func eventBusURLIsLoopback(raw string) bool {
	switch strings.ToLower(eventBusURLHost(raw)) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func allEventBusURLsLoopback(urls []string) bool {
	if len(urls) == 0 {
		return true
	}
	for _, item := range urls {
		if strings.TrimSpace(item) == "" || !eventBusURLIsLoopback(item) {
			return false
		}
	}
	return true
}

func anyEventBusURLRoutable(urls []string) bool {
	for _, item := range urls {
		if strings.TrimSpace(item) != "" && !eventBusURLIsLoopback(item) {
			return true
		}
	}
	return false
}
