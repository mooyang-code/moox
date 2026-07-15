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

func TestBuildMetadataImportCallsBackfillsColumnDisplayName(t *testing.T) {
	seed := metadataSeed{
		Fields: []seedField{{FieldID: "close", Name: "收盘价", ValueType: "DOUBLE"}},
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

func TestMetadataContractsEqualTreatsNilAndEmptyAttributesAsEqual(t *testing.T) {
	assert.True(t, metadataContractsEqual("spaces",
		&pb.Space{SpaceId: "moox_system", Name: "MooX System", Status: "active"},
		&pb.Space{SpaceId: "moox_system", Name: "MooX System", Status: "active", Attributes: map[string]string{}},
	))
	assert.True(t, metadataContractsEqual("data_sources",
		&pb.DataSource{SpaceId: "moox_system", DataSourceId: "moox_monitor", Name: "MooX Monitor", Status: "active"},
		&pb.DataSource{SpaceId: "moox_system", DataSourceId: "moox_monitor", Name: "MooX Monitor", Status: "active", Attributes: map[string]string{}},
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
		SpaceID: "stock_cn", GroupID: "missing", FieldID: "close", Name: "收盘价", ValueType: "double",
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
	req := &pb.CreateFieldReq{Field: &pb.Field{SpaceId: "stock_cn", FieldId: "close"}}
	assert.Equal(t, "stock_cn", protoMessageSpaceID(req.ProtoReflect()))
	assert.Empty(t, protoMessageSpaceID((&pb.ListPrimaryStoreNodesReq{}).ProtoReflect()))
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
