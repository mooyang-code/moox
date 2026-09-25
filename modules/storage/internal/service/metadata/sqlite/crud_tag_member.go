package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func tagMode(ctx context.Context, db queryRower, spaceID, tagID string) (string, error) {
	var mode string
	if err := db.QueryRowContext(ctx, `SELECT c_mode FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tagID).Scan(&mode); err != nil {
		return "", err
	}
	return mode, nil
}

func requireManualTag(ctx context.Context, db queryRower, spaceID, tagID string) error {
	mode, err := tagMode(ctx, db, spaceID, tagID)
	if err != nil {
		return err
	}
	if mode != metadatastore.TagModeManual {
		return metadatastore.ErrTagAutoMembers
	}
	return nil
}

func (s *Store) ListTagMembers(ctx context.Context, query metadatastore.TagMemberQuery) ([]*pb.TagMember, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(query.Page)
	if strings.TrimSpace(query.TagID) == "" {
		return s.listAllSubjectMembers(ctx, query, pageNo, size, offset)
	}
	where := []string{"m.c_space_id = ?"}
	args := []any{query.SpaceID}
	if strings.TrimSpace(query.TagID) != "" {
		where = append(where, "m.c_tag_id = ?")
		args = append(args, query.TagID)
	}
	if strings.TrimSpace(query.Status) != "" {
		where = append(where, "m.c_status = ?")
		args = append(args, query.Status)
	}
	if keyword := strings.ToLower(strings.TrimSpace(query.Keyword)); keyword != "" {
		where = append(where, `(instr(lower(s.c_subject_id), ?) > 0 OR instr(lower(s.c_name), ?) > 0 OR instr(lower(s.c_subject_type), ?) > 0 OR instr(lower(s.c_market), ?) > 0)`)
		args = append(args, keyword, keyword, keyword, keyword)
	}
	whereSQL := strings.Join(where, " AND ")
	db := s.queryDB(ctx)
	var total uint64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_subject_tags m JOIN t_subjects s ON s.c_space_id = m.c_space_id AND s.c_subject_id = m.c_subject_id WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT m.c_space_id, m.c_tag_id, m.c_status, m.c_inactive_at, s.c_attrs_json
		FROM t_subject_tags m
		JOIN t_subjects s ON s.c_space_id = m.c_space_id AND s.c_subject_id = m.c_subject_id
		WHERE `+whereSQL+` ORDER BY m.c_tag_id, m.c_subject_id LIMIT ? OFFSET ?`, append(args, size, offset)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.TagMember, 0)
	for rows.Next() {
		item := &pb.TagMember{Subject: &pb.Subject{}}
		var raw string
		if err := rows.Scan(&item.SpaceId, &item.TagId, &item.Status, &item.InactiveAt, &raw); err != nil {
			return nil, nil, err
		}
		if err := unmarshalOptions.Unmarshal([]byte(raw), item.Subject); err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: uint32(total), HasMore: uint64(offset)+uint64(len(items)) < total}, nil
}

