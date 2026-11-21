// Package hkex 是港股场所（HKEX）：交易时段、腾讯实时快照做的公共行情、恒生指数成分做的标的列表。
// 只有模拟盘：行情给 Trade 内的模拟撮合用，没有实盘下单通道，也不接任何券商。时段、快照缓存、只读适配器的通用实现在 stockfeed。
package hkex

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/stockfeed"
	"github.com/mooyang-code/moox/packages/cnstock"
	"trpc.group/trpc-go/trpc-go/log"
)

// SettlementAsset 是港股的计价和结算资产。账户以 HKD 计，不做汇率换算。
const SettlementAsset = "HKD"

// SessionHours 是给人看的下单时段说明。
const SessionHours = "港股 09:30-12:00、13:00-16:00（半日市到 12:00）"

// InstrumentScope 说明港股模拟盘的标的表收录哪些标的，目标里的标的不在表里时写进原因。
const InstrumentScope = "港股模拟盘只收录恒生指数成分里取得到每手股数的标的"

// NewSession 返回港股的交易时段：09:30-12:00、13:00-16:00（取 barclock 的 stockhk，半日市只有上午）。
// 连续竞价时段内都能发单；16:00 之后的收市竞价不参与。
func NewSession() (*stockfeed.Session, error) {
	return stockfeed.NewSession("stockhk", 0)
}

// ConstituentSource 读标的范围（标的 ID）。
type ConstituentSource func(context.Context) ([]string, error)

// NewMarket 用腾讯港股实时快照（r_hk 前缀）和恒生指数成分列表构造港股公共行情；client 为空时用各自的默认超时。
func NewMarket(client *http.Client, seed stockfeed.Seed) *stockfeed.Market {
	tencent := cnstock.TencentClient{HTTPClient: client}
	return NewMarketFrom(tencent.SnapshotHK, func(ctx context.Context) ([]string, error) { return cnstock.HSIConstituents(ctx, client, "") }, seed)
}

// NewMarketFrom 用给定的快照来源和标的范围来源构造港股公共行情（NewMarket 用腾讯的两个接口；测试换成替身）。
func NewMarketFrom(quotes stockfeed.QuoteSource, constituents ConstituentSource, seed stockfeed.Seed) *stockfeed.Market {
	market := &stockfeed.Market{Exchange: exchange.ExchangeHKEX, Label: "港股", FillEmptyBook: true, QuoteTimeIsLastTrade: true, Quotes: quotes, Seed: seed}
	market.List = func(ctx context.Context) ([]exchange.Instrument, error) {
		at := time.Now().UTC()
		if market.Now != nil {
			at = market.Now().UTC()
		}
		return listInstruments(ctx, quotes, constituents, seed, at)
	}
	return market
}

// listInstruments 生成港股的标的表。
//
// 范围：恒生指数成分（与 Collector 默认采集的港股范围同一个腾讯接口），加上库里已有的港股标的——被调出指数的旧成分
// 可能还有持仓，不能从表里消失。
//
// 每手股数：港股逐只不同，取自腾讯实时快照的每手股数字段（见 cnstock.ParseTencentHK；东方财富的行情与 K 线接口、
// 腾讯的成分列表接口都不带这一项）。快照里取不到每手股数的标的：库里已有就沿用库里的规则，库里也没有就不收录——
// 不猜每手股数；之后目标里出现它时整份目标被拒（“没有启用的成员支持标的 …”），原因里带 InstrumentScope。
func listInstruments(ctx context.Context, quotes stockfeed.QuoteSource, constituents ConstituentSource, seed stockfeed.Seed, at time.Time) ([]exchange.Instrument, error) {
	listed, err := constituents(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]exchange.Instrument{}
	if seed != nil {
		seeded, err := seed(ctx)
		if err != nil {
			return nil, err
		}
		for _, instrument := range seeded {
			known[instrument.InstrumentID] = instrument
			listed = append(listed, instrument.InstrumentID)
		}
	}
	// 成分在前、库里已有的在后，去掉重复。
	seen := make(map[string]struct{}, len(listed))
	ids := make([]string, 0, len(listed))
	for _, id := range listed {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	snapshot, err := quotes(ctx, ids)
	if err != nil {
		return nil, err
	}
	instruments := make([]exchange.Instrument, 0, len(ids))
	var missing []string
	for _, id := range ids {
		if lot := snapshot[id].LotSize; lot > 0 {
			instruments = append(instruments, stockInstrument(id, lot, at))
			continue
		}
		if instrument, ok := known[id]; ok {
			instruments = append(instruments, instrument)
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) > 0 {
		log.WarnContextf(ctx, "港股标的取不到每手股数，不收录（不能下单）：%v", missing)
	}
	if len(instruments) == 0 {
		return nil, errors.New("港股标的列表为空：没有一只标的取得到每手股数")
	}
	return instruments, nil
}

// stockInstrument 是一只港股的交易规则：整手交易，每手 lot 股（最少一手、按一手递增）；标的表里的价格步长存价位表
// 最小的一档（0.001），下单限价按价格所在档位对齐（exchange.HKPriceTick）；没有涨跌停和价格笼子（比例为 0）；
// 没有最小名义金额。停牌不写进标的状态，只在报价上标记。
func stockInstrument(instrumentID string, lot int64, at time.Time) exchange.Instrument {
	size := shared.MustDecimal(strconv.FormatInt(lot, 10))
	return exchange.Instrument{
		Exchange: exchange.ExchangeHKEX, MarketType: exchange.MarketTypeStock,
		ExchangeSymbol: instrumentID, InstrumentID: instrumentID,
		BaseAsset: instrumentID, QuoteAsset: SettlementAsset, SettlementAsset: SettlementAsset,
		ExchangeQuantityStep: size, MinExchangeQuantity: size,
		PriceTick: shared.MustDecimal(exchange.HKMinPriceTick), MinNotional: shared.Zero(),
		MaxBuyPriceRatio: shared.Zero(), MinSellPriceRatio: shared.Zero(),
		Status: "TRADING", ExchangeUpdatedAt: at,
	}
}
