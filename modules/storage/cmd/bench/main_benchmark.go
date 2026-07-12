package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/bench"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type benchmarkReport struct {
	StartedAt         string                 `json:"started_at"`
	FinishedAt        string                 `json:"finished_at"`
	DurationSeconds   float64                `json:"duration_seconds"`
	ZipPath           string                 `json:"zip_path"`
	DataRoot          string                 `json:"data_root"`
	Warnings          []string               `json:"warnings,omitempty"`
	Files             []bench.KlineFile      `json:"files"`
	Datasets          []datasetInfo          `json:"datasets"`
	MetadataReady     metadataReadyReport    `json:"metadata_ready"`
	Write             writeReport            `json:"write"`
	RecordWrite       writeReport            `json:"record_write"`
	PrimaryRead       operationReport        `json:"primary_read"`
	PrimaryKlinePoint operationReport        `json:"primary_kline_point"`
	KlinePointTarget  querySample            `json:"kline_point_target"`
	DuckDBView        duckDBReport           `json:"duckdb_view"`
	ReportJSONPath    string                 `json:"report_json_path,omitempty"`
	ReportMarkdown    string                 `json:"report_markdown_path,omitempty"`
	StorageWorkDir    string                 `json:"storage_work_dir"`
	StorageLogPath    string                 `json:"storage_log_path"`
	Extra             map[string]interface{} `json:"extra,omitempty"`
}

func benchmarkPrimaryReads(ctx context.Context, data pb.AccessClientProxy, files []bench.KlineFile, opts options) (operationReport, error) {
	return runConcurrentBenchmark(opts.readRequests, opts.concurrency, func(worker int, request int) (int, error) {
		file := files[(worker+request)%len(files)]
		rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
			Keys: []*pb.TimeSeriesKey{{
				SpaceId:   spaceID,
				DatasetId: datasetID(file.Market),
				SubjectId: file.SubjectID,
				Freq:      freq,
			}},
			ColumnNames: []string{"close", "volume"},
			Page:        &pb.Page{Size: opts.pageSize},
		})
		if err != nil {
			return 0, err
		}
		if err := retErr("ReadTimeSeriesRows", rsp.GetRetInfo()); err != nil {
			return 0, err
		}
		return len(rsp.GetRows()), nil
	}, opts.pageSize)
}

func benchmarkPrimaryKlinePoint(ctx context.Context, data pb.AccessClientProxy, sample querySample, opts options) (operationReport, error) {
	return runConcurrentBenchmark(opts.klinePointRequests, opts.concurrency, func(worker int, request int) (int, error) {
		rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
			Keys: []*pb.TimeSeriesKey{{
				SpaceId:   spaceID,
				DatasetId: sample.DatasetID,
				SubjectId: sample.SubjectID,
				Freq:      freq,
			}},
			TimeRange:   exactTimeRange(sample.DataTime),
			ColumnNames: []string{"close", "volume"},
			Page:        &pb.Page{Size: 1},
		})
		if err != nil {
			return 0, err
		}
		if err := retErr("ReadTimeSeriesRows kline point", rsp.GetRetInfo()); err != nil {
			return 0, err
		}
		if len(rsp.GetRows()) != 1 {
			return 0, fmt.Errorf("kline point read %s/%s/%s returned %d rows, want 1", sample.Market, sample.SubjectID, sample.DataTime, len(rsp.GetRows()))
		}
		return len(rsp.GetRows()), nil
	}, 1)
}

func benchmarkViewReads(ctx context.Context, query pb.DataViewClientProxy, files []bench.KlineFile, opts options) (operationReport, error) {
	return runConcurrentBenchmark(opts.viewRequests, opts.concurrency, func(worker int, request int) (int, error) {
		file := files[(worker+request)%len(files)]
		dataset := datasetID(file.Market)
		rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			SpaceId:     spaceID,
			ViewId:      viewID(file.Market),
			Keys:        []*pb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: dataset, SubjectId: file.SubjectID, Freq: freq}},
			ColumnNames: []string{dataset + ".close", dataset + ".volume"},
			Page:        &pb.Page{Size: opts.pageSize},
		})
		if err != nil {
			return 0, err
		}
		if err := retErr("QueryTimeSeriesRows", rsp.GetRetInfo()); err != nil {
			return 0, err
		}
		return len(rsp.GetRows()), nil
	}, opts.pageSize)
}

func exactTimeRange(value string) *pb.TimeRange {
	return &pb.TimeRange{StartTime: value, EndTime: value}
}

func selectKlinePointTarget(samples []querySample, opts options) (querySample, error) {
	if len(samples) == 0 {
		return querySample{}, errors.New("no query samples available")
	}
	wantTime, err := normalizeTargetTime(opts.targetTime)
	if err != nil {
		return querySample{}, err
	}
	for _, sample := range samples {
		if opts.targetMarket != "" && sample.Market != opts.targetMarket {
			continue
		}
		if opts.targetSubject != "" && sample.SubjectID != opts.targetSubject {
			continue
		}
		if wantTime != "" && sample.DataTime != wantTime {
			continue
		}
		return sample, nil
	}
	return querySample{}, fmt.Errorf("no K-line sample matches market=%q subject=%q time=%q", opts.targetMarket, opts.targetSubject, opts.targetTime)
}

func normalizeTargetTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(time.RFC3339), nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC); err == nil {
		return parsed.UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("target-time %q must be RFC3339 or 2006-01-02 15:04:05", value)
}

func runConcurrentBenchmark(requests int, concurrency int, fn func(worker int, request int) (int, error), pageSize uint32) (operationReport, error) {
	if requests == 0 {
		return operationReport{PageSize: pageSize, Concurrency: concurrency}, nil
	}
	started := time.Now()
	var recorder bench.LatencyRecorder
	var mu sync.Mutex
	var firstErr error
	var rows int
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for request := range jobs {
				begin := time.Now()
				gotRows, err := fn(worker, request)
				elapsed := time.Since(begin)
				mu.Lock()
				recorder.Add(elapsed)
				rows += gotRows
				if err != nil && firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return operationReport{}, firstErr
	}
	duration := time.Since(started).Seconds()
	report := operationReport{Requests: requests, Concurrency: concurrency, PageSize: pageSize, RowsReturned: rows, DurationSeconds: duration, Latency: recorder.Summary()}
	if duration > 0 {
		report.RequestsPerSec = float64(requests) / duration
		report.RowsPerSecond = float64(rows) / duration
	}
	return report, nil
}

func trimBenchmarkDatasetID(id string) string {
	return strings.TrimSuffix(strings.TrimPrefix(id, "bench_"), "_1h")
}

func call(name string, fn func() (*pb.RetInfo, error)) error {
	ret, err := fn()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return retErr(name, ret)
}

func retErr(name string, ret *pb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s failed: missing ret_info", name)
	}
	if ret.GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s failed: %s", name, ret.GetMsg())
	}
	return nil
}

func retry(timeout time.Duration, interval time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		last = fn()
		if last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(interval)
	}
}
