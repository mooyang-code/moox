package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/packages/requestauth"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(arguments []string, output io.Writer) int {
	if len(arguments) == 0 {
		return fail(output, errors.New("command is required"))
	}
	var err error
	switch arguments[0] {
	case "check-config":
		err = checkConfig(arguments[1:], output)
	case "routes":
		err = printRoutes(arguments[1:], output)
	case "health":
		err = checkHealth(arguments[1:], output)
	default:
		err = fmt.Errorf("unknown command %q", arguments[0])
	}
	if err != nil {
		return fail(output, err)
	}
	return 0
}

func checkConfig(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("check-config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config/app.yaml", "gateway configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("check-config accepts no positional arguments")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if _, _, err := config.LoadIdentity(cfg); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(output, "configuration valid")
	return nil
}

func printRoutes(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("routes", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config/app.yaml", "gateway configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("routes accepts no positional arguments")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	view, err := store.NewSnapshots(cfg.Store.Path, cfg.Host.ID).Load()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(map[string]any{"host_id": view.HostID(), "hash": view.Hash(), "disabled": view.Disabled(), "routes": view.Proto().Routes, "directory": view.Directory()})
}

func checkHealth(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("url", "", "readiness URL, defaults to configured health listener")
	configPath := flags.String("config", "config/app.yaml", "host gateway configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("health accepts no positional arguments")
	}
	if *endpoint == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		host, port, _ := net.SplitHostPort(cfg.Server.HealthAddr)
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			host = "127.0.0.1"
			if ip.To4() == nil {
				host = "::1"
			}
		}
		*endpoint = "http://" + net.JoinHostPort(host, port) + "/readyz"
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
		return fmt.Errorf("gateway is not ready: HTTP %d", response.StatusCode)
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
