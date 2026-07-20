//go:build legacy_storage

package primarystore

import (
	"context"
	"net/http"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

func TestMetadataCatalogCRUDFlow(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto",
		Name:    "Crypto",
		Status:  "active",
	}})
	mustRetOK(t, spaceRsp, err)

	dataSourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{
		SpaceId:      "crypto",
		DataSourceId: "binance",
		Name:         "Binance",
		Kind:         "exchange",
		Market:       "crypto",
		Status:       "active",
	}})
	mustRetOK(t, dataSourceRsp, err)

	getDataSourceRsp, err := svc.GetDataSource(ctx, &pb.GetDataSourceReq{SpaceId: "crypto", DataSourceId: "binance"})
	mustRetOK(t, getDataSourceRsp, err)
	assert.Equal(t, "Binance", getDataSourceRsp.GetDataSource().GetName())

	dataSourceRsp.GetDataSource().Name = "Binance Updated"
	updateDataSourceRsp, err := svc.UpdateDataSource(ctx, &pb.UpdateDataSourceReq{DataSource: dataSourceRsp.GetDataSource()})
	mustRetOK(t, updateDataSourceRsp, err)

	listDataSourcesRsp, err := svc.ListDataSources(ctx, &pb.ListDataSourcesReq{SpaceId: "crypto"})
	mustRetOK(t, listDataSourcesRsp, err)
	require.NotEmpty(t, listDataSourcesRsp.GetDataSources())

	datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId:      "crypto",
		DatasetId:    "binance_kline",
		DataSourceId: "binance",
		Name:         "币安K线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	}})
	mustRetOK(t, datasetRsp, err)

	subjectRsp, err := svc.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{
		SpaceId:   "crypto",
		SubjectId: "BTC",
		Name:      "Bitcoin",
		Status:    "active",
	}})
	mustRetOK(t, subjectRsp, err)

	registerRsp, err := svc.RegisterDataSubject(ctx, &pb.RegisterDataSubjectReq{
		SpaceId:        "crypto",
		DataSourceId:   "binance",
		ExternalSymbol: "BTCUSDT",
		Subject:        subjectRsp.GetSubject(),
		DatasetBindings: []*pb.DatasetSubject{{
			DatasetId: "binance_kline",
		}},
	})
	mustRetOK(t, registerRsp, err)

	getSubjectRsp, err := svc.GetSubject(ctx, &pb.GetSubjectReq{SpaceId: "crypto", SubjectId: "BTC"})
	mustRetOK(t, getSubjectRsp, err)

	listSubjectsRsp, err := svc.ListSubjects(ctx, &pb.ListSubjectsReq{SpaceId: "crypto"})
	mustRetOK(t, listSubjectsRsp, err)

	symbolRsp, err := svc.UpsertSubjectSymbol(ctx, &pb.UpsertSubjectSymbolReq{SubjectSymbol: &pb.SubjectSymbol{
		SpaceId:        "crypto",
		SubjectId:      "BTC",
		DataSourceId:   "binance",
		ExternalSymbol: "BTCUSDT",
		Status:         "active",
	}})
	mustRetOK(t, symbolRsp, err)

	listSymbolsRsp, err := svc.ListSubjectSymbols(ctx, &pb.ListSubjectSymbolsReq{SpaceId: "crypto", SubjectId: "BTC"})
	mustRetOK(t, listSymbolsRsp, err)

	bindRsp, err := svc.BindDatasetSubject(ctx, &pb.BindDatasetSubjectReq{DatasetSubject: &pb.DatasetSubject{
		SpaceId:   "crypto",
		DatasetId: "binance_kline",
		SubjectId: "BTC",
		Status:    "active",
	}})
	mustRetOK(t, bindRsp, err)

	listBindingsRsp, err := svc.ListDatasetSubjects(ctx, &pb.ListDatasetSubjectsReq{
		SpaceId: "crypto", DatasetId: "binance_kline",
	})
	mustRetOK(t, listBindingsRsp, err)

	groupRsp, err := svc.CreateFieldGroup(ctx, &pb.CreateFieldGroupReq{FieldGroup: &pb.FieldGroup{
		SpaceId: "crypto", GroupId: "quote", Name: "行情字段", Status: "active",
	}})
	mustRetOK(t, groupRsp, err)
	getGroupRsp, err := svc.GetFieldGroup(ctx, &pb.GetFieldGroupReq{SpaceId: "crypto", GroupId: "quote"})
	mustRetOK(t, getGroupRsp, err)
	getGroupRsp.GetFieldGroup().Description = "价格与成交字段"
	updateGroupRsp, err := svc.UpdateFieldGroup(ctx, &pb.UpdateFieldGroupReq{FieldGroup: getGroupRsp.GetFieldGroup()})
	mustRetOK(t, updateGroupRsp, err)
	listGroupRsp, err := svc.ListFieldGroups(ctx, &pb.ListFieldGroupsReq{SpaceId: "crypto"})
	mustRetOK(t, listGroupRsp, err)

	fieldRsp, err := svc.CreateField(ctx, &pb.CreateFieldReq{Field: &pb.Field{
		SpaceId:   "crypto",
		GroupId:   "quote",
		FieldId:   "close",
		Name:      "Close",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:    "active",
	}})
	mustRetOK(t, fieldRsp, err)

	getFieldRsp, err := svc.GetField(ctx, &pb.GetFieldReq{SpaceId: "crypto", FieldId: "close"})
	mustRetOK(t, getFieldRsp, err)

	fieldRsp.GetField().Name = "Close Price"
	updateFieldRsp, err := svc.UpdateField(ctx, &pb.UpdateFieldReq{Field: fieldRsp.GetField()})
	mustRetOK(t, updateFieldRsp, err)

	listFieldsRsp, err := svc.ListFields(ctx, &pb.ListFieldsReq{SpaceId: "crypto"})
	mustRetOK(t, listFieldsRsp, err)

	factorRsp, err := svc.CreateFactor(ctx, &pb.CreateFactorReq{Factor: &pb.Factor{
		SpaceId:   "crypto",
		FactorId:  "ma5",
		Name:      "MA5",
		Algorithm: "sma",
		Status:    "active",
	}})
	mustRetOK(t, factorRsp, err)

	getFactorRsp, err := svc.GetFactor(ctx, &pb.GetFactorReq{SpaceId: "crypto", FactorId: "ma5"})
	mustRetOK(t, getFactorRsp, err)

	updateFactorRsp, err := svc.UpdateFactor(ctx, &pb.UpdateFactorReq{Factor: factorRsp.GetFactor()})
	mustRetOK(t, updateFactorRsp, err)

	listFactorsRsp, err := svc.ListFactors(ctx, &pb.ListFactorsReq{SpaceId: "crypto"})
	mustRetOK(t, listFactorsRsp, err)

	columnRsp, err := svc.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: &pb.DatasetColumn{
		SpaceId:    "crypto",
		DatasetId:  "binance_kline",
		ColumnName: "close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "收盘价"},
	}})
	mustRetOK(t, columnRsp, err)

	listColumnsRsp, err := svc.ListDatasetColumns(ctx, &pb.ListDatasetColumnsReq{
		SpaceId: "crypto", DatasetId: "binance_kline",
	})
	mustRetOK(t, listColumnsRsp, err)

	getDatasetRsp, err := svc.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: "crypto", DatasetId: "binance_kline"})
	mustRetOK(t, getDatasetRsp, err)

	updateDatasetRsp, err := svc.UpdateDataset(ctx, &pb.UpdateDatasetReq{Dataset: getDatasetRsp.GetDataset()})
	mustRetOK(t, updateDatasetRsp, err)

	listDatasetsRsp, err := svc.ListDatasets(ctx, &pb.ListDatasetsReq{SpaceId: "crypto"})
	mustRetOK(t, listDatasetsRsp, err)
}

