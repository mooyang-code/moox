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
	if len(c.KeyID) > 128 || strings.ContainsAny(c.KeyID, "/\\") || strings.ContainsFunc(c.KeyID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("gateway_client.key_id must be a bounded credential identifier")
	}
	if c.KeyFile == "" || c.KeyFile != strings.TrimSpace(c.KeyFile) || strings.ContainsAny(c.KeyFile, "\x00\r\n") {
		return errors.New("gateway_client.key_file is required")
	}
	return nil
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
	keyPath := c.KeyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(filepath.Dir(configPath), keyPath)
	}
	secret, err := gatewayauth.ReadSigningSecret(keyPath)
	if err != nil {
		return nil, fmt.Errorf("load gateway signing key: %w", err)
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
		Mode: Internal, Credentials: gatewayauth.Credentials{Caller: c.Caller, KeyID: c.KeyID, Secret: secret},
		LocalHostID: host.Host.ID, LocalAddress: host.Server.LocalAddr, CAFile: host.TLS.CAFile,
		CachePath: cachePath, OnRefreshError: onRefreshError,
	})
}
