package doctor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
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

func TestEmbeddedManifestMatchesSharedCatalog(t *testing.T) {
	t.Parallel()

	manifest, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Components) != len(catalog.Components) {
		t.Fatalf("Doctor components %d, catalog components %d", len(manifest.Components), len(catalog.Components))
	}
	manifestServices := make(map[string]Component, len(manifest.Components))
	for _, component := range manifest.Components {
		manifestServices[component.ServiceName] = component
	}
	for _, component := range catalog.Components {
		got, ok := manifestServices[component.ID]
		if !ok || got.ComponentID != component.ID {
			t.Fatalf("canonical component %q is missing", component.ID)
		}
		if got.Role != component.Doctor.Role || got.RequiredInDefaultProfile != component.Doctor.RequiredInDefaultProfile ||
			string(got.Transport) != component.Doctor.Transport || string(got.FunctionalObservability) != component.Doctor.FunctionalObservability ||
			!reflect.DeepEqual(got.Dependencies, component.Doctor.Dependencies) || !reflect.DeepEqual(got.ConfigPaths, component.Doctor.ConfigPaths) ||
			!reflect.DeepEqual(got.WritablePaths, component.Doctor.WritablePaths) || !reflect.DeepEqual(got.RecoveryActionIDs, component.Doctor.RecoveryActionIDs) {
			t.Errorf("Doctor metadata differs from catalog for %s", component.ID)
		}
	}
	if got := manifestServices["console-proxy"]; got.HealthPath != "/readyz" || got.Transport != TransportHealthOnly {
		t.Fatalf("console proxy health contract: %+v", got)
	}
}

func TestReleaseCatalogChecksumAndStrictValidation(t *testing.T) {
	manifest, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "catalog.yaml")
	raw := servicecatalog.EmbeddedYAML()
	for _, tc := range []struct {
		name         string
		raw          []byte
		valid        bool
		sameChecksum bool
	}{
		{name: "exact release copy", raw: raw, valid: true, sameChecksum: true},
		{name: "changed bytes", raw: append([]byte("# release change\n"), raw...), valid: true},
		{name: "unknown fields", raw: append(append([]byte{}, raw...), []byte("unknown: true\n")...)},
		{name: "multiple documents", raw: append(append([]byte{}, raw...), []byte("\n---\nversion: 1\n")...)},
		{name: "oversize", raw: []byte(strings.Repeat(" ", (2<<20)+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filename, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadManifestFile(filename)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
			if tc.valid && (got.Checksum == manifest.Checksum) != tc.sameChecksum {
				t.Fatalf("unexpected checksum %s", got.Checksum)
			}
		})
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
