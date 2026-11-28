package delist

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// 时间线：发现即禁止买入；T−24h 规划第 1 轮清仓；T−2h 规划第 2 轮复查清仓；T+10 分钟核对。
const (
	flattenLead = 24 * time.Hour
	reviewLead  = 2 * time.Hour
	// verifyDelay 是下架时间之后再等一会儿才核对：交易所到点结算后，Trade 的持仓要等下一次同步才会更新。
	verifyDelay = 10 * time.Minute
	// failedRetryAfter 是 FAILED 的清仓用同一个操作号重发的间隔（按动作的最后修改时间算）。
	failedRetryAfter = 10 * time.Minute
	// launchStuckAfter 是当前轮的行建好后动作仍不存在（一直忙或解析不到标的）多久开始提醒。
	launchStuckAfter = 30 * time.Minute
	// failedAttentionWindow 是核对失败的记录结束后持续上报的时长。
	failedAttentionWindow = 24 * time.Hour
)

// ActionID 由（记录、轮次、空间、组合账户）确定地生成操作号：重启后重新发起用的是同一个号，由动作表的主键和同号幂等挡住重复。
// 每一轮、每条记录的号都不同，改期新建的记录不会撞上旧记录的号。
func ActionID(delistID string, round int, spaceID, logicalAccountID string) string {
	return store.DelistFlattenActionID(delistID, round, spaceID, logicalAccountID)
}

// dueRound 是 now 时应到的轮次：0（T−24h 之前）、1（[T−24h, T−2h)）、2（[T−2h, T)）。
func dueRound(now, delistAt time.Time) int {
	switch {
	case now.Before(delistAt.Add(-flattenLead)):
		return 0
	case now.Before(delistAt.Add(-reviewLead)):
		return 1
	default:
		return 2
	}
}

func recordKey(record store.DelistRecord) Key {
	return Key{Exchange: record.Exchange, MarketType: record.MarketType, ExchangeSymbol: record.ExchangeSymbol}
}

func describeRecord(record store.DelistRecord) string {
	return record.Exchange + " " + record.MarketType + " " + record.ExchangeSymbol
}

// flattenReason 只由不变的字段生成：同一个操作号对应的说明不能变，否则重发会被拒绝。
func flattenReason(record store.DelistRecord, round int) string {
	return fmt.Sprintf("下架清仓：%s（下架记录 %s，第 %d 轮）", describeRecord(record), record.DelistID, round)
}

