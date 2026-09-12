package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

var ErrCatalogRevisionRegression = errors.New("catalog revision regression")
var ErrCatalogRevisionConflict = errors.New("catalog revision has different content")

// CatalogSnapshot reads the revision, source definitions and bindings from one
// SQLite read transaction, including disabled and pending catalog entries.
func (s *Store) CatalogSnapshot(ctx context.Context) (*domain.CatalogSnapshot, error) {
	snapshot := &domain.CatalogSnapshot{Factors: []domain.FactorDef{}, Bindings: []domain.FactorBinding{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT c_revision FROM t_factor_catalog WHERE c_id = 1").Row().Scan(&snapshot.Revision); err != nil {
			return err
		}
		if err := tx.Order("c_factor_id").Find(&snapshot.Factors).Error; err != nil {
			return err
		}
		return tx.Order("c_binding_id").Find(&snapshot.Bindings).Error
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// ReplaceCatalogSnapshot atomically replaces only catalog tables. Revision
// equality is idempotent only for identical canonical payloads. The initial
// write acquires SQLite's writer lock before checking the current revision.
func (s *Store) ReplaceCatalogSnapshot(ctx context.Context, snapshot domain.CatalogSnapshot) (bool, error) {
	if snapshot.Revision <= 0 {
		return false, fmt.Errorf("catalog revision must be positive")
	}
	snapshot.Factors = append([]domain.FactorDef{}, snapshot.Factors...)
	snapshot.Bindings = append([]domain.FactorBinding{}, snapshot.Bindings...)
	sort.Slice(snapshot.Factors, func(i, j int) bool { return snapshot.Factors[i].FactorID < snapshot.Factors[j].FactorID })
	sort.Slice(snapshot.Bindings, func(i, j int) bool { return snapshot.Bindings[i].BindingID < snapshot.Bindings[j].BindingID })
	canonical := snapshot
	canonical.Factors = append([]domain.FactorDef{}, snapshot.Factors...)
	canonical.Bindings = append([]domain.FactorBinding{}, snapshot.Bindings...)
	// Local artifact paths and bookkeeping timestamps do not version the catalog.
	for i := range canonical.Factors {
		canonical.Factors[i].SourcePath = ""
		canonical.Factors[i].CreateTime = time.Time{}
		canonical.Factors[i].ModifyTime = time.Time{}
	}
	for i := range canonical.Bindings {
		canonical.Bindings[i].SourceDataset = ""
		canonical.Bindings[i].TargetDataset = ""
		canonical.Bindings[i].CreateTime = time.Time{}
		canonical.Bindings[i].ModifyTime = time.Time{}
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return false, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	changed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE t_factor_catalog SET c_revision = c_revision WHERE c_id = 1").Error; err != nil {
			return err
		}
		var revision int64
		var previousHash string
		if err := tx.Raw("SELECT c_revision, c_snapshot_hash FROM t_factor_catalog WHERE c_id = 1").Row().Scan(&revision, &previousHash); err != nil {
			return err
		}
		if snapshot.Revision < revision {
			return ErrCatalogRevisionRegression
		}
		if snapshot.Revision == revision {
			if hash != previousHash {
				return ErrCatalogRevisionConflict
			}
			return nil
		}
		if err := tx.Exec("DELETE FROM t_factor_bindings").Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM t_factor_defs").Error; err != nil {
			return err
		}
		if len(snapshot.Factors) > 0 {
			if err := tx.Create(&snapshot.Factors).Error; err != nil {
				return err
			}
		}
		if len(snapshot.Bindings) > 0 {
			if err := tx.Create(&snapshot.Bindings).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec("UPDATE t_factor_catalog SET c_revision = ?, c_snapshot_hash = ? WHERE c_id = 1", snapshot.Revision, hash).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed && err == nil, err
}
