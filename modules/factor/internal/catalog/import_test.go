package catalog

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorlib"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 整批导入：分类随定义建立；任一因子已存在或编译失败时整批拒绝，什么都不写。
func TestImportCatalogIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	svc := NewService(db, newMetadataFake())

	imported, err := svc.ImportCatalog(ctx, []factorlib.File{
		{Name: "a.yaml", Content: []byte("- {factor_id: ma, name: 均线, category_id: trend, category_name: 趋势, spec: 'output: ts_mean(close, 5)'}\n")},
		{Name: "b.yaml", Content: []byte("- {factor_id: mom, name: 动量, spec: 'output: close / ts_mean(close, 10) - 1'}\n")},
	})
	require.NoError(t, err)
	require.Len(t, imported, 2)
	require.Equal(t, "trend", imported[0].CategoryID)
	categories, err := svc.ListCategories(ctx)
	require.NoError(t, err)
	names := map[string]string{}
	for _, category := range categories {
		names[category.CategoryID] = category.Name
	}
	require.Equal(t, "趋势", names["trend"])

	_, err = svc.ImportCatalog(ctx, []factorlib.File{{Name: "c.yaml", Content: []byte("- {factor_id: fresh, name: 新, spec: 'output: close'}\n- {factor_id: ma, name: 均线, spec: 'output: close'}\n")}})
	require.ErrorContains(t, err, "已存在")
	_, err = svc.ImportCatalog(ctx, []factorlib.File{{Name: "d.yaml", Content: []byte("- {factor_id: fresh, name: 新, spec: 'output: close'}\n- {factor_id: broken, name: 坏, spec: 'output: ts_mean(close,'}\n")}})
	require.ErrorContains(t, err, "broken")
	_, err = db.GetFactor(ctx, "fresh")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 导入因子库时分类创建与因子写入在同一事务内原子执行：已有分类保留人工调整的 sort_order，
// 且后一条因子写库失败（例如引用不存在的分类）时，前一条带入的新分类与改名一并回滚。
func TestImportCatalogPreservesCategorySortOrderAndRollsBackCategoriesAtomically(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	svc := NewService(db, newMetadataFake())

	_, err := svc.UpsertCategory(ctx, domain.FactorCategory{CategoryID: "trend", Name: "趋势旧名", SortOrder: 42})
	require.NoError(t, err)

	// 第二条因子引用不存在且未声明 category_name 的分类，触发外键冲突：整批回滚，包括第一条带来的新分类与已有分类改名。
	_, err = svc.ImportCatalog(ctx, []factorlib.File{
		{Name: "a.yaml", Content: []byte("- {factor_id: f1, name: 因子一, category_id: new_cat, category_name: 新分类, spec: 'output: close'}\n- {factor_id: f2, name: 因子二, category_id: trend, category_name: 趋势新名, spec: 'output: close'}\n- {factor_id: f3, name: 因子三, category_id: missing_cat, spec: 'output: close'}\n")},
	})
	require.Error(t, err)
	_, err = db.GetFactor(ctx, "f1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	categories, err := svc.ListCategories(ctx)
	require.NoError(t, err)
	byID := map[string]domain.FactorCategory{}
	for _, cat := range categories {
		byID[cat.CategoryID] = cat
	}
	require.NotContains(t, byID, "new_cat", "事务回滚后新分类不能残留")
	require.Equal(t, "趋势旧名", byID["trend"].Name, "事务回滚后已有分类名称不能被改写")
	require.Equal(t, 42, byID["trend"].SortOrder)

	// 正常导入带已有分类 category_name 的因子：更新名称但保留已有 sort_order。
	imported, err := svc.ImportCatalog(ctx, []factorlib.File{
		{Name: "ok.yaml", Content: []byte("- {factor_id: f1, name: 因子一, category_id: trend, category_name: 趋势新名, spec: 'output: close'}\n")},
	})
	require.NoError(t, err)
	require.Len(t, imported, 1)
	categories, err = svc.ListCategories(ctx)
	require.NoError(t, err)
	for _, cat := range categories {
		if cat.CategoryID == "trend" {
			require.Equal(t, "趋势新名", cat.Name)
			require.Equal(t, 42, cat.SortOrder, "导入不能把已有分类的 sort_order 覆盖回 0")
		}
	}
}

// 清孤立结果数据集：只删没有任务引用的因子结果数据集。
func TestPurgeResultDatasetOnlyDeletesOrphanFactorResults(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	task, err := svc.CreateTask(ctx, newTask())
	require.NoError(t, err)

	require.ErrorContains(t, svc.PurgeResultDataset(ctx, task.SpaceID, task.ResultDatasetID), task.TaskID)
	require.ErrorContains(t, svc.PurgeResultDataset(ctx, task.SpaceID, "dataset_prices"), "不是因子结果数据集")

	_, err = svc.SetTaskStatus(ctx, task.TaskID, domain.TaskStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteTask(ctx, task.TaskID, false))
	_, err = meta.GetDataset(ctx, task.SpaceID, task.ResultDatasetID)
	require.NoError(t, err)
	require.NoError(t, svc.PurgeResultDataset(ctx, task.SpaceID, task.ResultDatasetID))
	_, err = meta.GetDataset(ctx, task.SpaceID, task.ResultDatasetID)
	require.Error(t, err)
}
