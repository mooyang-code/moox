package catalog

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	err := s.reconcileResultColumns(ctx, set)
	switch {
	case err == nil:
		s.markReady(set.SetID, true)
	case errors.Is(err, storageio.ErrInfra):
		// A transient Storage failure says nothing about the result dataset;
		// keep the engine computing with the last known readiness.
	default:
		s.markReady(set.SetID, false)
	}
	return err
}

func (s *Service) reconcileResultColumns(ctx context.Context, set domain.FactorSet) error {
	source, columns, err := s.sourceDataset(ctx, set)
	if err != nil {
		return err
	}
	members, err := s.db.ListMembers(ctx, set.SetID, domain.MemberStatusEnabled)
	if err != nil {
		return fmt.Errorf("list enabled members: %w", err)
	}
	defs := make([]domain.MemberDef, 0, len(members))
	for _, member := range members {
		defs = append(defs, member.Def())
	}
	return s.ensureResultColumns(ctx, set, source, columns, defs...)
}

func (s *Service) ensureResultColumns(ctx context.Context, set domain.FactorSet, source storageio.DatasetInfo, sourceColumns []storageio.ColumnInfo, members ...domain.MemberDef) error {
	if s.metadata == nil {
		return errors.New("Storage metadata is required")
	}
	result, err := s.metadata.GetDataset(ctx, set.SpaceID, set.ResultDatasetID)
	if err != nil {
		return fmt.Errorf("get factor result dataset: %w", err)
	}
	if result.SpaceID != set.SpaceID || result.DatasetID != set.ResultDatasetID ||
		result.DataSourceID != source.DataSourceID || result.DataNodeID != source.DataNodeID ||
		result.DataKind != storageio.DataKindTimeSeries ||
		result.Freq != set.Freq ||
		result.Attributes["owner_module"] != "factor" || result.Attributes["dataset_role"] != storageio.DatasetRoleFactorResult ||
		result.Attributes["source_dataset_id"] != set.SourceDatasetID || result.Attributes["write_owner"] != "factor" {
		return fmt.Errorf("result dataset %q does not match the factor result contract", set.ResultDatasetID)
	}
	// The result dataset mirrors its source's subject scope; follow a source
	// tag change instead of halting the set.
	if !equalStrings(result.SubjectTags, source.SubjectTags) {
		if err := s.metadata.SetDatasetSubjectTags(ctx, set.SpaceID, set.ResultDatasetID, source.SubjectTags); err != nil {
			return fmt.Errorf("sync factor result subject tags: %w", err)
		}
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
	for _, member := range members {
		for _, column := range append(member.OutputColumns(), member.ParamColumns()...) {
			if _, exists := sourceNames[column]; exists {
				return fmt.Errorf("source column %q collides with enabled member %s", column, member.Alias)
			}
		}
	}
	wanted = appendFactorColumns(wanted, members...)
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
			// 身份一致但属性过期（成员换绑了另一个定义：factor_id、分类等变了）：刷新属性，否则策略按旧属性固化因子身份。
			if refreshed, stale := refreshedColumnAttributes(existing, col); stale {
				col.Attributes = refreshed
				missing = append(missing, col)
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

// refreshedColumnAttributes 返回把期望的属性叠到已有属性上的结果，以及已有属性是否过期（期望的某个键与已有值不同）。
// 只覆盖期望里写出的键，其他键（例如别处设置的显示属性）保留。
func refreshedColumnAttributes(existing, wanted storageio.ColumnInfo) (map[string]string, bool) {
	merged := make(map[string]string, len(existing.Attributes)+len(wanted.Attributes))
	maps.Copy(merged, existing.Attributes)
	stale := false
	for key, value := range wanted.Attributes {
		if existing.Attributes[key] != value {
			stale = true
		}
		merged[key] = value
	}
	return merged, stale
}

func compatibleResultColumn(existing, wanted storageio.ColumnInfo) bool {
	if existing.OriginType != wanted.OriginType || existing.OriginID != wanted.OriginID || existing.ValueType != wanted.ValueType {
		return false
	}
	if wanted.OriginType == storageio.ColumnOriginFactor {
		return existing.Attributes["origin_factor_id"] == wanted.Attributes["origin_factor_id"] &&
			existing.Attributes["factor_output"] == wanted.Attributes["factor_output"] &&
			existing.Attributes["column_kind"] == wanted.Attributes["column_kind"]
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
	if source.Freq != set.Freq {
		return storageio.DatasetInfo{}, nil, fmt.Errorf("source dataset freq is %q, not %q", source.Freq, set.Freq)
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
