package bootstrap

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/mooyang-code/moox/modules/collector/internal/ruleseed"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// builtinTasksPath 是随发布安装的内置采集任务种子（config/setup/collection-tasks.yaml 的副本）。
const builtinTasksPath = "./config/collection-tasks.yaml"

// seedBuiltinTasks 在全新安装（任务表为空）时写入内置采集任务；没有种子文件时什么也不做。
// 任务 ID 固定，因子集等配置依赖这些 ID。
func seedBuiltinTasks(ctx context.Context, repo *store.TaskRepository, path string) error {
	tasks, err := ruleseed.LoadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(errors.Unwrap(err), os.ErrNotExist) {
			return nil
		}
		return err
	}
	summary, err := ruleseed.SeedIfEmpty(ctx, repo, tasks)
	if err != nil {
		return err
	}
	if summary.TasksCreated > 0 {
		log.InfoContextf(ctx, "全新安装：写入内置采集任务 %d 个", summary.TasksCreated)
	}
	return nil
}
