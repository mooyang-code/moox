package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	sqlite "modernc.org/sqlite"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const sqliteTimeLayout = "2006-01-02 15:04:05"

var tagIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func normalizeTag(item *pb.Tag) error {
	if item == nil {
		return fmt.Errorf("%w: tag is required", metadatastore.ErrTagInvalid)
	}
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.TagId = strings.TrimSpace(item.GetTagId())
	item.TagName = strings.TrimSpace(item.GetTagName())
	item.Mode = strings.ToLower(strings.TrimSpace(item.GetMode()))
	switch {
	case item.SpaceId == "":
		return fmt.Errorf("%w: space_id is required", metadatastore.ErrTagInvalid)
	case !tagIDPattern.MatchString(item.TagId):
		return fmt.Errorf("%w: tag_id must be lower_snake_case", metadatastore.ErrTagInvalid)
	case item.TagName == "":
		return fmt.Errorf("%w: tag_name is required", metadatastore.ErrTagInvalid)
	case item.Mode != metadatastore.TagModeAuto && item.Mode != metadatastore.TagModeManual:
		return fmt.Errorf("%w: mode must be auto or manual", metadatastore.ErrTagInvalid)
	}
	return nil
}

func tagAttributesJSON(item *pb.Tag) (string, error) {
	raw, err := json.Marshal(item.GetAttributes())
	if err != nil {
		return "", err
	}
	if string(raw) == "null" {
		return "{}", nil
	}
	return string(raw), nil
}

func (s *Store) UpsertTag(ctx context.Context, item *pb.Tag) (*pb.Tag, error) {
	if err := normalizeTag(item); err != nil {
		return nil, err
	}
	attrs, err := tagAttributesJSON(item)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO t_tags (c_space_id, c_tag_id, c_tag_name, c_description, c_mode, c_builtin, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_tag_id) DO UPDATE SET
			c_tag_name = excluded.c_tag_name,
			c_description = excluded.c_description,
			c_mode = excluded.c_mode,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetTagId(), item.GetTagName(), item.GetDescription(), item.GetMode(), boolInt(item.GetBuiltin()), attrs); err != nil {
		return nil, err
	}
	return s.GetTag(ctx, item.GetSpaceId(), item.GetTagId())
}

// CreateTag inserts a new tag without replacing an existing tag identity.
func (s *Store) CreateTag(ctx context.Context, item *pb.Tag) (*pb.Tag, error) {
	if err := normalizeTag(item); err != nil {
		return nil, err
	}
	attrs, err := tagAttributesJSON(item)
	if err != nil {
		return nil, err
	}
	item.Builtin = false
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO t_tags (c_space_id, c_tag_id, c_tag_name, c_description, c_mode, c_builtin, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetTagId(), item.GetTagName(), item.GetDescription(), item.GetMode(), boolInt(item.GetBuiltin()), attrs); err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
			if strings.Contains(strings.ToLower(sqliteErr.Error()), "c_tag_name") {
				return nil, fmt.Errorf("tag name %q already exists in space %s: %w", item.GetTagName(), item.GetSpaceId(), err)
			}
			return nil, fmt.Errorf("tag %s/%s already exists: %w", item.GetSpaceId(), item.GetTagId(), err)
		}
		return nil, err
	}
	return s.GetTag(ctx, item.GetSpaceId(), item.GetTagId())
}

const tagSelect = `
	SELECT t.c_space_id, t.c_tag_id, t.c_tag_name, t.c_description, t.c_mode, t.c_builtin,
	       t.c_attrs_json, t.c_ctime, t.c_mtime,
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'active'),
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'inactive')
	FROM t_tags t`

func scanTag(row rowScanner) (*pb.Tag, error) {
	item := &pb.Tag{}
	var builtin int
	var attrs string
	if err := row.Scan(&item.SpaceId, &item.TagId, &item.TagName, &item.Description, &item.Mode, &builtin,
		&attrs, &item.CreatedAt, &item.UpdatedAt, &item.ActiveCount, &item.InactiveCount); err != nil {
		return nil, err
	}
	item.Builtin = builtin == 1
	if attrs != "" && attrs != "{}" {
		if err := json.Unmarshal([]byte(attrs), &item.Attributes); err != nil {
			return nil, fmt.Errorf("decode tag attributes: %w", err)
		}
	}
	return item, nil
}

func (s *Store) GetTag(ctx context.Context, spaceID, tagID string) (*pb.Tag, error) {
	return scanTag(s.queryDB(ctx).QueryRowContext(ctx, tagSelect+` WHERE t.c_space_id = ? AND t.c_tag_id = ?`, spaceID, tagID))
}

func (s *Store) ListTags(ctx context.Context, spaceID string, page *pb.Page) ([]*pb.Tag, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(page)
	db := s.queryDB(ctx)
	var total uint64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_tags WHERE (? = '' OR c_space_id = ?)`, spaceID, spaceID).Scan(&total); err != nil {
		return nil, nil, err
	}
	rows, err := db.QueryContext(ctx, tagSelect+` WHERE (? = '' OR t.c_space_id = ?) ORDER BY t.c_space_id, t.c_builtin DESC, t.c_tag_id LIMIT ? OFFSET ?`, spaceID, spaceID, size, offset)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.Tag, 0)
	for rows.Next() {
		item, err := scanTag(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: uint32(total)}, rows.Err()
}

func (s *Store) DeleteTag(ctx context.Context, spaceID, tagID string) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var builtin int
	if err := tx.QueryRowContext(ctx, `SELECT c_builtin FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tagID).Scan(&builtin); err != nil {
		return err
	}
	if builtin == 1 {
		return fmt.Errorf("%w: %s", metadatastore.ErrTagBuiltin, tagID)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT d.c_dataset_id, d.c_name, COALESCE(json_extract(d.c_attrs_json, '$.attributes.collector_task_id'), '')
		FROM t_datasets d
		WHERE d.c_space_id = ? AND EXISTS (SELECT 1 FROM json_each(d.c_subject_tags_json) j WHERE j.value = ?)
		ORDER BY d.c_dataset_id`, spaceID, tagID)
	if err != nil {
		return err
	}
	refs := make([]*pb.TagReference, 0)
	for rows.Next() {
		ref := &pb.TagReference{}
		if err := rows.Scan(&ref.DatasetId, &ref.DatasetName, &ref.CollectorTaskId); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(refs) > 0 {
		return &metadatastore.TagReferencedError{TagID: tagID, References: refs}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tagID); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeSubjectTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

func validateSubjectTagsExist(ctx context.Context, db execQueryRower, spaceID string, tags []string) error {
	for _, tag := range tags {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tag).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: subject tag %s does not exist", metadatastore.ErrTagInvalid, tag)
		}
	}
	return nil
}
