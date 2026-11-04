package eastmoney

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

type Config struct {
	// BaseURL 是标的列表（clist）的地址；KlineBaseURL 是 K 线的地址。两者不同：5m K 线只在 push2his 有数据。
	BaseURL                  string
	KlineBaseURL             string
	KlineEndpoint            string
	SourceID                 string
	HTTPClient               *http.Client
	Now                      func() time.Time
	InstrumentRequestTimeout time.Duration
	RateLimit                marketdata.RateLimitPolicy
	MaxBarsPerRequest        int
}

type Provider struct {
	baseURL             string
	klineBaseURL        string
	klineEndpoint       string
	sourceID            string
	client              *http.Client
	now                 func() time.Time
	rateLimit           marketdata.RateLimitPolicy
	instrumentRateLimit marketdata.RateLimitPolicy
	maxBars             int
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://push2.eastmoney.com"
	}
	if cfg.KlineBaseURL == "" {
		cfg.KlineBaseURL = eastMoneyKlineBase
	}
	if cfg.KlineEndpoint == "" {
		cfg.KlineEndpoint = "/api/qt/stock/kline/get"
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = "stockcn_http"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = marketdata.RateLimitPolicy{RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	}
	if cfg.InstrumentRequestTimeout <= 0 {
		cfg.InstrumentRequestTimeout = 15 * time.Second
	}
	if cfg.HTTPClient == nil {
		// Keep the HTTP transport at least as patient as the instrument page
		// guard; otherwise a configured 15-second guard is silently truncated.
		cfg.HTTPClient = &http.Client{Timeout: cfg.InstrumentRequestTimeout}
	}
	if cfg.MaxBarsPerRequest <= 0 {
		cfg.MaxBarsPerRequest = 1205
	}
	instrumentRateLimit := cfg.RateLimit
	instrumentRateLimit.RequestTimeout = cfg.InstrumentRequestTimeout
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), klineBaseURL: strings.TrimRight(cfg.KlineBaseURL, "/"), klineEndpoint: cfg.KlineEndpoint, sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now, rateLimit: cfg.RateLimit, instrumentRateLimit: instrumentRateLimit, maxBars: cfg.MaxBarsPerRequest}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "eastmoney", SourceID: p.sourceID, DisplayName: "EastMoney", Hosts: []string{eastMoneyPushHost, eastMoneyKlineHost}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stockcn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m", "5m"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: p.maxBars,
		// 时间标签是 K 线的收盘时刻（2026-11-04 实测：1m 首根 09:31、5m 首根 09:35，末根 11:30、15:00，
		// 午后首根 13:01 / 13:05），首根含 09:25 开盘集合竞价的成交。
		TimestampMode: marketdata.TimestampModeClose,
		// 1m 只能取到当天：保持 24 小时。5m 实测能取到约 1500 根（约 31 个交易日），保守声明 30 个自然日。
		History:            marketdata.KlineHistoryCapability{MaxLookback: 24 * time.Hour},
		HistoryByFrequency: map[string]marketdata.KlineHistoryCapability{"5m": {MaxLookback: 30 * 24 * time.Hour}},
		RateLimit:          p.rateLimit,
	}
}

func (p *Provider) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{
		Markets:      []string{"stockcn"},
		Exchanges:    []string{"XSHG", "XSHE", "XBSE"},
		FullSnapshot: true,
		PageSize:     500,
		RateLimit:    p.instrumentRateLimit,
	}
}

