package tdx

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gitee.com/quant1x/exchange"
	"gitee.com/quant1x/gotdx/proto"
	"gitee.com/quant1x/gotdx/quotes"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

type RawKline struct {
	Time                                   time.Time
	Open, High, Low, Close, Volume, Amount float64
}
type RawSecurity struct {
	Market     uint8
	Code, Name string
}
type Client interface {
	Klines(string, uint16, uint16, uint16, bool) ([]RawKline, error)
	Securities(uint8, uint16) ([]RawSecurity, error)
	Close()
}
type Dialer interface {
	Dial(context.Context, string) (Client, error)
}

type Config struct {
	Address string
	Dialer  Dialer
	Now     func() time.Time
}
type Provider struct {
	address  string
	dialer   Dialer
	now      func() time.Time
	location *time.Location
}

func New(cfg Config) *Provider {
	if cfg.Address == "" {
		cfg.Address = os.Getenv("MOOX_TDX_ADDRESS")
	}
	if cfg.Dialer == nil {
		cfg.Dialer = quant1xDialer{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	return &Provider{address: strings.TrimSpace(cfg.Address), dialer: cfg.Dialer, now: cfg.Now, location: location}
}
func (*Provider) ID() marketdata.ProviderID { return "tdx" }
func (*Provider) Capabilities() []providers.Capability {
	result := []providers.Capability{{Feed: providers.FeedInstrument, ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity}}
	for _, instrument := range []marketdata.InstrumentType{marketdata.InstrumentEquity, marketdata.InstrumentETF, marketdata.InstrumentIndex} {
		for _, frequency := range []marketdata.Frequency{marketdata.FrequencyMinute, marketdata.Frequency5Min, marketdata.Frequency15Min, marketdata.Frequency30Min, marketdata.FrequencyHour, marketdata.FrequencyDay} {
			result = append(result, providers.Capability{Feed: providers.FeedKline, ProductType: marketdata.ProductType(instrument), InstrumentType: instrument, Frequency: frequency})
		}
	}
	return result
}

func (p *Provider) FetchKlines(ctx context.Context, gate providers.RequestGate, req providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	category, err := categoryFor(req.Frequency)
	if err != nil {
		return providers.FetchKlinesResult{}, err
	}
	if p.address == "" {
		return providers.FetchKlinesResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, "MOOX_TDX_ADDRESS is required", nil)
	}
	result := providers.FetchKlinesResult{Complete: true}
	startOffset := uint16(0)
	if req.Cursor != "" {
		parsed, parseErr := strconv.ParseUint(req.Cursor, 10, 16)
		if parseErr != nil {
			return result, providers.NewError(providers.ErrorParseFailed, "invalid tdx kline cursor", parseErr)
		}
		startOffset = uint16(parsed)
	}
	for index, subject := range req.Subjects {
		coveredStart := req.StartTime.IsZero()
		permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: index, EndpointClass: "security_bars", RequestCost: 1})
		if err != nil {
			return result, err
		}
		if !permit.Allowed {
			return result, providers.NewError(providers.ErrorRateLimited, permit.DenialReason, nil)
		}
		client, err := p.dialer.Dial(ctx, p.address)
		if err != nil {
			return result, providers.NewError(providers.ErrorTemporarilyUnavailable, "tdx dial", err)
		}
		count := req.Limit
		if count <= 0 || count > 800 {
			count = 800
		}
		values, fetchErr := client.Klines(subject.ProviderSymbol, category, startOffset, uint16(count), req.InstrumentType == marketdata.InstrumentIndex)
		client.Close()
		if fetchErr != nil {
			return result, providers.NewError(providers.ErrorTemporarilyUnavailable, "tdx bars", fetchErr)
		}
		for _, value := range values {
			row, err := p.normalizeKline(subject, req, value)
			if err != nil {
				return result, err
			}
			if !req.StartTime.IsZero() && !row.DataTime.After(req.StartTime.UTC()) {
				coveredStart = true
			}
			if !req.StartTime.IsZero() && row.DataTime.Before(req.StartTime.UTC()) || !req.EndTime.IsZero() && row.DataTime.After(req.EndTime.UTC()) {
				continue
			}
			result.Rows = append(result.Rows, row)
		}
		result.RequestCount++
		if len(values) == count && !coveredStart {
			result.Complete = false
			result.NextCursor = strconv.Itoa(int(startOffset) + len(values))
		}
	}
	return result, nil
}

