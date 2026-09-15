package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

func SeedMergedDatasets(ctx context.Context, db *store.Store, defs []domain.MergedDataset) error {
	if db == nil || db.MergedDatasets() == nil {
		return fmt.Errorf("merged dataset catalog is required")
	}
	if len(defs) == 0 {
		return fmt.Errorf("engine merged dataset definitions are required")
	}
	for _, def := range defs {
		snapshot := strings.TrimSpace(def.ConfigSnapshotID)
		if err := db.MergedDatasets().Save(ctx, def); err != nil {
			return err
		}
		if err := db.MergedDatasets().EnableSnapshot(ctx, def.DatasetID, snapshot); err != nil {
			return err
		}
	}
	return nil
}
