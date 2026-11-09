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
	// SourceDown 是汇合 bar 上因子被整体跳过的附加来源（来源名 → 说明）：本期不读它，它的列按缺数处理。
	SourceDown map[string]string
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
	markets := resolved.Markets()
	check := func(key string) readiness.Result {
		var previous *readiness.Record
		if adjacent != nil {
			previous = &readiness.Record{Factors: adjacent.Views[resolved.Tasks[key].ViewID].Factors}
		}
		return readiness.Check(resolved.ReadinessBinding(), previous, p.events[key])
	}
	// 统一数据集只有一个 View 和一条事件：判断一次，再按序列拆成各市场的结论（每个市场的失败、被跳过的 tag 各自判断）。
	var unified readiness.Result
	if resolved.Unified() {
		unified = check(input.MarketMulti)
	}
	for _, market := range markets {
		viewID := resolved.TaskOf(market).ViewID
		var result readiness.Result
		if resolved.Unified() {
			result = unified.ForMarket(market)
		} else {
			result = check(market)
		}
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
	if merged.Reason == "" && len(merged.Unavailable) == len(markets) {
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
		return merged
	}
	h.checkSourcesReady(p, resolved, &merged)
	return merged
}

// checkSourcesReady 在汇合 bar 上对到达的附加来源事件做就绪判断（非汇合 bar 没有来源事件，不判断）：来源的因子指纹写进本期输入记录；
// 成员契约变了（factor_changed）等配置、契约类原因整期按原因处理；来源的因子被跳过时只让这个来源本期不可用，它的列按缺数处理。
func (h *Handler) checkSourcesReady(p *period, resolved input.Resolved, merged *mergedReadiness) {
	for _, name := range resolved.SourceNames() {
		source := resolved.Sources[name]
		for _, key := range resolved.TaskKeys() {
			event, ok := p.events[dsl.TaskKey(name, key)]
			if !ok {
				continue
			}
			result := readiness.Check(source.ReadinessBinding(), nil, event)
			viewID := source.Tasks[key].ViewID
			view := p.input.Views[viewID]
			if len(result.Hashes) > 0 {
				view.Factors = result.Hashes
			}
			p.input.Views[viewID] = view
			switch {
			case result.Reason == "":
			case readiness.IsDataReason(result.Reason):
				if merged.SourceDown == nil {
					merged.SourceDown = map[string]string{}
				}
				merged.SourceDown[name] = result.Detail
			case merged.Reason == "":
				merged.Reason, merged.Detail = result.Reason, fmt.Sprintf("来源 %s：%s", name, result.Detail)
			}
		}
	}
	if merged.Reason != "" {
		merged.Unavailable, merged.SourceDown = map[string]string{}, nil
	}
}

// eventUniverses 返回各市场事件里的标的名单（市场 → 名单）。统一数据集只有一条事件，名单是各市场标的的并集（事件里没有按序列的名单），
// 每个市场用它再与自己的活跃标的求交。
func (p *period) eventUniverses(resolved input.Resolved) map[string][]string {
	universes := make(map[string][]string, len(p.events))
	for key, event := range p.events {
		if source, _ := dsl.SplitTaskKey(key); source != "" {
			continue // 附加来源的事件不提供基础集合
		}
		if resolved.Unified() && key == input.MarketMulti {
			for _, market := range resolved.Markets() {
				universes[market] = eventUniverse(event)
			}
			continue
		}
		universes[key] = eventUniverse(event)
	}
	return universes
}

// gather 汇合本期各 View 的就绪事件，返回各市场的事件与是否到齐（设计 3.2.1 第 2～4 步）。绑定两个任务的实例先把到达的
// 这条事件幂等地记下，再检查本期的两个 View 是否都到了；之后把更早还不会再等到齐的期补记为 skipped。
func (h *Handler) gather(ctx context.Context, p *period, payload *storagepb.ViewDataReady, taskKey string, periodTime time.Time) (map[string]*storagepb.ViewDataReady, bool, error) {
	resolved := p.runtime.resolved
	if !resolved.Gathers() {
		return map[string]*storagepb.ViewDataReady{taskKey: payload}, true, nil
	}
	if err := h.putArrival(ctx, p.instance, p.runtime.sessionID, p.boundary.BarEnd, p.eventID, payload); err != nil {
		return nil, false, err
	}
	records, err := h.Store.ReadyEventsAt(ctx, p.instance.InstanceID, p.runtime.sessionID, p.boundary.BarEnd)
	if err != nil {
		return nil, false, err
	}
	events := make(map[string]*storagepb.ViewDataReady, len(records))
	arrived := make([]string, 0, len(records))
	for _, record := range records {
		recordKey, source, reference, ok := resolved.ViewRole(record.ViewID)
		if !ok || reference {
			continue
		}
		recordKey = dsl.TaskKey(source, recordKey)
		event := &storagepb.ViewDataReady{}
		if err := proto.Unmarshal(record.Payload, event); err != nil {
			h.logf("实例 %s 的到达记录 %s 无法解析：%v", p.instance.InstanceID, record.ViewID, err)
			return nil, false, fmt.Errorf("解析 View %s 的到达记录：%w", record.ViewID, err)
		}
		events[recordKey] = event
		arrived = append(arrived, record.ViewID)
		p.input.Views[record.ViewID] = store.ViewInput{EventID: record.EventID}
	}
	complete, err := arrivedAll(resolved, p.boundary.BarEnd, arrived)
	if err != nil {
		return nil, false, err
	}
	if err := h.sweepStale(ctx, p, complete); err != nil {
		return nil, false, err
	}
	return events, complete, nil
}

