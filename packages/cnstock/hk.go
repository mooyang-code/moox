package cnstock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 港股：标的 ID 与腾讯代码的互转、腾讯实时快照、恒生指数成分列表。Trade 的港股模拟盘用它做行情和标的规则。

// ExchangeHongKong 是香港交易所的后缀，与采集、策略使用的标的 ID 一致（00700.XHKG）。
const ExchangeHongKong = "XHKG"

// hkCodeLength 是港股代码的位数（保留前导零，例如 00700）。
const hkCodeLength = 5

// HKInstrumentID 把五位港股代码换成标的 ID：00700 → 00700.XHKG。
func HKInstrumentID(code string) (string, error) {
	code = strings.TrimSpace(code)
	if len(code) != hkCodeLength {
		return "", fmt.Errorf("%w：港股代码 %q 不是五位数字", ErrInvalidCode, code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%w：港股代码 %q 不是五位数字", ErrInvalidCode, code)
		}
	}
	return code + "." + ExchangeHongKong, nil
}

// HKCode 把标的 ID 换成场所内的五位代码：00700.XHKG → 00700。
func HKCode(instrumentID string) (string, error) {
	code, exchange, ok := strings.Cut(strings.ToUpper(strings.TrimSpace(instrumentID)), ".")
	if !ok || exchange != ExchangeHongKong {
		return "", fmt.Errorf("%w：港股标的 ID %q 的交易所后缀必须是 %s", ErrInvalidCode, instrumentID, ExchangeHongKong)
	}
	if _, err := HKInstrumentID(code); err != nil {
		return "", err
	}
	return code, nil
}

// hkTencentPrefix 是腾讯港股实时行情的代码前缀。带 r_ 的是实时行情；不带的（hk00700）延迟 15 分钟以上，不能用来撮合。
const hkTencentPrefix = "r_hk"

// HKTencentSymbol 把标的 ID 换成腾讯实时行情代码：00700.XHKG → r_hk00700。
func HKTencentSymbol(instrumentID string) (string, error) {
	code, err := HKCode(instrumentID)
	if err != nil {
		return "", err
	}
	return hkTencentPrefix + code, nil
}

// 腾讯港股实时快照 qt.gtimg.cn/q=r_hk00700,r_hk00005,... 返回 GBK 文本，每只一行：v_r_hk00700="100~腾讯控股~00700~...";
// 字段用 ~ 分隔，共 78 个。下面是用到的字段位置，2026-11-17 收市后用 00700、00005、09988、02800、01810、00001、
// 00204（停牌）、00232（长期停牌）等真实响应核对（样例见 testdata/tencent_hk_20261117.txt）：
// 每手股数 00700 是 100、00005 是 400、02800 是 500、01810 是 200，与港交所公布的一致。
// 上游不认识或已退市的代码（例如 03333）不返回任何行。
// 2026-11-18 盘中实测（两次请求、各 9 只，相隔 22 秒）：第 30 字段是这只标的最后一笔成交的时间，不随快照刷新
// （00027 两次都是 09:42:43），所以 Trade 不拿它当报价时间；第 9、19 字段盘中都等于现价，第 10、20 字段（买量、卖量）为 0，
// 五档其余位置全是 0——r_hk 没有真实盘口，模拟撮合只能按现价。第 40 字段对正常交易的标的为空。
const (
	hkFieldCode      = 2
	hkFieldLast      = 3
	hkFieldPrevClose = 4
	hkFieldBid       = 9
	hkFieldAsk       = 19
	hkFieldTime      = 30 // 最后一笔成交的时间，2006/01/02 15:04:05，香港时间
	hkFieldStatus    = 40 // 空：正常；S：停牌
	hkFieldLotSize   = 60 // 每手股数
	hkMinFields      = 61
)

// hkTimeLayout 是腾讯港股快照的行情时间格式。香港时间与北京时间相同（UTC+8，没有夏令时）。
const hkTimeLayout = "2006/01/02 15:04:05"

// ParseTencentHK 解析一次港股快照响应，规则与 ParseTencent 相同：某一只的行格式不对时跳过这一行、记日志，其他照常返回。
// 每手股数读不出来（不是正整数）时 LotSize 为 0，由调用方按“取不到每手股数”处理，不猜。
func ParseTencentHK(body []byte) []Quote {
	var quotes []Quote
	for _, line := range bytes.Split(body, []byte(";")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		quote, ok, err := parseTencentHKLine(line)
		if err != nil {
			log.Printf("cnstock: 跳过一行腾讯港股快照：%v", err)
			continue
		}
		if ok {
			quotes = append(quotes, quote)
		}
	}
	return quotes
}

