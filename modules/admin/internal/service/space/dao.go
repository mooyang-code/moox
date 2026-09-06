package space

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/softdelete"
	"gorm.io/gorm"
)

// DAO 封装 Space 相关表的显式读写操作。
type DAO struct {
	db *gorm.DB
}

// NewDAO 创建 Space DAO。
func NewDAO(db *gorm.DB) *DAO { return &DAO{db: db} }

// CreateSpace 新建 Space。
func (d *DAO) CreateSpace(ctx context.Context, item *Space) error {
	exists, err := d.spaceExists(ctx, item.SpaceID)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("space_id already exists: %s", item.SpaceID)
	}

	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	if item.Status == "" {
		item.Status = "active"
	}
	if item.Attributes == "" {
		item.Attributes = "{}"
	}
	if err := d.db.WithContext(ctx).Create(item).Error; err != nil {
		if isUniqueConstraintError(err) {
			return fmt.Errorf("space_id already exists: %s", item.SpaceID)
		}
		return err
	}
	return nil
}

// UpdateSpace 更新 Space 的管理台展示属性。
func (d *DAO) UpdateSpace(ctx context.Context, item *Space) error {
	if item.SpaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	if item.Status == "" {
		item.Status = "active"
	}
	if item.Attributes == "" {
		item.Attributes = "{}"
	}
	result := d.db.WithContext(ctx).Model(&Space{}).
		Where("c_space_id = ? AND c_is_deleted = ?", item.SpaceID, softdelete.IsDeletedFalse).
		Updates(map[string]interface{}{
			"c_name":        item.Name,
			"c_description": item.Description,
			"c_owner":       item.Owner,
			"c_market":      item.Market,
			"c_timezone":    item.Timezone,
			"c_status":      item.Status,
			"c_attributes":  item.Attributes,
			"c_mtime":       time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("space not found: %s", item.SpaceID)
	}
	return nil
}

// DeleteSpace permanently removes an isolated management Space and its members.
func (d *DAO) DeleteSpace(ctx context.Context, spaceID string) error {
	if strings.TrimSpace(spaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	tx := d.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if err := tx.Exec("DELETE FROM t_space_members WHERE c_space_id = ?", spaceID).Error; err != nil {
		tx.Rollback()
		return err
	}
	result := tx.Exec("DELETE FROM t_spaces WHERE c_space_id = ? AND c_is_deleted = ?", spaceID, softdelete.IsDeletedFalse)
	if result.Error != nil {
		tx.Rollback()
		return result.Error
	}
	if result.RowsAffected == 0 {
		tx.Rollback()
		return fmt.Errorf("space not found: %s", spaceID)
	}
	return tx.Commit().Error
}

// ListSpaces 按 owner/status 分页查询有效 Space。
func (d *DAO) ListSpaces(ctx context.Context, owner string, status string, offset int, limit int) ([]Space, int64, error) {
	query := d.db.WithContext(ctx).Model(&Space{}).Where("c_is_deleted = ?", softdelete.IsDeletedFalse)
	if owner != "" {
		query = query.Where("c_owner = ?", owner)
	}
	if status != "" {
		query = query.Where("c_status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Space
	if err := query.Order("c_mtime DESC, c_id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListSpaceMembers 分页查询指定 Space 下的成员。
func (d *DAO) ListSpaceMembers(ctx context.Context, spaceID string, offset int, limit int) ([]SpaceMember, int64, error) {
	query := d.db.WithContext(ctx).Model(&SpaceMember{}).Where("c_space_id = ?", spaceID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []SpaceMember
	if err := query.Order("c_mtime DESC, c_id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// AuthorizeTradeRequest enforces the Admin BFF's Space boundary. Global
// administrators may operate any active Space; ordinary users must have an
// active membership, and mutating Trade operations require owner/admin role.
func (d *DAO) AuthorizeTradeRequest(ctx context.Context, userID, spaceID, method string, globalRole int32) error {
	userID, spaceID = strings.TrimSpace(userID), strings.TrimSpace(spaceID)
	if userID == "" || spaceID == "" {
		return fmt.Errorf("user_id and space_id are required")
	}
	var item Space
	if err := d.db.WithContext(ctx).Where("c_space_id = ? AND c_is_deleted = ? AND c_status = ?", spaceID, softdelete.IsDeletedFalse, "active").First(&item).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("space is unavailable: %s", spaceID)
		}
		return err
	}
	if globalRole >= 2 {
		return nil
	}
	var member SpaceMember
	if err := d.db.WithContext(ctx).Where("c_space_id = ? AND c_user_id = ? AND c_status = ?", spaceID, userID, "active").First(&member).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("user is not an active member of space")
		}
		return err
	}
	if tradeMethodMutates(method) {
		role := strings.ToLower(strings.TrimSpace(member.Role))
		if role != "owner" && role != "admin" {
			return fmt.Errorf("trade mutation requires space owner or admin role")
		}
	}
	return nil
}

func tradeMethodMutates(method string) bool {
	method = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(method)))
	switch method {
	case "gettradingaccount", "listtradingaccounts", "getlogicalaccount", "listlogicalaccounts", "getoperatoraction", "getlogicalaccounttarget", "getorder", "listorders", "listfills", "listpositions", "getexecutioncapabilities", "queryequitycurve", "listholdings", "getstrategy", "liststrategies", "getrunner", "listrunners", "liststrategyresults", "getstrategyresult", "liststrategytargets", "getstrategyinstance", "liststrategyinstances":
		return false
	default:
		// Mutations are deny-by-default for ordinary Space members. Keeping a
		// read-only allowlist avoids silently exposing a newly added Trade RPC.
		return true
	}
}

func (d *DAO) spaceExists(ctx context.Context, spaceID string) (bool, error) {
	var count int64
	err := d.db.WithContext(ctx).Model(&Space{}).
		Where("c_space_id = ? AND c_is_deleted = ?", spaceID, softdelete.IsDeletedFalse).
		Count(&count).Error
	return count > 0, err
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint") || strings.Contains(message, "constraint failed")
}
