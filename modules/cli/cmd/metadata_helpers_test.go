package cmd

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	seed := metadataSeed{Spaces: []seedSpace{{SpaceID: "moox_system", seedCommon: seedCommon{Attributes: map[string]string{"scope": "internal"}}}}}
	assert.True(t, hasInternalSpace(seed, "moox_system"))
	assert.False(t, hasInternalSpace(seed, "default"))
}

func TestValidateReservedInternalSpacesAcceptsDeclaredSpace(t *testing.T) {
	seed := metadataSeed{Spaces: []seedSpace{{
		SpaceID: "moox_system",
		seedCommon: seedCommon{Attributes: map[string]string{
			"scope": "internal", "owner_module": "monitor", "managed_by": "platform",
		}},
	}}}
	require.NoError(t, validateReservedInternalSpaces(seed))
}

func TestBuildMetadataImportCallsFullSeed(t *testing.T) {
	seed := metadataSeed{
		Spaces:             []seedSpace{{SpaceID: "crypto", Name: "Crypto"}},
		DataSources:        []seedDataSource{{SpaceID: "crypto", DataSourceID: "binance", Name: "Binance", Kind: "exchange"}},
		Subjects:           []seedSubject{{SpaceID: "crypto", SubjectID: "BTC", Name: "Bitcoin"}},
		SubjectSymbols:     []seedSubjectSymbol{{SpaceID: "crypto", SubjectID: "BTC", DataSourceID: "binance", ExternalSymbol: "BTCUSDT"}},
		Datasets:           []seedDataset{{SpaceID: "crypto", DatasetID: "kline", DataSourceID: "binance", DataKind: "TIME_SERIES", Freqs: []string{"1m"}}},
		DatasetSubjects:    []seedDatasetSubject{{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC"}},
		Fields:             []seedField{{SpaceID: "crypto", FieldID: "close", ValueType: "DOUBLE"}},
		Factors:            []seedFactor{{SpaceID: "crypto", FactorID: "ma", ValueType: "DOUBLE"}},
		DatasetColumns:     []seedDatasetColumn{{SpaceID: "crypto", DatasetID: "kline", ColumnName: "close", OriginType: "FIELD", ValueType: "DOUBLE"}},
		Views:              []seedView{{SpaceID: "crypto", ViewID: "v1", Name: "View", DatasetIDs: []string{"kline"}}},
		ViewColumns:        []seedViewColumn{{SpaceID: "crypto", ViewID: "v1", ColumnName: "close", OriginType: "DATASET_COLUMN", ValueType: "DOUBLE"}},
		PrimaryStoreNodes:  []seedPrimaryStoreNode{{NodeID: "node-1", Name: "Node"}},
		Devices:            []seedDevice{{DeviceID: "dev-1", NodeID: "node-1", Name: "Device"}},
		PrimaryStoreRoutes: []seedPrimaryStoreRoute{{SpaceID: "moox_system", RouteID: "r1", DatasetID: "metrics", SubjectPattern: "*", HashRule: "subject_id"}},
	}
	calls, err := buildMetadataImportCalls(seed)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(calls), 14)
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
	assert.True(t, metadataContractsEqual("primary_store_routes",
		&pb.PrimaryStoreRoute{SpaceId: "moox_system", RouteId: "r1", DatasetId: "metrics", Status: "active"},
		&pb.PrimaryStoreRoute{SpaceId: "moox_system", RouteId: "r1", DatasetId: "metrics", Status: "active"},
	))
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
		{"SNAPSHOT", pb.DataKind_DATA_KIND_SNAPSHOT},
		{"EVENT", pb.DataKind_DATA_KIND_EVENT},
		{"DOCUMENT", pb.DataKind_DATA_KIND_DOCUMENT},
		{"TABLE", pb.DataKind_DATA_KIND_TABLE},
	} {
		got, err := parseDataKind(tc.input)
		require.NoError(t, err)
		assert.Equal(t, tc.kind, got)
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
	assert.Equal(t, "node-1", (seedPrimaryStoreNode{NodeID: "node-1"}).toPB().GetNodeId())
	assert.Equal(t, "dev-1", (seedDevice{DeviceID: "dev-1", NodeID: "node-1"}).toPB().GetDeviceId())
	assert.Equal(t, "r1", (seedPrimaryStoreRoute{SpaceID: "moox_system", RouteID: "r1"}).toPB().GetRouteId())
}

func TestIsReservedReferenceSeed(t *testing.T) {
	seed := metadataSeed{PrimaryStoreRoutes: []seedPrimaryStoreRoute{{SpaceID: "moox_system"}}}
	assert.True(t, isReservedReferenceSeed(seed))
	assert.False(t, isReservedReferenceSeed(metadataSeed{Spaces: []seedSpace{{SpaceID: "default"}}}))
}
