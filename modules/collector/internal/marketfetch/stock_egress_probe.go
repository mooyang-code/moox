package marketfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
)

// StockEgressProbe requires both a public outbound IP and a real stock feed.
// The deployment gate compares one result from every Timer function.
func StockEgressProbe(ctx context.Context) (*model.Response, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	return stockEgressProbeWithClient(ctx, client,
		"https://api.ipify.org?format=text",
		"https://quotes.sina.cn/cn/api/jsonp_v2.php/var%20moox_probe=/CN_MarketDataService.getKLineData?symbol=sh600000&scale=1&ma=no&datalen=1",
	)
}

func stockEgressProbeWithClient(ctx context.Context, client *http.Client, publicIPURL, sinaKlineURL string) (*model.Response, error) {
	details := make(map[string]string, 4)
	checks := []struct {
		name string
		url  string
	}{
		{name: "public_ip", url: publicIPURL},
		{name: "sina_kline", url: sinaKlineURL},
	}
	for _, check := range checks {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "moox-collector/1.0")
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s request: %w", check.name, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
		response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%s response: %w", check.name, readErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("%s returned HTTP %d", check.name, response.StatusCode)
		}
		value := strings.TrimSpace(string(body))
		if check.name == "public_ip" && value == "" {
			return nil, fmt.Errorf("public_ip response is empty")
		}
		if check.name == "sina_kline" && !strings.Contains(value, "\"open\"") {
			return nil, fmt.Errorf("sina_kline response has no OHLC payload")
		}
		if check.name == "sina_kline" {
			details[check.name] = "ok"
		} else {
			details[check.name] = value
		}
	}
	return &model.Response{Success: true, Message: "stock_cn egress probe ok", Data: map[string]interface{}{"provider": "sina", "market": "stock_cn", "details": details}, Timestamp: time.Now().UTC()}, nil
}