// arrivedAll 报告主帧收盘于 barEnd 的这一根要等的 View 都已到达（见 input.Resolved.ExpectedViews）：主帧的全部 View，
// 收盘时刻对齐时还有附加来源的 View；其余时刻只等主帧。
func arrivedAll(resolved input.Resolved, barEnd time.Time, arrived []string) (bool, error) {
	expected, err := resolved.ExpectedViews(barEnd)
	if err != nil {
		return false, err
	}
	have := make(map[string]struct{}, len(arrived))
	for _, viewID := range arrived {
		have[viewID] = struct{}{}
	}
	for _, viewID := range expected {
		if _, ok := have[viewID]; !ok {
			return false, nil
		}
	}
	return true, nil
}

// sourceArrival 处理附加来源的就绪事件：按来源的日历与周期换算出它的收盘时刻，就是要汇合的那根主帧的 bar_end（只有收盘时刻对齐的
// 那一根才等来源）。事件记为这根主帧的到达记录；主帧这一根已经到了时，用主帧的事件重新走一遍处理（这时就会汇合求值），否则等主帧。
// 这一根已经处理过（例如来源超期、已补记 market_missing）时直接确认。
func (h *Handler) sourceArrival(ctx context.Context, loader Loader, instance store.Instance, rt *runtime, message *eventpb.EventMessage, payload *storagepb.ViewDataReady, name string, periodTime time.Time) error {
	barEnd, err := h.arrivalBarEnd(rt, payload.GetViewId(), periodTime)
	if err != nil {
		if h.firstDrop(message.GetEventId(), instance.InstanceID) {
			h.logf("实例 %s 的来源 %s 的事件（period_time %s）无法对应到主帧的周期，本期丢弃：%v", instance.InstanceID, name, periodTime.Format(time.RFC3339), err)
		}
		return nil
	}
	latest, hasLatest, err := h.Store.LatestProcessed(ctx, instance.InstanceID, rt.sessionID)
	if err != nil {
		return err
	}
	if hasLatest && !barEnd.After(latest.BarEndTime) {
		return nil
	}
	if err := h.putArrival(ctx, instance, rt.sessionID, barEnd, message.GetEventId(), payload); err != nil {
		return err
	}
	records, err := h.Store.ReadyEventsAt(ctx, instance.InstanceID, rt.sessionID, barEnd)
	if err != nil {
		return err
	}
	main, ok := mainRecord(rt.resolved, records)
	if !ok {
		return nil
	}
	event := &storagepb.ViewDataReady{}
	if err := proto.Unmarshal(main.Payload, event); err != nil {
		return fmt.Errorf("解析 View %s 的到达记录：%w", main.ViewID, err)
	}
	if err := h.process(ctx, loader, instance, &eventpb.EventMessage{EventId: main.EventID, SpaceId: instance.SpaceID}, event); err != nil {
		return err
	}
	h.forget(main.EventID)
	return nil
}

// mainRecord 返回到达记录里第一条主帧 View 的记录。
func mainRecord(resolved input.Resolved, records []store.ReadyEvent) (store.ReadyEvent, bool) {
	for _, record := range records {
		if _, ok := resolved.TaskKeyOfView(record.ViewID); ok {
			return record, true
		}
	}
	return store.ReadyEvent{}, false
}

