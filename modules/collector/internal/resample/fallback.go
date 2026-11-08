package resample

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	"trpc.group/trpc-go/trpc-go/log"
)

// FallbackConfig 是兜底检查的参数。
type FallbackConfig struct {
	SpaceID string
	// LookbackBars 是检查最近多少根已收盘的目标 K 线。
	LookbackBars int
	// Grace 是收盘后给事件主路径留出的时间：只看收盘时刻不早于这么久之前的桶。这条规则只为减少重复劳动，不是正确性的前提。
	Grace time.Duration
	// MaxBucketsPerTick 限制一次检查最多补多少个桶。
	MaxBucketsPerTick int
}

func (c FallbackConfig) withDefaults() FallbackConfig {
	if c.LookbackBars <= 0 {
		c.LookbackBars = 3
	}
	if c.Grace <= 0 {
		c.Grace = time.Minute
	}
	if c.MaxBucketsPerTick <= 0 {
		c.MaxBucketsPerTick = 200
	}
	return c
}

// Fallback 是兜底检查：每分钟检查每个已就绪合成任务最近 N 根已收盘的目标 K 线，找出 Storage 里还缺的标的并补上。
// 它不保存任何状态，“该合成没合成”只看目标数据集里有没有行；事件主路径正常时它几乎什么都不用做。
type Fallback struct {
	Tasks   *store.TaskRepository
	Source  subjectSource
	Engine  *Engine
	Config  FallbackConfig
	Metrics *Metrics

	mu sync.Mutex
}

// Tick 做一次兜底检查。上一次检查还没结束时直接跳过，不排队。
func (f *Fallback) Tick(ctx context.Context, now time.Time) error {
	if f == nil || f.Tasks == nil || f.Source == nil || f.Engine == nil {
		return fmt.Errorf("resample fallback dependencies are required")
	}
	if !f.mu.TryLock() {
		return nil
	}
	defer f.mu.Unlock()
	cfg := f.Config.withDefaults()
	tasks, err := f.Tasks.ListEnabled(ctx, cfg.SpaceID)
	if err != nil {
		return err
	}
	budget := cfg.MaxBucketsPerTick
	var firstErr error
	for _, collection := range tasks {
		if budget <= 0 || ctx.Err() != nil {
			break
		}
		if !strings.EqualFold(collection.DataType, "kline_resample") || collection.PrepareState != domain.PrepareStateReady {
			continue
		}
		used, err := f.checkTask(ctx, collection, now, cfg, budget)
		budget -= used
		if err != nil {
			log.WarnContextf(ctx, "[Collector] resample fallback check failed space=%s task=%s: %v", collection.SpaceID, collection.TaskID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// checkTask 检查并补齐一个任务；返回用掉的桶数。
func (f *Fallback) checkTask(ctx context.Context, collection domain.CollectionTask, now time.Time, cfg FallbackConfig, budget int) (int, error) {
	task, err := NewTask(collection)
	if err != nil {
		return 0, err
	}
	members, err := SourceMembers(ctx, f.Source, task)
	if err != nil || members.Count() == 0 {
		return 0, err
	}
	if err := f.Engine.EnsureSnapshot(ctx, task, members); err != nil {
		log.WarnContextf(ctx, "[Collector] resample snapshot registration failed task=%s: %v", task.Spec.TaskID, err)
	}
	buckets, err := task.Clock.ClosedBefore(now.Add(-cfg.Grace), cfg.LookbackBars)
	if err != nil {
		return 0, err
	}
	missing, err := f.missing(ctx, task, buckets, members)
	if err != nil {
		return 0, err
	}
	used := 0
	for _, bucket := range buckets {
		list := missing[bucket.Key.Unix()]
		if list.Count() == 0 {
			continue
		}
		if used >= budget {
			break
		}
		used++
		result, err := f.Engine.RunBucketMembers(ctx, task, bucket, list, "")
		if err != nil {
			return used, err
		}
		if result.Written > 0 && f.Metrics != nil {
			f.Metrics.Fallbacks.Inc()
		}
	}
	return used, nil
}

// missing 读目标数据集，返回每个桶（以键的 Unix 秒索引）里还没有行的序列，按源序列分组。
func (f *Fallback) missing(ctx context.Context, task *Task, buckets []barclock.Bucket, members Members) (map[int64]Members, error) {
	out := map[int64]Members{}
	if len(buckets) == 0 {
		return out, nil
	}
	series := members.Series()
	keys := make([]*storagepb.RowKey, 0, len(buckets)*len(series))
	for _, bucket := range buckets {
		for _, item := range series {
			keys = append(keys, rowKey(task.Spec.SpaceID, task.Spec.TargetDatasetID, item.SubjectID, task.Spec.TargetFrequency.Storage, bucket.Key, item.SeriesTag))
		}
	}
	present := map[string]struct{}{}
	limit := f.Engine.MaxReadKeys
	if limit <= 0 {
		limit = defaultMaxReadKeys
	}
	for start := 0; start < len(keys); start += limit {
		rows, err := f.Engine.Primary.ReadRows(ctx, keys[start:min(start+limit, len(keys))], []string{fieldClose}, nil)
		if err != nil {
			return nil, fmt.Errorf("read target rows: %w", err)
		}
		for _, row := range rows {
			if key := row.GetKey().GetTimeSeries(); key != nil {
				if at, parseErr := time.Parse(time.RFC3339Nano, key.GetDataTime()); parseErr == nil {
					present[fmt.Sprintf("%s\x00%s\x00%d", key.GetSubjectId(), key.GetSeriesTag(), at.Unix())] = struct{}{}
				}
			}
		}
	}
	for _, bucket := range buckets {
		for _, item := range series {
			if _, ok := present[fmt.Sprintf("%s\x00%s\x00%d", item.SubjectID, item.SeriesTag, bucket.Key.Unix())]; !ok {
				if out[bucket.Key.Unix()] == nil {
					out[bucket.Key.Unix()] = Members{}
				}
				out[bucket.Key.Unix()][item.SeriesTag] = append(out[bucket.Key.Unix()][item.SeriesTag], item.SubjectID)
			}
		}
	}
	return out, nil
}