func (p *Provider) normalizeKline(subject providers.ProviderSubject, req providers.FetchKlinesRequest, value RawKline) (marketdata.ProviderKline, error) {
	decimal := func(raw float64) (marketdata.Decimal, error) {
		return marketdata.ParseDecimal(strconv.FormatFloat(raw, 'f', -1, 64))
	}
	open, err := decimal(value.Open)
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	high, err := decimal(value.High)
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	low, err := decimal(value.Low)
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	closeValue, err := decimal(value.Close)
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	volume, err := decimal(value.Volume)
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	amount, err := decimal(value.Amount)
	if err != nil {
		return marketdata.ProviderKline{}, err
	}
	local := value.Time.In(p.location)
	dataTime, closeTime := value.Time.UTC(), value.Time.Add(time.Duration(req.Frequency.DurationMinutes())*time.Minute).UTC()
	if req.Frequency == marketdata.FrequencyDay {
		dataTime = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, p.location).UTC()
		closeTime = time.Date(local.Year(), local.Month(), local.Day(), 15, 0, 0, 0, p.location).UTC()
	}
	return marketdata.ProviderKline{SubjectID: subject.SubjectID, ProviderID: p.ID(), ProviderSymbol: subject.ProviderSymbol, Frequency: req.Frequency, DataTime: dataTime, CloseTime: closeTime, TradeDate: local.Format("2006-01-02"), FeedScope: string(req.InstrumentType), VolumeUnit: "share", AmountUnit: "CNY", Open: open, High: high, Low: low, Close: closeValue, Volume: &volume, Amount: &amount, ProviderTimestamp: closeTime, FetchedAt: p.now().UTC(), RequestID: "tdx:" + subject.ProviderSymbol + ":" + dataTime.Format(time.RFC3339Nano), Closed: !closeTime.After(p.now().UTC())}, nil
}

func (p *Provider) FetchInstruments(ctx context.Context, gate providers.RequestGate, req providers.FetchInstrumentsRequest) (providers.FetchInstrumentsResult, error) {
	if p.address == "" {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, "MOOX_TDX_ADDRESS is required", nil)
	}
	marketIndex, offset, err := parseCursor(req.Cursor)
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	markets := []uint8{exchange.MarketIdShangHai, exchange.MarketIdShenZhen, exchange.MarketIdBeiJing}
	if marketIndex >= len(markets) {
		return providers.FetchInstrumentsResult{Complete: true}, nil
	}
	permit, err := gate.BeforeRequest(ctx, providers.RequestMeta{ProviderID: p.ID(), RequestIndex: marketIndex*100000 + int(offset), EndpointClass: "security_list", RequestCost: 1})
	if err != nil {
		return providers.FetchInstrumentsResult{}, err
	}
	if !permit.Allowed {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorRateLimited, permit.DenialReason, nil)
	}
	client, err := p.dialer.Dial(ctx, p.address)
	if err != nil {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, "tdx dial", err)
	}
	values, fetchErr := client.Securities(markets[marketIndex], offset)
	client.Close()
	if fetchErr != nil {
		return providers.FetchInstrumentsResult{}, providers.NewError(providers.ErrorTemporarilyUnavailable, "tdx security list", fetchErr)
	}
	result := providers.FetchInstrumentsResult{RequestCount: 1}
	for _, value := range values {
		if len(value.Code) != 6 {
			continue
		}
		subjectID := value.Code + "." + string(exchangeID(value.Market))
		instrument := classify(value.Market, value.Code)
		if !wantedInstrument(req.InstrumentTypes, instrument) {
			continue
		}
		result.Instruments = append(result.Instruments, providers.ProviderInstrument{SubjectID: subjectID, ProviderID: p.ID(), ProviderSymbol: value.Code, ExchangeID: exchangeID(value.Market), ProductType: marketdata.ProductType(instrument), InstrumentType: instrument, Name: value.Name, Currency: "CNY", Status: "active", EffectiveAt: req.SnapshotAt.UTC(), FetchedAt: p.now().UTC(), RequestID: fmt.Sprintf("tdx:security-list:%d:%d", value.Market, offset)})
	}
	if len(values) < 1000 {
		marketIndex++
		offset = 0
	} else {
		offset += uint16(len(values))
	}
	result.Complete = marketIndex >= len(markets)
	if !result.Complete {
		result.NextCursor = fmt.Sprintf("%d:%d", marketIndex, offset)
	}
	return result, nil
}

