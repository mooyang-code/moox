package trigger

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// 本文件实现绑定两个因子任务（现货、永续各一个）的实例的汇合求值（设计 3.2.1）：每个 View 的就绪事件到达时先记下并确认，
// 两个 View 本期都到齐才求值一次。只绑一个任务的实例到达即算到齐，用手上的这条事件，不写到达记录。

// mergedReadiness 是各市场 View 就绪判断的合并结论。
type mergedReadiness struct {
	// Reason 与 Detail 非空表示整期按这个原因处理。
	Reason, Detail string
	// Results 是各市场的就绪结论（市场 → 结论）。
	Results map[string]readiness.Result
	// Members 只有策略池用：各成员各自的就绪结论（成员 id → 结论）。池自己的 Reason 只在所有成员都没通过时非空。
	Members map[string]mergedReadiness
	// Unavailable 是本期整体不可用的市场（数据类原因，市场 → 说明）：只让这个市场不可用，不整期跳过。
	Unavailable map[string]string
}

// checkReady 对每个 View 的事件各跑一次就绪判断，再合并结论（设计 3.2.1 第 3 步）：
//   - 配置、契约类原因（成员契约变了、事件缺失、没有上一根的版本证据）：整期按那个原因处理；
//   - 数据类原因（该市场的因子本期被跳过）：需要的市场全部不可用时仍按原因整期跳过（只绑一个任务的实例就是这种情况）；
//     只有一部分市场不可用时，只让这些市场本期不可用。
//
// 各 View 的因子指纹写进本期输入记录，下一期据此核对相邻周期。
func (h *Handler) checkReady(p *period, adjacent *store.InputRecord) mergedReadiness {
	return h.checkReadyFor(p, p.runtime.resolved, adjacent)
}

// checkReadyFor 按一个绑定（普通策略的绑定，或策略池一个成员的绑定）做就绪判断，只看这个绑定用到的市场与因子。
func (h *Handler) checkReadyFor(p *period, resolved input.Resolved, adjacent *store.InputRecord) mergedReadiness {
	merged := mergedReadiness{Results: map[string]readiness.Result{}, Unavailable: map[string]string{}}
	var dataReason, dataDetail string
	for _, market := range resolved.Markets() {
		viewID := resolved.Tasks[market].ViewID
		var previous *readiness.Record
		if adjacent != nil {
			previous = &readiness.Record{Factors: adjacent.Views[viewID].Factors}
		}
		result := readiness.Check(resolved.ReadinessBinding(), previous, p.events[market])
		merged.Results[market] = result
		// 策略池里多个成员可能用同一个 View：各成员的因子指纹合并成一份。
		view := p.input.Views[viewID]
		if view.Factors == nil {
			view.Factors = make(map[string]string, len(result.Hashes))
		}
		for factor, hash := range result.Hashes {
			view.Factors[factor] = hash
		}
		p.input.Views[viewID] = view
		switch {
		case result.Reason == "":
		case readiness.IsDataReason(result.Reason):
			merged.Unavailable[market] = result.Detail
			if dataReason == "" {
				dataReason, dataDetail = result.Reason, result.Detail
			}
		case merged.Reason == "":
			merged.Reason, merged.Detail = result.Reason, result.Detail
		}
	}
	if merged.Reason == "" && len(merged.Unavailable) == len(resolved.Tasks) {
		merged.Reason, merged.Detail = dataReason, dataDetail
	}
	// 选币市场整体不可用时没有候选池（select 为单个市场，另一个市场只决定下单市场）：按数据类原因整期跳过，
	// 不能让它走“预期标的全部缺数”的 too_many_missing。
	if selectMarket := resolved.Market.Select; merged.Reason == "" && selectMarket != dsl.MarketBoth {
		if detail, down := merged.Unavailable[selectMarket]; down {
			merged.Reason, merged.Detail = merged.Results[selectMarket].Reason, detail
		}
	}
	if merged.Reason != "" {
		merged.Unavailable = map[string]string{}
	}
	return merged
}

