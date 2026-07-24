package binance

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RecentTradeAPI is the exchange boundary used by TickCollector.
type RecentTradeAPI interface {
	GetRecentTrades(context.Context, *exchange.TradeRequest) ([]*exchange.Trade, error)
}

// TickCollector polls the public cursorable aggregate-trade endpoint and
// publishes normalized market ticks. It is intentionally stateless across
// process restarts: the deterministic EventID and JetStream duplicate window
// make a replayed REST page harmless, while streamcalc remains the sole K-line
// aggregator.
type TickCollector struct {
	spotAPI RecentTradeAPI
	swapAPI RecentTradeAPI
	publish func(context.Context, events.EventType, proto.Message, events.PublishOptions) error

	mu      sync.Mutex
	lastIDs map[string]int64
}

// NewTickCollector constructs a tick collector with explicit dependencies.
// The runtime normally uses SetEventPublisher, while integration testkits can
// inject a publisher without mutating process-global state.
func NewTickCollector(spotAPI, swapAPI RecentTradeAPI, publisher *events.Publisher) *TickCollector {
	collector := &TickCollector{spotAPI: spotAPI, swapAPI: swapAPI, lastIDs: make(map[string]int64)}
	if publisher != nil {
		collector.publish = func(ctx context.Context, event events.EventType, message proto.Message, opts events.PublishOptions) error {
			_, err := publisher.Publish(ctx, event, message, opts)
			return err
		}
	}
	return collector
}

func (c *TickCollector) Source() string   { return "binance" }
func (c *TickCollector) DataType() string { return "tick" }

func (c *TickCollector) Collect(ctx context.Context, params *sources.CollectParams) error {
	if c == nil {
		return fmt.Errorf("Tick采集器为空")
	}
	if params == nil || strings.TrimSpace(params.SpaceID) == "" || strings.TrimSpace(params.Symbol) == "" {
		return fmt.Errorf("Tick采集需要 space_id 和 symbol")
	}
	if eventPublisher == nil && c.publish == nil {
		return fmt.Errorf("Tick采集未配置 EventBus publisher")
	}
	api, err := c.api(params.InstType)
	if err != nil {
		return err
	}
	key := tickCursorKey(params)
	lastID := c.lastID(key)
	fromID := int64(0)
	if lastID > 0 {
		fromID = lastID + 1
	}
	trades, err := api.GetRecentTrades(ctx, &exchange.TradeRequest{Symbol: params.Symbol, Limit: 100, FromID: fromID})
	if err != nil {
		return err
	}
	sort.SliceStable(trades, func(i, j int) bool {
		if trades[i].ID != trades[j].ID {
			return trades[i].ID < trades[j].ID
		}
		return trades[i].TradeTime.Before(trades[j].TradeTime)
	})
	subjectID := strings.TrimSpace(params.SubjectID)
	if subjectID == "" {
		subjectID = params.Symbol
	}
	for _, trade := range trades {
		if trade == nil || trade.ID <= lastID {
			continue
		}
		// Event subject_id is the canonical MooX instrument identity. The
		// exchange symbol may be formatted differently (for example BTCUSDT vs
		// BTC-USDT), so downstream processors must receive the same value in the
		// typed payload and the envelope.
		payload, err := tickPayload(subjectID, trade)
		if err != nil {
			return err
		}
		eventID := fmt.Sprintf("%s:%s:%s:%d", params.SpaceID, strings.ToLower(params.InstType), params.Symbol, trade.ID)
		publish := c.publish
		if publish == nil {
			publish = func(ctx context.Context, event events.EventType, message proto.Message, opts events.PublishOptions) error {
				_, err := eventPublisher.Publish(ctx, event, message, opts)
				return err
			}
		}
		if err := publish(ctx, events.TickReceived, payload, events.PublishOptions{EventID: eventID, OccurredAt: trade.TradeTime, SpaceID: params.SpaceID, SubjectID: subjectID}); err != nil {
			return fmt.Errorf("发布Tick事件: %w", err)
		}
		lastID = trade.ID
		c.setLastID(key, lastID)
	}
	return nil
}

func (c *TickCollector) api(instType string) (RecentTradeAPI, error) {
	switch instType {
	case InstTypeSPOT:
		if c.spotAPI == nil {
			return nil, fmt.Errorf("Spot Tick API 未配置")
		}
		return c.spotAPI, nil
	case InstTypeSWAP:
		if c.swapAPI == nil {
			return nil, fmt.Errorf("Swap Tick API 未配置")
		}
		return c.swapAPI, nil
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", instType)
	}
}

func tickPayload(symbol string, trade *exchange.Trade) (*marketpb.Tick, error) {
	if trade == nil || trade.ID <= 0 || trade.TradeTime.IsZero() {
		return nil, fmt.Errorf("Tick成交ID和时间不能为空")
	}
	price, err := trade.Price.Float64()
	if err != nil {
		return nil, fmt.Errorf("解析Tick价格: %w", err)
	}
	quantity, err := trade.Quantity.Float64()
	if err != nil {
		return nil, fmt.Errorf("解析Tick数量: %w", err)
	}
	return &marketpb.Tick{Exchange: "binance", TradeId: strconv.FormatInt(trade.ID, 10), Symbol: symbol, Price: price, Quantity: quantity, BuyerMaker: trade.BuyerMaker, TradeTime: timestamppb.New(trade.TradeTime.UTC())}, nil
}

func tickCursorKey(params *sources.CollectParams) string {
	return strings.Join([]string{params.SpaceID, strings.ToLower(params.InstType), params.Symbol}, "|")
}

func (c *TickCollector) lastID(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastIDs == nil {
		c.lastIDs = make(map[string]int64)
	}
	return c.lastIDs[key]
}

func (c *TickCollector) setLastID(key string, id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastIDs == nil {
		c.lastIDs = make(map[string]int64)
	}
	if id > c.lastIDs[key] {
		c.lastIDs[key] = id
	}
}

var _ sources.Collector = (*TickCollector)(nil)
var _ RecentTradeAPI = (*binanceapi.SpotAPI)(nil)
