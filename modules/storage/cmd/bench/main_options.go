package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"trpc.group/trpc-go/trpc-go/client"
)

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
			if _, err := os.Stat(filepath.Join(wd, "cmd", "server")); err == nil {
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
