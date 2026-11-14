package config

import (
	"bytes"
	"errors"
	"io"
	"os"

	"github.com/mooyang-code/moox/packages/security/domainpolicy"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Domains []string `yaml:"domains"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return Config{}, errors.New("egress configuration unreadable or exceeds 1 MiB")
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, errors.New("invalid egress configuration YAML or unknown fields")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("egress configuration requires one YAML document")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	_, err := domainpolicy.New(c.Domains)
	return err
}
