package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// health 询问本机 archive-mgr 的就绪状态；健康端口只听本机、不验签。
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
