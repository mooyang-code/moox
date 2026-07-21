package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func (s *Store) UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetSubjectType() == "" {
		return nil, errors.New("space_id, subject_id and subject_type are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if err := upsertSubject(ctx, s.db, item); err != nil {
		return nil, err
	}
	return s.GetSubject(ctx, item.GetSpaceId(), item.GetSubjectId())
}

func upsertSubject(ctx context.Context, store execQueryRower, item *pb.Subject) error {
	raw, err := marshal(item)
	if err != nil {
		return err
	}
	_, err = store.ExecContext(ctx, `
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
	return err
}

func (s *Store) GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error) {
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, []any{spaceID, subjectID}, func() *pb.Subject { return &pb.Subject{} })
}

func (s *Store) ListSubjects(ctx context.Context, spaceID string, subjectType string, market string, subjectIDs []string, keyword string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	subjectIDsJSON, err := marshalJSON(subjectIDs)
	if err != nil {
		return nil, nil, err
	}
	const where = `
		FROM t_subjects
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_subject_type = ?)
		  AND (? = '' OR c_market = ?)
		  AND (? = '[]' OR c_subject_id IN (SELECT value FROM json_each(?)))
		  AND (? = '' OR instr(lower(c_subject_id), ?) > 0 OR instr(lower(c_subject_type), ?) > 0 OR instr(lower(c_name), ?) > 0 OR instr(lower(c_market), ?) > 0 OR instr(lower(c_currency), ?) > 0)`
	args := []any{
		spaceID, spaceID, subjectType, subjectType, market, market,
		subjectIDsJSON, subjectIDsJSON,
		keyword, keyword, keyword, keyword, keyword, keyword,
	}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_subject_id`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.Subject { return &pb.Subject{} },
	)
}

func (s *Store) UpsertSubjectSymbol(ctx context.Context, item *pb.SubjectSymbol) (*pb.SubjectSymbol, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetDataSourceId() == "" || item.GetExternalSymbol() == "" {
		return nil, errors.New("space_id, subject_id, data_source_id and external_symbol are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if err := upsertSubjectSymbol(ctx, s.db, item); err != nil {
		return nil, err
	}
	return item, nil
}

func upsertSubjectSymbol(ctx context.Context, store execQueryRower, item *pb.SubjectSymbol) error {
	raw, err := marshal(item)
	if err != nil {
		return err
	}
	_, err = store.ExecContext(ctx, `
		INSERT INTO t_subject_symbols (c_space_id, c_subject_id, c_data_source_id, c_external_symbol, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_data_source_id, c_external_symbol) DO UPDATE SET
			c_subject_id = excluded.c_subject_id,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetSubjectId(), item.GetDataSourceId(), item.GetExternalSymbol(), item.GetStatus(), raw)
	return err
}

func (s *Store) ListSubjectSymbols(ctx context.Context, spaceID string, subjectID string, dataSourceID string, externalSymbol string, page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	const where = `
		FROM t_subject_symbols
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_subject_id = ?)
		  AND (? = '' OR c_data_source_id = ?)
		  AND (? = '' OR c_external_symbol = ?)`
	args := []any{spaceID, spaceID, subjectID, subjectID, dataSourceID, dataSourceID, externalSymbol, externalSymbol}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_data_source_id, c_external_symbol`,
		`SELECT COUNT(1) `+where,
		args,
		page,
		func() *pb.SubjectSymbol { return &pb.SubjectSymbol{} },
	)
}

func (s *Store) BindDatasetSubject(ctx context.Context, item *pb.DatasetSubject) (*pb.DatasetSubject, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return nil, errors.New("space_id, dataset_id and subject_id are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.SubjectRole == "" {
		item.SubjectRole = "normal"
	}
	if err := bindDatasetSubject(ctx, s.db, item); err != nil {
		return nil, err
	}
	return item, nil
}

func bindDatasetSubject(ctx context.Context, store execQueryRower, item *pb.DatasetSubject) error {
	raw, err := marshal(item)
	if err != nil {
		return err
	}
	_, err = store.ExecContext(ctx, `
		INSERT INTO t_dataset_subjects (c_space_id, c_dataset_id, c_subject_id, c_subject_role, c_effective_start_time, c_effective_end_time, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_subject_id) DO UPDATE SET
			c_subject_role = excluded.c_subject_role,
			c_effective_start_time = excluded.c_effective_start_time,
			c_effective_end_time = excluded.c_effective_end_time,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId(), item.GetSubjectRole(), item.GetEffectiveStartTime(), item.GetEffectiveEndTime(), item.GetStatus(), raw)
	return err
}

func (s *Store) RegisterDataSubject(ctx context.Context, subject *pb.Subject, symbol *pb.SubjectSymbol, bindings []*pb.DatasetSubject) (*pb.Subject, []*pb.DatasetSubject, error) {
	if subject == nil || subject.GetSpaceId() == "" || subject.GetSubjectId() == "" || subject.GetSubjectType() == "" {
		return nil, nil, errors.New("space_id, subject_id and subject_type are required")
	}
	if symbol == nil || symbol.GetSpaceId() != subject.GetSpaceId() || symbol.GetSubjectId() != subject.GetSubjectId() || symbol.GetDataSourceId() == "" || symbol.GetExternalSymbol() == "" {
		return nil, nil, errors.New("subject symbol must match space_id and subject_id and include data_source_id and external_symbol")
	}

	subject = proto.Clone(subject).(*pb.Subject)
	symbol = proto.Clone(symbol).(*pb.SubjectSymbol)
	subject.Status = defaultStatus(subject.GetStatus())
	symbol.Status = defaultStatus(symbol.GetStatus())
	normalizedBindings := make([]*pb.DatasetSubject, 0, len(bindings))
	for _, binding := range bindings {
		if binding == nil || binding.GetSpaceId() != subject.GetSpaceId() || binding.GetSubjectId() != subject.GetSubjectId() || binding.GetDatasetId() == "" {
			return nil, nil, errors.New("dataset binding must match space_id and subject_id and include dataset_id")
		}
		copied := proto.Clone(binding).(*pb.DatasetSubject)
		copied.Status = defaultStatus(copied.GetStatus())
		if copied.SubjectRole == "" {
			copied.SubjectRole = "normal"
		}
		normalizedBindings = append(normalizedBindings, copied)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, binding := range normalizedBindings {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`, binding.GetSpaceId(), binding.GetDatasetId()).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil, fmt.Errorf("dataset %s/%s not found: %w", binding.GetSpaceId(), binding.GetDatasetId(), err)
			}
			return nil, nil, err
		}
	}
	if err := upsertSubject(ctx, tx, subject); err != nil {
		return nil, nil, err
	}
	if err := upsertSubjectSymbol(ctx, tx, symbol); err != nil {
		return nil, nil, err
	}
	for _, binding := range normalizedBindings {
		if err := bindDatasetSubject(ctx, tx, binding); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return subject, normalizedBindings, nil
}

func (s *Store) ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	const where = `
		FROM t_dataset_subjects
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		  AND (? = '' OR c_subject_id = ?)`
	args := []any{spaceID, spaceID, datasetID, datasetID, subjectID, subjectID}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_dataset_id, c_subject_id`,
		`SELECT COUNT(1) `+where,
		args,
		page,
		func() *pb.DatasetSubject { return &pb.DatasetSubject{} },
	)
}