func TestFieldGovernanceRPCFlow(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{Root: t.TempDir(), InitSchemaPath: storageSchemaPath(t)})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "stock_cn", Name: "A股市场"}})
	mustRetOK(t, spaceRsp, err)
	for _, group := range []*pb.FieldGroup{
		{SpaceId: "stock_cn", GroupId: "market", Name: "市场数据"},
		{SpaceId: "stock_cn", GroupId: "quote", ParentGroupId: "market", Name: "行情价格"},
		{SpaceId: "stock_cn", GroupId: "empty", Name: "空字段组"},
	} {
		groupRsp, groupErr := svc.CreateFieldGroup(ctx, &pb.CreateFieldGroupReq{FieldGroup: group})
		mustRetOK(t, groupRsp, groupErr)
	}
	for _, field := range []*pb.Field{
		{SpaceId: "stock_cn", GroupId: "quote", FieldId: "close", Name: "收盘价", Status: "active"},
		{SpaceId: "stock_cn", GroupId: "quote", FieldId: "open", Name: "开盘价", Status: "active"},
	} {
		fieldRsp, fieldErr := svc.CreateField(ctx, &pb.CreateFieldReq{Field: field})
		mustRetOK(t, fieldRsp, fieldErr)
	}

	groups, err := svc.ListFieldGroups(ctx, &pb.ListFieldGroupsReq{SpaceId: "stock_cn"})
	mustRetOK(t, groups, err)
	assert.Equal(t, uint64(2), groups.GetTotalFieldCount())
	assert.Equal(t, uint64(2), groups.GetFieldCounts()["market"])
	assert.Equal(t, uint64(2), groups.GetFieldCounts()["quote"])

	fields, err := svc.ListFields(ctx, &pb.ListFieldsReq{SpaceId: "stock_cn", GroupId: "market", IncludeDescendants: true, Keyword: "价", SortBy: "field_id", SortOrder: "desc"})
	mustRetOK(t, fields, err)
	require.Len(t, fields.GetFields(), 2)
	assert.Equal(t, "open", fields.GetFields()[0].GetFieldId())

	batch, err := svc.BatchUpdateFields(ctx, &pb.BatchUpdateFieldsReq{SpaceId: "stock_cn", FieldIds: []string{"close", "open"}, TargetStatus: "disabled"})
	mustRetOK(t, batch, err)
	assert.Equal(t, uint32(2), batch.GetUpdatedCount())

	deleteRsp, err := svc.DeleteFieldGroup(ctx, &pb.DeleteFieldGroupReq{SpaceId: "stock_cn", GroupId: "empty"})
	mustRetOK(t, deleteRsp, err)
	failedDelete, err := svc.DeleteFieldGroup(ctx, &pb.DeleteFieldGroupReq{SpaceId: "stock_cn", GroupId: "quote"})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, failedDelete.GetRetInfo().GetCode())
}

