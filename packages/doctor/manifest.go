package doctor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

const MaxManifestComponents = 64

type Transport string

// Transport 是组件健康的上报来源（设计 3.8，组件健康只看上报）：reporter 是组件自己经 packages/report 上报；
// host_snapshot 是 host-agent 的主机快照；delegated 是外部程序只给管理端发心跳，由管理端代报（因子引擎、归档 worker）。
const (
	TransportReporter     Transport = "reporter"
	TransportHostSnapshot Transport = "host_snapshot"
	TransportDelegated    Transport = "delegated"
)

type FunctionalObservability string

const (
	FunctionalObservabilityActive        FunctionalObservability = "active"
	FunctionalObservabilityDeferred      FunctionalObservability = "deferred"
	FunctionalObservabilityNotApplicable FunctionalObservability = "not_applicable"
)

// ManifestVersion 是 Doctor 清单自己的格式版本，与服务目录的版本无关（目录改写调用权限不影响 doctor 段）。
const ManifestVersion = 1

type Manifest struct {
	Version    int         `yaml:"version" json:"version"`
	Components []Component `yaml:"components" json:"components"`
	Checksum   string      `yaml:"-" json:"checksum"`
}

type Component struct {
	ComponentID              string                  `yaml:"component_id" json:"component_id"`
	ServiceName              string                  `yaml:"service_name" json:"service_name"`
	Role                     string                  `yaml:"role" json:"role"`
	Description              string                  `yaml:"description" json:"description"`
	Duties                   []string                `yaml:"duties" json:"duties"`
	Inputs                   []string                `yaml:"inputs" json:"inputs"`
	Outputs                  []string                `yaml:"outputs" json:"outputs"`
	Dependencies             []string                `yaml:"dependencies" json:"dependencies"`
	Transport                Transport               `yaml:"transport" json:"transport"`
	FunctionalObservability  FunctionalObservability `yaml:"functional_observability" json:"functional_observability"`
	HealthPort               int                     `yaml:"health_port" json:"health_port"` // 只听本机的健康端口，doctor 在目标机上请求它
	ConfigPaths              []string                `yaml:"config_paths" json:"config_paths"`
	WritablePaths            []string                `yaml:"writable_paths" json:"writable_paths"`
	RecoveryActionIDs        []string                `yaml:"recovery_action_ids" json:"recovery_action_ids"`
	RequiredInDefaultProfile bool                    `yaml:"required_in_default_profile" json:"required_in_default_profile"`
}

var allowedRecoveryActions = map[string]bool{
	"sync_host_deployments":         true,
	"verify_service_identity":       true,
	"repair_path_permissions":       true,
	"verify_eventbus_credentials":   true,
	"restart_service_manually":      true,
	"inspect_health_check_input":    true,
	"replay_factor_window_manually": true,
	"free_disk_space":               true,
	"run_bootstrap":                 true,
}

func LoadEmbeddedManifest() (Manifest, error) {
	return loadManifest(servicecatalog.EmbeddedYAML(), "embedded component catalog")
}

// LoadManifestFile validates the release copy of the manifest and computes its
// checksum from the exact bytes on disk.
func LoadManifestFile(filename string) (Manifest, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return Manifest{}, fmt.Errorf("read doctor manifest: %w", err)
	}
	if len(raw) > 2<<20 {
		return Manifest{}, fmt.Errorf("doctor manifest exceeds 2 MiB")
	}
	return loadManifest(raw, filename)
}

