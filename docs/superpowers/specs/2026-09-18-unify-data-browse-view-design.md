# 统一数据展示页设计

## 背景

当前采集器“查看数据”页使用 `DatasetBrowse`，虽然能够读取真实数据，但交互偏旧，缺少历史版本中的 View 切换、筛选和 K 线弹窗。Git 历史中的 `81cab407` 已将 View 浏览页的 K 线流程拆为可复用的 `KlineModal`，当前 `ViewBrowse` 仍保留完整交互。

## 目标

所有进入“查看数据”的空间统一使用 `ViewBrowse` 交互，恢复历史版本的 K 线弹窗，同时继续通过 Storage View 查询真实数据。

## 方案

将 `web/src/views/collector/datasets/index.vue` 中查看数据分支的 `DatasetBrowse` 替换为 `ViewBrowse`，传入采集器范围的 View 归属过滤，并保留现有页面标题插槽和“集合定义 / 查看数据”子 Tab。

ViewBrowse 继续使用现有接口：

- `ListViews` / `ListDatasets` 加载 View 和 Dataset 元数据；
- `QueryTimeSeriesRows` / `SearchRecordRows` 查询真实行；
- `KlineModal` 展示时序数据的 K 线图。

历史数据没有归属属性时，通过 `includeUnowned` 保证仍可见；明确归属于其他模块的因子结果 View 不会混入采集器数据页。没有可用 View 时显示 ViewBrowse 的空状态。

## 交互与边界

- 保留“集合定义 / 查看数据”入口和当前空间选择机制。
- 查看数据中保留 View 标签切换、查询筛选、排序、分页、详情、重建日志和 K 线按钮。
- K 线按钮仅在时序 View 中显示；记录 View 继续使用记录检索交互。
- 不修改 Storage 数据、View 定义或部署配置。

## 测试

- 增加源码契约测试，确认采集器查看数据分支使用 `ViewBrowse`、传入采集器 View 过滤和保留 K 线入口。
- 运行 View 浏览现有 Vitest 契约测试。
- 运行数据管理导航 Playwright 回归测试和生产构建。

