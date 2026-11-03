package okx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

const (
	// billsPageLimit 是 bills、bills-archive 每页的上限。
	billsPageLimit = 100
	// billsMaxRequests 限制一次查询最多翻几页。
	billsMaxRequests = 10
	// billsRecentWindow 内用 bills，更早的用 bills-archive（近 3 个月）。
	billsRecentWindow = 7 * 24 * time.Hour
	// 翻页间隔：bills 限 5 次/秒，bills-archive 限 5 次/2 秒（按用户计）。
	billsPageInterval        = 220 * time.Millisecond
	billsArchivePageInterval = 420 * time.Millisecond
	// billsQueryBudget 是一次查询的总时长上限，要小于网关给 ListAccountLedger 的 15 秒。
	billsQueryBudget = 10 * time.Second
)

type billPayload struct {
	BillID   string `json:"billId"`
	InstType string `json:"instType"`
	Fee      string `json:"fee"`
	Pnl      string `json:"pnl"`
	Type     string `json:"type"`
	SubType  string `json:"subType"`
	Ccy      string `json:"ccy"`
	BalChg   string `json:"balChg"`
	InstID   string `json:"instId"`
	TradeID  string `json:"tradeId"`
	Notes    string `json:"notes"`
	Ts       string `json:"ts"`
}

// LedgerSupported：OKX 统一账户的现货和永续都有账单接口。
func (a *Adapter) LedgerSupported() bool { return true }

// ListLedger 返回 [Start, End] 内最新的至多 Limit 条账单，按时间倒序。账单接口本身按时间倒序，
// 用 after=<上一页最后一条 billId> 往更早翻页。
func (a *Adapter) ListLedger(ctx context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	if query.Limit <= 0 || query.End.Before(query.Start) {
		return exchange.LedgerPage{}, rejected("invalid ledger query", nil)
	}
	path, interval := "/api/v5/account/bills", billsPageInterval
	// 留一点余量：默认“近 7 天”在请求发出时已经超过 7 天整几毫秒。
	if time.Since(query.Start) > billsRecentWindow+time.Minute {
		path, interval = "/api/v5/account/bills-archive", billsArchivePageInterval
	}
	started := time.Now()
	var entries []exchange.LedgerEntry
	after := ""
	// 不按 instType 过滤：划转、利息等账单没有 instType，过滤后会查不到。
	for requests := 0; len(entries) < query.Limit; requests++ {
		if requests >= billsMaxRequests || (requests > 0 && time.Since(started)+interval > billsQueryBudget) {
			return partialLedger(entries, query.Limit), nil
		}
		if requests > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return exchange.LedgerPage{}, ctx.Err()
			case <-timer.C:
			}
		}
		values := url.Values{
			"begin": []string{strconv.FormatInt(query.Start.UnixMilli(), 10)},
			"end":   []string{strconv.FormatInt(query.End.UnixMilli(), 10)},
			"limit": []string{strconv.Itoa(billsPageLimit)},
		}
		if after != "" {
			values.Set("after", after)
		}
		data, err := a.request(ctx, http.MethodGet, path, values, nil, true)
		if err != nil {
			// 翻到一半被限频：已取到的照样返回，标记还有更早的。
			if requests > 0 && exchange.NotAccepted(err) {
				return partialLedger(entries, query.Limit), nil
			}
			return exchange.LedgerPage{}, err
		}
		var rows []billPayload
		if err := json.Unmarshal(data, &rows); err != nil {
			return exchange.LedgerPage{}, rejected("decode bills", err)
		}
		for _, row := range rows {
			rowEntries, err := billEntries(row)
			if err != nil {
				return exchange.LedgerPage{}, err
			}
			entries = append(entries, rowEntries...)
		}
		if len(rows) < billsPageLimit || rows[len(rows)-1].BillID == "" || rows[len(rows)-1].BillID == after {
			exchange.SortLedgerNewestFirst(entries)
			return exchange.LedgerPage{Entries: trimLedger(entries, query.Limit), HasMore: len(entries) > query.Limit}, nil
		}
		after = rows[len(rows)-1].BillID
	}
	return partialLedger(entries, query.Limit), nil
}

func partialLedger(entries []exchange.LedgerEntry, limit int) exchange.LedgerPage {
	exchange.SortLedgerNewestFirst(entries)
	return exchange.LedgerPage{Entries: trimLedger(entries, limit), HasMore: true}
}

// billEntries 把一条账单转成流水。永续、交割的交易账单（type 2）里 balChg 是已实现盈亏和手续费的合计，
// 拆成手续费和已实现盈亏两条，与币安的口径一致；其他账单记一条余额变动。
func billEntries(row billPayload) ([]exchange.LedgerEntry, error) {
	at := millisString(row.Ts)
	if row.BillID == "" || at.IsZero() {
		return nil, rejected("bill lacks billId or ts", nil)
	}
	base := exchange.LedgerEntry{
		ID: row.BillID, Time: at, Type: billLedgerType(row.Type), ExchangeType: row.Type + "/" + row.SubType,
		Asset: row.Ccy, ExchangeSymbol: row.InstID, TradeID: row.TradeID, Info: row.Notes,
	}
	if row.Type == "2" && (row.InstType == "SWAP" || row.InstType == "FUTURES") {
		var out []exchange.LedgerEntry
		for _, part := range []struct {
			suffix string
			kind   exchange.LedgerType
			raw    string
		}{{":pnl", exchange.LedgerRealizedPnL, row.Pnl}, {":fee", exchange.LedgerFee, row.Fee}} {
			amount, err := decimalOrZero(part.raw)
			if err != nil {
				return nil, err
			}
			if amount.IsZero() {
				continue
			}
			entry := base
			entry.ID, entry.Type, entry.Amount = row.BillID+part.suffix, part.kind, amount
			out = append(out, entry)
		}
		return out, nil
	}
	amount, err := decimalOrZero(row.BalChg)
	if err != nil {
		return nil, err
	}
	base.Amount = amount
	return []exchange.LedgerEntry{base}, nil
}

// billLedgerType 按 OKX 账单类型映射：1 划转、2 交易、5 强平、7 利息、8 资金费、9 ADL。
func billLedgerType(value string) exchange.LedgerType {
	switch value {
	case "1":
		return exchange.LedgerTransfer
	case "2":
		return exchange.LedgerTrade
	case "5", "9":
		return exchange.LedgerLiquidation
	case "7":
		return exchange.LedgerInterest
	case "8":
		return exchange.LedgerFundingFee
	default:
		return exchange.LedgerOther
	}
}

func trimLedger(entries []exchange.LedgerEntry, limit int) []exchange.LedgerEntry {
	if len(entries) > limit {
		return entries[:limit]
	}
	return entries
}
