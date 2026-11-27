package store

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// LogicalAccountSummary 是组合账户列表直接携带的聚合摘要：持仓标的数与近 24 小时净值变化。
type LogicalAccountSummary struct {
	PositionCount      int32
	EquityChange24h    *string
	EquityChangePct24h *string
}

// BatchLogicalAccountSummaries 批量计算一组组合账户的持仓标的数（去重后的基础资产/股票代码数，
// 现货/股票余额与永续持仓口径一致，不发外部交易所请求）以及 [now-24h, now] 窗口内的净值绝对变化与涨跌幅。
func (s *Store) BatchLogicalAccountSummaries(
	ctx context.Context,
	spaceID string,
	logicalIDs []string,
	now time.Time,
) (map[string]LogicalAccountSummary, error) {
	result := make(map[string]LogicalAccountSummary, len(logicalIDs))
	if len(logicalIDs) == 0 {
		return result, nil
	}
	for _, id := range logicalIDs {
		result[id] = LogicalAccountSummary{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	counts, err := s.batchLogicalPositionCounts(ctx, spaceID, logicalIDs)
	if err != nil {
		return nil, err
	}
	for id, count := range counts {
		summary := result[id]
		summary.PositionCount = count
		result[id] = summary
	}

	changes, err := s.batchLogicalEquityChange24h(ctx, spaceID, logicalIDs, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	for id, change := range changes {
		summary := result[id]
		summary.EquityChange24h = change.change
		summary.EquityChangePct24h = change.pct
		result[id] = summary
	}
	return result, nil
}

func (s *Store) batchLogicalPositionCounts(
	ctx context.Context,
	spaceID string,
	logicalIDs []string,
) (map[string]int32, error) {
	var members []struct {
		LogicalAccountID string `gorm:"column:c_logical_account_id"`
		TradingAccountID string `gorm:"column:c_trading_account_id"`
	}
	if err := s.db.WithContext(ctx).Table("t_logical_account_members").
		Select("c_logical_account_id, c_trading_account_id").
		Where("c_space_id = ? AND c_logical_account_id IN ? AND c_enabled = 1", spaceID, logicalIDs).
		Scan(&members).Error; err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return map[string]int32{}, nil
	}

	memberToLogical := make(map[string]string, len(members))
	accountIDs := make([]string, 0, len(members))
	for _, m := range members {
		memberToLogical[m.TradingAccountID] = m.LogicalAccountID
		accountIDs = append(accountIDs, m.TradingAccountID)
	}

	var accountRows []tradingAccountRow
	if err := s.db.WithContext(ctx).Table("t_trading_accounts").
		Where("c_space_id = ? AND c_trading_account_id IN ?", spaceID, accountIDs).
		Find(&accountRows).Error; err != nil {
		return nil, err
	}
	accounts := make([]TradingAccountRecord, 0, len(accountRows))
	swapAccountIDs := make([]string, 0, len(accountRows))
	balanceAccounts := make([]TradingAccountRecord, 0, len(accountRows))
	for _, row := range accountRows {
		account, err := decodeAccountRow(row)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
		if account.SupportsMarket(string(exchange.MarketTypeSwap)) {
			swapAccountIDs = append(swapAccountIDs, account.TradingAccountID)
		}
		if account.HasBalanceMarket() {
			balanceAccounts = append(balanceAccounts, account)
		}
	}

	coinsByLogical := make(map[string]map[string]struct{}, len(logicalIDs))
	addCoin := func(logicalID, instrumentID string) {
		coin := summaryCoinOf(instrumentID)
		if coin == "" {
			return
		}
		set := coinsByLogical[logicalID]
		if set == nil {
			set = make(map[string]struct{})
			coinsByLogical[logicalID] = set
		}
		set[coin] = struct{}{}
	}

	if len(swapAccountIDs) > 0 {
		var positions []struct {
			TradingAccountID string `gorm:"column:c_trading_account_id"`
			InstrumentID     string `gorm:"column:c_instrument_id"`
			SignedQuantity   string `gorm:"column:c_signed_quantity"`
		}
		if err := s.db.WithContext(ctx).Table("t_trading_positions").
			Select("c_trading_account_id, c_instrument_id, c_signed_quantity").
			Where("c_space_id = ? AND c_trading_account_id IN ? AND c_market_type = ?", spaceID, swapAccountIDs, string(exchange.MarketTypeSwap)).
			Scan(&positions).Error; err != nil {
			return nil, err
		}
		for _, pos := range positions {
			qty, err := shared.ParseDecimal(pos.SignedQuantity)
			if err != nil || qty.IsZero() {
				continue
			}
			if logicalID, ok := memberToLogical[pos.TradingAccountID]; ok {
				addCoin(logicalID, pos.InstrumentID)
			}
		}
	}

	if len(balanceAccounts) > 0 {
		// 按（交易所、环境）批量查一次余额市场标的，避免按账户 N+1 查询。
		exchangesSet := make(map[string]struct{})
		for _, account := range balanceAccounts {
			exchangesSet[account.Exchange] = struct{}{}
		}
		exchanges := make([]string, 0, len(exchangesSet))
		for ex := range exchangesSet {
			exchanges = append(exchanges, ex)
		}
		var instruments []struct {
			Exchange     string `gorm:"column:c_exchange"`
			Environment  string `gorm:"column:c_environment"`
			MarketType   string `gorm:"column:c_market_type"`
			InstrumentID string `gorm:"column:c_instrument_id"`
			BaseAsset    string `gorm:"column:c_base_asset"`
			QuoteAsset   string `gorm:"column:c_quote_asset"`
		}
		if err := s.db.WithContext(ctx).Table("t_trade_instruments").
			Select("c_exchange, c_environment, c_market_type, c_instrument_id, c_base_asset, c_quote_asset").
			Where("c_exchange IN ? AND c_market_type IN ?", exchanges, []string{string(exchange.MarketTypeSpot), string(exchange.MarketTypeEquity)}).
			Scan(&instruments).Error; err != nil {
			return nil, err
		}
		type scopeKey struct {
			exchange string
			env      string
			market   string
		}
		type lookupKey struct {
			exchange   string
			env        string
			market     string
			quoteAsset string
			baseAsset  string
		}
		hasScope := make(map[scopeKey]bool)
		index := make(map[lookupKey]string, len(instruments))
		for _, inst := range instruments {
			hasScope[scopeKey{exchange: inst.Exchange, env: inst.Environment, market: inst.MarketType}] = true
			if inst.InstrumentID == "" {
				continue
			}
			key := lookupKey{
				exchange:   inst.Exchange,
				env:        inst.Environment,
				market:     inst.MarketType,
				quoteAsset: strings.ToUpper(inst.QuoteAsset),
				baseAsset:  strings.ToUpper(inst.BaseAsset),
			}
			index[key] = inst.InstrumentID
		}

		for _, account := range balanceAccounts {
			logicalID := memberToLogical[account.TradingAccountID]
			env := account.Environment
			if account.ExecutionMode == string(exchange.ExecutionModePaper) || env == "" {
				env = string(exchange.AccountEnvironmentProduction)
			}
			settlementUpper := strings.ToUpper(account.SettlementAsset)
			for _, balance := range account.SpotBalances() {
				if strings.EqualFold(balance.Asset, account.SettlementAsset) {
					continue
				}
				qty, err := shared.ParseDecimal(balance.Total)
				if err != nil || qty.IsZero() {
					continue
				}
				assetUpper := strings.ToUpper(balance.Asset)
				for _, market := range account.Markets {
					if !exchange.RuleFor(market).PositionFromBalance {
						continue
					}
					lookupEnv := env
					// 与 ListInstrumentsForAccount 对齐：非生产环境在该（交易所、环境、市场）下无标的时回退查 PRODUCTION。
					if lookupEnv != string(exchange.AccountEnvironmentProduction) &&
						!hasScope[scopeKey{exchange: account.Exchange, env: lookupEnv, market: market}] {
						lookupEnv = string(exchange.AccountEnvironmentProduction)
					}
					if instID, ok := index[lookupKey{
						exchange:   account.Exchange,
						env:        lookupEnv,
						market:     market,
						quoteAsset: settlementUpper,
						baseAsset:  assetUpper,
					}]; ok {
						addCoin(logicalID, instID)
						break
					}
				}
			}
		}
	}

	counts := make(map[string]int32, len(coinsByLogical))
	for logicalID, coins := range coinsByLogical {
		counts[logicalID] = int32(len(coins))
	}
	return counts, nil
}

type equityChangeResult struct {
	change *string
	pct    *string
}

func (s *Store) batchLogicalEquityChange24h(
	ctx context.Context,
	spaceID string,
	logicalIDs []string,
	fromMS, toMS int64,
) (map[string]equityChangeResult, error) {
	var rows []struct {
		LogicalAccountID string `gorm:"column:c_logical_account_id"`
		Equity           string `gorm:"column:c_equity"`
		RNAsc            int    `gorm:"column:rn_asc"`
		RNDesc           int    `gorm:"column:rn_desc"`
	}
	if err := s.db.WithContext(ctx).Raw(sqlBatchLogicalEquityEndpoints, spaceID, logicalIDs, fromMS, toMS).Scan(&rows).Error; err != nil {
		return nil, err
	}
	type pair struct {
		first    shared.Decimal
		hasFirst bool
		last     shared.Decimal
		hasLast  bool
	}
	pairs := make(map[string]*pair, len(logicalIDs))
	for _, row := range rows {
		val, err := shared.ParseDecimal(row.Equity)
		if err != nil {
			continue
		}
		p := pairs[row.LogicalAccountID]
		if p == nil {
			p = &pair{}
			pairs[row.LogicalAccountID] = p
		}
		if row.RNAsc == 1 {
			p.first, p.hasFirst = val, true
		}
		if row.RNDesc == 1 {
			p.last, p.hasLast = val, true
		}
	}
	result := make(map[string]equityChangeResult, len(pairs))
	for logicalID, p := range pairs {
		if !p.hasFirst || !p.hasLast {
			continue
		}
		delta := p.last.Sub(p.first)
		changeStr := delta.String()
		item := equityChangeResult{change: &changeStr}
		if !p.first.IsZero() {
			pctStr := delta.Div(p.first).RoundDown(8).String()
			item.pct = &pctStr
		}
		result[logicalID] = item
	}
	return result, nil
}

// summaryCoinOf 与前端 coinOf 口径一致：去掉 -SPOT/-SWAP 后缀后取第一段；股票代码没有分隔符则原样返回。
func summaryCoinOf(instrumentID string) string {
	trimmed := strings.TrimSpace(instrumentID)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimSuffix(trimmed, "-SPOT")
	trimmed = strings.TrimSuffix(trimmed, "-SWAP")
	if idx := strings.IndexByte(trimmed, '-'); idx > 0 {
		return trimmed[:idx]
	}
	return trimmed
}

const sqlBatchLogicalEquityEndpoints = `
	WITH ranged AS (
		SELECT c_logical_account_id,
			c_equity,
			ROW_NUMBER() OVER (PARTITION BY c_logical_account_id ORDER BY c_bucket_time ASC) AS rn_asc,
			ROW_NUMBER() OVER (PARTITION BY c_logical_account_id ORDER BY c_bucket_time DESC) AS rn_desc,
			COUNT(*) OVER (PARTITION BY c_logical_account_id) AS cnt
		FROM t_logical_account_equity_points
		WHERE c_space_id = ? AND c_logical_account_id IN ? AND c_bucket_time >= ? AND c_bucket_time <= ?
	)
	SELECT c_logical_account_id, c_equity, rn_asc, rn_desc
	FROM ranged
	WHERE cnt >= 2 AND (rn_asc = 1 OR rn_desc = 1)
`
