package gatewayclient

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// FileConfig contains a caller's public identity and private key location.
// KeyID is assigned by Admin; it is independent from the caller name.
// HostID、LocalAddress、CAFile 由部署写进每个组件的 gateway_client 段：本机的主机 ID、本机网关 11002 的地址、
// MooX CA 根证书；组件不再去找相邻目录里的主机网关配置。
type FileConfig struct {
	Caller  string `yaml:"caller"`
	KeyID   string `yaml:"key_id"`
	KeyFile string `yaml:"key_file"`
	HostID  string `yaml:"host_id"`
	// LocalAddress 是本机网关的回环地址，部署写 127.0.0.1:11002。
	LocalAddress string `yaml:"local_address"`
	// CAFile 是 MooX CA 根证书；相对路径按模块配置文件所在目录解析。
	CAFile string `yaml:"ca_file"`
	// Pool 调整连接本机与跨主机网关的连接池；省略时使用默认。
	Pool PoolConfig `yaml:"pool"`
}

// ExternalFileConfig selects one fixed Access instance, without directory
// discovery or host gateway configuration. Credentials belong to an external
// principal; KeyID is assigned by Admin independently from Caller.
type ExternalFileConfig struct {
	Address    string `yaml:"access_address"`
	InstanceID string `yaml:"access_id"`
	Caller     string `yaml:"caller"`
	KeyID      string `yaml:"key_id"`
	KeyFile    string `yaml:"key_file"`
}

// Validate checks the public configuration without opening private files.
// An unset KeyID permits offline commands; OpenInternal requires it.
func (c FileConfig) Validate() error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	component, ok := catalog.Component(c.Caller)
	if !ok || component.ID == "host-gateway" {
		return errors.New("gateway_client.caller must identify an internal component")
	}
	if err := c.Pool.Validate(); err != nil {
		return err
	}
	if c.HostID != "" && !hostID.MatchString(c.HostID) {
		return errors.New("gateway_client.host_id must be a canonical host ID")
	}
	if c.LocalAddress != "" && !loopbackAddress(c.LocalAddress) {
		return errors.New("gateway_client.local_address must be a loopback address")
	}
	if c.CAFile != strings.TrimSpace(c.CAFile) || strings.ContainsAny(c.CAFile, "\x00\r\n") {
		return errors.New("gateway_client.ca_file is invalid")
	}
	return validateCredentialFile(c.KeyID, c.KeyFile, false)
}

func validateCredentialFile(keyID, keyFile string, required bool) error {
	if required && keyID == "" {
		return errors.New("gateway_client.key_id is required")
	}
	if len(keyID) > 128 || strings.ContainsAny(keyID, "/\\") || strings.ContainsFunc(keyID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("gateway_client.key_id must be a bounded credential identifier")
	}
	if keyFile == "" || keyFile != strings.TrimSpace(keyFile) || strings.ContainsAny(keyFile, "\x00\r\n") {
		return errors.New("gateway_client.key_file is required")
	}
	return nil
}

// Validate checks public fields without reading the signing key. External
// clients require their assigned KeyID even before opening a connection.
func (c ExternalFileConfig) Validate() error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	if err := validateExternalIdentity(catalog, c.Caller, c.Address, c.InstanceID); err != nil {
		return err
	}
	return validateCredentialFile(c.KeyID, c.KeyFile, true)
}

func validateExternalIdentity(catalog servicecatalog.Catalog, caller, address, instanceID string) error {
	if !validAddress(address) || !strings.HasPrefix(instanceID, "access@") || !hostID.MatchString(strings.TrimPrefix(instanceID, "access@")) {
		return errors.New("external client requires an access address and access@host instance ID")
	}
	if catalog.ExternalIdentity(caller) {
		return nil
	}
	return errors.New("unknown external principal")
}

func fileCredentials(configPath, caller, keyID, keyFile string) (gatewayauth.Credentials, error) {
	keyPath := keyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(filepath.Dir(configPath), keyPath)
	}
	secret, err := gatewayauth.ReadSigningSecret(keyPath)
	if err != nil {
		return gatewayauth.Credentials{}, fmt.Errorf("load gateway signing key: %w", err)
	}
	return gatewayauth.Credentials{Caller: caller, KeyID: keyID, Secret: secret}, nil
}

// OpenExternal resolves key_file relative to the component configuration and
// owns the resulting client's connections. It never reads environment values,
// local host configuration, a directory cache, or SSH settings. The caller
// must close the client at the end of the process or invocation.
func (c ExternalFileConfig) OpenExternal(moduleConfigPath string) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if moduleConfigPath == "" {
		return nil, errors.New("external gateway client requires a configuration path")
	}
	configPath, err := filepath.Abs(moduleConfigPath)
	if err != nil {
		return nil, err
	}
	credentials, err := fileCredentials(configPath, c.Caller, c.KeyID, c.KeyFile)
	if err != nil {
		return nil, err
	}
	return New(Config{Mode: External, Credentials: credentials, AccessAddress: c.Address, AccessInstanceID: c.InstanceID})
}

// OpenInternal 用 gateway_client 段里的主机 ID、本机网关地址与 CA 连本机网关。key_file、ca_file 的相对路径
// 按模块配置文件所在目录解析；目录缓存放在模块的数据目录下。
func (c FileConfig) OpenInternal(moduleConfigPath, dataDirectory string, onRefreshError func(error)) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.KeyID == "" || moduleConfigPath == "" || dataDirectory == "" {
		return nil, errors.New("gateway client requires key_id, module configuration path and data directory")
	}
	if !hostID.MatchString(c.HostID) || c.LocalAddress == "" || c.CAFile == "" {
		return nil, errors.New("gateway_client 要写 host_id、local_address 与 ca_file（由部署渲染）")
	}
	configPath, err := filepath.Abs(moduleConfigPath)
	if err != nil {
		return nil, err
	}
	credentials, err := fileCredentials(configPath, c.Caller, c.KeyID, c.KeyFile)
	if err != nil {
		return nil, err
	}
	caFile := c.CAFile
	if !filepath.IsAbs(caFile) {
		caFile = filepath.Join(filepath.Dir(configPath), caFile)
	}
	cachePath, err := filepath.Abs(filepath.Join(dataDirectory, "gatewayclient", "directory.json"))
	if err != nil {
		return nil, err
	}
	return New(Config{
		Mode: Internal, Credentials: credentials,
		LocalHostID: c.HostID, LocalAddress: c.LocalAddress, CAFile: caFile,
		CachePath: cachePath, Pool: c.Pool, OnRefreshError: onRefreshError,
	})
}
