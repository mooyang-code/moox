package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
	"github.com/mooyang-code/moox/packages/security/domainpolicy"
	"gopkg.in/yaml.v3"
)

type DNS struct {
	Domains         []string `yaml:"domains"`
	LookupTimeoutMS int      `yaml:"lookup_timeout_ms"`
	ProbeTimeoutMS  int      `yaml:"probe_timeout_ms"`
	ProbePort       int      `yaml:"probe_port"`
	CacheTTLSeconds int      `yaml:"cache_ttl_seconds"`
	MaxIPsPerDomain int      `yaml:"max_ips_per_domain"`
}

type Config struct {
	Domains []string `yaml:"domains"`
	DNS     DNS      `yaml:"dns"`
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
	cfg := Config{DNS: DNS{LookupTimeoutMS: 1500, ProbeTimeoutMS: 500, ProbePort: 443, CacheTTLSeconds: 300, MaxIPsPerDomain: 4}}
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
	if _, err := domainpolicy.New(c.Domains); err != nil {
		return err
	}
	if len(c.DNS.Domains) > 16 {
		return errors.New("egress DNS supports at most 16 exact domains")
	}
	seen := map[string]bool{}
	for _, value := range c.DNS.Domains {
		domain, ok := domainpolicy.Host(value)
		if !ok || seen[domain] {
			return errors.New("egress DNS domains must be valid unique DNS names")
		}
		seen[domain] = true
	}
	if c.DNS.LookupTimeoutMS <= 0 || c.DNS.LookupTimeoutMS > 60000 || c.DNS.ProbeTimeoutMS <= 0 || c.DNS.ProbeTimeoutMS > 60000 || c.DNS.ProbePort < 1 || c.DNS.ProbePort > 65535 || c.DNS.CacheTTLSeconds < 1 || c.DNS.CacheTTLSeconds > 86400 || c.DNS.MaxIPsPerDomain < 1 || c.DNS.MaxIPsPerDomain > 4 {
		return errors.New("egress DNS timeout, port, cache TTL or IP limit is invalid")
	}
	return nil
}

func (c DNS) ResolverConfig(metrics *resolver.Metrics) resolver.Config {
	return resolver.Config{Domains: c.Domains, LookupTimeout: time.Duration(c.LookupTimeoutMS) * time.Millisecond, ProbeTimeout: time.Duration(c.ProbeTimeoutMS) * time.Millisecond, ProbePort: c.ProbePort, CacheTTL: time.Duration(c.CacheTTLSeconds) * time.Second, MaxIPsPerDomain: c.MaxIPsPerDomain, Metrics: metrics}
}
