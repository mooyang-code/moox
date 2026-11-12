package klinecheck

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// 收市后整天重取：有的市场最后一根 K 线在收市后还会变（港股 16:00 这根在 16:10 前才并入收市竞价的成交），
// 实时那一期取到的不是最终值；自动补采只补缺的根，不会重取已有的根。所以到了“收市 + 延迟”的那一轮，
// 对空间里仍在采集的数据集各提交一个强制补采作业，范围是当天整天。

// RefetchAfterClose 每轮对账调用一次。和自动补采一样按 autoScanInterval 分轮：重取时刻落在
// （上一轮起点, 这一轮起点] 之内的那一轮提交，每天只会命中一轮。不存持久状态：Collector 在那一轮里重启会再提交一次
// （重复重取一遍，结果相同）；那一轮整段都没在运行就不重取，要手工用 backfill --force 补。
// 没有配置 CloseRefetch 的装配、不需要重取的市场、非交易日什么都不做。
func (b *Backfiller) RefetchAfterClose(ctx context.Context, spaceID string) error {
	if b == nil || b.CloseRefetch == nil {
		return nil
	}
	round := b.now().Truncate(autoScanInterval)
	from, to, due, err := b.CloseRefetch(spaceID, round.Add(-autoScanInterval), round)
	if err != nil || !due {
		return err
	}
	b.mu.Lock()
	done := b.refetchedDay[spaceID].Equal(from)
	b.mu.Unlock()
	if done {
		return nil
	}
	records, err := b.Plans.ListAll(ctx, spaceID)
	if err != nil {
		return err
	}
	timeline, err := BuildTimeline(records)
	if err != nil {
		return err
	}
	// 作业按数据集提交；同一个数据集只提交一次。
	datasets := map[string]bool{}
	for key := range timeline {
		datasetID, frequency, _ := strings.Cut(key, "|")
		if !timeline.Active(key) || (b.RefetchFrequency != nil && !b.RefetchFrequency(spaceID, frequency)) {
			continue
		}
		datasets[datasetID] = true
	}
	ids := make([]string, 0, len(datasets))
	for id := range datasets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var failed error
	for _, datasetID := range ids {
		// 来源写 manual：自动补采遇到未结束的 auto 作业会跳过本轮，监控也把 auto 作业的分块算作实时采集漏掉的根。
		jobID, chunks, err := b.Submit(ctx, JobRequest{SpaceID: spaceID, DatasetID: datasetID, From: from, To: to, Force: true}, recoverySource)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		if jobID != "" {
			log.InfoContextf(ctx, "collector close refetch job created space=%s dataset=%s job=%s chunks=%d from=%s to=%s",
				spaceID, datasetID, jobID, chunks, from.Format(time.RFC3339), to.Format(time.RFC3339))
		}
	}
	if failed != nil {
		// 不记“已提交”：下一轮对账（仍在同一轮重取窗口内）再试，已经提交成功的数据集会重复重取一遍。
		return failed
	}
	b.mu.Lock()
	if b.refetchedDay == nil {
		b.refetchedDay = map[string]time.Time{}
	}
	b.refetchedDay[spaceID] = from
	b.mu.Unlock()
	return nil
}
