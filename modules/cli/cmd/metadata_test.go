package cmd

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
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

func TestLoadMetadataSeedRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.yaml")
	if err := os.WriteFile(path, []byte("spaces:\n  - space_id: stock_cn\n    name: CN\n    typo: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMetadataSeed(path); err == nil {
		t.Fatal("unknown seed field was accepted")
	}
}

func TestMetadataContractsCompareAttributesAndLogicalFields(t *testing.T) {
	expected := &pb.Dataset{SpaceId: "stock_cn", DatasetId: "equity_kline", DataSourceId: "internal", Name: "股票K线", Description: "canonical", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1d"}, Status: "active", Attributes: map[string]string{"dataset_role": "unified_data", "feed": "kline"}}
	actual := expected.ProtoReflect().New().Interface().(*pb.Dataset)
	*actual = *expected
	actual.CreatedAt = "2026-01-01T00:00:00Z"
	actual.UpdatedAt = "2026-01-02T00:00:00Z"
	if !metadataContractsEqual("datasets", expected, actual) {
		t.Fatal("timestamps should not change logical contract")
	}
	actual.Attributes = map[string]string{"dataset_role": "provider_data", "feed": "kline"}
	if metadataContractsEqual("datasets", expected, actual) {
		t.Fatal("attribute mutation should change logical contract")
	}
}

func TestValidateMarketSeedRequiresRoleAndFeed(t *testing.T) {
	seed := metadataSeed{
		Spaces:   []seedSpace{{SpaceID: "stock_cn"}},
		Datasets: []seedDataset{{SpaceID: "stock_cn", DatasetID: "equity_kline", DataKind: "time_series"}},
	}
	if err := validateMarketSeed(seed); err == nil {
		t.Fatal("market dataset without role/feed was accepted")
	}
}
