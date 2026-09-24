package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	trpc "trpc.group/trpc-go/trpc-go"
)

func accessRead() error {
	credentials := gatewayauth.Credentials{
		KeyID:  strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_INBOUND_KEY_ID")),
		Caller: strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_INBOUND_CALLER")),
		Secret: os.Getenv("MOOX_STORAGE_ACCESS_INBOUND_SECRET"),
	}
	target := strings.TrimSpace(os.Getenv("MOOX_ACCESS_TARGET"))
	targetNode := strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_TARGET_NODE"))
	options := gatewayauth.NewTRPCClientOptions(target, targetNode, credentials)
	reader := pb.NewPrimaryStoreClientProxy(options...)
	ctx, cancel := context.WithTimeout(trpc.BackgroundContext(), 30*time.Second)
	defer cancel()
	appID := strings.TrimSpace(os.Getenv("MOOX_STORAGE_APP_ID"))
	appKey := strings.TrimSpace(os.Getenv("MOOX_STORAGE_APP_KEY"))
	datasets := []struct {
		id      string
		freq    string
		subject string
	}{
		{"dataset_binance_spot_kline_1m", "1m", "BTC-USDT"},
		{"dataset_binance_swap_kline_1m", "1m", "ZORA-USDT"},
		{"dataset_spot_kline_1h", "1H", "MRNAB-USDT"},
		{"dataset_perpetual_kline_1h", "1H", "LTC-USDT"},
	}
	for _, dataset := range datasets {
		readAppID, readAppKey := appID, appKey
		if strings.Contains(dataset.id, "swap") {
			readAppID = strings.TrimSpace(os.Getenv("MOOX_SWAP_APP_ID"))
			readAppKey = strings.TrimSpace(os.Getenv("MOOX_SWAP_APP_KEY"))
		} else if strings.Contains(dataset.id, "spot") {
			readAppID = strings.TrimSpace(os.Getenv("MOOX_SPOT_APP_ID"))
			readAppKey = strings.TrimSpace(os.Getenv("MOOX_SPOT_APP_KEY"))
		}
		series := "venue:binance"
		rsp, err := reader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: readAppID, AppKey: readAppKey},
			Selectors: []*pb.TimeSeriesSelector{{
				SpaceId: "crypto", DatasetId: dataset.id, SubjectId: dataset.subject, Freq: dataset.freq, SeriesTag: &series,
			}},
			TimeRange: &pb.TimeRange{}, Order: pb.SortOrder_SORT_ORDER_DESC,
			Page: &pb.Page{Page: 1, Size: 5}, SpaceId: "crypto", DatasetId: dataset.id,
		})
		if err != nil {
			return fmt.Errorf("%s: rpc: %w", dataset.id, err)
		}
		if rsp.GetRetInfo().GetCode() != 0 {
			return fmt.Errorf("%s: ret=%d msg=%s", dataset.id, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
		}
		fmt.Printf("dataset=%s rows=%d complete=%t served_indexed_from=%s served_indexed_to=%s\n", dataset.id, len(rsp.GetRows()), rsp.GetComplete(), rsp.GetServedIndexedFrom(), rsp.GetServedIndexedTo())
		for _, row := range rsp.GetRows() {
			fmt.Printf("  subject=%s freq=%s time=%s series=%s\n", row.GetKey().GetSubjectId(), row.GetKey().GetFreq(), row.GetKey().GetDataTime(), row.GetKey().GetSeriesTag())
		}
	}
	return nil
}

func viewRead() error {
	credentials := gatewayauth.Credentials{
		KeyID:  strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_INBOUND_KEY_ID")),
		Caller: strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_INBOUND_CALLER")),
		Secret: os.Getenv("MOOX_STORAGE_ACCESS_INBOUND_SECRET"),
	}
	options := gatewayauth.NewTRPCClientOptions(strings.TrimSpace(os.Getenv("MOOX_ACCESS_TARGET")), strings.TrimSpace(os.Getenv("MOOX_STORAGE_ACCESS_TARGET_NODE")), credentials)
	reader := pb.NewDataViewClientProxy(options...)
	ctx, cancel := context.WithTimeout(trpc.BackgroundContext(), 30*time.Second)
	defer cancel()
	appID := strings.TrimSpace(os.Getenv("MOOX_STORAGE_APP_ID"))
	appKey := strings.TrimSpace(os.Getenv("MOOX_STORAGE_APP_KEY"))
	views := []struct {
		view, dataset, freq, subject string
	}{
		{"view_binance_spot_kline_1m", "dataset_binance_spot_kline_1m", "1m", "BTC-USDT"},
		{"view_binance_swap_kline_1m", "dataset_binance_swap_kline_1m", "1m", "ZORA-USDT"},
		{"view_crypto_spot_kline_1h", "dataset_spot_kline_1h", "1H", "MRNAB-USDT"},
		{"view_crypto_swap_kline_1h", "dataset_perpetual_kline_1h", "1H", "LTC-USDT"},
	}
	for _, view := range views {
		series := "venue:binance"
		rsp, err := reader.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: appID, AppKey: appKey}, SpaceId: "crypto", ViewId: view.view,
			Selectors: []*pb.TimeSeriesSelector{{SpaceId: "crypto", DatasetId: view.dataset, SubjectId: view.subject, Freq: view.freq, SeriesTag: &series}},
			Sorts:     []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
			Limit:     5,
		})
		if err != nil {
			return fmt.Errorf("%s: rpc: %w", view.view, err)
		}
		if rsp.GetRetInfo().GetCode() != 0 {
			return fmt.Errorf("%s: ret=%d msg=%s", view.view, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
		}
		fmt.Printf("view=%s rows=%d complete=%t active_index=%s revision=%d served_indexed_from=%s served_indexed_to=%s\n", view.view, len(rsp.GetRows()), rsp.GetComplete(), rsp.GetServedActiveIndexId(), rsp.GetServedActiveIndexRevision(), rsp.GetServedIndexedFrom(), rsp.GetServedIndexedTo())
		for _, row := range rsp.GetRows() {
			fmt.Printf("  subject=%s freq=%s time=%s series=%s\n", row.GetKey().GetSubjectId(), row.GetKey().GetFreq(), row.GetKey().GetDataTime(), row.GetKey().GetSeriesTag())
		}
	}
	return nil
}
