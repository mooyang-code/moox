// moox-factor-engine consumes collector period events, computes factors with
// a Python worker pool and writes results to Storage. It only dials out: to
// moox-factor-mgr through the service gateway, to EventBus and to
// storage-access.
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
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/requestauth"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/transinfo-blocker"
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
	case "health":
		return health(args, out)
	case "version":
		_, err := fmt.Fprintf(out, "moox-factor-engine %s %s %s\n", Version, GitCommit, BuildTime)
		return err
	default:
		return fmt.Errorf("unknown command %q (serve, run-once, health, version)", command)
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
