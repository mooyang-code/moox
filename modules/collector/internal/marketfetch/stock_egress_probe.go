package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
)

type stockEgressValidator func([]byte) error

// StockEgressIdentityProbe is an optional diagnostic. Provider reachability is
// exercised by the market canary; a blocked IP reflector must not block a
// release or turn into a false statement about function identity.
func StockEgressIdentityProbe(ctx context.Context) (*model.Response, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	return stockEgressIdentityProbeWithClient(ctx, client, []string{
		"https://api.ipify.org?format=text",
		"https://ifconfig.me/ip",
		"https://checkip.amazonaws.com/",
		"https://icanhazip.com/",
	}...)
}

func stockEgressIdentityProbeWithClient(ctx context.Context, client *http.Client, reflectors ...string) (*model.Response, error) {
	var failures []string
	for _, endpoint := range reflectors {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		request.Header.Set("User-Agent", "moox-collector/1.0")
		response, err := client.Do(request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		response.Body.Close()
		if readErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, readErr))
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d", endpoint, response.StatusCode))
			continue
		}
		if err := validatePublicIPAddress(body); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		return &model.Response{Success: true, Message: "stock_cn egress identity probe ok", Data: map[string]interface{}{
			"provider": "multi", "market": "stock_cn", "details": map[string]string{"public_ip": strings.TrimSpace(string(body))},
		}, Timestamp: time.Now().UTC()}, nil
	}
	return nil, fmt.Errorf("public_ip reflectors unavailable: %s", strings.Join(failures, "; "))
}

// StockEgressProbe retains the provider-feed diagnostic used by focused
// validation. It does not participate in release or Timer activation.
func StockEgressProbe(ctx context.Context) (*model.Response, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	return runStockEgressChecks(ctx, client, []stockEgressCheck{
		{name: "public_ip", url: "https://api.ipify.org?format=text", validate: validatePublicIPAddress},
		{name: "sina_kline", url: "https://quotes.sina.cn/cn/api/jsonp_v2.php/var%20moox_probe=/CN_MarketDataService.getKLineData?symbol=sh600000&scale=1&ma=no&datalen=1", validate: validateSinaKline},
		{name: "tencent_kline", url: "https://ifzq.gtimg.cn/appstock/app/kline/mkline?_var=m1_today&param=sh600000,m1,,1", validate: validateTencentKline},
		{name: "eastmoney_kline", url: "https://push2.eastmoney.com/api/qt/stock/kline/get?secid=1.600000&klt=1&fqt=0&beg=0&end=20500101&lmt=1&fields1=f1,f2,f3,f4,f5,f6&fields2=f51,f52,f53,f54,f55,f56,f57", validate: validateEastMoneyKline},
	})
}

func stockEgressProbeWithClient(ctx context.Context, client *http.Client, publicIPURL, sinaKlineURL string) (*model.Response, error) {
	return runStockEgressChecks(ctx, client, []stockEgressCheck{
		{name: "public_ip", url: publicIPURL, validate: validatePublicIPAddress},
		{name: "sina_kline", url: sinaKlineURL, validate: validateSinaKline},
	})
}

type stockEgressCheck struct {
	name     string
	url      string
	validate stockEgressValidator
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
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%s response: %w", check.name, readErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("%s returned HTTP %d", check.name, response.StatusCode)
		}
		if check.validate == nil {
			return nil, fmt.Errorf("%s response validator is missing", check.name)
		}
		if err := check.validate(body); err != nil {
			return nil, fmt.Errorf("%s response: %w", check.name, err)
		}
		if check.name == "public_ip" {
			details[check.name] = strings.TrimSpace(string(body))
		} else {
			details[check.name] = "ok"
		}
	}
	return &model.Response{Success: true, Message: "stock_cn egress probe ok", Data: map[string]interface{}{"provider": "multi", "market": "stock_cn", "details": details}, Timestamp: time.Now().UTC()}, nil
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001:2::/48"), netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("5f00::/16"),
}

