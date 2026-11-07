package events

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// 标的下架事件里 payload 的取值。
const (
	DelistMarketSpot = "SPOT"
	DelistMarketSwap = "SWAP"

	DelistSourceBinanceSpotSchedule     = "BINANCE_SPOT_SCHEDULE"
	DelistSourceBinanceSwapExchangeInfo = "BINANCE_SWAP_EXCHANGE_INFO"
	DelistSourceOKXSwapInstruments      = "OKX_SWAP_INSTRUMENTS"
	DelistSourceManual                  = "MANUAL"

	DelistScopeAll       = "ALL"
	DelistScopePaperOnly = "PAPER_ONLY"

	// DelistSystemSpaceID 是下架事件的信封空间：下架是交易所层面的事实，不属于任何业务空间，沿用系统事件的空间。
	DelistSystemSpaceID = "mooxsys"
)

// InstrumentDelistSubjectID 返回下架事件的信封 subject：“交易所:市场:交易所代码”。
func InstrumentDelistSubjectID(exchange, market, exchangeSymbol string) string {
	return exchange + ":" + market + ":" + exchangeSymbol
}

// InstrumentDelistEventID 由（交易所、市场、代码、范围、下架时间）确定性生成事件 ID，
// 同一条下架重复发布时 NATS 会在去重窗口内丢弃重复消息；范围也参与计算，演练用的“仅模拟盘”登记不会挡住同标的同时间的真实下架。
func InstrumentDelistEventID(exchange, market, exchangeSymbol, scope string, delistAt time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{exchange, market, exchangeSymbol, scope, strconv.FormatInt(delistAt.UTC().UnixMilli(), 10)}, "\x00")))
	return "delist-" + hex.EncodeToString(sum[:16])
}

func validateInstrumentDelistScheduled(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.InstrumentDelistScheduled)
	if !ok {
		return fmt.Errorf("instrument delist scheduled payload has type %T", value)
	}
	if !validRequiredToken(payload.GetExchange()) || !validRequiredToken(payload.GetExchangeSymbol()) || strings.Contains(payload.GetExchange()+payload.GetExchangeSymbol(), ":") {
		return fmt.Errorf("instrument delist scheduled exchange and exchange_symbol are required and must not contain colon")
	}
	if payload.GetExchange() != strings.ToLower(payload.GetExchange()) {
		return fmt.Errorf("instrument delist scheduled exchange %q must be lower case", payload.GetExchange())
	}
	if payload.GetExchangeSymbol() != strings.ToUpper(payload.GetExchangeSymbol()) {
		return fmt.Errorf("instrument delist scheduled exchange_symbol %q must be upper case", payload.GetExchangeSymbol())
	}
	switch payload.GetMarket() {
	case DelistMarketSpot, DelistMarketSwap:
	default:
		return fmt.Errorf("instrument delist scheduled market %q is invalid", payload.GetMarket())
	}
	if message == nil || message.GetSubjectId() != InstrumentDelistSubjectID(payload.GetExchange(), payload.GetMarket(), payload.GetExchangeSymbol()) {
		return fmt.Errorf("instrument delist scheduled instrument does not match subject_id")
	}
	if delistAt := payload.GetDelistAt(); delistAt == nil || delistAt.CheckValid() != nil || delistAt.AsTime().IsZero() {
		return fmt.Errorf("instrument delist scheduled delist_at is invalid")
	}
	if firstSeen := payload.GetFirstSeenAt(); firstSeen == nil || firstSeen.CheckValid() != nil {
		return fmt.Errorf("instrument delist scheduled first_seen_at is invalid")
	}
	switch payload.GetSource() {
	case DelistSourceBinanceSpotSchedule, DelistSourceBinanceSwapExchangeInfo, DelistSourceOKXSwapInstruments, DelistSourceManual:
	default:
		return fmt.Errorf("instrument delist scheduled source %q is invalid", payload.GetSource())
	}
	switch payload.GetScope() {
	case DelistScopeAll, DelistScopePaperOnly:
	default:
		return fmt.Errorf("instrument delist scheduled scope %q is invalid", payload.GetScope())
	}
	return nil
}
