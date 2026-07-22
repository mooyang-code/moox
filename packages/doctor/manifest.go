package doctor

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxManifestComponents = 64

type Transport string

const (
	TransportReporter     Transport = "reporter"
	TransportHostSnapshot Transport = "host_snapshot"
	TransportHealthOnly   Transport = "health_only"
)

type FunctionalObservability string

const (
	FunctionalObservabilityActive        FunctionalObservability = "active"
	FunctionalObservabilityDeferred      FunctionalObservability = "deferred"
	FunctionalObservabilityNotApplicable FunctionalObservability = "not_applicable"
)

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
	HealthPath               string                  `yaml:"health_path" json:"health_path"`
	ConfigPaths              []string                `yaml:"config_paths" json:"config_paths"`
	WritablePaths            []string                `yaml:"writable_paths" json:"writable_paths"`
	RecoveryActionIDs        []string                `yaml:"recovery_action_ids" json:"recovery_action_ids"`
	RequiredInDefaultProfile bool                    `yaml:"required_in_default_profile" json:"required_in_default_profile"`
}

var allowedRecoveryActions = map[string]bool{
	"apply_service_deployments_seed": true,
	"verify_service_identity":        true,
	"repair_path_permissions":        true,
	"verify_eventbus_credentials":    true,
	"restart_service_manually":       true,
	"inspect_pipeline_input":         true,
	"replay_factor_window_manually":  true,
	"free_disk_space":                true,
	"run_bootstrap":                  true,
}

//go:embed components.yaml
var embeddedManifest []byte

func LoadEmbeddedManifest() (Manifest, error) {
	return loadManifest(embeddedManifest, "embedded doctor manifest")
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
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", source, err)
	}
	sum := sha256.Sum256(raw)
	manifest.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate %s: %w", source, err)
	}
	return manifest, nil
}

func EmbeddedManifest() []byte {
	return append([]byte(nil), embeddedManifest...)
}

func (m Manifest) Validate() error {
	if m.Version != 1 {
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
	case TransportReporter, TransportHostSnapshot, TransportHealthOnly:
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
	if c.HealthPath != "/readyz" && c.HealthPath != "/healthz" {
		return fmt.Errorf("unsupported health_path %q", c.HealthPath)
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
