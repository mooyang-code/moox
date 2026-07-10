package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。
type rowScanner interface {
	Scan(dest ...any) error
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type execQueryRower interface {
	queryRower
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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
	const where = `
		FROM t_subject_symbols
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_subject_id = ?)
		  AND (? = '' OR c_data_source_id = ?)
		  AND (? = '' OR c_external_symbol = ?)`
	args := []any{spaceID, spaceID, subjectID, subjectID, dataSourceID, dataSourceID, externalSymbol, externalSymbol}
	return queryPagedMessages(ctx, s.db,
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_data_source_id, c_external_symbol`,
		`SELECT COUNT(1) `+where,
		args,
		page,
		func() *pb.SubjectSymbol { return &pb.SubjectSymbol{} },
	)
}

func (s *Store) UpsertDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error) {
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
	return s.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId())
}

func (s *Store) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`, []any{spaceID, datasetID}, func() *pb.Dataset { return &pb.Dataset{} })
}

func (s *Store) ListDatasets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_datasets
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_data_source_id = ?)
		  AND (? = '' OR c_data_kind = ?)
		ORDER BY c_space_id, c_dataset_id
	`, []any{spaceID, spaceID, dataSourceID, dataSourceID, dataKindFilter(dataKind), dataKindFilter(dataKind)}, func() *pb.Dataset { return &pb.Dataset{} })
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

func (s *Store) BindDatasetSubject(ctx context.Context, item *pb.DatasetSubject) (*pb.DatasetSubject, error) {
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

func (s *Store) ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	const where = `
		FROM t_dataset_subjects
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		  AND (? = '' OR c_subject_id = ?)`
	args := []any{spaceID, spaceID, datasetID, datasetID, subjectID, subjectID}
	return queryPagedMessages(ctx, s.db,
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_dataset_id, c_subject_id`,
		`SELECT COUNT(1) `+where,
		args,
		page,
		func() *pb.DatasetSubject { return &pb.DatasetSubject{} },
	)
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