func loadManifest(raw []byte, source string) (Manifest, error) {
	catalog, err := servicecatalog.Decode(bytes.NewReader(raw))
	if err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", source, err)
	}
	manifest := Manifest{Version: ManifestVersion, Components: make([]Component, 0, len(catalog.Components))}
	for _, component := range catalog.Components {
		// 第三方程序（内置 SeaweedFS）没有健康端口、不上报，不在诊断范围内。
		if component.ThirdParty {
			continue
		}
		metadata := component.Doctor
		manifest.Components = append(manifest.Components, Component{
			ComponentID: component.ID, ServiceName: component.ID,
			Role: metadata.Role, Description: metadata.Description,
			Duties: metadata.Duties, Inputs: metadata.Inputs, Outputs: metadata.Outputs,
			Dependencies: metadata.Dependencies, Transport: Transport(metadata.Transport),
			FunctionalObservability: FunctionalObservability(metadata.FunctionalObservability),
			HealthPort:              component.Health.Port, ConfigPaths: metadata.ConfigPaths, WritablePaths: metadata.WritablePaths,
			RecoveryActionIDs: metadata.RecoveryActionIDs, RequiredInDefaultProfile: metadata.RequiredInDefaultProfile,
		})
	}
	sum := sha256.Sum256(raw)
	manifest.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate %s: %w", source, err)
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported manifest version %d", m.Version)
	}
	if len(m.Components) == 0 {
		return errors.New("manifest components are required")
	}
	if len(m.Components) > MaxManifestComponents {
		return fmt.Errorf("manifest has %d components, limit is %d", len(m.Components), MaxManifestComponents)
	}
	componentIDs := make(map[string]bool, len(m.Components))
	serviceNames := make(map[string]bool, len(m.Components))
	for i, component := range m.Components {
		if err := component.validate(); err != nil {
			return fmt.Errorf("component %d: %w", i, err)
		}
		if componentIDs[component.ComponentID] {
			return fmt.Errorf("duplicate component_id %q", component.ComponentID)
		}
		componentIDs[component.ComponentID] = true
		if serviceNames[component.ServiceName] {
			return fmt.Errorf("duplicate service_name %q", component.ServiceName)
		}
		serviceNames[component.ServiceName] = true
	}
	for _, component := range m.Components {
		for _, dependency := range component.Dependencies {
			if dependency == component.ComponentID {
				return fmt.Errorf("component %q depends on itself", component.ComponentID)
			}
			if !componentIDs[dependency] {
				return fmt.Errorf("component %q depends on unknown component %q", component.ComponentID, dependency)
			}
		}
	}
	return nil
}

func (c Component) validate() error {
	if strings.TrimSpace(c.ComponentID) == "" || strings.TrimSpace(c.ServiceName) == "" {
		return errors.New("component_id and service_name are required")
	}
	if strings.TrimSpace(c.Role) == "" || strings.TrimSpace(c.Description) == "" {
		return errors.New("role and description are required")
	}
	if forbiddenServiceName(c.ServiceName) {
		return fmt.Errorf("service_name %q is not an independently deployed process", c.ServiceName)
	}
	switch c.Transport {
	case TransportReporter, TransportHostSnapshot, TransportDelegated:
	default:
		return fmt.Errorf("invalid transport %q", c.Transport)
	}
	switch c.FunctionalObservability {
	case FunctionalObservabilityActive, FunctionalObservabilityDeferred, FunctionalObservabilityNotApplicable:
	default:
		return fmt.Errorf("invalid functional_observability %q", c.FunctionalObservability)
	}
	if strings.HasPrefix(c.ServiceName, "storage-") && c.FunctionalObservability != FunctionalObservabilityDeferred {
		return fmt.Errorf("storage service %q must remain deferred", c.ServiceName)
	}
	if c.HealthPort <= 0 || c.HealthPort > 65535 {
		return fmt.Errorf("invalid health_port %d", c.HealthPort)
	}
	for _, candidate := range append(append([]string{}, c.ConfigPaths...), c.WritablePaths...) {
		if err := validateRelativePath(candidate); err != nil {
			return fmt.Errorf("path %q: %w", candidate, err)
		}
	}
	if len(c.RecoveryActionIDs) == 0 {
		return errors.New("at least one recovery_action_id is required")
	}
	for _, action := range c.RecoveryActionIDs {
		if !allowedRecoveryActions[action] {
			return fmt.Errorf("unknown recovery_action_id %q", action)
		}
	}
	return nil
}

func forbiddenServiceName(serviceName string) bool {
	if serviceName == "trade_account" || serviceName == "trade_order" {
		return true
	}
	if strings.HasSuffix(serviceName, "_timer") || strings.HasSuffix(serviceName, "-timer") {
		return true
	}
	return strings.HasPrefix(serviceName, "storage-") && strings.HasSuffix(serviceName, "-rpc")
}

func validateRelativePath(candidate string) error {
	if candidate == "" {
		return errors.New("empty path")
	}
	if strings.HasPrefix(candidate, "/") || path.Clean(candidate) != candidate || candidate == "." || strings.HasPrefix(candidate, "../") {
		return errors.New("path must be a clean release-relative path")
	}
	if strings.ContainsAny(candidate, "*?[]{}$`\\") {
		return errors.New("path templates, globbing, shell expansion, and backslashes are not allowed")
	}
	return nil
}
