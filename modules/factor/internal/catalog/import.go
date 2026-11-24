package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorlib"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"gorm.io/gorm"
)

// ImportCatalog 一次导入一个因子库目录（各文件按调用方给的顺序解析）：任一因子已存在、解析或编译失败时整批拒绝，
// 定义在一个事务里写入；带 category_name 的分类先建立或更新。锁顺序与单个创建相同：自定义算子锁 → 定义锁（升序）。
func (s *Service) ImportCatalog(ctx context.Context, files []factorlib.File) ([]domain.FactorDef, error) {
	entries, err := factorlib.ParseFiles(files)
	if err != nil {
		return nil, err
	}
	usesUDF := false
	for _, entry := range entries {
		usesUDF = usesUDF || len(domain.UDFNames(entry.Spec)) > 0
	}
	if usesUDF {
		unlockUdf, err := s.locks.LockUdfContext(ctx)
		if err != nil {
			return nil, err
		}
		defer unlockUdf()
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.FactorID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		unlock, err := s.locks.LockFactorContext(ctx, id)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	prepared := make([]domain.FactorDef, 0, len(entries))
	for _, entry := range entries {
		if _, err := s.db.GetFactor(ctx, entry.FactorID); err == nil {
			return nil, fmt.Errorf("因子 %s 已存在（%s），改定义请用 factor update", entry.FactorID, entry.File)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		def, err := s.withUDFs(ctx, domain.FactorDef{
			FactorID: entry.FactorID, Name: entry.Name, CategoryID: entry.CategoryID, Spec: entry.Spec, AllowPartialUniverse: entry.AllowPartialUniverse,
		})
		if err != nil {
			return nil, fmt.Errorf("因子 %s（%s）：%w", entry.FactorID, entry.File, err)
		}
		factor, err := domain.NormalizeFactorDefinition(def)
		if err != nil {
			return nil, fmt.Errorf("因子 %s（%s）：%w", entry.FactorID, entry.File, err)
		}
		prepared = append(prepared, factor)
	}
	for _, entry := range entries {
		if entry.CategoryID == "" || entry.CategoryName == "" {
			continue
		}
		if _, err := s.db.UpsertCategory(ctx, domain.FactorCategory{CategoryID: entry.CategoryID, Name: entry.CategoryName}); err != nil {
			return nil, fmt.Errorf("因子 %s 的分类 %s：%w", entry.FactorID, entry.CategoryID, err)
		}
	}
	if err := s.db.CreateFactors(ctx, prepared); err != nil {
		return nil, err
	}
	out := make([]domain.FactorDef, 0, len(prepared))
	for _, factor := range prepared {
		stored, err := s.db.GetFactor(ctx, factor.FactorID)
		if err != nil {
			return nil, err
		}
		out = append(out, stored)
	}
	return out, nil
}

// PurgeResultDataset 删除一个孤立的因子结果数据集（行与元数据），例如重建因子库后遗留的旧结果。
// 只允许因子结果数据集；仍是某个因子任务的结果数据集时拒绝。
func (s *Service) PurgeResultDataset(ctx context.Context, spaceID, datasetID string) error {
	spaceID, datasetID = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID)
	if spaceID == "" || datasetID == "" {
		return errors.New("space_id 和 dataset_id 不能为空")
	}
	if s.metadata == nil {
		return errors.New("缺少 Storage 元数据客户端，不能删除结果数据集")
	}
	tasks, err := s.db.ListTasks(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.SpaceID == spaceID && task.ResultDatasetID == datasetID {
			return fmt.Errorf("数据集 %s 仍是因子任务 %s 的结果数据集", datasetID, task.TaskID)
		}
	}
	dataset, err := s.metadata.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("读取数据集 %s：%w", datasetID, err)
	}
	if dataset.Attributes["dataset_role"] != storageio.DatasetRoleFactorResult {
		return fmt.Errorf("数据集 %s 不是因子结果数据集", datasetID)
	}
	if err := s.metadata.DeleteDataset(ctx, spaceID, datasetID); err != nil {
		return fmt.Errorf("删除结果数据集 %s：%w", datasetID, err)
	}
	return nil
}
