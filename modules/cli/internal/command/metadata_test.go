package command

import (
	"context"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReservedInternalSpacesRejectsUndeclaredLogicalResource(t *testing.T) {
	seed := metadataSeed{Datasets: []seedDataset{{SpaceID: "mooxsys", DatasetID: "dataset_mooxsys_service_metrics"}}}
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

func TestLoadMetadataSeedRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.yaml")
	require.NoError(t, os.WriteFile(path, []byte("spaces:\n- space_id: crypto\n  unknown_field: true\n"), 0o600))

	_, err := loadMetadataSeed(path)
	require.ErrorContains(t, err, "unknown_field")
}

func TestLoadMetadataSeedRejectsSecondDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.yaml")
	require.NoError(t, os.WriteFile(path, []byte("spaces: []\n---\nspaces: []\n"), 0o600))

	_, err := loadMetadataSeed(path)
	require.ErrorContains(t, err, "exactly one YAML document")
}

func TestBusinessSpacesExcludeInternalScopeAndSort(t *testing.T) {
	seed := metadataSeed{Spaces: []seedSpace{
		{
			SpaceID: "stockcn", Name: "A股市场", Market: "CN", Timezone: "Asia/Shanghai",
			seedCommon: seedCommon{Status: "active", Attributes: map[string]string{"managed_by": "moox-cli"}},
		},
		{
			SpaceID: "mooxsys", Name: "MooX System",
			seedCommon: seedCommon{Attributes: map[string]string{"scope": "internal"}},
		},
		{
			SpaceID: "crypto", Name: "加密货币市场", Market: "crypto", Timezone: "UTC",
		},
	}}

	spaces, err := businessSetupSpaces(seed)
	require.NoError(t, err)
	require.Len(t, spaces, 2)
	assert.Equal(t, "crypto", spaces[0].SpaceID)
	assert.Equal(t, "stockcn", spaces[1].SpaceID)
	assert.Equal(t, "CN", spaces[1].Market)
	assert.Equal(t, "Asia/Shanghai", spaces[1].Timezone)
	assert.Equal(t, `{"managed_by":"moox-cli"}`, spaces[1].AttributesJSON)
}

func TestBusinessSpacesRejectDuplicateID(t *testing.T) {
	_, err := businessSetupSpaces(metadataSeed{Spaces: []seedSpace{
		{SpaceID: "crypto", Name: "Crypto", Market: "crypto", Timezone: "UTC"},
		{SpaceID: "crypto", Name: "Crypto 2", Market: "crypto", Timezone: "UTC"},
	}})
	require.ErrorContains(t, err, "duplicate metadata space")
}

func TestSelectMetadataSpacesKeepsSelectedDependencyClosure(t *testing.T) {
	t.Parallel()
	seed := metadataSeed{
		Spaces:         []seedSpace{{SpaceID: "stockcn", Name: "A股市场"}, {SpaceID: "crypto", Name: "加密货币市场"}},
		DataSources:    []seedDataSource{{SpaceID: "stockcn", DataSourceID: "stock"}, {SpaceID: "crypto", DataSourceID: "binance"}},
		FieldGroups:    []seedFieldGroup{{SpaceID: "stockcn", GroupID: "quote"}, {SpaceID: "crypto", GroupID: "quote"}},
		Fields:         []seedField{{SpaceID: "stockcn", FieldID: "close", GroupID: "quote"}, {SpaceID: "crypto", FieldID: "close", GroupID: "quote"}},
		Datasets:       []seedDataset{{SpaceID: "stockcn", DatasetID: "kline"}, {SpaceID: "crypto", DatasetID: "kline"}},
		DatasetColumns: []seedDatasetColumn{{SpaceID: "stockcn", DatasetID: "kline", ColumnName: "close"}, {SpaceID: "crypto", DatasetID: "kline", ColumnName: "close"}},
		Views:          []seedView{{SpaceID: "stockcn", ViewID: "kline"}, {SpaceID: "crypto", ViewID: "kline"}},
		ViewColumns:    []seedViewColumn{{SpaceID: "stockcn", ViewID: "kline", ColumnName: "close"}, {SpaceID: "crypto", ViewID: "kline", ColumnName: "close"}},
		Devices:        []seedDevice{{DeviceID: "duckdb"}},
	}

	selected, err := selectMetadataSpaces(seed, []string{" A股市场 "})
	require.NoError(t, err)
	require.Len(t, selected.Spaces, 1)
	require.Equal(t, "stockcn", selected.Spaces[0].SpaceID)
	require.Len(t, selected.DataSources, 1)
	require.Len(t, selected.FieldGroups, 1)
	require.Len(t, selected.Fields, 1)
	require.Len(t, selected.Datasets, 1)
	require.Len(t, selected.DatasetColumns, 1)
	require.Len(t, selected.Views, 1)
	require.Len(t, selected.ViewColumns, 1)
	require.Len(t, selected.Devices, 1, "global devices are preserved")
}