func parseTencentHKLine(line []byte) (Quote, bool, error) {
	name, value, ok := bytes.Cut(line, []byte("="))
	if !ok || !bytes.HasPrefix(name, []byte("v_")) {
		return Quote{}, false, fmt.Errorf("%w：腾讯港股快照行格式不对", ErrProtocol)
	}
	symbol := string(bytes.TrimPrefix(name, []byte("v_")))
	value = bytes.Trim(value, `"`)
	if strings.HasPrefix(symbol, "pv_none_match") || len(value) <= 2 {
		return Quote{}, false, nil
	}
	code, isHK := strings.CutPrefix(symbol, hkTencentPrefix)
	if !isHK {
		return Quote{}, false, fmt.Errorf("%w：腾讯港股快照的代码 %q 不是 %s 开头", ErrProtocol, symbol, hkTencentPrefix)
	}
	instrumentID, err := HKInstrumentID(code)
	if err != nil {
		return Quote{}, false, fmt.Errorf("%w：%v", ErrProtocol, err)
	}
	fields := splitGBK(value, '~')
	if len(fields) < hkMinFields {
		return Quote{}, false, fmt.Errorf("%w：腾讯港股快照 %s 只有 %d 个字段", ErrProtocol, symbol, len(fields))
	}
	if fields[hkFieldCode] != code {
		return Quote{}, false, fmt.Errorf("%w：腾讯港股快照 %s 的代码字段是 %q", ErrProtocol, symbol, fields[hkFieldCode])
	}
	at, err := time.ParseInLocation(hkTimeLayout, fields[hkFieldTime], shanghai)
	if err != nil {
		return Quote{}, false, fmt.Errorf("%w：腾讯港股快照 %s 的行情时间 %q", ErrProtocol, symbol, fields[hkFieldTime])
	}
	// 只核对过 S（停牌）；其他非空状态含义不明，同样按不能交易处理。
	quote := Quote{InstrumentID: instrumentID, Time: at.UTC(), Halted: strings.TrimSpace(fields[hkFieldStatus]) != ""}
	for _, item := range []struct {
		target *string
		index  int
	}{
		{&quote.Last, hkFieldLast}, {&quote.PrevClose, hkFieldPrevClose},
		{&quote.Bid, hkFieldBid}, {&quote.Ask, hkFieldAsk},
	} {
		text, err := positiveDecimal(fields[item.index])
		if err != nil {
			return Quote{}, false, fmt.Errorf("%w：腾讯港股快照 %s 第 %d 个字段：%v", ErrProtocol, symbol, item.index, err)
		}
		*item.target = text
	}
	if lot, err := strconv.ParseInt(strings.TrimSpace(fields[hkFieldLotSize]), 10, 64); err == nil && lot > 0 {
		quote.LotSize = lot
	}
	return quote, true, nil
}

// SnapshotHK 取一批港股的实时快照，超过 TencentBatchLimit 只时分批请求；规则与 Snapshot 相同。
func (c TencentClient) SnapshotHK(ctx context.Context, instrumentIDs []string) (map[string]Quote, error) {
	result := make(map[string]Quote, len(instrumentIDs))
	for start := 0; start < len(instrumentIDs); start += TencentBatchLimit {
		end := min(start+TencentBatchLimit, len(instrumentIDs))
		symbols := make([]string, 0, end-start)
		for _, instrumentID := range instrumentIDs[start:end] {
			symbol, err := HKTencentSymbol(instrumentID)
			if err != nil {
				return nil, err
			}
			symbols = append(symbols, symbol)
		}
		body, err := c.fetch(ctx, symbols)
		if err != nil {
			return nil, err
		}
		received := time.Now().UTC()
		for _, quote := range ParseTencentHK(body) {
			if quote.Time.After(received) {
				quote.Time = received
			}
			result[quote.InstrumentID] = quote
		}
	}
	return result, nil
}

