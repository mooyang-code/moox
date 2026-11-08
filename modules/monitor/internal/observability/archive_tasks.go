package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/report"
)

// maxArchiveTaskSeries 是一次读取归档任务健康序列的上限（指标目录一次最多返回 500 条；单用户系统的任务数远小于它）。
const maxArchiveTaskSeries = 500

// ArchivableDatasets 列出应当有归档任务的数据集（"空间/数据集 ID"）：已激活、有限保留期的时序数据集。
type ArchivableDatasets interface {
	ListArchivableDatasets(ctx context.Context) ([]string, error)
}

// ArchiveTaskCheckName 是一个归档任务检查的名称。
func ArchiveTaskCheckName(datasetID string) string { return "归档任务 " + datasetID }

// ArchiveCoverageCheckName 是“未归档的数据集”检查的名称。
const ArchiveCoverageCheckName = "未归档的数据集"

// buildArchiveHealth 把 archive-mgr 上报的任务健康等级转成检查：每个任务一项，“异常”时失败（“需关注”只写在原因里，不告警）；
// 另有一项比对 Storage 的数据集清单，有限保留期的时序数据集还没有归档任务时失败。
// archive-mgr 没有部署或停止上报（没有新鲜的 worker 在线状态序列）时两类都不生成，免得把它的故障报成“全部未归档”；它自身的故障由服务检查告警。
func (b Builder) buildArchiveHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if spaceID != "" && spaceID != monmetrics.InternalMetricSpaceID {
		return nil, nil
	}
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	presence, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", "", "moox_archive"+report.PresenceStateSuffix, "", 100, now)
	if err != nil {
		return nil, err
	}
	reporting := false
	for _, item := range presence {
		if !item.IsStale {
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
	var out []BusinessStatus
	archived := map[string]bool{}
	for _, item := range series {
		if item.IsStale {
			continue // 任务已删除
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, err
		}
		if latest == nil {
			continue
		}
		space, dataset := labelValue(item.LabelsJSON, "space"), labelValue(item.LabelsJSON, "dataset")
		if space == "" || dataset == "" {
			continue
		}
		archived[space+"/"+dataset] = true
		status, reason := "healthy", "归档正常"
		switch int(latest.Value) {
		case report.ArchiveTaskHealthError:
			status, reason = "down", "归档任务异常：用 moox-cli archive tasks list 查看原因"
		case report.ArchiveTaskHealthWarning:
			reason = "归档任务需关注：用 moox-cli archive tasks list 查看原因"
		case report.ArchiveTaskHealthPaused:
			reason = "归档任务已停用"
		}
		out = append(out, BusinessStatus{SpaceID: space, Kind: "archive_task", Module: dataset, Status: status, Reason: reason, LastCheckedAt: now})
	}
	if b.Datasets == nil {
		return out, nil
	}
	datasets, err := b.Datasets.ListArchivableDatasets(ctx)
	if err != nil {
		return out, nil // 数据集清单暂时拿不到时不判断覆盖情况
	}
	var missing []string
	for _, key := range datasets {
		if !archived[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	coverage := BusinessStatus{SpaceID: monmetrics.InternalMetricSpaceID, Kind: "archive_coverage", Module: "archive", Status: "healthy",
		Reason: "有限保留期的时序数据集都有归档任务", LastCheckedAt: now}
	if len(missing) > 0 {
		shown := missing
		if len(shown) > 5 {
			shown = shown[:5]
		}
		coverage.Status = "down"
		coverage.Reason = fmt.Sprintf("%d 个有限保留期的时序数据集还没有归档任务（%s），线上过保留期的数据会被删除：在“数据归档 › 归档任务”页或用 moox-cli archive tasks create 建任务",
			len(missing), strings.Join(shown, "、"))
	}
	return append(out, coverage), nil
}