func validatePublicIPAddress(body []byte) error {
	address, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return fmt.Errorf("not a valid IP address")
	}
	if address.Zone() != "" {
		return fmt.Errorf("zoned address is not public")
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return fmt.Errorf("address %s is not public", address)
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return fmt.Errorf("address %s is private, loopback, or reserved", address)
		}
	}
	return nil
}

type probeSinaBar struct {
	Day    string `json:"day"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
	Amount string `json:"amount"`
}

func validateSinaKline(body []byte) error {
	raw := strings.TrimSpace(string(body))
	raw = trimProbeAssignment(raw, "var moox_probe=", "var moox_kline=")
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ";")
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.TrimSuffix(raw, ")")
	var bars []probeSinaBar
	if err := json.Unmarshal([]byte(raw), &bars); err != nil {
		return fmt.Errorf("invalid Sina kline JSON: %w", err)
	}
	for _, bar := range bars {
		if validateQuoteValues(bar.Day, bar.Open, bar.High, bar.Low, bar.Close, bar.Volume, bar.Amount) == nil {
			return nil
		}
	}
	return fmt.Errorf("contains no valid Sina kline")
}

func validateTencentKline(body []byte) error {
	raw := strings.TrimSpace(string(body))
	raw = trimProbeAssignment(raw, "m1_today=")
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ";")
	var payload struct {
		Data map[string]struct {
			M1 [][]json.RawMessage `json:"m1"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return fmt.Errorf("invalid Tencent kline JSON: %w", err)
	}
	for _, feed := range payload.Data {
		for _, record := range feed.M1 {
			if len(record) < 6 {
				continue
			}
			values := make([]string, 6)
			valid := true
			for index := range values {
				if err := json.Unmarshal(record[index], &values[index]); err != nil {
					valid = false
					break
				}
			}
			if valid && validateQuoteValues(values[0], values[1], values[3], values[4], values[2], values[5]) == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("contains no valid Tencent kline")
}

func validateEastMoneyKline(body []byte) error {
	var payload struct {
		Data struct {
			Trends []string `json:"trends"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("invalid EastMoney kline JSON: %w", err)
	}
	lines := payload.Data.Trends
	if len(lines) == 0 {
		lines = payload.Data.Klines
	}
	for _, line := range lines {
		fields := strings.Split(line, ",")
		if len(fields) >= 7 && validateQuoteValues(fields[0], fields[1], fields[3], fields[4], fields[2], fields[5], fields[6]) == nil {
			return nil
		}
	}
	return fmt.Errorf("contains no valid EastMoney kline")
}

func validateQuoteValues(timestamp string, open string, high string, low string, close string, volume string, optional ...string) error {
	if !validProbeTimestamp(timestamp) {
		return fmt.Errorf("timestamp is invalid")
	}
	prices := make([]float64, 4)
	for index, field := range []struct {
		name string
		raw  string
	}{{"open", open}, {"high", high}, {"low", low}, {"close", close}} {
		value, err := strconv.ParseFloat(strings.TrimSpace(field.raw), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return fmt.Errorf("%s is not a finite positive number", field.name)
		}
		prices[index] = value
	}
	openValue, highValue, lowValue, closeValue := prices[0], prices[1], prices[2], prices[3]
	if highValue < max(openValue, closeValue, lowValue) || lowValue > min(openValue, closeValue, highValue) {
		return fmt.Errorf("OHLC values are inconsistent")
	}
	values := append([]string{volume}, optional...)
	for _, raw := range values {
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("volume or amount is not a finite non-negative number")
		}
	}
	return nil
}

func trimProbeAssignment(raw string, markers ...string) string {
	for _, marker := range markers {
		if index := strings.Index(raw, marker); index >= 0 {
			return raw[index+len(marker):]
		}
	}
	return raw
}

func validProbeTimestamp(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return false
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "200601021504", time.RFC3339} {
		if _, err := time.ParseInLocation(layout, value, location); err == nil {
			return true
		}
	}
	return false
}
