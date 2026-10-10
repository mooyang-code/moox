package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute/proto/directorypb"
	"github.com/mooyang-code/moox/packages/requestauth"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(arguments []string, output io.Writer) int {
	if len(arguments) == 0 {
		return fail(output, errors.New("缺少命令：check-config、snapshot、health"))
	}
	var err error
	switch arguments[0] {
	case "check-config":
		err = checkConfig(arguments[1:], output)
	case "snapshot":
		err = printSnapshot(arguments[1:], output)
	case "health":
		err = checkHealth(arguments[1:], output)
	default:
		err = fmt.Errorf("未知命令 %q，可选 check-config、snapshot、health", arguments[0])
	}
	if err != nil {
		return fail(output, err)
	}
	return 0
}

func checkConfig(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("check-config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config/app.yaml", "主机网关配置文件")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("check-config 不接受位置参数")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	// 上线前的自检要覆盖进程启动时会失败的那些检查：证书必须由 MooX 私有 CA 签发并包含本机主机 ID，
	// 非 control 主机的调用方密钥必须可读，快照缓存目录的权限必须正确。
	if _, err := tlsconfig.LoadServer(cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.CAFile, cfg.Host.ID, time.Now()); err != nil {
		return err
	}
	if cfg.Control.KeyFile != "" {
		if _, err := gatewayauth.LoadCallerKey(cfg.Control.KeyFile); err != nil {
			return fmt.Errorf("读取调用方密钥 %s: %w", cfg.Control.KeyFile, err)
		}
	}
	if _, err := store.NewSnapshots(cfg.Store.Path).Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("快照缓存目录 %s 不可用: %w", cfg.Store.Path, err)
	}
	_, _ = fmt.Fprintln(output, "配置有效")
	return nil
}

func printSnapshot(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config/app.yaml", "主机网关配置文件")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("snapshot 不接受位置参数")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	cached, err := store.NewSnapshots(cfg.Store.Path).Load()
	if err != nil {
		return err
	}
	applied, err := snapshot.Validate(cfg.Host.ID, cached)
	if err != nil {
		return err
	}
	// 只输出路由与目录，不输出校验密钥。
	summary := struct {
		HostID    string                         `json:"host_id"`
		Hash      string                         `json:"hash"`
		Disabled  bool                           `json:"disabled"`
		Routes    []*adminpb.HostRoute           `json:"routes"`
		Directory *directorypb.DirectorySnapshot `json:"directory"`
		Callers   []string                       `json:"callers"`
	}{HostID: applied.HostID, Hash: applied.Hash, Disabled: applied.Disabled, Routes: cached.GetRoutes(), Directory: cached.GetDirectory()}
	for _, key := range cached.GetKeys() {
		summary.Callers = append(summary.Callers, key.GetCaller()+"/"+key.GetKeyId())
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func checkHealth(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("url", "http://127.0.0.1:11012/readyz", "readiness URL")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("health accepts no positional arguments")
	}
	parsed, err := url.Parse(*endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("health URL must be HTTP(S)")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	if err := signHealthRequest(request); err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("主机网关未就绪: HTTP %d", response.StatusCode)
	}
	_, _ = fmt.Fprintln(output, "ready")
	return nil
}

func signHealthRequest(request *http.Request) error {
	version := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_VERSION"))
	accessKey := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY"))
	if version == "" || accessKey == "" || secretKey == "" {
		return errors.New("health authentication environment is required")
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

func fail(output io.Writer, err error) int {
	_, _ = fmt.Fprintf(output, "error: %v\n", err)
	return 1
}
