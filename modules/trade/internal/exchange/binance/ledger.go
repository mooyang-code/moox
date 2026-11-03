package binance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

const (
	// incomePageLimit 是 /fapi/v1/income 单次最多返回的条数（权重 30）。
	incomePageLimit = 1000
	// incomeMaxRequests 限制一次查询最多发几次请求，避免为展示耗掉权重。
	incomeMaxRequests = 8
	// incomeInitialWindow 是从结束时间往前查的第一个窗口；满页就减半重查，稀疏就加倍。
	incomeInitialWindow = time.Hour
)

type incomePayload struct {
	Symbol     string      `json:"symbol"`
	IncomeType string      `json:"incomeType"`
	Income     string      `json:"income"`
	Asset      string      `json:"asset"`
	Info       string      `json:"info"`
	Time       int64       `json:"time"`
	TranID     json.Number `json:"tranId"`
	TradeID    string      `json:"tradeId"`
}

// LedgerSupported：U 本位永续有 /fapi/v1/income；现货没有统一的资金流水接口。
func (a *Adapter) LedgerSupported() bool {
	return a.config.MarketType == exchange.MarketTypeSwap
}

// ListLedger 返回 [Start, End] 内最新的至多 Limit 条资金流水，按时间倒序。
// income 接口在窗口内按时间升序返回最早的 1000 条，所以从结束时间往前分段查：
// 一段满页说明这段里还有没取到的，缩小窗口重查；不满页就整段收下再往前。
func (a *Adapter) ListLedger(ctx context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	if !a.LedgerSupported() {
		return exchange.LedgerPage{}, typedRejected("该账户不支持资金流水", nil)
	}
	if query.Limit <= 0 || query.End.Before(query.Start) {
		return exchange.LedgerPage{}, typedRejected("invalid ledger query", nil)
	}
	var entries []exchange.LedgerEntry
	end := query.End.UnixMilli()
	start := query.Start.UnixMilli()
	window := incomeInitialWindow.Milliseconds()
	for requests := 0; end >= start && len(entries) < query.Limit; requests++ {
		if requests >= incomeMaxRequests {
			return exchange.LedgerPage{Entries: newestLedger(entries, query.Limit), HasMore: true}, nil
		}
		from := max(start, end-window+1)
		rows, err := a.incomePage(ctx, from, end)
		if err != nil {
			return exchange.LedgerPage{}, err
		}
		if len(rows) >= incomePageLimit && window > 1 {
			// 这一段装不下，缩小窗口重查同一段的末尾。
			window = max(window/2, 1)
			continue
		}
		for _, row := range rows {
			entry, err := incomeEntry(row)
			if err != nil {
				return exchange.LedgerPage{}, err
			}
			entries = append(entries, entry)
		}
		end = from - 1
		if len(rows) < incomePageLimit/4 {
			window *= 2
		}
	}
	return exchange.LedgerPage{Entries: newestLedger(entries, query.Limit), HasMore: len(entries) > query.Limit || end >= start}, nil
}

func (a *Adapter) incomePage(ctx context.Context, from, to int64) ([]incomePayload, error) {
	raw, err := a.request(ctx, http.MethodGet, "/fapi/v1/income", url.Values{
		"startTime": []string{strconv.FormatInt(from, 10)},
		"endTime":   []string{strconv.FormatInt(to, 10)},
		"limit":     []string{strconv.Itoa(incomePageLimit)},
	})
	if err != nil {
		return nil, classifyAPIError(err, raw)
	}
	var rows []incomePayload
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, typedRejected("decode income", err)
	}
	return rows, nil
}

func incomeEntry(row incomePayload) (exchange.LedgerEntry, error) {
	amount, err := decimalOrZero(row.Income)
	if err != nil {
		return exchange.LedgerEntry{}, err
	}
	if row.Time <= 0 || row.TranID.String() == "" {
		return exchange.LedgerEntry{}, typedRejected("income row lacks time or tranId", errors.New(row.IncomeType))
	}
	return exchange.LedgerEntry{
		// tranId 在同一笔成交的手续费和已实现盈亏之间可能相同，带上类型保证唯一。
		ID:   row.TranID.String() + ":" + row.IncomeType,
		Time: millis(row.Time), Type: incomeLedgerType(row.IncomeType), ExchangeType: row.IncomeType,
		Asset: row.Asset, Amount: amount, ExchangeSymbol: row.Symbol, TradeID: row.TradeID, Info: row.Info,
	}, nil
}

func incomeLedgerType(value string) exchange.LedgerType {
	switch value {
	case "REALIZED_PNL":
		return exchange.LedgerRealizedPnL
	case "FUNDING_FEE":
		return exchange.LedgerFundingFee
	case "COMMISSION":
		return exchange.LedgerFee
	case "TRANSFER", "INTERNAL_TRANSFER":
		return exchange.LedgerTransfer
	case "INSURANCE_CLEAR":
		return exchange.LedgerLiquidation
	default:
		return exchange.LedgerOther
	}
}

func newestLedger(entries []exchange.LedgerEntry, limit int) []exchange.LedgerEntry {
	exchange.SortLedgerNewestFirst(entries)
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}