// arrivalBarEnd 返回一个 View 的事件（period_time 是它的 bar_start）要记在哪根主帧上：主帧的事件就是这一根的 bar_end；附加来源的事件
// 先按来源的日历与周期换算出来源的收盘时刻，它必须正好也是一根主帧的收盘时刻。
func (h *Handler) arrivalBarEnd(rt *runtime, viewID string, periodTime time.Time) (time.Time, error) {
	resolved := rt.resolved
	_, source, _, _ := resolved.ViewRole(viewID)
	if source == "" {
		boundary, err := input.FromStorageStart(resolved.Calendar, resolved.Bar, periodTime)
		if err != nil {
			return time.Time{}, err
		}
		return boundary.BarEnd, nil
	}
	binding := resolved.Sources[source]
	sourceBoundary, err := input.FromStorageStart(binding.Calendar, binding.Bar, periodTime)
	if err != nil {
		return time.Time{}, err
	}
	mainBoundary, err := input.FromBarEnd(resolved.Calendar, resolved.Bar, sourceBoundary.BarEnd)
	if err != nil {
		return time.Time{}, err
	}
	if !mainBoundary.BarEnd.Equal(sourceBoundary.BarEnd) {
		return time.Time{}, fmt.Errorf("来源 %s 的收盘时刻 %s 不是主帧的收盘时刻", source, sourceBoundary.BarEnd.Format(time.RFC3339))
	}
	return sourceBoundary.BarEnd, nil
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
	if rt == nil || rt.sessionID == "" || (!rt.resolved.Gathers() && rt.pool == nil) || rt.resolved.Calendar == "" {
		return
	}
	if _, _, reference, _ := rt.resolved.ViewRole(payload.GetViewId()); reference {
		return
	}
	barEnd, err := h.arrivalBarEnd(rt, payload.GetViewId(), periodTime)
	if err != nil {
		return
	}
	if err := h.putArrival(ctx, instance, rt.sessionID, barEnd, message.GetEventId(), payload); err != nil {
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
		if rt.pool == nil {
			complete, err := arrivedAll(rt.resolved, barEnd, arrived[barEnd])
			if err != nil {
				return err
			}
			if !complete {
				continue
			}
		} else if len(arrived[barEnd]) < len(rt.resolved.Tasks) {
			continue
		}
		records, err := h.Store.ReadyEventsAt(ctx, instance.InstanceID, rt.sessionID, barEnd)
		if err != nil || len(records) == 0 {
			return err
		}
		// 用主帧的那条事件处理（附加来源的事件只参与汇合）。
		record, ok := mainRecord(rt.resolved, records)
		if !ok {
			continue
		}
		event := &storagepb.ViewDataReady{}
		if err := proto.Unmarshal(record.Payload, event); err != nil {
			return fmt.Errorf("解析 View %s 的到达记录：%w", record.ViewID, err)
		}
		message := &eventpb.EventMessage{EventId: record.EventID, SpaceId: instance.SpaceID}
		if err := h.process(ctx, loader, instance, message, event); err != nil {
			return err
		}
		// 补处理用的是这一期已存事件的 ID，不会经过 Handle 的收尾：成功后在这里清掉它的尝试计数，内存里不留条目。
		h.forget(record.EventID)
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
	latest, err := h.Store.LatestReadyByView(ctx, instanceID, sessionID, resolved.AllViewIDs())
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
			viewID := resolved.TaskOf(market).ViewID
			if have[viewID] {
				continue
			}
			missing = append(missing, market)
			if latest[viewID].After(barEnd) {
				moved = true
			}
		}
		// 收盘时刻对齐的这一根还要等附加来源：来源已经到了更晚的一根、或更晚的一期已汇合时，同样补记。
		var missingSources []string
		for _, name := range resolved.SourceNames() {
			source := resolved.Sources[name]
			if _, aligned, err := source.Period(barEnd); err != nil || !aligned {
				continue
			}
			for _, key := range resolved.TaskKeys() {
				viewID := source.Tasks[key].ViewID
				if have[viewID] {
					continue
				}
				missingSources = append(missingSources, name)
				if latest[viewID].After(barEnd) {
					moved = true
				}
				break
			}
		}
		// 要等的 View 都已记下的期不是缺数据，只是还没求值：不补记，由 processPending 按 bar 顺序求值。
		if len(missing)+len(missingSources) == 0 || !(complete || moved) {
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
		var parts []string
		if len(missing) > 0 {
			parts = append(parts, fmt.Sprintf("另一个市场（%s）的数据未到", strings.Join(missing, "、")))
		}
		if len(missingSources) > 0 {
			parts = append(parts, fmt.Sprintf("附加来源（%s）的数据未到", strings.Join(missingSources, "、")))
		}
		detail := strings.Join(parts, "；")
		if err := h.commitSkipped(ctx, &stalePeriod, store.SkipMarketMissing, detail); err != nil {
			return err
		}
	}
	return nil
}
