package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/bench"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func prepareData(ctx context.Context, opts options) (string, []string, error) {
	if opts.dataDir != "" {
		return opts.dataDir, nil, nil
	}
	dataRoot := filepath.Join(opts.workDir, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		return "", nil, err
	}
	cmd := exec.CommandContext(ctx, "bsdtar", "-xf", opts.zipPath, "-C", dataRoot, "period_1h_kline")
	out, err := cmd.CombinedOutput()
	var warnings []string
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("解压 zip 时检测到异常（%s），但已恢复出可用 K 线 CSV 并继续压测。%s", localizedCommandError(err), localizedExtractOutput(string(out))))
	}
	if _, discoverErr := bench.DiscoverKlineFiles(dataRoot, freq); discoverErr != nil {
		if err != nil {
			return "", warnings, fmt.Errorf("extract %s failed: %w; output: %s", opts.zipPath, err, string(out))
		}
		return "", warnings, discoverErr
	}
	return dataRoot, warnings, nil
}

func seedMetadata(ctx context.Context, meta pb.MetadataClientProxy, files []bench.KlineFile, opts options) error {
	if err := call("CreateSpace", func() (*pb.RetInfo, error) {
		rsp, err := meta.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: spaceID, Name: "Storage Bench", Status: "active"}})
		return rsp.GetRetInfo(), err
	}); err != nil {
		return err
	}
	if err := call("CreateDataSource", func() (*pb.RetInfo, error) {
		rsp, err := meta.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: spaceID, DataSourceId: dataSourceID, Name: "Binance", Kind: "exchange", Market: "crypto", Status: "active"}})
		return rsp.GetRetInfo(), err
	}); err != nil {
		return err
	}
	for _, field := range fieldDefs() {
		field := field
		if err := call("CreateField:"+field.FieldId, func() (*pb.RetInfo, error) {
			rsp, err := meta.CreateField(ctx, &pb.CreateFieldReq{Field: field})
			return rsp.GetRetInfo(), err
		}); err != nil {
			return err
		}
	}
	if err := call("CreatePrimaryStoreNode", func() (*pb.RetInfo, error) {
		rsp, err := meta.CreatePrimaryStoreNode(ctx, &pb.CreatePrimaryStoreNodeReq{Node: &pb.PrimaryStoreNode{NodeId: "bench_node_pebble", Name: "bench_node_pebble", Endpoint: "local", Status: "active"}})
		return rsp.GetRetInfo(), err
	}); err != nil {
		return err
	}
	if err := call("CreateDevice", func() (*pb.RetInfo, error) {
		rsp, err := meta.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "bench_device_pebble", NodeId: "bench_node_pebble", Name: "pebble", Engine: "pebble", Endpoint: "local", Status: "active"}})
		return rsp.GetRetInfo(), err
	}); err != nil {
		return err
	}

	markets := make(map[string]bool)
	for _, file := range files {
		markets[file.Market] = true
	}
	for market := range markets {
		market := market
		datasetID := datasetID(market)
		if err := call("CreateDataset:"+datasetID, func() (*pb.RetInfo, error) {
			rsp, err := meta.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataSourceId: dataSourceID, Name: benchDatasetName(market), DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{freq}, Status: "active"}})
			return rsp.GetRetInfo(), err
		}); err != nil {
			return err
		}
		for _, column := range columnsForMarket(market) {
			column := column
			column.SpaceId = spaceID
			column.DatasetId = datasetID
			if err := call("UpsertDatasetColumn:"+datasetID+"."+column.ColumnName, func() (*pb.RetInfo, error) {
				rsp, err := meta.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: column})
				return rsp.GetRetInfo(), err
			}); err != nil {
				return err
			}
		}
		if err := call("CreatePrimaryStoreRoute:"+datasetID, func() (*pb.RetInfo, error) {
			rsp, err := meta.CreatePrimaryStoreRoute(ctx, &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{SpaceId: spaceID, DatasetId: datasetID, SubjectPattern: "*", NodeId: "bench_node_pebble", Status: "active"}})
			return rsp.GetRetInfo(), err
		}); err != nil {
			return err
		}
	}
	for _, file := range files {
		file := file
		if err := call("RegisterDataSubject:"+file.Market+"."+file.SubjectID, func() (*pb.RetInfo, error) {
			rsp, err := meta.RegisterDataSubject(ctx, &pb.RegisterDataSubjectReq{
				SpaceId:        spaceID,
				DataSourceId:   dataSourceID,
				ExternalSymbol: strings.ReplaceAll(file.SubjectID, "-", ""),
				Subject:        &pb.Subject{SubjectId: file.SubjectID, SubjectType: "crypto_pair", Name: file.SubjectID, Market: "crypto", Currency: "USDT", Status: "active"},
				DatasetBindings: []*pb.DatasetSubject{
					{DatasetId: datasetID(file.Market), SubjectRole: "normal", Status: "active"},
				},
			})
			return rsp.GetRetInfo(), err
		}); err != nil {
			return err
		}
	}
	if opts.recordRows > 0 {
		if err := seedRecordMetadata(ctx, meta); err != nil {
			return err
		}
	}
	return nil
}

