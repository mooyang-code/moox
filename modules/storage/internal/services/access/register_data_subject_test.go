package access

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestRegisterDataSubjectUpsertsObjectSymbolAndBindings(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "binance_spot_symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"symbol", "status"})
	seedDataset(t, ctx, svc, "binance_spot_kline", pb.DataKind_DATA_KIND_TIME_SERIES, []string{"1m"}, []string{"close"})

	req := &pb.RegisterDataSubjectReq{
		SpaceId:        "crypto",
		DataSourceId:   "binance",
		ExternalSymbol: "BTCUSDT",
		Subject: &pb.Subject{
			SubjectId:   "BTC-USDT",
			SubjectType: "crypto_pair",
			Name:        "BTC-USDT",
			Market:      "spot",
			Currency:    "USDT",
			Timezone:    "UTC",
			Status:      "active",
		},
		DatasetBindings: []*pb.DatasetSubject{
			{DatasetId: "binance_spot_symbols", SubjectRole: "record"},
			{DatasetId: "binance_spot_kline", SubjectRole: "normal"},
		},
	}

	rsp, err := svc.RegisterDataSubject(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "BTC-USDT", rsp.GetSubject().GetSubjectId())
	require.Len(t, rsp.GetDatasetBindings(), 2)

	subject, err := svc.metadataReader.GetSubject(ctx, "crypto", "BTC-USDT")
	require.NoError(t, err)
	require.Equal(t, "crypto_pair", subject.GetSubjectType())

	symbols, _, err := svc.metadataReader.ListSubjectSymbols(ctx, "crypto", "BTC-USDT", "binance", "BTCUSDT", nil)
	require.NoError(t, err)
	require.Len(t, symbols, 1)
	require.Equal(t, "BTCUSDT", symbols[0].GetExternalSymbol())

	bindings, _, err := svc.metadataReader.ListDatasetSubjects(ctx, "crypto", "", "BTC-USDT", nil)
	require.NoError(t, err)
	require.Len(t, bindings, 2)
	require.ElementsMatch(t, []string{"binance_spot_symbols", "binance_spot_kline"}, []string{
		bindings[0].GetDatasetId(),
		bindings[1].GetDatasetId(),
	})

	rsp, err = svc.RegisterDataSubject(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetDatasetBindings(), 2)

	bindings, _, err = svc.metadataReader.ListDatasetSubjects(ctx, "crypto", "", "BTC-USDT", nil)
	require.NoError(t, err)
	require.Len(t, bindings, 2)
}

func TestRegisterDataSubjectMissingDatasetLeavesNoPartialState(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	seedDataset(t, ctx, svc, "binance_spot_symbols", pb.DataKind_DATA_KIND_RECORD, nil, []string{"symbol", "status"})

	rsp, err := svc.RegisterDataSubject(ctx, &pb.RegisterDataSubjectReq{
		SpaceId:        "crypto",
		DataSourceId:   "binance",
		ExternalSymbol: "ETHUSDT",
		Subject: &pb.Subject{
			SubjectId:   "ETH-USDT",
			SubjectType: "crypto_pair",
			Name:        "ETH-USDT",
			Market:      "spot",
			Currency:    "USDT",
			Timezone:    "UTC",
			Status:      "active",
		},
		DatasetBindings: []*pb.DatasetSubject{
			{DatasetId: "binance_spot_symbols", SubjectRole: "record"},
			{DatasetId: "missing_dataset", SubjectRole: "normal"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, rsp.GetRetInfo().GetCode())

	_, err = svc.metadataReader.GetSubject(ctx, "crypto", "ETH-USDT")
	require.Error(t, err)

	symbols, _, err := svc.metadataReader.ListSubjectSymbols(ctx, "crypto", "ETH-USDT", "binance", "ETHUSDT", nil)
	require.NoError(t, err)
	require.Empty(t, symbols)

	bindings, _, err := svc.metadataReader.ListDatasetSubjects(ctx, "crypto", "", "ETH-USDT", nil)
	require.NoError(t, err)
	require.Empty(t, bindings)
}

func TestBindDatasetSubjectReturnsDatasetNotFoundForMissingDataset(t *testing.T) {
	ctx := context.Background()
	svc := newAccessTestService(t)
	_, err := svc.metadata.UpsertSubject(ctx, &pb.Subject{
		SpaceId:     "crypto",
		SubjectId:   "BTC-USDT",
		SubjectType: "crypto_pair",
		Name:        "BTC-USDT",
		Status:      "active",
	})
	require.NoError(t, err)

	rsp, err := svc.BindDatasetSubject(ctx, &pb.BindDatasetSubjectReq{DatasetSubject: &pb.DatasetSubject{
		SpaceId:     "crypto",
		DatasetId:   "missing_dataset",
		SubjectId:   "BTC-USDT",
		SubjectRole: "normal",
		Status:      "active",
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, rsp.GetRetInfo().GetCode())

	bindings, _, err := svc.metadataReader.ListDatasetSubjects(ctx, "crypto", "", "BTC-USDT", nil)
	require.NoError(t, err)
	require.Empty(t, bindings)
}
