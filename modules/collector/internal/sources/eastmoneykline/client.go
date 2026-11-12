// Package eastmoneykline 是东方财富 K 线接口（push2his 的 /api/qt/stock/kline/get）的共用代码：请求拼装、请求头、
// 返回解析、收盘标签换开盘键。各市场的东方财富数据源只给出自己的部分：标的 ID 怎么换成 secid、成交量倍数、
// 时间标签的时区、支持的周期；限速与单次根数由各自的数据源配置给出。
package eastmoneykline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

const (
	// Host 是 K 线接口的域名：5m K 线只在 push2his 有数据（push2 返回空列表），1m 两边一致，K 线统一走 push2his。
	Host = "push2his.eastmoney.com"
	// DefaultMaxBarsPerRequest 是数据源配置没写单次根数时的取值。
	DefaultMaxBarsPerRequest = 1205

	defaultBaseURL  = "https://" + Host
	defaultEndpoint = "/api/qt/stock/kline/get"
	// minuteLayout 是日内 K 线的时间标签格式（2026-11-04 09:35）。
	minuteLayout = "2006-01-02 15:04"
)

// klt 是 K 线接口的 klt 参数：日内是周期分钟数，日线是 101。
var klt = map[marketdata.Frequency]string{
	marketdata.FrequencyMinute: "1",
	marketdata.Frequency5Min:   "5",
	marketdata.FrequencyDay:    "101",
}

// DefaultRateLimit 是数据源配置没写限速时的取值。
func DefaultRateLimit() marketdata.RateLimitPolicy {
	return marketdata.RateLimitPolicy{RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
}

// Config 的前一半来自数据源配置（地址、source_id、单次根数），后一半是市场自己的部分。
type Config struct {
	// BaseURL、Endpoint 为空时用 push2his 的 K 线接口。
	BaseURL  string
	Endpoint string
	// ProviderID、SourceID 写进归一化后的每一根。
	ProviderID string
	SourceID   string
	HTTPClient *http.Client
	Now        func() time.Time
	// MaxBarsPerRequest 是按区间取时请求的根数（上游带 beg 时忽略它，返回区间内全部根）。
	MaxBarsPerRequest int

	// Frequencies 是这个市场支持的周期，其余的不发请求直接拒绝。
	Frequencies []string
	// SecID 把标的 ID 换成接口的 secid（A 股 600000.XSHG → 1.600000，港股 00700.XHKG → 116.00700）。
	SecID func(subjectID string) (string, error)
	// VolumeMultiplier 把上游的成交量换成股：A 股的单位是手，乘 100；港股已经是股，乘 1。
	VolumeMultiplier float64
	// Location 是时间标签的时区。日内标签是收盘时刻。
	Location *time.Location
	// DailyClose 是日线的上游时间戳：交易日当地零点之后多久收盘（A 股 15 小时）。不取日线的市场不用写。
	DailyClose time.Duration
}

// Client 按请求取一只标的的一段 K 线。
type Client struct {
	cfg Config
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	cfg.SourceID = strings.ToLower(strings.TrimSpace(cfg.SourceID))
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxBarsPerRequest <= 0 {
		cfg.MaxBarsPerRequest = DefaultMaxBarsPerRequest
	}
	return &Client{cfg: cfg}
}

// SourceID 返回规范化后的 source_id。
func (c *Client) SourceID() string { return c.cfg.SourceID }

// MaxBarsPerRequest 返回按区间取时请求的根数。
func (c *Client) MaxBarsPerRequest() int { return c.cfg.MaxBarsPerRequest }

type response struct {
	Data struct {
		Klines []string `json:"klines"`
	} `json:"data"`
}

// FetchKlines 取一段 K 线，只返回已经收盘的根。
//
// 请求带起止时刻时按 beg、end 两个日期取整天（上游忽略 lmt，返回区间内的全部根），不按时刻截取：定时采集只要目标那一根，
// 也是取当天整天，由流水线按开始时刻挑出来。所以新一根还没出现时（冷门股在边界后一分多钟里，刚收盘那根仍是最后一根）
// 照样取得到目标那根。不带起止时刻时取 end 之前最近 Limit 根。
//
// 任何一根解析或校验失败，整次请求失败，不会只丢掉那一根。
func (c *Client) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	frequency, _ := req.FrequencyValue() // Validate 已校验
	kltValue, ok := klt[frequency]
	if !ok || !slices.Contains(c.cfg.Frequencies, string(frequency)) {
		return nil, fmt.Errorf("%w: %s 不支持 %s", marketdata.ErrUnsupportedFrequency, c.cfg.ProviderID, req.Frequency)
	}
	secid, err := c.cfg.SecID(req.SubjectID)
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
	rangeRequest := !req.StartTime.IsZero() || !req.EndTime.IsZero()
	if rangeRequest {
		requestLimit = c.cfg.MaxBarsPerRequest
	}
	// 带 beg 时上游忽略 lmt、返回 [beg, end] 内的全部 K 线；不带 beg 时只取 end 之前最近 lmt 根。
	query := "secid=" + url.QueryEscape(secid) + "&klt=" + kltValue + "&fqt=0"
	if !req.StartTime.IsZero() {
		query += "&beg=" + url.QueryEscape(req.StartTime.UTC().Format("20060102"))
	}
	query += "&end=" + url.QueryEscape(endDate) + "&lmt=" + strconv.Itoa(requestLimit) +
		"&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13" +
		"&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+c.cfg.Endpoint+"?"+query, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Referer", "https://quote.eastmoney.com/")
	resp, err := c.cfg.HTTPClient.Do(httpReq)
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
		return nil, fmt.Errorf("%w: 读取返回内容：%v", marketdata.ErrProtocol, err)
	}
	var payload response
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	lines := payload.Data.Klines
	if !rangeRequest && len(lines) > req.Limit {
		lines = lines[len(lines)-req.Limit:]
	}
	rows := make([]marketdata.NormalizedKline, 0, len(lines))
	now := c.cfg.Now().UTC()
	for _, line := range lines {
		row, err := c.normalize(req, frequency, line, now)
		if err != nil {
			return nil, err
		}
		if !now.Before(row.BarEnd) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return rows, nil
}

