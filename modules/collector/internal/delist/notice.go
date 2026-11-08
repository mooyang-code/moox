// Package delist 是 Collector 的下架检测：定时轮询币安现货下架计划、币安 U 本位合约信息和 OKX 永续合约信息，
// 发现“标的即将下架”就经发件箱发布 event.collector.instrument.delist_scheduled；也接受手工登记（补漏报和演练）。
// 检测只负责发现和发布事件，怎么处理下架（禁止买入、平仓）由事件中心决定。
package delist

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Notice 是一条下架通知。
type Notice struct {
	// Exchange 是小写交易所代码：binance、okx。
	Exchange string
	// Market 是 SPOT 或 SWAP。
	Market string
	// Symbol 是大写的交易所代码，例如 FOOUSDT、FOO-USDT-SWAP：下游（事件中心、Trade）按这个写法判断“同一个标的”。
	Symbol   string
	DelistAt time.Time
	// Source 取 events.DelistSource* 之一。
	Source string
	// Scope 取 events.DelistScopeAll 或 events.DelistScopePaperOnly；空表示 ALL。
	Scope string
}

// ErrInvalidNotice 表示通知没通过校验（不是写库出错）：这一条不会发布，来源返回了不该有的内容，要让人知道，不能只打一行日志就跳过。
var ErrInvalidNotice = errors.New("下架通知不合法")

// Validate 检查通知的各个字段。
func (n Notice) Validate() error {
	if n.Exchange != "binance" && n.Exchange != "okx" {
		return errors.New("交易所只能是小写的 binance 或 okx")
	}
	if n.Market != events.DelistMarketSpot && n.Market != events.DelistMarketSwap {
		return fmt.Errorf("市场只能是 %s 或 %s", events.DelistMarketSpot, events.DelistMarketSwap)
	}
	if strings.TrimSpace(n.Symbol) == "" || strings.TrimSpace(n.Symbol) != n.Symbol || strings.Contains(n.Symbol, ":") || n.Symbol != strings.ToUpper(n.Symbol) {
		return errors.New("交易所代码不能为空，必须是大写，不能带空格或冒号")
	}
	if n.DelistAt.IsZero() {
		return errors.New("下架时间不能为空")
	}
	switch n.Scope {
	case "", events.DelistScopeAll, events.DelistScopePaperOnly:
	default:
		return fmt.Errorf("范围只能是 %s 或 %s", events.DelistScopeAll, events.DelistScopePaperOnly)
	}
	return nil
}

// Registrar 把下架通知记进去重表，第一次出现时经发件箱发布事件。
type Registrar struct {
	Repo     *store.DelistNoticeRepository
	Registry *events.Registry
	// Wake 在写入新事件后调用，让中继马上发布。
	Wake func()
	Now  func() time.Time
}

func (r *Registrar) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Register 登记一条下架通知。返回 created 为假说明这条（交易所、市场、代码、范围、下架时间）以前登记过，不会重复发布。
func (r *Registrar) Register(ctx context.Context, notice Notice) (created bool, eventID string, err error) {
	if notice.Scope == "" {
		notice.Scope = events.DelistScopeAll
	}
	// 交易所代码统一成大写再校验（与手工登记的 RPC 一致）：下游按大写判断“同一个标的”，来源返回小写或大小写混用时不能因此漏报。
	notice.Symbol = strings.ToUpper(strings.TrimSpace(notice.Symbol))
	if err := notice.Validate(); err != nil {
		return false, "", fmt.Errorf("%w：%v", ErrInvalidNotice, err)
	}
	now := r.now()
	delistAt := notice.DelistAt.UTC().Truncate(time.Millisecond)
	subject := events.InstrumentDelistSubjectID(notice.Exchange, notice.Market, notice.Symbol)
	// 事件 ID 由（交易所、市场、代码、范围、下架时间）确定性生成；同一标的第 n 次发布（n>0，例如下架时间改回以前出现过的值）
	// 带上序号，免得和以前的事件 ID 相同而被去重丢掉。
	build := func(revision int) (store.OutboxEvent, error) {
		eventID := events.InstrumentDelistEventID(notice.Exchange, notice.Market, notice.Symbol, notice.Scope, delistAt)
		if revision > 0 {
			eventID = fmt.Sprintf("%s-r%d", eventID, revision)
		}
		raw, err := r.Registry.MarshalMessage(events.InstrumentDelistScheduled, &storagepb.InstrumentDelistScheduled{
			Exchange: notice.Exchange, Market: notice.Market, ExchangeSymbol: notice.Symbol, DelistAt: timestamppb.New(delistAt),
			Source: notice.Source, FirstSeenAt: timestamppb.New(now), Scope: notice.Scope,
		}, events.PublishOptions{EventID: eventID, OccurredAt: now, SpaceID: events.DelistSystemSpaceID, SubjectID: subject})
		if err != nil {
			return store.OutboxEvent{}, fmt.Errorf("生成下架事件失败: %w", err)
		}
		return store.OutboxEvent{EventID: eventID, SpaceID: events.DelistSystemSpaceID, EventName: events.InstrumentDelistScheduled.Name(), SubjectToken: subject, Payload: raw}, nil
	}
	created, eventID, err = r.Repo.Register(ctx, store.DelistNotice{
		Exchange: notice.Exchange, Market: notice.Market, Symbol: notice.Symbol, Scope: notice.Scope, DelistAt: delistAt,
		Source: notice.Source, SeenAt: now,
	}, build)
	if err != nil {
		return false, "", err
	}
	if created && r.Wake != nil {
		r.Wake()
	}
	return created, eventID, nil
}
