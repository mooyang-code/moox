package service

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

// syncAccounts 返回本次同步要扫描的账户。
func (s *Service) syncAccounts(ctx context.Context, opts SyncOptions) ([]*Account, error) {
	if opts.SpaceID == "" {
		return nil, ErrInvalidParam
	}
	if opts.AccountID != "" {
		account, err := s.store.GetAccount(ctx, opts.SpaceID, opts.AccountID)
		if err != nil {
			return nil, err
		}
		return []*Account{account}, nil
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	var all []*Account
	for pageNo := 1; ; pageNo++ {
		items, total, err := s.store.ListAccounts(ctx, opts.SpaceID, AccountFilter{}, Page{PageNo: pageNo, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) == 0 || len(all) >= total {
			return all, nil
		}
	}
}

// SyncAllSnapshots 同步账户余额快照与合约持仓快照。
func (s *Service) SyncAllSnapshots(ctx context.Context, opts SyncOptions) (*SyncReport, error) {
	report := &SyncReport{SpaceID: opts.SpaceID}
	accounts, err := s.syncAccounts(ctx, opts)
	if err != nil {
		return report, err
	}
	for _, account := range accounts {
		report.AccountsScanned++
		if account.ChannelID == "" {
			report.SkippedAccounts++
			continue
		}
		if syncSectionEnabled(opts, SyncTypeBalances) {
			if _, err := s.Account.SyncBalances(ctx, opts.SpaceID, account.AccountID); err != nil {
				report.Errors = append(report.Errors, account.AccountID+": balances: "+err.Error())
			} else {
				report.BalancesSynced++
			}
		}
		if account.AccountType == AccountSwap && syncSectionEnabled(opts, SyncTypePositions) {
			positions, err := s.Order.SyncPositions(ctx, opts.SpaceID, account.AccountID, "")
			if err != nil {
				report.Errors = append(report.Errors, account.AccountID+": positions: "+err.Error())
			} else {
				report.PositionsSynced += len(positions)
			}
		}
	}
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("trade snapshot sync finished with %d errors", len(report.Errors))
	}
	return report, nil
}

func syncSectionEnabled(opts SyncOptions, section SyncType) bool {
	if len(opts.Sections) == 0 {
		return true
	}
	return opts.Sections[section]
}

func (s *Service) historySymbols(ctx context.Context, spaceID string, account *Account, maxSymbols int) []string {
	if account == nil || account.AccountID == "" {
		return nil
	}
	if maxSymbols <= 0 {
		maxSymbols = 10
	}
	base := strings.ToUpper(strings.TrimSpace(account.BaseCurrency))
	if base == "" {
		base = "USDT"
	}
	seen := map[string]bool{}
	add := func(symbol string) {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol != "" {
			seen[symbol] = true
		}
	}
	balances, _ := s.store.GetBalances(ctx, spaceID, account.AccountID, nil)
	for _, balance := range balances {
		ccy := strings.ToUpper(strings.TrimSpace(balance.Currency))
		if ccy == "" || ccy == base || !isPositiveServiceDecimal(balance.Total) {
			continue
		}
		add(ccy + base)
	}
	orders, _, _ := s.store.ListOrders(ctx, spaceID, OrderFilter{AccountID: account.AccountID}, Page{PageNo: 1, PageSize: maxSymbols})
	for _, order := range orders {
		add(order.Symbol)
	}
	trades, _, _ := s.store.ListTrades(ctx, spaceID, TradeFilter{AccountID: account.AccountID}, Page{PageNo: 1, PageSize: maxSymbols})
	for _, trade := range trades {
		add(trade.Symbol)
	}
	out := make([]string, 0, len(seen))
	for symbol := range seen {
		out = append(out, symbol)
	}
	sort.Strings(out)
	if len(out) > maxSymbols {
		out = out[:maxSymbols]
	}
	return out
}

func isPositiveServiceDecimal(value string) bool {
	parsed, _, err := big.ParseFloat(normSvcDec(strings.TrimSpace(value)), 10, svcDecimalPrec, big.ToNearestEven)
	return err == nil && parsed.Sign() > 0
}

func syncWindow(cursor *SyncCursor, opts SyncOptions) (startMS int64, endMS int64) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	windowHours := opts.WindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	endMS = now.UnixMilli()
	if cursor != nil && cursor.CursorEndMS > 0 {
		startMS = cursor.CursorEndMS + 1
	} else {
		startMS = now.Add(-time.Duration(windowHours) * time.Hour).UnixMilli()
	}
	if startMS > endMS {
		startMS = endMS
	}
	return startMS, endMS
}