func (s *Store) UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error) {
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
		INSERT INTO t_dataset_columns (c_space_id, c_dataset_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_required, c_is_unique, c_aliases_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_column_name) DO UPDATE SET
			c_origin_type = excluded.c_origin_type,
			c_origin_id = excluded.c_origin_id,
			c_value_type = excluded.c_value_type,
			c_required = excluded.c_required,
			c_is_unique = excluded.c_is_unique,
			c_aliases_json = excluded.c_aliases_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName(), datasetOriginSQL(item.GetOriginType()), item.GetOriginId(), valueTypeSQL(item.GetValueType()), boolInt(item.GetRequired()), boolInt(item.GetIsUnique()), aliases, item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_dataset_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		ORDER BY c_space_id, c_dataset_id, c_column_name
	`, []any{spaceID, spaceID, datasetID, datasetID}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetName() == "" || item.GetPrimaryDatasetId() == "" {
		return nil, errors.New("space_id, view_id, name and primary_dataset_id are required")
	}
	columns := item.GetColumns()
	next := proto.Clone(item).(*pb.View)
	next.Columns = nil
	next.IndexBuild = nil
	next.Status = defaultStatus(next.GetStatus())
	if strings.TrimSpace(next.Engine) == "" {
		next.Engine = s.defaultViewEngine(ctx, next.GetSpaceId(), next.GetPrimaryDatasetId())
	} else {
		next.Engine = strings.ToLower(strings.TrimSpace(next.Engine))
	}
	if len(next.DatasetIds) == 0 {
		next.DatasetIds = []string{next.GetPrimaryDatasetId()}
	}
	datasetIDs, err := marshalJSON(next.GetDatasetIds())
	if err != nil {
		return nil, err
	}
	grainKeys, err := marshalJSON(next.GetGrainKeys())
	if err != nil {
		return nil, err
	}
	activeColumns, err := marshalJSON(next.GetActiveColumns())
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{item.GetSpaceId(), item.GetViewId()}, func() *pb.View { return &pb.View{} })
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	shapeChanged := existing != nil && viewIndexShapeChanged(existing, next)
	mergeViewIndexState(existing, next, shapeChanged)
	raw, err := marshal(next)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_views (c_space_id, c_view_id, c_name, c_description, c_primary_dataset_id, c_dataset_ids_json, c_grain_keys_json, c_filter_json, c_engine, c_retention_window, c_active_index_id, c_view_version, c_active_view_version, c_active_columns_json, c_active_schema_hash, c_active_coverage_start, c_active_coverage_end, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_primary_dataset_id = excluded.c_primary_dataset_id,
			c_dataset_ids_json = excluded.c_dataset_ids_json,
			c_grain_keys_json = excluded.c_grain_keys_json,
			c_filter_json = excluded.c_filter_json,
			c_engine = excluded.c_engine,
			c_retention_window = excluded.c_retention_window,
			c_active_index_id = excluded.c_active_index_id,
			c_view_version = excluded.c_view_version,
			c_active_view_version = excluded.c_active_view_version,
			c_active_columns_json = excluded.c_active_columns_json,
			c_active_schema_hash = excluded.c_active_schema_hash,
			c_active_coverage_start = excluded.c_active_coverage_start,
			c_active_coverage_end = excluded.c_active_coverage_end,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, next.GetSpaceId(), next.GetViewId(), next.GetName(), next.GetDescription(), next.GetPrimaryDatasetId(), datasetIDs, grainKeys, defaultJSON(next.GetFilterJson()), next.GetEngine(), next.GetRetentionWindow(), next.GetActiveIndexId(), next.GetViewVersion(), next.GetActiveViewVersion(), activeColumns, next.GetActiveSchemaHash(), next.GetActiveCoverageStart(), next.GetActiveCoverageEnd(), next.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	if shapeChanged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, next.GetSpaceId(), next.GetViewId()); err != nil {
			return nil, err
		}
	}
	columnsChanged := false
	for _, column := range columns {
		if column.GetSpaceId() == "" {
			column.SpaceId = next.GetSpaceId()
		}
		if column.GetViewId() == "" {
			column.ViewId = next.GetViewId()
		}
		if column.GetColumnName() != "" {
			changed, err := upsertViewColumn(ctx, tx, column)
			if err != nil {
				return nil, err
			}
			columnsChanged = columnsChanged || changed
		}
	}
	if existing != nil && columnsChanged && !shapeChanged {
		if err := bumpViewVersion(ctx, tx, next.GetSpaceId(), next.GetViewId()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, next.GetSpaceId(), next.GetViewId())
}

func (s *Store) defaultViewEngine(ctx context.Context, spaceID string, datasetID string) string {
	dataset, err := s.GetDataset(ctx, spaceID, datasetID)
	if err == nil && dataset.GetDataKind() == pb.DataKind_DATA_KIND_RECORD {
		return "bleve"
	}
	return "duckdb"
}

func mergeViewIndexState(existing *pb.View, item *pb.View, shapeChanged bool) {
	if existing == nil {
		if item.ViewVersion == 0 {
			item.ViewVersion = 1
		}
		return
	}
	item.ActiveIndexId = existing.GetActiveIndexId()
	item.ActiveViewVersion = existing.GetActiveViewVersion()
	item.ActiveColumns = cloneViewColumns(existing.GetActiveColumns())
	item.ActiveSchemaHash = existing.GetActiveSchemaHash()
	item.ActiveCoverageStart = existing.GetActiveCoverageStart()
	item.ActiveCoverageEnd = existing.GetActiveCoverageEnd()
	item.ViewVersion = existing.GetViewVersion()
	if item.ViewVersion == 0 {
		item.ViewVersion = 1
	}
	if shapeChanged {
		item.ViewVersion++
	}
}

func viewIndexShapeChanged(existing *pb.View, next *pb.View) bool {
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
	return existing.GetRetentionWindow() != next.GetRetentionWindow()
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
	build, err := s.GetViewIndexBuild(ctx, spaceID, viewID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	view.IndexBuild = build
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
	for _, item := range items {
		columns, _, err := s.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), nil)
		if err != nil {
			return nil, nil, err
		}
		item.Columns = columns
		build, err := s.GetViewIndexBuild(ctx, item.GetSpaceId(), item.GetViewId())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		item.IndexBuild = build
	}
	return pageItems(items, page)
}

func cloneViewColumns(columns []*pb.ViewColumn) []*pb.ViewColumn {
	out := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		if column != nil {
			out = append(out, proto.Clone(column).(*pb.ViewColumn))
		}
	}
	return out
}

func (s *Store) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	items, _, err := s.ListViews(ctx, spaceID, datasetID, "active", nil)
	return items, err
}

func (s *Store) UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetColumnName() == "" {
		return nil, errors.New("space_id, view_id and column_name are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := upsertViewColumn(ctx, tx, item)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := bumpViewVersion(ctx, tx, item.GetSpaceId(), item.GetViewId()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func upsertViewColumn(ctx context.Context, store execQueryRower, item *pb.ViewColumn) (bool, error) {
	existing, err := getMessage(ctx, store, `SELECT c_attrs_json FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ? AND c_column_name = ?`, []any{item.GetSpaceId(), item.GetViewId(), item.GetColumnName()}, func() *pb.ViewColumn { return &pb.ViewColumn{} })
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	raw, err := marshal(item)
	if err != nil {
		return false, err
	}
	_, err = store.ExecContext(ctx, `
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
		return false, err
	}
	return existing == nil || viewColumnShapeChanged(existing, item), nil
}