func TestSelectMetadataSpacesRejectsUnknownAndDuplicateSelection(t *testing.T) {
	t.Parallel()
	seed := metadataSeed{Spaces: []seedSpace{{SpaceID: "stockcn", Name: "A股市场"}}}
	_, err := selectMetadataSpaces(seed, []string{"stockus"})
	require.EqualError(t, err, `unknown metadata space "stockus"`)
	_, err = selectMetadataSpaces(seed, []string{"stockcn", "A股市场"})
	require.EqualError(t, err, `duplicate metadata space "stockcn"`)
}

func TestMetadataSpaceCatalogIsStableAndSanitized(t *testing.T) {
	t.Parallel()
	seed := metadataSeed{Spaces: []seedSpace{
		{SpaceID: "stockcn", Name: "A股市场", Description: "A股数据"},
		{SpaceID: "crypto", Name: "加密货币市场", Description: "多交易所加密货币数据"},
	}}
	require.Equal(t, []metadataSpaceChoice{
		{SpaceID: "stockcn", Name: "A股市场", Description: "A股数据"},
		{SpaceID: "crypto", Name: "加密货币市场", Description: "多交易所加密货币数据"},
	}, metadataSpaceCatalog(seed))
}

func TestBuildMetadataImportCalls_AcceptsEmptySeed(t *testing.T) {
	calls, err := buildMetadataImportCalls(metadataSeed{})
	if err != nil || len(calls) != 0 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
}

func TestBuildMetadataImportCallsRequiresDatasetBindingAndRetention(t *testing.T) {
	_, err := buildMetadataImportCalls(metadataSeed{Datasets: []seedDataset{{DatasetID: "missing-node", KeepDuration: "1h"}}})
	require.ErrorContains(t, err, "data_node_id is required")
	_, err = buildMetadataImportCalls(metadataSeed{Datasets: []seedDataset{{DatasetID: "missing-retention", DataNodeID: "storage-node-0"}}})
	require.ErrorContains(t, err, "keep_duration is required")
}

func TestBuildMetadataImportCallsCanonicalizesKeepDurations(t *testing.T) {
	calls, err := buildMetadataImportCalls(metadataSeed{
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "binance",
			DataKind: "TIME_SERIES", DataNodeID: "storage-node-0", KeepDuration: "4320h",
			Freqs: []string{"1H"},
		}},
		Views: []seedView{{
			SpaceID: "crypto", ViewID: "kline_view", PrimaryDatasetID: "kline",
			DatasetIDs: []string{"kline"}, FilterJSON: `{"freq":"1H"}`, KeepDuration: "4320h",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "4320h0m0s", calls[0].Request.(*pb.CreateDatasetReq).GetDataset().GetKeepDuration())
	require.Equal(t, "4320h0m0s", calls[1].Request.(*pb.CreateViewReq).GetView().GetKeepDuration())
}

func TestSeedDatasetToPBAlwaysStartsDisabled(t *testing.T) {
	dataset, err := (seedDataset{
		SpaceID: "crypto", DatasetID: "kline", DataSourceID: "binance", Name: "Kline",
		DataKind: "TIME_SERIES", DataNodeID: " storage-node-0 ", KeepDuration: " 1h ",
		seedCommon: seedCommon{Status: "active"},
	}).toPB()
	require.NoError(t, err)
	assert.Equal(t, "storage-node-0", dataset.GetDataNodeId())
	assert.Equal(t, "1h0m0s", dataset.GetKeepDuration())
	assert.Equal(t, "disabled", dataset.GetStatus())
}

func TestBuildMetadataImportCallsCanonicalizesViewAsStorage(t *testing.T) {
	calls, err := buildMetadataImportCalls(metadataSeed{
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "market",
			DataKind: "TIME_SERIES", DataNodeID: "storage-node-0", KeepDuration: "1h",
			Freqs: []string{"1H"},
		}},
		Views: []seedView{{
			SpaceID: "crypto", ViewID: "kline_view", DatasetIDs: []string{"kline", "kline"},
			GrainKeys: []string{"wrong"}, FilterJSON: `{ "freq": "1H" }`, Engine: "pebble",
			KeepDuration: "1h",
		}},
	})
	require.NoError(t, err)
	view := calls[1].Request.(*pb.CreateViewReq).GetView()
	require.Equal(t, "kline", view.GetPrimaryDatasetId())
	require.Equal(t, []string{"kline"}, view.GetDatasetIds())
	require.Equal(t, []string{"subject_id", "freq", "data_time", "series_tag"}, view.GetGrainKeys())
	require.Equal(t, `{"freq":"1H"}`, view.GetFilterJson())
	require.Equal(t, "duckdb", view.GetEngine())
	require.Equal(t, "1h0m0s", view.GetKeepDuration())
}

func TestParseDataKind(t *testing.T) {
	kind, err := parseDataKind("TIME_SERIES")
	require.NoError(t, err)
	assert.Equal(t, pb.DataKind_DATA_KIND_TIME_SERIES, kind)
	_, err = parseDataKind("UNKNOWN")
	require.Error(t, err)
}

func TestParseFieldValueType(t *testing.T) {
	typ, err := parseFieldValueType("DOUBLE")
	require.NoError(t, err)
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, typ)
	_, err = parseFieldValueType("BAD")
	require.Error(t, err)
}

func TestParseDatasetColumnOriginType(t *testing.T) {
	typ, err := parseDatasetColumnOriginType("FIELD")
	require.NoError(t, err)
	assert.Equal(t, pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, typ)
}