// RunAll 推进全部进行中的下架记录（下架定时任务每 30 秒调用一次）。每条都按库里的记录、步骤行、动作状态和当前时间重新推导该做什么，
// 进程里不存状态，所以重启、暂停之后下一次推进就接着做。一条出错不挡其他的。
func (s *Service) RunAll(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	records, err := s.Store.ListActiveDelists(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, record := range records {
		if err := s.advance(ctx, record); err != nil {
			failures = append(failures, fmt.Errorf("下架记录 %s（%s）：%w", record.DelistID, describeRecord(record), err))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) advance(ctx context.Context, record store.DelistRecord) error {
	now := s.now()
	delistAt := time.UnixMilli(record.DelistAt)
	if !now.Before(delistAt.Add(verifyDelay)) {
		return s.verify(ctx, record, now)
	}
	if !now.Before(delistAt) {
		return nil // 已在跑的清仓按自己的截止时间停下，等核对
	}
	// 只规划应到的最新一轮，错过的轮次不补。
	if due := dueRound(now, delistAt); due > record.PlannedRound {
		planned, err := s.plan(ctx, record, due, now)
		if err != nil || !planned {
			return err
		}
		record.PlannedRound = due
	}
	if record.PlannedRound == 0 {
		return nil
	}
	return s.drive(ctx, record)
}

// plan 规划第 round 轮：按（空间、组合账户）聚合非零头持有者，在一个事务里推进已规划轮次并为每个组合建一行。
// 没有持有者也推进轮次，页面能看到“这一轮已做过、查到 0 个持有者”。记录已结束时 planned=false。
func (s *Service) plan(ctx context.Context, record store.DelistRecord, round int, now time.Time) (planned bool, err error) {
	result, err := s.scopedHolders(ctx, recordKey(record), record.Scope)
	if err != nil {
		_ = s.Store.UpdateDelistNote(ctx, record.DelistID, "第 "+fmt.Sprint(round)+" 轮规划时查持有者失败，稍后重试："+err.Error(), now.UnixMilli())
		return false, err
	}
	groups := groupHolders(liveHolders(result.Holders))
	rows := make([]store.DelistFlattenRecord, 0, len(groups))
	for _, group := range groups {
		rows = append(rows, store.DelistFlattenRecord{
			SpaceID: group.SpaceID, LogicalAccountID: group.LogicalAccountID,
		})
	}
	note := fmt.Sprintf("第 %d 轮查到 %d 个持有的组合", round, len(groups))
	if extra := describeHolders(result); extra != "" {
		note += "；" + extra
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		var planErr error
		planned, planErr = tx.PlanDelistRound(record.DelistID, round, note, now.UnixMilli(), rows)
		return planErr
	})
	return planned, err
}

// drive 推进当前轮的各行：动作不存在就发起；FAILED 满 failedRetryAfter 用同一个号重发；其余状态不动。
// 更早一轮还没发起的行不再发起，它们被新一轮取代。
func (s *Service) drive(ctx context.Context, record store.DelistRecord) error {
	rows, err := s.Store.ListDelistFlattens(ctx, record.DelistID)
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if row.Round != record.PlannedRound {
			continue
		}
		action, found, err := s.Store.FindOperatorAction(ctx, row.SpaceID, row.ActionID())
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if found && (operatordomain.Status(action.Status) != operatordomain.StatusFailed ||
			s.now().UTC().Sub(action.UpdatedAt.UTC()) < failedRetryAfter) {
			continue // RUNNING 由动作工人恢复；COMPLETED、PARTIAL 这一轮结束
		}
		if err := s.launch(ctx, record, row); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// launch 发起（或用同一个号重发）一行的清仓。发起前确认还没到下架时间、记录仍在进行中。
// 组合正忙、组合里没有这个交易所的成员或解析不到标的时不建动作，下一次推进再试，不打日志（满 30 分钟由关注项提醒）。
func (s *Service) launch(ctx context.Context, record store.DelistRecord, row store.DelistFlattenRecord) error {
	if s.Flatten == nil || !s.now().Before(time.UnixMilli(record.DelistAt)) {
		return nil
	}
	fresh, err := s.Store.GetDelist(ctx, record.DelistID)
	if err != nil {
		return err
	}
	if fresh.Status != store.DelistActive {
		return nil
	}
	instrumentID, err := s.ResolveInstrumentID(ctx, row.SpaceID, row.LogicalAccountID, recordKey(record))
	if err != nil {
		if errors.Is(err, ErrNoMemberOfExchange) || errors.Is(err, ErrInvalidRequest) {
			return nil
		}
		return err
	}
	err = s.Flatten(ctx, FlattenRequest{
		SpaceID: row.SpaceID, ActionID: row.ActionID(), LogicalAccountID: row.LogicalAccountID,
		Exchange: record.Exchange, InstrumentID: instrumentID, Reason: flattenReason(record, row.Round), DeadlineAt: record.DelistAt,
	})
	if errors.Is(err, ErrFlattenBusy) {
		return nil
	}
	if err != nil {
		// 动作已建好时它的状态会在下一次推进读到；这里只留一行日志。
		log.WarnContextf(ctx, "下架清仓 %s 第 %d 轮组合 %s 发起失败：%v", describeRecord(record), row.Round, row.LogicalAccountID, err)
	}
	return nil
}

// verify 在 T+10 分钟后核对：仍有非零头持有者或有没能判断的账户记 FAILED，否则 COMPLETED。
// 演练（PAPER_ONLY）结束时，名单里这一行的范围仍是 PAPER_ONLY 就在同一事务里删掉；已被真实下架升级为 ALL 的不删。
// 查持有者失败这次不动，原因写进说明，下一次推进重试。
func (s *Service) verify(ctx context.Context, record store.DelistRecord, now time.Time) error {
	result, err := s.scopedHolders(ctx, recordKey(record), record.Scope)
	if err != nil {
		_ = s.Store.UpdateDelistNote(ctx, record.DelistID, "核对时查持有者失败，稍后重试："+err.Error(), now.UnixMilli())
		return err
	}
	live := liveHolders(result.Holders)
	status, text := store.DelistCompleted, "核对通过：下架后没有策略持有"
	if len(live) > 0 || len(result.SkippedAccounts) > 0 {
		status = store.DelistFailed
		text = fmt.Sprintf("核对失败：下架后仍有 %d 个组合持有", len(groupHolders(live)))
		if len(live) == 0 {
			text = "核对失败：有账户没能判断是否持有，请人工确认"
		}
	}
	if extra := describeHolders(result); extra != "" {
		text += "；" + extra
	}
	var unblocked bool
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		finished, err := tx.FinishDelist(record.DelistID, status, text, now.UnixMilli())
		if err != nil || !finished || record.Scope != store.DelistScopePaperOnly {
			return err
		}
		unblocked, err = tx.DeletePaperOnlyBlockedInstrument(record.Exchange, record.MarketType, record.ExchangeSymbol)
		return err
	})
	if err == nil && unblocked {
		s.changed()
	}
	return err
}

// account 是一个（空间、组合账户）。
type account struct {
	SpaceID          string
	LogicalAccountID string
}

// groupHolders 把每个执行账户一行的持有者按（空间、组合账户）聚合并排序。
func groupHolders(holders []Holder) []account {
	seen := map[account]bool{}
	var accounts []account
	for _, holder := range holders {
		key := account{SpaceID: holder.SpaceID, LogicalAccountID: holder.LogicalAccountID}
		if !seen[key] {
			seen[key] = true
			accounts = append(accounts, key)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].SpaceID != accounts[j].SpaceID {
			return accounts[i].SpaceID < accounts[j].SpaceID
		}
		return accounts[i].LogicalAccountID < accounts[j].LogicalAccountID
	})
	return accounts
}

