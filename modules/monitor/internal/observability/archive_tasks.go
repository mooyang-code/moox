package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
)

// maxArchiveTaskSeries 是一次读取归档任务健康序列的上限（指标目录一次最多返回 500 条；单用户系统的任务数远小于它）。
const maxArchiveTaskSeries = 500

// ArchivableDatasets 列出应当有归档任务的数据集（"空间/数据集 ID"）：已激活、有限保留期的时序数据集。
type ArchivableDatasets interface {
	ListArchivableDatasets(ctx context.Context) ([]string, error)
}

// ArchiveCoverageCheckName 是“未归档的数据集”检查的名称。
const ArchiveCoverageCheckName = "未归档的数据集"

// archiveCoverageCheckID 是“未归档的数据集”检查的 ID。
const archiveCoverageCheckID = "archive_coverage:archive"

// buildArchiveCoverage 比对 Storage 的数据集清单与 archive-mgr 上报的归档任务（moox_archive_task_health 的 space、dataset 标签，
// 只当任务清单用；任务健康由 archive-mgr 的关注项 task_failing 判断）：有限保留期的时序数据集还没有归档任务时失败。
// archive-mgr 没有部署或停止上报时不产出（停止上报时由评估按它的组件检查冻结），免得把它的故障报成“全部未归档”；
// Storage 数据集清单读不到时本轮冻结。
func (b Builder) buildArchiveCoverage(ctx context.Context, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil || b.Datasets == nil {
		return nil, nil
	}
	services, _, err := b.Metrics.Catalog().ListServicesAt(ctx, "", 0, 500, now)
	if err != nil {
		return nil, err
	}
	reporting := false
	for _, service := range services {
		if service.ServiceName == "archive-mgr" && !service.IsStale {
			reporting = true
			break
		}
	}
	if !reporting {
		return nil, nil
	}
	series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", report.ArchiveTaskHealthMetric, "", maxArchiveTaskSeries, now)
	if err != nil {
		return nil, err
	}
	archived := map[string]bool{}
	for _, item := range series {
		if item.IsStale {
			continue // 任务已删除
		}
		space, dataset := labelValue(item.LabelsJSON, "space"), labelValue(item.LabelsJSON, "dataset")
		if space != "" && dataset != "" {
			archived[space+"/"+dataset] = true
		}
	}
	coverage := BusinessStatus{SpaceID: events.SystemSpaceID, Kind: "archive_coverage", Module: "archive", CheckID: archiveCoverageCheckID, Name: ArchiveCoverageCheckName}
	datasets, err := b.Datasets.ListArchivableDatasets(ctx)
	if err != nil {
		// 数据集清单暂时拿不到时不判断覆盖情况：保持原状态，不能当成“不再检查”把已有告警恢复掉。
		coverage.Frozen = true
		return []BusinessStatus{coverage}, nil
	}
	var missing []string
	for _, key := range datasets {
		if !archived[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	coverage.Status, coverage.Reason, coverage.LastCheckedAt = "healthy", "有限保留期的时序数据集都有归档任务", now
	if len(missing) > 0 {
		shown := missing
		if len(shown) > 5 {
			shown = shown[:5]
		}
		coverage.Status = "down"
		coverage.Reason = fmt.Sprintf("%d 个有限保留期的时序数据集还没有归档任务（%s），线上过保留期的数据会被删除：在“数据归档 › 归档任务”页或用 moox-cli archive tasks create 建任务",
			len(missing), strings.Join(shown, "、"))
	}
	return []BusinessStatus{coverage}, nil
}