func TestParseColumnOriginType(t *testing.T) {
	typ, err := parseColumnOriginType("SYSTEM")
	require.NoError(t, err)
	assert.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM, typ)
	typ, err = parseColumnOriginType("DATASET_COLUMN")
	require.NoError(t, err)
	assert.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, typ)
}

func TestNormalizeEnum(t *testing.T) {
	assert.Equal(t, "TIME_SERIES", normalizeEnum("data_kind_time_series"))
	assert.Equal(t, "DOUBLE", normalizeEnum("FIELD_VALUE_TYPE_DOUBLE"))
	assert.Equal(t, "FIELD_VALUE_TYPE_DOUBLE", normalizeEnum("field-value-type-double"))
}

func TestParseColumnAndValueTypes(t *testing.T) {
	origin, value, err := parseColumnAndValueTypes("DATASET_COLUMN", "DOUBLE")
	require.NoError(t, err)
	assert.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, origin)
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, value)
}

func TestHasInternalSpace(t *testing.T) {
	seed := metadataSeed{Spaces: []seedSpace{{SpaceID: "mooxsys", seedCommon: seedCommon{Attributes: map[string]string{"scope": "internal"}}}}}
	assert.True(t, hasInternalSpace(seed, "mooxsys"))
	assert.False(t, hasInternalSpace(seed, "default"))
}

func TestValidateReservedInternalSpacesAcceptsDeclaredSpace(t *testing.T) {
	seed := metadataSeed{Spaces: []seedSpace{{
		SpaceID: "mooxsys",
		seedCommon: seedCommon{Attributes: map[string]string{
			"scope": "internal", "owner_module": "monitor", "managed_by": "platform",
		}},
	}}}
	require.NoError(t, validateReservedInternalSpaces(seed))
}

