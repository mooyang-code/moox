package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	manifestName = "custom.toml"
	maxFileSize  = 1 << 20
)

type Admin struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
}

type TencentCloud struct {
	SecretID  string `toml:"secret_id"`
	SecretKey string `toml:"secret_key"`
	Region    string `toml:"region"`
}

type EventBus struct {
	PublicAddress string `toml:"public_address"`
	Port          int    `toml:"port"`
	TLSEnabled    bool   `toml:"tls_enabled"`
}

type Monitoring struct {
	WeComWebhook string `toml:"wecom_webhook"`
}

type Host struct {
	Name     string `toml:"name"`
	Address  string `toml:"address"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

type Manifest struct {
	Admin        Admin        `toml:"admin"`
	TencentCloud TencentCloud `toml:"tencent_cloud"`
	EventBus     EventBus     `toml:"eventbus"`
	Monitoring   Monitoring   `toml:"monitoring"`
	ControlHost  Host         `toml:"control_host"`
	CompileHost  Host         `toml:"compile_host"`
	OtherHosts   []Host       `toml:"other_hosts"`
}

func (m Manifest) Hosts() []Host {
	hosts := make([]Host, 0, 1+len(m.OtherHosts))
	hosts = append(hosts, m.ControlHost)
	hosts = append(hosts, m.OtherHosts...)
	return hosts
}

func (m Manifest) HasCompileHost() bool {
	return hostConfigured(m.CompileHost)
}

type Snapshot struct {
	Manifest Manifest
	path     string
	info     os.FileInfo
	digest   [sha256.Size]byte
}

func Load(path, repositoryRoot string) (*Snapshot, error) {
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: resolve custom.toml path")
	}
	resolvedRoot, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: resolve repository root")
	}
	expectedPath := filepath.Join(filepath.Clean(resolvedRoot), manifestName)
	if filepath.Clean(resolvedPath) != expectedPath {
		if filepath.Base(resolvedPath) != manifestName {
			return nil, fmt.Errorf("config_invalid: setup file must be named custom.toml")
		}
		return nil, fmt.Errorf("config_invalid: custom.toml must be in repository root")
	}

	info, raw, err := readSecureFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := decodeStrict(raw, &manifest); err != nil {
		return nil, err
	}
	return &Snapshot{
		Manifest: manifest,
		path:     resolvedPath,
		info:     info,
		digest:   sha256.Sum256(raw),
	}, nil
}

func (s *Snapshot) VerifyUnchanged() error {
	info, raw, err := readSecureFile(s.path)
	if err != nil {
		return fmt.Errorf("config_changed: custom.toml security or identity changed")
	}
	if !os.SameFile(s.info, info) || s.digest != sha256.Sum256(raw) {
		return fmt.Errorf("config_changed: custom.toml changed during command")
	}
	return nil
}

func readSecureFile(path string) (os.FileInfo, []byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml is not readable")
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must be a regular file")
	}
	if linkInfo.Mode().Perm() != 0o600 {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must have mode 0600")
	}
	if !ownedByCurrentUser(linkInfo) {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must be owned by the current user")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml is not readable")
	}
	defer f.Close()
	openInfo, err := f.Stat()
	if err != nil || !openInfo.Mode().IsRegular() || !os.SameFile(linkInfo, openInfo) {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must be a stable regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml is not readable")
	}
	if len(raw) > maxFileSize {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml exceeds 1 MiB")
	}
	return openInfo, raw, nil
}

func decodeStrict(raw []byte, out *Manifest) error {
	md, err := toml.DecodeReader(bytes.NewReader(raw), out)
	if err != nil {
		return fmt.Errorf("config_invalid: decode custom.toml")
	}
	if keys := md.Undecoded(); len(keys) != 0 {
		return fmt.Errorf("config_invalid: unknown field %s", keys[0].String())
	}
	if out.ControlHost.Port == 0 {
		out.ControlHost.Port = 22
	}
	if !md.IsDefined("eventbus", "port") {
		out.EventBus.Port = 4222
	}
	if !md.IsDefined("tencent_cloud", "region") {
		out.TencentCloud.Region = "ap-guangzhou"
	}
	if out.HasCompileHost() && out.CompileHost.Port == 0 {
		out.CompileHost.Port = 22
	}
	for i := range out.OtherHosts {
		if out.OtherHosts[i].Port == 0 {
			out.OtherHosts[i].Port = 22
		}
	}
	return validate(out)
}

func validate(manifest *Manifest) error {
	manifest.Admin.Username = strings.TrimSpace(manifest.Admin.Username)
	if manifest.Admin.Username == "" {
		return fmt.Errorf("config_invalid: admin.username is required")
	}
	if manifest.Admin.Password == "" {
		return fmt.Errorf("config_invalid: admin.password is required")
	}
	if len([]byte(manifest.Admin.Password)) > 72 {
		return fmt.Errorf("config_invalid: admin.password must not exceed 72 bytes")
	}
	manifest.TencentCloud.SecretID = strings.TrimSpace(manifest.TencentCloud.SecretID)
	if manifest.TencentCloud.SecretID == "" {
		return fmt.Errorf("config_invalid: tencent_cloud.secret_id is required")
	}
	if manifest.TencentCloud.SecretKey == "" {
		return fmt.Errorf("config_invalid: tencent_cloud.secret_key is required")
	}
	manifest.TencentCloud.Region = strings.TrimSpace(manifest.TencentCloud.Region)
	if manifest.TencentCloud.Region == "" {
		return fmt.Errorf("config_invalid: tencent_cloud.region is required")
	}
	eventBusAddress := strings.TrimSpace(manifest.EventBus.PublicAddress)
	if eventBusAddress != manifest.EventBus.PublicAddress || !validEventBusAddress(eventBusAddress) {
		return fmt.Errorf("config_invalid: eventbus.public_address must be an IPv4 address or DNS hostname")
	}
	manifest.EventBus.PublicAddress = eventBusAddress
	if manifest.EventBus.Port < 1 || manifest.EventBus.Port > 65535 {
		return fmt.Errorf("config_invalid: eventbus.port must be between 1 and 65535")
	}
	if !manifest.EventBus.TLSEnabled {
		return fmt.Errorf("config_invalid: eventbus.tls_enabled must be true")
	}
	manifest.Monitoring.WeComWebhook = strings.TrimSpace(manifest.Monitoring.WeComWebhook)
	if manifest.Monitoring.WeComWebhook != "" && !validHTTPSWebhook(manifest.Monitoring.WeComWebhook) {
		return fmt.Errorf("config_invalid: monitoring.wecom_webhook must be a valid HTTPS URL")
	}

	names := make(map[string]struct{}, 1+len(manifest.OtherHosts))
	addresses := make(map[string]struct{}, 1+len(manifest.OtherHosts))
	if err := validateHost("control_host", &manifest.ControlHost, names, addresses, true); err != nil {
		return err
	}
	if manifest.HasCompileHost() {
		// The compiler may intentionally run on the control host. Keep it out
		// of the deployment-host uniqueness sets while still validating it.
		if err := validateHost("compile_host", &manifest.CompileHost, nil, nil, false); err != nil {
			return err
		}
	}
	for i := range manifest.OtherHosts {
		if err := validateHost(fmt.Sprintf("other_hosts[%d]", i), &manifest.OtherHosts[i], names, addresses, true); err != nil {
			return err
		}
	}
	return nil
}

func validHTTPSWebhook(rawURL string) bool {
	parsed, err := url.ParseRequestURI(rawURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.Fragment == ""
}

var dnsLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func validEventBusAddress(address string) bool {
	if address == "" {
		return false
	}
	if ip := net.ParseIP(address); ip != nil {
		return ip.To4() != nil
	}
	if len(address) > 253 || strings.Contains(address, "..") {
		return false
	}
	labels := strings.Split(address, ".")
	for _, label := range labels {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func hostConfigured(host Host) bool {
	return strings.TrimSpace(host.Name) != "" ||
		strings.TrimSpace(host.Address) != "" ||
		host.Port != 0 ||
		strings.TrimSpace(host.Username) != "" ||
		host.Password != ""
}

func validateHost(path string, host *Host, names, addresses map[string]struct{}, requirePassword bool) error {
	host.Name = strings.TrimSpace(host.Name)
	host.Address = strings.TrimSpace(host.Address)
	host.Username = strings.TrimSpace(host.Username)
	if host.Name == "" {
		return fmt.Errorf("config_invalid: %s.name is required", path)
	}
	if host.Address == "" {
		return fmt.Errorf("config_invalid: %s.address is required", path)
	}
	if host.Port < 1 || host.Port > 65535 {
		return fmt.Errorf("config_invalid: %s.port must be between 1 and 65535", path)
	}
	if host.Username == "" {
		return fmt.Errorf("config_invalid: %s.username is required", path)
	}
	if requirePassword && host.Password == "" {
		return fmt.Errorf("config_invalid: %s.password is required", path)
	}
	if names != nil {
		nameKey := strings.ToLower(host.Name)
		if _, exists := names[nameKey]; exists {
			return fmt.Errorf("config_invalid: duplicate host name %s", host.Name)
		}
		names[nameKey] = struct{}{}
	}
	if addresses != nil {
		addressKey := strings.ToLower(host.Address)
		if _, exists := addresses[addressKey]; exists {
			return fmt.Errorf("config_invalid: duplicate host address %s", host.Address)
		}
		addresses[addressKey] = struct{}{}
	}
	return nil
}