// eventUniverses 返回各市场事件里的标的名单（市场 → 名单）。
func (p *period) eventUniverses() map[string][]string {
	universes := make(map[string][]string, len(p.events))
	for market, event := range p.events {
		universes[market] = eventUniverse(event)
	}
	return universes
}

// gather 汇合本期各 View 的就绪事件，返回各市场的事件与是否到齐（设计 3.2.1 第 2～4 步）。绑定两个任务的实例先把到达的
// 这条事件幂等地记下，再检查本期的两个 View 是否都到了；之后把更早还不会再等到齐的期补记为 skipped。
func (h *Handler) gather(ctx context.Context, p *period, payload *storagepb.ViewDataReady, market string, periodTime time.Time) (map[string]*storagepb.ViewDataReady, bool, error) {
	resolved := p.runtime.resolved
	if len(resolved.Tasks) == 1 {
		return map[string]*storagepb.ViewDataReady{market: payload}, true, nil
	}
	if err := h.putArrival(ctx, p.instance, p.runtime.sessionID, p.boundary.BarEnd, p.eventID, payload); err != nil {
		return nil, false, err
	}
	records, err := h.Store.ReadyEventsAt(ctx, p.instance.InstanceID, p.runtime.sessionID, p.boundary.BarEnd)
	if err != nil {
		return nil, false, err
	}
	events := make(map[string]*storagepb.ViewDataReady, len(records))
	for _, record := range records {
		recordMarket, ok := resolved.MarketOfView(record.ViewID)
		if !ok {
			continue
		}
		event := &storagepb.ViewDataReady{}
		if err := proto.Unmarshal(record.Payload, event); err != nil {
			h.logf("实例 %s 的到达记录 %s 无法解析：%v", p.instance.InstanceID, record.ViewID, err)
			return nil, false, fmt.Errorf("解析 View %s 的到达记录：%w", record.ViewID, err)
		}
		events[recordMarket] = event
		p.input.Views[record.ViewID] = store.ViewInput{EventID: record.EventID}
	}
	complete := len(events) == len(resolved.Tasks)
	if err := h.sweepStale(ctx, p, complete); err != nil {
		return nil, false, err
	}
	return events, complete, nil
}

// putArrival 幂等地记下一个 View 的就绪事件（原始 ViewDataReady 按字节保存）。
func (h *Handler) putArrival(ctx context.Context, instance store.Instance, sessionID string, barEnd time.Time, eventID string, payload *storagepb.ViewDataReady) error {
	raw, err := proto.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = h.Store.PutReadyEvent(ctx, store.ReadyEvent{InstanceID: instance.InstanceID, SessionID: sessionID, ViewID: payload.GetViewId(), BarEnd: barEnd, EventID: eventID, Payload: raw, ArrivedAt: h.now()})
	if err != nil {
		return fmt.Errorf("记录 View %s 的就绪事件：%w", payload.GetViewId(), err)
	}
	return nil
}

// recordArrival 在门禁返回等待时把到达的事件记下（尽力而为，失败只记日志；事件本身会重投）。
func (h *Handler) recordArrival(ctx context.Context, instance store.Instance, rt *runtime, message *eventpb.EventMessage, payload *storagepb.ViewDataReady, periodTime time.Time) {
	if rt == nil || rt.sessionID == "" || (len(rt.resolved.Tasks) < 2 && rt.pool == nil) || rt.resolved.Calendar == "" {
		return
	}
	boundary, err := input.FromStorageStart(rt.resolved.Calendar, rt.resolved.Bar, periodTime)
	if err != nil {
		return
	}
	if err := h.putArrival(ctx, instance, rt.sessionID, boundary.BarEnd, message.GetEventId(), payload); err != nil {
		h.logf("实例 %s 门禁等待期间记录到达事件失败：%v", instance.InstanceID, err)
	}
}