type eastMoneyResponse struct {
	Data struct {
		Klines []string `json:"klines"`
	} `json:"data"`
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	frequency, _ := req.FrequencyValue() // Validate 已校验
	klt, ok := eastMoneyKlt[frequency]
	if !ok {
		return nil, fmt.Errorf("%w: eastmoney 不支持 %s", marketdata.ErrUnsupportedFrequency, req.Frequency)
	}
	secid, err := commonsrc.EastMoneySecID(req.SubjectID)
	if err != nil {
		return nil, err
	}
	endDate := "20500101"
	if !req.EndTime.IsZero() {
		endDate = req.EndTime.UTC().Format("20060102")
	} else if !req.Now.IsZero() {
		endDate = req.Now.UTC().Format("20060102")
	}
	requestLimit := req.Limit
	historicalRequest := !req.StartTime.IsZero() || !req.EndTime.IsZero()
	if historicalRequest {
		requestLimit = p.maxBars
	}
	// 带 beg 时上游忽略 lmt、返回 [beg, end] 内的全部 K 线；实时请求不带 beg，只取 end 之前最近 lmt 根。
	query := "secid=" + url.QueryEscape(secid) + "&klt=" + klt + "&fqt=0"
	if !req.StartTime.IsZero() {
		query += "&beg=" + url.QueryEscape(req.StartTime.UTC().Format("20060102"))
	}
	query += "&end=" + url.QueryEscape(endDate) + "&lmt=" + strconv.Itoa(requestLimit) +
		"&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13" +
		"&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.klineBaseURL+p.klineEndpoint+"?"+query, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Referer", "https://quote.eastmoney.com/")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, marketdata.ErrRateLimited
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read response body: %v", marketdata.ErrProtocol, err)
	}
	var payload eastMoneyResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	lines := payload.Data.Klines
	if !historicalRequest && len(lines) > req.Limit {
		lines = lines[len(lines)-req.Limit:]
	}
	rows := make([]marketdata.NormalizedKline, 0, len(lines))
	now := p.now().UTC()
	for _, line := range lines {
		fields := strings.Split(line, ",")
		if len(fields) < 7 {
			return nil, fmt.Errorf("%w: eastmoney kline columns", marketdata.ErrProtocol)
		}
		openValue, err := commonsrc.ParseFloat(fields[1])
		if err != nil {
			return nil, err
		}
		closeValue, err := commonsrc.ParseFloat(fields[2])
		if err != nil {
			return nil, err
		}
		highValue, err := commonsrc.ParseFloat(fields[3])
		if err != nil {
			return nil, err
		}
		lowValue, err := commonsrc.ParseFloat(fields[4])
		if err != nil {
			return nil, err
		}
		volumeValue, err := commonsrc.ParseFloat(fields[5])
		if err != nil {
			return nil, err
		}
		amountValue, err := commonsrc.ParseFloat(fields[6])
		if err != nil {
			return nil, err
		}
		row, err := commonsrc.NormalizeIntradayKline(req.SubjectID, "eastmoney", req.ProviderSymbol, frequency, marketdata.TimestampModeClose, fields[0], openValue, highValue, lowValue, closeValue, volumeValue, amountValue, eastMoneyVolumeMultiplier, now, req.RequestID)
		if err != nil {
			return nil, err
		}
		row.SourceID = p.sourceID
		if !now.Before(row.BarEnd) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return rows, nil
}

func (p *Provider) FetchInstrumentSnapshot(ctx context.Context, req marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	marketID := strings.TrimSpace(string(req.MarketID))
	if marketID == "" {
		marketID = "stockcn"
	}
	if marketID != "stockcn" {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: market_id %q is unsupported", marketdata.ErrInvalidRequest, req.MarketID)
	}
	fetchedAt := req.SnapshotAt.UTC()
	if req.SnapshotAt.IsZero() {
		fetchedAt = p.now().UTC()
	}
	snapshot, _, err := p.fetchClistSnapshot(ctx, eastMoneyAShareFilter, p.InstrumentSpec().PageSize, fetchedAt)
	return snapshot, err
}