func viewColumnShapeChanged(existing *pb.ViewColumn, next *pb.ViewColumn) bool {
	if existing.GetOriginType() != next.GetOriginType() {
		return true
	}
	if existing.GetOriginId() != next.GetOriginId() {
		return true
	}
	if existing.GetValueType() != next.GetValueType() {
		return true
	}
	if existing.GetOnlineTime() != next.GetOnlineTime() {
		return true
	}
	if existing.GetSortOrder() != next.GetSortOrder() {
		return true
	}
	return !mapsEqual(existing.GetAttributes(), next.GetAttributes())
}

func bumpViewVersion(ctx context.Context, tx *sql.Tx, spaceID string, viewID string) error {
	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return err
	}
	if view.ViewVersion == 0 {
		view.ViewVersion = 1
	}
	view.ViewVersion++
	view.Columns = nil
	view.IndexBuild = nil
	raw, err := marshal(view)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_view_version = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ?
	`, view.GetViewVersion(), raw, spaceID, viewID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID); err != nil {
		return err
	}
	return nil
}

var ErrViewIndexBuildConflict = errors.New("view index build conflict")

const sqliteBuildTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

const viewIndexBuildColumns = `
	c_space_id, c_view_id, c_build_id, c_index_id, c_engine,
	c_target_view_version, c_state, c_owner_id, c_lease_expires_at,
	c_cursor_json, c_snapshot_end, c_coverage_start, c_coverage_end,
	c_entries_written, c_schema_hash, c_columns_json, c_started_at,
	c_updated_at, c_finished_at, c_error`

func (s *Store) GetViewIndexBuild(ctx context.Context, spaceID string, viewID string) (*pb.ViewIndexBuild, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+viewIndexBuildColumns+`
		FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID)
	return scanViewIndexBuild(row)
}

