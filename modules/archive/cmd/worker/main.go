// Command moox-archive-worker 是归档 worker：经 Access 领取运行，读取 Storage 的时间区间数据，
// 写入本地 SQLite 暂存区，并导出 CSV 分区文件。它只出站，可以部署在任何能访问 Access 的机器上（Linux、macOS、Windows）。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/app"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/config"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/manager"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/pacing"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/runner"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-go/log"
)

// 构建时通过 -ldflags 注入。
var (
	Version   = "dev"
	BuildTime = ""
	GitCommit = ""
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "version":
			fmt.Printf("moox-archive-worker %s %s %s\n", Version, GitCommit, BuildTime)
			return nil
		case "health":
			return health(args[1:])
		case "probe":
			return probe(args[1:])
		}
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "config/app.yaml", "archive worker config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return serve(context.Background(), cfg)
}

// serve 运行 worker 直到收到退出信号，或 parent 结束。
func serve(parent context.Context, cfg *config.Config) (err error) {
	gateway, err := cfg.OpenGateway()
	if err != nil {
		return fmt.Errorf("open gateway client: %w", err)
	}
	defer func() { err = errors.Join(err, gateway.Close()) }()
	secret, err := gatewayauth.ReadSigningSecret(cfg.StorageSecretPath())
	if err != nil {
		return fmt.Errorf("read storage auth file: %w", err)
	}
	store, err := staging.Open(cfg.StatePath(), staging.Options{CacheBytes: int64(cfg.Staging.CacheSize)})
	if err != nil {
		return fmt.Errorf("open staging database: %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	pacer, err := pacing.New(cfg.PacingConfig(), nil)
	if err != nil {
		return err
	}
	files := export.Files{Root: cfg.Worker.RootDir, Format: cfg.Worker.ExportFormats[0]}
	instance := newInstanceID()
	worker := app.New(app.Options{
		Config: cfg, Manager: manager.New(gateway), Staging: store, Files: files, InstanceID: instance, Version: Version,
		Runner:    &runner.Runner{Source: source.New(gateway, secret), Staging: store, Files: files, Pace: pacer, PageRows: cfg.Pacing.PageRows},
		DiskUsage: app.StatfsDiskUsage,
	})
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownHealth, err := serveHealth(cfg.Health.Addr, worker)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, shutdownHealth()) }()
	log.Infof("moox-archive-worker %s started worker_id=%s instance=%s root=%s", Version, cfg.Worker.WorkerID, instance, cfg.Worker.RootDir)
	return worker.Run(ctx)
}

func newInstanceID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(raw[:]))
}

// serveHealth 在本机地址上提供 /healthz /readyz /metrics，接受 MOOX_HEALTH_AUTH_* 环境配置的签名校验。
func serveHealth(addr string, worker *app.App) (func() error, error) {
	state := healthz.NewState("archive-worker", "archive-worker", Version, GitCommit)
	state.SnapshotFunc = func(context.Context) healthz.Response {
		snap := worker.Snapshot()
		status := "ok"
		var reasons []string
		switch {
		case !snap.Ready:
			status = "error"
			reasons = append(reasons, "no recent heartbeat acknowledged by archive-mgr")
		case snap.DiskLow:
			status = "degraded"
			reasons = append(reasons, "disk_low")
		}
		if snap.LastError != "" {
			reasons = append(reasons, snap.LastError)
		}
		return healthz.Response{
			Module: "archive-worker", Ready: snap.Ready, Status: status, Time: time.Now().UTC(),
			Details: map[string]any{"active": snap.Active, "running": snap.Running, "maintaining": snap.Maintaining, "disk_low": snap.DiskLow, "disk_free_bytes": snap.DiskFree, "reasons": reasons},
		}
	}
	state.SetReady(true)
	handler, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return nil, err
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("archive worker health server: %v", err)
		}
	}()
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}, nil
}

// health 询问本机 worker 的就绪状态，请求用 MOOX_HEALTH_AUTH_* 环境变量签名。
func health(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:11418/readyz", "worker health URL")
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
	fmt.Println(strings.TrimSpace(string(body)))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("archive worker is not ready: HTTP %d", response.StatusCode)
	}
	return nil
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
	signature, err := requestauth.Sign(secretKey, requestauth.Material{Method: request.Method, Path: request.URL.EscapedPath(), Timestamp: timestamp, Nonce: nonce})
	if err != nil {
		return fmt.Errorf("sign health request: %w", err)
	}
	request.Header.Set("X-Moox-Health-Auth", strings.Join([]string{version, accessKey, strconv.FormatInt(timestamp, 10), nonce, signature}, "/"))
	return nil
}
