// Package store owns Strategy persistence adapters.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
)

type Store struct {
	db             *gorm.DB
	legacyCompiled sync.Map
	processed      sync.Map
}

func New(db *gorm.DB) *Store { return &Store{db: db} }

type strategyRow struct {
	ID           string `gorm:"column:strategy_id"`
	StrategyName string `gorm:"column:strategy_name"`
	DSLYaml      string `gorm:"column:dsl_yaml"`
	CreatedAt    int64  `gorm:"column:created_at"`
	UpdatedAt    int64  `gorm:"column:updated_at"`
}

func (r strategyRow) domain() domain.Strategy {
	sum := sha256.Sum256([]byte(r.DSLYaml))
	return domain.Strategy{
		ID: r.ID, Name: r.StrategyName, ManifestYAML: r.DSLYaml,
		Kind: "coin_selection", SourceHash: hex.EncodeToString(sum[:]),
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func stringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
