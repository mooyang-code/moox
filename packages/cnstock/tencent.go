package cnstock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 腾讯实时快照 qt.gtimg.cn/q=sh600519,sz000001,... 返回 GBK 文本，每只一行：v_sh600519="1~贵州茅台~600519~...";
// 字段用 ~ 分隔。下面是用到的字段位置（2026-11-08 按 600519、688981、002306、002129 等收盘后的真实样例核对）。
const (
	tencentFieldCode      = 2
	tencentFieldLast      = 3
	tencentFieldPrevClose = 4
	tencentFieldBid       = 9
	tencentFieldAsk       = 19
	tencentFieldTime      = 30 // YYYYMMDDHHMMSS，北京时间
	tencentFieldStatus    = 40 // 空：正常；S：停牌；D：退市；C：北交所旧代码已换号
	tencentFieldLimitUp   = 47 // 涨停价，没有涨跌停时是 -1
	tencentFieldLimitDown = 48 // 跌停价
	tencentMinFields      = 49
)

const (
	tencentStatusHalted   = "S"
	tencentStatusDelisted = "D"
	// tencentStatusRenamed 是北交所旧代码（43、83、87 开头）换成 920 号段后旧代码上的状态（2026-11-08 样例 430047），当退市处理。
	tencentStatusRenamed = "C"
)

// TencentBatchLimit 是一次快照请求最多带的代码数。
const TencentBatchLimit = 60

// DefaultTencentBaseURL 是腾讯实时快照的地址。
const DefaultTencentBaseURL = "https://qt.gtimg.cn"

// shanghai 是北京时间（没有夏令时，用固定时差，不依赖系统时区库）。
var shanghai = time.FixedZone("CST", 8*60*60)

// Quote 是一只股票的实时快照。价格是十进制文本（原样取自上游，不经过浮点数），为空表示上游没有给出。
type Quote struct {
	InstrumentID string
	Last         string
	PrevClose    string
	// Bid、Ask 是买一、卖一价；没有挂单时为空（上游给 0.00）。
	Bid string
	Ask string
	// LimitUp、LimitDown 是当天的涨停价、跌停价；没有涨跌停限制时为空。
	LimitUp   string
	LimitDown string
	// Time 是行情时间。
	Time time.Time
	// Halted 为真表示停牌；Delisted 为真表示已退市（含北交所旧代码已换号）。两者都不能交易。
	Halted   bool
	Delisted bool
}

// ParseTencent 解析一次快照响应。上游对不认识的代码返回 v_pv_none_match="1"; 这类行跳过。
// 某一只的行格式不对时跳过这一行、记日志，其他股票照常返回：一只坏数据不能拖垮整批，缺的那只由调用方按“取不到报价”处理。
// 正文是 GBK：GBK 双字节字符的第二个字节可能是 0x7E（~），所以按字节切分时跳过双字节字符，股票名不需要解码。
func ParseTencent(body []byte) []Quote {
	var quotes []Quote
	for _, line := range bytes.Split(body, []byte(";")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		quote, ok, err := parseTencentLine(line)
		if err != nil {
			log.Printf("cnstock: 跳过一行腾讯快照：%v", err)
			continue
		}
		if ok {
			quotes = append(quotes, quote)
		}
	}
	return quotes
}