// 恒生指数成分：腾讯恒生指数网页（gu.qq.com/hkHSI/zs/cfg）自己用的成分列表接口，与 Collector 同步港股标的范围
// 用的是同一个接口（modules/collector/internal/sources/stockhk/tencent）。必须同时带 offset 和 count=20 才能翻页。
const (
	// DefaultHKIndexBaseURL 是成分列表接口的地址。
	DefaultHKIndexBaseURL = "https://proxy.finance.qq.com"
	hkIndexListPath       = "/cgi/cgi-bin/rank/hk/getList"
	hkIndexBoard          = "hkHSI"
	hkIndexPageSize       = 20
	// hkIndexMaxPages 防止上游一直返回数据时无限翻页：恒生指数最多 100 只成分（5 页）。
	hkIndexMaxPages = 20
)

// HSIConstituents 翻页取恒生指数的全部成分，返回标的 ID（00700.XHKG）。名单必须完整才返回：条数等于接口报的 total、
// total 大于一页（等于一页说明翻页参数没生效）、没有重复。baseURL 为空用默认地址，client 为空用 15 秒超时。
func HSIConstituents(ctx context.Context, client *http.Client, baseURL string) ([]string, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = DefaultHKIndexBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	total := 0
	seen := map[string]struct{}{}
	var instrumentIDs []string
	for page, offset := 0, 0; ; page, offset = page+1, offset+hkIndexPageSize {
		if page >= hkIndexMaxPages {
			return nil, fmt.Errorf("%w：恒生指数成分翻页超过 %d 页", ErrProtocol, hkIndexMaxPages)
		}
		codes, pageTotal, err := fetchHSIPage(ctx, client, base, offset)
		if err != nil {
			return nil, fmt.Errorf("恒生指数成分（offset=%d）：%w", offset, err)
		}
		switch {
		case page == 0 && pageTotal <= hkIndexPageSize:
			return nil, fmt.Errorf("%w：恒生指数成分接口报的总数是 %d，不超过一页 %d 条，翻页参数没有生效", ErrProtocol, pageTotal, hkIndexPageSize)
		case page == 0:
			total = pageTotal
		case pageTotal != total:
			return nil, fmt.Errorf("%w：恒生指数成分接口报的总数从 %d 变成 %d（offset=%d）", ErrProtocol, total, pageTotal, offset)
		}
		if len(codes) == 0 {
			return nil, fmt.Errorf("%w：恒生指数成分 offset=%d 这一页为空，已取到 %d 只，接口报的总数是 %d", ErrProtocol, offset, len(instrumentIDs), total)
		}
		for _, raw := range codes {
			code, ok := strings.CutPrefix(strings.TrimSpace(raw), "hk")
			instrumentID, err := HKInstrumentID(code)
			if !ok || err != nil {
				return nil, fmt.Errorf("%w：恒生指数成分代码 %q 不是 hk 加五位数字", ErrProtocol, raw)
			}
			if _, dup := seen[instrumentID]; dup {
				return nil, fmt.Errorf("%w：恒生指数成分里 %s 重复出现", ErrProtocol, instrumentID)
			}
			seen[instrumentID] = struct{}{}
			instrumentIDs = append(instrumentIDs, instrumentID)
		}
		if len(instrumentIDs) >= total {
			break
		}
	}
	if len(instrumentIDs) != total {
		return nil, fmt.Errorf("%w：恒生指数成分取到 %d 只，接口报的总数是 %d", ErrProtocol, len(instrumentIDs), total)
	}
	return instrumentIDs, nil
}

func fetchHSIPage(ctx context.Context, client *http.Client, base string, offset int) ([]string, int, error) {
	values := url.Values{
		"board_type": {hkIndexBoard},
		"sort_type":  {"price"},
		"direct":     {"down"},
		"offset":     {strconv.Itoa(offset)},
		"count":      {strconv.Itoa(hkIndexPageSize)},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+hkIndexListPath+"?"+values.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("%w：%v", ErrTimeout, err)
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return nil, 0, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("%w：读取响应：%v", ErrProtocol, err)
	}
	var payload struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			RankList []struct {
				Code string `json:"code"`
			} `json:"rank_list"`
			Total *int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, fmt.Errorf("%w：%v", ErrProtocol, err)
	}
	if payload.Code == nil || *payload.Code != 0 {
		return nil, 0, fmt.Errorf("%w：接口返回失败 msg=%q", ErrProtocol, payload.Msg)
	}
	if payload.Data == nil || payload.Data.Total == nil {
		return nil, 0, fmt.Errorf("%w：返回内容里没有 data.total", ErrProtocol)
	}
	codes := make([]string, len(payload.Data.RankList))
	for i, item := range payload.Data.RankList {
		codes[i] = item.Code
	}
	return codes, *payload.Data.Total, nil
}
