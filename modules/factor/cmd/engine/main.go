// moox-factor-engine consumes collector period events, computes factors with
// the factorexpr expression engine and writes results to Storage. It only dials out: to
// moox-factor-mgr through the service gateway, to EventBus and to
// access.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/requestauth"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	trpc "trpc.group/trpc-go/trpc-go"
)

var (
	Version   = "dev"
	BuildTime = ""
	GitCommit = ""
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "serve":
		return serve(args)
	case "run-once":
		return runOnce(args, out)
	case "compare":
		return compare(args, out)
	case "health":
		return health(args, out)
	case "reset-gap-ledger":
		return resetGapLedger(args, out)
	case "version":
		_, err := fmt.Fprintf(out, "moox-factor-engine %s %s %s\n", Version, GitCommit, BuildTime)
		return err
	default:
		return fmt.Errorf("unknown command %q (serve, run-once, compare, health, reset-gap-ledger, version)", command)
	}
}

func serve(args []string) (err error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	framework := fs.String("conf", "config/trpc_go.engine.yaml", "factor engine tRPC config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	trpc.ServerConfigPath = *framework
	s := trpc.NewServer()
	trpclog.InstallServiceName("factor-engine")
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	runtime, err := engine.Initialize(ctx, s, cfg, Version)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	return s.Serve()
}

type listFlag []string

func (v *listFlag) String() string { return strings.Join(*v, ",") }

func (v *listFlag) Set(raw string) error {
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*v = append(*v, item)
		}
	}
	return nil
}