func (s *Store) ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error) {
	if err := validateClaimViewIndexBuild(req); err != nil {
		return nil, false, err
	}
	previous, previousErr := s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		return nil, false, previousErr
	}
	now := s.nowUTC()
	nowText := now.Format(sqliteBuildTimestampLayout)
	leaseText := now.Add(buildLeaseTTL(req.GetLeaseTtlSeconds())).Format(sqliteBuildTimestampLayout)
	columnsJSON, err := marshalJSON(req.GetColumns())
	if err != nil {
		return nil, false, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO t_view_index_builds (`+viewIndexBuildColumns+`)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', '', 0, ?, ?, ?, ?, '', ''
		FROM t_views
		WHERE c_space_id = ? AND c_view_id = ? AND c_view_version = ? AND c_active_index_id = ?
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_build_id = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_build_id ELSE excluded.c_build_id END,
			c_index_id = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_index_id ELSE excluded.c_index_id END,
			c_engine = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_engine ELSE excluded.c_engine END,
			c_target_view_version = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_target_view_version ELSE excluded.c_target_view_version END,
			c_state = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_state ELSE excluded.c_state END,
			c_owner_id = excluded.c_owner_id,
			c_lease_expires_at = excluded.c_lease_expires_at,
			c_cursor_json = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_cursor_json ELSE '' END,
			c_snapshot_end = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_snapshot_end ELSE excluded.c_snapshot_end END,
			c_coverage_start = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_coverage_start ELSE '' END,
			c_coverage_end = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_coverage_end ELSE '' END,
			c_entries_written = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_entries_written ELSE 0 END,
			c_schema_hash = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_schema_hash ELSE excluded.c_schema_hash END,
			c_columns_json = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_columns_json ELSE excluded.c_columns_json END,
			c_started_at = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_started_at ELSE excluded.c_started_at END,
			c_updated_at = excluded.c_updated_at,
			c_finished_at = '',
			c_error = ''
		WHERE (t_view_index_builds.c_target_view_version < excluded.c_target_view_version)
		   OR (t_view_index_builds.c_state = ?)
		   OR (t_view_index_builds.c_build_id = excluded.c_build_id AND t_view_index_builds.c_lease_expires_at <= ?)
	`, req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetIndexId(), req.GetEngine(), req.GetTargetViewVersion(), pb.ViewIndexBuild_PREPARING,
		req.GetOwnerId(), leaseText, req.GetSnapshotEnd(), req.GetSchemaHash(), columnsJSON, nowText, nowText,
		req.GetSpaceId(), req.GetViewId(), req.GetTargetViewVersion(), req.GetExpectedActiveIndexId(), pb.ViewIndexBuild_FAILED, nowText)
	if err != nil {
		return nil, false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if rows == 0 {
		return nil, false, ErrViewIndexBuildConflict
	}
	build, err := s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return nil, false, err
	}
	resumed := previous != nil && previous.GetBuildId() == req.GetBuildId()
	return build, resumed, nil
}

func (s *Store) UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	if err := validateUpdateViewIndexBuild(req); err != nil {
		return nil, err
	}
	now := s.nowUTC()
	nowText := now.Format(sqliteBuildTimestampLayout)
	leaseText := now.Add(buildLeaseTTL(req.GetLeaseTtlSeconds())).Format(sqliteBuildTimestampLayout)
	finishedAt := ""
	if req.GetNextState() == pb.ViewIndexBuild_READY || req.GetNextState() == pb.ViewIndexBuild_FAILED {
		finishedAt = nowText
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE t_view_index_builds SET
			c_state = ?, c_lease_expires_at = ?,
			c_cursor_json = CASE WHEN ? <> '' THEN ? ELSE c_cursor_json END,
			c_snapshot_end = CASE WHEN ? <> '' THEN ? ELSE c_snapshot_end END,
			c_coverage_start = CASE WHEN ? <> '' THEN ? ELSE c_coverage_start END,
			c_coverage_end = CASE WHEN ? <> '' THEN ? ELSE c_coverage_end END,
			c_entries_written = CASE WHEN ? > 0 THEN ? ELSE c_entries_written END,
			c_updated_at = ?,
			c_finished_at = CASE WHEN ? <> '' THEN ? ELSE c_finished_at END
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ?
		  AND c_state = ? AND c_lease_expires_at >= ?
	`, req.GetNextState(), leaseText,
		req.GetCursorJson(), req.GetCursorJson(), req.GetSnapshotEnd(), req.GetSnapshotEnd(),
		req.GetCoverageStart(), req.GetCoverageStart(), req.GetCoverageEnd(), req.GetCoverageEnd(),
		req.GetEntriesWritten(), req.GetEntriesWritten(), nowText, finishedAt, finishedAt,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(), req.GetExpectedState(), nowText)
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	return s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
}