func TestBuildMetadataImportCallsFullSeed(t *testing.T) {
	seed := metadataSeed{
		Spaces:          []seedSpace{{SpaceID: "crypto", Name: "Crypto"}},
		DataSources:     []seedDataSource{{SpaceID: "crypto", DataSourceID: "binance", Name: "Binance", Kind: "exchange"}},
		Subjects:        []seedSubject{{SpaceID: "crypto", SubjectID: "BTC", Name: "Bitcoin"}},
		SubjectSymbols:  []seedSubjectSymbol{{SpaceID: "crypto", SubjectID: "BTC", DataSourceID: "binance", ExternalSymbol: "BTCUSDT"}},
		Datasets:        []seedDataset{{SpaceID: "crypto", DatasetID: "kline", DataSourceID: "binance", DataKind: "TIME_SERIES", DataNodeID: "storage-node-0", KeepDuration: "1h", Freqs: []string{"1m"}}},
		DatasetSubjects: []seedDatasetSubject{{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC"}},
		Fields:          []seedField{{SpaceID: "crypto", FieldID: "close", ValueType: "DOUBLE"}},
		Factors:         []seedFactor{{SpaceID: "crypto", FactorID: "ma", ValueType: "DOUBLE"}},
		DatasetColumns:  []seedDatasetColumn{{SpaceID: "crypto", DatasetID: "kline", ColumnName: "close", OriginType: "FIELD", ValueType: "DOUBLE"}},
		Views: []seedView{{
			SpaceID: "crypto", ViewID: "v1", Name: "View", PrimaryDatasetID: "kline",
			DatasetIDs: []string{"kline"}, GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"},
			FilterJSON: `{"freq":"1m"}`, Engine: "duckdb",
		}},
		ViewColumns: []seedViewColumn{{SpaceID: "crypto", ViewID: "v1", ColumnName: "close", OriginType: "DATASET_COLUMN", ValueType: "DOUBLE"}},
		Devices:     []seedDevice{{DeviceID: "dev-1", Name: "Device"}},
	}
	calls, err := buildMetadataImportCalls(seed)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(calls), 13)
}

func TestBuildMetadataImportCallsBackfillsColumnDisplayName(t *testing.T) {
	seed := metadataSeed{
		Fields: []seedField{{SpaceID: "crypto", FieldID: "close", Name: "收盘价", ValueType: "DOUBLE"}},
		DatasetColumns: []seedDatasetColumn{{
			SpaceID: "crypto", DatasetID: "kline", ColumnName: "close",
			OriginType: "FIELD", OriginID: "close", ValueType: "DOUBLE",
		}},
	}
	calls, err := buildMetadataImportCalls(seed)
	require.NoError(t, err)
	for _, call := range calls {
		if call.Resource != "dataset_columns" {
			continue
		}
		column := call.Request.(*pb.UpsertDatasetColumnReq).GetColumn()
		assert.Equal(t, "收盘价", column.GetAttributes()["display_name"])
		return
	}
	t.Fatal("dataset_columns call missing")
}

func TestBuildMetadataImportCallsScopesColumnDisplayNameBySpaceAndOriginType(t *testing.T) {
	seed := metadataSeed{
		Fields: []seedField{
			{SpaceID: "stockcn", FieldID: "close", Name: "股票收盘价", ValueType: "DOUBLE"},
			{SpaceID: "crypto", FieldID: "close", Name: "币种收盘价", ValueType: "DOUBLE"},
			{SpaceID: "crypto", FieldID: "alpha", Name: "普通指标", ValueType: "DOUBLE"},
		},
		Factors: []seedFactor{
			{SpaceID: "crypto", FactorID: "alpha", Name: "因子指标", ValueType: "DOUBLE"},
		},
		DatasetColumns: []seedDatasetColumn{
			{
				SpaceID: "stockcn", DatasetID: "stock", ColumnName: "close",
				OriginType: "FIELD", OriginID: "close", ValueType: "DOUBLE",
			},
			{
				SpaceID: "crypto", DatasetID: "spot", ColumnName: "close",
				OriginType: "FIELD", OriginID: "close", ValueType: "DOUBLE",
			},
			{
				SpaceID: "crypto", DatasetID: "spot", ColumnName: "alpha_field",
				OriginType: "FIELD", OriginID: "alpha", ValueType: "DOUBLE",
			},
			{
				SpaceID: "crypto", DatasetID: "spot", ColumnName: "alpha_factor",
				OriginType: "FACTOR", OriginID: "alpha", ValueType: "DOUBLE",
			},
		},
	}
	calls, err := buildMetadataImportCalls(seed)
	require.NoError(t, err)
	got := map[string]string{}
	for _, call := range calls {
		if call.Resource != "dataset_columns" {
			continue
		}
		column := call.Request.(*pb.UpsertDatasetColumnReq).GetColumn()
		got[column.GetSpaceId()+"/"+column.GetColumnName()] = column.GetAttributes()["display_name"]
	}
	require.Equal(t, map[string]string{
		"stockcn/close":       "股票收盘价",
		"crypto/close":        "币种收盘价",
		"crypto/alpha_field":  "普通指标",
		"crypto/alpha_factor": "因子指标",
	}, got)
}

func TestBuildMetadataImportCallsBackfillsInternalViewColumnDisplayName(t *testing.T) {
	seed := metadataSeed{ViewColumns: []seedViewColumn{{
		SpaceID: "mooxsys", ViewID: "view_mooxsys_host_resource", ColumnName: "cpu_usage_percent",
		OriginType: "DATASET_COLUMN", OriginID: "dataset_mooxsys_host_resource.cpu_usage_percent", ValueType: "DOUBLE",
	}}}
	calls, err := buildMetadataImportCalls(seed)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	column := calls[0].Request.(*pb.UpsertViewColumnReq).GetColumn()
	assert.Equal(t, "cpu_usage_percent", column.GetAttributes()["display_name"])
}

func TestMetadataContractsEqualAllResources(t *testing.T) {
	assert.True(t, metadataContractsEqual("data_sources",
		&pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Status: "active"},
		&pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Status: "active"},
	))
	assert.True(t, metadataContractsEqual("datasets",
		&pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"},
		&pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"},
	))
	assert.True(t, metadataContractsEqual("fields",
		&pb.Field{SpaceId: "crypto", FieldId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		&pb.Field{SpaceId: "crypto", FieldId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
	))
	assert.True(t, metadataContractsEqual("dataset_columns",
		&pb.DatasetColumn{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		&pb.DatasetColumn{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
	))
}

func TestMetadataContractsEqualTreatsNilAndEmptyAttributesAsEqual(t *testing.T) {
	assert.True(t, metadataContractsEqual("spaces",
		&pb.Space{SpaceId: "mooxsys", Name: "MooX System", Status: "active"},
		&pb.Space{SpaceId: "mooxsys", Name: "MooX System", Status: "active", Attributes: map[string]string{}},
	))
	assert.True(t, metadataContractsEqual("data_sources",
		&pb.DataSource{SpaceId: "mooxsys", DataSourceId: "moox_monitor", Name: "MooX Monitor", Status: "active"},
		&pb.DataSource{SpaceId: "mooxsys", DataSourceId: "moox_monitor", Name: "MooX Monitor", Status: "active", Attributes: map[string]string{}},
	))
}

func TestMetadataContractsEqualAcceptsActivatedLockedDataset(t *testing.T) {
	expected := &pb.Dataset{
		SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance",
		Name: "行情", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		DataNodeId: "storage-node-0", KeepDuration: "8760h",
		Freqs: []string{"1H"}, Status: "disabled",
	}
	actual := proto.Clone(expected).(*pb.Dataset)
	actual.Status = "active"
	actual.BindingLocked = true
	actual.Revision = 8
	assert.True(t, metadataContractsEqual("datasets", expected, actual))

	actual.BindingLocked = false
	assert.False(t, metadataContractsEqual("datasets", expected, actual))
}

func TestApplyProbeResultOtherResources(t *testing.T) {
	ds := &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance"}
	probe := &metadataExistsProbe{
		Method: "GetDataSource", Request: &pb.GetDataSourceReq{}, Response: &pb.GetDataSourceRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, DataSource: ds,
		},
	}
	found, actual := applyProbeResult("data_sources", probe, &pb.CreateDataSourceReq{DataSource: ds})
	assert.True(t, found)
	assert.Equal(t, ds, actual)
}

func TestVerifyMetadataResourceMoreTypes(t *testing.T) {
	ds := &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Status: "active"}
	require.NoError(t, verifyMetadataResource("data_sources", &pb.CreateDataSourceReq{DataSource: ds}, ds))
	require.NoError(t, verifyMetadataResource("data_sources", &pb.CreateDataSourceReq{DataSource: ds}, &pb.DataSource{
		SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Status: "active", Attributes: map[string]string{},
	}))
	col := &pb.DatasetColumn{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", Status: "active"}
	require.NoError(t, verifyMetadataResource("dataset_columns", &pb.UpsertDatasetColumnReq{Column: col}, col))
	err := verifyMetadataResource("spaces", &pb.UpsertSubjectReq{}, &pb.Space{})
	require.Error(t, err)
}

func TestParseEnumsAllValues(t *testing.T) {
	for _, tc := range []struct {
		input string
		kind  pb.DataKind
	}{
		{"RECORD", pb.DataKind_DATA_KIND_RECORD},
		{"TIME_SERIES", pb.DataKind_DATA_KIND_TIME_SERIES},
	} {
		got, err := parseDataKind(tc.input)
		require.NoError(t, err)
		assert.Equal(t, tc.kind, got)
	}
	for _, value := range []string{"SNAPSHOT", "EVENT", "DOCUMENT", "TABLE"} {
		_, err := parseDataKind(value)
		require.Error(t, err)
	}
	for _, tc := range []struct {
		input string
		typ   pb.FieldValueType
	}{
		{"STRING", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{"INT", pb.FieldValueType_FIELD_VALUE_TYPE_INT},
		{"BOOL", pb.FieldValueType_FIELD_VALUE_TYPE_BOOL},
		{"TIME", pb.FieldValueType_FIELD_VALUE_TYPE_TIME},
		{"JSON", pb.FieldValueType_FIELD_VALUE_TYPE_JSON},
		{"BYTES", pb.FieldValueType_FIELD_VALUE_TYPE_BYTES},
	} {
		got, err := parseFieldValueType(tc.input)
		require.NoError(t, err)
		assert.Equal(t, tc.typ, got)
	}
	origin, value, err := parseDatasetColumnAndValueTypes("FACTOR", "DOUBLE")
	require.NoError(t, err)
	assert.Equal(t, pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR, origin)
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, value)
	origin2, err := parseColumnOriginType("EXPRESSION")
	require.NoError(t, err)
	assert.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION, origin2)
}

func TestBuildMetadataImportCallsRejectsUndefinedExplicitFieldGroup(t *testing.T) {
	_, err := buildMetadataImportCalls(metadataSeed{Fields: []seedField{{
		SpaceID: "stockcn", GroupID: "missing", FieldID: "close", Name: "收盘价", ValueType: "double",
	}}})
	require.ErrorContains(t, err, `undefined field_group "missing"`)
}

func TestBuildMetadataImportCallsValidatesFieldGroupHierarchy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []seedFieldGroup
		want   string
	}{
		{name: "duplicate", groups: []seedFieldGroup{{SpaceID: "s", GroupID: "g", Name: "G"}, {SpaceID: "s", GroupID: "g", Name: "G2"}}, want: "duplicate field_group"},
		{name: "missing parent", groups: []seedFieldGroup{{SpaceID: "s", GroupID: "child", ParentGroupID: "missing", Name: "Child"}}, want: "undefined parent"},
		{name: "third level", groups: []seedFieldGroup{{SpaceID: "s", GroupID: "root", Name: "Root"}, {SpaceID: "s", GroupID: "child", ParentGroupID: "root", Name: "Child"}, {SpaceID: "s", GroupID: "leaf", ParentGroupID: "child", Name: "Leaf"}}, want: "two-level hierarchy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildMetadataImportCalls(metadataSeed{FieldGroups: tc.groups})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestMetadataNotFoundAcceptsGenericNotFound(t *testing.T) {
	require.True(t, metadataNotFound(&pb.RetInfo{Code: pb.ErrorCode_NOT_FOUND, Msg: "sql: no rows"}))
}

func TestProtoMessageSpaceIDFindsNestedMetadataResource(t *testing.T) {
	req := &pb.CreateFieldReq{Field: &pb.Field{SpaceId: "stockcn", FieldId: "close"}}
	assert.Equal(t, "stockcn", protoMessageSpaceID(req.ProtoReflect()))
	assert.Empty(t, protoMessageSpaceID((&pb.ListDataNodesReq{}).ProtoReflect()))
}

func TestSeedToPBAllTypes(t *testing.T) {
	assert.Equal(t, "binance", (seedDataSource{SpaceID: "crypto", DataSourceID: "binance", Name: "Binance"}).toPB().GetDataSourceId())
	assert.Equal(t, "BTC", (seedSubject{SpaceID: "crypto", SubjectID: "BTC"}).toPB().GetSubjectId())
	assert.Equal(t, "BTCUSDT", (seedSubjectSymbol{SpaceID: "crypto", SubjectID: "BTC", ExternalSymbol: "BTCUSDT"}).toPB().GetExternalSymbol())
	field, err := (seedField{SpaceID: "crypto", FieldID: "close", ValueType: "DOUBLE"}).toPB()
	require.NoError(t, err)
	assert.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, field.GetValueType())
	factor, err := (seedFactor{SpaceID: "crypto", FactorID: "ma", ValueType: "DOUBLE"}).toPB()
	require.NoError(t, err)
	assert.Equal(t, "ma", factor.GetFactorId())
	col, err := (seedDatasetColumn{SpaceID: "crypto", DatasetID: "kline", ColumnName: "close", OriginType: "FIELD", ValueType: "DOUBLE"}).toPB()
	require.NoError(t, err)
	assert.Equal(t, "close", col.GetColumnName())
	assert.Equal(t, "kline", (seedDatasetSubject{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC"}).toPB().GetDatasetId())
	assert.Equal(t, "v1", (seedView{SpaceID: "crypto", ViewID: "v1"}).toPB().GetViewId())
	viewCol, err := (seedViewColumn{SpaceID: "crypto", ViewID: "v1", ColumnName: "close", OriginType: "DATASET_COLUMN", ValueType: "DOUBLE"}).toPB()
	require.NoError(t, err)
	assert.Equal(t, "close", viewCol.GetColumnName())
	assert.Equal(t, "dev-1", (seedDevice{DeviceID: "dev-1"}).toPB().GetDeviceId())
}

func metadataTestServer(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]
		if h, ok := handlers[method]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected metadata call: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
}

func writeProtoJSON(w http.ResponseWriter, msg proto.Message) {
	w.Header().Set("Content-Type", "application/json")
	raw, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	_, _ = w.Write(raw)
}

func TestRunMetadataImportCreatesResource(t *testing.T) {
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"CreateSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.CreateSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
	}}
	summary, err := runMetadataImport(context.Background(), server.URL, calls, false)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Applied)
	assert.Equal(t, 0, summary.Skipped)
}