// processPending 把早于 before、晚于 after（已处理到的那一期）、两个 View 都已记下但还没有结果的期，按 bar 顺序用已存的原始事件求值。
func (h *Handler) processPending(ctx context.Context, loader Loader, instance store.Instance, rt *runtime, after, before time.Time) error {
	arrived, periods, err := h.Store.UnprocessedReadyPeriods(ctx, instance.InstanceID, rt.sessionID, after, before)
	if err != nil {
		return err
	}
	for _, barEnd := range periods {
		if len(arrived[barEnd]) < len(rt.resolved.Tasks) {
			continue
		}
		records, err := h.Store.ReadyEventsAt(ctx, instance.InstanceID, rt.sessionID, barEnd)
		if err != nil || len(records) == 0 {
			return err
		}
		event := &storagepb.ViewDataReady{}
		if err := proto.Unmarshal(records[0].Payload, event); err != nil {
			return fmt.Errorf("解析 View %s 的到达记录：%w", records[0].ViewID, err)
		}
		message := &eventpb.EventMessage{EventId: records[0].EventID, SpaceId: instance.SpaceID}
		if err := h.process(ctx, loader, instance, message, event); err != nil {
			return err
		}
		// 补处理用的是这一期已存事件的 ID，不会经过 Handle 的收尾：成功后在这里清掉它的尝试计数，内存里不留条目。
		h.forget(records[0].EventID)
	}
	return nil
}

// sweepStale 把更早还没有结果、也不会再等到齐的期补记为 skipped(market_missing)（设计 3.2.1 第 4 步）。补记必须在提交
// 更晚一期的结果之前完成，否则会被当成乱序。只在两种情况下补记：
//   - 缺的那个 View 本身已经到了更晚的一期，说明它跳过了这一期，不会再来；
//   - 更晚的某一期已经汇合完成（complete 为真，马上求值）。
//
// 只是“另一个 View 先到了下一期”不补记：两个 View 稳定相差一根以上是正常情况。
func (h *Handler) sweepStale(ctx context.Context, p *period, complete bool) error {
	instanceID, sessionID := p.instance.InstanceID, p.runtime.sessionID
	arrived, periods, err := h.Store.UnprocessedReadyPeriods(ctx, instanceID, sessionID, p.after, p.boundary.BarEnd)
	if err != nil || len(periods) == 0 {
		return err
	}
	resolved := p.runtime.resolved
	latest, err := h.Store.LatestReadyByView(ctx, instanceID, sessionID, resolved.ViewIDs())
	if err != nil {
		return err
	}
	for _, barEnd := range periods {
		have := make(map[string]bool, len(arrived[barEnd]))
		for _, viewID := range arrived[barEnd] {
			have[viewID] = true
		}
		var missing []string
		moved := false
		for _, market := range resolved.Markets() {
			viewID := resolved.Tasks[market].ViewID
			if have[viewID] {
				continue
			}
			missing = append(missing, market)
			if latest[viewID].After(barEnd) {
				moved = true
			}
		}
		// 两个 View 都已记下的期不是缺数据，只是还没求值：不补记，由 processPending 按 bar 顺序求值。
		if len(missing) == 0 || !(complete || moved) {
			continue
		}
		boundary, err := input.FromBarEnd(resolved.Calendar, resolved.Bar, barEnd)
		if err != nil {
			return err
		}
		validUntil, err := advance(resolved.Calendar, resolved.Bar, barEnd, ValidBars)
		if err != nil {
			return err
		}
		stalePeriod := *p
		stalePeriod.boundary, stalePeriod.validUntil = boundary, validUntil
		stalePeriod.input = store.InputRecord{Bar: resolved.Bar, Calendar: resolved.Calendar, BarStart: boundary.StorageStart, Views: map[string]store.ViewInput{}}
		for _, viewID := range arrived[barEnd] {
			stalePeriod.input.Views[viewID] = store.ViewInput{}
		}
		detail := fmt.Sprintf("另一个市场（%s）的数据未到", strings.Join(missing, "、"))
		if err := h.commitSkipped(ctx, &stalePeriod, store.SkipMarketMissing, detail); err != nil {
			return err
		}
	}
	return nil
}
