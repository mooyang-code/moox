package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"gorm.io/gorm"
)

type datasetsStub []string

func (s datasetsStub) ListArchivableDatasets(context.Context) ([]string, error) { return s, nil }

func archiveChecks(t *testing.T, datasets []string, seed func(db *gorm.DB, now time.Time)) map[string]BusinessStatus {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) { seed(db, now) })
	builder := Builder{Metrics: query, Now: func() time.Time { return now }, Datasets: datasetsStub(datasets)}
	overview, err := builder.buildForTest(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]BusinessStatus{}
	for _, item := range overview.BusinessChecks {
		if item.Kind == "archive_coverage" {
			out[item.Kind+":"+item.Module] = item
		}
	}
	return out
}

// seedArchiveMgr 写一条 archive-mgr 的服务上报（seenAt 决定是否新鲜）。
func seedArchiveMgr(t *testing.T, db *gorm.DB, seenAt time.Time) {
	t.Helper()
	if err := db.Create(&monmetrics.MetricService{ServiceName: "archive-mgr", InstanceID: "archive-mgr@control", NodeID: "control", LastSeenAt: seenAt}).Error; err != nil {
		t.Fatal(err)
	}
}

type failingDatasets struct{}

func (failingDatasets) ListArchivableDatasets(context.Context) ([]string, error) {
	return nil, errors.New("storage unavailable")
}

// 有限保留期的时序数据集没有归档任务时“未归档的数据集”告警；任务清单取 moox_archive_task_health 的标签，健康等级不在这里判断。
func TestOverviewReportsArchiveCoverage(t *testing.T) {
	entries := []string{"crypto/ds_spot", "crypto/ds_swap"}
	checks := archiveChecks(t, entries, func(db *gorm.DB, now time.Time) {
		seedArchiveMgr(t, db, now)
		seedOverviewMetricForInstance(t, db, "task-spot", "archive-mgr", "archive-mgr@control", "moox_archive_task_health", `{"space":"crypto","dataset":"ds_spot"}`, 2, now)
	})
	coverage := checks["archive_coverage:archive"]
	if coverage.Status != "down" || coverage.CheckID != "archive_coverage:archive" || !strings.Contains(coverage.Reason, "crypto/ds_swap") || strings.Contains(coverage.Reason, "ds_spot") {
		t.Fatalf("archive coverage = %+v", coverage)
	}
	checks = archiveChecks(t, entries[:1], func(db *gorm.DB, now time.Time) {
		seedArchiveMgr(t, db, now)
		seedOverviewMetricForInstance(t, db, "task-spot", "archive-mgr", "archive-mgr@control", "moox_archive_task_health", `{"space":"crypto","dataset":"ds_spot"}`, 1, now)
	})
	if coverage := checks["archive_coverage:archive"]; coverage.Status != "healthy" {
		t.Fatalf("archive coverage = %+v", coverage)
	}
}

// archive-mgr 没有部署或停止上报时不生成，不能把它的故障报成“全部未归档”；Storage 数据集清单读不到时冻结。
func TestOverviewArchiveCoverageWithoutArchiveMgrOrDatasets(t *testing.T) {
	if checks := archiveChecks(t, []string{"crypto/ds_spot"}, func(*gorm.DB, time.Time) {}); len(checks) != 0 {
		t.Fatalf("checks = %+v", checks)
	}
	checks := archiveChecks(t, []string{"crypto/ds_spot"}, func(db *gorm.DB, now time.Time) { seedArchiveMgr(t, db, now.Add(-time.Hour)) })
	if len(checks) != 0 {
		t.Fatalf("archive-mgr 停止上报时不应生成检查: %+v", checks)
	}
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) { seedArchiveMgr(t, db, now) })
	items, err := (Builder{Metrics: query, Now: func() time.Time { return now }, Datasets: failingDatasets{}}).BusinessChecks(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Frozen || items[0].CheckID != "archive_coverage:archive" {
		t.Fatalf("数据集清单读不到时应冻结: %+v", items)
	}
}
