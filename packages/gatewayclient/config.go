package gatewayclient

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

// FileConfig contains a caller's public identity and private key location.
// KeyID is assigned by Admin; it is independent from the caller name.
type FileConfig struct {
	Caller  string `yaml:"caller"`
	KeyID   string `yaml:"key_id"`
	KeyFile string `yaml:"key_file"`
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
	for _, principal := range catalog.Principals {
		if principal.ID == caller {
			return nil
		}
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

// OpenInternal uses the canonical release layout: <root>/<module>/config/app.yaml
// and <root>/hostgateway/config/app.yaml. Host identity, loopback address and CA
// come from the host gateway configuration. The key path is relative to the
// module configuration; the directory cache belongs to the module data directory.
func (c FileConfig) OpenInternal(moduleConfigPath, dataDirectory string, onRefreshError func(error)) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.KeyID == "" || moduleConfigPath == "" || dataDirectory == "" {
		return nil, errors.New("gateway client requires key_id, module configuration path and data directory")
	}
	configPath, err := filepath.Abs(moduleConfigPath)
	if err != nil {
		return nil, err
	}
	credentials, err := fileCredentials(configPath, c.Caller, c.KeyID, c.KeyFile)
	if err != nil {
		return nil, err
	}
	hostPath := filepath.Join(filepath.Dir(configPath), "..", "..", "hostgateway", "config", "app.yaml")
	host, err := hostgatewayconfig.Load(hostPath)
	if err != nil {
		return nil, fmt.Errorf("load local host gateway configuration: %w", err)
	}
	cachePath, err := filepath.Abs(filepath.Join(dataDirectory, "gatewayclient", "directory.json"))
	if err != nil {
		return nil, err
	}
	return New(Config{
		Mode: Internal, Credentials: credentials,
		LocalHostID: host.Host.ID, LocalAddress: host.Server.LocalAddr, CAFile: host.TLS.CAFile,
		CachePath: cachePath, OnRefreshError: onRefreshError,
	})
}
