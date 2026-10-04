package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

const MaxOperationalStatsRows = 100000

type OperationalRowCount struct {
	Count  int64
	Capped bool
}

// OperationalStats contains only Space-level aggregates, never task/subject identities.
type OperationalStats struct {
	DBBytes, WALBytes              int64
	Rows                           map[string]OperationalRowCount
	OldestPendingRetry             time.Time
	OldestDispatchedRetry          time.Time
	OldestWaitingPeriod            time.Time
	OldestSnapshotCleanupCandidate time.Time
}

var operationalTables = map[string]string{
	"runs": "t_collector_runs", "instances": "t_collector_task_instances",
	"write_targets": "t_collector_instance_write_targets", "batches": "t_collector_fetch_batches",
	"retry": "t_collector_fetch_retry_items", "period_snapshot": "t_collector_task_period_series",
}

// OperationalStats caps each count scan. Capped counts are explicit lower bounds;
// callers also bound the entire observation with a context deadline.
func (s *Store) OperationalStats(ctx context.Context, spaceID string, snapshotBefore time.Time, limit int) (OperationalStats, error) {
	stats := OperationalStats{Rows: make(map[string]OperationalRowCount, len(operationalTables))}
	if s == nil || s.db == nil || strings.TrimSpace(spaceID) == "" || snapshotBefore.IsZero() {
		return stats, fmt.Errorf("Collector store, space_id and snapshot cutoff are required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if limit <= 0 || limit > MaxOperationalStatsRows {
		limit = MaxOperationalStatsRows
	}
	var err error
	if stats.DBBytes, err = databaseFileBytes(s.path, false); err != nil {
		return stats, err
	}
	if stats.WALBytes, err = databaseFileBytes(s.path+"-wal", true); err != nil {
		return stats, err
	}
	db := s.db.WithContext(ctx)
	for kind, table := range operationalTables {
		var count int64
		if err = db.Raw("SELECT COUNT(*) FROM (SELECT 1 FROM "+table+" WHERE c_space_id = ? LIMIT ?)", spaceID, limit+1).Scan(&count).Error; err != nil {
			return stats, fmt.Errorf("observe Collector %s rows: %w", kind, err)
		}
		stats.Rows[kind] = OperationalRowCount{Count: min(count, int64(limit)), Capped: count > int64(limit)}
	}
	for _, retry := range []struct {
		status string
		oldest *time.Time
	}{{"pending", &stats.OldestPendingRetry}, {"dispatched", &stats.OldestDispatchedRetry}} {
		var rows []struct {
			Time time.Time `gorm:"column:c_ctime"`
		}
		if err = db.Table("t_collector_fetch_retry_items").Select("c_ctime").Where("c_space_id = ? AND c_status = ?", spaceID, retry.status).Order("c_ctime").Limit(1).Scan(&rows).Error; err != nil {
			return stats, fmt.Errorf("observe oldest %s retry: %w", retry.status, err)
		}
		if len(rows) > 0 {
			*retry.oldest = rows[0].Time.UTC()
		}
	}
	var periods []struct {
		Time time.Time `gorm:"column:c_period_time"`
	}
	if err = db.Table("t_collector_period_storage_states").Select("c_period_time").Where("c_space_id = ? AND c_status = 'waiting'", spaceID).Order("c_period_time").Limit(1).Scan(&periods).Error; err != nil {
		return stats, fmt.Errorf("observe oldest waiting period: %w", err)
	}
	if len(periods) > 0 {
		stats.OldestWaitingPeriod = periods[0].Time.UTC()
	}
	candidates, err := listTerminalPeriodCleanupCandidates(db, spaceID, snapshotBefore.UTC(), nil, 1)
	if err != nil {
		return stats, fmt.Errorf("observe oldest snapshot cleanup candidate: %w", err)
	}
	if len(candidates) > 0 {
		stats.OldestSnapshotCleanupCandidate = candidates[0].Key.PeriodTime
	}
	return stats, nil
}

func databaseFileBytes(path string, absentOK bool) (int64, error) {
	info, err := os.Stat(path)
	if absentOK && os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("observe Collector database file: %w", err)
	}
	return info.Size(), nil
}
