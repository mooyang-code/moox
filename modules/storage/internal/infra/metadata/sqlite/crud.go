package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type rowScanner interface {
	Scan(dest ...any) error
}

var (
	marshalOptions   = protojson.MarshalOptions{UseProtoNames: true}
	unmarshalOptions = protojson.UnmarshalOptions{DiscardUnknown: true}
)

func (s *Store) UpsertSpace(ctx context.Context, item *pb.Space) (*pb.Space, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_spaces (c_space_id, c_name, c_description, c_owner, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_owner = excluded.c_owner,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetName(), item.GetDescription(), item.GetOwner(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetSpace(ctx, item.GetSpaceId())
}

func (s *Store) GetSpace(ctx context.Context, spaceID string) (*pb.Space, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_spaces WHERE c_space_id = ?`, []any{spaceID}, func() *pb.Space { return &pb.Space{} })
}

func (s *Store) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	query := `SELECT c_attrs_json FROM t_spaces WHERE (? = '' OR c_owner = ?) ORDER BY c_space_id`
	items, err := queryMessages(ctx, s.db, query, []any{owner, owner}, func() *pb.Space { return &pb.Space{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDataSourceId() == "" || item.GetName() == "" || item.GetKind() == "" {
		return nil, errors.New("space_id, data_source_id, name and kind are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_data_sources (c_space_id, c_data_source_id, c_name, c_kind, c_market, c_timezone, c_config_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_data_source_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_kind = excluded.c_kind,
			c_market = excluded.c_market,
			c_timezone = excluded.c_timezone,
			c_config_json = excluded.c_config_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDataSourceId(), item.GetName(), item.GetKind(), item.GetMarket(), item.GetTimezone(), defaultJSON(item.GetConfigJson()), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetDataSource(ctx, item.GetSpaceId(), item.GetDataSourceId())
}

func (s *Store) GetDataSource(ctx context.Context, spaceID string, dataSourceID string) (*pb.DataSource, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_data_sources WHERE c_space_id = ? AND c_data_source_id = ?`, []any{spaceID, dataSourceID}, func() *pb.DataSource { return &pb.DataSource{} })
}

func (s *Store) ListDataSources(ctx context.Context, spaceID string, kind string, market string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_data_sources
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_kind = ?)
		  AND (? = '' OR c_market = ?)
		ORDER BY c_space_id, c_data_source_id
	`, []any{spaceID, spaceID, kind, kind, market, market}, func() *pb.DataSource { return &pb.DataSource{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetSubjectType() == "" {
		return nil, errors.New("space_id, subject_id and subject_type are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_subjects (c_space_id, c_subject_id, c_subject_type, c_name, c_market, c_currency, c_timezone, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_subject_id) DO UPDATE SET
			c_subject_type = excluded.c_subject_type,
			c_name = excluded.c_name,
			c_market = excluded.c_market,
			c_currency = excluded.c_currency,
			c_timezone = excluded.c_timezone,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetSubjectId(), item.GetSubjectType(), item.GetName(), item.GetMarket(), item.GetCurrency(), item.GetTimezone(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetSubject(ctx, item.GetSpaceId(), item.GetSubjectId())
}

func (s *Store) GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, []any{spaceID, subjectID}, func() *pb.Subject { return &pb.Subject{} })
}

func (s *Store) ListSubjects(ctx context.Context, spaceID string, subjectType string, market string, subjectIDs []string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_subjects
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_subject_type = ?)
		  AND (? = '' OR c_market = ?)
		ORDER BY c_space_id, c_subject_id
	`, []any{spaceID, spaceID, subjectType, subjectType, market, market}, func() *pb.Subject { return &pb.Subject{} })
	if err != nil {
		return nil, nil, err
	}
	if len(subjectIDs) > 0 {
		allow := stringSet(subjectIDs)
		filtered := items[:0]
		for _, item := range items {
			if allow[item.GetSubjectId()] {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return pageItems(items, page)
}

func (s *Store) UpsertSubjectSymbol(ctx context.Context, item *pb.SubjectSymbol) (*pb.SubjectSymbol, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetDataSourceId() == "" || item.GetExternalSymbol() == "" {
		return nil, errors.New("space_id, subject_id, data_source_id and external_symbol are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_subject_symbols (c_space_id, c_subject_id, c_data_source_id, c_external_symbol, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_data_source_id, c_external_symbol) DO UPDATE SET
			c_subject_id = excluded.c_subject_id,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetSubjectId(), item.GetDataSourceId(), item.GetExternalSymbol(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) ListSubjectSymbols(ctx context.Context, spaceID string, subjectID string, dataSourceID string, externalSymbol string, page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_subject_symbols
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_subject_id = ?)
		  AND (? = '' OR c_data_source_id = ?)
		  AND (? = '' OR c_external_symbol = ?)
		ORDER BY c_space_id, c_data_source_id, c_external_symbol
	`, []any{spaceID, spaceID, subjectID, subjectID, dataSourceID, dataSourceID, externalSymbol, externalSymbol}, func() *pb.SubjectSymbol { return &pb.SubjectSymbol{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertDataSet(ctx context.Context, item *pb.DataSet) (*pb.DataSet, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDataSourceId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id, dataset_id, data_source_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	freqs, err := marshalJSON(item.GetFreqs())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_datasets (c_space_id, c_dataset_id, c_data_source_id, c_name, c_description, c_data_kind, c_freqs_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id) DO UPDATE SET
			c_data_source_id = excluded.c_data_source_id,
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_data_kind = excluded.c_data_kind,
			c_freqs_json = excluded.c_freqs_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetDataSourceId(), item.GetName(), item.GetDescription(), dataKindSQL(item.GetDataKind()), freqs, item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetDataSet(ctx, item.GetSpaceId(), item.GetDatasetId())
}

func (s *Store) GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`, []any{spaceID, datasetID}, func() *pb.DataSet { return &pb.DataSet{} })
}

func (s *Store) ListDataSets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.DataSet, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_datasets
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_data_source_id = ?)
		  AND (? = '' OR c_data_kind = ?)
		ORDER BY c_space_id, c_dataset_id
	`, []any{spaceID, spaceID, dataSourceID, dataSourceID, dataKindFilter(dataKind), dataKindFilter(dataKind)}, func() *pb.DataSet { return &pb.DataSet{} })
	if err != nil {
		return nil, nil, err
	}
	if freq != "" {
		filtered := items[:0]
		for _, item := range items {
			if containsString(item.GetFreqs(), freq) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return pageItems(items, page)
}

func (s *Store) BindDataSetSubject(ctx context.Context, item *pb.DataSetSubject) (*pb.DataSetSubject, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return nil, errors.New("space_id, dataset_id and subject_id are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.SubjectRole == "" {
		item.SubjectRole = "normal"
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_dataset_subjects (c_space_id, c_dataset_id, c_subject_id, c_subject_role, c_effective_start_time, c_effective_end_time, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_subject_id) DO UPDATE SET
			c_subject_role = excluded.c_subject_role,
			c_effective_start_time = excluded.c_effective_start_time,
			c_effective_end_time = excluded.c_effective_end_time,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId(), item.GetSubjectRole(), item.GetEffectiveStartTime(), item.GetEffectiveEndTime(), item.GetStatus(), raw)
	return item, err
}

func (s *Store) ListDataSetSubjects(ctx context.Context, spaceID string, datasetID string) ([]*pb.DataSetSubject, error) {
	items, _, err := s.ListDataSetSubjectsPage(ctx, spaceID, datasetID, "", nil)
	return items, err
}

func (s *Store) ListDataSetSubjectsPage(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DataSetSubject, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_dataset_subjects
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		  AND (? = '' OR c_subject_id = ?)
		ORDER BY c_space_id, c_dataset_id, c_subject_id
	`, []any{spaceID, spaceID, datasetID, datasetID, subjectID, subjectID}, func() *pb.DataSetSubject { return &pb.DataSetSubject{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertField(ctx context.Context, item *pb.Field) (*pb.Field, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetFieldId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id, field_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_fields (c_space_id, c_field_id, c_name, c_description, c_value_type, c_unit, c_validation_rule_json, c_write_example, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_field_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_value_type = excluded.c_value_type,
			c_unit = excluded.c_unit,
			c_validation_rule_json = excluded.c_validation_rule_json,
			c_write_example = excluded.c_write_example,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetFieldId(), item.GetName(), item.GetDescription(), valueTypeSQL(item.GetValueType()), item.GetUnit(), defaultJSON(item.GetValidationRuleJson()), item.GetWriteExample(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetField(ctx, item.GetSpaceId(), item.GetFieldId())
}

func (s *Store) GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_fields WHERE c_space_id = ? AND c_field_id = ?`, []any{spaceID, fieldID}, func() *pb.Field { return &pb.Field{} })
}

func (s *Store) ListFields(ctx context.Context, spaceID string, valueType pb.FieldValueType, page *pb.Page) ([]*pb.Field, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_fields
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_value_type = ?)
		ORDER BY c_space_id, c_field_id
	`, []any{spaceID, spaceID, valueTypeFilter(valueType), valueTypeFilter(valueType)}, func() *pb.Field { return &pb.Field{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertFactor(ctx context.Context, item *pb.Factor) (*pb.Factor, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetFactorId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id, factor_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_factors (c_space_id, c_factor_id, c_name, c_description, c_algorithm, c_params_json, c_value_type, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_factor_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_algorithm = excluded.c_algorithm,
			c_params_json = excluded.c_params_json,
			c_value_type = excluded.c_value_type,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetFactorId(), item.GetName(), item.GetDescription(), item.GetAlgorithm(), defaultJSON(item.GetParamsJson()), valueTypeSQL(item.GetValueType()), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetFactor(ctx, item.GetSpaceId(), item.GetFactorId())
}

func (s *Store) GetFactor(ctx context.Context, spaceID string, factorID string) (*pb.Factor, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_factors WHERE c_space_id = ? AND c_factor_id = ?`, []any{spaceID, factorID}, func() *pb.Factor { return &pb.Factor{} })
}

func (s *Store) ListFactors(ctx context.Context, spaceID string, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_factors
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_algorithm = ?)
		ORDER BY c_space_id, c_factor_id
	`, []any{spaceID, spaceID, algorithm, algorithm}, func() *pb.Factor { return &pb.Factor{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertDataSetColumn(ctx context.Context, item *pb.DataSetColumn) (*pb.DataSetColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return nil, errors.New("space_id, dataset_id and column_name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	aliases, err := marshalJSON(item.GetAliases())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_dataset_columns (c_space_id, c_dataset_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_required, c_is_unique, c_aliases_json, c_text_indexed, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_column_name) DO UPDATE SET
			c_origin_type = excluded.c_origin_type,
			c_origin_id = excluded.c_origin_id,
			c_value_type = excluded.c_value_type,
			c_required = excluded.c_required,
			c_is_unique = excluded.c_is_unique,
			c_aliases_json = excluded.c_aliases_json,
			c_text_indexed = excluded.c_text_indexed,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName(), datasetOriginSQL(item.GetOriginType()), item.GetOriginId(), valueTypeSQL(item.GetValueType()), boolInt(item.GetRequired()), boolInt(item.GetIsUnique()), aliases, boolInt(item.GetTextIndexed()), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_dataset_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		  AND (? = 0 OR c_text_indexed = 1)
		ORDER BY c_space_id, c_dataset_id, c_column_name
	`, []any{spaceID, spaceID, datasetID, datasetID, boolInt(textIndexedOnly)}, func() *pb.DataSetColumn { return &pb.DataSetColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetName() == "" || item.GetPrimaryDatasetId() == "" {
		return nil, errors.New("space_id, view_id, name and primary_dataset_id are required")
	}
	existing, _ := getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{item.GetSpaceId(), item.GetViewId()}, func() *pb.View { return &pb.View{} })
	inputBuildStatus := item.GetBuildStatus()
	item.Status = defaultStatus(item.GetStatus())
	if item.Engine == "" {
		item.Engine = "duckdb"
	}
	if len(item.DatasetIds) == 0 {
		item.DatasetIds = []string{item.GetPrimaryDatasetId()}
	}
	if existing != nil && item.ActiveResult == "" {
		item.ActiveResult = existing.GetActiveResult()
	}
	if inputBuildStatus == "" {
		if existing == nil {
			item.BuildStatus = "pending"
		} else if viewBuildShapeChanged(existing, item) {
			item.BuildStatus = "pending"
		} else {
			item.BuildStatus = existing.GetBuildStatus()
		}
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	datasetIDs, err := marshalJSON(item.GetDatasetIds())
	if err != nil {
		return nil, err
	}
	grainKeys, err := marshalJSON(item.GetGrainKeys())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_views (c_space_id, c_view_id, c_name, c_description, c_primary_dataset_id, c_dataset_ids_json, c_grain_keys_json, c_filter_json, c_engine, c_query_window, c_active_result, c_build_status, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_primary_dataset_id = excluded.c_primary_dataset_id,
			c_dataset_ids_json = excluded.c_dataset_ids_json,
			c_grain_keys_json = excluded.c_grain_keys_json,
			c_filter_json = excluded.c_filter_json,
			c_engine = excluded.c_engine,
			c_query_window = excluded.c_query_window,
			c_active_result = excluded.c_active_result,
			c_build_status = excluded.c_build_status,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetViewId(), item.GetName(), item.GetDescription(), item.GetPrimaryDatasetId(), datasetIDs, grainKeys, defaultJSON(item.GetFilterJson()), item.GetEngine(), item.GetQueryWindow(), item.GetActiveResult(), item.GetBuildStatus(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	for _, column := range item.GetColumns() {
		if column.GetSpaceId() == "" {
			column.SpaceId = item.GetSpaceId()
		}
		if column.GetViewId() == "" {
			column.ViewId = item.GetViewId()
		}
		if column.GetColumnName() != "" {
			if _, err := s.UpsertViewColumn(ctx, column); err != nil {
				return nil, err
			}
		}
	}
	return s.GetView(ctx, item.GetSpaceId(), item.GetViewId())
}

func viewBuildShapeChanged(existing *pb.View, next *pb.View) bool {
	if existing.GetPrimaryDatasetId() != next.GetPrimaryDatasetId() {
		return true
	}
	if !slices.Equal(existing.GetDatasetIds(), next.GetDatasetIds()) {
		return true
	}
	if !slices.Equal(existing.GetGrainKeys(), next.GetGrainKeys()) {
		return true
	}
	if existing.GetFilterJson() != next.GetFilterJson() {
		return true
	}
	if existing.GetEngine() != next.GetEngine() {
		return true
	}
	return existing.GetQueryWindow() != next.GetQueryWindow()
}

func (s *Store) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	view, err := getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	columns, _, err := s.ListViewColumns(ctx, spaceID, viewID, nil)
	if err != nil {
		return nil, err
	}
	view.Columns = columns
	return view, nil
}

func (s *Store) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_views
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_status = ?)
		ORDER BY c_space_id, c_view_id
	`, []any{spaceID, spaceID, status, status}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, nil, err
	}
	if datasetID != "" {
		filtered := items[:0]
		for _, item := range items {
			if containsString(item.GetDatasetIds(), datasetID) || item.GetPrimaryDatasetId() == datasetID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return pageItems(items, page)
}

func (s *Store) UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetColumnName() == "" {
		return nil, errors.New("space_id, view_id and column_name are required")
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_online_time, c_sort_order, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id, c_column_name) DO UPDATE SET
			c_origin_type = excluded.c_origin_type,
			c_origin_id = excluded.c_origin_id,
			c_value_type = excluded.c_value_type,
			c_online_time = excluded.c_online_time,
			c_sort_order = excluded.c_sort_order,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetViewId(), item.GetColumnName(), viewOriginSQL(item.GetOriginType()), item.GetOriginId(), valueTypeSQL(item.GetValueType()), item.GetOnlineTime(), item.GetSortOrder(), raw)
	if err != nil {
		return nil, err
	}
	if err := s.markViewPending(ctx, item.GetSpaceId(), item.GetViewId()); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) markViewPending(ctx context.Context, spaceID string, viewID string) error {
	view, err := getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return err
	}
	if view.GetBuildStatus() == "pending" || view.GetBuildStatus() == "building" {
		return nil
	}
	view.BuildStatus = "pending"
	raw, err := marshal(view)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE t_views
		SET c_build_status = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ?
	`, view.GetBuildStatus(), raw, spaceID, viewID)
	return err
}

func (s *Store) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_view_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_view_id = ?)
		ORDER BY c_sort_order, c_column_name
	`, []any{spaceID, spaceID, viewID, viewID}, func() *pb.ViewColumn { return &pb.ViewColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertStorageNode(ctx context.Context, item *pb.StorageNode) (*pb.StorageNode, error) {
	if item == nil || item.GetNodeId() == "" || item.GetName() == "" {
		return nil, errors.New("node_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.Weight == 0 {
		item.Weight = 100
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_storage_nodes (c_node_id, c_name, c_endpoint, c_weight, c_status, c_config_json, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_node_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_endpoint = excluded.c_endpoint,
			c_weight = excluded.c_weight,
			c_status = excluded.c_status,
			c_config_json = excluded.c_config_json,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetNodeId(), item.GetName(), item.GetEndpoint(), item.GetWeight(), item.GetStatus(), defaultJSON(item.GetConfigJson()), raw)
	if err != nil {
		return nil, err
	}
	return s.GetStorageNode(ctx, item.GetNodeId())
}

func (s *Store) GetStorageNode(ctx context.Context, nodeID string) (*pb.StorageNode, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_storage_nodes WHERE c_node_id = ?`, []any{nodeID}, func() *pb.StorageNode { return &pb.StorageNode{} })
}

func (s *Store) ListStorageNodes(ctx context.Context, page *pb.Page) ([]*pb.StorageNode, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `SELECT c_attrs_json FROM t_storage_nodes ORDER BY c_node_id`, nil, func() *pb.StorageNode { return &pb.StorageNode{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error) {
	if item == nil || item.GetDeviceId() == "" || item.GetNodeId() == "" || item.GetName() == "" || item.GetEngine() == "" {
		return nil, errors.New("device_id, node_id, name and engine are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_storage_devices (c_device_id, c_node_id, c_name, c_engine, c_endpoint, c_config_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_device_id) DO UPDATE SET
			c_node_id = excluded.c_node_id,
			c_name = excluded.c_name,
			c_engine = excluded.c_engine,
			c_endpoint = excluded.c_endpoint,
			c_config_json = excluded.c_config_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetDeviceId(), item.GetNodeId(), item.GetName(), item.GetEngine(), item.GetEndpoint(), defaultJSON(item.GetConfigJson()), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetDevice(ctx, item.GetDeviceId())
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (*pb.Device, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_storage_devices WHERE c_device_id = ?`, []any{deviceID}, func() *pb.Device { return &pb.Device{} })
}

func (s *Store) ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_storage_devices
		WHERE (? = '' OR c_node_id = ?)
		  AND (? = '' OR c_engine = ?)
		ORDER BY c_device_id
	`, []any{nodeID, nodeID, engine, engine}, func() *pb.Device { return &pb.Device{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertStorageRoute(ctx context.Context, item *pb.StorageRoute) (*pb.StorageRoute, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetRouteId() == "" || item.GetDatasetId() == "" || item.GetNodeId() == "" {
		return nil, errors.New("space_id, route_id, dataset_id and node_id are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.Priority == 0 {
		item.Priority = 100
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_storage_routes (c_space_id, c_route_id, c_dataset_id, c_subject_id, c_subject_pattern, c_hash_rule, c_node_id, c_priority, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_route_id) DO UPDATE SET
			c_dataset_id = excluded.c_dataset_id,
			c_subject_id = excluded.c_subject_id,
			c_subject_pattern = excluded.c_subject_pattern,
			c_hash_rule = excluded.c_hash_rule,
			c_node_id = excluded.c_node_id,
			c_priority = excluded.c_priority,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetRouteId(), item.GetDatasetId(), item.GetSubjectId(), item.GetSubjectPattern(), item.GetHashRule(), item.GetNodeId(), item.GetPriority(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetStorageRoute(ctx, item.GetSpaceId(), item.GetRouteId())
}

func (s *Store) GetStorageRoute(ctx context.Context, spaceID string, routeID string) (*pb.StorageRoute, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_storage_routes WHERE c_space_id = ? AND c_route_id = ?`, []any{spaceID, routeID}, func() *pb.StorageRoute { return &pb.StorageRoute{} })
}

func (s *Store) ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_storage_routes
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		  AND (? = '' OR c_subject_id = ?)
		  AND (? = '' OR c_node_id = ?)
		ORDER BY c_priority, c_route_id
	`, []any{spaceID, spaceID, datasetID, datasetID, subjectID, subjectID, nodeID, nodeID}, func() *pb.StorageRoute { return &pb.StorageRoute{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetArchiveFileId() == "" || item.GetDatasetId() == "" || item.GetDeviceId() == "" || item.GetFileUri() == "" {
		return nil, errors.New("space_id, archive_file_id, dataset_id, device_id and file_uri are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.FileFormat == "" {
		item.FileFormat = "parquet"
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	columns, err := marshalJSON(item.GetColumns())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_archive_files (c_space_id, c_archive_file_id, c_dataset_id, c_device_id, c_partition_key, c_file_uri, c_file_format, c_min_time, c_max_time, c_row_count, c_content_hash, c_columns_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_archive_file_id) DO UPDATE SET
			c_dataset_id = excluded.c_dataset_id,
			c_device_id = excluded.c_device_id,
			c_partition_key = excluded.c_partition_key,
			c_file_uri = excluded.c_file_uri,
			c_file_format = excluded.c_file_format,
			c_min_time = excluded.c_min_time,
			c_max_time = excluded.c_max_time,
			c_row_count = excluded.c_row_count,
			c_content_hash = excluded.c_content_hash,
			c_columns_json = excluded.c_columns_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetArchiveFileId(), item.GetDatasetId(), item.GetDeviceId(), item.GetPartitionKey(), item.GetFileUri(), item.GetFileFormat(), item.GetMinTime(), item.GetMaxTime(), item.GetRowCount(), item.GetContentHash(), columns, item.GetStatus(), raw)
	return item, err
}

func (s *Store) ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_archive_files
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		ORDER BY c_partition_key, c_file_uri
	`, []any{spaceID, spaceID, datasetID, datasetID}, func() *pb.ArchiveFile { return &pb.ArchiveFile{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func getMessage[T proto.Message](ctx context.Context, db *sql.DB, query string, args []any, newMessage func() T) (T, error) {
	row := db.QueryRowContext(ctx, query, args...)
	return scanMessage(row, newMessage)
}

func queryMessages[T proto.Message](ctx context.Context, db *sql.DB, query string, args []any, newMessage func() T) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []T
	for rows.Next() {
		item, err := scanMessage(rows, newMessage)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanMessage[T proto.Message](row rowScanner, newMessage func() T) (T, error) {
	var raw string
	if err := row.Scan(&raw); err != nil {
		var zero T
		if errors.Is(err, sql.ErrNoRows) {
			return zero, fmt.Errorf("metadata row not found: %w", err)
		}
		return zero, err
	}
	msg := newMessage()
	if err := unmarshalOptions.Unmarshal([]byte(raw), msg); err != nil {
		var zero T
		return zero, err
	}
	return msg, nil
}

func marshal(msg proto.Message) (string, error) {
	data, err := marshalOptions.Marshal(msg)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func marshalJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func pageItems[T any](items []T, page *pb.Page) ([]T, *pb.PageResult, error) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint64(len(items)), HasMore: end < len(items)}, nil
}

func defaultStatus(status string) string {
	if status == "" {
		return "active"
	}
	return status
}

func defaultJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func dataKindFilter(kind pb.DataKind) string {
	if kind == pb.DataKind_DATA_KIND_UNSPECIFIED {
		return ""
	}
	return dataKindSQL(kind)
}

func dataKindSQL(kind pb.DataKind) string {
	switch kind {
	case pb.DataKind_DATA_KIND_TIME_SERIES:
		return "time_series"
	case pb.DataKind_DATA_KIND_SNAPSHOT:
		return "snapshot"
	case pb.DataKind_DATA_KIND_EVENT:
		return "event"
	case pb.DataKind_DATA_KIND_DOCUMENT:
		return "document"
	case pb.DataKind_DATA_KIND_TABLE:
		return "table"
	default:
		return "object"
	}
}

func valueTypeFilter(valueType pb.FieldValueType) string {
	if valueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
		return ""
	}
	return valueTypeSQL(valueType)
}

func valueTypeSQL(valueType pb.FieldValueType) string {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "int"
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "double"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "bool"
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return "time"
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return "json"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return "bytes"
	default:
		return "string"
	}
}

func datasetOriginSQL(origin pb.ColumnOriginType) string {
	switch origin {
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FACTOR:
		return "factor"
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM:
		return "system"
	default:
		return "field"
	}
}

func viewOriginSQL(origin pb.ColumnOriginType) string {
	switch origin {
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION:
		return "expression"
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM:
		return "system"
	default:
		return "dataset_column"
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sortBy[T any](items []T, less func(left, right T) bool) {
	sort.SliceStable(items, func(i, j int) bool { return less(items[i], items[j]) })
}
