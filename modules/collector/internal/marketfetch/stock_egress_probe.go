package marketfetch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
)

// StockEgressProbe requires both a public outbound IP and a real stock feed.
// The deployment gate compares one result from every Timer function.
func StockEgressProbe(ctx context.Context) (*model.Response, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	return runStockEgressChecks(ctx, client, []stockEgressCheck{
		{name: "public_ip", url: "https://api.ipify.org?format=text"},
		{name: "sina_kline", url: "https://quotes.sina.cn/cn/api/jsonp_v2.php/var%20moox_probe=/CN_MarketDataService.getKLineData?symbol=sh600000&scale=1&ma=no&datalen=1", marker: "\"open\""},
		{name: "tencent_kline", url: "https://ifzq.gtimg.cn/appstock/app/kline/mkline?_var=m1_today&param=sh600000,m1,,1", marker: "\"m1\""},
		{name: "eastmoney_kline", url: "http://push2.eastmoney.com/api/qt/stock/trends2/get?secid=1.600000&ndays=1&iscr=0&fields1=f1,f2,f3&fields2=f51,f52,f53,f54,f55,f56,f57,f58", marker: "\"trends\""},
	})
}

func stockEgressProbeWithClient(ctx context.Context, client *http.Client, publicIPURL, sinaKlineURL string) (*model.Response, error) {
	return runStockEgressChecks(ctx, client, []stockEgressCheck{{name: "public_ip", url: publicIPURL}, {name: "sina_kline", url: sinaKlineURL, marker: "\"open\""}})
}

type stockEgressCheck struct {
	name   string
	url    string
	marker string
}

func runStockEgressChecks(ctx context.Context, client *http.Client, checks []stockEgressCheck) (*model.Response, error) {
	details := make(map[string]string, len(checks))
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
		if check.name == "public_ip" && net.ParseIP(value) == nil {
			return nil, fmt.Errorf("public_ip response is not a valid IP address")
		}
		if check.marker != "" && !strings.Contains(value, check.marker) {
			return nil, fmt.Errorf("%s response has no expected market payload", check.name)
		}
		if check.name != "public_ip" {
			details[check.name] = "ok"
		} else {
			details[check.name] = value
		}
	}
	return &model.Response{Success: true, Message: "stock_cn egress probe ok", Data: map[string]interface{}{"provider": "multi", "market": "stock_cn", "details": details}, Timestamp: time.Now().UTC()}, nil
}
