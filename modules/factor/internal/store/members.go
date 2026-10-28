package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

type memberRow struct {
	TaskID              string     `gorm:"column:c_task_id"`
	MemberKey           string     `gorm:"column:c_member_key"`
	FactorID            string     `gorm:"column:c_factor_id"`
	Kind                string     `gorm:"column:c_kind"`
	Status              string     `gorm:"column:c_status"`
	Params              string     `gorm:"column:c_params"`
	LastSubmittedParams string     `gorm:"column:c_last_submitted_params"`
	LastSubmittedPeriod int64      `gorm:"column:c_last_submitted_period"`
	LastRequestID       string     `gorm:"column:c_last_request_id"`
	LastSubmittedAt     *time.Time `gorm:"column:c_last_submitted_at"`
	CreatedAt           time.Time  `gorm:"column:c_ctime"`
	UpdatedAt           time.Time  `gorm:"column:c_mtime"`
}

func (r memberRow) domain() (domain.FactorTaskMember, error) {
	params, err := decodeMemberParams(r.Params)
	if err != nil {
		return domain.FactorTaskMember{}, fmt.Errorf("member %s/%s: %w", r.TaskID, r.MemberKey, err)
	}
	submitted, err := domain.ParseParamSnapshot(r.LastSubmittedParams)
	if err != nil {
		return domain.FactorTaskMember{}, fmt.Errorf("member %s/%s: %w", r.TaskID, r.MemberKey, err)
	}
	if len(submitted) == 0 {
		submitted = nil
	}
	return domain.FactorTaskMember{
		TaskID: r.TaskID, MemberKey: r.MemberKey, FactorID: r.FactorID, Kind: r.Kind, Status: r.Status, Params: params,
		LastSubmittedParams: submitted, LastSubmittedPeriod: r.LastSubmittedPeriod, LastRequestID: r.LastRequestID,
		LastSubmittedAt: r.LastSubmittedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// encodeMemberParams 把成员参数写成规范 JSON（键有序）；没有参数存空对象。
func encodeMemberParams(params map[string]float64) (string, error) {
	if len(params) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeMemberParams(raw string) (map[string]float64, error) {
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var params map[string]float64
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("member params is not valid JSON: %w", err)
	}
	return params, nil
}

const memberColumns = `c_task_id, c_member_key, c_factor_id, c_kind, c_status, c_params, c_last_submitted_params,
	c_last_submitted_period, c_last_request_id, c_last_submitted_at, c_ctime, c_mtime`

// joinedMemberRow is one member row joined with its definition.
type joinedMemberRow struct {
	MemberTaskID              string       `gorm:"column:m_task_id"`
	MemberKey                 string       `gorm:"column:m_member_key"`
	MemberKind                string       `gorm:"column:m_kind"`
	MemberStatus              string       `gorm:"column:m_status"`
	Params                    string       `gorm:"column:m_params"`
	MemberLastSubmittedParams string       `gorm:"column:m_last_submitted_params"`
	MemberLastSubmittedPeriod int64        `gorm:"column:m_last_submitted_period"`
	MemberLastRequestID       string       `gorm:"column:m_last_request_id"`
	MemberLastSubmittedAt     *time.Time   `gorm:"column:m_last_submitted_at"`
	MemberCreatedAt           time.Time    `gorm:"column:m_ctime"`
	MemberUpdatedAt           time.Time    `gorm:"column:m_mtime"`
	Def                       factorDefRow `gorm:"embedded"`
}

const joinedMemberColumns = `m.c_task_id AS m_task_id, m.c_member_key AS m_member_key, m.c_kind AS m_kind, m.c_status AS m_status,
	 m.c_params AS m_params, m.c_last_submitted_params AS m_last_submitted_params,
	 m.c_last_submitted_period AS m_last_submitted_period, m.c_last_request_id AS m_last_request_id,
	 m.c_last_submitted_at AS m_last_submitted_at, m.c_ctime AS m_ctime, m.c_mtime AS m_mtime,
	 d.c_factor_id, d.c_name, d.c_category_id, d.c_spec, d.c_spec_hash, d.c_allow_partial_universe, d.c_ctime, d.c_mtime`

// AddMember attaches a definition to a factor task as a disabled member under member key. params 是成员的固定参数，
// 或可变成员的默认参数（完整参数，之后不变）。成员键与类型由调用方按公共命名包推导。
func (s *Store) AddMember(ctx context.Context, taskID, memberKey, factorID, kind string, params map[string]float64) (domain.FactorTaskMember, error) {
	if s == nil || s.db == nil {
		return domain.FactorTaskMember{}, fmt.Errorf("factor database is not open")
	}
	taskID, memberKey, factorID = strings.TrimSpace(taskID), strings.TrimSpace(memberKey), strings.TrimSpace(factorID)
	if taskID == "" || memberKey == "" || factorID == "" {
		return domain.FactorTaskMember{}, fmt.Errorf("task id, member key and factor id are required")
	}
	if kind != domain.MemberKindFixed && kind != domain.MemberKindVar {
		return domain.FactorTaskMember{}, fmt.Errorf("invalid member kind %q", kind)
	}
	encoded, err := encodeMemberParams(params)
	if err != nil {
		return domain.FactorTaskMember{}, err
	}
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for table, column := range map[string]struct{ name, value string }{
			"t_factor_tasks": {"c_task_id", taskID}, "t_factor_defs": {"c_factor_id", factorID},
		} {
			var count int64
			if err := tx.Raw("SELECT COUNT(*) FROM "+table+" WHERE "+column.name+" = ?", column.value).Scan(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		err := tx.Exec(`
			INSERT INTO t_factor_task_members (c_task_id, c_member_key, c_factor_id, c_kind, c_status, c_params, c_ctime, c_mtime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, taskID, memberKey, factorID, kind, domain.MemberStatusDisabled, encoded, now, now).Error
		if err != nil && isUniqueConstraint(err) {
			if kind == domain.MemberKindVar {
				return fmt.Errorf("%w: factor %q already has a var member in task %q", ErrConflict, factorID, taskID)
			}
			return fmt.Errorf("%w: member %q already exists in task %q", ErrConflict, memberKey, taskID)
		}
		return err
	})
	if err != nil {
		return domain.FactorTaskMember{}, err
	}
	return s.GetMember(ctx, taskID, memberKey)
}

// RemoveMember detaches a disabled member. Enabled members must be disabled first.
func (s *Store) RemoveMember(ctx context.Context, taskID, memberKey string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	taskID, memberKey = strings.TrimSpace(taskID), strings.TrimSpace(memberKey)
	result := s.db.WithContext(ctx).Exec(
		"DELETE FROM t_factor_task_members WHERE c_task_id = ? AND c_member_key = ? AND c_status = ?",
		taskID, memberKey, domain.MemberStatusDisabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	member, err := s.GetMember(ctx, taskID, memberKey)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: member %q of task %q is %s; disable it before removal", ErrConflict, memberKey, taskID, member.Status)
}

// SetMemberStatus changes a member's status only if it still matches expectedStatus.
func (s *Store) SetMemberStatus(ctx context.Context, taskID, memberKey, expectedStatus, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	taskID, memberKey = strings.TrimSpace(taskID), strings.TrimSpace(memberKey)
	if taskID == "" || memberKey == "" {
		return fmt.Errorf("task id and member key are required")
	}
	if !validMemberStatus(expectedStatus) || !validMemberStatus(status) {
		return fmt.Errorf("invalid member status transition %q to %q", expectedStatus, status)
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_task_members SET c_status = ?, c_mtime = ?
		WHERE c_task_id = ? AND c_member_key = ? AND c_status = ?
	`, status, time.Now().UTC(), taskID, memberKey, expectedStatus)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	member, err := s.GetMember(ctx, taskID, memberKey)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: member %q of task %q status is %q, expected %q", ErrConflict, memberKey, taskID, member.Status, expectedStatus)
}

// GetMember returns one membership row.
func (s *Store) GetMember(ctx context.Context, taskID, memberKey string) (domain.FactorTaskMember, error) {
	if s == nil || s.db == nil {
		return domain.FactorTaskMember{}, fmt.Errorf("factor database is not open")
	}
	var row memberRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT `+memberColumns+` FROM t_factor_task_members WHERE c_task_id = ? AND c_member_key = ?
	`, strings.TrimSpace(taskID), strings.TrimSpace(memberKey)).Scan(&row)
	if result.Error != nil {
		return domain.FactorTaskMember{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.FactorTaskMember{}, gorm.ErrRecordNotFound
	}
	return row.domain()
}

// ListMembers returns one set's members joined with their definitions, sorted
// by member key. An empty status returns members in every status.
func (s *Store) ListMembers(ctx context.Context, taskID, status string) ([]domain.TaskMember, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := `SELECT ` + joinedMemberColumns + `
		FROM t_factor_task_members m JOIN t_factor_defs d ON d.c_factor_id = m.c_factor_id
		WHERE m.c_task_id = ?`
	args := []any{strings.TrimSpace(taskID)}
	if status = strings.TrimSpace(status); status != "" {
		query += " AND m.c_status = ?"
		args = append(args, status)
	}
	query += " ORDER BY m.c_member_key"
	var rows []joinedMemberRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	members := make([]domain.TaskMember, 0, len(rows))
	for _, row := range rows {
		def, err := s.resolveDef(ctx, s.db, row.Def)
		if err != nil {
			return nil, err
		}
		member, err := memberRow{
			TaskID: row.MemberTaskID, MemberKey: row.MemberKey, FactorID: def.FactorID, Kind: row.MemberKind, Status: row.MemberStatus,
			Params: row.Params, LastSubmittedParams: row.MemberLastSubmittedParams, LastSubmittedPeriod: row.MemberLastSubmittedPeriod,
			LastRequestID: row.MemberLastRequestID, LastSubmittedAt: row.MemberLastSubmittedAt,
			CreatedAt: row.MemberCreatedAt, UpdatedAt: row.MemberUpdatedAt,
		}.domain()
		if err != nil {
			return nil, err
		}
		members = append(members, domain.TaskMember{FactorTaskMember: member, Factor: def})
	}
	return members, nil
}

// ListUsages maps each definition to the set members that reference it. With no
// factor ids it returns the usages of every definition.
func (s *Store) ListUsages(ctx context.Context, factorIDs ...string) (map[string][]domain.FactorUsage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := "SELECT " + memberColumns + " FROM t_factor_task_members"
	var args []any
	if len(factorIDs) > 0 {
		query += " WHERE c_factor_id IN ?"
		args = append(args, factorIDs)
	}
	query += " ORDER BY c_factor_id, c_task_id, c_member_key"
	var rows []memberRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	usages := make(map[string][]domain.FactorUsage)
	for _, row := range rows {
		member, err := row.domain()
		if err != nil {
			return nil, err
		}
		usages[row.FactorID] = append(usages[row.FactorID], domain.FactorUsage{
			TaskID: row.TaskID, MemberKey: row.MemberKey, Status: row.Status, Params: member.Params,
		})
	}
	return usages, nil
}

// CountMembers returns how many members a set has in any status.
func (s *Store) CountMembers(ctx context.Context, taskID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("factor database is not open")
	}
	var count int64
	if err := s.db.WithContext(ctx).Raw(
		"SELECT COUNT(*) FROM t_factor_task_members WHERE c_task_id = ?", strings.TrimSpace(taskID)).Scan(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

func validMemberStatus(status string) bool {
	return status == domain.MemberStatusEnabled || status == domain.MemberStatusDisabled
}
