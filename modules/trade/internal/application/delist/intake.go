package delist

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"trpc.group/trpc-go/trpc-go/log"
)

// Event 是一条已解码、已校验的下架事件（Collector 发现或手工登记）。
type Event struct {
	EventID    string
	OccurredAt time.Time
	Key        Key
	// Scope 是 ALL 或 PAPER_ONLY（演练）。
	Scope    string
	DelistAt time.Time
	Source   string
}

// IntakeResult 是收到一条下架事件的结果。
type IntakeResult struct {
	// DelistID 是新建的下架记录；为空表示事件被忽略（重投、乱序的旧事件、下架时间没变）。
	DelistID string
	// Rescheduled 是因改期而结束的旧记录。
	Rescheduled string
	// BlockChanged 为真表示名单有变化（新写入、范围升级为 ALL）。
	BlockChanged bool
}

func blockReason(key Key, delistAt time.Time) string {
	return fmt.Sprintf("下架：%s %s %s，下架时间 %s", key.Exchange, key.MarketType, key.ExchangeSymbol, delistAt.UTC().Format(time.RFC3339))
}

// Intake 在一个事务里处理一条下架事件：挡住重投与乱序的旧事件，改期时结束旧记录，新建记录并写禁止名单。
// 提交之后名单有变化就唤醒目标、提醒特殊持仓；新建了记录时查一次持有者，有持有或没能判断时记 delist_detected 关注项。
func (s *Service) Intake(ctx context.Context, event Event) (IntakeResult, error) {
	if err := s.ready(); err != nil {
		return IntakeResult{}, err
	}
	key, err := normalize(event.Key)
	if err != nil {
		return IntakeResult{}, err
	}
	event.EventID = strings.TrimSpace(event.EventID)
	if event.EventID == "" || event.DelistAt.IsZero() || strings.TrimSpace(event.Source) == "" ||
		(event.Scope != store.DelistScopeAll && event.Scope != store.DelistScopePaperOnly) {
		return IntakeResult{}, fmt.Errorf("%w: 下架事件缺少事件 ID、下架时间、来源或范围", ErrInvalidRequest)
	}
	instrument, known, err := s.lookup(ctx, key)
	if err != nil {
		return IntakeResult{}, err
	}
	now := s.now().UnixMilli()
	delistAt, occurredAt := event.DelistAt.UnixMilli(), event.OccurredAt.UnixMilli()
	var result IntakeResult
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		result = IntakeResult{}
		if _, found, err := tx.FindDelistBySourceEvent(event.EventID); err != nil || found {
			return err // 重投
		}
		// 防乱序：和这个（标的、范围）最近的一条记录比，不论状态；只比进行中的记录的话，旧事件在记录结束后才到会重新建记录、写名单。
		latest, found, err := tx.LatestDelist(key.Exchange, key.MarketType, key.ExchangeSymbol, event.Scope)
		if err != nil {
			return err
		}
		if found && occurredAt < latest.EventOccurredAt {
			return nil
		}
		active, found, err := tx.FindActiveDelist(key.Exchange, key.MarketType, key.ExchangeSymbol, event.Scope)
		if err != nil {
			return err
		}
		if found && active.DelistAt == delistAt {
			return nil
		}
		delistID := uuid.NewString()
		if found {
			// 先结束旧记录再插新记录，否则撞“同一标的同一范围只能有一条进行中的记录”的部分唯一索引。
			text := fmt.Sprintf("改期为 %s，由记录 %s 接着处理", event.DelistAt.UTC().Format(time.RFC3339), delistID)
			if _, err := tx.FinishDelist(active.DelistID, store.DelistRescheduled, text, now); err != nil {
				return err
			}
			result.Rescheduled = active.DelistID
		}
		if err := tx.InsertDelist(store.DelistRecord{
			DelistID: delistID, Exchange: key.Exchange, MarketType: key.MarketType, ExchangeSymbol: key.ExchangeSymbol,
			Scope: event.Scope, DelistAt: delistAt, Source: event.Source, SourceEventID: event.EventID, EventOccurredAt: occurredAt,
			Status: store.DelistActive, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		result.DelistID = delistID
		changed, err := writeBlock(tx, key, event, delistID, result.Rescheduled, instrument, now)
		result.BlockChanged = changed
		return err
	})
	if err != nil {
		return IntakeResult{}, err
	}
	if result.BlockChanged {
		s.changed()
		if known {
			s.warnSpecialHolders(ctx, key, instrument)
		}
	}
	if result.DelistID != "" {
		s.noteDetected(ctx, key, event.Scope, event.DelistAt)
	}
	return result, nil
}

