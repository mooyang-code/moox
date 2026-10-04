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
	datasetID := envDefault("MOOX_SMOKE_DATASET_ID", "dataset_binance_kline_1m")
	viewID := envDefault("MOOX_SMOKE_VIEW_ID", "view_binance_kline_1m")
	subjectID := envDefault("MOOX_SMOKE_SUBJECT_ID", "BTC-USDT")
	frequency := envDefault("MOOX_SMOKE_FREQUENCY", "1m")
	spaceID := envDefault("MOOX_SMOKE_SPACE_ID", "crypto")
	targets := smokeSeriesTargets(datasetID, frequency)
	primaryRows := make(map[string][]*pb.TimeSeriesRow, len(targets))
	viewRows := make(map[string][]*pb.TimeSeriesRow, len(targets))
	viewResponses := make(map[string]*pb.QueryTimeSeriesRowsRsp, len(targets))
	primaryCounts := make(map[string]int, len(targets))
	viewCounts := make(map[string]int, len(targets))
	for _, target := range targets {
		primaryRsp, err := primary.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: "storage-primary-smoke", AppKey: datanode.ServiceAuthKey(primarySecret, "storage-primary-smoke")},
			SpaceId:  spaceID, DatasetId: datasetID,
			Selectors: []*pb.TimeSeriesSelector{smokeSelector(spaceID, datasetID, subjectID, frequency, target)}, TimeRange: &pb.TimeRange{StartTime: start, EndTime: end},
			Order: pb.SortOrder_SORT_ORDER_DESC, Page: &pb.Page{Page: 1, Size: 2000},
		})
		if err != nil {
			panic(fmt.Errorf("primary query for %s series: %w", target.name, err))
		}
		if primaryRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			panic(fmt.Sprintf("primary query for %s series failed: code=%v msg=%s", target.name, primaryRsp.GetRetInfo().GetCode(), primaryRsp.GetRetInfo().GetMsg()))
		}
		primaryRows[target.name] = primaryRsp.GetRows()
		primaryCounts[target.name] = len(primaryRsp.GetRows())
	}
	for _, target := range targets {
		rsp, err := view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: "storage-view-smoke", AppKey: datanode.ServiceAuthKey(secret, "storage-view-smoke")},
			SpaceId:  spaceID, ViewId: viewID,
			Selectors: []*pb.TimeSeriesSelector{smokeSelector(spaceID, datasetID, subjectID, frequency, target)},
			TimeRange: &pb.TimeRange{StartTime: start, EndTime: end},
			Sorts:     []*pb.SortSpec{{FieldName: "data_time", Desc: true}}, Page: &pb.Page{Page: 1, Size: 2000}, TotalMode: pb.TotalMode_NONE,
		})
		if err != nil {
			panic(fmt.Errorf("View query for %s series: %w", target.name, err))
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			panic(fmt.Sprintf("View query for %s series failed: code=%v msg=%s", target.name, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg()))
		}
		viewRows[target.name] = rsp.GetRows()
		viewCounts[target.name] = len(rsp.GetRows())
		viewResponses[target.name] = rsp
	}
	if err := validateSmokeSeriesCounts(targets, primaryCounts, viewCounts); err != nil {
		panic(err)
	}
	var allPrimaryRows, allViewRows []*pb.TimeSeriesRow
	servedTo := ""
	complete := true
	for _, target := range targets {
		primaryForSeries, viewForSeries := primaryRows[target.name], viewRows[target.name]
		allPrimaryRows = append(allPrimaryRows, primaryForSeries...)
		allViewRows = append(allViewRows, viewForSeries...)
		response := viewResponses[target.name]
		if response.GetServedIndexedTo() > servedTo {
			servedTo = response.GetServedIndexedTo()
		}
		complete = complete && response.GetComplete()
		fmt.Printf("series=%s series_tag=%s primary_rows=%d primary_latest=%s view_rows=%d view_latest=%s\n", target.name, target.seriesTag, len(primaryForSeries), maxDataTime(primaryForSeries), len(viewForSeries), maxDataTime(viewForSeries))
	}
	fmt.Printf("production view smoke passed: primary_rows=%d primary_latest=%s view_rows=%d view_latest=%s served_to=%s complete=%t series_checks=%d\n", len(allPrimaryRows), maxDataTime(allPrimaryRows), len(allViewRows), maxDataTime(allViewRows), servedTo, complete, len(targets))
	if os.Getenv("MOOX_SMOKE_DUMP_LATEST") == "1" {
		fmt.Printf("primary_latest_row=%v\nview_latest_row=%v\n", latestRow(allPrimaryRows), latestRow(allViewRows))
	}
}

type smokeSeriesTarget struct {
	name      string
	seriesTag string
}

func smokeSeriesTargets(datasetID, frequency string) []smokeSeriesTarget {
	if datasetID == "dataset_binance_kline_1m" && frequency == "1m" {
		return []smokeSeriesTarget{
			{name: "spot", seriesTag: "venue:binance|market:spot|source:spot_http"},
			{name: "swap", seriesTag: "venue:binance|market:swap|source:swap_http"},
		}
	}
	return []smokeSeriesTarget{{name: "unscoped"}}
}

func smokeSelector(spaceID, datasetID, subjectID, frequency string, target smokeSeriesTarget) *pb.TimeSeriesSelector {
	selector := &pb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: frequency}
	if target.seriesTag != "" {
		seriesTag := target.seriesTag
		selector.SeriesTag = &seriesTag
	}
	return selector
}

func validateSmokeSeriesCounts(targets []smokeSeriesTarget, primaryCounts, viewCounts map[string]int) error {
	for _, target := range targets {
		if primaryCounts[target.name] == 0 {
			return fmt.Errorf("primary query returned no rows for %s series", target.name)
		}
		if viewCounts[target.name] == 0 {
			return fmt.Errorf("View query returned no rows for %s series", target.name)
		}
	}
	return nil
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
