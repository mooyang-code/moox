package binance

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
)

// StreamConfig 是币安 WS 推送的地址与限额（binance.yaml 的 stream 段）。
type StreamConfig struct {
	SpotEndpoints     []string `yaml:"spot_endpoints"`
	SwapEndpoints     []string `yaml:"swap_endpoints"`
	StreamsPerConn    int      `yaml:"streams_per_conn"`
	StreamsPerMessage int      `yaml:"streams_per_message"`
	MessagesPerSecond int      `yaml:"messages_per_second"`
	MaxConnAgeHours   int      `yaml:"max_conn_age_hours"`
}

func (c StreamConfig) withDefaults() StreamConfig {
	if len(c.SpotEndpoints) == 0 {
		c.SpotEndpoints = []string{"wss://stream.binance.com:9443/stream", "wss://stream.binance.com:443/stream"}
	}
	if len(c.SwapEndpoints) == 0 {
		c.SwapEndpoints = []string{"wss://fstream.binance.com/market/stream"}
	}
	if c.StreamsPerConn <= 0 {
		c.StreamsPerConn = 1000
	}
	if c.StreamsPerMessage <= 0 {
		c.StreamsPerMessage = 100
	}
	if c.MessagesPerSecond <= 0 {
		// 现货文档每秒 5 条，合约文档每秒 10 条；两边都按 5 条保守取值。
		c.MessagesPerSecond = 5
	}
	if c.MaxConnAgeHours <= 0 {
		c.MaxConnAgeHours = 24
	}
	return c
}

// binanceIntervals 是币安 K 线支持的周期（与 marketdata 的规范频率同名）。
var binanceIntervals = map[marketdata.Frequency]string{}

func init() {
	for _, name := range []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d"} {
		if frequency, err := marketdata.ParseFrequency(name); err == nil && string(frequency) == name {
			binanceIntervals[frequency] = name
		}
	}
}

// StreamSource 返回某个市场的 WS K 线来源：现货订阅 kline，合约订阅连续合约 K 线（按 pair）。
func (a *MarketDataAdapter) StreamSource(marketType string) (stream.Source, error) {
	cfg, err := ResolveStreamConfig()
	if err != nil {
		return nil, err
	}
	return NewStreamSource(marketType, cfg)
}

// NewStreamSource 按配置构造来源（测试可直接给地址）。
func NewStreamSource(marketType string, cfg StreamConfig) (stream.Source, error) {
	cfg = cfg.withDefaults()
	switch strings.ToLower(strings.TrimSpace(marketType)) {
	case "spot":
		return &streamSource{swap: false, endpoints: cfg.SpotEndpoints, cfg: cfg}, nil
	case "swap":
		return &streamSource{swap: true, endpoints: cfg.SwapEndpoints, cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("binance stream does not support market %q", marketType)
	}
}

type streamSource struct {
	swap      bool
	endpoints []string
	cfg       StreamConfig
}

var _ stream.Provider = (*MarketDataAdapter)(nil)

func (s *streamSource) Key() marketdata.SourceKey {
	if s.swap {
		return marketdata.SourceKey{ProviderID: "binance", SourceID: "swap_http"}
	}
	return marketdata.SourceKey{ProviderID: "binance", SourceID: "spot_http"}
}

func (s *streamSource) MarketType() string {
	if s.swap {
		return "swap"
	}
	return "spot"
}

func (s *streamSource) Endpoints() []string { return append([]string(nil), s.endpoints...) }

func (s *streamSource) Limits() stream.Limits {
	return stream.Limits{
		StreamsPerConn: s.cfg.StreamsPerConn, StreamsPerMessage: s.cfg.StreamsPerMessage, MessagesPerSecond: s.cfg.MessagesPerSecond,
		MaxConnAge: time.Duration(s.cfg.MaxConnAgeHours) * time.Hour, ConnectsPerWindow: 300, ConnectWindow: 5 * time.Minute,
	}
}

func (s *streamSource) Stream(symbol string, frequency marketdata.Frequency) (string, bool) {
	interval, ok := binanceIntervals[frequency]
	symbol = strings.ToLower(strings.TrimSpace(symbol))
	if !ok || symbol == "" {
		return "", false
	}
	if s.swap {
		return symbol + "_perpetual@continuousKline_" + interval, true
	}
	return symbol + "@kline_" + interval, true
}

// StatusStreams：合约订阅 !contractInfo（上下架、交割时推送合约状态），用于尽早发现下架。
func (s *streamSource) StatusStreams() []string {
	if s.swap {
		return []string{"!contractInfo"}
	}
	return nil
}

type streamRequest struct {
	Method string   `json:"method"`
	Params []string `json:"params"`
	ID     int      `json:"id"`
}

func (s *streamSource) Subscribe(streams []string, id int) ([]byte, error) {
	return json.Marshal(streamRequest{Method: "SUBSCRIBE", Params: streams, ID: id})
}

func (s *streamSource) Unsubscribe(streams []string, id int) ([]byte, error) {
	return json.Marshal(streamRequest{Method: "UNSUBSCRIBE", Params: streams, ID: id})
}

func (s *streamSource) Keepalive() stream.Keepalive {
	return stream.Keepalive{ServerPing: true, ReadTimeout: time.Minute}
}

// streamEnvelope 是组合流的外层：{"stream": "...", "data": {...}}；订阅回执是 {"result": null, "id": 1} 或 {"error": {...}, "id": 1}。
type streamEnvelope struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	} `json:"error"`
}

