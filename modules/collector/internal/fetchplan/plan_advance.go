package fetchplan

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// advance 续跑一份未生效的采集计划：登记快照、写“开始”日志、下发，全部确认并写完“已生效”日志就记 applied。
func (r *PlanReconciler) advance(ctx context.Context, record *store.FetchPlanRecord) error {
	content, err := decodeFetchPlanContent(record.Plan)
	if err != nil {
		return r.fail("store", err)
	}
	progress, err := decodeFetchPlanProgress(record.Progress)
	if err != nil {
		return r.fail("store", err)
	}
	ready, err := r.registerSnapshots(ctx, record, content, progress)
	if err != nil || !ready {
		return err
	}
	// 登记完成后才记“开始”日志：被撤回的数据集不会出现在日志里。
	if !progress.LoggedStart {
		if r.logPlan(ctx, record, content, false) == nil {
			progress.LoggedStart = true
			if err := r.saveProgress(ctx, record, progress, record.Status, record.LastError); err != nil {
				return err
			}
		}
	}
	if record.Status == store.FetchPlanPending {
		if err := r.saveProgress(ctx, record, progress, store.FetchPlanApplying, ""); err != nil {
			return err
		}
	}
	done, err := r.pushFunctions(ctx, record, content, progress)
	if err != nil || !done {
		return err
	}
	if !progress.LoggedApply {
		if r.logPlan(ctx, record, content, true) == nil {
			progress.LoggedApply = true
			if err := r.saveProgress(ctx, record, progress, record.Status, ""); err != nil {
				return err
			}
		}
	}
	if !progress.LoggedStart || !progress.LoggedApply {
		return r.fail("activity", errors.New("activity log is not written yet; fetch plan stays open"))
	}
	if err := r.saveProgress(ctx, record, progress, store.FetchPlanApplied, ""); err != nil {
		return err
	}
	log.InfoContextf(ctx, "collector fetch plan applied space=%s plan=%d", r.SpaceID, record.ID)
	return nil
}

// registerSnapshots 为要登记的数据集登记快照。Storage 返回“生效点不可用”时把这个数据集从本计划里撤回（withdraw），
// 下一份计划重新查表，不在本计划里重选 P（重选可能需要第三份）；其他错误下一轮用同一个请求重放。
// 未生效计划里数据集的内容一旦发出过登记请求就不再改：崩溃后重放同一个请求，Storage 对同 P 同 hash 一定返回成功。
func (r *PlanReconciler) registerSnapshots(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress) (bool, error) {
	for _, key := range content.sortedDatasetKeys() {
		dataset := content.Datasets[key]
		if !dataset.Register || progress.Registered[key] {
			continue
		}
		registration, err := r.Storage.RegisterSeriesSnapshot(ctx, r.SpaceID, dataset.DatasetID, dataset.Frequency, dataset.Series, dataset.P)
		switch {
		case errors.Is(err, marketstorage.ErrStaleEffectiveFrom):
			return false, r.withdraw(ctx, record, content, progress, key)
		case err != nil:
			// 结果不明或暂时失败：下一轮用同一份请求重放。
			_ = r.saveProgress(ctx, record, progress, record.Status, err.Error())
			return false, r.fail("storage", fmt.Errorf("register series snapshot %s: %w", key, err))
		case registration.Hash != dataset.Hash || registration.Count != dataset.Count:
			return false, r.fail("storage", fmt.Errorf("Storage computed hash %s/%d for %s, Collector computed %s/%d", registration.Hash, registration.Count, key, dataset.Hash, dataset.Count))
		}
		// Storage 已确认登记：登记标记与时间线上“从 P 起是这份序列”的一行一起落库。
		progress.mark(&progress.Registered, key)
		var timeline []store.SeriesTimelineEntry
		if dataset.Timeline {
			timeline = append(timeline, seriesTimelineEntry(r.SpaceID, dataset))
		}
		if err := r.saveProgress(ctx, record, progress, record.Status, "", timeline...); err != nil {
			return false, err
		}
	}
	return true, nil
}

