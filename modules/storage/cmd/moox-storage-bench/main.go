package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/bench"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	spaceID                     = "crypto_bench"
	dataSourceID                = "binance"
	freq                        = "1h"
	recordDatasetID             = "bench_synthetic_records"
	metadataRefreshIntervalHint = "snapshotcache 默认 10s"
)

// options 保存压测命令行解析后的运行参数。
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

// serviceEnv 保存压测期间拉起 Storage 服务所需的进程和路径。
type serviceEnv struct {
	workDir    string
	binPath    string
	configPath string
	storageCfg string
	logPath    string
	ports      servicePorts
	cmd        *exec.Cmd
	logFile    *os.File
}

// servicePorts 保存压测服务监听的端口集合。
type servicePorts struct {
	admin    int
	data     int
	metadata int
	query    int
	primary  int
	timer    int
}

// datasetInfo 描述压测报告中的数据集与视图信息。
type datasetInfo struct {
	Market    string `json:"market"`
	DatasetID string `json:"dataset_id"`
	ViewID    string `json:"view_id"`
	Rows      int    `json:"rows"`
}

// querySample 描述压测中用于点查的一条样本数据。
type querySample struct {
	Market    string `json:"market"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	DataTime  string `json:"data_time"`
	RowID     string `json:"row_id"`
}

// benchmarkReport 汇总一次 Storage 压测的输入、结果和产物路径。
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

// metadataReadyReport 记录等待元数据缓存可读的耗时与重试信息。
type metadataReadyReport struct {
	DurationSeconds     float64 `json:"duration_seconds"`
	Attempts            int     `json:"attempts"`
	RefreshIntervalHint string  `json:"refresh_interval_hint"`
	TimeoutSeconds      float64 `json:"timeout_seconds"`
}

// writeReport 记录一类写入压测的吞吐与延迟指标。
type writeReport struct {
	Rows             int                  `json:"rows"`
	Batches          int                  `json:"batches"`
	DurationSeconds  float64              `json:"duration_seconds"`
	RowsPerSecond    float64              `json:"rows_per_second"`
	SteadyRows       int                  `json:"steady_rows,omitempty"`
	SteadySeconds    float64              `json:"steady_seconds,omitempty"`
	SteadyRowsPerSec float64              `json:"steady_rows_per_second,omitempty"`
	SlowestBatchMS   float64              `json:"slowest_batch_ms,omitempty"`
	BatchLatency     bench.LatencySummary `json:"batch_latency"`
}

// operationReport 记录一类读查询压测的吞吐与延迟指标。
type operationReport struct {
	Requests        int                  `json:"requests"`
	Concurrency     int                  `json:"concurrency"`
	PageSize        uint32               `json:"page_size"`
	RowsReturned    int                  `json:"rows_returned"`
	DurationSeconds float64              `json:"duration_seconds"`
	RequestsPerSec  float64              `json:"requests_per_second"`
	RowsPerSecond   float64              `json:"rows_per_second"`
	Latency         bench.LatencySummary `json:"latency"`
}

// duckDBReport 记录 DuckDB 视图物化校验和查询压测结果。
type duckDBReport struct {
	Verified       bool              `json:"verified"`
	ExpectedRows   int               `json:"expected_rows"`
	Materialized   map[string]uint64 `json:"materialized_rows"`
	QueryBenchmark operationReport   `json:"query_benchmark"`
}

func main() {
	if err := run(trpc.BackgroundContext()); err != nil {
		fmt.Fprintf(os.Stderr, "moox-storage-bench failed: %v\n", err)
		os.Exit(1)
	}
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

	meta := pb.NewMetadataServiceClientProxy(targetOpts(env.ports.metadata)...)
	data := pb.NewAccessServiceClientProxy(targetOpts(env.ports.data)...)
	query := pb.NewViewServiceClientProxy(targetOpts(env.ports.query)...)

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
	recordWriteStats, err := writeRecords(ctx, data, opts)
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
		market := strings.TrimSuffix(strings.TrimPrefix(datasetID, "bench_binance_"), "_kline_1h")
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

func parseOptions() options {
	home, _ := os.UserHomeDir()
	defaultZip := filepath.Join(home, "Downloads", "coin-binance-spot-swap-preprocess-pkl-1h-5m-2026-03-03.zip")
	var opts options
	pageSize := uint(1000)
	flag.StringVar(&opts.zipPath, "zip", defaultZip, "K-line zip path")
	flag.StringVar(&opts.dataDir, "data-dir", "", "pre-extracted data root; skips zip extraction when set")
	flag.StringVar(&opts.workDir, "work-dir", "", "working directory; defaults to a temp dir")
	flag.StringVar(&opts.moduleDir, "module-dir", "", "modules/storage directory; defaults to current module")
	flag.StringVar(&opts.reportDir, "report-dir", "", "report output directory; defaults to ./docs/bench-reports")
	flag.IntVar(&opts.rowLimit, "row-limit", 0, "max rows per CSV; 0 means all")
	flag.IntVar(&opts.recordRows, "record-rows", 1000, "synthetic non-time-series record rows to write; 0 disables record write benchmark")
	flag.IntVar(&opts.batchSize, "batch-size", 1000, "write batch size")
	flag.IntVar(&opts.readRequests, "read-requests", 200, "primary ReadTimeSeriesRows requests")
	flag.IntVar(&opts.klinePointRequests, "kline-point-requests", 200, "single K-line time-point ReadTimeSeriesRows requests")
	flag.IntVar(&opts.viewRequests, "view-requests", 200, "DuckDB QueryTimeSeriesRows requests")
	flag.IntVar(&opts.concurrency, "concurrency", 4, "read benchmark concurrency")
	flag.UintVar(&pageSize, "page-size", pageSize, "read/query page size")
	flag.DurationVar(&opts.viewWait, "view-wait", 2*time.Minute, "max wait for async DuckDB view materialization")
	flag.DurationVar(&opts.metadataWait, "metadata-wait", 35*time.Second, "max wait for metadata cache to expose seeded datasets/routes before measured writes")
	flag.DurationVar(&opts.metadataProbe, "metadata-probe-interval", time.Second, "metadata cache readiness probe interval")
	flag.BoolVar(&opts.keepWorkDir, "keep-work-dir", false, "keep storage working directory")
	flag.StringVar(&opts.targetMarket, "target-market", "", "market for single K-line benchmark, such as spot or swap")
	flag.StringVar(&opts.targetSubject, "target-subject", "", "subject for single K-line benchmark, such as BTC-USDT")
	flag.StringVar(&opts.targetTime, "target-time", "", "RFC3339 or CSV time for single K-line benchmark")
	flag.Parse()
	if opts.batchSize <= 0 {
		opts.batchSize = 1000
	}
	if opts.concurrency <= 0 {
		opts.concurrency = 1
	}
	if opts.readRequests < 0 {
		opts.readRequests = 0
	}
	if opts.klinePointRequests < 0 {
		opts.klinePointRequests = 0
	}
	if opts.viewRequests < 0 {
		opts.viewRequests = 0
	}
	if opts.recordRows < 0 {
		opts.recordRows = 0
	}
	if opts.metadataWait < 0 {
		opts.metadataWait = 0
	}
	if opts.metadataProbe <= 0 {
		opts.metadataProbe = time.Second
	}
	opts.pageSize = uint32(pageSize)
	return opts
}

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

func newServiceEnv(opts options) (*serviceEnv, error) {
	ports, err := allocatePorts(6)
	if err != nil {
		return nil, err
	}
	workDir := filepath.Join(opts.workDir, "storage")
	return &serviceEnv{
		workDir:    workDir,
		binPath:    filepath.Join(workDir, "moox-storage"),
		configPath: filepath.Join(workDir, "trpc_go.yaml"),
		storageCfg: filepath.Join(workDir, "storage.yaml"),
		logPath:    filepath.Join(workDir, "server.log"),
		ports: servicePorts{
			admin: ports[0], data: ports[1], metadata: ports[2], query: ports[3], primary: ports[4], timer: ports[5],
		},
	}, nil
}

func (e *serviceEnv) Start(ctx context.Context, moduleDir string) error {
	if err := os.MkdirAll(e.workDir, 0o755); err != nil {
		return err
	}
	if err := e.writeConfig(); err != nil {
		return err
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", e.binPath, "./cmd/moox-storage")
	build.Dir = moduleDir
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build moox-storage failed: %w\n%s", err, out)
	}
	initCmd := exec.CommandContext(ctx, e.binPath, "-conf="+e.configPath, "-init-metadata")
	initCmd.Dir = e.workDir
	initCmd.Env = e.childEnv(moduleDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("init metadata failed: %w\n%s", err, out)
	}
	logFile, err := os.Create(e.logPath)
	if err != nil {
		return err
	}
	e.logFile = logFile
	cmd := exec.Command(e.binPath, "-conf="+e.configPath)
	cmd.Dir = e.workDir
	cmd.Env = e.childEnv(moduleDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	e.cmd = cmd
	if err := waitPorts([]int{e.ports.data, e.ports.metadata, e.ports.query, e.ports.primary}, time.Minute); err != nil {
		_ = e.Stop()
		return fmt.Errorf("%w\n----- server.log -----\n%s", err, e.tailLog())
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}

func (e *serviceEnv) Stop() error {
	var first error
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() {
			_, err := e.cmd.Process.Wait()
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, os.ErrProcessDone) {
				first = err
			}
		case <-time.After(5 * time.Second):
			if err := e.cmd.Process.Kill(); err != nil {
				first = err
			}
			<-done
		}
	}
	if e.logFile != nil {
		if err := e.logFile.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (e *serviceEnv) childEnv(moduleDir string) []string {
	return append(os.Environ(),
		"STORAGE_SCHEMA_FILE="+filepath.Join(moduleDir, "schema", "metadata.sql"),
		"MOOX_STORAGE_CONFIG="+e.storageCfg,
	)
}

func (e *serviceEnv) writeConfig() error {
	storageRoot := filepath.Join(e.workDir, "var", "storage")
	if err := os.MkdirAll(storageRoot, 0o755); err != nil {
		return err
	}
	storageCfg := fmt.Sprintf(storageConfigTemplate,
		storageRoot,
		filepath.Join(storageRoot, "metadata", "metadata.db"),
		filepath.Join(storageRoot, "pebble"),
		filepath.Join(storageRoot, "duckdb", "views.duckdb"),
		filepath.Join(storageRoot, "bleve"),
		filepath.Join(storageRoot, "archive"),
	)
	if err := os.WriteFile(e.storageCfg, []byte(storageCfg), 0o644); err != nil {
		return err
	}
	trpcCfg := fmt.Sprintf(trpcConfigTemplate,
		e.ports.admin, e.ports.data, e.ports.query, e.ports.primary, e.ports.metadata, e.ports.timer,
	)
	return os.WriteFile(e.configPath, []byte(trpcCfg), 0o644)
}

func (e *serviceEnv) tailLog() string {
	data, err := os.ReadFile(e.logPath)
	if err != nil {
		return ""
	}
	if len(data) > 8000 {
		data = data[len(data)-8000:]
	}
	return string(data)
}

const storageConfigTemplate = `
storage:
  root: %s
  metadata:
    path: %s
  devices:
    pebble_path: %s
    duckdb_path: %s
    bleve_path: %s
    parquet_path: %s
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
    - name: trpc.storage.access.AccessService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.storage.view.ViewService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.storage.store.PrimaryStoreService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.storage.metadata.MetadataService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.storage.view.timer
      port: %d
      network: "*/5 * * * * *?scheduler=viewBuilderSchedule&startAtOnce=1&params="
      protocol: timer
      timeout: 60000

plugins:
  log:
    default:
      - writer: console
        level: info
`

func seedMetadata(ctx context.Context, meta pb.MetadataServiceClientProxy, files []bench.KlineFile, opts options) error {
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

func seedRecordMetadata(ctx context.Context, meta pb.MetadataServiceClientProxy) error {
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

func waitMetadataReady(ctx context.Context, data pb.AccessServiceClientProxy, files []bench.KlineFile, opts options) (metadataReadyReport, error) {
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

func runMetadataReadyProbe(ctx context.Context, data pb.AccessServiceClientProxy, timeSeriesRows []*pb.TimeSeriesRow, recordRows []*pb.RecordRow) error {
	if len(timeSeriesRows) > 0 {
		rsp, err := data.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: timeSeriesRows})
		if err != nil {
			return err
		}
		if err := retErr("WriteTimeSeriesRows metadata ready probe", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	if len(recordRows) > 0 {
		rsp, err := data.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: recordRows})
		if err != nil {
			return err
		}
		if err := retErr("WriteRecordRows metadata ready probe", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
}

func writeKlines(ctx context.Context, data pb.AccessServiceClientProxy, files []bench.KlineFile, opts options) (writeReport, map[string]int, []querySample, error) {
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
			req := &pb.WriteTimeSeriesRowsReq{Rows: timeSeriesRows[start:end]}
			begin := time.Now()
			if err := retry(30*time.Second, time.Second, func() error {
				rsp, err := data.WriteTimeSeriesRows(ctx, req)
				if err != nil {
					return err
				}
				return retErr("WriteTimeSeriesRows", rsp.GetRetInfo())
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

func writeRecords(ctx context.Context, data pb.AccessServiceClientProxy, opts options) (writeReport, error) {
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
		req := &pb.WriteRecordRowsReq{Rows: rows[start:end]}
		begin := time.Now()
		if err := retry(30*time.Second, time.Second, func() error {
			rsp, err := data.WriteRecordRows(ctx, req)
			if err != nil {
				return err
			}
			return retErr("WriteRecordRows", rsp.GetRetInfo())
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

func finishWriteReport(report *writeReport, recorder bench.LatencyRecorder, batchRows []int, batchDurations []time.Duration) {
	report.BatchLatency = recorder.Summary()
	var total time.Duration
	var slowest time.Duration
	slowestIndex := -1
	for i, elapsed := range batchDurations {
		total += elapsed
		if elapsed > slowest {
			slowest = elapsed
			slowestIndex = i
		}
	}
	report.DurationSeconds = total.Seconds()
	if report.DurationSeconds > 0 {
		report.RowsPerSecond = float64(report.Rows) / report.DurationSeconds
	}
	report.SlowestBatchMS = float64(slowest) / float64(time.Millisecond)
	if len(batchDurations) <= 1 || slowestIndex < 0 {
		return
	}
	steadyRows := report.Rows - batchRows[slowestIndex]
	steadyDuration := total - slowest
	if steadyRows <= 0 || steadyDuration <= 0 {
		return
	}
	report.SteadyRows = steadyRows
	report.SteadySeconds = steadyDuration.Seconds()
	report.SteadyRowsPerSec = float64(steadyRows) / report.SteadySeconds
}

func createViews(ctx context.Context, meta pb.MetadataServiceClientProxy, datasetRows map[string]int) error {
	for datasetID := range datasetRows {
		market := strings.TrimSuffix(strings.TrimPrefix(datasetID, "bench_binance_"), "_kline_1h")
		viewID := viewID(market)
		if err := call("CreateView:"+viewID, func() (*pb.RetInfo, error) {
			rsp, err := meta.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{SpaceId: spaceID, ViewId: viewID, Name: benchViewName(market), PrimaryDatasetId: datasetID, DatasetIds: []string{datasetID}, QueryWindow: "4000d", Status: "active"}})
			return rsp.GetRetInfo(), err
		}); err != nil {
			return err
		}
		for _, name := range []string{"close", "volume", "quote_volume", "trade_num"} {
			name := name
			if err := call("UpsertViewColumn:"+viewID+"."+name, func() (*pb.RetInfo, error) {
				rsp, err := meta.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{SpaceId: spaceID, ViewId: viewID, ColumnName: name, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: datasetID + "." + name, ValueType: valueTypeForColumn(name), Attributes: displayNameAttrs(name)}})
				return rsp.GetRetInfo(), err
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitViews(ctx context.Context, query pb.ViewServiceClientProxy, datasetRows map[string]int, timeout time.Duration) (map[string]uint64, error) {
	out := make(map[string]uint64)
	err := retry(timeout, 2*time.Second, func() error {
		for datasetID, want := range datasetRows {
			market := strings.TrimSuffix(strings.TrimPrefix(datasetID, "bench_binance_"), "_kline_1h")
			rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{SpaceId: spaceID, ViewId: viewID(market), Page: &pb.Page{Size: 1}})
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

func benchmarkPrimaryReads(ctx context.Context, data pb.AccessServiceClientProxy, files []bench.KlineFile, opts options) (operationReport, error) {
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

func benchmarkPrimaryKlinePoint(ctx context.Context, data pb.AccessServiceClientProxy, sample querySample, opts options) (operationReport, error) {
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

func benchmarkViewReads(ctx context.Context, query pb.ViewServiceClientProxy, files []bench.KlineFile, opts options) (operationReport, error) {
	return runConcurrentBenchmark(opts.viewRequests, opts.concurrency, func(worker int, request int) (int, error) {
		file := files[(worker+request)%len(files)]
		rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			SpaceId:     spaceID,
			ViewId:      viewID(file.Market),
			Keys:        []*pb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: datasetID(file.Market), SubjectId: file.SubjectID, Freq: freq}},
			ColumnNames: []string{"close", "volume"},
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

func writeReports(dir string, report *benchmarkReport) error {
	stamp := time.Now().Format("20060102-150405")
	jsonPath := filepath.Join(dir, "storage-bench-"+stamp+".json")
	mdPath := filepath.Join(dir, "storage-bench-"+stamp+".md")
	report.ReportJSONPath = jsonPath
	report.ReportMarkdown = mdPath
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(mdPath, []byte(markdownReport(*report)), 0o644)
}

func markdownReport(report benchmarkReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# MooX Storage 压测报告\n\n")
	fmt.Fprintf(&b, "- 开始时间: `%s`\n- 结束时间: `%s`\n- 总耗时: `%.2fs`\n- 数据目录: `%s`\n- 数据压缩包: `%s`\n\n", report.StartedAt, report.FinishedAt, report.DurationSeconds, report.DataRoot, report.ZipPath)
	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "## 注意事项\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "## 数据集\n\n| 市场 | Dataset | View | 行数 |\n| --- | --- | --- | ---: |\n")
	for _, dataset := range report.Datasets {
		fmt.Fprintf(&b, "| %s | `%s` | `%s` | %d |\n", dataset.Market, dataset.DatasetID, dataset.ViewID, dataset.Rows)
	}
	fmt.Fprintf(&b, "\n")
	appendMetadataReadySection(&b, report.MetadataReady)
	appendWriteSection(&b, "## 时序 K 线写入性能", "场景: `WriteTimeSeriesRows`，K 线数据按 `subject_id + freq + data_time` 写入", report.Write)
	appendWriteSection(&b, "## 非时序记录写入性能", fmt.Sprintf("场景: `WriteRecordRows`，合成记录数据写入 `%s`，按 `record_id + version` 定位", recordDatasetID), report.RecordWrite)
	fmt.Fprintf(&b, "## Primary 主存读取性能\n\n- 场景: `ReadTimeSeriesRows` 首页读取，默认每次返回 `%d` 行\n- 请求数: `%d`\n- 并发数: `%d`\n- 返回行数: `%d`\n- 平均请求吞吐: `%.2f req/s`\n- 平均行吞吐: `%.2f rows/s`\n", report.PrimaryRead.PageSize, report.PrimaryRead.Requests, report.PrimaryRead.Concurrency, report.PrimaryRead.RowsReturned, report.PrimaryRead.RequestsPerSec, report.PrimaryRead.RowsPerSecond)
	appendLatencyLines(&b, "读取", report.PrimaryRead.Latency)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "## Primary 单根 K 线查询性能\n\n- 场景: `ReadTimeSeriesRows` + 固定标的 + 固定 `data_time` 精确 TimeRange，每次返回 1 根 K 线\n- 目标市场: `%s`\n- 目标标的: `%s`\n- 目标时间: `%s`\n- 请求数: `%d`\n- 并发数: `%d`\n- 返回行数: `%d`\n- 平均请求吞吐: `%.2f req/s`\n- 平均行吞吐: `%.2f rows/s`\n", report.KlinePointTarget.Market, report.KlinePointTarget.SubjectID, report.KlinePointTarget.DataTime, report.PrimaryKlinePoint.Requests, report.PrimaryKlinePoint.Concurrency, report.PrimaryKlinePoint.RowsReturned, report.PrimaryKlinePoint.RequestsPerSec, report.PrimaryKlinePoint.RowsPerSecond)
	appendLatencyLines(&b, "查询", report.PrimaryKlinePoint.Latency)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "## DuckDB 视图读取性能\n\n- 异步物化验证: `%t`\n- 预期物化行数: `%d`\n\n| Dataset | DuckDB 物化行数 |\n| --- | ---: |\n", report.DuckDBView.Verified, report.DuckDBView.ExpectedRows)
	keys := make([]string, 0, len(report.DuckDBView.Materialized))
	for key := range report.DuckDBView.Materialized {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "| `%s` | %d |\n", key, report.DuckDBView.Materialized[key])
	}
	fmt.Fprintf(&b, "\n- QueryTimeSeriesRows 请求数: `%d`\n- 并发数: `%d`\n- 返回行数: `%d`\n- 平均查询吞吐: `%.2f req/s`\n- 平均行吞吐: `%.2f rows/s`\n", report.DuckDBView.QueryBenchmark.Requests, report.DuckDBView.QueryBenchmark.Concurrency, report.DuckDBView.QueryBenchmark.RowsReturned, report.DuckDBView.QueryBenchmark.RequestsPerSec, report.DuckDBView.QueryBenchmark.RowsPerSecond)
	appendLatencyLines(&b, "查询", report.DuckDBView.QueryBenchmark.Latency)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "## 运行环境\n\n- Storage 工作目录: `%s`\n- Storage 日志: `%s`\n", report.StorageWorkDir, report.StorageLogPath)
	return b.String()
}

func appendMetadataReadySection(b *strings.Builder, report metadataReadyReport) {
	fmt.Fprintf(b, "## 元数据缓存预热\n\n")
	fmt.Fprintf(b, "- 缓存刷新周期参考: `%s`\n", report.RefreshIntervalHint)
	fmt.Fprintf(b, "- 等待超时: `%.2fs`\n", report.TimeoutSeconds)
	fmt.Fprintf(b, "- 等待耗时: `%.2fs`\n", report.DurationSeconds)
	fmt.Fprintf(b, "- 探测轮次: `%d`\n", report.Attempts)
	fmt.Fprintf(b, "- 说明: `正式写入压测在 Access 校验与路由可见后开始，预热探针不计入写入性能`\n\n")
}

func appendWriteSection(b *strings.Builder, title string, scene string, report writeReport) {
	fmt.Fprintf(b, "%s\n\n- %s\n- 写入行数: `%d`\n- 写入批次: `%d`\n- 写入耗时: `%.2fs`\n- 平均写入吞吐: `%.2f rows/s`\n- 最慢批次: `%.2f ms`\n",
		title, scene, report.Rows, report.Batches, report.DurationSeconds, report.RowsPerSecond, report.SlowestBatchMS)
	appendLatencyLines(b, "批次", report.BatchLatency)
	if report.SteadyRows > 0 {
		fmt.Fprintf(b, "- 稳态写入行数: `%d`\n- 稳态写入耗时: `%.2fs`\n- 稳态吞吐（排除最慢批次）: `%.2f rows/s`\n", report.SteadyRows, report.SteadySeconds, report.SteadyRowsPerSec)
	}
	fmt.Fprintf(b, "\n")
}

func appendLatencyLines(b *strings.Builder, label string, latency bench.LatencySummary) {
	fmt.Fprintf(b, "- 平均延迟: `%.2f ms`\n", latency.AvgMS)
	fmt.Fprintf(b, "- P50 延迟: `%.2f ms`\n", latency.P50MS)
	fmt.Fprintf(b, "- P95 延迟: `%.2f ms`\n", latency.P95MS)
	fmt.Fprintf(b, "- P99 延迟: `%.2f ms`\n", latency.P99MS)
	if label != "" {
		fmt.Fprintf(b, "- %s样本数: `%d`\n", label, latency.Count)
	}
}

func fieldDefs() []*pb.Field {
	defs := []struct {
		name string
		typ  pb.FieldValueType
	}{
		{"open", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"high", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"low", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"quote_volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"trade_num", pb.FieldValueType_FIELD_VALUE_TYPE_INT},
		{"taker_buy_base_asset_volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"taker_buy_quote_asset_volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"symbol", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{"avg_price_1m", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"avg_price_5m", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"fundingRate", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"title", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{"status", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{"score", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"updated_at", pb.FieldValueType_FIELD_VALUE_TYPE_TIME},
		{"payload_json", pb.FieldValueType_FIELD_VALUE_TYPE_JSON},
	}
	out := make([]*pb.Field, 0, len(defs))
	for _, def := range defs {
		out = append(out, &pb.Field{SpaceId: spaceID, FieldId: def.name, Name: def.name, ValueType: def.typ, Status: "active"})
	}
	return out
}

func columnsForMarket(market string) []*pb.DatasetColumn {
	names := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num", "taker_buy_base_asset_volume", "taker_buy_quote_asset_volume", "symbol", "avg_price_1m", "avg_price_5m"}
	if market == "swap" {
		names = append(names, "fundingRate")
	}
	out := make([]*pb.DatasetColumn, 0, len(names))
	for _, name := range names {
		out = append(out, &pb.DatasetColumn{ColumnName: name, OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: name, ValueType: valueTypeForColumn(name), Required: isRequiredColumn(name), Status: "active", Attributes: displayNameAttrs(name)})
	}
	return out
}

func recordColumns() []*pb.DatasetColumn {
	defs := []struct {
		name        string
		valueType   pb.FieldValueType
		textIndexed bool
	}{
		{name: "title", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, textIndexed: true},
		{name: "status", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{name: "score", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "updated_at", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_TIME},
		{name: "payload_json", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_JSON},
	}
	out := make([]*pb.DatasetColumn, 0, len(defs))
	for _, def := range defs {
		out = append(out, &pb.DatasetColumn{
			SpaceId:    spaceID,
			DatasetId:  recordDatasetID,
			ColumnName: def.name,
			OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD,
			OriginId:   def.name,
			ValueType:  def.valueType,
			Status:     "active",
			Attributes: displayNameAttrs(def.name),
		})
	}
	return out
}

func benchDatasetName(market string) string {
	if market == "swap" {
		return "合约K线"
	}
	return "现货K线"
}

func benchViewName(market string) string {
	if market == "swap" {
		return "合约收盘"
	}
	return "现货收盘"
}

func displayNameAttrs(name string) map[string]string {
	return map[string]string{"display_name": displayName(name)}
}

func displayName(name string) string {
	switch name {
	case "open":
		return "开盘价"
	case "high":
		return "最高价"
	case "low":
		return "最低价"
	case "close":
		return "收盘价"
	case "volume":
		return "成交量"
	case "quote_volume":
		return "成交额"
	case "trade_num":
		return "成交笔数"
	case "taker_buy_base_asset_volume":
		return "主动买量"
	case "taker_buy_quote_asset_volume":
		return "主动买额"
	case "symbol":
		return "交易标的"
	case "avg_price_1m":
		return "均价1分"
	case "avg_price_5m":
		return "均价5分"
	case "fundingRate":
		return "资金费率"
	case "title":
		return "标题"
	case "status":
		return "状态"
	case "score":
		return "分数"
	case "updated_at":
		return "更新时间"
	case "payload_json":
		return "载荷JSON"
	default:
		return "字段"
	}
}

func valueTypeForColumn(name string) pb.FieldValueType {
	if name == "trade_num" {
		return pb.FieldValueType_FIELD_VALUE_TYPE_INT
	}
	if name == "symbol" {
		return pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	return pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
}

func isRequiredColumn(name string) bool {
	switch name {
	case "open", "high", "low", "close":
		return true
	default:
		return false
	}
}

func datasetID(market string) string {
	return "bench_binance_" + market + "_kline_1h"
}

func viewID(market string) string {
	return "bench_" + market + "_kline_view"
}

func syntheticRecordID(index int) string {
	return fmt.Sprintf("bench-record-%06d", index)
}

func syntheticRecordRows(count int) []*pb.RecordRow {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]*pb.RecordRow, 0, count)
	for i := 0; i < count; i++ {
		recordID := syntheticRecordID(i)
		updatedAt := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		out = append(out, &pb.RecordRow{
			Key: &pb.RecordKey{
				SpaceId:   spaceID,
				DatasetId: recordDatasetID,
				RecordId:  recordID,
				Version:   updatedAt,
			},
			Columns: []*pb.ColumnValue{
				stringValue("title", fmt.Sprintf("synthetic record %06d", i)),
				stringValue("status", []string{"active", "paused", "archived"}[i%3]),
				doubleValue("score", 100+float64(i%1000)/10),
				timeValue("updated_at", updatedAt),
				jsonValue("payload_json", fmt.Sprintf(`{"bucket":%d,"rank":%d}`, i%10, i)),
			},
		})
	}
	return out
}

func stringValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func doubleValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func timeValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: value}},
	}
}

func jsonValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_JSON,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: value}},
	}
}

func sumDatasetRows(values map[string]int) int {
	var total int
	for _, value := range values {
		total += value
	}
	return total
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

func targetOpts(port int) []client.Option {
	return []client.Option{
		client.WithTarget(fmt.Sprintf("ip://127.0.0.1:%d", port)),
		client.WithProtocol("trpc"),
		client.WithNetwork("tcp"),
		client.WithTimeout(30 * time.Second),
	}
}

func allocatePorts(count int) ([]int, error) {
	ports := make([]int, 0, count)
	listeners := make([]net.Listener, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for len(ports) < count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

func waitPorts(ports []int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var missing []int
		for _, port := range ports {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
			if err != nil {
				missing = append(missing, port)
				continue
			}
			_ = conn.Close()
		}
		if len(missing) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("ports not ready: %v", ports)
}

func locateModuleDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(wd, "cmd", "moox-storage")); err == nil {
				return wd, nil
			}
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("cannot locate modules/storage from cwd")
		}
		wd = parent
	}
}