func seedRecordMetadata(ctx context.Context, meta pb.MetadataClientProxy) error {
	if err := call("CreateDataset:"+recordDatasetID, func() (*pb.RetInfo, error) {
		rsp, err := meta.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{SpaceId: spaceID, DatasetId: recordDatasetID, DataSourceId: dataSourceID, Name: "合成记录", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}})
		return rsp.GetRetInfo(), err
	}); err != nil {
		return err
	}
	for _, column := range recordColumns() {
		column := column
		if err := call("UpsertDatasetColumn:"+recordDatasetID+"."+column.ColumnName, func() (*pb.RetInfo, error) {
			rsp, err := meta.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: column})
			return rsp.GetRetInfo(), err
		}); err != nil {
			return err
		}
	}
	if err := call("CreatePrimaryStoreRoute:"+recordDatasetID, func() (*pb.RetInfo, error) {
		rsp, err := meta.CreatePrimaryStoreRoute(ctx, &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{SpaceId: spaceID, DatasetId: recordDatasetID, SubjectPattern: "*", NodeId: "bench_node_pebble", Status: "active"}})
		return rsp.GetRetInfo(), err
	}); err != nil {
		return err
	}
	return nil
}

func waitMetadataReady(ctx context.Context, data pb.PrimaryStoreClientProxy, files []bench.KlineFile, opts options) (metadataReadyReport, error) {
	report := metadataReadyReport{
		RefreshIntervalHint: metadataRefreshIntervalHint,
		TimeoutSeconds:      opts.metadataWait.Seconds(),
	}
	if opts.metadataWait == 0 {
		return report, nil
	}
	timeSeriesRows, err := metadataReadyTimeSeriesRows(files)
	if err != nil {
		return report, err
	}
	recordRows := metadataReadyRecordRows(opts)
	started := time.Now()
	deadline := started.Add(opts.metadataWait)
	var last error
	for {
		report.Attempts++
		last = runMetadataReadyProbe(ctx, data, timeSeriesRows, recordRows)
		report.DurationSeconds = time.Since(started).Seconds()
		if last == nil {
			return report, nil
		}
		if time.Now().Add(opts.metadataProbe).After(deadline) {
			return report, fmt.Errorf("metadata cache not ready after %.2fs and %d probes: %w", report.DurationSeconds, report.Attempts, last)
		}
		time.Sleep(opts.metadataProbe)
	}
}

func metadataReadyTimeSeriesRows(files []bench.KlineFile) ([]*pb.TimeSeriesRow, error) {
	rows := make([]*pb.TimeSeriesRow, 0, len(files))
	for _, file := range files {
		klineRows, err := bench.ReadKlineCSV(file, 1)
		if err != nil {
			return nil, err
		}
		if len(klineRows) == 0 {
			continue
		}
		converted := bench.KlineRowsToTimeSeriesRows(spaceID, datasetID(file.Market), file.SubjectID, freq, klineRows)
		rows = append(rows, converted...)
	}
	return rows, nil
}

func metadataReadyRecordRows(opts options) []*pb.RecordRow {
	if opts.recordRows == 0 {
		return nil
	}
	return syntheticRecordRows(1)
}

