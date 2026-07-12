package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateReservedInternalSpacesAllowsTopologyReferenceSeed(t *testing.T) {
	seed := metadataSeed{PrimaryStoreRoutes: []seedPrimaryStoreRoute{{SpaceID: "moox_system", DatasetID: "moox_service_metrics", SubjectPattern: "*", HashRule: "subject_id"}}}
	if err := validateReservedInternalSpaces(seed); err != nil {
		t.Fatalf("route-only seed should reference an existing reserved space: %v", err)
	}
}

func TestValidateReservedInternalSpacesRejectsUndeclaredLogicalResource(t *testing.T) {
	seed := metadataSeed{Datasets: []seedDataset{{SpaceID: "moox_system", DatasetID: "moox_service_metrics"}}}
	if err := validateReservedInternalSpaces(seed); err == nil {
		t.Fatal("logical resource without an internal space declaration should be rejected")
	}
}

func TestLoadMetadataSeed_ParsesMinimalYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.yaml")
	content := "spaces:\n  - space_id: default\n    attributes: {scope: public}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	seed, err := loadMetadataSeed(path)
	if err != nil || len(seed.Spaces) != 1 || seed.Spaces[0].SpaceID != "default" {
		t.Fatalf("seed=%+v err=%v", seed, err)
	}
}

func TestBuildMetadataImportCalls_AcceptsEmptySeed(t *testing.T) {
	calls, err := buildMetadataImportCalls(metadataSeed{})
	if err != nil || len(calls) != 0 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
}