func (s *Store) listAllSubjectMembers(ctx context.Context, query metadatastore.TagMemberQuery, pageNo, size uint32, offset int) ([]*pb.TagMember, *pb.PageResult, error) {
	where := []string{"s.c_space_id = ?"}
	args := []any{query.SpaceID}
	if keyword := strings.ToLower(strings.TrimSpace(query.Keyword)); keyword != "" {
		where = append(where, `(instr(lower(s.c_subject_id), ?) > 0 OR instr(lower(s.c_name), ?) > 0 OR instr(lower(s.c_subject_type), ?) > 0 OR instr(lower(s.c_market), ?) > 0)`)
		args = append(args, keyword, keyword, keyword, keyword)
	}
	whereSQL := strings.Join(where, " AND ")
	db := s.queryDB(ctx)
	var total uint64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_subjects s WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT s.c_attrs_json FROM t_subjects s WHERE `+whereSQL+` ORDER BY s.c_subject_id LIMIT ? OFFSET ?`, append(args, size, offset)...)
	if err != nil {
		return nil, nil, err
	}
	items := make([]*pb.TagMember, 0)
	for rows.Next() {
		subject := &pb.Subject{}
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if err := unmarshalOptions.Unmarshal([]byte(raw), subject); err != nil {
			rows.Close()
			return nil, nil, err
		}
		items = append(items, &pb.TagMember{SpaceId: subject.GetSpaceId(), Subject: subject})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	for _, item := range items {
		if err := s.loadSubjectTagIDs(ctx, item.GetSpaceId(), item.GetSubject().GetSubjectId(), item); err != nil {
			return nil, nil, err
		}
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: uint32(total), HasMore: uint64(offset)+uint64(len(items)) < total}, nil
}

func (s *Store) loadSubjectTagIDs(ctx context.Context, spaceID, subjectID string, item *pb.TagMember) error {
	rows, err := s.queryDB(ctx).QueryContext(ctx, `SELECT c_tag_id FROM t_subject_tags WHERE c_space_id = ? AND c_subject_id = ? ORDER BY c_tag_id`, spaceID, subjectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tagID string
		if err := rows.Scan(&tagID); err != nil {
			return err
		}
		item.TagIds = append(item.TagIds, tagID)
	}
	return rows.Err()
}

func (s *Store) AddTagMembers(ctx context.Context, spaceID, tagID string, subjectIDs []string) (int, error) {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := requireManualTag(ctx, tx, spaceID, tagID); err != nil {
		return 0, err
	}
	affected := 0
	for _, subjectID := range uniqueStrings(subjectIDs) {
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO t_subject_tags (c_space_id, c_tag_id, c_subject_id, c_status) SELECT ?, ?, c_subject_id, 'active' FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, spaceID, tagID, spaceID, subjectID)
		if err != nil {
			return 0, err
		}
		n, _ := result.RowsAffected()
		affected += int(n)
	}
	return affected, tx.Commit()
}

func (s *Store) RemoveTagMembers(ctx context.Context, spaceID, tagID string, subjectIDs []string) (int, error) {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := requireManualTag(ctx, tx, spaceID, tagID); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM t_subject_tags WHERE c_space_id = ? AND c_tag_id = ? AND c_subject_id IN (`+placeholders(len(uniqueStrings(subjectIDs)))+`)`, append([]any{spaceID, tagID}, stringsToAny(uniqueStrings(subjectIDs))...)...)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), tx.Commit()
}

func (s *Store) SetTagMemberStatus(ctx context.Context, spaceID, tagID string, subjectIDs []string, status string) (int, error) {
	if status != metadatastore.TagMemberActive && status != metadatastore.TagMemberInactive {
		return 0, fmt.Errorf("%w: invalid member status %q", metadatastore.ErrTagInvalid, status)
	}
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := requireManualTag(ctx, tx, spaceID, tagID); err != nil {
		return 0, err
	}
	inactiveAt := ""
	if status == metadatastore.TagMemberInactive {
		inactiveAt = formatSQLiteTime(s.nowUTC())
	}
	result, err := tx.ExecContext(ctx, `UPDATE t_subject_tags SET c_status = ?, c_inactive_at = ? WHERE c_space_id = ? AND c_tag_id = ? AND c_subject_id IN (`+placeholders(len(uniqueStrings(subjectIDs)))+`)`, append([]any{status, inactiveAt, spaceID, tagID}, stringsToAny(uniqueStrings(subjectIDs))...)...)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), tx.Commit()
}