// withdraw 把登记被拒（生效点已不可用）的数据集从未生效计划里撤回：动作改成 keep，份恢复成已生效状态里的样子（裁剪照做），
// 重新组合所有函数。撤回之后计划里什么变化都没有了（这时也不可能有已登记的数据集），就删掉这条未生效记录。
// 所有登记完成之前不下发，所以撤回时还没有任何函数用过这些内容。
func (r *PlanReconciler) withdraw(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress, key string) error {
	var previousDatasets map[string]*PlanDataset
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil {
		return r.fail("store", fmt.Errorf("read applied fetch plan: %w", err))
	}
	if applied != nil {
		appliedContent, err := decodeFetchPlanContent(applied.Plan)
		if err != nil {
			return r.fail("store", err)
		}
		_, previousDatasets = appliedContent.Applied()
	}
	dataset := content.Datasets[key]
	withdrawDataset(content, key, previousDatasets[key])
	content.composeFunctions()
	reason := fmt.Sprintf("%s %s 的生效点 %s 已不可用，已撤回，下一份计划重新安排", dataset.DatasetID, dataset.Frequency, time.Unix(dataset.P, 0).UTC().Format("01-02 15:04"))
	r.waiting = reason
	log.WarnContextf(ctx, "collector fetch plan withdrew a dataset space=%s plan=%d dataset=%s p=%d", r.SpaceID, record.ID, key, dataset.P)
	if !content.changedFrom(previousDatasets) && len(progress.Registered) == 0 && record.Status == store.FetchPlanPending {
		if err := r.Store.DeleteOpen(ctx, record.ID); err != nil {
			return r.fail("store", fmt.Errorf("delete withdrawn fetch plan: %w", err))
		}
		return nil
	}
	if err := r.Store.SavePlan(ctx, record.ID, mustJSON(content)); err != nil {
		return r.fail("store", err)
	}
	record.Plan = mustJSON(content)
	return nil
}

// withdrawDataset 把一个数据集在计划里的动作撤回：加的那一份去掉（数据集原来没有份时整个去掉），被替换的头份换回已生效的那一份。
func withdrawDataset(content *FetchPlanContent, key string, previous *PlanDataset) {
	dataset := content.Datasets[key]
	if dataset == nil {
		return
	}
	switch dataset.Action {
	case PlanActionAppend:
		dataset.Copies = dataset.Copies[:len(dataset.Copies)-1]
	case PlanActionReplace:
		if previous != nil {
			for _, item := range previous.Copies {
				if item.From == dataset.P {
					dataset.Copies[len(dataset.Copies)-1] = item
				}
			}
		}
	}
	if len(dataset.Copies) == 0 || previous == nil {
		delete(content.Datasets, key)
		return
	}
	dataset.PlanDatasetState, dataset.TimelineFrom = previous.PlanDatasetState, previous.TimelineFrom
	dataset.Action, dataset.P, dataset.Register, dataset.Timeline = PlanActionKeep, 0, false, false
	dataset.Series, dataset.Added, dataset.Removed = nil, nil, nil
}