// SyncTradingHistory 增量同步订单快照与成交明细。
func (s *Service) SyncTradingHistory(ctx context.Context, opts SyncOptions) (*SyncReport, error) {
	report := &SyncReport{SpaceID: opts.SpaceID}
	accounts, err := s.syncAccounts(ctx, opts)
	if err != nil {
		return report, err
	}
	for _, account := range accounts {
		report.AccountsScanned++
		if account.ChannelID == "" {
			report.SkippedAccounts++
			continue
		}
		symbols := s.historySymbols(ctx, opts.SpaceID, account, opts.MaxSymbolsPerRun)
		for _, symbol := range symbols {
			if syncSectionEnabled(opts, SyncTypeOrders) {
				orderCount, err := s.syncOrdersForSymbol(ctx, opts, account, symbol)
				if err != nil {
					report.Errors = append(report.Errors, account.AccountID+": orders "+symbol+": "+err.Error())
				} else {
					report.OrdersSynced += orderCount
				}
			}
			if syncSectionEnabled(opts, SyncTypeTrades) {
				tradeCount, err := s.syncTradesForSymbol(ctx, opts, account, symbol)
				if err != nil {
					report.Errors = append(report.Errors, account.AccountID+": trades "+symbol+": "+err.Error())
				} else {
					report.TradesSynced += tradeCount
				}
			}
		}
	}
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("trade history sync finished with %d errors", len(report.Errors))
	}
	return report, nil
}

func (s *Service) syncOrdersForSymbol(ctx context.Context, opts SyncOptions, account *Account, symbol string) (int, error) {
	cursor, err := s.store.GetSyncCursor(ctx, opts.SpaceID, account.AccountID, SyncTypeOrders, symbol)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	if errors.Is(err, ErrNotFound) {
		cursor = nil
	}
	startMS, endMS := syncWindow(cursor, opts)
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	orders, _, err := s.Order.SyncOrders(ctx, opts.SpaceID, account.AccountID, symbol, false, startMS, endMS, Page{PageNo: 1, PageSize: pageSize})
	if err != nil {
		_ = s.store.UpsertSyncCursor(ctx, opts.SpaceID, failedCursor(opts, account, SyncTypeOrders, symbol, startMS, cursorEnd(cursor), err))
		return 0, err
	}
	if err := s.store.UpsertSyncCursor(ctx, opts.SpaceID, successCursor(opts, account, SyncTypeOrders, symbol, startMS, endMS)); err != nil {
		return 0, err
	}
	return len(orders), nil
}

func (s *Service) syncTradesForSymbol(ctx context.Context, opts SyncOptions, account *Account, symbol string) (int, error) {
	cursor, err := s.store.GetSyncCursor(ctx, opts.SpaceID, account.AccountID, SyncTypeTrades, symbol)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	if errors.Is(err, ErrNotFound) {
		cursor = nil
	}
	startMS, endMS := syncWindow(cursor, opts)
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	trades, _, err := s.Order.SyncTrades(ctx, opts.SpaceID, account.AccountID, symbol, "", startMS, endMS, Page{PageNo: 1, PageSize: pageSize})
	if err != nil {
		_ = s.store.UpsertSyncCursor(ctx, opts.SpaceID, failedCursor(opts, account, SyncTypeTrades, symbol, startMS, cursorEnd(cursor), err))
		return 0, err
	}
	if err := s.store.UpsertSyncCursor(ctx, opts.SpaceID, successCursor(opts, account, SyncTypeTrades, symbol, startMS, endMS)); err != nil {
		return 0, err
	}
	return len(trades), nil
}

func successCursor(opts SyncOptions, account *Account, syncType SyncType, symbol string, startMS, endMS int64) *SyncCursor {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	return &SyncCursor{
		AccountID:     account.AccountID,
		ChannelID:     account.ChannelID,
		MarketType:    string(account.AccountType),
		SyncType:      syncType,
		Symbol:        symbol,
		CursorStartMS: startMS,
		CursorEndMS:   endMS,
		LastSuccessAt: now,
		LastError:     "",
		IsEnabled:     true,
	}
}

func failedCursor(opts SyncOptions, account *Account, syncType SyncType, symbol string, startMS, endMS int64, err error) *SyncCursor {
	return &SyncCursor{
		AccountID:     account.AccountID,
		ChannelID:     account.ChannelID,
		MarketType:    string(account.AccountType),
		SyncType:      syncType,
		Symbol:        symbol,
		CursorStartMS: startMS,
		CursorEndMS:   endMS,
		LastError:     err.Error(),
		IsEnabled:     true,
	}
}

func cursorEnd(cursor *SyncCursor) int64 {
	if cursor == nil {
		return 0
	}
	return cursor.CursorEndMS
}
