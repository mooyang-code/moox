package stockcn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"golang.org/x/time/rate"
)

// FieldSymbol 返回附加字段来源用的沪深股票代码（sh600519、sz000001）；北交所和无法识别的标的返回 false，按“没有值”处理。
func FieldSymbol(subjectID string) (string, bool) {
	symbol, err := ProviderSymbol(subjectID)
	if err != nil || symbol[:2] == "bj" {
		return "", false
	}
	return symbol, true
}

// DayKeys 返回 [start, end) 覆盖的各个 UTC 零点（A 股日线的行键是交易日期的 UTC 零点）。
// 非交易日也在其中：那些键上没有 K 线，值不会被用到。
func DayKeys(start, end time.Time) []time.Time {
	var keys []time.Time
	for day := start.UTC().Truncate(24 * time.Hour); day.Before(end); day = day.Add(24 * time.Hour) {
		keys = append(keys, day)
	}
	return keys
}

// DateInt 把日期写成 yyyymmdd 整数，用来比较先后。
func DateInt(day time.Time) int {
	day = day.UTC()
	return day.Year()*10000 + int(day.Month())*100 + day.Day()
}

// FieldHTTP 是附加字段来源共用的取数客户端：按数据源配置文件里的限速参数控制节奏、并发和单次超时。
type FieldHTTP struct {
	client  *http.Client
	limiter *rate.Limiter
	slots   chan struct{}
	timeout time.Duration
	maxBody int64
	headers map[string]string
}

// NewFieldHTTP 构造取数客户端；policy 没有配置时用 fallback，maxBody 是响应体的大小上限。
func NewFieldHTTP(client *http.Client, policy, fallback marketdata.RateLimitPolicy, maxBody int64) *FieldHTTP {
	if policy.RequestsPerSecond <= 0 {
		policy = fallback
	}
	if client == nil {
		client = &http.Client{}
	}
	return &FieldHTTP{
		client: client, limiter: rate.NewLimiter(rate.Limit(policy.RequestsPerSecond), max(policy.Burst, 1)),
		slots: make(chan struct{}, max(policy.MaxConcurrent, 1)), timeout: policy.RequestTimeout, maxBody: maxBody,
	}
}

// WithHeaders 设置每次请求都带的请求头（覆盖默认的 User-Agent）：有的来源只对浏览器的请求头返回数据。
func (h *FieldHTTP) WithHeaders(headers map[string]string) *FieldHTTP {
	h.headers = headers
	return h
}

// Get 发一次 GET。404 时 found 为 false（来源没有这个标的）；其他非 2xx、超时、响应体不完整都返回错误。
// Go 的 HTTP 客户端默认带 Accept-Encoding: gzip 并自动解压，这里不手工设置。
func (h *FieldHTTP) Get(ctx context.Context, url string) (body []byte, found bool, err error) {
	if err := h.limiter.Wait(ctx); err != nil {
		return nil, false, fmt.Errorf("%w: 等待限速：%v", marketdata.ErrTimeout, err)
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-ctx.Done():
		return nil, false, fmt.Errorf("%w: 等待并发名额：%v", marketdata.ErrTimeout, ctx.Err())
	}
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", "moox-collector/1.0")
	for name, value := range h.headers {
		req.Header.Set(name, value)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, false, marketdata.ErrRateLimited
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, false, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, h.maxBody+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, false, fmt.Errorf("%w: 读取响应：%v", marketdata.ErrTimeout, err)
		}
		return nil, false, fmt.Errorf("%w: 读取响应：%v", marketdata.ErrProtocol, err)
	}
	if int64(len(body)) > h.maxBody {
		return nil, false, fmt.Errorf("%w: 响应超过 %d 字节", marketdata.ErrProtocol, h.maxBody)
	}
	return body, true, nil
}

// EastMoneyHeaders 是东方财富接口要的请求头：它只对浏览器的 User-Agent 和来自行情页的 Referer 稳定返回数据。
func EastMoneyHeaders() map[string]string {
	return map[string]string{"User-Agent": "Mozilla/5.0", "Referer": "https://quote.eastmoney.com/", "Accept": "*/*"}
}