// pushFunctions 下发：每个云函数一个 Ensure（建函数与下发环境、定时器合成一次），退役的函数只停定时器；
// 推送与本地执行项靠 collector-stream 心跳确认。全部确认后返回 true。
// 对每个云函数只有三种情况：没有操作在跑就起一个；有操作在跑就本轮跳过（不算失败）；
// 操作已结束：成功且用途是本计划就记确认，失败就按原因上报、下一轮重来。
func (r *PlanReconciler) pushFunctions(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress) (bool, error) {
	var cloudNames, streamItems []string
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if !function.Push || progress.Pushed[name] {
			continue
		}
		if function.IsSCF() {
			cloudNames = append(cloudNames, name)
		} else {
			streamItems = append(streamItems, name)
		}
	}
	if len(cloudNames) == 0 && len(streamItems) == 0 {
		return true, nil
	}
	if len(streamItems) > 0 {
		if err := r.confirmStream(ctx, record, content, progress, streamItems); err != nil {
			return false, err
		}
	}
	if len(cloudNames) == 0 {
		return allPushed(content, progress), nil
	}
	key := planOpKey(record.ID)
	ops := r.ops()
	changed := false
	var failures []string
	var firstErr error
	settle := func(name string, op *functionOp) {
		if op.confirmed() {
			progress.mark(&progress.Pushed, name)
			changed = true
			return
		}
		if op.err != nil {
			failures = append(failures, name+": "+op.err.Error())
			if firstErr == nil {
				firstErr = op.err
			}
		}
		// 定时器因为暂停被设成停用的成功不算确认：下一轮（恢复之后）重推。
	}
	var started []*functionOp
	names := map[string]string{} // 操作表的键 → 函数名
	for _, name := range cloudNames {
		opName := refKey(r.functionRef(name, content.Functions[name].Region))
		names[opName] = name
		if op := ops.take(opName, key); op != nil {
			settle(name, op)
			if op.confirmed() || op.err != nil {
				continue
			}
		}
		if ops.busy(opName) {
			continue // 另一个操作在跑：本轮跳过，等它结束
		}
		op, err := r.startPush(content.Functions[name], key)
		switch {
		case errors.Is(err, errOpBusy), errors.Is(err, errOpPausing), errors.Is(err, errOpsClosed):
			continue
		case err != nil:
			// 期望内容算不出来（环境、网络配置）：不碰云端，按环境问题上报。
			_ = r.saveProgress(ctx, record, progress, record.Status, err.Error())
			return false, r.fail("environment", fmt.Errorf("function %s: %w", name, err))
		}
		started = append(started, op)
	}
	ops.wait(ctx, started, r.roundWait(ctx))
	for _, op := range started {
		if done := ops.take(op.name, key); done != nil {
			settle(names[op.name], done)
		}
	}
	if changed || len(failures) > 0 {
		lastError := strings.Join(failures, "; ")
		if err := r.saveProgress(ctx, record, progress, record.Status, lastError); err != nil {
			return false, err
		}
	}
	if firstErr != nil {
		return false, r.opFailure(fmt.Errorf("fetch plan %d push: %d functions failed, first: %w", record.ID, len(failures), firstErr))
	}
	return allPushed(content, progress), nil
}

// startPush 为计划推进起一个操作：按 Target 与 Cron Ensure；退役的函数（Target 为空）只停定时器。
func (r *PlanReconciler) startPush(function *PlanFunctionChange, key string) (*functionOp, error) {
	if function.Target == nil {
		return r.startDisableTimer(r.functionRef(function.Name, function.Region), key)
	}
	spec, err := r.desiredSpec(functionTarget{Name: function.Name, Region: function.Region, Plan: function.Target, Cron: function.Cron})
	if err != nil {
		return nil, err
	}
	return r.startEnsure(key, spec)
}

// allPushed 判断需要下发的项都已确认。
func allPushed(content *FetchPlanContent, progress *FetchPlanProgress) bool {
	for name, function := range content.Functions {
		if function.Push && !progress.Pushed[name] {
			return false
		}
	}
	return true
}

// confirmStream 把推送与本地执行端的项“下发”：记下要求应用这份计划（collector-stream 心跳拿到新版本后拉取），
// 看到实例回报已应用该记录才算确认。
// 固定规则：计划里没有数据集正切到推送或本地执行端（collector-stream 只是旧执行端，或数据集留在它上面只是标的变了），
// 并且 collector-stream 已离线（心跳超时）时，这份计划不等它确认；有数据集正切到它上面的计划仍要等。
func (r *PlanReconciler) confirmStream(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress, names []string) error {
	if !progress.StreamPushed {
		progress.StreamPushed = true
		if err := r.saveProgress(ctx, record, progress, record.Status, record.LastError); err != nil {
			return err
		}
	}
	ok := false
	if !movesToWorker(content) {
		offline, err := r.streamOffline(ctx)
		if err != nil {
			return err
		}
		ok = offline
	}
	if !ok && r.Workers != nil {
		worker, err := r.Workers.Get(ctx, r.SpaceID)
		if err != nil {
			return r.fail("store", err)
		}
		if worker != nil {
			ok = worker.AppliedPlanID == record.ID
		}
	}
	if !ok {
		return nil
	}
	for _, name := range names {
		progress.mark(&progress.Pushed, name)
	}
	return r.saveProgress(ctx, record, progress, record.Status, record.LastError)
}