func TestRunMetadataImportSkipsWhenExists(t *testing.T) {
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GetSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.GetSpaceRsp{
				RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
				Space:   &pb.Space{SpaceId: "crypto", Name: "Crypto"},
			})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
		Exists: &metadataExistsProbe{
			Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{},
		},
	}}
	summary, err := runMetadataImport(context.Background(), server.URL, calls, true)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Applied)
	assert.Equal(t, 1, summary.Skipped)
}

func TestMetadataResourceExists(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"GetSpace": func(w http.ResponseWriter, r *http.Request) {
				writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
			},
		})
		defer server.Close()
		probe := &metadataExistsProbe{Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{}}
		ok, err := metadataResourceExists(context.Background(), server.URL, probe)
		require.NoError(t, err)
		assert.True(t, ok)
	})
	t.Run("not found", func(t *testing.T) {
		server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"GetSpace": func(w http.ResponseWriter, r *http.Request) {
				writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SPACE_NOT_FOUND, Msg: "missing"}})
			},
		})
		defer server.Close()
		probe := &metadataExistsProbe{Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{}}
		ok, err := metadataResourceExists(context.Background(), server.URL, probe)
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("error", func(t *testing.T) {
		server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"GetSpace": func(w http.ResponseWriter, r *http.Request) {
				writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "boom"}})
			},
		})
		defer server.Close()
		probe := &metadataExistsProbe{Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{}}
		_, err := metadataResourceExists(context.Background(), server.URL, probe)
		require.Error(t, err)
	})
}

