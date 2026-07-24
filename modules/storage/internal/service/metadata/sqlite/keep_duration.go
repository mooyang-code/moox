package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrDatasetKeepDurationShorterThanView = errors.New("dataset keep_duration must be 0 or not shorter than view keep_duration")

func keepDurationCovers(datasetKeep, viewKeep string) (bool, error) {
	datasetKeep = strings.TrimSpace(datasetKeep)
	viewKeep = strings.TrimSpace(viewKeep)
	if datasetKeep == "0" {
		return true, nil
	}
	if viewKeep == "0" {
		return false, nil
	}
	datasetDuration, err := time.ParseDuration(datasetKeep)
	if err != nil {
		return false, fmt.Errorf("parse dataset keep_duration %q: %w", datasetKeep, err)
	}
	viewDuration, err := time.ParseDuration(viewKeep)
	if err != nil {
		return false, fmt.Errorf("parse view keep_duration %q: %w", viewKeep, err)
	}
	return datasetDuration >= viewDuration, nil
}

func validateViewKeepDuration(ctx context.Context, tx *sql.Tx, spaceID, viewID, viewKeep, primaryDatasetID string, datasetIDs []string) error {
	seen := make(map[string]struct{}, len(datasetIDs)+1)
	allDatasetIDs := append([]string{primaryDatasetID}, datasetIDs...)
	for _, datasetID := range allDatasetIDs {
		datasetID = strings.TrimSpace(datasetID)
		if datasetID == "" {
			continue
		}
		if _, ok := seen[datasetID]; ok {
			continue
		}
		seen[datasetID] = struct{}{}

		var datasetKeep string
		err := tx.QueryRowContext(ctx, `
			SELECT c_keep_duration
			FROM t_datasets
			WHERE c_space_id = ? AND c_dataset_id = ?
		`, spaceID, datasetID).Scan(&datasetKeep)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("dataset %s/%s does not exist", spaceID, datasetID)
		}
		if err != nil {
			return err
		}
		covers, err := keepDurationCovers(datasetKeep, viewKeep)
		if err != nil {
			return err
		}
		if !covers {
			return fmt.Errorf(
				"%w: dataset %s keep_duration %s is shorter than view %s keep_duration %s",
				ErrDatasetKeepDurationShorterThanView, datasetID, datasetKeep, viewID, viewKeep,
			)
		}
	}
	return nil
}

func validateDatasetKeepDuration(ctx context.Context, tx *sql.Tx, spaceID, datasetID, datasetKeep string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT c_view_id, c_keep_duration
		FROM t_views
		WHERE c_space_id = ?
		  AND (
			c_primary_dataset_id = ?
			OR EXISTS (
				SELECT 1
				FROM json_each(t_views.c_dataset_ids_json) dataset_ref
				WHERE dataset_ref.value = ?
			)
		  )
	`, spaceID, datasetID, datasetID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var viewID, viewKeep string
		if err := rows.Scan(&viewID, &viewKeep); err != nil {
			return err
		}
		covers, err := keepDurationCovers(datasetKeep, viewKeep)
		if err != nil {
			return err
		}
		if !covers {
			return fmt.Errorf(
				"%w: dataset %s keep_duration %s is shorter than view %s keep_duration %s",
				ErrDatasetKeepDurationShorterThanView, datasetID, datasetKeep, viewID, viewKeep,
			)
		}
	}
	return rows.Err()
}
