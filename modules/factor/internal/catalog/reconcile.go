package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// Reconcile refreshes result columns for all enabled factor sets. Bootstrap
// owns when this method runs at startup and on the periodic interval.
func (s *Service) Reconcile(ctx context.Context) error {
	sets, err := s.db.ListSets(ctx)
	if err != nil {
		return err
	}
	var reconcileErrors []error
	for _, set := range sets {
		factors, err := s.db.ListFactors(ctx, set.SetID, "")
		if err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("list factors for set %q: %w", set.SetID, err))
		} else {
			unlock, lockErr := s.locks.LockContext(ctx, set.SetID)
			if lockErr != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("lock factor set %q: %w", set.SetID, lockErr))
				continue
			}
			for _, factor := range factors {
				if _, err := s.artifacts.Materialize(factor); err != nil {
					reconcileErrors = append(reconcileErrors, fmt.Errorf("restore factor source %q: %w", factor.FactorID, err))
				}
			}
			unlock()
		}
		if set.Status == domain.SetStatusDeleting {
			if err := s.DeleteSet(ctx, set.SetID, true); err != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("resume factor set purge %q: %w", set.SetID, err))
			}
			continue
		}
		if set.Status != domain.SetStatusEnabled {
			continue
		}
		if err := s.ReconcileSet(ctx, set.SetID); err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("reconcile factor set %q: %w", set.SetID, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (s *Service) ReconcileSet(ctx context.Context, setID string) error {
	unlock, err := s.locks.LockContext(ctx, strings.TrimSpace(setID))
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return err
	}
	if set.Status != domain.SetStatusEnabled {
		return nil
	}
	return s.reconcileSetUnlocked(ctx, set)
}

func (s *Service) reconcileSetUnlocked(ctx context.Context, set domain.FactorSet) error {
	source, columns, err := s.sourceDataset(ctx, set)
	if err != nil {
		return err
	}
	factors, err := s.db.ListFactors(ctx, set.SetID, domain.FactorStatusEnabled)
	if err != nil {
		return fmt.Errorf("list enabled factors: %w", err)
	}
	return s.ensureResultColumns(ctx, set, source, columns, factors...)
}

func (s *Service) ensureResultColumns(ctx context.Context, set domain.FactorSet, source storageio.DatasetInfo, sourceColumns []storageio.ColumnInfo, factors ...domain.FactorDef) error {
	if s.metadata == nil {
		return errors.New("Storage metadata is required")
	}
	result, err := s.metadata.GetDataset(ctx, set.SpaceID, set.ResultDatasetID)
	if err != nil {
		return fmt.Errorf("get factor result dataset: %w", err)
	}
	if result.SpaceID != set.SpaceID || result.DatasetID != set.ResultDatasetID ||
		result.DataSourceID != source.DataSourceID || result.DataNodeID != source.DataNodeID ||
		result.KeepDuration != source.KeepDuration || result.DataKind != storageio.DataKindTimeSeries ||
		!equalStrings(result.Freqs, []string{set.Freq}) || !equalStrings(result.SubjectTags, source.SubjectTags) ||
		result.Attributes["owner_module"] != "factor" || result.Attributes["dataset_role"] != storageio.DatasetRoleFactorResult ||
		result.Attributes["source_dataset_id"] != set.SourceDatasetID || result.Attributes["write_owner"] != "factor" {
		return fmt.Errorf("result dataset %q does not match the factor result contract", set.ResultDatasetID)
	}
	if result.Status != storageio.DatasetStatusActive {
		return fmt.Errorf("factor result dataset %q is not active", set.ResultDatasetID)
	}
	current, err := s.metadata.ListColumns(ctx, set.SpaceID, set.ResultDatasetID)
	if err != nil {
		return fmt.Errorf("list factor result columns: %w", err)
	}
	wanted := append([]storageio.ColumnInfo(nil), sourceColumns...)
	sourceNames := make(map[string]struct{}, len(sourceColumns))
	for _, col := range sourceColumns {
		sourceNames[col.ColumnName] = struct{}{}
	}
	for _, factor := range factors {
		for _, output := range factor.Outputs {
			if _, exists := sourceNames[output]; exists {
				return fmt.Errorf("source column %q collides with enabled factor output", output)
			}
		}
	}
	wanted = appendFactorColumns(wanted, factors...)
	known := make(map[string]storageio.ColumnInfo, len(current))
	for _, col := range current {
		known[col.ColumnName] = col
	}
	missing := make([]storageio.ColumnInfo, 0, len(wanted))
	wantedNames := make(map[string]struct{}, len(wanted))
	for _, col := range wanted {
		if _, duplicate := wantedNames[col.ColumnName]; duplicate {
			return fmt.Errorf("duplicate result column contract %q", col.ColumnName)
		}
		wantedNames[col.ColumnName] = struct{}{}
		if existing, ok := known[col.ColumnName]; ok {
			if !compatibleResultColumn(existing, col) {
				return fmt.Errorf("result column %q conflicts with the current source/factor contract", col.ColumnName)
			}
			continue
		}
		known[col.ColumnName] = col
		missing = append(missing, col)
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].ColumnName < missing[j].ColumnName })
	if err := s.metadata.UpsertColumns(ctx, set.SpaceID, set.ResultDatasetID, missing); err != nil {
		return fmt.Errorf("upsert factor result columns: %w", err)
	}
	return nil
}

func compatibleResultColumn(existing, wanted storageio.ColumnInfo) bool {
	if existing.OriginType != wanted.OriginType || existing.OriginID != wanted.OriginID || existing.ValueType != wanted.ValueType {
		return false
	}
	if wanted.OriginType == storageio.ColumnOriginFactor {
		return existing.Attributes["origin_factor_id"] == wanted.Attributes["origin_factor_id"] &&
			existing.Attributes["factor_output"] == wanted.Attributes["factor_output"]
	}
	return true
}

func (s *Service) sourceDataset(ctx context.Context, set domain.FactorSet) (storageio.DatasetInfo, []storageio.ColumnInfo, error) {
	if s.metadata == nil {
		return storageio.DatasetInfo{}, nil, errors.New("Storage metadata is required")
	}
	source, err := s.metadata.GetDataset(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return storageio.DatasetInfo{}, nil, fmt.Errorf("get source dataset: %w", err)
	}
	if source.Status != storageio.DatasetStatusActive {
		return storageio.DatasetInfo{}, nil, errors.New("source dataset must be active")
	}
	if source.Attributes["dataset_role"] == storageio.DatasetRoleFactorResult {
		return storageio.DatasetInfo{}, nil, errors.New("factor_result dataset cannot be used as a source")
	}
	if source.DataKind != storageio.DataKindTimeSeries {
		return storageio.DatasetInfo{}, nil, errors.New("factor sets require a time_series source dataset")
	}
	frequencyDeclared := false
	for _, frequency := range source.Freqs {
		if strings.EqualFold(strings.TrimSpace(frequency), set.Freq) {
			frequencyDeclared = true
			break
		}
	}
	if !frequencyDeclared {
		return storageio.DatasetInfo{}, nil, fmt.Errorf("source dataset does not declare frequency %q", set.Freq)
	}
	if source.SpaceID != set.SpaceID || source.DatasetID != set.SourceDatasetID || source.DataNodeID == "" || source.DataSourceID == "" {
		return storageio.DatasetInfo{}, nil, errors.New("source dataset identity or DataNode is invalid")
	}
	columns, err := s.metadata.ListColumns(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return storageio.DatasetInfo{}, nil, fmt.Errorf("list source dataset columns: %w", err)
	}
	return source, businessColumns(columns), nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