func (s *Store) ApplyTagSnapshot(ctx context.Context, spaceID, tagID string, runAt time.Time, items []*pb.TagSnapshotItem) (metadatastore.TagSnapshotResult, error) {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return metadatastore.TagSnapshotResult{}, err
	}
	defer tx.Rollback()
	mode, err := tagMode(ctx, tx, spaceID, tagID)
	if err != nil {
		return metadatastore.TagSnapshotResult{}, err
	}
	ids := make([]string, 0, len(items))
	seen := map[string]bool{}
	result := metadatastore.TagSnapshotResult{}
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.GetSubjectId()) == "" || seen[item.GetSubjectId()] {
			continue
		}
		item.SubjectId = strings.TrimSpace(item.GetSubjectId())
		seen[item.GetSubjectId()] = true
		ids = append(ids, item.GetSubjectId())
		if mode == metadatastore.TagModeAuto {
			subject := &pb.Subject{SpaceId: spaceID, SubjectId: item.GetSubjectId(), SubjectType: item.GetSubjectType(), Name: item.GetName(), Market: item.GetMarket(), Currency: item.GetCurrency(), Timezone: item.GetTimezone(), Status: "active"}
			var raw string
			if err := tx.QueryRowContext(ctx, `SELECT c_attrs_json FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, spaceID, item.GetSubjectId()).Scan(&raw); err == nil {
				if err := unmarshalOptions.Unmarshal([]byte(raw), subject); err != nil {
					return metadatastore.TagSnapshotResult{}, err
				}
				subject.SpaceId = spaceID
				subject.SubjectId = item.GetSubjectId()
				if subject.Status == "" {
					subject.Status = "active"
				}
				if item.GetSubjectType() != "" {
					subject.SubjectType = item.GetSubjectType()
				}
				if item.GetName() != "" {
					subject.Name = item.GetName()
				}
				if item.GetMarket() != "" {
					subject.Market = item.GetMarket()
				}
				if item.GetCurrency() != "" {
					subject.Currency = item.GetCurrency()
				}
				if item.GetTimezone() != "" {
					subject.Timezone = item.GetTimezone()
				}
			} else if err != sql.ErrNoRows {
				return metadatastore.TagSnapshotResult{}, err
			}
			if err := upsertSubject(ctx, tx, subject); err != nil {
				return metadatastore.TagSnapshotResult{}, err
			}
			memberResult, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO t_subject_tags (c_space_id, c_tag_id, c_subject_id, c_status) VALUES (?, ?, ?, 'active')`, spaceID, tagID, item.GetSubjectId())
			if err != nil {
				return metadatastore.TagSnapshotResult{}, err
			}
			n, _ := memberResult.RowsAffected()
			result.Added += int(n)
		}
	}
	if mode == metadatastore.TagModeAuto && len(ids) == 0 {
		return metadatastore.TagSnapshotResult{}, metadatastore.ErrTagSnapshotEmpty
	}
	if len(ids) > 0 {
		resultSet := `(` + placeholders(len(ids)) + `)`
		args := append([]any{spaceID, tagID}, stringsToAny(ids)...)
		res, err := tx.ExecContext(ctx, `UPDATE t_subject_tags SET c_status = 'active', c_inactive_at = '' WHERE c_space_id = ? AND c_tag_id = ? AND c_subject_id IN `+resultSet+` AND c_status <> 'active'`, args...)
		if err != nil {
			return metadatastore.TagSnapshotResult{}, err
		}
		n, _ := res.RowsAffected()
		result.Activated = int(n)
		res, err = tx.ExecContext(ctx, `UPDATE t_subject_tags SET c_status = 'inactive', c_inactive_at = ? WHERE c_space_id = ? AND c_tag_id = ? AND c_status = 'active' AND c_subject_id NOT IN `+resultSet, append([]any{formatSQLiteTime(runAt), spaceID, tagID}, stringsToAny(ids)...)...)
		if err != nil {
			return metadatastore.TagSnapshotResult{}, err
		}
		n, _ = res.RowsAffected()
		result.Inactivated = int(n)
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE t_subject_tags SET c_status = 'inactive', c_inactive_at = ? WHERE c_space_id = ? AND c_tag_id = ? AND c_status = 'active'`, formatSQLiteTime(runAt), spaceID, tagID)
		if err != nil {
			return metadatastore.TagSnapshotResult{}, err
		}
		n, _ := res.RowsAffected()
		result.Inactivated = int(n)
	}
	if mode == metadatastore.TagModeManual {
		// A probing snapshot can only change existing manual members.
		for _, item := range items {
			if item == nil {
				continue
			}
			_, _ = tx.ExecContext(ctx, `UPDATE t_subject_tags SET c_status = 'active', c_inactive_at = '' WHERE c_space_id = ? AND c_tag_id = ? AND c_subject_id = ?`, spaceID, tagID, item.GetSubjectId())
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE t_tags SET c_last_run_at = ?, c_last_status = 'success', c_last_error = '' WHERE c_space_id = ? AND c_tag_id = ?`, formatSQLiteTime(runAt), spaceID, tagID); err != nil {
		return metadatastore.TagSnapshotResult{}, err
	}
	return result, tx.Commit()
}

func (s *Store) ReportTagRunFailure(ctx context.Context, spaceID, tagID string, runAt time.Time, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE t_tags SET c_last_run_at = ?, c_last_status = 'failed', c_last_error = ? WHERE c_space_id = ? AND c_tag_id = ?`, formatSQLiteTime(runAt), message, spaceID, tagID)
	return err
}

func (s *Store) UpdateSubjectAttributes(ctx context.Context, spaceID string, items []*pb.SubjectAttributes) (int, int, error) {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	updated, skipped := 0, 0
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.GetSubjectId()) == "" {
			skipped++
			continue
		}
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT c_attrs_json FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, spaceID, item.GetSubjectId()).Scan(&raw); err != nil {
			if err == sql.ErrNoRows {
				skipped++
				continue
			}
			return 0, 0, err
		}
		subject := &pb.Subject{}
		if err := unmarshalOptions.Unmarshal([]byte(raw), subject); err != nil {
			return 0, 0, err
		}
		for key, value := range item.GetAttributes() {
			if subject.Attributes == nil {
				subject.Attributes = map[string]string{}
			}
			subject.Attributes[key] = value
		}
		if strings.TrimSpace(item.GetName()) != "" {
			subject.Name = strings.TrimSpace(item.GetName())
		}
		encoded, err := marshal(subject)
		if err != nil {
			return 0, 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE t_subjects SET c_name = ?, c_attrs_json = ? WHERE c_space_id = ? AND c_subject_id = ?`, subject.GetName(), encoded, spaceID, item.GetSubjectId()); err != nil {
			return 0, 0, err
		}
		updated++
	}
	return updated, skipped, tx.Commit()
}