// streamOffline 判断 collector-stream 是否已离线到可以不等它确认：心跳超时，并且 Collector 进程启动已满一个心跳超时。
// 重启后心跳表里的时间是旧的，要先给它一次上报的机会。
func (r *PlanReconciler) streamOffline(ctx context.Context) (bool, error) {
	if r.StreamOnline == nil || r.StartedAt.IsZero() || r.now().Sub(r.StartedAt) < r.StreamTimeout {
		return false, nil
	}
	online, err := r.StreamOnline(ctx)
	if err != nil {
		return false, r.fail("store", fmt.Errorf("read collector-stream status: %w", err))
	}
	return !online, nil
}

// movesToWorker 判断计划里有数据集正从别的执行端切到推送或本地执行端（collector-stream 是新执行端）。
func movesToWorker(content *FetchPlanContent) bool {
	for _, dataset := range content.Datasets {
		if dataset.changesCopies() && dataset.HandoffFrom() > 0 && dataset.Head().Ingest != marketfetch.PlanExecutorSCF {
			return true
		}
	}
	return false
}

// logPlan 写入活动日志：计划开始（每个换了份或符号的数据集一条）与计划生效。写入失败返回错误，下一轮重试；幂等键保证不重复。
// 只做裁剪或只改函数内容的计划（没有 replace、append、symbols）两条都不写。
func (r *PlanReconciler) logPlan(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, applied bool) error {
	if r.Activity == nil {
		return nil
	}
	var keys []string
	for _, key := range content.sortedDatasetKeys() {
		if dataset := content.Datasets[key]; dataset.changesCopies() || dataset.Action == PlanActionSymbols {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	var entries []ActivityEntry
	refID := strconv.FormatInt(record.ID, 10)
	if !applied {
		for _, key := range keys {
			dataset := content.Datasets[key]
			effective := time.Unix(dataset.P, 0).UTC()
			detail := map[string]any{
				"dataset_id": dataset.DatasetID, "frequency": dataset.Frequency, "action": dataset.Action, "new_hash": dataset.Hash,
				"effective_period": effective.Format(time.RFC3339), "added": dataset.Added, "removed": dataset.Removed, "count": dataset.Count,
			}
			title := fmt.Sprintf("%s %s：新增 %d 个、移除 %d 个标的，将于 %s 生效", dataset.DatasetID, dataset.Frequency, len(dataset.Added), len(dataset.Removed), effective.Format("2006-01-02 15:04"))
			switch dataset.Action {
			case PlanActionReplace:
				title = fmt.Sprintf("%s %s：替换 %s 起的标的（新增 %d 个、移除 %d 个）", dataset.DatasetID, dataset.Frequency, effective.Format("2006-01-02 15:04"), len(dataset.Added), len(dataset.Removed))
			case PlanActionSymbols:
				title = fmt.Sprintf("%s %s：交易所符号变更，将于 %s 生效", dataset.DatasetID, dataset.Frequency, effective.Format("2006-01-02 15:04"))
			}
			entries = append(entries, ActivityEntry{
				SpaceID: r.SpaceID, Source: "collector", Category: "collector.subject_change", Level: "info", RefID: refID,
				Title: title, Detail: mustJSON(detail),
				DedupKey: fmt.Sprintf("collector.fetch_plan.%s.%d.created.%s", r.SpaceID, record.ID, key),
			})
		}
	} else {
		entries = append(entries, ActivityEntry{
			SpaceID: r.SpaceID, Source: "collector", Category: "collector.fetch_plan_applied", Level: "info", RefID: refID,
			Title: fmt.Sprintf("采集计划 %d 已生效", record.ID), Detail: "{}", DedupKey: fmt.Sprintf("collector.fetch_plan.%s.%d.applied", r.SpaceID, record.ID),
		})
	}
	return r.Activity.AppendActivityLogs(ctx, r.SpaceID, entries)
}
