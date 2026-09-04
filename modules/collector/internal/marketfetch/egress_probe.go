package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
)

// EgressProbe performs bounded provider and public-IP checks. It is called once
// after deployment and its result is stored by the control plane; it is not a
// continuously running SCF watchdog.
func EgressProbe(ctx context.Context, provider, market string) (*model.Response, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "binance"
	}
	if provider != "binance" {
		return nil, fmt.Errorf("unsupported egress provider %q", provider)
	}
	apiConfig, err := binance.ResolveAPIConfig()
	if err != nil {
		return nil, fmt.Errorf("load Binance API config: %w", err)
	}
	baseURLs := apiConfig.SpotBaseURLs
	pingPath := "/api/v3/ping"
	klinePath := "/api/v3/klines?symbol=BTCUSDT&interval=1m&limit=1"
	if strings.EqualFold(strings.TrimSpace(market), "swap") || strings.EqualFold(strings.TrimSpace(market), "futures") {
		baseURLs = apiConfig.SwapBaseURLs
		pingPath = "/fapi/v1/ping"
		klinePath = "/fapi/v1/klines?symbol=BTCUSDT&interval=1m&limit=1"
	}
	if len(baseURLs) == 0 {
		return nil, fmt.Errorf("Binance API base URL is empty")
	}
	var lastErr error
	for _, baseURL := range baseURLs {
		response, err := runBinanceEgressChecks(ctx, nil, baseURL, "https://api.ipify.org?format=text", pingPath, klinePath, provider, market)
		if err == nil {
			return response, nil
		}
		lastErr = fmt.Errorf("%s: %w", baseURL, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("Binance provider endpoints unavailable: %w", lastErr)
}

func runBinanceEgressChecks(ctx context.Context, client *http.Client, baseURL, publicIPURL, pingPath, klinePath, provider, market string) (*model.Response, error) {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("Binance API base URL is empty")
	}
	details := make(map[string]string, 4)
	// The provider endpoint is the only required check. Public-IP services
	// are auxiliary and are often blocked independently by egress policy; a
	// blocked IP reflector must not hide that Binance itself is reachable.
	checks := []struct {
		name     string
		url      string
		required bool
		validate func([]byte) error
	}{
		{name: "provider_ping", url: baseURL + pingPath, required: true},
		{name: "provider_kline", url: baseURL + klinePath, required: true, validate: validateBinanceKlineResponse},
		{name: "public_ip", url: publicIPURL},
	}
	for _, check := range checks {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.url, nil)
		if err != nil {
			if check.required {
				return nil, fmt.Errorf("%s request: %w", check.name, err)
			}
			details[check.name+"_error"] = err.Error()
			continue
		}
		response, err := client.Do(req)
		if err != nil {
			if check.required {
				return nil, fmt.Errorf("%s request: %w", check.name, err)
			}
			details[check.name+"_error"] = err.Error()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 256))
		response.Body.Close()
		if readErr != nil {
			if check.required {
				return nil, fmt.Errorf("%s response: %w", check.name, readErr)
			}
			details[check.name+"_error"] = readErr.Error()
			continue
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			if check.required {
				return nil, fmt.Errorf("%s returned HTTP %d: %s", check.name, response.StatusCode, strings.TrimSpace(string(body)))
			}
			details[check.name+"_error"] = fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
			continue
		}
		if check.validate != nil {
			if err := check.validate(body); err != nil {
				if check.required {
					return nil, fmt.Errorf("%s response: %w", check.name, err)
				}
				details[check.name+"_error"] = err.Error()
				continue
			}
		}
		details[check.name] = strings.TrimSpace(string(body))
	}
	return &model.Response{Success: true, Message: "egress probe ok", Data: map[string]interface{}{"provider": provider, "market": market, "details": details}, Timestamp: time.Now().UTC()}, nil
}

func validateBinanceKlineResponse(body []byte) error {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if len(rows) == 0 || len(rows[0]) < 6 {
		return fmt.Errorf("response contains no candle rows")
	}
	return nil
}
