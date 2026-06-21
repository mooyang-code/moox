package cmd

import (
	"path/filepath"
	"testing"
)

func TestLoadMetadataSeedUsesDomainRecords(t *testing.T) {
	seedPath := filepath.Join("..", "..", "storage", "config", "metadata.seed.yaml")
	seed, err := loadMetadataSeed(seedPath)
	if err != nil {
		t.Fatalf("loadMetadataSeed returned error: %v", err)
	}

	if len(seed.Spaces) != 1 || seed.Spaces[0].SpaceID != "crypto" {
		t.Fatalf("spaces = %+v, want crypto seed space", seed.Spaces)
	}
	if len(seed.Datasets) < 2 {
		t.Fatalf("datasets = %d, want kline and symbols datasets", len(seed.Datasets))
	}
	if len(seed.DatasetSubjects) == 0 {
		t.Fatalf("seed must bind datasets to subjects")
	}
	if len(seed.PrimaryStoreRoutes) == 0 {
		t.Fatalf("seed must include storage routes")
	}
	if len(seed.ViewColumns) == 0 {
		t.Fatalf("seed must include view columns")
	}
}

func TestBuildMetadataImportCallsOrdersDependencies(t *testing.T) {
	seed := metadataSeed{
		Spaces:             []seedSpace{{SpaceID: "crypto", Name: "crypto"}},
		DataSources:        []seedDataSource{{SpaceID: "crypto", DataSourceID: "binance", Name: "Binance"}},
		Subjects:           []seedSubject{{SpaceID: "crypto", SubjectID: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT"}},
		Datasets:           []seedDataset{{SpaceID: "crypto", DatasetID: "kline", DataSourceID: "binance", Name: "kline", DataKind: "time_series"}},
		DatasetSubjects:    []seedDatasetSubject{{SpaceID: "crypto", DatasetID: "kline", SubjectID: "APT-USDT"}},
		Fields:             []seedField{{SpaceID: "crypto", FieldID: "close", Name: "close", ValueType: "double"}},
		DatasetColumns:     []seedDatasetColumn{{SpaceID: "crypto", DatasetID: "kline", ColumnName: "close", OriginType: "field", OriginID: "close", ValueType: "double"}},
		PrimaryStoreNodes:  []seedPrimaryStoreNode{{NodeID: "local", Name: "local", Endpoint: "local"}},
		Devices:            []seedDevice{{DeviceID: "pebble", NodeID: "local", Name: "pebble", Engine: "pebble", Endpoint: "./pebble"}},
		PrimaryStoreRoutes: []seedPrimaryStoreRoute{{SpaceID: "crypto", RouteID: "route-kline", DatasetID: "kline", SubjectPattern: "*", NodeID: "local"}},
		Views:              []seedView{{SpaceID: "crypto", ViewID: "close_view", Name: "close view", PrimaryDatasetID: "kline", DatasetIDs: []string{"kline"}}},
		ViewColumns:        []seedViewColumn{{SpaceID: "crypto", ViewID: "close_view", ColumnName: "close", OriginType: "dataset_column", OriginID: "kline.close", ValueType: "double"}},
	}

	calls, err := buildMetadataImportCalls(seed)
	if err != nil {
		t.Fatalf("buildMetadataImportCalls returned error: %v", err)
	}
	var methods []string
	for _, call := range calls {
		methods = append(methods, call.Method)
	}
	want := []string{
		"CreateSpace",
		"CreateDataSource",
		"UpsertSubject",
		"CreateDataset",
		"BindDatasetSubject",
		"CreateField",
		"UpsertDatasetColumn",
		"CreatePrimaryStoreNode",
		"CreateDevice",
		"CreatePrimaryStoreRoute",
		"CreateView",
		"UpsertViewColumn",
	}
	if len(methods) != len(want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("methods = %v, want %v", methods, want)
		}
	}
}

func TestMetadataImportCommandExposesServiceFlags(t *testing.T) {
	metadataCmd, _, err := rootCmd.Find([]string{"metadata"})
	if err != nil || metadataCmd == nil {
		t.Fatalf("metadata command not registered: %v", err)
	}
	importCmd, _, err := rootCmd.Find([]string{"metadata", "import"})
	if err != nil || importCmd == nil {
		t.Fatalf("metadata import command not registered: %v", err)
	}
	for _, name := range []string{"file", "metadata-url", "dry-run", "if-not-exists"} {
		if flag := importCmd.Flags().Lookup(name); flag == nil {
			t.Fatalf("metadata import missing --%s", name)
		}
	}
}
