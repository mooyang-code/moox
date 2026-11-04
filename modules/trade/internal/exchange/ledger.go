package exchange

import (
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// LedgerType 是统一后的资金流水类型。
type LedgerType string

const (
	LedgerTrade       LedgerType = "TRADE"
	LedgerFee         LedgerType = "FEE"
	LedgerRealizedPnL LedgerType = "REALIZED_PNL"
	LedgerFundingFee  LedgerType = "FUNDING_FEE"
	LedgerTransfer    LedgerType = "TRANSFER"
	LedgerLiquidation LedgerType = "LIQUIDATION"
	LedgerInterest    LedgerType = "INTEREST"
	LedgerOther       LedgerType = "OTHER"
)

// LedgerEntry 是一条资金流水：某个资产余额的一次变动。只用于展示和对账，不参与交易决策。
type LedgerEntry struct {
	// ID 是交易所流水号，同一账户内唯一。
	ID   string
	Time time.Time
	Type LedgerType
	// ExchangeType 是交易所原始类型，便于排查。
	ExchangeType string
	Asset        string
	// Amount 是余额变动，正数为增加。
	Amount         shared.Decimal
	ExchangeSymbol string
	TradeID        string
	Info           string
}

// LedgerQuery 是资金流水查询：[Start, End] 时间窗内最新的至多 Limit 条。
type LedgerQuery struct {
	Start time.Time
	End   time.Time
	Limit int
}

// LedgerPage 是一页资金流水，按时间倒序；HasMore 表示窗口内还有更早的记录没取到。
type LedgerPage struct {
	Entries []LedgerEntry
	HasMore bool
}

// SortLedgerNewestFirst 按时间倒序排列，同一时间按流水号倒序，保证结果稳定。
func SortLedgerNewestFirst(entries []LedgerEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].Time.Equal(entries[j].Time) {
			return entries[i].Time.After(entries[j].Time)
		}
		return entries[i].ID > entries[j].ID
	})
}
