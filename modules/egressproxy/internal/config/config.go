// Package config 加载出口代理的 config/app.yaml。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/proxy"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"gopkg.in/yaml.v3"
)

// Config 是出口代理的配置。
type Config struct {
	HTTP HTTPConfig `yaml:"http"`
	DNS  DNSConfig  `yaml:"dns"`
}

// HTTPConfig 是 Do 的设置。
type HTTPConfig struct {
	// Domains 是允许访问的域名白名单，支持 "*." 前缀；Collector 按同一份白名单决定哪些请求走出口代理。
	Domains []string `yaml:"domains"`
	// AllowedHeaders 是允许转发的请求头。
	AllowedHeaders   []string      `yaml:"allowed_headers"`
	MaxResponseBytes int64         `yaml:"max_response_bytes"`
	DefaultTimeout   time.Duration `yaml:"default_timeout"`
}

// DNSConfig 是 ResolveDomains 的设置；domains 为空时不提供 DNS 解析。
type DNSConfig struct {
	Domains         []string      `yaml:"domains"`
	LookupTimeout   time.Duration `yaml:"lookup_timeout"`
	ProbeTimeout    time.Duration `yaml:"probe_timeout"`
	ProbePort       int           `yaml:"probe_port"`
	CacheTTL        time.Duration `yaml:"cache_ttl"`
	MaxIPsPerDomain int           `yaml:"max_ips_per_domain"`
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		HTTP: HTTPConfig{
			AllowedHeaders: proxy.DefaultHeaders, MaxResponseBytes: proxy.DefaultMaxResponseBytes, DefaultTimeout: proxy.DefaultTimeout,
		},
		DNS: DNSConfig{
			LookupTimeout: 1500 * time.Millisecond, ProbeTimeout: 500 * time.Millisecond, ProbePort: 443,
			CacheTTL: 5 * time.Minute, MaxIPsPerDomain: 4,
		},
	}
}

// Load 读取配置文件，未写的字段使用默认值。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取出口代理配置 %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析出口代理配置 %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate 检查配置。
func (c Config) Validate() error {
	if domains, err := egresspb.ParseDomainList(c.HTTP.Domains); err != nil {
		return fmt.Errorf("http.domains: %w", err)
	} else if domains.Empty() {
		return errors.New("http.domains 不能为空")
	}
	if c.HTTP.MaxResponseBytes <= 0 {
		return errors.New("http.max_response_bytes 必须大于 0")
	}
	if c.HTTP.DefaultTimeout <= 0 || c.HTTP.DefaultTimeout > proxy.MaxTimeout {
		return fmt.Errorf("http.default_timeout 必须在 (0, %s] 之内", proxy.MaxTimeout)
	}
	if len(c.DNS.Domains) > 16 {
		return errors.New("dns.domains 最多 16 个")
	}
	for _, domain := range c.DNS.Domains {
		if !egresspb.ValidDomain(domain) {
			return fmt.Errorf("dns.domains 中的 %q 不是合法域名", domain)
		}
	}
	if c.DNS.LookupTimeout <= 0 || c.DNS.ProbeTimeout <= 0 || c.DNS.CacheTTL <= 0 {
		return errors.New("dns 的超时和缓存时间必须大于 0")
	}
	if c.DNS.ProbePort <= 0 || c.DNS.ProbePort > 65535 {
		return errors.New("dns.probe_port 无效")
	}
	if c.DNS.MaxIPsPerDomain <= 0 || c.DNS.MaxIPsPerDomain > 4 {
		return errors.New("dns.max_ips_per_domain 必须在 1 到 4 之间")
	}
	return nil
}
