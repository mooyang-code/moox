package observability

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type datasetsStub []string

func (s datasetsStub) ListArchivableDatasets(context.Context) ([]string, error) { return s, nil }

func archiveChecks(t *testing.T, datasets []string, seed func(db *gorm.DB, now time.Time)) map[string]BusinessStatus {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) { seed(db, now) })
	builder := Builder{Metrics: query, Now: func() time.Time { return now }, Datasets: datasetsStub(datasets)}
	overview, err := builder.Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]BusinessStatus{}
	for _, item := range overview.BusinessChecks {
		if item.Kind == "archive_task" || item.Kind == "archive_coverage" {
			out[item.Kind+":"+item.Module] = item
		}
	}
	return out
}

// 任务“异常”时告警、“需关注”只写原因；有限保留期的时序数据集没有归档任务时“未归档的数据集”告警。
func TestOverviewReportsArchiveTaskHealthAndCoverage(t *testing.T) {
	entries := []string{"crypto/ds_spot", "crypto/ds_swap"}
	checks := archiveChecks(t, entries, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "archive-state", "archive-mgr", "archive-mgr@control", "moox_archive_worker_presence_state", `{"worker":"archive-worker@m2"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "task-spot", "archive-mgr", "archive-mgr@control", "moox_archive_task_health", `{"space":"crypto","dataset":"ds_spot"}`, 2, now)
	})
	if spot := checks["archive_task:ds_spot"]; spot.Status != "down" || spot.SpaceID != "crypto" {
		t.Fatalf("archive task = %+v", spot)
	}
	coverage := checks["archive_coverage:archive"]
	if coverage.Status != "down" || !strings.Contains(coverage.Reason, "crypto/ds_swap") || strings.Contains(coverage.Reason, "ds_spot") {
		t.Fatalf("archive coverage = %+v", coverage)
	}
	checks = archiveChecks(t, entries[:1], func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "archive-state", "archive-mgr", "archive-mgr@control", "moox_archive_worker_presence_state", `{"worker":"archive-worker@m2"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "task-spot", "archive-mgr", "archive-mgr@control", "moox_archive_task_health", `{"space":"crypto","dataset":"ds_spot"}`, 1, now)
	})
	if spot := checks["archive_task:ds_spot"]; spot.Status != "healthy" || !strings.Contains(spot.Reason, "需关注") {
		t.Fatalf("warning should not alert: %+v", spot)
	}
	if coverage := checks["archive_coverage:archive"]; coverage.Status != "healthy" {
		t.Fatalf("archive coverage = %+v", coverage)
	}
}

// archive-mgr 没有部署（从没上报在线状态）或停止上报（在线状态序列已过期）时两种检查都不生成，不能把它的故障报成“全部未归档”。
func TestOverviewSkipsArchiveChecksWithoutArchiveMgr(t *testing.T) {
	checks := archiveChecks(t, []string{"crypto/ds_spot"}, func(*gorm.DB, time.Time) {})
	if len(checks) != 0 {
		t.Fatalf("checks = %+v", checks)
	}
	checks = archiveChecks(t, []string{"crypto/ds_spot"}, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "archive-state", "archive-mgr", "archive-mgr@control", "moox_archive_worker_presence_state", `{"worker":"archive-worker@m2"}`, 1, now.Add(-time.Hour))
		seedOverviewMetricForInstance(t, db, "task-spot", "archive-mgr", "archive-mgr@control", "moox_archive_task_health", `{"space":"crypto","dataset":"ds_spot"}`, 0, now.Add(-time.Hour))
	})
	if len(checks) != 0 {
		t.Fatalf("archive-mgr 停止上报时不应生成检查: %+v", checks)
	}
}
