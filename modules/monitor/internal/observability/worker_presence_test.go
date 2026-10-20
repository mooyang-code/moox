package observability

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func presenceChecks(t *testing.T, seed func(db *gorm.DB, now time.Time)) map[string]BusinessStatus {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) { seed(db, now) })
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]BusinessStatus{}
	for _, item := range overview.BusinessChecks {
		if item.Kind == "worker_presence" {
			out[item.Module] = item
		}
	}
	return out
}

// 因子引擎离线：检查失败，原因写明机器与最后心跳；归档“不需要”：通过；换机后旧标签的序列被忽略。
func TestOverviewReportsExternalWorkerPresence(t *testing.T) {
	checks := presenceChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "factor-old", "factor-mgr", "factor-mgr@control", "moox_factor_worker_presence_state", `{"worker":"factor-engine@old"}`, 1, now.Add(-time.Hour))
		seedOverviewMetricForInstance(t, db, "factor-state", "factor-mgr", "factor-mgr@control", "moox_factor_worker_presence_state", `{"worker":"factor-engine@m1"}`, 2, now)
		seedOverviewMetricForInstance(t, db, "factor-last", "factor-mgr", "factor-mgr@control", "moox_factor_worker_last_heartbeat_timestamp_seconds", `{"worker":"factor-engine@m1"}`, float64(now.Add(-5*time.Minute).Unix()), now)
		seedOverviewMetricForInstance(t, db, "archive-state", "archive-mgr", "archive-mgr@control", "moox_archive_worker_presence_state", `{"worker":""}`, 0, now)
	})
	factor, archive := checks["factor-engine"], checks["archive-worker"]
	if factor.Status != "down" || !strings.Contains(factor.Reason, "factor-engine@m1") || !strings.Contains(factor.Reason, "5 分钟") {
		t.Fatalf("factor engine presence = %+v", factor)
	}
	if archive.Status != "healthy" || !strings.Contains(archive.Reason, "没有需要") {
		t.Fatalf("archive worker presence = %+v", archive)
	}
}

// 从未收到心跳时提示检查运维机；在线时写明机器；没有部署管理端时不生成检查。
func TestOverviewWorkerPresenceNeverSeenAndOnline(t *testing.T) {
	checks := presenceChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "factor-state", "factor-mgr", "factor-mgr@control", "moox_factor_worker_presence_state", `{"worker":""}`, 2, now)
	})
	if factor := checks["factor-engine"]; factor.Status != "down" || !strings.Contains(factor.Reason, "从未收到心跳") {
		t.Fatalf("factor engine presence = %+v", factor)
	}
	if _, ok := checks["archive-worker"]; ok {
		t.Fatal("archive-mgr is not deployed; no check expected")
	}
	checks = presenceChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "archive-state", "archive-mgr", "archive-mgr@control", "moox_archive_worker_presence_state", `{"worker":"archive-worker@m2"}`, 1, now)
	})
	if archive := checks["archive-worker"]; archive.Status != "healthy" || !strings.Contains(archive.Reason, "archive-worker@m2 在线") {
		t.Fatalf("archive worker presence = %+v", archive)
	}
	if WorkerPresenceName("factor-engine") != "因子引擎在线状态" {
		t.Fatal(WorkerPresenceName("factor-engine"))
	}
}
