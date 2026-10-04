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

	"github.com/mooyang-code/moox/modules/gateway/internal/config"
	"github.com/mooyang-code/moox/modules/gateway/internal/store"
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
	if _, err := config.Load(*path); err != nil {
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
	snapshot, err := store.NewRoutes(cfg.Store.Path).Load()
	if err != nil {
		return err
	}
	if snapshot.NodeID != cfg.Node.ID {
		return fmt.Errorf("cached routes target %q, want %q", snapshot.NodeID, cfg.Node.ID)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
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