func runMetadataReadyProbe(ctx context.Context, data pb.PrimaryStoreClientProxy, timeSeriesRows []*pb.TimeSeriesRow, recordRows []*pb.RecordRow) error {
	if len(timeSeriesRows) > 0 {
		rsp, err := data.MergeTimeSeriesRows(ctx, &pb.MergeTimeSeriesRowsReq{Rows: timeSeriesRows})
		if err != nil {
			return err
		}
		if err := retErr("MergeTimeSeriesRows metadata ready probe", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	if len(recordRows) > 0 {
		rsp, err := data.MergeRecordRows(ctx, &pb.MergeRecordRowsReq{Rows: recordRows})
		if err != nil {
			return err
		}
		if err := retErr("MergeRecordRows metadata ready probe", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
}

func writeKlines(ctx context.Context, data pb.PrimaryStoreClientProxy, files []bench.KlineFile, opts options) (writeReport, map[string]int, []querySample, error) {
	var report writeReport
	var recorder bench.LatencyRecorder
	var batchDurations []time.Duration
	var batchRows []int
	datasetRows := make(map[string]int)
	var samples []querySample
	for _, file := range files {
		rows, err := bench.ReadKlineCSV(file, opts.rowLimit)
		if err != nil {
			return report, nil, nil, err
		}
		timeSeriesRows := bench.KlineRowsToTimeSeriesRows(spaceID, datasetID(file.Market), file.SubjectID, freq, rows)
		for _, row := range timeSeriesRows {
			key := row.GetKey()
			samples = append(samples, querySample{
				Market:    file.Market,
				DatasetID: datasetID(file.Market),
				SubjectID: file.SubjectID,
				DataTime:  key.GetDataTime(),
				RowID:     key.GetDataTime(),
			})
		}
		for start := 0; start < len(timeSeriesRows); start += opts.batchSize {
			end := start + opts.batchSize
			if end > len(timeSeriesRows) {
				end = len(timeSeriesRows)
			}
			req := &pb.MergeTimeSeriesRowsReq{Rows: timeSeriesRows[start:end]}
			begin := time.Now()
			if err := retry(30*time.Second, time.Second, func() error {
				rsp, err := data.MergeTimeSeriesRows(ctx, req)
				if err != nil {
					return err
				}
				return retErr("MergeTimeSeriesRows", rsp.GetRetInfo())
			}); err != nil {
				return report, nil, nil, err
			}
			elapsed := time.Since(begin)
			recorder.Add(elapsed)
			batchDurations = append(batchDurations, elapsed)
			batchRows = append(batchRows, end-start)
			report.Batches++
			report.Rows += end - start
		}
		datasetRows[datasetID(file.Market)] += len(timeSeriesRows)
	}
	finishWriteReport(&report, recorder, batchRows, batchDurations)
	return report, datasetRows, samples, nil
}

func mergeRecords(ctx context.Context, data pb.PrimaryStoreClientProxy, opts options) (writeReport, error) {
	var report writeReport
	if opts.recordRows == 0 {
		return report, nil
	}
	var recorder bench.LatencyRecorder
	var batchDurations []time.Duration
	var batchRows []int
	rows := syntheticRecordRows(opts.recordRows)
	for start := 0; start < len(rows); start += opts.batchSize {
		end := start + opts.batchSize
		if end > len(rows) {
			end = len(rows)
		}
		req := &pb.MergeRecordRowsReq{Rows: rows[start:end]}
		begin := time.Now()
		if err := retry(30*time.Second, time.Second, func() error {
			rsp, err := data.MergeRecordRows(ctx, req)
			if err != nil {
				return err
			}
			return retErr("MergeRecordRows", rsp.GetRetInfo())
		}); err != nil {
			return report, err
		}
		elapsed := time.Since(begin)
		recorder.Add(elapsed)
		batchDurations = append(batchDurations, elapsed)
		batchRows = append(batchRows, end-start)
		report.Batches++
		report.Rows += end - start
	}
	finishWriteReport(&report, recorder, batchRows, batchDurations)
	return report, nil
}

func createViews(ctx context.Context, meta pb.MetadataClientProxy, datasetRows map[string]int) error {
	for datasetID := range datasetRows {
		market := trimBenchmarkDatasetID(datasetID)
		viewID := viewID(market)
		if err := call("CreateView:"+viewID, func() (*pb.RetInfo, error) {
			rsp, err := meta.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{SpaceId: spaceID, ViewId: viewID, Name: benchViewName(market), PrimaryDatasetId: datasetID, DatasetIds: []string{datasetID}, FilterJson: `{"freq":"1h"}`, RetentionWindow: "4000d", Status: "active"}})
			return rsp.GetRetInfo(), err
		}); err != nil {
			return err
		}
		for _, name := range []string{"close", "volume", "quote_volume", "trade_num"} {
			name := name
			if err := call("UpsertViewColumn:"+viewID+"."+name, func() (*pb.RetInfo, error) {
				columnName := datasetID + "." + name
				rsp, err := meta.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{SpaceId: spaceID, ViewId: viewID, ColumnName: columnName, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: columnName, ValueType: valueTypeForColumn(name), Attributes: displayNameAttrs(name)}})
				return rsp.GetRetInfo(), err
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitViews(ctx context.Context, query pb.DataViewClientProxy, datasetRows map[string]int, timeout time.Duration) (map[string]uint64, error) {
	out := make(map[string]uint64)
	err := retry(timeout, 2*time.Second, func() error {
		for datasetID, want := range datasetRows {
			market := trimBenchmarkDatasetID(datasetID)
			rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{SpaceId: spaceID, ViewId: viewID(market), Page: &pb.Page{Size: 1}, TotalMode: pb.TotalMode_FORCE_EXACT})
			if err != nil {
				return err
			}
			if err := retErr("QueryTimeSeriesRows", rsp.GetRetInfo()); err != nil {
				return err
			}
			total := rsp.GetPageResult().GetTotal()
			out[datasetID] = uint64(total)
			if uint64(total) < uint64(want) {
				return fmt.Errorf("view %s materialized rows=%d, want %d", viewID(market), total, want)
			}
		}
		return nil
	})
	return out, err
}
