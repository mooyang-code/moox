package cmd

import "testing"

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