// parseTencentLine 解析一行；ok 为假表示这一行不是报价（上游不认识的代码）。
func parseTencentLine(line []byte) (Quote, bool, error) {
	name, value, ok := bytes.Cut(line, []byte("="))
	if !ok || !bytes.HasPrefix(name, []byte("v_")) {
		return Quote{}, false, fmt.Errorf("%w：腾讯快照行格式不对", ErrProtocol)
	}
	symbol := string(bytes.TrimPrefix(name, []byte("v_")))
	value = bytes.Trim(value, `"`)
	if strings.HasPrefix(symbol, "pv_none_match") || len(value) <= 2 {
		return Quote{}, false, nil
	}
	fields := splitGBK(value, '~')
	if len(fields) < tencentMinFields {
		return Quote{}, false, fmt.Errorf("%w：腾讯快照 %s 只有 %d 个字段", ErrProtocol, symbol, len(fields))
	}
	instrumentID, err := FromTencentSymbol(symbol)
	if err != nil {
		return Quote{}, false, fmt.Errorf("%w：%v", ErrProtocol, err)
	}
	if code := fields[tencentFieldCode]; !strings.HasPrefix(instrumentID, code+".") {
		return Quote{}, false, fmt.Errorf("%w：腾讯快照 %s 的代码字段是 %q", ErrProtocol, symbol, code)
	}
	at, err := time.ParseInLocation("20060102150405", fields[tencentFieldTime], shanghai)
	if err != nil {
		return Quote{}, false, fmt.Errorf("%w：腾讯快照 %s 的行情时间 %q", ErrProtocol, symbol, fields[tencentFieldTime])
	}
	quote := Quote{
		InstrumentID: instrumentID,
		Time:         at.UTC(),
		Halted:       fields[tencentFieldStatus] == tencentStatusHalted,
		Delisted:     fields[tencentFieldStatus] == tencentStatusDelisted || fields[tencentFieldStatus] == tencentStatusRenamed,
	}
	for _, item := range []struct {
		target *string
		index  int
	}{
		{&quote.Last, tencentFieldLast}, {&quote.PrevClose, tencentFieldPrevClose},
		{&quote.Bid, tencentFieldBid}, {&quote.Ask, tencentFieldAsk},
		{&quote.LimitUp, tencentFieldLimitUp}, {&quote.LimitDown, tencentFieldLimitDown},
	} {
		text, err := positiveDecimal(fields[item.index])
		if err != nil {
			return Quote{}, false, fmt.Errorf("%w：腾讯快照 %s 第 %d 个字段：%v", ErrProtocol, symbol, item.index, err)
		}
		*item.target = text
	}
	return quote, true, nil
}

// positiveDecimal 校验十进制文本；0 和负数（-1 表示没有）返回空串。
func positiveDecimal(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", fmt.Errorf("%q 不是数字", raw)
	}
	if value <= 0 {
		return "", nil
	}
	return raw, nil
}

// splitGBK 按单字节分隔符切分 GBK 文本：遇到 0x81–0xFE 的首字节就连同下一个字节一起跳过。
func splitGBK(value []byte, separator byte) []string {
	var fields []string
	start := 0
	for i := 0; i < len(value); i++ {
		switch b := value[i]; {
		case b >= 0x81 && b <= 0xFE && i+1 < len(value):
			i++
		case b == separator:
			fields = append(fields, string(value[start:i]))
			start = i + 1
		}
	}
	return append(fields, string(value[start:]))
}

// TencentClient 请求腾讯实时快照。
type TencentClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Snapshot 取一批股票的快照，超过 TencentBatchLimit 只时分批请求。返回的快照按标的 ID 索引；上游不认识的代码不在结果里。
// 行情时间取“腾讯给的时间”和“收到响应的时刻”中较早的一个：本机时钟慢于上游时，报价不会显得来自未来。
func (c TencentClient) Snapshot(ctx context.Context, instrumentIDs []string) (map[string]Quote, error) {
	result := make(map[string]Quote, len(instrumentIDs))
	for start := 0; start < len(instrumentIDs); start += TencentBatchLimit {
		end := min(start+TencentBatchLimit, len(instrumentIDs))
		symbols := make([]string, 0, end-start)
		for _, instrumentID := range instrumentIDs[start:end] {
			symbol, err := TencentSymbol(instrumentID)
			if err != nil {
				return nil, err
			}
			symbols = append(symbols, symbol)
		}
		body, err := c.fetch(ctx, symbols)
		if err != nil {
			return nil, err
		}
		quotes := ParseTencent(body)
		received := time.Now().UTC()
		for _, quote := range quotes {
			if quote.Time.After(received) {
				quote.Time = received
			}
			result[quote.InstrumentID] = quote
		}
	}
	return result, nil
}

func (c TencentClient) fetch(ctx context.Context, symbols []string) ([]byte, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultTencentBaseURL
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/q="+strings.Join(symbols, ","), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, fmt.Errorf("%w：%v", ErrTimeout, err)
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w：读取响应：%v", ErrProtocol, err)
	}
	return body, nil
}
