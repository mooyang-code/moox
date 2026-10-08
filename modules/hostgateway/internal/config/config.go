// Package config 读取并校验主机网关的配置（执行计划 2.5）。
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

const (
	// RemoteAddress 是跨主机入口（TLS），LocalAddress 是本机入口与服务目录，HealthAddress 是健康与指标。
	RemoteAddress = "0.0.0.0:11003"
	LocalAddress  = "127.0.0.1:11002"
	HealthAddress = "0.0.0.0:11012"
	// ControlLocalTarget 是 control 主机上网关控制的本机地址。
	ControlLocalTarget = "127.0.0.1:11112"
)

// Config 是主机网关的配置。
type Config struct {
	Host struct {
		ID string `yaml:"id"`
	} `yaml:"host"`
	Server struct {
		RemoteAddr string `yaml:"remote_addr"`
		LocalAddr  string `yaml:"local_addr"`
		HealthAddr string `yaml:"health_addr"`
	} `yaml:"server"`
	TLS struct {
		CertFile string `yaml:"cert_file"`
		KeyFile  string `yaml:"key_file"`
		CAFile   string `yaml:"ca_file"`
	} `yaml:"tls"`
	Control struct {
		// Target 是网关控制的地址：control 主机上为 127.0.0.1:11112，其他主机为 <control>:11003。
		Target  string `yaml:"target"`
		Caller  string `yaml:"caller"`
		KeyFile string `yaml:"key_file"`
	} `yaml:"control"`
	Store struct {
		Path string `yaml:"path"`
	} `yaml:"store"`
}

// Direct 判断是否直连本机的网关控制：只有 control 主机如此。
func (c Config) Direct() bool { return c.Host.ID == servicecatalog.ControlHostID }

// Load 读取配置文件；相对路径按配置文件所在目录解析。
func Load(path string) (Config, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取主机网关配置: %w", err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(encoded)))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("解析主机网关配置: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, errors.New("主机网关配置只能包含一个 YAML 文档")
	}
	cfg.Host.ID = strings.TrimSpace(cfg.Host.ID)
	cfg.Server.RemoteAddr = defaultString(cfg.Server.RemoteAddr, RemoteAddress)
	cfg.Server.LocalAddr = defaultString(cfg.Server.LocalAddr, LocalAddress)
	cfg.Server.HealthAddr = defaultString(cfg.Server.HealthAddr, HealthAddress)
	cfg.TLS.CertFile = resolvePath(path, cfg.TLS.CertFile)
	cfg.TLS.KeyFile = resolvePath(path, cfg.TLS.KeyFile)
	cfg.TLS.CAFile = resolvePath(path, cfg.TLS.CAFile)
	cfg.Control.Target = strings.TrimSpace(cfg.Control.Target)
	cfg.Control.Caller = strings.TrimSpace(cfg.Control.Caller)
	cfg.Control.KeyFile = resolvePath(path, cfg.Control.KeyFile)
	cfg.Store.Path = resolvePath(path, cfg.Store.Path)
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 校验配置。
func Validate(cfg Config) error {
	if cfg.Host.ID == "" {
		return errors.New("host.id 不能为空")
	}
	if want := servicecatalog.HostGatewayIdentity(cfg.Host.ID); cfg.Control.Caller != want {
		return fmt.Errorf("control.caller 必须是 %s", want)
	}
	for name, address := range map[string]string{"server.remote_addr": cfg.Server.RemoteAddr, "server.local_addr": cfg.Server.LocalAddr, "server.health_addr": cfg.Server.HealthAddr, "control.target": cfg.Control.Target} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return fmt.Errorf("%s %q 不是 host:port: %w", name, address, err)
		}
	}
	if host, _, _ := net.SplitHostPort(cfg.Server.LocalAddr); !isLoopback(host) {
		return errors.New("server.local_addr 只能监听本机回环地址")
	}
	if cfg.Direct() {
		// 直连时自己写入可信的调用方身份，只能发往本机回环地址上的网关控制。
		if host, _, _ := net.SplitHostPort(cfg.Control.Target); !isLoopback(host) {
			return fmt.Errorf("control 主机只能直连本机的网关控制（%s）", ControlLocalTarget)
		}
		if cfg.Control.KeyFile != "" {
			return errors.New("control 主机直连本机的网关控制，不使用 control.key_file")
		}
	} else if cfg.Control.KeyFile == "" {
		return errors.New("control.key_file 不能为空：其他主机经 control 的 11003 访问网关控制时要签名")
	}
	for name, file := range map[string]string{"tls.cert_file": cfg.TLS.CertFile, "tls.key_file": cfg.TLS.KeyFile, "tls.ca_file": cfg.TLS.CAFile} {
		if file == "" {
			return fmt.Errorf("%s 不能为空", name)
		}
	}
	if cfg.Store.Path == "" {
		return errors.New("store.path 不能为空")
	}
	return nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func defaultString(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func resolvePath(configPath, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return filepath.Join(filepath.Dir(configPath), value)
	}
	return filepath.Join(filepath.Dir(absConfig), value)
}
