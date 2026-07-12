package access

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	fieldRsp, err := svc.CreateField(ctx, &pb.CreateFieldReq{Field: &pb.Field{
		SpaceId:   "crypto",
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
