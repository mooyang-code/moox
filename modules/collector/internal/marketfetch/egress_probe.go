package marketfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
)

// EgressProbe performs only two bounded HTTP requests. It is called once
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
	baseURL := apiConfig.SpotBaseURL
	pingPath := "/api/v3/ping"
	if strings.EqualFold(strings.TrimSpace(market), "swap") || strings.EqualFold(strings.TrimSpace(market), "futures") {
		baseURL = apiConfig.SwapBaseURL
		pingPath = "/fapi/v1/ping"
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("Binance API base URL is empty")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	details := make(map[string]string, 3)
	// The provider endpoint is the only required check. Public-IP services
	// are auxiliary and are often blocked independently by egress policy; a
	// blocked IP reflector must not hide that Binance itself is reachable.
	checks := []struct {
		name     string
		url      string
		required bool
	}{
		{name: "provider_ping", url: strings.TrimRight(baseURL, "/") + pingPath, required: true},
		{name: "public_ip", url: "https://api.ipify.org?format=text"},
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
		details[check.name] = strings.TrimSpace(string(body))
	}
	return &model.Response{Success: true, Message: "egress probe ok", Data: map[string]interface{}{"provider": provider, "market": market, "details": details}, Timestamp: time.Now().UTC()}, nil
}
