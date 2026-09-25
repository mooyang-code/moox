package sqlite

import (
	"context"
	"errors"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

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
	if subjectIDs == nil {
		subjectIDs = []string{}
	}
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
