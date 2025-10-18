package logic

import (
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/dao"
)

// FunctionPackageService 云函数代码包服务
// COS客户端不再在初始化时创建，而是在异步上传任务执行时动态获取
type FunctionPackageService struct {
	dao         dao.FunctionPackageDAO
	taskService asynctask.Service // 异步任务服务接口

	// 内存缓存优化下载性能
	fileCache     map[int64][]byte
	fileCacheLock sync.RWMutex
	cacheExpiry   map[int64]time.Time
}

// NewFunctionPackageService 创建云函数代码包服务
// COS客户端在异步任务中动态获取，不再需要预先传入
func NewFunctionPackageService(dao dao.FunctionPackageDAO) *FunctionPackageService {
	service := &FunctionPackageService{
		dao:         dao,
		fileCache:   make(map[int64][]byte),
		cacheExpiry: make(map[int64]time.Time),
	}

	return service
}

// SetAsyncTaskService 设置异步任务服务
func (s *FunctionPackageService) SetAsyncTaskService(taskService asynctask.Service) {
	s.taskService = taskService
}
