package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// retentionDeleteRows bounds one retention delete. Monitor's SQLite has a
// single connection, so a large cleanup runs as short statements that let
// checks, alerts and metric ingest in between instead of one long delete.
const retentionDeleteRows = 2000

// DeleteBefore removes the rows of table whose column is before cutoff, in
// batches of at most retentionDeleteRows. column must be indexed.
func DeleteBefore(ctx context.Context, db *gorm.DB, table, column string, cutoff time.Time) (int64, error) {
	query := fmt.Sprintf("DELETE FROM %[1]s WHERE c_id IN (SELECT c_id FROM %[1]s WHERE %[2]s < ? ORDER BY %[2]s LIMIT ?)", table, column)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		result := db.WithContext(ctx).Exec(query, cutoff, retentionDeleteRows)
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
		if result.RowsAffected < retentionDeleteRows {
			return total, nil
		}
	}
}