// writeBlock 按事件写名单：没有这一行就插入；已有 PAPER_ONLY 行而事件是 ALL 就升级；行指向刚改期结束的旧记录就改指新记录（范围不变）。
// changed 只在名单对交易的效果有变化（新写入、升级）时为真。
func writeBlock(tx *store.Tx, key Key, event Event, delistID, rescheduled string, instrument store.InstrumentRecord, now int64) (changed bool, err error) {
	row, found, err := tx.GetBlockedInstrument(key.Exchange, key.MarketType, key.ExchangeSymbol)
	if err != nil {
		return false, err
	}
	reason := blockReason(key, event.DelistAt)
	switch {
	case !found:
		return tx.BlockInstrument(store.BlockedInstrumentRecord{
			Exchange: key.Exchange, MarketType: key.MarketType, ExchangeSymbol: key.ExchangeSymbol, Scope: event.Scope,
			InstrumentID: instrument.InstrumentID, BaseAsset: instrument.BaseAsset,
			Reason: reason, DelistID: delistID, BlockedAt: now,
		})
	case row.Scope == store.DelistScopePaperOnly && event.Scope == store.DelistScopeAll:
		row.Scope, row.Reason, row.DelistID = store.DelistScopeAll, reason, delistID
		return true, tx.UpdateBlockedInstrument(row)
	case rescheduled != "" && row.DelistID == rescheduled:
		row.Reason, row.DelistID = reason, delistID
		return false, tx.UpdateBlockedInstrument(row)
	}
	return false, nil
}

// noteDetected 查一次持有者：有非零头持有者，或没查到持有者但有账户没能判断，就记 delist_detected 关注项（固定文字）。查询失败只打日志。
func (s *Service) noteDetected(ctx context.Context, key Key, scope string, delistAt time.Time) {
	result, err := s.scopedHolders(ctx, key, scope)
	if err != nil {
		log.WarnContextf(ctx, "下架 %s %s %s：查持有者失败，不产生发现通知：%v", key.Exchange, key.MarketType, key.ExchangeSymbol, err)
		return
	}
	prefix := fmt.Sprintf("%s %s %s 将于 %s 下架", key.Exchange, key.MarketType, key.ExchangeSymbol, delistAt.UTC().Format("2006-01-02 15:04 UTC"))
	switch {
	case len(liveHolders(result.Holders)) > 0:
		telemetry.Note(telemetry.AttentionDelistDetected, prefix+"，有组合持有", s.now())
	case len(result.SkippedAccounts) > 0:
		telemetry.Note(telemetry.AttentionDelistDetected, prefix+"，有账户没能判断是否持有，请人工确认", s.now())
	}
}

// scopedHolders 查持有者。范围是 PAPER_ONLY 时只查模拟盘，并自己再过滤一遍非模拟盘的持有者。
func (s *Service) scopedHolders(ctx context.Context, key Key, scope string) (HoldersResult, error) {
	paperOnly := scope == store.DelistScopePaperOnly
	query := s.Holders
	if s.holders != nil {
		query = s.holders
	}
	result, err := query(ctx, key, paperOnly)
	if err != nil || !paperOnly {
		return result, err
	}
	paper := result.Holders[:0:0]
	for _, holder := range result.Holders {
		if holder.Paper {
			paper = append(paper, holder)
		}
	}
	result.Holders = paper
	return result, nil
}

// liveHolders 去掉零头：零头下不了单，清仓也不会处理它。
func liveHolders(holders []Holder) []Holder {
	var live []Holder
	for _, holder := range holders {
		if !holder.Dust {
			live = append(live, holder)
		}
	}
	return live
}
