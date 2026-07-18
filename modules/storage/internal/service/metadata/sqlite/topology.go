package sqlite

import (
	"context"
	"errors"
	"fmt"
)

// LockDatasetTopology records the first successful write for a dataset. It is
// idempotent and runs in the same single-writer SQLite connection as route
// updates, so placement cannot change between the check and the insert.
func (s *Store) LockDatasetTopology(ctx context.Context, spaceID, datasetID string) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	if spaceID == "" || datasetID == "" {
		return errors.New("space_id and dataset_id are required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO t_dataset_topology_locks (c_space_id, c_dataset_id)
		VALUES (?, ?)
		ON CONFLICT(c_space_id, c_dataset_id) DO NOTHING
	`, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("lock dataset topology: %w", err)
	}
	return nil
}

func (s *Store) datasetTopologyLocked(ctx context.Context, spaceID, datasetID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM t_dataset_topology_locks
		WHERE c_space_id = ? AND c_dataset_id = ?
	`, spaceID, datasetID).Scan(&count)
	return count > 0, err
}
