package asynctask

import (
	"context"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask/model"
)

// JobCompletionHandler Job完成处理器接口
// 用于在Job完成时执行自定义的后处理逻辑
type JobCompletionHandler interface {
	// CanHandle 判断是否处理该TaskType
	// 返回true表示该Handler可以处理此类型的Job
	CanHandle(taskType string) bool

	// OnJobCompleted Job完成时的回调
	// job: 已完成的Job信息
	// firstTask: Job中的任一Task（用于获取TaskType等信息）
	// 返回error将被记录日志，但不会导致重试
	OnJobCompleted(ctx context.Context, job *model.AsyncJob, firstTask *model.AsyncJobTask) error
}
