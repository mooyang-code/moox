package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

const (
	defaultPnlWindow = 7 * 24 * time.Hour
	// fundingPageLimit 是流水每页条数；fundingMaxPages 限制翻页次数：流水里成交、手续费也占条数，
	// 窗口太长时只取到最近的一部分，如实标明截断。
	fundingPageLimit = 1000
	fundingMaxPages  = 5
)

// SummarizeAccountPnl 收益拆解：手续费和已实现盈亏从成交表汇总，资金费实盘即查交易所流水，模拟盘直接汇总本地资金费记录。
// 手续费按手续费资产分别列出，不折算成结算币；资金费翻页取不完或有账户查不到时标明截断。时间范围最长 90 天（请求校验里限制）。
func (s *ConsoleServer) SummarizeAccountPnl(ctx context.Context, req *tradepb.SummarizeAccountPnlReq) (*tradepb.SummarizeAccountPnlRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.SummarizeAccountPnlRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.SummarizeAccountPnlRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	var accounts []store.TradingAccountRecord
	query := store.FillSummaryQuery{LogicalAccountID: req.GetLogicalAccountId()}
	switch {
	case req.GetTradingAccountId() != "":
		account, err := s.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
		if err != nil {
			return &tradepb.SummarizeAccountPnlRsp{RetInfo: errorInfo(err)}, nil
		}
		accounts, query = []store.TradingAccountRecord{account}, store.FillSummaryQuery{TradingAccountID: account.TradingAccountID}
	default:
		if _, err := s.Store.GetLogicalAccount(ctx, spaceID, req.GetLogicalAccountId()); err != nil {
			return &tradepb.SummarizeAccountPnlRsp{RetInfo: errorInfo(err)}, nil
		}
		members, err := s.Store.ListLogicalAccountMembers(ctx, spaceID, req.GetLogicalAccountId(), true)
		if err != nil {
			return &tradepb.SummarizeAccountPnlRsp{RetInfo: errorInfo(err)}, nil
		}
		for _, member := range members {
			account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
			if err != nil {
				return &tradepb.SummarizeAccountPnlRsp{RetInfo: errorInfo(err)}, nil
			}
			accounts = append(accounts, account)
		}
	}
	end := time.Now().UTC()
	if req.GetEndTime() > 0 {
		end = time.UnixMilli(req.GetEndTime()).UTC()
	}
	start := end.Add(-defaultPnlWindow)
	if req.GetStartTime() > 0 {
		start = time.UnixMilli(req.GetStartTime()).UTC()
	}
	query.StartTime, query.EndTime = start.UnixMilli(), end.UnixMilli()
	summary, err := s.Store.SummarizeFills(ctx, spaceID, query)
	if err != nil {
		return &tradepb.SummarizeAccountPnlRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.SummarizeAccountPnlRsp{
		RetInfo: success(), FillCount: summary.FillCount,
		Fees: pnlAmountsToPB(summary.Fees), RealizedPnl: pnlAmountsToPB(summary.RealizedPnL),
		FundingFees: []*tradepb.PnlAmount{},
	}
	s.summarizeFunding(ctx, accounts, start, end, rsp)
	return rsp, nil
}

func pnlAmountsToPB(values []store.AssetAmount) []*tradepb.PnlAmount {
	result := make([]*tradepb.PnlAmount, 0, len(values))
	for _, value := range values {
		result = append(result, &tradepb.PnlAmount{Asset: value.Asset, Amount: value.Amount.String()})
	}
	return result
}

// summarizeFunding 汇总各个有永续市场的执行账户的资金费：实盘翻页取交易所流水；模拟盘直接按资产汇总本地资金费记录，
// 不经过流水翻页，记录再多也不截断。
func (s *ConsoleServer) summarizeFunding(ctx context.Context, accounts []store.TradingAccountRecord, start, end time.Time, rsp *tradepb.SummarizeAccountPnlRsp) {
	truncated := false
	// 先把各账户取到的流水都收齐，算出统计起点之后再合计：起点之前只有部分账户取到的流水不能计入，否则合计和起点对不上。
	var collected []exchange.LedgerEntry
	var notes []string
	queried := 0
	// reached 是各个没取完的账户已经取到的最老时间里最晚的一个：只有这个时间之后的资金费，所有账户都取全了。
	var reached time.Time
	// 所有账户的流水查询共用一个期限：要明显小于网关给这个接口的调用超时，到点后没查的账户记为截断。
	ctx, cancel := context.WithTimeout(ctx, ledgerQueryTimeout)
	defer cancel()
	var paperAccounts []store.TradingAccountRecord
	for _, account := range accounts {
		if !account.SupportsMarket(string(exchange.MarketTypeSwap)) {
			continue
		}
		if account.ExecutionMode == string(exchange.ExecutionModePaper) {
			// 第一版只有币安的模拟永续结算资金费。
			if account.Exchange != string(exchange.ExchangeBinance) {
				notes = append(notes, fmt.Sprintf("账户 %s：%s 模拟盘暂不结算资金费", account.Name, account.Exchange))
			}
			paperAccounts = append(paperAccounts, account)
			continue
		}
		source, err := s.ledgerSource(account.TradingAccountID)
		if err != nil {
			notes = append(notes, fmt.Sprintf("账户 %s：%v", account.Name, err))
			truncated = true
			continue
		}
		entries, complete, oldest, err := fundingEntries(ctx, source, start, end)
		if err != nil {
			notes = append(notes, fmt.Sprintf("账户 %s 的流水查询失败：%v", account.Name, err))
			truncated = true
			continue
		}
		queried++
		if !complete {
			truncated = true
			if oldest.After(reached) {
				reached = oldest
			}
		}
		collected = append(collected, entries...)
	}
	if queried == 0 && len(paperAccounts) == 0 {
		rsp.FundingAvailable = false
		if len(notes) == 0 {
			notes = append(notes, "没有开通永续市场的账户，没有资金费")
		}
		rsp.FundingNote = strings.Join(notes, "；")
		return
	}
	rsp.FundingAvailable = true
	from := start
	if reached.After(from) {
		from = reached
	}
	rsp.FundingFrom = from.UnixMilli()
	rsp.FundingTruncated = truncated
	totals := map[string]shared.Decimal{}
	var assets []string
	for _, entry := range collected {
		if entry.Time.Before(from) {
			continue
		}
		if _, seen := totals[entry.Asset]; !seen {
			assets = append(assets, entry.Asset)
		}
		totals[entry.Asset] = totals[entry.Asset].Add(entry.Amount)
	}
	// 模拟盘按同一个统计起点汇总，合计和起点对得上。
	for _, account := range paperAccounts {
		amounts, err := s.Store.SummarizePaperFundingFees(ctx, account.SpaceID, account.TradingAccountID, from.UnixMilli(), end.UnixMilli())
		if err != nil {
			notes = append(notes, fmt.Sprintf("账户 %s 的资金费记录读取失败：%v", account.Name, err))
			rsp.FundingTruncated = true
			continue
		}
		for _, amount := range amounts {
			if _, seen := totals[amount.Asset]; !seen {
				assets = append(assets, amount.Asset)
			}
			totals[amount.Asset] = totals[amount.Asset].Add(amount.Amount)
		}
	}
	sort.Strings(assets)
	for _, asset := range assets {
		rsp.FundingFees = append(rsp.FundingFees, &tradepb.PnlAmount{Asset: asset, Amount: totals[asset].String()})
	}
	if from.After(start) {
		notes = append(notes, "流水条数太多，只统计到最近的一部分")
	}
	rsp.FundingNote = strings.Join(notes, "；")
}

// fundingEntries 翻页取窗口内的资金费流水：每页是窗口内最新的一批，下一页的终点移到上一页最老一条的时间，按流水号去重。
// complete 为假表示翻页次数用完仍有更早的记录没取到；oldest 是已取到的最老一条的时间。
func fundingEntries(ctx context.Context, source interface {
	ListLedger(context.Context, exchange.LedgerQuery) (exchange.LedgerPage, error)
}, start, end time.Time) (entries []exchange.LedgerEntry, complete bool, oldest time.Time, err error) {
	seen := map[string]struct{}{}
	pageEnd := end
	oldest = end
	for page := 0; page < fundingMaxPages; page++ {
		result, err := source.ListLedger(ctx, exchange.LedgerQuery{Market: exchange.MarketTypeSwap, Start: start, End: pageEnd, Limit: fundingPageLimit})
		if err != nil {
			return nil, false, oldest, err
		}
		for _, entry := range result.Entries {
			if entry.Time.Before(oldest) {
				oldest = entry.Time
			}
			if entry.Type != exchange.LedgerFundingFee {
				continue
			}
			key := entry.ID
			if key == "" {
				key = entry.Time.String() + entry.Asset + entry.Amount.String() + entry.ExchangeSymbol
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			entries = append(entries, entry)
		}
		if !result.HasMore {
			return entries, true, oldest, nil
		}
		// 这一页的最老一条和终点同一时刻时，再翻也挪不动，按没取完处理。
		if !oldest.Before(pageEnd) {
			return entries, false, oldest, nil
		}
		pageEnd = oldest
	}
	return entries, false, oldest, nil
}

// ListOperatorActions 列出组合的人工操作记录（人工单、撤单、清仓、减仓、下架清仓），按创建时间倒序。
func (s *ConsoleServer) ListOperatorActions(ctx context.Context, req *tradepb.ListOperatorActionsReq) (*tradepb.ListOperatorActionsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListOperatorActionsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.ListOperatorActionsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	if _, err := s.Store.GetLogicalAccount(ctx, spaceID, req.GetLogicalAccountId()); err != nil {
		return &tradepb.ListOperatorActionsRsp{RetInfo: errorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := s.Store.ListOperatorActions(ctx, spaceID, store.OperatorActionQuery{
		LogicalAccountID: req.GetLogicalAccountId(), ActionType: req.GetActionType(),
		StartTime: req.GetStartTime(), EndTime: req.GetEndTime(), Offset: page.offset, Limit: page.size,
	})
	actions := make([]*tradepb.OperatorAction, 0, len(records))
	for _, record := range records {
		actions = append(actions, operatorActionToPB(record))
	}
	return &tradepb.ListOperatorActionsRsp{RetInfo: errorInfo(err), Actions: actions, PageResult: pageResult(page, total)}, nil
}
