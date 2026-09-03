package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

func main() {
	secret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")
	primarySecret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	if secret == "" || primarySecret == "" {
		panic("storage auth secrets are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	view := pb.NewDataViewClientProxy(client.WithTarget("ip://127.0.0.1:20202"), client.WithNetwork("tcp"), client.WithProtocol("http"))
	primary := pb.NewPrimaryStoreClientProxy(client.WithTarget("ip://127.0.0.1:20201"), client.WithNetwork("tcp"), client.WithProtocol("http"))
	now := time.Now().UTC()
	lookback := 24 * time.Hour
	if raw := os.Getenv("MOOX_SMOKE_LOOKBACK_HOURS"); raw != "" {
		if hours, err := time.ParseDuration(raw + "h"); err == nil && hours > 0 {
			lookback = hours
		}
	}
	start := now.Add(-lookback).Format(time.RFC3339Nano)
	end := now.Format(time.RFC3339Nano)
	datasetID := envDefault("MOOX_SMOKE_DATASET_ID", "binance_spot_kline_1m")
	viewID := envDefault("MOOX_SMOKE_VIEW_ID", "binance_spot_kline_1m_view")
	subjectID := envDefault("MOOX_SMOKE_SUBJECT_ID", "BTC-USDT")
	frequency := envDefault("MOOX_SMOKE_FREQUENCY", "1m")
	spaceID := envDefault("MOOX_SMOKE_SPACE_ID", "crypto")
	selector := &pb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: frequency}
	primaryRsp, err := primary.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "storage-primary-smoke", AppKey: datanode.ServiceAuthKey(primarySecret, "storage-primary-smoke")},
		SpaceId:  spaceID, DatasetId: datasetID,
		Selectors: []*pb.TimeSeriesSelector{selector}, TimeRange: &pb.TimeRange{StartTime: start, EndTime: end},
		Order: pb.SortOrder_SORT_ORDER_DESC, Page: &pb.Page{Page: 1, Size: 2000},
	})
	if err != nil {
		panic(err)
	}
	if primaryRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(primaryRsp.GetRows()) == 0 {
		panic(fmt.Sprintf("primary query failed: code=%v msg=%s rows=%d", primaryRsp.GetRetInfo().GetCode(), primaryRsp.GetRetInfo().GetMsg(), len(primaryRsp.GetRows())))
	}
	primaryLast := maxDataTime(primaryRsp.GetRows())
	rsp, err := view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "storage-view-smoke", AppKey: datanode.ServiceAuthKey(secret, "storage-view-smoke")},
		SpaceId:  spaceID, ViewId: viewID,
		Selectors: []*pb.TimeSeriesSelector{selector},
		TimeRange: &pb.TimeRange{StartTime: start, EndTime: end},
		Sorts:     []*pb.SortSpec{{FieldName: "data_time", Desc: true}}, Page: &pb.Page{Page: 1, Size: 2000}, TotalMode: pb.TotalMode_NONE,
	})
	if err != nil {
		panic(err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		panic(fmt.Sprintf("query failed: code=%v msg=%s", rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg()))
	}
	if len(rsp.GetRows()) == 0 {
		panic("query returned no rows")
	}
	last := maxDataTime(rsp.GetRows())
	fmt.Printf("production view smoke passed: primary_rows=%d primary_latest=%s view_rows=%d view_latest=%s served_to=%s complete=%t\n", len(primaryRsp.GetRows()), primaryLast, len(rsp.GetRows()), last, rsp.GetServedIndexedTo(), rsp.GetComplete())
	if os.Getenv("MOOX_SMOKE_DUMP_LATEST") == "1" {
		fmt.Printf("primary_latest_row=%v\nview_latest_row=%v\n", latestRow(primaryRsp.GetRows()), latestRow(rsp.GetRows()))
	}
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func maxDataTime(rows []*pb.TimeSeriesRow) string {
	latest := ""
	for _, row := range rows {
		if value := row.GetKey().GetDataTime(); value > latest {
			latest = value
		}
	}
	return latest
}

func latestRow(rows []*pb.TimeSeriesRow) *pb.TimeSeriesRow {
	var latest *pb.TimeSeriesRow
	for _, row := range rows {
		if latest == nil || row.GetKey().GetDataTime() > latest.GetKey().GetDataTime() {
			latest = row
		}
	}
	return latest
}
