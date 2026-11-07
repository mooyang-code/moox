package setup

import (
	"errors"
	"sync"

	"gorm.io/gorm"
)

const tencentSecretID = "tencent-default"

var (
	ErrConflict = errors.New("setup_conflict")
	ErrStorage  = errors.New("setup_storage_failed")
	ErrInvalid  = errors.New("setup_invalid")
)

type Admin struct {
	Username string
	Password string
}

type TencentCloud struct {
	SecretID  string
	SecretKey string
}

type Space struct {
	SpaceID        string
	Name           string
	Description    string
	Owner          string
	Market         string
	Timezone       string
	Status         string
	AttributesJSON string
}

// ExchangeKey 是 moox.toml 的一条 [[exchange_keys]]。
type ExchangeKey struct {
	Name        string
	Exchange    string
	Environment string
	APIKey      string
	SecretKey   string
	Passphrase  string
	ReadOnly    bool
}

// 交易所密钥在 SecretMgr 里的状态。交易所密钥只建不改：库里有这个 ID（包括已在管理台软删除的）就不再写入。
const (
	ExchangeKeyCreated = "created" // 本次新建
	ExchangeKeyExists  = "exists"  // 已存在，以管理台为准，不覆盖
	ExchangeKeyDeleted = "deleted" // 已在管理台删除，不重建
	ExchangeKeyMissing = "missing" // 还没有（只出现在状态查询里）
)

// ExchangeKeyResult 是一把交易所密钥的处理结果。
type ExchangeKeyResult struct {
	SecretID string
	State    string
	ReadOnly bool
}

type Manifest struct {
	Admin        Admin
	TencentCloud TencentCloud
	Spaces       []Space
	ExchangeKeys []ExchangeKey
}

type Result struct {
	Action  string
	Users   int
	Secrets int
	Spaces  int
	// SpacesCreated、SpacesUnchanged 只统计业务空间。
	SpacesCreated   int
	SpacesUnchanged int
	ExchangeKeys    []ExchangeKeyResult
}

type Status struct {
	State   string
	Users   int
	Secrets int
	Spaces  int
	// Missing 只统计真正缺失的记录；交易所密钥“已存在”和“已在管理台删除”都算完成。
	Missing      int
	Conflicts    int
	ExchangeKeys []ExchangeKeyResult
}

// Service coordinates the idempotent initial data setup flow.
// Implementation details live in impl.go so this file remains the public contract.
type Service struct {
	db            *gorm.DB
	encryptionKey string
	writeHook     func(string) error
	applyMu       sync.Mutex
}

func NewService(db *gorm.DB, encryptionKey string) *Service {
	return &Service{db: db, encryptionKey: encryptionKey}
}
