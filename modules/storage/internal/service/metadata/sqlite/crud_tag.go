package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron"
	sqlite "modernc.org/sqlite"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
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
	item.Source = strings.ToLower(strings.TrimSpace(item.GetSource()))
	item.MarketType = strings.ToLower(strings.TrimSpace(item.GetMarketType()))
	item.Cron = strings.TrimSpace(item.GetCron())
	item.Timezone = strings.TrimSpace(item.GetTimezone())
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
	case item.Source == "":
		return fmt.Errorf("%w: source is required", metadatastore.ErrTagInvalid)
	case item.MarketType == "":
		return fmt.Errorf("%w: market_type is required", metadatastore.ErrTagInvalid)
	case !instrumentTypes[item.MarketType]:
		return fmt.Errorf("%w: unsupported market_type %q", metadatastore.ErrTagInvalid, item.MarketType)
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
	if err := s.validateTagSource(ctx, item); err != nil {
		return nil, err
	}
	if existing, err := s.GetTag(ctx, item.GetSpaceId(), item.GetTagId()); err == nil {
		if existing.GetSource() != item.GetSource() || existing.GetMarketType() != item.GetMarketType() {
			return nil, fmt.Errorf("%w: source and market_type are immutable; create a new tag", metadatastore.ErrTagInvalid)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO t_tags (c_space_id, c_tag_id, c_tag_name, c_description, c_mode, c_builtin, c_source_id, c_market_type, c_cron, c_timezone)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_tag_id) DO UPDATE SET
			c_tag_name = excluded.c_tag_name,
			c_description = excluded.c_description,
			c_mode = excluded.c_mode,
			c_cron = excluded.c_cron,
			c_timezone = excluded.c_timezone
		WHERE t_tags.c_source_id = excluded.c_source_id AND t_tags.c_market_type = excluded.c_market_type
	`, item.GetSpaceId(), item.GetTagId(), item.GetTagName(), item.GetDescription(), item.GetMode(), boolInt(item.GetBuiltin()), item.GetSource(), item.GetMarketType(), item.GetCron(), item.GetTimezone())
	if err != nil {
		return nil, err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if affected == 0 {
		return nil, fmt.Errorf("%w: source and market_type are immutable; create a new tag", metadatastore.ErrTagInvalid)
	}
	return s.GetTag(ctx, item.GetSpaceId(), item.GetTagId())
}

// CreateTag inserts a new tag without replacing an existing tag identity.
func (s *Store) CreateTag(ctx context.Context, item *pb.Tag) (*pb.Tag, error) {
	if err := normalizeTag(item); err != nil {
		return nil, err
	}
	if err := s.validateTagSource(ctx, item); err != nil {
		return nil, err
	}
	item.Builtin = false
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO t_tags (c_space_id, c_tag_id, c_tag_name, c_description, c_mode, c_builtin, c_source_id, c_market_type, c_cron, c_timezone)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetTagId(), item.GetTagName(), item.GetDescription(), item.GetMode(), boolInt(item.GetBuiltin()), item.GetSource(), item.GetMarketType(), item.GetCron(), item.GetTimezone())
	if err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
			if strings.Contains(strings.ToLower(sqliteErr.Error()), "c_tag_name") {
				return nil, fmt.Errorf("tag name %q already exists in space %s: %w", item.GetTagName(), item.GetSpaceId(), err)
			}
			return nil, fmt.Errorf("tag %s/%s already exists: %w", item.GetSpaceId(), item.GetTagId(), err)
		}
		return nil, err
	}
	if _, err := result.RowsAffected(); err != nil {
		return nil, err
	}
	return s.GetTag(ctx, item.GetSpaceId(), item.GetTagId())
}

func (s *Store) validateTagSource(ctx context.Context, item *pb.Tag) error {
	source, err := s.GetDataSource(ctx, item.GetSpaceId(), item.GetSource())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: data source %q does not exist in space %q", metadatastore.ErrTagInvalid, item.GetSource(), item.GetSpaceId())
		}
		return err
	}
	if status := strings.ToLower(strings.TrimSpace(source.GetStatus())); status != "active" {
		return fmt.Errorf("%w: data source %q is not active", metadatastore.ErrTagInvalid, item.GetSource())
	}
	// Capability policy is deliberately fail-open while bootstrap metadata is
	// incomplete. The subject-sync process publishes authoritative
	// subject_listing metadata for sources it can enumerate; when that metadata
	// exists we enforce it strictly. Missing metadata is observable and the
	// Collector planner still revalidates the concrete provider/source route
	// before execution, so an unsupported Tag cannot silently dispatch.
	raw := strings.TrimSpace(source.GetAttributes()["subject_listing"])
	if raw == "" {
		log.WarnContextf(ctx, "tag source capability metadata is missing; accepting fail-open space=%s source=%s market_type=%s", item.GetSpaceId(), item.GetSource(), item.GetMarketType())
		return nil
	}
	{
		var listing struct {
			InstrumentTypes []string `json:"instrument_types"`
		}
		if err := json.Unmarshal([]byte(raw), &listing); err != nil {
			return fmt.Errorf("%w: data source %q has invalid subject_listing metadata", metadatastore.ErrTagInvalid, item.GetSource())
		}
		if len(listing.InstrumentTypes) > 0 {
			matched := false
			for _, instrumentType := range listing.InstrumentTypes {
				if strings.EqualFold(strings.TrimSpace(instrumentType), item.GetMarketType()) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("%w: data source %q does not support market_type %q", metadatastore.ErrTagInvalid, item.GetSource(), item.GetMarketType())
			}
		}
	}
	return nil
}

const tagSelect = `
	SELECT t.c_space_id, t.c_tag_id, t.c_tag_name, t.c_description, t.c_mode, t.c_builtin,
	       t.c_source_id, t.c_market_type, t.c_cron, t.c_timezone,
	       t.c_last_run_at, t.c_last_status, t.c_last_error, t.c_ctime, t.c_mtime,
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'active'),
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'inactive')
	FROM t_tags t`

func scanTag(row rowScanner) (*pb.Tag, error) {
	item := &pb.Tag{}
	var builtin int
	if err := row.Scan(&item.SpaceId, &item.TagId, &item.TagName, &item.Description, &item.Mode, &builtin,
		&item.Source, &item.MarketType, &item.Cron, &item.Timezone, &item.LastRunAt, &item.LastStatus,
		&item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.ActiveCount, &item.InactiveCount); err != nil {
		return nil, err
	}
	item.Builtin = builtin == 1
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