func TestRunMetadataApplyCreatesWhenMissing(t *testing.T) {
	createCalled := false
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GetSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SPACE_NOT_FOUND, Msg: "missing"}})
		},
		"CreateSpace": func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
			writeProtoJSON(w, &pb.CreateSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
		Exists: &metadataExistsProbe{
			Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{},
		},
	}}
	summary, err := runMetadataApply(context.Background(), server.URL, calls)
	require.NoError(t, err)
	assert.True(t, createCalled)
	assert.Equal(t, 1, summary.Applied)
}

func TestRunMetadataApplySkipsUnchanged(t *testing.T) {
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GetSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.GetSpaceRsp{
				RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
				Space:   &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"},
			})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
		Exists: &metadataExistsProbe{
			Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{},
		},
	}}
	summary, err := runMetadataApply(context.Background(), server.URL, calls)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Applied)
	assert.Equal(t, 1, summary.Skipped)
	assert.Equal(t, 1, summary.Unchanged)
}

func TestRunMetadataApplyDatasetColumnProbe(t *testing.T) {
	createCalled := false
	column := &pb.DatasetColumn{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"ListDatasetColumns": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.ListDatasetColumnsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Columns: []*pb.DatasetColumn{}})
		},
		"UpsertDatasetColumn": func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
			writeProtoJSON(w, &pb.UpsertDatasetColumnRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		},
	})
	defer server.Close()

	calls := []metadataImportCall{{
		Resource: "dataset_columns", Method: "UpsertDatasetColumn",
		Request: &pb.UpsertDatasetColumnReq{Column: column}, Response: &pb.UpsertDatasetColumnRsp{},
	}}
	summary, err := runMetadataApply(context.Background(), server.URL, calls)
	require.NoError(t, err)
	assert.True(t, createCalled)
	assert.Equal(t, 1, summary.Applied)
}

