// Package config 加载外部接入的 config/app.yaml。相对路径按进程工作目录（组件目录）解析。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Config 是外部接入的配置。
type Config struct {
	// GatewayClient 是外部接入转发请求使用的 gatewayclient 配置（内部方式，access 身份）。
	GatewayClient gatewayclient.Config `yaml:"gateway_client"`
	// PrincipalsFile 是外部调用方的密钥集，由 Admin 的 keys export-principals 导出。
	PrincipalsFile string `yaml:"principals_file"`
	// NoncePath 是入站 nonce 的 SQLite 文件。
	NoncePath string `yaml:"nonce_path"`
	// MaxBodyBytes 是请求体和响应体的上限。
	MaxBodyBytes int64 `yaml:"max_body_bytes"`
	// Timeout 是一次转发的超时；调用方给出的截止时间更短时以调用方为准。
	Timeout time.Duration `yaml:"timeout"`
}

// Default 返回与部署布局一致的默认配置：密钥和 CA 在安装根目录，数据在组件的数据目录。
func Default() *Config {
	return &Config{
		GatewayClient: gatewayclient.Config{
			Mode: gatewayclient.ModeLocal, Caller: "access", KeyFile: "../secrets/caller-access.key",
			CAFile: "../certs/moox-ca.crt", CacheDir: "./data/gatewayclient",
		},
		PrincipalsFile: "../secrets/access-principals.json",
		NoncePath:      "./data/nonces.db",
		MaxBodyBytes:   32 << 20,
		Timeout:        30 * time.Second,
	}
}

// Load 读取配置文件，未写的字段使用默认值；文件不存在时使用默认配置。
func Load(path string) (*Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取外部接入配置 %s: %w", path, err)
	}
	if err == nil {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(cfg); err != nil {
			return nil, fmt.Errorf("解析外部接入配置 %s: %w", path, err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate 检查配置。
func (c Config) Validate() error {
	if err := c.GatewayClient.Validate(); err != nil {
		return err
	}
	if c.GatewayClient.Mode != gatewayclient.ModeLocal {
		return errors.New("外部接入的 gateway_client 必须是内部方式（local）")
	}
	if strings.TrimSpace(c.GatewayClient.Caller) != "access" {
		return errors.New("外部接入的 gateway_client.caller 必须是 access")
	}
	if strings.TrimSpace(c.PrincipalsFile) == "" {
		return errors.New("principals_file 不能为空")
	}
	if strings.TrimSpace(c.NoncePath) == "" {
		return errors.New("nonce_path 不能为空")
	}
	if c.MaxBodyBytes <= 0 {
		return errors.New("max_body_bytes 必须大于 0")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout 必须大于 0")
	}
	return nil
}