// streamPayload 显式声明全部大小写不同的键：encoding/json 对字段名大小写不敏感，只声明一个会把另一个的值串进来。
type streamPayload struct {
	EventType string       `json:"e"`
	EventTime int64        `json:"E"`
	Symbol    string       `json:"s"`
	Pair      string       `json:"ps"`
	Contract  string       `json:"ct"`
	Status    string       `json:"cs"`
	Kline     *streamKline `json:"k"`
	Delivery  int64        `json:"dt"`
	Onboard   int64        `json:"ot"`
}

type streamKline struct {
	Start          int64  `json:"t"`
	End            int64  `json:"T"`
	Symbol         string `json:"s"`
	Interval       string `json:"i"`
	FirstTrade     int64  `json:"f"`
	LastTrade      int64  `json:"L"`
	Open           string `json:"o"`
	Close          string `json:"c"`
	High           string `json:"h"`
	Low            string `json:"l"`
	Volume         string `json:"v"`
	Trades         int64  `json:"n"`
	Final          bool   `json:"x"`
	QuoteVolume    string `json:"q"`
	TakerBuyVolume string `json:"V"`
	TakerBuyQuote  string `json:"Q"`
	Ignore         string `json:"B"`
}

func (s *streamSource) Decode(raw []byte, receivedAt time.Time) ([]stream.Event, error) {
	var envelope streamEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode binance stream message: %w", err)
	}
	if envelope.ID != nil && envelope.Stream == "" {
		event := stream.Event{Kind: stream.EventControl, ID: *envelope.ID}
		if envelope.Error != nil {
			event.Err = fmt.Sprintf("%d %s", envelope.Error.Code, envelope.Error.Msg)
		}
		return []stream.Event{event}, nil
	}
	if envelope.Error != nil {
		return []stream.Event{{Kind: stream.EventError, Err: fmt.Sprintf("%d %s", envelope.Error.Code, envelope.Error.Msg)}}, nil
	}
	if len(envelope.Data) == 0 {
		return nil, nil
	}
	var payload streamPayload
	if err := json.Unmarshal(envelope.Data, &payload); err != nil {
		return nil, fmt.Errorf("decode binance stream data %s: %w", envelope.Stream, err)
	}
	eventAt := time.UnixMilli(payload.EventTime).UTC()
	switch payload.EventType {
	case "contractInfo":
		symbol := strings.ToUpper(firstNonEmpty(payload.Pair, payload.Symbol))
		return []stream.Event{{Kind: stream.EventStatus, Symbol: symbol, EventAt: eventAt, Trading: strings.EqualFold(payload.Status, "TRADING")}}, nil
	case "kline", "continuous_kline":
	default:
		return nil, nil
	}
	if payload.Kline == nil {
		return nil, fmt.Errorf("binance %s message has no kline", payload.EventType)
	}
	symbol := payload.Symbol
	if s.swap {
		symbol = payload.Pair
	}
	symbol = strings.ToUpper(strings.TrimSpace(firstNonEmpty(symbol, payload.Kline.Symbol)))
	kline, err := s.normalize(payload.Kline, receivedAt)
	if err != nil {
		return nil, fmt.Errorf("binance %s %s: %w", payload.EventType, symbol, err)
	}
	kind := stream.EventKlineUpdate
	if payload.Kline.Final {
		kind = stream.EventKlineFinal
	}
	return []stream.Event{{Kind: kind, Symbol: symbol, Kline: kline, EventAt: eventAt}}, nil
}

// normalize 与 REST 适配器的字段含义一致：VolumeShares=v、AmountCNY=q、TradeCount=n、ProviderTimestamp=收盘时间 T。
func (s *streamSource) normalize(k *streamKline, receivedAt time.Time) (marketdata.NormalizedKline, error) {
	frequency, err := marketdata.ParseFrequency(k.Interval)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	values := make([]float64, 6)
	for index, text := range []string{k.Open, k.High, k.Low, k.Close, k.Volume, k.QuoteVolume} {
		value, parseErr := strconv.ParseFloat(text, 64)
		if parseErr != nil {
			return marketdata.NormalizedKline{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, parseErr)
		}
		values[index] = value
	}
	start := normalizeBarStart(time.UnixMilli(k.Start).UTC(), frequency)
	return marketdata.NormalizedKline{
		ProviderID: "binance", SourceID: s.Key().SourceID, Frequency: string(frequency), BarStart: start, BarEnd: frequency.BarEnd(start),
		Open: values[0], High: values[1], Low: values[2], Close: values[3], VolumeShares: values[4], AmountCNY: values[5], TradeCount: k.Trades,
		ProviderTimestamp: time.UnixMilli(k.End).UTC(), FetchedAt: receivedAt.UTC(),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
