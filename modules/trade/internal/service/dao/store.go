// Package dao 实现 Trade 模块的 gorm 持久化层（service.Store 接口）。
//
// 约定：
//   - 所有查询按 c_space_id 硬隔离；可软删除表过滤 c_is_deleted != 'true'。
//   - API 凭证：CreateAPIKey/ListAPIKeys 走脱敏语义（secret/passphrase 不出参、api_key 脱敏）；
//     GetAPIKey（供适配层调用）返回解密后的明文凭证。
//   - AppendFundFlows 在单个 gorm 事务内追加流水并按 direction 调整余额（乐观锁 c_version），
//     保证「成对划转 / 成交结算」与余额变更原子。
package dao

import (
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
)

// GormStore 是 service.Store 的 gorm 实现。
type GormStore struct {
	db            *gorm.DB
	encryptionKey string
}

// New 创建 DAO。encryptionKey 用于 API 凭证加解密（32 字节）。
func New(db *gorm.DB, encryptionKey string) *GormStore {
	return &GormStore{db: db, encryptionKey: encryptionKey}
}

// DB 暴露底层 gorm 句柄（供需要自定义事务的上层使用）。
func (g *GormStore) DB() *gorm.DB { return g.db }

// 编译期断言：GormStore 实现 service.Store。
var _ service.Store = (*GormStore)(nil)

// validFilter 返回软删除过滤条件片段（调用方拼接到 Where）。
// 这里返回列名，统一用 `c_is_deleted != 'true'` 语义。
func notDeleted() string { return "c_is_deleted != 'true'" }