func (s *Store) ActivateViewIndex(ctx context.Context, req *pb.ActivateViewIndexReq) (*pb.View, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetOwnerId() == "" {
		return nil, errors.New("space_id, view_id, build_id and owner_id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	nowText := s.nowUTC().Format(sqliteBuildTimestampLayout)
	build, err := scanViewIndexBuild(tx.QueryRowContext(ctx, `SELECT `+viewIndexBuildColumns+`
		FROM t_view_index_builds
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ? AND c_state = ?
		  AND c_lease_expires_at >= ?`,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(), pb.ViewIndexBuild_READY, nowText))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrViewIndexBuildConflict
	}
	if err != nil {
		return nil, err
	}
	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{req.GetSpaceId(), req.GetViewId()}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	if view.GetViewVersion() != build.GetTargetViewVersion() {
		return nil, ErrViewIndexBuildConflict
	}
	view.ActiveIndexId = build.GetIndexId()
	view.ActiveViewVersion = build.GetTargetViewVersion()
	view.ActiveColumns = cloneViewColumns(build.GetColumns())
	view.ActiveSchemaHash = build.GetSchemaHash()
	view.ActiveCoverageStart = build.GetCoverageStart()
	view.ActiveCoverageEnd = build.GetCoverageEnd()
	view.Columns = nil
	view.IndexBuild = nil
	raw, err := marshal(view)
	if err != nil {
		return nil, err
	}
	activeColumns, err := marshalJSON(view.GetActiveColumns())
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_active_index_id = ?, c_active_view_version = ?,
			c_active_columns_json = ?, c_active_schema_hash = ?,
			c_active_coverage_start = ?, c_active_coverage_end = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_view_version = ?
	`, view.GetActiveIndexId(), view.GetActiveViewVersion(), activeColumns, view.GetActiveSchemaHash(),
		view.GetActiveCoverageStart(), view.GetActiveCoverageEnd(), raw, view.GetSpaceId(), view.GetViewId(), build.GetTargetViewVersion())
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM t_view_index_builds
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ? AND c_state = ?`,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(), pb.ViewIndexBuild_READY)
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, req.GetSpaceId(), req.GetViewId())
}

func (s *Store) FailViewIndexBuild(ctx context.Context, req *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetOwnerId() == "" {
		return nil, errors.New("space_id, view_id, build_id and owner_id are required")
	}
	nowText := s.nowUTC().Format(sqliteBuildTimestampLayout)
	message := strings.TrimSpace(req.GetError())
	if message == "" {
		message = "view index build failed"
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE t_view_index_builds SET c_state = ?, c_error = ?, c_updated_at = ?, c_finished_at = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ?
		  AND c_state IN (?, ?, ?) AND c_lease_expires_at >= ?
	`, pb.ViewIndexBuild_FAILED, message, nowText, nowText, req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(),
		pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, nowText)
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	return s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
}

func validateClaimViewIndexBuild(req *pb.ClaimViewIndexBuildReq) error {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetIndexId() == "" ||
		req.GetEngine() == "" || req.GetTargetViewVersion() == 0 || req.GetOwnerId() == "" || req.GetSchemaHash() == "" {
		return errors.New("space_id, view_id, build_id, index_id, engine, target_view_version, owner_id and schema_hash are required")
	}
	return nil
}

func validateUpdateViewIndexBuild(req *pb.UpdateViewIndexBuildReq) error {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetOwnerId() == "" {
		return errors.New("space_id, view_id, build_id and owner_id are required")
	}
	if !validViewIndexBuildTransition(req.GetExpectedState(), req.GetNextState()) {
		return fmt.Errorf("invalid view index build transition %s -> %s", req.GetExpectedState(), req.GetNextState())
	}
	return nil
}

func validViewIndexBuildTransition(from pb.ViewIndexBuild_State, to pb.ViewIndexBuild_State) bool {
	switch from {
	case pb.ViewIndexBuild_PREPARING:
		return to == pb.ViewIndexBuild_BUILDING
	case pb.ViewIndexBuild_BUILDING:
		return to == pb.ViewIndexBuild_BUILDING || to == pb.ViewIndexBuild_CATCHING_UP
	case pb.ViewIndexBuild_CATCHING_UP:
		return to == pb.ViewIndexBuild_CATCHING_UP || to == pb.ViewIndexBuild_READY
	default:
		return false
	}
}

func buildLeaseTTL(seconds uint32) time.Duration {
	if seconds == 0 {
		seconds = 90
	}
	return time.Duration(seconds) * time.Second
}

func requireChangedRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrViewIndexBuildConflict
	}
	return nil
}

func scanViewIndexBuild(row rowScanner) (*pb.ViewIndexBuild, error) {
	build := &pb.ViewIndexBuild{}
	var state int32
	var columnsJSON string
	if err := row.Scan(
		&build.SpaceId, &build.ViewId, &build.BuildId, &build.IndexId, &build.Engine,
		&build.TargetViewVersion, &state, &build.OwnerId, &build.LeaseExpiresAt,
		&build.CursorJson, &build.SnapshotEnd, &build.CoverageStart, &build.CoverageEnd,
		&build.EntriesWritten, &build.SchemaHash, &columnsJSON, &build.StartedAt,
		&build.UpdatedAt, &build.FinishedAt, &build.Error,
	); err != nil {
		return nil, err
	}
	build.State = pb.ViewIndexBuild_State(state)
	if err := json.Unmarshal([]byte(columnsJSON), &build.Columns); err != nil {
		return nil, err
	}
	return build, nil
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

func (s *Store) UpsertPrimaryStoreNode(ctx context.Context, item *pb.PrimaryStoreNode) (*pb.PrimaryStoreNode, error) {
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
		INSERT INTO t_primary_store_nodes (c_node_id, c_name, c_endpoint, c_weight, c_status, c_config_json, c_attrs_json)
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
	return s.GetPrimaryStoreNode(ctx, item.GetNodeId())
}

func (s *Store) GetPrimaryStoreNode(ctx context.Context, nodeID string) (*pb.PrimaryStoreNode, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_primary_store_nodes WHERE c_node_id = ?`, []any{nodeID}, func() *pb.PrimaryStoreNode { return &pb.PrimaryStoreNode{} })
}

