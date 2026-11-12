// Package tencent 是港股的腾讯指数成分来源：只取恒生指数的成分股，不取 K 线、不取全部标的。
package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

const (
	providerID = "tencent"
	marketID   = "stockhk"
	exchange   = "XHKG"
	// DefaultSourceID 是行情源目录里登记的 source_id。
	DefaultSourceID = "index_constituent_http"

	host        = "proxy.finance.qq.com"
	defaultBase = "https://" + host
	// 腾讯恒生指数网页（gu.qq.com/hkHSI/zs/cfg）自己用的成分列表接口，不需要登录、Referer 或特定 UA。
	listPath = "/cgi/cgi-bin/rank/hk/getList"
	// pageSize 是每页条数，只能是 20：必须同时带 offset 和 count=20 才能翻页，count 写别的值或不带都只返回
	// 前 20 条并把 total 报成 20（2026-11-12 实测）。
	pageSize = 20
	// maxPages 是翻页的上限，防止上游一直返回数据时无限翻页：恒生指数最多 100 只成分（5 页），
	// 20 页（400 只）给同一接口的其他指数留足余量。
	maxPages = 20
)

// indexBoards 是指数代码到接口 board_type 的对照。同一接口还能取国企指数、恒生科技，用到时再登记。
var indexBoards = map[string]string{"HSI": "hkHSI"}

type Config struct {
	BaseURL    string
	SourceID   string
	HTTPClient *http.Client
	Now        func() time.Time
	// RequestTimeout 是单页请求的超时。
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
		cfg.RequestTimeout = 15 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.RequestTimeout}
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.sourceID, DisplayName: "Tencent HK", Hosts: []string{host}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

// IndexCodes 返回能取成分股的指数代码。
func (p *Provider) IndexCodes() []string { return []string{"HSI"} }

// FetchIndexConstituents 翻页取指数的全部成分股。名单必须完整才返回：条数等于接口报的 total、total 大于一页
// （等于一页说明翻页参数没生效）、没有重复；不满足就报错，标签同步会保留原成员。
func (p *Provider) FetchIndexConstituents(ctx context.Context, indexCode string) (marketdata.InstrumentSnapshot, error) {
	board, ok := indexBoards[strings.TrimSpace(indexCode)]
	if !ok {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 腾讯没有登记指数 %q 的成分列表", marketdata.ErrInvalidRequest, indexCode)
	}
	fetchedAt := p.now().UTC()
	total, pages := 0, 0
	seen := map[string]struct{}{}
	var instruments []marketdata.Instrument
	for offset := 0; ; offset += pageSize {
		if pages >= maxPages {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股翻页超过 %d 页", marketdata.ErrProtocol, indexCode, maxPages)
		}
		page, err := p.fetchPage(ctx, board, offset)
		if err != nil {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("指数 %s 成分股（offset=%d）：%w", indexCode, offset, err)
		}
		pages++
		switch {
		case pages == 1 && page.Total <= pageSize:
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股接口报的总数是 %d，不超过一页 %d 条，翻页参数没有生效", marketdata.ErrProtocol, indexCode, page.Total, pageSize)
		case pages == 1:
			total = page.Total
		case page.Total != total:
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股接口报的总数从 %d 变成 %d（offset=%d）", marketdata.ErrProtocol, indexCode, total, page.Total, offset)
		}
		if len(page.Items) == 0 {
			return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股 offset=%d 这一页为空，已取到 %d 只，接口报的总数是 %d", marketdata.ErrProtocol, indexCode, offset, len(instruments), total)
		}
		for _, item := range page.Items {
			instrument, err := instrumentFromItem(item)
			if err != nil {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("指数 %s 成分股：%w", indexCode, err)
			}
			if _, dup := seen[instrument.SubjectID]; dup {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股里 %s 重复出现", marketdata.ErrProtocol, indexCode, instrument.SubjectID)
			}
			seen[instrument.SubjectID] = struct{}{}
			instruments = append(instruments, instrument)
		}
		if len(instruments) >= total {
			break
		}
	}
	if len(instruments) != total {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股取到 %d 只，接口报的总数是 %d", marketdata.ErrProtocol, indexCode, len(instruments), total)
	}
	snapshot := marketdata.InstrumentSnapshot{
		SnapshotID:     marketdata.SnapshotID(providerID, marketID, fetchedAt),
		SourceProvider: providerID,
		MarketID:       marketID,
		FetchedAt:      fetchedAt,
		Complete:       true,
		PageCount:      pages,
		ExchangeCounts: map[string]int{exchange: len(instruments)},
		Instruments:    instruments,
	}
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股：%v", marketdata.ErrProtocol, indexCode, err)
	}
	return snapshot, nil
}

type listItem struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type listPage struct {
	Total int
	Items []listItem
}

func (p *Provider) fetchPage(ctx context.Context, board string, offset int) (listPage, error) {
	values := url.Values{
		"board_type": {board},
		"sort_type":  {"price"},
		"direct":     {"down"},
		"offset":     {strconv.Itoa(offset)},
		"count":      {strconv.Itoa(pageSize)},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+listPath+"?"+values.Encode(), nil)
	if err != nil {
		return listPage{}, err
	}
	request.Header.Set("User-Agent", "moox-collector/1.0")
	response, err := p.client.Do(request)
	if err != nil {
		return listPage{}, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return listPage{}, marketdata.ErrRateLimited
	case response.StatusCode != http.StatusOK:
		return listPage{}, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return listPage{}, fmt.Errorf("%w: 读返回内容：%v", marketdata.ErrProtocol, err)
	}
	var payload struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			RankList []listItem `json:"rank_list"`
			Total    *int       `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return listPage{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	if payload.Code == nil || *payload.Code != 0 {
		return listPage{}, fmt.Errorf("%w: 接口返回失败 msg=%q", marketdata.ErrProtocol, payload.Msg)
	}
	if payload.Data == nil || payload.Data.Total == nil {
		return listPage{}, fmt.Errorf("%w: 返回内容里没有 data.total", marketdata.ErrProtocol)
	}
	return listPage{Total: *payload.Data.Total, Items: payload.Data.RankList}, nil
}

// instrumentFromItem 把一行成分换成标的：hk00700 换成 00700.XHKG，传给数据源的代码是五位数字 00700。
func instrumentFromItem(item listItem) (marketdata.Instrument, error) {
	code, ok := strings.CutPrefix(strings.TrimSpace(item.Code), "hk")
	if !ok || !isCode(code) {
		return marketdata.Instrument{}, fmt.Errorf("%w: 成分代码 %q 不是 hk 加五位数字", marketdata.ErrProtocol, item.Code)
	}
	return marketdata.Instrument{SubjectID: code + "." + exchange, ProviderSymbol: code, Exchange: exchange, Name: strings.TrimSpace(item.Name), Status: "active"}, nil
}

func isCode(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