func TestRunMetadataApplyFindsDatasetColumnOnLaterPage(t *testing.T) {
	column := &pb.DatasetColumn{
		SpaceId: "crypto", DatasetId: "kline", ColumnName: "close",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
	}
	createCalled := false
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"ListDatasetColumns": func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			req := &pb.ListDatasetColumnsReq{}
			require.NoError(t, protojson.Unmarshal(raw, req))
			if req.GetPage().GetPage() == 1 {
				writeProtoJSON(w, &pb.ListDatasetColumnsRsp{
					RetInfo: storageOK(), PageResult: &commonpb.PageResult{Page: 1, Size: 500, HasMore: true},
				})
				return
			}
			writeProtoJSON(w, &pb.ListDatasetColumnsRsp{
				RetInfo: storageOK(), Columns: []*pb.DatasetColumn{column},
				PageResult: &commonpb.PageResult{Page: 2, Size: 500},
			})
		},
		"UpsertDatasetColumn": func(w http.ResponseWriter, _ *http.Request) {
			createCalled = true
			writeProtoJSON(w, &pb.UpsertDatasetColumnRsp{RetInfo: storageOK()})
		},
	})
	defer server.Close()

	summary, err := runMetadataApply(context.Background(), server.URL, []metadataImportCall{{
		Resource: "dataset_columns", Method: "UpsertDatasetColumn",
		Request: &pb.UpsertDatasetColumnReq{Column: column}, Response: &pb.UpsertDatasetColumnRsp{},
	}})
	require.NoError(t, err)
	require.False(t, createCalled)
	require.Equal(t, 1, summary.Unchanged)
}

func TestRunMetadataApplyFindsViewColumnOnLaterPage(t *testing.T) {
	column := &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "kline_view", ColumnName: "kline.close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	createCalled := false
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"ListViewColumns": func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			req := &pb.ListViewColumnsReq{}
			require.NoError(t, protojson.Unmarshal(raw, req))
			if req.GetPage().GetPage() == 1 {
				writeProtoJSON(w, &pb.ListViewColumnsRsp{
					RetInfo: storageOK(), PageResult: &commonpb.PageResult{Page: 1, Size: 500, HasMore: true},
				})
				return
			}
			writeProtoJSON(w, &pb.ListViewColumnsRsp{
				RetInfo: storageOK(), Columns: []*pb.ViewColumn{column},
				PageResult: &commonpb.PageResult{Page: 2, Size: 500},
			})
		},
		"UpsertViewColumn": func(w http.ResponseWriter, _ *http.Request) {
			createCalled = true
			writeProtoJSON(w, &pb.UpsertViewColumnRsp{RetInfo: storageOK()})
		},
	})
	defer server.Close()

	summary, err := runMetadataApply(context.Background(), server.URL, []metadataImportCall{{
		Resource: "view_columns", Method: "UpsertViewColumn",
		Request: &pb.UpsertViewColumnReq{Column: column}, Response: &pb.UpsertViewColumnRsp{},
	}})
	require.NoError(t, err)
	require.False(t, createCalled)
	require.Equal(t, 1, summary.Unchanged)
}

