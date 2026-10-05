package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

type factorDefRow struct {
	FactorID             string    `gorm:"column:c_factor_id"`
	Name                 string    `gorm:"column:c_name"`
	FactorType           string    `gorm:"column:c_factor_type"`
	SourceCode           string    `gorm:"column:c_source_code"`
	SourceHash           string    `gorm:"column:c_source_hash"`
	InputColumnsJSON     string    `gorm:"column:c_input_columns_json"`
	OutputsJSON          string    `gorm:"column:c_outputs_json"`
	ParamsJSON           string    `gorm:"column:c_params_json"`
	LookbackPeriods      int       `gorm:"column:c_lookback_periods"`
	AllowPartialUniverse int       `gorm:"column:c_allow_partial_universe"`
	CreatedAt            time.Time `gorm:"column:c_ctime"`
	UpdatedAt            time.Time `gorm:"column:c_mtime"`
}

func (r factorDefRow) domain() (domain.FactorDef, error) {
	var inputs, outputs []string
	if err := json.Unmarshal([]byte(r.InputColumnsJSON), &inputs); err != nil {
		return domain.FactorDef{}, fmt.Errorf("decode factor input columns: %w", err)
	}
	if err := json.Unmarshal([]byte(r.OutputsJSON), &outputs); err != nil {
		return domain.FactorDef{}, fmt.Errorf("decode factor outputs: %w", err)
	}
	if inputs == nil {
		inputs = []string{}
	}
	if outputs == nil {
		outputs = []string{}
	}
	return domain.FactorDef{
		FactorID: r.FactorID, Name: r.Name, FactorType: r.FactorType,
		SourceCode: r.SourceCode, SourceHash: r.SourceHash, InputColumns: inputs,
		Outputs: outputs, ParamsJSON: r.ParamsJSON, LookbackPeriods: r.LookbackPeriods,
		AllowPartialUniverse: r.AllowPartialUniverse != 0,
		CreatedAt:            r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateFactor creates a standalone definition.
func (s *Store) CreateFactor(ctx context.Context, def domain.FactorDef) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return insertFactor(tx, def)
	})
}

// CreateFactors inserts one catalog import atomically.
func (s *Store) CreateFactors(ctx context.Context, defs []domain.FactorDef) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, def := range defs {
			if err := insertFactor(tx, def); err != nil {
				return fmt.Errorf("insert factor %q: %w", def.FactorID, err)
			}
		}
		return nil
	})
}

