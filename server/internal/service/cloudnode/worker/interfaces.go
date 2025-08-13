package worker

import (
	"context"
	
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
)

// CloudAccountService 云账户服务接口（用于worker）
type CloudAccountService interface {
	// GetAccountWithoutMask 获取云账户（不脱敏，仅供内部使用）
	GetAccountWithoutMask(ctx context.Context, accountID string) (*model.CloudAccount, error)
}

// AsyncTaskService 异步任务服务接口（用于worker）
type AsyncTaskService interface {
	// UpdateTaskDetailStatus 更新任务详情状态
	UpdateTaskDetailStatus(ctx context.Context, taskID, itemID string, status int, errorMessage string) error
}