// IndexCodes 返回登记了成分板块的指数代码。
func (p *Provider) IndexCodes() []string {
	codes := make([]string, 0, len(eastMoneyIndexBoards))
	for code := range eastMoneyIndexBoards {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// FetchIndexConstituents 取指数的全部成分股：与全部 A 股同一个 clist 列表请求，筛选条件换成该指数的成分板块。
// 条数必须等于接口自报的 total，翻页不完整就报错。
func (p *Provider) FetchIndexConstituents(ctx context.Context, indexCode string) (marketdata.InstrumentSnapshot, error) {
	board, ok := eastMoneyIndexBoards[strings.TrimSpace(indexCode)]
	if !ok {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 东方财富没有登记指数 %q 的成分板块", marketdata.ErrInvalidRequest, indexCode)
	}
	snapshot, total, err := p.fetchClistSnapshot(ctx, "b:"+board, eastMoneyClistMaxPageSize, p.now().UTC())
	if err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("指数 %s 成分股：%w", indexCode, err)
	}
	if total <= 0 || len(snapshot.Instruments) != total {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股取到 %d 只，接口报的总数是 %d", marketdata.ErrProtocol, indexCode, len(snapshot.Instruments), total)
	}
	return snapshot, nil
}

// fetchClistSnapshot 按筛选条件 fs 翻页取完整的标的列表，返回快照与接口自报的总数（没报时为 0）。
func (p *Provider) fetchClistSnapshot(ctx context.Context, fs string, pageSize int, fetchedAt time.Time) (marketdata.InstrumentSnapshot, int, error) {
	marketID := "stockcn"
	builder := commonsrc.NewInstrumentSnapshotBuilder(p.Descriptor().ID, marketID, fetchedAt)
	declaredTotal := 0
	itemsSeen := 0
	sawUnverifiedShortPage := false
	activeBase := ""
	for page := 1; ; page++ {
		if page > maxInstrumentPages {
			return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument pagination exceeded %d pages", marketdata.ErrProtocol, maxInstrumentPages)
		}
		query := url.Values{
			"pn":     {strconv.Itoa(page)},
			"pz":     {strconv.Itoa(pageSize)},
			"fs":     {fs},
			"fid":    {"f12"},
			"fields": {"f12,f13,f14,f115,f152,f103,f128,f129"},
		}
		bases := []string{activeBase}
		if activeBase == "" {
			bases = p.instrumentPageBases()
		}
		var body []byte
		var fetchErr error
		for i, base := range bases {
			body, fetchErr = p.fetchInstrumentPage(ctx, base, query)
			if fetchErr == nil {
				activeBase = base
				break
			}
			if i == len(bases)-1 || !instrumentHostRetryable(fetchErr) {
				return marketdata.InstrumentSnapshot{}, 0, fetchErr
			}
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
		}
		data := commonsrc.ObjectAt(payload, "data")
		if data == nil {
			return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument payload missing data", marketdata.ErrProtocol)
		}
		if total, ok := commonsrc.IntField(data, "total", "count", "total_count", "totalnum", "total_nums"); ok && total > 0 {
			if declaredTotal == 0 {
				declaredTotal = total
			} else if declaredTotal != total {
				return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument total changed from %d to %d on page %d", marketdata.ErrProtocol, declaredTotal, total, page)
			}
		}
		items := commonsrc.ItemSlice(data, "diff", "clist", "list")
		if len(items) == 0 {
			if diff := commonsrc.ObjectAt(data, "diff"); len(diff) > 0 {
				keys := make([]string, 0, len(diff))
				for key := range diff {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					if item, ok := diff[key].(map[string]any); ok {
						items = append(items, item)
					}
				}
			}
		}
		totalPages := commonsrc.PageLimit(data, pageSize)
		hasMore, hasMoreKnown := commonsrc.BoolField(data, "hasnext", "has_next", "hasMore", "has_more")
		if declaredTotal == 0 && len(items) > 0 && len(items) < pageSize {
			sawUnverifiedShortPage = true
			if (totalPages > 0 && page < totalPages) || (hasMoreKnown && hasMore) {
				return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument page %d is short (%d < %d) before a declared terminal page", marketdata.ErrProtocol, page, len(items), pageSize)
			}
		}
		itemsSeen += len(items)
		if declaredTotal > 0 && itemsSeen > declaredTotal {
			return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument items %d exceed declared total %d on page %d", marketdata.ErrProtocol, itemsSeen, declaredTotal, page)
		}
		if len(items) == 0 {
			if declaredTotal > 0 && itemsSeen != declaredTotal {
				return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items on terminal page %d", marketdata.ErrProtocol, declaredTotal, itemsSeen, page)
			}
			if declaredTotal == 0 && sawUnverifiedShortPage {
				return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", marketdata.ErrProtocol)
			}
			if page == 1 || (totalPages > 0 && page < totalPages) || (hasMoreKnown && hasMore) {
				return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument page %d is empty before terminal evidence", marketdata.ErrProtocol, page)
			}
			// Without an explicit total or has-more marker, the extra empty page is
			// the provider's terminal signal; a short final page is valid. Body
			// truncation is still rejected by io.ReadAll before this point.
			builder.NextPage()
			break
		}
		for _, item := range items {
			instrument, err := commonsrc.InstrumentFromFields(
				commonsrc.StringField(item, "f12", "code", "symbol"),
				commonsrc.StringField(item, "symbol", "provider_symbol"),
				commonsrc.StringField(item, "f13", "market", "exchange"),
				commonsrc.StringField(item, "f14", "name", "f14_name"),
				commonsrc.StringField(item, "f107", "status", "state"),
			)
			if err != nil {
				return marketdata.InstrumentSnapshot{}, 0, err
			}
			if err := builder.Add(instrument); err != nil {
				return marketdata.InstrumentSnapshot{}, 0, err
			}
		}
		builder.NextPage()
		if totalPages := commonsrc.PageLimit(data, pageSize); totalPages > 0 {
			if page >= totalPages {
				// Without an explicit page count, PageLimit derives the last
				// page from total/requested page size. Eastmoney caps pz below
				// that size, so keep reading while a non-empty page is still
				// short of the declared total. An explicit pagecount stays terminal.
				_, hasExplicitPageCount := commonsrc.IntField(data, "pagecount", "page_count", "total_pages", "totalpage", "pages")
				if !hasExplicitPageCount && declaredTotal > 0 && itemsSeen < declaredTotal && len(items) > 0 {
					continue
				}
				if declaredTotal > 0 && itemsSeen != declaredTotal {
					return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items after page %d", marketdata.ErrProtocol, declaredTotal, itemsSeen, page)
				}
				if declaredTotal == 0 && sawUnverifiedShortPage {
					return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", marketdata.ErrProtocol)
				}
				break
			}
			continue
		}
		if hasMore, ok := commonsrc.BoolField(data, "hasnext", "has_next", "hasMore", "has_more"); ok {
			if !hasMore {
				if declaredTotal > 0 && itemsSeen != declaredTotal {
					return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument total %d does not match %d items after page %d", marketdata.ErrProtocol, declaredTotal, itemsSeen, page)
				}
				if declaredTotal == 0 && sawUnverifiedShortPage {
					return marketdata.InstrumentSnapshot{}, 0, fmt.Errorf("%w: eastmoney instrument ended after an unverified short page", marketdata.ErrProtocol)
				}
				break
			}
			continue
		}
		// A short page is not terminal evidence unless the response also
		// carries total/page or has-more metadata. Fetch the next page and
		// require an empty terminal response instead.
	}
	snapshot, err := builder.Snapshot()
	return snapshot, declaredTotal, err
}

func (p *Provider) instrumentPageBases() []string {
	bases := []string{p.baseURL}
	if instrumentHost(p.baseURL) == eastMoneyPushHost {
		bases = append(bases, eastMoneyPushFallbackBase)
	}
	return bases
}

func (p *Provider) fetchInstrumentPage(ctx context.Context, base string, query url.Values) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/qt/clist/get?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "moox-collector/1.0")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, marketdata.ErrRateLimited
	}
	if resp.StatusCode >= 400 {
		return nil, &instrumentHTTPError{status: resp.StatusCode, err: fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read response body: %v", marketdata.ErrProtocol, err)
	}
	return body, nil
}

