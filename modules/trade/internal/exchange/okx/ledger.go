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
)

type billPayload struct {
	BillID  string `json:"billId"`
	Type    string `json:"type"`
	SubType string `json:"subType"`
	Ccy     string `json:"ccy"`
	BalChg  string `json:"balChg"`
	InstID  string `json:"instId"`
	TradeID string `json:"tradeId"`
	Notes   string `json:"notes"`
	Ts      string `json:"ts"`
}

// LedgerSupported：OKX 统一账户的现货和永续都有账单接口。
func (a *Adapter) LedgerSupported() bool { return true }

// ListLedger 返回 [Start, End] 内最新的至多 Limit 条账单，按时间倒序。账单接口本身按时间倒序，
// 用 after=<上一页最后一条 billId> 往更早翻页。
func (a *Adapter) ListLedger(ctx context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	if query.Limit <= 0 || query.End.Before(query.Start) {
		return exchange.LedgerPage{}, rejected("invalid ledger query", nil)
	}
	path := "/api/v5/account/bills"
	if time.Since(query.Start) > billsRecentWindow {
		path = "/api/v5/account/bills-archive"
	}
	var entries []exchange.LedgerEntry
	after := ""
	for requests := 0; len(entries) < query.Limit; requests++ {
		if requests >= billsMaxRequests {
			return exchange.LedgerPage{Entries: entries, HasMore: true}, nil
		}
		values := url.Values{
			"instType": []string{a.instrumentType()},
			"begin":    []string{strconv.FormatInt(query.Start.UnixMilli(), 10)},
			"end":      []string{strconv.FormatInt(query.End.UnixMilli(), 10)},
			"limit":    []string{strconv.Itoa(billsPageLimit)},
		}
		if after != "" {
			values.Set("after", after)
		}
		data, err := a.request(ctx, http.MethodGet, path, values, nil, true)
		if err != nil {
			return exchange.LedgerPage{}, err
		}
		var rows []billPayload
		if err := json.Unmarshal(data, &rows); err != nil {
			return exchange.LedgerPage{}, rejected("decode bills", err)
		}
		for _, row := range rows {
			entry, err := billEntry(row)
			if err != nil {
				return exchange.LedgerPage{}, err
			}
			entries = append(entries, entry)
		}
		if len(rows) < billsPageLimit || rows[len(rows)-1].BillID == "" || rows[len(rows)-1].BillID == after {
			exchange.SortLedgerNewestFirst(entries)
			return exchange.LedgerPage{Entries: trimLedger(entries, query.Limit), HasMore: len(entries) > query.Limit}, nil
		}
		after = rows[len(rows)-1].BillID
	}
	exchange.SortLedgerNewestFirst(entries)
	return exchange.LedgerPage{Entries: trimLedger(entries, query.Limit), HasMore: true}, nil
}

func billEntry(row billPayload) (exchange.LedgerEntry, error) {
	amount, err := decimalOrZero(row.BalChg)
	if err != nil {
		return exchange.LedgerEntry{}, err
	}
	at := millisString(row.Ts)
	if row.BillID == "" || at.IsZero() {
		return exchange.LedgerEntry{}, rejected("bill lacks billId or ts", nil)
	}
	return exchange.LedgerEntry{
		ID: row.BillID, Time: at, Type: billLedgerType(row.Type), ExchangeType: row.Type + "/" + row.SubType,
		Asset: row.Ccy, Amount: amount, ExchangeSymbol: row.InstID, TradeID: row.TradeID, Info: row.Notes,
	}, nil
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
