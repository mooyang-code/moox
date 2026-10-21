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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicehost"
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
		return servicehost.Run(engineService, func(ctx context.Context) error { return serve(ctx, args) })
	case "service":
		return serviceCommand(args)
	case "run-once":
		return runOnce(args, out)
	case "health":
		return health(args, out)
	case "reset-gap-ledger":
		return resetGapLedger(args, out)
	case "reconcile":
		return reconcile(args, out)
	case "version":
		_, err := fmt.Fprintf(out, "moox-factor-engine %s %s %s\n", Version, GitCommit, BuildTime)
		return err
	default:
		return fmt.Errorf("unknown command %q (serve, service, run-once, health, reset-gap-ledger, reconcile, version)", command)
	}
}

// engineService 是 Windows 服务名。
const engineService = "moox-factor-engine"

// serviceCommand 处理 Windows 上的 `service install|uninstall|start|stop [-config 路径] [-conf 路径]`：
// 安装时把配置路径转成绝对路径写进服务的启动参数。
func serviceCommand(args []string) error {
	if len(args) == 0 {
		return servicehost.Command(servicehost.Spec{Name: engineService}, args)
	}
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	framework := fs.String("conf", "config/trpc_go.engine.yaml", "factor engine tRPC config")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	appPath, err := filepath.Abs(*app)
	if err != nil {
		return err
	}
	frameworkPath, err := filepath.Abs(*framework)
	if err != nil {
		return err
	}
	return servicehost.Command(servicehost.Spec{
		Name: engineService, DisplayName: "MooX factor engine", Description: "MooX 因子计算引擎（运维机外部程序）",
		Args: []string{"serve", "-config", appPath, "-conf", frameworkPath},
	}, args[:1])
}

// serve 运行引擎直到收到退出信号，或 ctx 结束（Windows 服务收到停止请求）。
func serve(ctx context.Context, args []string) (err error) {
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
	runCtx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	runtime, err := engine.Initialize(runCtx, s, cfg, Version)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close(nil) // 与收到 SIGTERM 相同的优雅退出
		case <-stopped:
		}
	}()
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

// reconcile 对账一个因子集合的源数据：本地时序存储与 Storage 保留期内的数据比对并整段替换，完成后重建变更日志位点。
// 变更日志断档（离线超过保留期、Storage 节点更换）时引擎把该数据源判为未就绪，由人工执行本命令。
// 本地存储同一时刻只能由一个进程打开，必须在引擎停止后执行。
func reconcile(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	healthAddr := fs.String("health-addr", "127.0.0.1:11417", "引擎健康检查地址；能连上说明引擎还在运行，拒绝执行")
	set := fs.String("set", "", "要对账的因子集合 ID")
	var subjects listFlag
	fs.Var(&subjects, "subject", "只对账这个标的（可重复）；不写表示本地有数据的全部标的")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*set) == "" {
		return errors.New("--set 是必填项")
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	if conn, dialErr := net.DialTimeout("tcp", *healthAddr, time.Second); dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("因子引擎仍在运行（%s 可以连上）：先停止引擎再对账（本地存储同一时刻只能由一个进程打开）", *healthAddr)
	}
	summary, err := engine.Reconcile(context.Background(), cfg, engine.ReconcileRequest{
		SetID: *set, Subjects: subjects,
		Progress: func(done, total int) {
			if done == total || done%50 == 0 {
				fmt.Fprintf(os.Stderr, "对账进度 %d/%d\n", done, total)
			}
		},
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(summary)
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