func TestRunMetadataApplySecondPassIsUnchanged(t *testing.T) {
	seed := metadataSeed{
		Spaces: []seedSpace{{
			SpaceID: "crypto", Name: "加密货币", Description: "数字资产", Owner: "quant",
			seedCommon: seedCommon{Status: "active", Attributes: map[string]string{"managed_by": "moox-cli"}},
		}},
		DataSources: []seedDataSource{{
			SpaceID: "crypto", DataSourceID: "binance", Name: "币安", Kind: "exchange",
			Market: "crypto", Timezone: "UTC", ConfigJSON: "{}",
		}},
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "binance", Name: "行情",
			Description: "小时行情", DataKind: "TIME_SERIES", DataNodeID: "storage-node-0",
			KeepDuration: "8760h", Freqs: []string{"1H"},
		}},
		FieldGroups: []seedFieldGroup{{
			SpaceID: "crypto", GroupID: "quote", Name: "行情", Description: "行情字段", SortOrder: 1,
		}},
		Fields: []seedField{{
			SpaceID: "crypto", GroupID: "quote", FieldID: "close", Name: "收盘价",
			Description: "收盘价格", ValueType: "DOUBLE", Unit: "USDT",
			ValidationRuleJSON: "{}", WriteExample: "1.5", SortOrder: 1,
		}},
		Factors: []seedFactor{{
			SpaceID: "crypto", FactorID: "ma20", Name: "均线", Description: "20周期均线",
			Algorithm: "ma", ParamsJSON: "{}", ValueType: "DOUBLE",
		}},
		DatasetColumns: []seedDatasetColumn{{
			SpaceID: "crypto", DatasetID: "kline", ColumnName: "close",
			OriginType: "FIELD", OriginID: "close", ValueType: "DOUBLE", Required: true,
			Aliases: []string{"收盘"}, seedCommon: seedCommon{Attributes: map[string]string{"display_name": "收盘价"}},
		}},
		Devices: []seedDevice{{
			DeviceID: "primary", Name: "主存储", Engine: "pebble", Endpoint: "/data",
			ConfigJSON: "{}",
		}},
		Views: []seedView{{
			SpaceID: "crypto", ViewID: "kline", Name: "行情视图", Description: "默认行情",
			PrimaryDatasetID: "kline", DatasetIDs: []string{"kline"},
			GrainKeys:  []string{"subject_id", "freq", "data_time", "series_tag"},
			FilterJSON: `{"freq":"1H"}`, Engine: "duckdb", KeepDuration: "8760h",
		}},
		ViewColumns: []seedViewColumn{{
			SpaceID: "crypto", ViewID: "kline", ColumnName: "kline.close",
			OriginType: "DATASET_COLUMN", OriginID: "kline.close", ValueType: "DOUBLE",
			OnlineTime: "2026-01-01T00:00:00Z", SortOrder: 1,
			seedCommon: seedCommon{Attributes: map[string]string{"display_name": "收盘价"}},
		}},
	}
	calls, err := buildMetadataImportCalls(seed)
	require.NoError(t, err)
	dataset, err := seed.Datasets[0].toPB()
	require.NoError(t, err)
	field, err := seed.Fields[0].toPB()
	require.NoError(t, err)
	factor, err := seed.Factors[0].toPB()
	require.NoError(t, err)
	datasetColumn, err := seed.DatasetColumns[0].toPB()
	require.NoError(t, err)
	viewColumn, err := seed.ViewColumns[0].toPB()
	require.NoError(t, err)

	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GetSpace": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: storageOK(), Space: seed.Spaces[0].toPB()})
		},
		"GetDataSource": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.GetDataSourceRsp{RetInfo: storageOK(), DataSource: seed.DataSources[0].toPB()})
		},
		"GetDataset": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.GetDatasetRsp{RetInfo: storageOK(), Dataset: dataset})
		},
		"GetFieldGroup": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.GetFieldGroupRsp{RetInfo: storageOK(), FieldGroup: seed.FieldGroups[0].toPB()})
		},
		"GetField": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.GetFieldRsp{RetInfo: storageOK(), Field: field})
		},
		"GetFactor": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.GetFactorRsp{RetInfo: storageOK(), Factor: factor})
		},
		"ListDatasetColumns": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.ListDatasetColumnsRsp{RetInfo: storageOK(), Columns: []*pb.DatasetColumn{datasetColumn}})
		},
		"GetDevice": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.GetDeviceRsp{RetInfo: storageOK(), Device: seed.Devices[0].toPB()})
		},
		"GetView": func(w http.ResponseWriter, _ *http.Request) {
			view := seed.Views[0].toPB()
			view.KeepDuration, _ = canonicalMetadataKeepDuration(view.GetKeepDuration())
			writeProtoJSON(w, &pb.GetViewRsp{RetInfo: storageOK(), View: view})
		},
		"ListViewColumns": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.ListViewColumnsRsp{RetInfo: storageOK(), Columns: []*pb.ViewColumn{viewColumn}})
		},
	})
	defer server.Close()

	summary, err := runMetadataApply(context.Background(), server.URL, calls)
	require.NoError(t, err)
	assert.Zero(t, summary.Applied)
	assert.Equal(t, len(calls), summary.Unchanged)
}

func TestRunMetadataApplyRejectsViewColumnConflict(t *testing.T) {
	expected := &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "kline", ColumnName: "kline.close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "收盘价"},
	}
	actual := proto.Clone(expected).(*pb.ViewColumn)
	actual.ValueType = pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"ListViewColumns": func(w http.ResponseWriter, _ *http.Request) {
			writeProtoJSON(w, &pb.ListViewColumnsRsp{RetInfo: storageOK(), Columns: []*pb.ViewColumn{actual}})
		},
	})
	defer server.Close()

	_, err := runMetadataApply(context.Background(), server.URL, []metadataImportCall{{
		Resource: "view_columns", Method: "UpsertViewColumn",
		Request: &pb.UpsertViewColumnReq{Column: expected}, Response: &pb.UpsertViewColumnRsp{},
	}})
	require.ErrorContains(t, err, "contract differs")
}

func TestRunMetadataApplyRejectsUnsupportedResource(t *testing.T) {
	_, err := runMetadataApply(context.Background(), "http://unused", []metadataImportCall{{
		Resource: "subjects", Method: "UpsertSubject",
		Request: &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: "crypto", SubjectId: "BTC"}},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply does not support")
}

func TestWriteMetadataImportSummary(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	err = writeMetadataImportSummary(metadataImportSummary{Status: "ok", Planned: 1})
	w.Close()
	os.Stdout = old
	require.NoError(t, err)
	raw, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"status": "ok"`)
}

func TestApplyProbeResultDatasetColumns(t *testing.T) {
	column := &pb.DatasetColumn{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close"}
	probe := &metadataExistsProbe{
		Method:  "ListDatasetColumns",
		Request: &pb.ListDatasetColumnsReq{SpaceId: "crypto", DatasetId: "kline", Page: &commonpb.Page{Page: 1, Size: 500}},
		Response: &pb.ListDatasetColumnsRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
			Columns: []*pb.DatasetColumn{column},
		},
	}
	found, actual := applyProbeResult("dataset_columns", probe, &pb.UpsertDatasetColumnReq{Column: column})
	assert.True(t, found)
	assert.Equal(t, column, actual)
}
