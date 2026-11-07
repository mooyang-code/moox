package delist

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/packages/events"
)

// 下架处理的轮次：0 发现即禁止买入，1 下架前 24 小时平仓，2 下架前 2 小时复查平仓，3 下架后核对。
const (
	RoundBlock   = 0
	RoundFlatten = 1
	RoundReview  = 2
	RoundVerify  = 3
)

// target 是一条下架执行记录要处理的标的和时间，从事件内容里取出。
type target struct {
	Instrument action.InstrumentRef
	DelistAt   time.Time
	// PaperOnly 为真时只处理模拟盘组合（演练）。
	PaperOnly bool
}

// parseTarget 读出事件内容里的标的、下架时间和范围；内容不合法时返回错误（重试也没用）。
func parseTarget(execution store.Execution) (target, error) {
	var payload struct {
		Exchange       string `json:"exchange"`
		Market         string `json:"market"`
		ExchangeSymbol string `json:"exchange_symbol"`
		DelistAt       string `json:"delist_at"`
		Scope          string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(execution.PayloadJSON), &payload); err != nil {
		return target{}, fmt.Errorf("事件内容不是合法的 JSON: %w", err)
	}
	if payload.Exchange == "" || payload.ExchangeSymbol == "" || (payload.Market != events.DelistMarketSpot && payload.Market != events.DelistMarketSwap) {
		return target{}, fmt.Errorf("事件内容缺少交易所、市场或交易所代码")
	}
	delistAt, err := time.Parse(time.RFC3339Nano, payload.DelistAt)
	if err != nil {
		return target{}, fmt.Errorf("事件里的下架时间不合法: %w", err)
	}
	return target{
		Instrument: action.InstrumentRef{Exchange: payload.Exchange, Market: payload.Market, ExchangeSymbol: payload.ExchangeSymbol},
		DelistAt:   delistAt,
		PaperOnly:  payload.Scope == events.DelistScopePaperOnly,
	}, nil
}

// subject 是标的的唯一名字（交易所:市场:代码），与执行记录的 c_subject_id 一致，也是进程内锁的键。
func (t target) subject() string {
	return t.Instrument.Exchange + ":" + t.Instrument.Market + ":" + t.Instrument.ExchangeSymbol
}

func (t target) describe() string {
	return t.Instrument.Exchange + " " + t.Instrument.Market + " " + t.Instrument.ExchangeSymbol
}

// 下架时间与各轮的时间点。
const (
	flattenLead = 24 * time.Hour
	reviewLead  = 2 * time.Hour
	// verifyDelay 是下架时间之后再等一会儿才核对：交易所到点结算后，Trade 的持仓要等下一次同步才会更新。
	verifyDelay = 10 * time.Minute
)

func (t target) flattenAt() time.Time { return t.DelistAt.Add(-flattenLead) }
func (t target) reviewAt() time.Time  { return t.DelistAt.Add(-reviewLead) }
func (t target) verifyAt() time.Time  { return t.DelistAt.Add(verifyDelay) }

// blockReason 是写入下架名单时的说明，只用于审计。
func blockReason(execution store.Execution, t target) string {
	return fmt.Sprintf("下架：%s，下架时间 %s（事件中心执行记录 %s）", t.describe(), t.DelistAt.UTC().Format(time.RFC3339), execution.ExecutionID)
}

// flattenReason 是平仓的说明。它只由合并时不会变的字段（执行记录、轮次、标的）生成，建行时存下，之后一律读回使用：
// 同一个操作号对应的说明不能变，否则 Trade 会拒绝重试。
func flattenReason(execution store.Execution, t target, round int) string {
	return fmt.Sprintf("下架清仓：%s（事件中心执行记录 %s，第 %d 轮）", t.describe(), execution.ExecutionID, round)
}

// operatorNamespace 是 Trade 操作号的命名空间。
var operatorNamespace = uuid.MustParse("6f1c3a52-7d1e-4a55-9d0b-0c6d6f6b7c01")

// operatorActionID 由（执行记录、轮次、空间、组合账户）确定地生成：卡住后被放回的旧线程和新线程即使同时发起平仓，
// 用的也是同一个操作号，由 Trade 的幂等挡住重复。每一轮的号不同，所以复查（包括上一轮 PARTIAL 之后）总是换新号。
func operatorActionID(executionID string, round int, spaceID, logicalAccountID string) string {
	return uuid.NewSHA1(operatorNamespace, []byte(fmt.Sprintf("%s|%d|%s|%s", executionID, round, spaceID, logicalAccountID))).String()
}

// stepKey 是下架步骤的唯一键，与 t_delist_actions 的主键一致。
type stepKey struct {
	Round            int
	Kind             string
	SpaceID          string
	LogicalAccountID string
}

func keyOf(step store.DelistAction) stepKey {
	return stepKey{Round: step.Round, Kind: step.Kind, SpaceID: step.SpaceID, LogicalAccountID: step.LogicalAccountID}
}

func terminal(status string) bool {
	return status == store.DelistSucceeded || status == store.DelistFailed || status == store.DelistPartial
}

func joinLines(lines []string) string {
	return strings.Join(lines, "；")
}
