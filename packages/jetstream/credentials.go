package jetstream

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type CredentialFile struct {
	Version              int    `yaml:"version"`
	Username             string `yaml:"username"`
	Password             string `yaml:"password"`
	Token                string `yaml:"token"`
	EventBusToken        string `yaml:"eventbus_token"`
	MonitorEventBusToken string `yaml:"monitor_eventbus_token"`
	CAFile               string `yaml:"ca_file"`
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
	c.Username, c.Password, c.TLSCAFile = file.Username, file.Password, caFile
	return nil
}