func (s *Store) ResolveSubjects(ctx context.Context, spaceID string, tagIDs []string) ([]*pb.Subject, error) {
	ids := uniqueStrings(tagIDs)
	if len(ids) == 0 {
		return []*pb.Subject{}, nil
	}
	rows, err := s.queryDB(ctx).QueryContext(ctx, `SELECT s.c_attrs_json FROM t_subjects s JOIN t_subject_tags m ON m.c_space_id = s.c_space_id AND m.c_subject_id = s.c_subject_id WHERE s.c_space_id = ? AND s.c_status = 'active' AND m.c_status = 'active' AND m.c_tag_id IN (`+placeholders(len(ids))+`) GROUP BY s.c_subject_id, s.c_attrs_json ORDER BY s.c_subject_id`, append([]any{spaceID}, stringsToAny(ids)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*pb.Subject, 0)
	for rows.Next() {
		item := &pb.Subject{}
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := unmarshalOptions.Unmarshal([]byte(raw), item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListDatasetSubjects(ctx context.Context, spaceID, datasetID, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	dataset, err := s.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return nil, nil, err
	}
	ids := uniqueStrings(dataset.GetSubjectTags())
	where := `s.c_space_id = ? AND (? = '' OR s.c_subject_id = ?)`
	args := []any{spaceID, subjectID, subjectID}
	if len(ids) > 0 {
		where += ` AND EXISTS (SELECT 1 FROM t_subject_tags m WHERE m.c_space_id = s.c_space_id AND m.c_subject_id = s.c_subject_id AND m.c_tag_id IN (` + placeholders(len(ids)) + `))`
		args = append(args, stringsToAny(ids)...)
	} else {
		where += ` AND 1 = 0`
	}
	query := `SELECT s.c_subject_id, CASE WHEN EXISTS (SELECT 1 FROM t_subject_tags m WHERE m.c_space_id = s.c_space_id AND m.c_subject_id = s.c_subject_id AND m.c_tag_id IN (` + placeholders(len(ids)) + `) AND m.c_status = 'active') THEN 'active' ELSE 'inactive' END, s.c_attrs_json FROM t_subjects s WHERE ` + where
	// The CASE expression precedes the WHERE clause, so its arguments must be
	// supplied first, followed by the outer subject filter and membership query.
	queryArgs := append([]any{spaceID}, stringsToAny(ids)...)
	queryArgs = append(queryArgs, spaceID, subjectID, subjectID)
	if len(ids) > 0 {
		queryArgs = append(queryArgs, spaceID)
		queryArgs = append(queryArgs, stringsToAny(ids)...)
	}
	// Keep the DatasetSubject compatibility projection small and deterministic.
	return queryDatasetSubjects(ctx, s.queryDB(ctx), query, queryArgs, datasetID, page)
}

func queryDatasetSubjects(ctx context.Context, db readDB, query string, args []any, datasetID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(page)
	rows, err := db.QueryContext(ctx, query+` ORDER BY s.c_subject_id LIMIT ? OFFSET ?`, append(args, size, offset)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.DatasetSubject, 0)
	for rows.Next() {
		item := &pb.DatasetSubject{SpaceId: "", DatasetId: datasetID}
		var raw string
		if err := rows.Scan(&item.SubjectId, &item.Status, &raw); err != nil {
			return nil, nil, err
		}
		subject := &pb.Subject{}
		if err := unmarshalOptions.Unmarshal([]byte(raw), subject); err != nil {
			return nil, nil, err
		}
		item.SpaceId = subject.GetSpaceId()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var total uint32
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM (`+query+`)`, args...).Scan(&total); err != nil {
		return nil, nil, err
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total, HasMore: uint64(offset)+uint64(len(items)) < uint64(total)}, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func formatSQLiteTime(t time.Time) string { return t.UTC().Format(sqliteTimeLayout) }
