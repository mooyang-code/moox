package doctor

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEmbeddedManifestIsValid(t *testing.T) {
	t.Parallel()

	manifest, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatalf("load embedded manifest: %v", err)
	}
	if len(manifest.Components) == 0 {
		t.Fatal("embedded manifest is empty")
	}
	for _, component := range manifest.Components {
		if strings.HasPrefix(component.ServiceName, "storage-") && component.FunctionalObservability != FunctionalObservabilityDeferred {
			t.Fatalf("storage component %q observability = %q", component.ComponentID, component.FunctionalObservability)
		}
	}
}

func TestEmbeddedManifestMatchesDefaultSeed(t *testing.T) {
	t.Parallel()

	manifest, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../config/setup/service-deployments.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var seed struct {
		Services []struct {
			Name           string `yaml:"name"`
			DeploymentMode string `yaml:"deployment_mode"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &seed); err != nil {
		t.Fatal(err)
	}
	manifestServices := make(map[string]Component, len(manifest.Components))
	for _, component := range manifest.Components {
		manifestServices[component.ServiceName] = component
	}
	seedProcesses := map[string]bool{}
	for _, service := range seed.Services {
		if service.DeploymentMode != "process" {
			continue
		}
		seedProcesses[service.Name] = true
		if _, ok := manifestServices[service.Name]; !ok {
			t.Errorf("seed process %q has no manifest component", service.Name)
		}
	}
	for _, component := range manifest.Components {
		if component.RequiredInDefaultProfile && !seedProcesses[component.ServiceName] {
			t.Errorf("required manifest service %q is missing from seed", component.ServiceName)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	t.Parallel()

	base := Component{
		ComponentID:             "moox_monitor",
		ServiceName:             "moox_monitor",
		Role:                    "monitor",
		Description:             "monitor",
		Transport:               TransportReporter,
		FunctionalObservability: FunctionalObservabilityActive,
		HealthPath:              "/readyz",
		RecoveryActionIDs:       []string{"restart_service_manually"},
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "duplicate component", mutate: func(m *Manifest) { m.Components = append(m.Components, m.Components[0]) }},
		{name: "endpoint service", mutate: func(m *Manifest) { m.Components[0].ServiceName = "trade_order" }},
		{name: "storage internal endpoint", mutate: func(m *Manifest) { m.Components[0].ServiceName = "storage-primary-rpc" }},
		{name: "timer service", mutate: func(m *Manifest) { m.Components[0].ServiceName = "collector_timer" }},
		{name: "unsafe path", mutate: func(m *Manifest) { m.Components[0].ConfigPaths = []string{"../secret"} }},
		{name: "unknown dependency", mutate: func(m *Manifest) { m.Components[0].Dependencies = []string{"missing"} }},
		{name: "unknown recovery", mutate: func(m *Manifest) { m.Components[0].RecoveryActionIDs = []string{"shell_anything"} }},
		{name: "storage active", mutate: func(m *Manifest) {
			m.Components[0].ServiceName = "storage-primary"
			m.Components[0].FunctionalObservability = FunctionalObservabilityActive
		}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			manifest := Manifest{Version: 1, Components: []Component{base}}
			tt.mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestManifestChecksumStable(t *testing.T) {
	t.Parallel()

	one, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	two, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	if one.Checksum == "" || one.Checksum != two.Checksum {
		t.Fatalf("checksums %q and %q", one.Checksum, two.Checksum)
	}
}
