package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
)

// health 询问本机 archive-mgr 的就绪状态，请求用 MOOX_HEALTH_AUTH_* 环境变量签名。
func health(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:11416/readyz", "archive-mgr health URL")
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
		return fmt.Errorf("archive-mgr is not ready: HTTP %d", response.StatusCode)
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
