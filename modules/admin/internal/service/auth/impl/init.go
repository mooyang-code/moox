package impl

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/activitylog"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
)

// AuthServiceImpl 认证服务实现
type AuthServiceImpl struct {
	cfg     *config.Config
	userDAO *dao.UserDAO
	// sessions 是浏览器会话表，与控制台共用同一份。
	sessions *session.Store
	failures *loginFailures
	// loginLog 把成功登录写进全局活动日志；为空时不记录。
	loginLog *activitylog.DAO
	now      func() time.Time
}

// InitAuthServiceImpl 初始化认证服务实现
func InitAuthServiceImpl(cfg *config.Config, dbManager *database.Manager, sessions *session.Store) (*AuthServiceImpl, error) {
	db := dbManager.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库连接未初始化")
	}
	if cfg == nil || sessions == nil {
		return nil, fmt.Errorf("认证服务需要配置与会话表")
	}
	placeholderPasswordHash()
	return &AuthServiceImpl{
		cfg:      cfg,
		userDAO:  dao.NewUserDAO(db),
		sessions: sessions,
		failures: newLoginFailures(),
		loginLog: activitylog.NewDAO(db),
		now:      time.Now,
	}, nil
}
