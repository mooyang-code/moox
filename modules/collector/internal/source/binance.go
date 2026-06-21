package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const defaultBinanceBaseURL = "https://api.binance.com"

// Instrument 表示从 Binance 发现的交易标的。
type Instrument struct {
	Symbol         string
	ExternalSymbol string
	BaseAsset      string
	QuoteAsset     string
	Exchange       string
	Status         string
}

// DiscoverRequest 描述一次数据源标的发现请求。
type DiscoverRequest struct {
	QuoteAsset string
}

// BinanceSource 实现 Binance 交易标的发现能力。
type BinanceSource struct {
	baseURL string
	client  *http.Client
}

func NewBinanceSource(baseURL string) *BinanceSource {
	if baseURL == "" {
		baseURL = defaultBinanceBaseURL
	}
	return &BinanceSource{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  http.DefaultClient,
	}
}

func (s *BinanceSource) DiscoverInstruments(ctx context.Context, req DiscoverRequest) ([]Instrument, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/v3/exchangeInfo", nil)
	if err != nil {
		return nil, fmt.Errorf("build binance exchangeInfo request: %w", err)
	}
	rsp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request binance exchangeInfo: %w", err)
	}
	defer rsp.Body.Close()
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return nil, fmt.Errorf("binance exchangeInfo status: %s", rsp.Status)
	}

	var payload struct {
		Symbols []struct {
			Symbol     string `json:"symbol"`
			BaseAsset  string `json:"baseAsset"`
			QuoteAsset string `json:"quoteAsset"`
			Status     string `json:"status"`
		} `json:"symbols"`
	}
	if err := json.NewDecoder(rsp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode binance exchangeInfo: %w", err)
	}

	quote := strings.ToUpper(strings.TrimSpace(req.QuoteAsset))
	out := make([]Instrument, 0, len(payload.Symbols))
	for _, item := range payload.Symbols {
		if item.Status != "TRADING" {
			continue
		}
		if quote != "" && strings.ToUpper(item.QuoteAsset) != quote {
			continue
		}
		base := strings.ToUpper(item.BaseAsset)
		q := strings.ToUpper(item.QuoteAsset)
		out = append(out, Instrument{
			Symbol:         base + "-" + q,
			ExternalSymbol: item.Symbol,
			BaseAsset:      base,
			QuoteAsset:     q,
			Exchange:       "BINANCE",
			Status:         item.Status,
		})
	}
	return out, nil
}