func categoryFor(value marketdata.Frequency) (uint16, error) {
	switch value {
	case marketdata.FrequencyMinute:
		return proto.KLINE_TYPE_1MIN, nil
	case marketdata.Frequency5Min:
		return proto.KLINE_TYPE_5MIN, nil
	case marketdata.Frequency15Min:
		return proto.KLINE_TYPE_15MIN, nil
	case marketdata.Frequency30Min:
		return proto.KLINE_TYPE_30MIN, nil
	case marketdata.FrequencyHour:
		return proto.KLINE_TYPE_1HOUR, nil
	case marketdata.FrequencyDay:
		return proto.KLINE_TYPE_RI_K, nil
	default:
		return 0, providers.NewError(providers.ErrorUnsupported, "tdx frequency", nil)
	}
}
func parseCursor(raw string) (int, uint16, error) {
	if raw == "" {
		return 0, 0, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 0, 0, providers.NewError(providers.ErrorParseFailed, "invalid tdx cursor", nil)
	}
	market, err1 := strconv.Atoi(parts[0])
	offset, err2 := strconv.ParseUint(parts[1], 10, 16)
	if err1 != nil || err2 != nil || market < 0 {
		return 0, 0, providers.NewError(providers.ErrorParseFailed, "invalid tdx cursor", nil)
	}
	return market, uint16(offset), nil
}
func classify(market uint8, code string) marketdata.InstrumentType {
	if market == exchange.MarketIdShangHai && strings.HasPrefix(code, "000") || market == exchange.MarketIdShenZhen && strings.HasPrefix(code, "399") {
		return marketdata.InstrumentIndex
	}
	if market == exchange.MarketIdShangHai && strings.HasPrefix(code, "5") || market == exchange.MarketIdShenZhen && (strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16")) {
		return marketdata.InstrumentETF
	}
	return marketdata.InstrumentEquity
}
func wantedInstrument(values []marketdata.InstrumentType, value marketdata.InstrumentType) bool {
	if len(values) == 0 {
		return true
	}
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func exchangeID(market uint8) marketdata.ExchangeID {
	switch market {
	case exchange.MarketIdShangHai:
		return "XSHG"
	case exchange.MarketIdBeiJing:
		return "XBSE"
	default:
		return "XSHE"
	}
}

type quant1xDialer struct{}

func (quant1xDialer) Dial(_ context.Context, address string) (Client, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	client, err := quotes.NewStdApiWithServers([]quotes.Server{{Name: "moox", Host: host, Port: port}})
	if err != nil {
		return nil, err
	}
	return quant1xClient{client}, nil
}

type quant1xClient struct{ value *quotes.StdApi }

func (c quant1xClient) Klines(code string, category, start, count uint16, index bool) ([]RawKline, error) {
	var rsp *quotes.SecurityBarsReply
	var err error
	if index {
		rsp, err = c.value.GetIndexBars(code, category, start, count)
	} else {
		rsp, err = c.value.GetKLine(code, category, start, count)
	}
	if err != nil {
		return nil, err
	}
	out := make([]RawKline, 0, len(rsp.List))
	for _, item := range rsp.List {
		out = append(out, RawKline{Time: time.Date(item.Year, time.Month(item.Month), item.Day, item.Hour, item.Minute, 0, 0, time.FixedZone("CST", 8*3600)), Open: item.Open, High: item.High, Low: item.Low, Close: item.Close, Volume: item.Vol, Amount: item.Amount})
	}
	return out, nil
}
func (c quant1xClient) Securities(market uint8, start uint16) ([]RawSecurity, error) {
	rsp, err := c.value.GetSecurityList(exchange.MarketType(market), start)
	if err != nil {
		return nil, err
	}
	out := make([]RawSecurity, 0, len(rsp.List))
	for _, item := range rsp.List {
		out = append(out, RawSecurity{Market: market, Code: item.Code, Name: item.Name})
	}
	return out, nil
}
func (c quant1xClient) Close() { c.value.Close() }

var _ providers.KlineProvider = (*Provider)(nil)
var _ providers.InstrumentProvider = (*Provider)(nil)
