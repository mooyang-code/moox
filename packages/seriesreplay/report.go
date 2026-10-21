package seriesreplay

import (
	"context"
	"fmt"
	"strconv"
)

// Summary 是一次回放的结果摘要：权益曲线、回撤、费用与成交。
type Summary struct {
	ReplayID      string  `json:"replay_id"`
	Periods       int     `json:"periods"`
	InitialEquity float64 `json:"initial_equity"`
	FinalEquity   float64 `json:"final_equity"`
	TotalReturn   float64 `json:"total_return"`
	// MaxDrawdown 是权益曲线的最大回撤（正数，0.1 表示 10%）。
	MaxDrawdown    float64 `json:"max_drawdown"`
	Fees           float64 `json:"fees"`
	Turnover       float64 `json:"turnover"`
	Orders         int     `json:"orders"`
	RejectedOrders int     `json:"rejected_orders"`
	// SkippedPeriods 是策略跳过（SKIP）的周期数。
	SkippedPeriods int `json:"skipped_periods"`
}

// BuildSummary 从 Trade 的周期结果汇总指标；initialCash 取会话的初始资金。
func BuildSummary(replayID string, initialCash float64, periods []TradeResult) (Summary, error) {
	s := Summary{ReplayID: replayID, Periods: len(periods), InitialEquity: initialCash, FinalEquity: initialCash}
	peak := initialCash
	for _, p := range periods {
		equity, err := strconv.ParseFloat(p.Equity, 64)
		if err != nil {
			return Summary{}, fmt.Errorf("周期 %d 的权益 %q 无法解析", p.Period, p.Equity)
		}
		fees, _ := strconv.ParseFloat(p.Fees, 64)
		turnover, _ := strconv.ParseFloat(p.Turnover, 64)
		s.FinalEquity = equity
		s.Fees += fees
		s.Turnover += turnover
		s.Orders += len(p.Orders)
		for _, o := range p.Orders {
			if o.Status == "REJECTED" {
				s.RejectedOrders++
			}
		}
		if p.Kind == "SKIP" {
			s.SkippedPeriods++
		}
		if equity > peak {
			peak = equity
		}
		if peak > 0 {
			if dd := (peak - equity) / peak; dd > s.MaxDrawdown {
				s.MaxDrawdown = dd
			}
		}
	}
	if initialCash > 0 {
		s.TotalReturn = s.FinalEquity/initialCash - 1
	}
	return s, nil
}

// Report 读取 Trade 里这次回放的全部周期结果并汇总。
func Report(ctx context.Context, trade TradeAPI, replayID string) (Summary, error) {
	session, err := trade.GetSession(ctx, replayID)
	if err != nil {
		return Summary{}, err
	}
	initial, err := strconv.ParseFloat(session.InitialCash, 64)
	if err != nil {
		return Summary{}, fmt.Errorf("初始资金 %q 无法解析", session.InitialCash)
	}
	var all []TradeResult
	var after int64
	for {
		page, err := trade.Periods(ctx, replayID, after, 5000)
		if err != nil {
			return Summary{}, err
		}
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
		after = page[len(page)-1].Period
	}
	return BuildSummary(replayID, initial, all)
}
