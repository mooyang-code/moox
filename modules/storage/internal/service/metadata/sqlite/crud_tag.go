package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	defaultTagCron     = "0 * * * *"
	defaultTagTimezone = "UTC"
	sqliteTimeLayout   = "2006-01-02 15:04:05"
)

var (
	tagIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	instrumentTypes = map[string]bool{"spot": true, "swap": true, "equity": true, "etf": true, "index": true, "convertible_bond": true}
)

func normalizeTag(item *pb.Tag) error {
	if item == nil {
		return fmt.Errorf("%w: tag is required", metadatastore.ErrTagInvalid)
	}
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.TagId = strings.TrimSpace(item.GetTagId())
	item.TagName = strings.TrimSpace(item.GetTagName())
	item.Mode = strings.ToLower(strings.TrimSpace(item.GetMode()))
	item.InstrumentType = strings.ToLower(strings.TrimSpace(item.GetInstrumentType()))
	item.Cron = strings.TrimSpace(item.GetCron())
	item.Timezone = strings.TrimSpace(item.GetTimezone())
	sources := make([]string, 0, len(item.GetSources()))
	seen := map[string]bool{}
	for _, source := range item.GetSources() {
		source = strings.ToLower(strings.TrimSpace(source))
		if source != "" && !seen[source] {
			seen[source] = true
			sources = append(sources, source)
		}
	}
	item.Sources = sources
	if item.Cron == "" {
		item.Cron = defaultTagCron
	}
	if item.Timezone == "" {
		item.Timezone = defaultTagTimezone
	}
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
	probe := len(item.Sources) > 0 || item.InstrumentType != ""
	if item.Mode == metadatastore.TagModeAuto || probe {
		if len(item.Sources) == 0 || item.InstrumentType == "" {
			return fmt.Errorf("%w: sources and instrument_type must be set together", metadatastore.ErrTagInvalid)
		}
		if !instrumentTypes[item.InstrumentType] {
			return fmt.Errorf("%w: unsupported instrument_type %q", metadatastore.ErrTagInvalid, item.InstrumentType)
		}
	}
	if _, err := cron.ParseStandard(item.Cron); err != nil {
		return fmt.Errorf("%w: cron: %v", metadatastore.ErrTagInvalid, err)
	}
	if _, err := time.LoadLocation(item.Timezone); err != nil {
		return fmt.Errorf("%w: timezone: %v", metadatastore.ErrTagInvalid, err)
	}
	return nil
}

func (s *Store) UpsertTag(ctx context.Context, item *pb.Tag) (*pb.Tag, error) {
	if err := normalizeTag(item); err != nil {
		return nil, err
	}
	sources, err := marshalJSON(item.GetSources())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_tags (c_space_id, c_tag_id, c_tag_name, c_description, c_mode, c_builtin, c_sources_json, c_instrument_type, c_cron, c_timezone)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_tag_id) DO UPDATE SET
			c_tag_name = excluded.c_tag_name,
			c_description = excluded.c_description,
			c_mode = excluded.c_mode,
			c_sources_json = excluded.c_sources_json,
			c_instrument_type = excluded.c_instrument_type,
			c_cron = excluded.c_cron,
			c_timezone = excluded.c_timezone
	`, item.GetSpaceId(), item.GetTagId(), item.GetTagName(), item.GetDescription(), item.GetMode(), boolInt(item.GetBuiltin()), sources, item.GetInstrumentType(), item.GetCron(), item.GetTimezone())
	if err != nil {
		return nil, err
	}
	return s.GetTag(ctx, item.GetSpaceId(), item.GetTagId())
}

const tagSelect = `
	SELECT t.c_space_id, t.c_tag_id, t.c_tag_name, t.c_description, t.c_mode, t.c_builtin,
	       t.c_sources_json, t.c_instrument_type, t.c_cron, t.c_timezone,
	       t.c_last_run_at, t.c_last_status, t.c_last_error, t.c_ctime, t.c_mtime,
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'active'),
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'inactive')
	FROM t_tags t`

func scanTag(row rowScanner) (*pb.Tag, error) {
	item := &pb.Tag{}
	var builtin int
	var sources string
	if err := row.Scan(&item.SpaceId, &item.TagId, &item.TagName, &item.Description, &item.Mode, &builtin,
		&sources, &item.InstrumentType, &item.Cron, &item.Timezone, &item.LastRunAt, &item.LastStatus,
		&item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.ActiveCount, &item.InactiveCount); err != nil {
		return nil, err
	}
	item.Builtin = builtin == 1
	if err := json.Unmarshal([]byte(sources), &item.Sources); err != nil {
		return nil, fmt.Errorf("decode tag sources: %w", err)
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
