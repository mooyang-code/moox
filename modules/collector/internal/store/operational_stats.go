package store

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const MaxOperationalStatsRows = 100000

type OperationalRowCount struct {
	Count  int64
	Capped bool
}

// OperationalStats contains only Space-level aggregates, never task/subject identities.
type OperationalStats struct {
	DBBytes, WALBytes int64
	Rows              map[string]OperationalRowCount
}

var operationalTables = map[string]string{
	"instances":     "t_collector_task_instances",
	"write_targets": "t_collector_instance_write_targets",
}

// OperationalStats caps each count scan. Capped counts are explicit lower bounds;
// callers also bound the entire observation with a context deadline.
func (s *Store) OperationalStats(ctx context.Context, spaceID string, limit int) (OperationalStats, error) {
	stats := OperationalStats{Rows: make(map[string]OperationalRowCount, len(operationalTables))}
	if s == nil || s.db == nil || strings.TrimSpace(spaceID) == "" {
		return stats, fmt.Errorf("Collector store and space_id are required")
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
