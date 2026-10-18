package service

import (
	"context"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// sanitizeFilePrefix 把数据集名变成文件名前缀：去掉所有空白，路径不安全或控制字符换成下划线，去掉开头的点；
// 结果为空时返回空串（调用方改用数据集 ID）。
func sanitizeFilePrefix(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
		case unicode.IsControl(r), strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), ".")
}

// chooseFilePrefix 为数据集选择文件名前缀：用数据集名去空格；为空或与空间内其他任务的前缀重名时，
// 退回到「名称_数据集 ID」或数据集 ID，保证不同数据集的文件不会互相覆盖。
func (s *Service) chooseFilePrefix(ctx context.Context, spaceID, datasetID, name string) (string, error) {
	tasks, _, err := s.store.ListTasks(ctx, spaceID, 100000, 0)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, t := range tasks {
		if t.DatasetID != datasetID && t.FilePrefix != "" {
			taken[t.FilePrefix] = true
		}
	}
	base := sanitizeFilePrefix(name)
	for _, candidate := range []string{base, base + "_" + datasetID, datasetID} {
		if candidate != "" && candidate != "_"+datasetID && !taken[candidate] {
			return candidate, nil
		}
	}
	return datasetID, nil
}

// fillFilePrefix 给升级前创建的任务补上文件名前缀（之后不再随数据集改名变化）。
func (s *Service) fillFilePrefix(ctx context.Context, task store.Task, name string) error {
	prefix, err := s.chooseFilePrefix(ctx, task.SpaceID, task.DatasetID, name)
	if err != nil {
		return err
	}
	return s.store.SetFilePrefixIfEmpty(ctx, task.SpaceID, task.DatasetID, prefix)
}

// ensureFilePrefixes 在派发运行之前尽量给所有缺前缀的任务补上文件名前缀；某个任务补不上（比如数据集已删除）只记日志，
// 不影响其他任务——领取时会跳过仍没有前缀的任务。
func (s *Service) ensureFilePrefixes(ctx context.Context) {
	tasks, err := s.store.TasksWithoutFilePrefix(ctx)
	if err != nil {
		log.WarnContextf(ctx, "archive: list tasks without file prefix: %v", err)
		return
	}
	for _, task := range tasks {
		ds, err := s.datasets.GetDataset(ctx, task.SpaceID, task.DatasetID)
		if err != nil {
			log.WarnContextf(ctx, "archive: resolve file prefix of %s/%s: %v", task.SpaceID, task.DatasetID, err)
			continue
		}
		if err := s.fillFilePrefix(ctx, task, ds.Name); err != nil {
			log.WarnContextf(ctx, "archive: set file prefix of %s/%s: %v", task.SpaceID, task.DatasetID, err)
		}
	}
}