func TestFieldGovernanceRejectsUpsertSemanticsAndSpaceMismatch(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{Root: t.TempDir(), InitSchemaPath: storageSchemaPath(t)})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "stock_cn", Name: "A股市场"}})
	mustRetOK(t, spaceRsp, err)
	group := &pb.FieldGroup{SpaceId: "stock_cn", GroupId: "quote", Name: "行情价格"}
	groupRsp, err := svc.CreateFieldGroup(ctx, &pb.CreateFieldGroupReq{FieldGroup: group})
	mustRetOK(t, groupRsp, err)
	duplicateGroup, err := svc.CreateFieldGroup(ctx, &pb.CreateFieldGroupReq{FieldGroup: group})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, duplicateGroup.GetRetInfo().GetCode())

	missingUpdate, err := svc.UpdateField(ctx, &pb.UpdateFieldReq{Field: &pb.Field{SpaceId: "stock_cn", GroupId: "quote", FieldId: "missing", Name: "不存在"}})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, missingUpdate.GetRetInfo().GetCode())

	req := httptestRequestWithSpace("crypto")
	headerCtx := thttp.WithHeader(ctx, &thttp.Header{Request: req})
	mismatch, err := svc.ListFields(headerCtx, &pb.ListFieldsReq{SpaceId: "stock_cn"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, mismatch.GetRetInfo().GetCode())
	getMismatch, err := svc.GetFieldGroup(headerCtx, &pb.GetFieldGroupReq{SpaceId: "stock_cn", GroupId: "quote"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, getMismatch.GetRetInfo().GetCode())
	missingHeaderCtx := thttp.WithHeader(ctx, &thttp.Header{Request: httptestRequestWithSpace("")})
	missingHeader, err := svc.ListFields(missingHeaderCtx, &pb.ListFieldsReq{SpaceId: "stock_cn"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, missingHeader.GetRetInfo().GetCode())
}

func httptestRequestWithSpace(spaceID string) *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Space-Id", spaceID)
	return req
}

func TestMetadataCatalogRejectsInvalidInput(t *testing.T) {
	svc := &Service{metadata: &stubMetadataStore{}}
	ctx := context.Background()

	cases := []struct {
		name string
		run  func() (*pb.RetInfo, error)
	}{
		{
			name: "create data source missing fields",
			run: func() (*pb.RetInfo, error) {
				rsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{})
				return rsp.GetRetInfo(), err
			},
		},
		{
			name: "upsert subject missing fields",
			run: func() (*pb.RetInfo, error) {
				rsp, err := svc.UpsertSubject(ctx, &pb.UpsertSubjectReq{})
				return rsp.GetRetInfo(), err
			},
		},
		{
			name: "register data subject missing fields",
			run: func() (*pb.RetInfo, error) {
				rsp, err := svc.RegisterDataSubject(ctx, &pb.RegisterDataSubjectReq{})
				return rsp.GetRetInfo(), err
			},
		},
		{
			name: "bind dataset subject missing fields",
			run: func() (*pb.RetInfo, error) {
				rsp, err := svc.BindDatasetSubject(ctx, &pb.BindDatasetSubjectReq{})
				return rsp.GetRetInfo(), err
			},
		},
		{
			name: "upsert dataset column missing fields",
			run: func() (*pb.RetInfo, error) {
				rsp, err := svc.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{})
				return rsp.GetRetInfo(), err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ret, err := tc.run()
			require.NoError(t, err)
			assert.Equal(t, pb.ErrorCode_INVALID_PARAM, ret.GetCode())
		})
	}
}
