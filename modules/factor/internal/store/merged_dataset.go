package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

// StorageDatasetClient creates or returns an existing Storage Dataset.
// A lost success response must be retried by Dataset ID, not by creating a
// second Dataset.
type StorageDatasetClient interface {
	EnsureDataset(context.Context, domain.MergedDataset) (string, error)
}

// MergedDatasetRepository persists mdataset definitions and snapshots.
type MergedDatasetRepository struct {
	db *gorm.DB
}

func NewMergedDatasetRepository(db *gorm.DB) *MergedDatasetRepository {
	return &MergedDatasetRepository{db: db}
}

func (r *MergedDatasetRepository) Save(ctx context.Context, def domain.MergedDataset) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("merged dataset repository is not open")
	}
	encoded, err := domain.EncodeMergedDatasetConfig(def)
	if err != nil {
		return err
	}
	existing, err := r.Get(ctx, encoded.DatasetID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := domain.ValidateMergedDataset(encoded); err != nil {
			return err
		}
		encoded.Enabled = false
		encoded.ConfigSnapshotID = ""
		encoded.StorageResourceState = domain.StorageResourcePending
		encoded.StorageSchemaID = ""
		return r.db.WithContext(ctx).Create(&encoded).Error
	}
	if err != nil {
		return err
	}
	if err := domain.ValidateMergedDatasetUpdate(existing, encoded); err != nil {
		return err
	}
	encoded.InputSemanticsHash = domain.InputSemanticsHash(encoded)
	return r.db.WithContext(ctx).Model(&domain.MergedDataset{}).Where("c_dataset_id = ?", encoded.DatasetID).Updates(map[string]any{
		"c_space_id": encoded.SpaceID, "c_frequency": encoded.Frequency,
		"c_key_contract_json": encoded.KeyContractJSON, "c_object_set_json": encoded.ObjectSetJSON,
		"c_sources_json": encoded.SourcesJSON, "c_merge_mode": encoded.MergeMode,
		"c_field_mappings_json": encoded.FieldMappingsJSON, "c_input_semantics_hash": encoded.InputSemanticsHash,
		"c_mtime": time.Now().UTC(),
	}).Error
}

func (r *MergedDatasetRepository) Enable(ctx context.Context, datasetID string) error {
	return r.EnableSnapshot(ctx, datasetID, "")
}

func (r *MergedDatasetRepository) EnableSnapshot(ctx context.Context, datasetID, snapshotID string) error {
	current, err := r.Get(ctx, datasetID)
	if err != nil {
		return err
	}
	currentSnapshot := strings.TrimSpace(current.ConfigSnapshotID)
	wanted := strings.TrimSpace(snapshotID)
	if current.Enabled && currentSnapshot != "" {
		if wanted == "" || wanted == currentSnapshot {
			return nil
		}
		return fmt.Errorf("mdataset %s already enabled with snapshot %s", datasetID, currentSnapshot)
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if wanted == "" {
		sum := sha256.Sum256(append(append([]byte(current.DatasetID), 0), raw...))
		wanted = hex.EncodeToString(sum[:16])
	}
	snapshot := domain.MergedDatasetSnapshot{
		SnapshotID: wanted, DatasetID: current.DatasetID,
		ConfigJSON: string(raw), InputSemanticsHash: current.InputSemanticsHash,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}
		return tx.Model(&domain.MergedDataset{}).Where("c_dataset_id = ?", datasetID).Updates(map[string]any{
			"c_enabled": true, "c_config_snapshot_id": snapshot.SnapshotID, "c_mtime": time.Now().UTC(),
		}).Error
	})
}

func (r *MergedDatasetRepository) Get(ctx context.Context, datasetID string) (domain.MergedDataset, error) {
	if r == nil || r.db == nil {
		return domain.MergedDataset{}, fmt.Errorf("merged dataset repository is not open")
	}
	var row domain.MergedDataset
	if err := r.db.WithContext(ctx).Where("c_dataset_id = ?", strings.TrimSpace(datasetID)).First(&row).Error; err != nil {
		return domain.MergedDataset{}, err
	}
	return domain.DecodeMergedDataset(row)
}

func (r *MergedDatasetRepository) ListSnapshots(ctx context.Context, datasetID string) ([]domain.MergedDatasetSnapshot, error) {
	var rows []domain.MergedDatasetSnapshot
	if err := r.db.WithContext(ctx).Where("c_dataset_id = ?", strings.TrimSpace(datasetID)).Order("c_ctime ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *MergedDatasetRepository) ReconcileStorage(ctx context.Context, datasetID string, client StorageDatasetClient) error {
	if client == nil {
		return fmt.Errorf("storage dataset client is required")
	}
	current, err := r.Get(ctx, datasetID)
	if err != nil {
		return err
	}
	if current.StorageResourceState == domain.StorageResourceCreated && strings.TrimSpace(current.StorageSchemaID) != "" {
		return nil
	}
	schemaID, err := client.EnsureDataset(ctx, current)
	if err != nil {
		_ = r.db.WithContext(ctx).Model(&domain.MergedDataset{}).Where("c_dataset_id = ?", datasetID).Updates(map[string]any{
			"c_storage_resource_state": domain.StorageResourceFailed, "c_mtime": time.Now().UTC(),
		}).Error
		return err
	}
	if strings.TrimSpace(schemaID) == "" {
		return fmt.Errorf("storage schema identity is required")
	}
	return r.db.WithContext(ctx).Model(&domain.MergedDataset{}).Where("c_dataset_id = ?", datasetID).Updates(map[string]any{
		"c_storage_resource_state": domain.StorageResourceCreated,
		"c_storage_schema_id":      schemaID,
		"c_mtime":                  time.Now().UTC(),
	}).Error
}