func insertFactor(tx *gorm.DB, def domain.FactorDef) error {
	def.FactorID = strings.TrimSpace(def.FactorID)
	if def.FactorID == "" {
		return fmt.Errorf("factor id is required")
	}
	if def.ParamsJSON == "" {
		def.ParamsJSON = "{}"
	}
	inputsJSON, err := marshalStringSlice(def.InputColumns)
	if err != nil {
		return err
	}
	outputsJSON, err := marshalStringSlice(def.Outputs)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if def.CreatedAt.IsZero() {
		def.CreatedAt = now
	}
	def.UpdatedAt = now
	partial := 0
	if def.AllowPartialUniverse {
		partial = 1
	}
	err = tx.Exec(`
		INSERT INTO t_factor_defs
		(c_factor_id, c_name, c_factor_type, c_source_code, c_source_hash,
		 c_input_columns_json, c_outputs_json, c_params_json, c_lookback_periods,
		 c_allow_partial_universe, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, def.FactorID, def.Name, def.FactorType, def.SourceCode, def.SourceHash,
		inputsJSON, outputsJSON, def.ParamsJSON, def.LookbackPeriods, partial,
		def.CreatedAt, def.UpdatedAt).Error
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("%w: factor id already exists", ErrConflict)
		}
		return err
	}
	return nil
}

// UpdateFactor rewrites a definition while no enabled member references it.
// The NOT EXISTS guard runs in the same statement as the update, so a member
// enabled concurrently cannot slip past the check.
func (s *Store) UpdateFactor(ctx context.Context, def domain.FactorDef) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	def.FactorID = strings.TrimSpace(def.FactorID)
	if def.FactorID == "" {
		return fmt.Errorf("factor id is required")
	}
	if def.ParamsJSON == "" {
		def.ParamsJSON = "{}"
	}
	inputsJSON, err := marshalStringSlice(def.InputColumns)
	if err != nil {
		return err
	}
	outputsJSON, err := marshalStringSlice(def.Outputs)
	if err != nil {
		return err
	}
	partial := 0
	if def.AllowPartialUniverse {
		partial = 1
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_defs SET c_name = ?, c_factor_type = ?, c_source_code = ?, c_source_hash = ?,
		 c_input_columns_json = ?, c_outputs_json = ?, c_params_json = ?, c_lookback_periods = ?,
		 c_allow_partial_universe = ?, c_mtime = ?
		WHERE c_factor_id = ?
		 AND NOT EXISTS (SELECT 1 FROM t_factor_set_members WHERE c_factor_id = ? AND c_status = ?)
	`, def.Name, def.FactorType, def.SourceCode, def.SourceHash, inputsJSON, outputsJSON,
		def.ParamsJSON, def.LookbackPeriods, partial, time.Now().UTC(), def.FactorID,
		def.FactorID, domain.MemberStatusEnabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		if _, err := s.GetFactor(ctx, def.FactorID); err != nil {
			return err
		}
		return fmt.Errorf("%w: factor %q is used by an enabled member; disable it before updating", ErrConflict, def.FactorID)
	}
	return nil
}

// GetFactor returns one definition by id.
func (s *Store) GetFactor(ctx context.Context, factorID string) (domain.FactorDef, error) {
	if s == nil || s.db == nil {
		return domain.FactorDef{}, fmt.Errorf("factor database is not open")
	}
	row, err := s.getFactorRow(ctx, strings.TrimSpace(factorID))
	if err != nil {
		return domain.FactorDef{}, err
	}
	return row.domain()
}

// ListFactors returns every definition in factor id order.
func (s *Store) ListFactors(ctx context.Context) ([]domain.FactorDef, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var rows []factorDefRow
	if err := s.db.WithContext(ctx).Raw(`SELECT ` + factorDefColumns + ` FROM t_factor_defs ORDER BY c_factor_id`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return factorDefRowsToDomain(rows)
}

// DeleteFactor removes one definition that no set member references.
func (s *Store) DeleteFactor(ctx context.Context, factorID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	factorID = strings.TrimSpace(factorID)
	result := s.db.WithContext(ctx).Exec("DELETE FROM t_factor_defs WHERE c_factor_id = ?", factorID)
	if result.Error != nil {
		if isForeignKeyConstraint(result.Error) {
			return fmt.Errorf("%w: factor %q is still referenced by factor set members", ErrConflict, factorID)
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

const factorDefColumns = `c_factor_id, c_name, c_factor_type, c_source_code, c_source_hash,
	 c_input_columns_json, c_outputs_json, c_params_json, c_lookback_periods,
	 c_allow_partial_universe, c_ctime, c_mtime`

func factorDefRowsToDomain(rows []factorDefRow) ([]domain.FactorDef, error) {
	defs := make([]domain.FactorDef, 0, len(rows))
	for _, row := range rows {
		def, err := row.domain()
		if err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func (s *Store) getFactorRow(ctx context.Context, factorID string) (factorDefRow, error) {
	var row factorDefRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT `+factorDefColumns+` FROM t_factor_defs WHERE c_factor_id = ?
	`, factorID).Scan(&row)
	if result.Error != nil {
		return factorDefRow{}, result.Error
	}
	if result.RowsAffected == 0 {
		return factorDefRow{}, gorm.ErrRecordNotFound
	}
	return row, nil
}
