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

func validateViewKeepDuration(ctx context.Context, tx *sql.Tx, spaceID, viewID, viewKeep, datasetID string) error {
	datasetID = strings.TrimSpace(datasetID)
	if datasetID == "" {
		return errors.New("dataset_id is required")
	}
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
	return nil
}

func validateDatasetKeepDuration(ctx context.Context, tx *sql.Tx, spaceID, datasetID, datasetKeep string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT c_view_id, c_keep_duration
		FROM t_views
		WHERE c_space_id = ?
		  AND c_dataset_id = ?
	`, spaceID, datasetID)
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
