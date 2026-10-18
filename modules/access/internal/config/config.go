package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

type Config struct {
	GatewayClient    gatewayclient.FileConfig `yaml:"gateway_client"`
	VerificationFile string                   `yaml:"verification_file"`
	NoncePath        string                   `yaml:"nonce_path"`
	SourcePath       string                   `yaml:"-"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return Config{}, errors.New("access configuration unreadable or exceeds 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, errors.New("invalid access configuration YAML or unknown fields")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, errors.New("access configuration requires one YAML document")
	}
	if config.GatewayClient.Caller != "access" {
		return Config{}, errors.New("access gateway_client.caller must be access")
	}
	if err := config.GatewayClient.Validate(); err != nil {
		return Config{}, err
	}
	config.SourcePath, err = filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	for _, value := range []*string{&config.VerificationFile, &config.NoncePath} {
		if *value == "" || *value != strings.TrimSpace(*value) || strings.ContainsAny(*value, "\x00\r\n") {
			return Config{}, errors.New("access verification_file and nonce_path are required")
		}
		if !filepath.IsAbs(*value) {
			*value = filepath.Join(filepath.Dir(config.SourcePath), *value)
		}
	}
	return config, nil
}
