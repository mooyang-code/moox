package observability

import (
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

func objectStoreCheck(t *testing.T, seed func(db *gorm.DB, now time.Time)) (BusinessStatus, bool) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) { seed(db, now) })
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range overview.BusinessChecks {
		if item.Kind == "archive_object_store" {
			return item, true
		}
	}
	return BusinessStatus{}, false
}

// 不可用超过 5 分钟：失败，原因带探测错误；probe_error 变化留下的旧序列被忽略。
func TestOverviewArchiveObjectStoreDown(t *testing.T) {
	check, ok := objectStoreCheck(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "os-old", "archive-mgr", "archive-mgr@control", report.ArchiveObjectStoreStateMetric, `{"worker":"archive-worker@m1","probe_error":""}`, 0, now.Add(-time.Minute))
		seedOverviewMetricForInstance(t, db, "os-new", "archive-mgr", "archive-mgr@control", report.ArchiveObjectStoreStateMetric, `{"worker":"archive-worker@m1","probe_error":"connection refused"}`, 2, now)
	})
	if !ok || check.Status != "down" || check.Module != "archive-worker" || !strings.Contains(check.Reason, "connection refused") || !strings.Contains(check.Reason, "超过 5 分钟") {
		t.Fatalf("object store check = %+v %v", check, ok)
	}
}

// 不可用未满 5 分钟只写在原因里；可用时通过；没有序列时不生成。
func TestOverviewArchiveObjectStoreUnavailableAndAvailable(t *testing.T) {
	check, _ := objectStoreCheck(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "os", "archive-mgr", "archive-mgr@control", report.ArchiveObjectStoreStateMetric, `{"worker":"archive-worker@m1","probe_error":"访问被拒绝"}`, 1, now)
	})
	if check.Status != "healthy" || !strings.Contains(check.Reason, "未满 5 分钟") || !strings.Contains(check.Reason, "访问被拒绝") {
		t.Fatalf("unavailable = %+v", check)
	}
	check, _ = objectStoreCheck(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "os", "archive-mgr", "archive-mgr@control", report.ArchiveObjectStoreStateMetric, `{"worker":"archive-worker@m1","probe_error":""}`, 0, now)
	})
	if check.Status != "healthy" || !strings.Contains(check.Reason, "可用") {
		t.Fatalf("available = %+v", check)
	}
	if _, ok := objectStoreCheck(t, func(*gorm.DB, time.Time) {}); ok {
		t.Fatal("没有序列时不应生成检查")
	}
}
