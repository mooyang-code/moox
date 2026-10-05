package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

type memberRow struct {
	SetID     string    `gorm:"column:c_set_id"`
	FactorID  string    `gorm:"column:c_factor_id"`
	Status    string    `gorm:"column:c_status"`
	CreatedAt time.Time `gorm:"column:c_ctime"`
	UpdatedAt time.Time `gorm:"column:c_mtime"`
}

func (r memberRow) domain() domain.FactorSetMember {
	return domain.FactorSetMember{
		SetID: r.SetID, FactorID: r.FactorID, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// joinedMemberRow is one member row joined with its definition.
type joinedMemberRow struct {
	MemberStatus    string       `gorm:"column:m_status"`
	MemberCreatedAt time.Time    `gorm:"column:m_ctime"`
	MemberUpdatedAt time.Time    `gorm:"column:m_mtime"`
	MemberSetID     string       `gorm:"column:m_set_id"`
	Def             factorDefRow `gorm:"embedded"`
}

const joinedMemberColumns = `m.c_set_id AS m_set_id, m.c_status AS m_status, m.c_ctime AS m_ctime, m.c_mtime AS m_mtime,
	 d.c_factor_id, d.c_name, d.c_factor_type, d.c_source_code, d.c_source_hash,
	 d.c_input_columns_json, d.c_outputs_json, d.c_params_json, d.c_lookback_periods,
	 d.c_allow_partial_universe, d.c_ctime, d.c_mtime`

// AddMember attaches a definition to a factor set as a disabled member.
func (s *Store) AddMember(ctx context.Context, setID, factorID string) (domain.FactorSetMember, error) {
	if s == nil || s.db == nil {
		return domain.FactorSetMember{}, fmt.Errorf("factor database is not open")
	}
	setID, factorID = strings.TrimSpace(setID), strings.TrimSpace(factorID)
	if setID == "" || factorID == "" {
		return domain.FactorSetMember{}, fmt.Errorf("set id and factor id are required")
	}
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for table, column := range map[string]struct{ name, value string }{
			"t_factor_sets": {"c_set_id", setID}, "t_factor_defs": {"c_factor_id", factorID},
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
			INSERT INTO t_factor_set_members (c_set_id, c_factor_id, c_status, c_ctime, c_mtime)
			VALUES (?, ?, ?, ?, ?)
		`, setID, factorID, domain.MemberStatusDisabled, now, now).Error
		if err != nil && isUniqueConstraint(err) {
			return fmt.Errorf("%w: factor %q is already a member of set %q", ErrConflict, factorID, setID)
		}
		return err
	})
	if err != nil {
		return domain.FactorSetMember{}, err
	}
	return s.GetMember(ctx, setID, factorID)
}

// RemoveMember detaches a disabled member. Enabled members must be disabled first.
func (s *Store) RemoveMember(ctx context.Context, setID, factorID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	setID, factorID = strings.TrimSpace(setID), strings.TrimSpace(factorID)
	result := s.db.WithContext(ctx).Exec(
		"DELETE FROM t_factor_set_members WHERE c_set_id = ? AND c_factor_id = ? AND c_status = ?",
		setID, factorID, domain.MemberStatusDisabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	member, err := s.GetMember(ctx, setID, factorID)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: member %q of set %q is %s; disable it before removal", ErrConflict, factorID, setID, member.Status)
}

// SetMemberStatus changes a member's status only if it still matches expectedStatus.
func (s *Store) SetMemberStatus(ctx context.Context, setID, factorID, expectedStatus, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	setID, factorID = strings.TrimSpace(setID), strings.TrimSpace(factorID)
	if setID == "" || factorID == "" {
		return fmt.Errorf("set id and factor id are required")
	}
	if !validMemberStatus(expectedStatus) || !validMemberStatus(status) {
		return fmt.Errorf("invalid member status transition %q to %q", expectedStatus, status)
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_set_members SET c_status = ?, c_mtime = ?
		WHERE c_set_id = ? AND c_factor_id = ? AND c_status = ?
	`, status, time.Now().UTC(), setID, factorID, expectedStatus)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	member, err := s.GetMember(ctx, setID, factorID)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: member %q of set %q status is %q, expected %q", ErrConflict, factorID, setID, member.Status, expectedStatus)
}

// GetMember returns one membership row.
func (s *Store) GetMember(ctx context.Context, setID, factorID string) (domain.FactorSetMember, error) {
	if s == nil || s.db == nil {
		return domain.FactorSetMember{}, fmt.Errorf("factor database is not open")
	}
	var row memberRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT c_set_id, c_factor_id, c_status, c_ctime, c_mtime FROM t_factor_set_members
		WHERE c_set_id = ? AND c_factor_id = ?
	`, strings.TrimSpace(setID), strings.TrimSpace(factorID)).Scan(&row)
	if result.Error != nil {
		return domain.FactorSetMember{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.FactorSetMember{}, gorm.ErrRecordNotFound
	}
	return row.domain(), nil
}

// ListMembers returns one set's members joined with their definitions, sorted
// by factor id. An empty status returns members in every status.
func (s *Store) ListMembers(ctx context.Context, setID, status string) ([]domain.SetMember, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := `SELECT ` + joinedMemberColumns + `
		FROM t_factor_set_members m JOIN t_factor_defs d ON d.c_factor_id = m.c_factor_id
		WHERE m.c_set_id = ?`
	args := []any{strings.TrimSpace(setID)}
	if status = strings.TrimSpace(status); status != "" {
		query += " AND m.c_status = ?"
		args = append(args, status)
	}
	query += " ORDER BY m.c_factor_id"
	var rows []joinedMemberRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	members := make([]domain.SetMember, 0, len(rows))
	for _, row := range rows {
		def, err := row.Def.domain()
		if err != nil {
			return nil, err
		}
		members = append(members, domain.SetMember{
			FactorSetMember: domain.FactorSetMember{
				SetID: row.MemberSetID, FactorID: def.FactorID, Status: row.MemberStatus,
				CreatedAt: row.MemberCreatedAt, UpdatedAt: row.MemberUpdatedAt,
			},
			Factor: def,
		})
	}
	return members, nil
}

// ListUsages maps each definition to the sets that reference it. With no
// factor ids it returns the usages of every definition.
func (s *Store) ListUsages(ctx context.Context, factorIDs ...string) (map[string][]domain.FactorUsage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := "SELECT c_set_id, c_factor_id, c_status, c_ctime, c_mtime FROM t_factor_set_members"
	var args []any
	if len(factorIDs) > 0 {
		query += " WHERE c_factor_id IN ?"
		args = append(args, factorIDs)
	}
	query += " ORDER BY c_factor_id, c_set_id"
	var rows []memberRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	usages := make(map[string][]domain.FactorUsage)
	for _, row := range rows {
		usages[row.FactorID] = append(usages[row.FactorID], domain.FactorUsage{SetID: row.SetID, Status: row.Status})
	}
	return usages, nil
}

// CountMembers returns how many members a set has in any status.
func (s *Store) CountMembers(ctx context.Context, setID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("factor database is not open")
	}
	var count int64
	if err := s.db.WithContext(ctx).Raw(
		"SELECT COUNT(*) FROM t_factor_set_members WHERE c_set_id = ?", strings.TrimSpace(setID)).Scan(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

func validMemberStatus(status string) bool {
	return status == domain.MemberStatusEnabled || status == domain.MemberStatusDisabled
}