// describeHolders 写出没能判断的账户、零头和不算持有的说明。
func describeHolders(result HoldersResult) string {
	var parts []string
	if result.InstrumentUnknown {
		parts = append(parts, "Trade 的标的表里没有这个标的")
	}
	if len(result.SkippedAccounts) > 0 {
		items := make([]string, 0, len(result.SkippedAccounts))
		for _, item := range result.SkippedAccounts {
			items = append(items, fmt.Sprintf("%s（%s）", item.TradingAccountID, item.Reason))
		}
		parts = append(parts, "没能判断的账户："+strings.Join(items, "、"))
	}
	var dust []string
	for _, holder := range result.Holders {
		if holder.Dust {
			dust = append(dust, fmt.Sprintf("%s/%s 数量 %s", holder.SpaceID, holder.LogicalAccountID, holder.Quantity))
		}
	}
	if len(dust) > 0 {
		parts = append(parts, "零头（不处理）："+strings.Join(dust, "、"))
	}
	parts = append(parts, result.Notes...)
	return strings.Join(parts, "；")
}

// Attention 推出 delist_failed 关注项（固定文字，不带会变的数字）：
//   - 进行中记录的当前轮有动作 FAILED 或 PARTIAL；
//   - 当前轮有行建好超过 30 分钟、动作还不存在（一直忙或解析不到标的）；
//   - 核对失败的记录在结束后 24 小时内。
func (s *Service) Attention(ctx context.Context, now time.Time) ([]string, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	readStore := s.Store.ReadOnly()
	active, err := readStore.ListActiveDelists(ctx)
	if err != nil {
		return nil, err
	}
	var items []string
	for _, record := range active {
		if record.PlannedRound == 0 {
			continue
		}
		rows, err := readStore.ListDelistFlattens(ctx, record.DelistID)
		if err != nil {
			return items, err
		}
		for _, row := range rows {
			if row.Round != record.PlannedRound {
				continue
			}
			action, found, err := readStore.FindOperatorAction(ctx, row.SpaceID, row.ActionID())
			if err != nil {
				return items, err
			}
			prefix := fmt.Sprintf("%s 第 %d 轮组合 %s", describeRecord(record), row.Round, s.logicalName(ctx, row.SpaceID, row.LogicalAccountID))
			switch {
			case !found && now.Sub(time.UnixMilli(row.CreatedAt)) >= launchStuckAfter:
				items = append(items, prefix+" 30 分钟还没能发起清仓")
			case found && operatordomain.Status(action.Status) == operatordomain.StatusFailed:
				items = append(items, prefix+" 清仓失败")
			case found && operatordomain.Status(action.Status) == operatordomain.StatusPartial:
				items = append(items, prefix+" 清仓只平了一部分")
			}
		}
	}
	failed, err := readStore.ListFailedDelistsSince(ctx, now.Add(-failedAttentionWindow).UnixMilli())
	if err != nil {
		return items, err
	}
	for _, record := range failed {
		items = append(items, describeRecord(record)+" 下架后核对仍有持有")
	}
	return items, nil
}

func (s *Service) logicalName(ctx context.Context, spaceID, logicalAccountID string) string {
	logical, err := s.Store.ReadOnly().GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil || logical.Name == "" {
		return logicalAccountID
	}
	return logical.Name
}

// FlattenView 是一轮里一个组合的清仓及其动作状态。
type FlattenView struct {
	store.DelistFlattenRecord
	// ActionStatus 为空表示还没发起。
	ActionStatus string
	LastError    string
}

// DelistView 是一条下架记录及其各轮各组合的清仓。
type DelistView struct {
	store.DelistRecord
	Flattens []FlattenView
}

const (
	defaultListLimit = 100
	maxListLimit     = 500
)

// ListDelists 列出下架记录（按创建时间倒序），带各轮各组合的动作状态。经只读视图读。
func (s *Service) ListDelists(ctx context.Context, activeOnly bool, limit int) ([]DelistView, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	readStore := s.Store.ReadOnly()
	records, err := readStore.ListDelists(ctx, activeOnly, limit)
	if err != nil {
		return nil, err
	}
	views := make([]DelistView, 0, len(records))
	for _, record := range records {
		rows, err := readStore.ListDelistFlattens(ctx, record.DelistID)
		if err != nil {
			return nil, err
		}
		view := DelistView{DelistRecord: record, Flattens: make([]FlattenView, 0, len(rows))}
		for _, row := range rows {
			flatten := FlattenView{DelistFlattenRecord: row}
			action, found, err := readStore.FindOperatorAction(ctx, row.SpaceID, row.ActionID())
			if err != nil {
				return nil, err
			}
			if found {
				flatten.ActionStatus, flatten.LastError = action.Status, action.LastError
			}
			view.Flattens = append(view.Flattens, flatten)
		}
		views = append(views, view)
	}
	return views, nil
}