func (s *Store) ListPrimaryStoreNodes(ctx context.Context, page *pb.Page) ([]*pb.PrimaryStoreNode, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `SELECT c_attrs_json FROM t_primary_store_nodes ORDER BY c_node_id`, nil, func() *pb.PrimaryStoreNode { return &pb.PrimaryStoreNode{} })
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

func (s *Store) UpsertPrimaryStoreRoute(ctx context.Context, item *pb.PrimaryStoreRoute) (*pb.PrimaryStoreRoute, error) {
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
		INSERT INTO t_primary_store_routes (c_space_id, c_route_id, c_dataset_id, c_subject_id, c_subject_pattern, c_hash_rule, c_node_id, c_priority, c_status, c_attrs_json)
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
	return s.GetPrimaryStoreRoute(ctx, item.GetSpaceId(), item.GetRouteId())
}

func (s *Store) GetPrimaryStoreRoute(ctx context.Context, spaceID string, routeID string) (*pb.PrimaryStoreRoute, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_primary_store_routes WHERE c_space_id = ? AND c_route_id = ?`, []any{spaceID, routeID}, func() *pb.PrimaryStoreRoute { return &pb.PrimaryStoreRoute{} })
}

func (s *Store) ListPrimaryStoreRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_primary_store_routes
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		  AND (? = '' OR c_subject_id = ?)
		  AND (? = '' OR c_node_id = ?)
		ORDER BY c_priority, c_route_id
	`, []any{spaceID, spaceID, datasetID, datasetID, subjectID, subjectID, nodeID, nodeID}, func() *pb.PrimaryStoreRoute { return &pb.PrimaryStoreRoute{} })
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

func getMessage[T proto.Message](ctx context.Context, db queryRower, query string, args []any, newMessage func() T) (T, error) {
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

func queryPagedMessages[T proto.Message](ctx context.Context, db *sql.DB, query string, countQuery string, args []any, page *pb.Page, newMessage func() T) ([]T, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(page)
	limit := int(size)
	pagedArgs := append([]any{}, args...)
	pagedArgs = append(pagedArgs, limit, offset)
	items, err := queryMessages(ctx, db, query+` LIMIT ? OFFSET ?`, pagedArgs, newMessage)
	if err != nil {
		return nil, nil, err
	}
	total, err := countRows(ctx, db, countQuery, args)
	if err != nil {
		return nil, nil, err
	}
	hasMore := uint64(offset)+uint64(len(items)) < uint64(total)
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total, HasMore: hasMore}, nil
}

func countRows(ctx context.Context, db *sql.DB, query string, args []any) (uint32, error) {
	var total int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	if total < 0 {
		return 0, nil
	}
	if total > int64(^uint32(0)) {
		return ^uint32(0), nil
	}
	return uint32(total), nil
}

func normalizePage(page *pb.Page) (pageNo uint32, size uint32, offset int) {
	pageNo = 1
	size = 1000
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	offset64 := uint64(pageNo-1) * uint64(size)
	maxInt := int(^uint(0) >> 1)
	if offset64 > uint64(maxInt) {
		return pageNo, size, maxInt
	}
	return pageNo, size, int(offset64)
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
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(items)), HasMore: end < len(items)}, nil
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
		return "record"
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

func datasetOriginSQL(origin pb.DatasetColumnOriginType) string {
	switch origin {
	case pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR:
		return "factor"
	case pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM:
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

func mapsEqual(left map[string]string, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}
