package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/bench"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	spaceID                     = "crypto_bench"
	dataSourceID                = "binance"
	freq                        = "1h"
	recordDatasetID             = "bench_records"
	metadataRefreshIntervalHint = "snapshotcache 默认 10s"
)

type options struct {
	zipPath            string
	dataDir            string
	workDir            string
	moduleDir          string
	reportDir          string
	rowLimit           int
	recordRows         int
	batchSize          int
	readRequests       int
	klinePointRequests int
	viewRequests       int
	concurrency        int
	pageSize           uint32
	viewWait           time.Duration
	metadataWait       time.Duration
	metadataProbe      time.Duration
	keepWorkDir        bool
	targetMarket       string
	targetSubject      string
	targetTime         string
}

type servicePorts struct {
	admin    int
	data     int
	scan     int
	metadata int
	query    int
	primary  int
	index    int
	timer    int
}

type datasetInfo struct {
	Market    string `json:"market"`
	DatasetID string `json:"dataset_id"`
	ViewID    string `json:"view_id"`
	Rows      int    `json:"rows"`
}

type querySample struct {
	Market    string `json:"market"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	DataTime  string `json:"data_time"`
	RowID     string `json:"row_id"`
}

func run(ctx context.Context) error {
	opts := parseOptions()
	started := time.Now()
	if opts.workDir == "" {
		dir, err := os.MkdirTemp("", "moox-storage-bench-")
		if err != nil {
			return err
		}
		opts.workDir = dir
	}
	if opts.moduleDir == "" {
		moduleDir, err := locateModuleDir()
		if err != nil {
			return err
		}
		opts.moduleDir = moduleDir
	}
	if opts.reportDir == "" {
		opts.reportDir = filepath.Join(opts.moduleDir, "docs", "bench-reports")
	}
	if err := os.MkdirAll(opts.reportDir, 0o755); err != nil {
		return err
	}

	dataRoot, warnings, err := prepareData(ctx, opts)
	if err != nil {
		return err
	}
	files, err := bench.DiscoverKlineFiles(dataRoot, freq)
	if err != nil {
		return err
	}

	env, err := newServiceEnv(opts)
	if err != nil {
		return err
	}
	if !opts.keepWorkDir {
		defer func() { _ = os.RemoveAll(env.workDir) }()
	}
	if err := env.Start(ctx, opts.moduleDir); err != nil {
		return err
	}
	defer func() { _ = env.Stop() }()

	meta := pb.NewMetadataClientProxy(targetOpts(env.ports.metadata)...)
	data := pb.NewPrimaryStoreClientProxy(targetOpts(env.ports.data)...)
	query := pb.NewDataViewClientProxy(targetOpts(env.ports.query)...)

	if err := seedMetadata(ctx, meta, files, opts); err != nil {
		return err
	}
	metadataReady, err := waitMetadataReady(ctx, data, files, opts)
	if err != nil {
		return err
	}
	writeStats, datasetRows, samples, err := writeKlines(ctx, data, files, opts)
	if err != nil {
		return err
	}
	recordWriteStats, err := mergeRecords(ctx, data, opts)
	if err != nil {
		return err
	}
	if err := createViews(ctx, meta, datasetRows); err != nil {
		return err
	}
	materialized, err := waitViews(ctx, query, datasetRows, opts.viewWait)
	if err != nil {
		return err
	}

	primaryRead, err := benchmarkPrimaryReads(ctx, data, files, opts)
	if err != nil {
		return err
	}
	klinePointTarget, err := selectKlinePointTarget(samples, opts)
	if err != nil {
		return err
	}
	primaryKlinePoint, err := benchmarkPrimaryKlinePoint(ctx, data, klinePointTarget, opts)
	if err != nil {
		return err
	}
	viewRead, err := benchmarkViewReads(ctx, query, files, opts)
	if err != nil {
		return err
	}

	datasets := make([]datasetInfo, 0, len(datasetRows))
	for datasetID, rows := range datasetRows {
		market := trimBenchmarkDatasetID(datasetID)
		datasets = append(datasets, datasetInfo{Market: market, DatasetID: datasetID, ViewID: viewID(market), Rows: rows})
	}
	sort.Slice(datasets, func(i, j int) bool { return datasets[i].DatasetID < datasets[j].DatasetID })

	report := benchmarkReport{
		StartedAt:         started.UTC().Format(time.RFC3339),
		FinishedAt:        time.Now().UTC().Format(time.RFC3339),
		DurationSeconds:   time.Since(started).Seconds(),
		ZipPath:           opts.zipPath,
		DataRoot:          dataRoot,
		Warnings:          warnings,
		Files:             files,
		Datasets:          datasets,
		MetadataReady:     metadataReady,
		Write:             writeStats,
		RecordWrite:       recordWriteStats,
		PrimaryRead:       primaryRead,
		PrimaryKlinePoint: primaryKlinePoint,
		KlinePointTarget:  klinePointTarget,
		DuckDBView: duckDBReport{
			Verified:       true,
			ExpectedRows:   sumDatasetRows(datasetRows),
			Materialized:   materialized,
			QueryBenchmark: viewRead,
		},
		StorageWorkDir: env.workDir,
		StorageLogPath: env.logPath,
	}
	if err := writeReports(opts.reportDir, &report); err != nil {
		return err
	}
	fmt.Printf("Benchmark report written:\n- %s\n- %s\n", report.ReportMarkdown, report.ReportJSONPath)
	return nil
}

func localizedExtractOutput(output string) string {
	text := strings.TrimSpace(output)
	if text == "" {
		return ""
	}
	text = regexp.MustCompile(`needed ([0-9]+) bytes, only ([0-9]+) available`).ReplaceAllString(text, "需要 $1 字节，但文件仅有 $2 字节")
	text = strings.ReplaceAll(text, "bsdtar: Truncated input file", "bsdtar: 输入文件疑似截断")
	text = strings.ReplaceAll(text, "bsdtar: Error exit delayed from previous errors.", "bsdtar: 因前述错误退出。")
	return "解压工具输出: " + text
}

func localizedCommandError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if strings.HasPrefix(text, "exit status ") {
		return "退出码 " + strings.TrimPrefix(text, "exit status ")
	}
	return text
}

const storageConfigTemplate = `
storage:
  root: %s
  roles:
    - primary
    - view
  metadata:
    path: %s
  devices:
    pebble_path: %s
    view_index_root: %s
  primary:
    service_name: ""
  eventbus:
    type: memory
    stream_name: MOOX_STORAGE_BENCH
`

const trpcConfigTemplate = `global:
  namespace: Development
  env_name: bench

server:
  timeout: 10000
  admin:
    ip: 127.0.0.1
    port: %d
    read_timeout: 5000
    write_timeout: 60000
  service:
    - name: trpc.moox.storage.PrimaryStore
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.moox.storage.PrimaryStoreScan.trpc
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
      timeout: 120000
    - name: trpc.moox.storage.DataView
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.moox.storage.PrimaryStore
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.moox.storage.Metadata
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.moox.storage.ViewIndex
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.moox.storage.view.timer
      port: %d
      network: "*/5 * * * * *?scheduler=viewBuilderSchedule&startAtOnce=1"
      protocol: timer
      timeout: 60000

plugins:
  log:
    default:
      - writer: console
        level: info
`

func datasetID(market string) string {
	return "bench_" + market + "_1h"
}

func viewID(market string) string {
	return "bench_" + market + "_kline_view"
}
