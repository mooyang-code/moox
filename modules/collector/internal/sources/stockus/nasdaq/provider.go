// Package nasdaq 是美股的纳斯达克官网指数成分来源：只取纳斯达克 100 的成分股，不取 K 线、不取全部标的。
//
// 成分按编制规则全部是纳斯达克上市，所以交易所一律写 XNAS，不去查东方财富的美股列表
// （2026-11-12 实测 99/99 都在东方财富 105 市场）。
package nasdaq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockus"
)

const (
	providerID = "nasdaq"
	marketID   = "stockus"
	// DefaultSourceID 是行情源目录里登记的 source_id。
	DefaultSourceID = "index_constituent_http"

	host        = "api.nasdaq.com"
	defaultBase = "https://" + host
	// 纳斯达克官网网页自己用的接口：返回全部成分，自报 totalrecords。
	listPath = "/api/quote/list-type/nasdaq100"
	// 纳斯达克 100 是 100 家公司 101 个证券（谷歌 A、C 各一行）；行数不在这个范围说明接口返回的不是完整名单。
	minRows = 95
	maxRows = 110
	// 单次超时：控制机实测 4.4～11.9 秒。
	defaultTimeout = 30 * time.Second
	// 返回内容的上限，防止异常大的返回占满内存（实测约 22KB）。
	maxBodyBytes = 4 << 20
)

// Config 是数据源的参数，零值都有默认值。
type Config struct {
	// BaseURL 是接口的地址（不含路径），默认纳斯达克官网；测试用假服务。
	BaseURL  string
	SourceID string
	// HTTPClient 为空时自建直连客户端，超时取 RequestTimeout。
	HTTPClient *http.Client
	Now        func() time.Time
	// RequestTimeout 是单次请求的超时，默认 30 秒。
	RequestTimeout time.Duration
}

type Provider struct {
	baseURL  string
	sourceID string
	client   *http.Client
	now      func() time.Time
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBase
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = DefaultSourceID
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.RequestTimeout}
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.sourceID, DisplayName: "Nasdaq 100", Hosts: []string{host}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

// IndexCodes 返回能取成分股的指数代码。
func (p *Provider) IndexCodes() []string { return []string{"NDX"} }

type row struct {
	Symbol      string `json:"symbol"`
	CompanyName string `json:"companyName"`
}

// FetchIndexConstituents 取纳斯达克 100 的全部成分股。名单必须完整才返回：行数等于接口报的 totalrecords、
// 在 95～110 之间、没有重复、每个拼出的标的 ID 都合法；不满足就整次报错，标签同步会保留原成员。
func (p *Provider) FetchIndexConstituents(ctx context.Context, indexCode string) (marketdata.InstrumentSnapshot, error) {
	if strings.TrimSpace(indexCode) != "NDX" {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 纳斯达克没有登记指数 %q 的成分列表", marketdata.ErrInvalidRequest, indexCode)
	}
	fetchedAt := p.now().UTC()
	rows, err := p.fetchRows(ctx)
	if err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("指数 %s 成分股：%w", indexCode, err)
	}
	seen := make(map[string]struct{}, len(rows))
	var instruments []marketdata.Instrument
	var invalid []string
	for _, r := range rows {
		code := strings.TrimSpace(r.Symbol)
		subjectID := code + "." + stockus.ExchangeNasdaq
		if _, err := (stockus.Rules{}).ParseSubject(marketdata.InstrumentEquity, subjectID); err != nil {
			invalid = append(invalid, fmt.Sprintf("%q", r.Symbol))
			continue
		}
		if _, dup := seen[subjectID]; dup {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股里 %s 重复出现", marketdata.ErrProtocol, indexCode, subjectID)
		}
		seen[subjectID] = struct{}{}
		instruments = append(instruments, marketdata.Instrument{SubjectID: subjectID, ProviderSymbol: code, Exchange: stockus.ExchangeNasdaq, Name: strings.TrimSpace(r.CompanyName), Status: "active"})
	}
	if len(invalid) > 0 {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股里有不合法的代码：%s", marketdata.ErrProtocol, indexCode, strings.Join(invalid, "、"))
	}
	snapshot := marketdata.InstrumentSnapshot{
		SnapshotID:     marketdata.SnapshotID(providerID, marketID, fetchedAt),
		SourceProvider: providerID,
		MarketID:       marketID,
		FetchedAt:      fetchedAt,
		Complete:       true,
		PageCount:      1,
		ExchangeCounts: map[string]int{stockus.ExchangeNasdaq: len(instruments)},
		Instruments:    instruments,
	}
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股：%v", marketdata.ErrProtocol, indexCode, err)
	}
	return snapshot, nil
}

// fetchRows 请求接口并校验行数；返回的行已经满足行数等于 totalrecords 且在 95～110 之间。
func (p *Provider) fetchRows(ctx context.Context) ([]row, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+listPath, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: 请求已取消或超时：%w（%v）", marketdata.ErrTimeout, ctxErr, err)
		}
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return nil, marketdata.ErrRateLimited
	case response.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: 读返回内容：%v", marketdata.ErrProtocol, err)
	}
	var payload struct {
		Data *struct {
			TotalRecords *int `json:"totalrecords"`
			Data         *struct {
				Rows []row `json:"rows"`
			} `json:"data"`
		} `json:"data"`
		Status *struct {
			RCode *int `json:"rCode"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	if payload.Status != nil && payload.Status.RCode != nil && *payload.Status.RCode != http.StatusOK {
		return nil, fmt.Errorf("%w: 接口返回失败 rCode=%d", marketdata.ErrProtocol, *payload.Status.RCode)
	}
	if payload.Data == nil || payload.Data.TotalRecords == nil || payload.Data.Data == nil {
		return nil, fmt.Errorf("%w: 返回内容里没有 data.totalrecords 或 data.data.rows", marketdata.ErrProtocol)
	}
	rows, total := payload.Data.Data.Rows, *payload.Data.TotalRecords
	if len(rows) != total {
		return nil, fmt.Errorf("%w: 取到 %d 行，接口报的总数是 %d", marketdata.ErrProtocol, len(rows), total)
	}
	if len(rows) < minRows || len(rows) > maxRows {
		return nil, fmt.Errorf("%w: 成分股有 %d 行，不在合理范围 %d～%d 内", marketdata.ErrProtocol, len(rows), minRows, maxRows)
	}
	return rows, nil
}
