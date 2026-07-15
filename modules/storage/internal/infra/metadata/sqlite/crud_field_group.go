package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Store) UpsertFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error) {
	raw, err := s.prepareFieldGroup(ctx, item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_field_groups (c_space_id, c_group_id, c_name, c_description, c_parent_group_id, c_sort_order, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT(c_space_id, c_group_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_parent_group_id = excluded.c_parent_group_id,
			c_sort_order = excluded.c_sort_order,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetGroupId(), item.GetName(), item.GetDescription(), item.GetParentGroupId(), item.GetSortOrder(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetFieldGroup(ctx, item.GetSpaceId(), item.GetGroupId())
}

func (s *Store) CreateFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error) {
	raw, err := s.prepareFieldGroup(ctx, item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_field_groups (c_space_id, c_group_id, c_name, c_description, c_parent_group_id, c_sort_order, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)
	`, item.GetSpaceId(), item.GetGroupId(), item.GetName(), item.GetDescription(), item.GetParentGroupId(), item.GetSortOrder(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetFieldGroup(ctx, item.GetSpaceId(), item.GetGroupId())
}

func (s *Store) UpdateFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error) {
	raw, err := s.prepareFieldGroup(ctx, item)
	if err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE t_field_groups SET c_name = ?, c_description = ?, c_parent_group_id = NULLIF(?, ''),
			c_sort_order = ?, c_status = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_group_id = ?
	`, item.GetName(), item.GetDescription(), item.GetParentGroupId(), item.GetSortOrder(), item.GetStatus(), raw, item.GetSpaceId(), item.GetGroupId())
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, sql.ErrNoRows
	}
	return s.GetFieldGroup(ctx, item.GetSpaceId(), item.GetGroupId())
}

func (s *Store) prepareFieldGroup(ctx context.Context, item *pb.FieldGroup) (string, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetGroupId() == "" || item.GetName() == "" {
		return "", errors.New("space_id, group_id and name are required")
	}
	if item.GetParentGroupId() == item.GetGroupId() {
		return "", errors.New("field group cannot be its own parent")
	}
	if item.GetParentGroupId() != "" {
		parent, err := s.GetFieldGroup(ctx, item.GetSpaceId(), item.GetParentGroupId())
		if err != nil {
			return "", err
		}
		if parent.GetParentGroupId() != "" {
			return "", errors.New("field groups support at most two levels")
		}
		children, _, err := s.ListFieldGroups(ctx, item.GetSpaceId(), item.GetGroupId(), nil)
		if err != nil {
			return "", err
		}
		if len(children) > 0 {
			return "", errors.New("a field group with children cannot become a child group")
		}
	}
	item.Status = defaultStatus(item.GetStatus())
	return marshal(item)
}

func (s *Store) GetFieldGroup(ctx context.Context, spaceID string, groupID string) (*pb.FieldGroup, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_field_groups WHERE c_space_id = ? AND c_group_id = ?`, []any{spaceID, groupID}, func() *pb.FieldGroup { return &pb.FieldGroup{} })
}

func (s *Store) ListFieldGroups(ctx context.Context, spaceID string, parentGroupID string, page *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_field_groups
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR COALESCE(c_parent_group_id, '') = ?)
		ORDER BY c_space_id, COALESCE(c_parent_group_id, ''), c_sort_order, c_group_id
	`, []any{spaceID, spaceID, parentGroupID, parentGroupID}, func() *pb.FieldGroup { return &pb.FieldGroup{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) CountFieldsByGroup(ctx context.Context, spaceID string) (coremetadata.FieldGroupCounts, error) {
	result := coremetadata.FieldGroupCounts{ByGroup: make(map[string]uint64)}
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.c_group_id, COALESCE(g.c_parent_group_id, ''), COUNT(f.c_field_id)
		FROM t_field_groups g
		LEFT JOIN t_fields f ON f.c_space_id = g.c_space_id AND f.c_group_id = g.c_group_id
		WHERE g.c_space_id = ?
		GROUP BY g.c_group_id, g.c_parent_group_id
	`, spaceID)
	if err != nil {
		return result, err
	}
	parents := make(map[string]string)
	for rows.Next() {
		var groupID, parentID string
		var count uint64
		if err := rows.Scan(&groupID, &parentID, &count); err != nil {
			_ = rows.Close()
			return result, err
		}
		parents[groupID] = parentID
		result.ByGroup[groupID] = count
		result.Total += count
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	for groupID, parentID := range parents {
		if parentID != "" {
			result.ByGroup[parentID] += result.ByGroup[groupID]
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_fields WHERE c_space_id = ? AND COALESCE(c_group_id, '') = ''`, spaceID).Scan(&result.Ungrouped); err != nil {
		return result, err
	}
	result.Total += result.Ungrouped
	return result, nil
}

func (s *Store) BatchUpdateFields(ctx context.Context, spaceID string, fieldIDs []string, targetGroupID string, targetStatus string) (uint32, error) {
	if spaceID == "" || len(fieldIDs) == 0 {
		return 0, errors.New("space_id and field_ids are required")
	}
	if len(fieldIDs) > 100 {
		return 0, errors.New("at most 100 fields can be updated at once")
	}
	if targetGroupID == "" && targetStatus == "" {
		return 0, errors.New("target_group_id or target_status is required")
	}
	if targetStatus != "" && targetStatus != "active" && targetStatus != "disabled" {
		return 0, errors.New("target_status must be active or disabled")
	}
	seen := make(map[string]struct{}, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		if strings.TrimSpace(fieldID) == "" {
			return 0, errors.New("field_ids cannot contain empty values")
		}
		if _, ok := seen[fieldID]; ok {
			return 0, fmt.Errorf("duplicate field_id %q", fieldID)
		}
		seen[fieldID] = struct{}{}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if targetGroupID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_field_groups WHERE c_space_id = ? AND c_group_id = ?`, spaceID, targetGroupID).Scan(&exists); err != nil {
			return 0, err
		}
		if exists != 1 {
			return 0, fmt.Errorf("target field group %q does not exist", targetGroupID)
		}
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(fieldIDs)), ",")
	args := make([]any, 0, len(fieldIDs)+1)
	args = append(args, spaceID)
	for _, fieldID := range fieldIDs {
		args = append(args, fieldID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT c_attrs_json FROM t_fields WHERE c_space_id = ? AND c_field_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	fields := make([]*pb.Field, 0, len(fieldIDs))
	for rows.Next() {
		field := &pb.Field{}
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if err := unmarshalOptions.Unmarshal([]byte(raw), field); err != nil {
			_ = rows.Close()
			return 0, err
		}
		fields = append(fields, field)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(fields) != len(fieldIDs) {
		return 0, errors.New("one or more fields do not exist in the requested space")
	}
	for _, field := range fields {
		if targetGroupID != "" {
			field.GroupId = targetGroupID
		}
		if targetStatus != "" {
			field.Status = targetStatus
		}
		field.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		raw, err := marshal(field)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE t_fields SET c_group_id = ?, c_status = ?, c_attrs_json = ? WHERE c_space_id = ? AND c_field_id = ?`, field.GetGroupId(), field.GetStatus(), raw, spaceID, field.GetFieldId()); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return uint32(len(fields)), nil
}

func (s *Store) DeleteFieldGroup(ctx context.Context, spaceID string, groupID string) error {
	if spaceID == "" || groupID == "" {
		return errors.New("space_id and group_id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_field_groups WHERE c_space_id = ? AND c_group_id = ?`, spaceID, groupID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("field group %q not found: %w", groupID, sql.ErrNoRows)
	}
	var children, fields int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_field_groups WHERE c_space_id = ? AND c_parent_group_id = ?`, spaceID, groupID).Scan(&children); err != nil {
		return err
	}
	if children > 0 {
		return errors.New("field group has child groups")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_fields WHERE c_space_id = ? AND c_group_id = ?`, spaceID, groupID).Scan(&fields); err != nil {
		return err
	}
	if fields > 0 {
		return errors.New("field group contains fields")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_field_groups WHERE c_space_id = ? AND c_group_id = ?`, spaceID, groupID); err != nil {
		return err
	}
	return tx.Commit()
}
