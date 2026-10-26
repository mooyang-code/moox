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
	TaskID            string    `gorm:"column:c_task_id"`
	MemberAlias       string    `gorm:"column:c_member_alias"`
	FactorID          string    `gorm:"column:c_factor_id"`
	Status            string    `gorm:"column:c_status"`
	CarryAlertPeriods int       `gorm:"column:c_carry_alert_periods"`
	Params            string    `gorm:"column:c_params"`
	CreatedAt         time.Time `gorm:"column:c_ctime"`
	UpdatedAt         time.Time `gorm:"column:c_mtime"`
}

func (r memberRow) domain() (domain.FactorTaskMember, error) {
	params, err := decodeMemberParams(r.Params)
	if err != nil {
		return domain.FactorTaskMember{}, fmt.Errorf("member %s/%s: %w", r.TaskID, r.MemberAlias, err)
	}
	return domain.FactorTaskMember{
		TaskID: r.TaskID, MemberAlias: r.MemberAlias, FactorID: r.FactorID, Status: r.Status,
		CarryAlertPeriods: r.CarryAlertPeriods, Params: params, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// encodeMemberParams 把成员固定参数写成规范 JSON（键有序）；没有固定参数存空对象。
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

const memberColumns = `c_task_id, c_member_alias, c_factor_id, c_status, c_carry_alert_periods, c_params, c_ctime, c_mtime`

// joinedMemberRow is one member row joined with its definition.
type joinedMemberRow struct {
	MemberTaskID      string       `gorm:"column:m_task_id"`
	MemberAlias       string       `gorm:"column:m_member_alias"`
	MemberStatus      string       `gorm:"column:m_status"`
	CarryAlertPeriods int          `gorm:"column:m_carry_alert_periods"`
	Params            string       `gorm:"column:m_params"`
	MemberCreatedAt   time.Time    `gorm:"column:m_ctime"`
	MemberUpdatedAt   time.Time    `gorm:"column:m_mtime"`
	Def               factorDefRow `gorm:"embedded"`
}

const joinedMemberColumns = `m.c_task_id AS m_task_id, m.c_member_alias AS m_member_alias, m.c_status AS m_status,
	 m.c_carry_alert_periods AS m_carry_alert_periods, m.c_params AS m_params, m.c_ctime AS m_ctime, m.c_mtime AS m_mtime,
	 d.c_factor_id, d.c_name, d.c_category_id, d.c_spec, d.c_spec_hash, d.c_allow_partial_universe, d.c_ctime, d.c_mtime`

// AddMember attaches a definition to a factor task as a disabled member under alias. params 是成员的固定参数
// （完整参数，之后不变；策略专属任务的成员传 nil，参数由写入方逐期提交）。
func (s *Store) AddMember(ctx context.Context, taskID, alias, factorID string, params map[string]float64) (domain.FactorTaskMember, error) {
	if s == nil || s.db == nil {
		return domain.FactorTaskMember{}, fmt.Errorf("factor database is not open")
	}
	taskID, alias, factorID = strings.TrimSpace(taskID), strings.TrimSpace(alias), strings.TrimSpace(factorID)
	if taskID == "" || alias == "" || factorID == "" {
		return domain.FactorTaskMember{}, fmt.Errorf("set id, member alias and factor id are required")
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
			INSERT INTO t_factor_task_members (c_task_id, c_member_alias, c_factor_id, c_status, c_carry_alert_periods, c_params, c_ctime, c_mtime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, taskID, alias, factorID, domain.MemberStatusDisabled, domain.DefaultCarryAlertPeriods, encoded, now, now).Error
		if err != nil && isUniqueConstraint(err) {
			return fmt.Errorf("%w: member %q already exists in set %q", ErrConflict, alias, taskID)
		}
		return err
	})
	if err != nil {
		return domain.FactorTaskMember{}, err
	}
	return s.GetMember(ctx, taskID, alias)
}

// RemoveMember detaches a disabled member. Enabled members must be disabled first.
func (s *Store) RemoveMember(ctx context.Context, taskID, alias string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	taskID, alias = strings.TrimSpace(taskID), strings.TrimSpace(alias)
	result := s.db.WithContext(ctx).Exec(
		"DELETE FROM t_factor_task_members WHERE c_task_id = ? AND c_member_alias = ? AND c_status = ?",
		taskID, alias, domain.MemberStatusDisabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	member, err := s.GetMember(ctx, taskID, alias)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: member %q of set %q is %s; disable it before removal", ErrConflict, alias, taskID, member.Status)
}

// SetMemberStatus changes a member's status only if it still matches expectedStatus.
func (s *Store) SetMemberStatus(ctx context.Context, taskID, alias, expectedStatus, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	taskID, alias = strings.TrimSpace(taskID), strings.TrimSpace(alias)
	if taskID == "" || alias == "" {
		return fmt.Errorf("set id and member alias are required")
	}
	if !validMemberStatus(expectedStatus) || !validMemberStatus(status) {
		return fmt.Errorf("invalid member status transition %q to %q", expectedStatus, status)
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_task_members SET c_status = ?, c_mtime = ?
		WHERE c_task_id = ? AND c_member_alias = ? AND c_status = ?
	`, status, time.Now().UTC(), taskID, alias, expectedStatus)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	member, err := s.GetMember(ctx, taskID, alias)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: member %q of set %q status is %q, expected %q", ErrConflict, alias, taskID, member.Status, expectedStatus)
}

// SetMemberCarryAlert changes how many carried periods in a row raise an alert.
func (s *Store) SetMemberCarryAlert(ctx context.Context, taskID, alias string, periods int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if periods <= 0 {
		return fmt.Errorf("carry alert periods must be positive")
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_task_members SET c_carry_alert_periods = ?, c_mtime = ? WHERE c_task_id = ? AND c_member_alias = ?
	`, periods, time.Now().UTC(), strings.TrimSpace(taskID), strings.TrimSpace(alias))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetMember returns one membership row.
func (s *Store) GetMember(ctx context.Context, taskID, alias string) (domain.FactorTaskMember, error) {
	if s == nil || s.db == nil {
		return domain.FactorTaskMember{}, fmt.Errorf("factor database is not open")
	}
	var row memberRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT `+memberColumns+` FROM t_factor_task_members WHERE c_task_id = ? AND c_member_alias = ?
	`, strings.TrimSpace(taskID), strings.TrimSpace(alias)).Scan(&row)
	if result.Error != nil {
		return domain.FactorTaskMember{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.FactorTaskMember{}, gorm.ErrRecordNotFound
	}
	return row.domain()
}

// ListMembers returns one set's members joined with their definitions, sorted
// by member alias. An empty status returns members in every status.
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
	query += " ORDER BY m.c_member_alias"
	var rows []joinedMemberRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	members := make([]domain.TaskMember, 0, len(rows))
	for _, row := range rows {
		def, err := row.Def.domain()
		if err != nil {
			return nil, err
		}
		params, err := decodeMemberParams(row.Params)
		if err != nil {
			return nil, fmt.Errorf("member %s/%s: %w", row.MemberTaskID, row.MemberAlias, err)
		}
		members = append(members, domain.TaskMember{
			FactorTaskMember: domain.FactorTaskMember{
				TaskID: row.MemberTaskID, MemberAlias: row.MemberAlias, FactorID: def.FactorID, Status: row.MemberStatus,
				CarryAlertPeriods: row.CarryAlertPeriods, Params: params, CreatedAt: row.MemberCreatedAt, UpdatedAt: row.MemberUpdatedAt,
			},
			Factor: def,
		})
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
	query += " ORDER BY c_factor_id, c_task_id, c_member_alias"
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
			TaskID: row.TaskID, MemberAlias: row.MemberAlias, Status: row.Status, Params: member.Params,
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