// normalize 把返回里的一行（时间标签,开,收,高,低,成交量,成交额,…）归一化成一根 K 线。
// 日内的标签是收盘时刻，键是它减去一个周期；日线的标签是交易日期，键是这一天的 UTC 零点（与 barclock 的 1d 键一致）。
func (c *Client) normalize(req marketdata.KlineRequest, frequency marketdata.Frequency, line string, fetchedAt time.Time) (marketdata.NormalizedKline, error) {
	fields := strings.Split(line, ",")
	if len(fields) < 7 {
		return marketdata.NormalizedKline{}, fmt.Errorf("%w: %s 的 K 线少于 7 列", marketdata.ErrProtocol, c.cfg.ProviderID)
	}
	var values [6]float64 // 开、收、高、低、成交量、成交额
	for index := range values {
		value, err := strconv.ParseFloat(strings.TrimSpace(fields[index+1]), 64)
		if err != nil {
			return marketdata.NormalizedKline{}, fmt.Errorf("%w: K 线第 %d 列 %q 不是数字", marketdata.ErrProtocol, index+2, fields[index+1])
		}
		values[index] = value
	}
	label := strings.TrimSpace(fields[0])
	var barStart, barEnd, providerTime time.Time
	if frequency == marketdata.FrequencyDay {
		day, err := time.Parse(time.DateOnly, label)
		if err != nil {
			return marketdata.NormalizedKline{}, fmt.Errorf("%w: 日线的时间标签 %q 不是日期", marketdata.ErrProtocol, fields[0])
		}
		barStart, barEnd = day, marketdata.FrequencyDay.BarEnd(day)
		providerTime = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, c.cfg.Location).Add(c.cfg.DailyClose).UTC()
	} else {
		closeAt, err := time.ParseInLocation(minuteLayout, label, c.cfg.Location)
		if err != nil {
			return marketdata.NormalizedKline{}, fmt.Errorf("%w: 日内 K 线的时间标签 %q 无效", marketdata.ErrProtocol, fields[0])
		}
		barEnd = closeAt.UTC()
		barStart = barEnd.Add(-frequency.Duration())
		providerTime = barEnd
	}
	row := marketdata.NormalizedKline{
		SubjectID:         req.SubjectID,
		ProviderID:        c.cfg.ProviderID,
		SourceID:          c.cfg.SourceID,
		ProviderSymbol:    req.ProviderSymbol,
		Frequency:         string(frequency),
		BarStart:          barStart,
		BarEnd:            barEnd,
		Open:              values[0],
		High:              values[2],
		Low:               values[3],
		Close:             values[1],
		Volume:            values[4] * c.cfg.VolumeMultiplier,
		Amount:            values[5],
		ProviderTimestamp: providerTime,
		FetchedAt:         fetchedAt,
		RequestID:         req.RequestID,
	}
	if err := marketdata.ValidateNormalizedKline(row); err != nil {
		return marketdata.NormalizedKline{}, err
	}
	return row, nil
}
