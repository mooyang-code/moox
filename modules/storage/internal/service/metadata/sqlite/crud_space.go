package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

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

// CreateSpace inserts a new Space without overwriting an existing one. The
// catalog CreateSpace RPC uses this path so concurrent acceptance runs cannot
// race through an existence check and then delete each other's metadata.
func (s *Store) CreateSpace(ctx context.Context, item *pb.Space) (*pb.Space, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO t_spaces (c_space_id, c_name, c_description, c_owner, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetName(), item.GetDescription(), item.GetOwner(), item.GetStatus(), raw); err != nil {
		return nil, err
	}
	return s.GetSpace(ctx, item.GetSpaceId())
}

func (s *Store) GetSpace(ctx context.Context, spaceID string) (*pb.Space, error) {
	return scanMessageWithSQLTimestamps(s.queryDB(ctx).QueryRowContext(ctx, `SELECT c_attrs_json, c_ctime, c_mtime FROM t_spaces WHERE c_space_id = ?`, spaceID), func() *pb.Space { return &pb.Space{} })
}

func (s *Store) DeleteSpace(ctx context.Context, spaceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	// Fields are space-scoped through their group, whose foreign key is RESTRICT.
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_fields WHERE c_space_id = ?`, spaceID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM t_spaces WHERE c_space_id = ?`, spaceID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(page)
	rows, err := s.queryDB(ctx).QueryContext(ctx, `SELECT c_attrs_json, c_ctime, c_mtime FROM t_spaces WHERE (? = '' OR c_owner = ?) ORDER BY c_space_id LIMIT ? OFFSET ?`, owner, owner, size, offset)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.Space, 0, size)
	for rows.Next() {
		item, err := scanMessageWithSQLTimestamps(rows, func() *pb.Space { return &pb.Space{} })
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var total uint32
	if err := s.queryDB(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM t_spaces WHERE (? = '' OR c_owner = ?)`, owner, owner).Scan(&total); err != nil {
		return nil, nil, err
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total, HasMore: uint64(offset)+uint64(len(items)) < uint64(total)}, nil
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
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_data_sources WHERE c_space_id = ? AND c_data_source_id = ?`, []any{spaceID, dataSourceID}, func() *pb.DataSource { return &pb.DataSource{} })
}

func (s *Store) DeleteDataSource(ctx context.Context, spaceID string, dataSourceID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM t_data_sources WHERE c_space_id = ? AND c_data_source_id = ?`, spaceID, dataSourceID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListDataSources(ctx context.Context, spaceID string, kind string, market string, keyword string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	keyword = strings.TrimSpace(keyword)
	const where = `
		FROM t_data_sources
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_kind = ?)
		  AND (? = '' OR c_market = ?)
		  AND (? = '' OR instr(lower(c_data_source_id), lower(?)) > 0 OR instr(lower(c_name), lower(?)) > 0 OR instr(lower(c_kind), lower(?)) > 0 OR instr(lower(c_market), lower(?)) > 0)`
	args := []any{spaceID, spaceID, kind, kind, market, market, keyword, keyword, keyword, keyword, keyword}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_data_source_id`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.DataSource { return &pb.DataSource{} },
	)
}
