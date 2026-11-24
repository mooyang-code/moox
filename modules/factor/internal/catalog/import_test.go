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