func instrumentHost(base string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func instrumentHostRetryable(err error) bool {
	if errors.Is(err, marketdata.ErrTimeout) {
		return true
	}
	var httpErr *instrumentHTTPError
	return errors.As(err, &httpErr) && httpErr.status >= 500
}

type instrumentHTTPError struct {
	status int
	err    error
}

func (e *instrumentHTTPError) Error() string {
	return e.err.Error()
}

func (e *instrumentHTTPError) Unwrap() error {
	return e.err
}

const (
	maxInstrumentPages        = 128
	eastMoneyPushHost         = "push2.eastmoney.com"
	eastMoneyPushFallbackBase = "https://push2test.eastmoney.com"
	// 5m K 线只在 push2his 有数据（push2 返回空列表），1m 两边一致：K 线统一走 push2his。
	eastMoneyKlineHost = "push2his.eastmoney.com"
	eastMoneyKlineBase = "https://" + eastMoneyKlineHost
	// eastMoneyAShareFilter 是全部 A 股的 clist 筛选条件：深主板、创业板、沪主板。
	eastMoneyAShareFilter = "m:0 t:6,m:0 t:80,m:1 t:2"
	// eastMoneyClistMaxPageSize 是 clist 每页的实际上限：请求更大的 pz 也只返回 100 条（2026-11-04 实测）。
	eastMoneyClistMaxPageSize = 100
	// 成交量单位是手：同一根 5m 东方财富 6735、通达信 673500 股（2026-11-04 实测 600519），乘 100 换成股。
	eastMoneyVolumeMultiplier = 100
)

// eastMoneyIndexBoards 是指数代码到东方财富成分板块的对照：沪深 300 是 BK0500（板块名 HS300_，
// 2026-11-04 实测 clist fs=b:BK0500 返回 total=300、三页各 100 条）。
var eastMoneyIndexBoards = map[string]string{"000300": "BK0500"}

// eastMoneyKlt 是 K 线接口的 klt 参数（周期分钟数）。
var eastMoneyKlt = map[marketdata.Frequency]string{
	marketdata.FrequencyMinute: "1",
	marketdata.Frequency5Min:   "5",
}