func runOnce(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run-once", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	setID := fs.String("set", "", "factor set id")
	period := fs.String("period", "", "period start timestamp RFC3339")
	var factors, subjects listFlag
	fs.Var(&factors, "factor", "factor id (repeatable)")
	fs.Var(&subjects, "subject", "subject id (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*setID) == "" {
		return errors.New("--set is required")
	}
	at, err := time.Parse(time.RFC3339Nano, *period)
	if err != nil {
		return fmt.Errorf("--period must be an RFC3339 timestamp: %w", err)
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	outcome, err := engine.RunOnce(context.Background(), cfg, engine.RunOnceRequest{
		SetID: *setID, FactorIDs: factors, Subjects: subjects, Period: at.UTC(),
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "set_id": *setID, "period": at.UTC().Format(time.RFC3339),
		"status": outcome.Status, "rows_written": outcome.RowsWritten, "factors": outcome.Factors,
	})
}

// compare 是因子表达式引擎的一致性核对（只读）：用 factorexpr 重算一段历史，与结果数据集里已写入的值逐个比较。
func compare(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	setFile := fs.String("set-file", "", "copy of data/engine/catalog.json")
	library := fs.String("library", "", "optional YAML factor library directory overriding the snapshot definitions")
	startText := fs.String("start", "", "start timestamp RFC3339 (inclusive)")
	endText := fs.String("end", "", "end timestamp RFC3339 (exclusive)")
	output := fs.String("output", "", "write the JSON report to this file")
	computeWorkers := fs.Int("compute-workers", 4, "factorexpr goroutines")
	chunk := fs.Int("chunk-periods", 0, "periods per chunk (0 uses recalc.chunk_periods)")
	var sets, factors, subjects listFlag
	fs.Var(&sets, "set", "factor set id (repeatable)")
	fs.Var(&factors, "factor", "factor id (repeatable)")
	fs.Var(&subjects, "subject", "subject id (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(sets) == 0 {
		return errors.New("--set is required")
	}
	start, err := time.Parse(time.RFC3339Nano, *startText)
	if err != nil {
		return fmt.Errorf("--start must be an RFC3339 timestamp: %w", err)
	}
	end, err := time.Parse(time.RFC3339Nano, *endText)
	if err != nil {
		return fmt.Errorf("--end must be an RFC3339 timestamp: %w", err)
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	report, err := engine.Compare(context.Background(), cfg, engine.CompareRequest{
		SetFile: *setFile, Library: *library, SetIDs: sets, Start: start, End: end,
		Factors: factors, Subjects: subjects, ComputeWorkers: *computeWorkers, ChunkPeriods: *chunk,
	})
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if *output != "" {
		if err := os.WriteFile(*output, append(encoded, '\n'), 0o644); err != nil {
			return err
		}
	}
	for _, stats := range report.Outputs {
		fmt.Fprintf(out, "%s %-24s compared=%d both_value=%d both_null=%d max_rel=%.3g outliers=%v flips=%d(tie=%d,verified=%d,float=%d,unexplained=%d) old_value_new_null=%v old_null_new_value=%d missing_baseline=%d\n",
			stats.Set, stats.Output, stats.Compared, stats.BothValue, stats.BothNull, stats.MaxRelError, stats.ContinuousOutliers,
			stats.DiscreteFlips, stats.TieFlips, stats.VerifiedFlips, stats.FloatLimitedFlips, stats.UnexplainedFlips, stats.OldValueNewNull, stats.OldNullNewValue,
			stats.MissingBaseline)
	}
	if failures := report.Failures(); len(failures) > 0 {
		for _, failure := range failures {
			fmt.Fprintln(out, "FAIL", failure)
		}
		return fmt.Errorf("一致性验收不通过：%d 项", len(failures))
	}
	fmt.Fprintf(out, "PASS（耗时 %s）\n", report.Elapsed)
	return nil
}

// health asks the local engine for readiness, signing the request with the
// MOOX_HEALTH_AUTH_* environment the engine runs with.
func health(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:11417/readyz", "engine health URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	if err := signHealthRequest(request); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	_, _ = fmt.Fprintln(out, strings.TrimSpace(string(body)))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("factor engine is not ready: HTTP %d", response.StatusCode)
	}
	return nil
}

// resetGapLedger 删除空洞账本。数据被整体清空之后账本里的待恢复项指向已不存在的数据，必须在引擎停止后执行：
// 引擎持有打开的账本文件，并在内存里维护待恢复集合。
func resetGapLedger(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reset-gap-ledger", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	healthAddr := fs.String("health-addr", "127.0.0.1:11417", "引擎健康检查地址；能连上说明引擎还在运行，拒绝执行")
	yes := fs.Bool("yes", false, "确认删除全部空洞账本")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	if conn, dialErr := net.DialTimeout("tcp", *healthAddr, time.Second); dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("因子引擎仍在运行（%s 可以连上）：先停止引擎再重置账本", *healthAddr)
	}
	if !*yes {
		return errors.New("该命令会删除全部空洞账本（含待恢复记录与审计历史）；确认后加 --yes 重新执行")
	}
	removed, err := gapledger.Reset(cfg.Gap.LedgerDir)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "ok", "ledger_dir": cfg.Gap.LedgerDir, "files_removed": removed})
}

func signHealthRequest(request *http.Request) error {
	version := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_VERSION"))
	if version == "" {
		version = "moox-health-v1"
	}
	accessKey := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY"))
	if accessKey == "" || secretKey == "" {
		return errors.New("MOOX_HEALTH_AUTH_ACCESS_KEY and MOOX_HEALTH_AUTH_SECRET_KEY are required")
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("generate health nonce: %w", err)
	}
	timestamp := time.Now().Unix()
	nonce := hex.EncodeToString(nonceBytes)
	signature, err := requestauth.Sign(secretKey, requestauth.Material{
		Method: request.Method, Path: request.URL.EscapedPath(), Timestamp: timestamp, Nonce: nonce,
	})
	if err != nil {
		return fmt.Errorf("sign health request: %w", err)
	}
	request.Header.Set("X-Moox-Health-Auth", strings.Join([]string{
		version, accessKey, strconv.FormatInt(timestamp, 10), nonce, signature,
	}, "/"))
	return nil
}
