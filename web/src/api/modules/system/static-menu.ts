const baseMeta = {
  hide: false,
  disable: false,
  affix: false,
  link: "",
  iframe: false,
  icon: "icon-menu",
  sort: 1,
  type: 2
};

const menu = (
  id: string,
  parentId: string,
  path: string,
  name: string,
  title: string,
  component: string,
  sort: number,
  extra: Record<string, unknown> = {}
) => ({
  id,
  parentId,
  path,
  name,
  component,
  meta: { ...baseMeta, title, sort, ...extra },
  children: null
});

const directory = (
  id: string,
  parentId: string,
  path: string,
  name: string,
  title: string,
  sort: number,
  extra: Record<string, unknown> = {}
) => ({
  id,
  parentId,
  path,
  name,
  redirect: path,
  meta: { ...baseMeta, title, sort, type: 1, ...extra },
  children: null
});

export const systemMenu = [
  menu("01", "0", "/home", "home", "首页", "home/home", 1, { affix: true, svgIcon: "home", icon: "" }),

  // 最常用的“采集任务”排第一、作为目录落地页；数据源、数据对象、基础字段排在后面。
  directory("03", "0", "/collector/tasks", "compute-collector", "数据采集", 2, {
    svgIcon: "functions",
    icon: ""
  }),
  // 目录下的菜单项会替换 route.ts 的同名路由（已在浏览器核对）：采集任务的页签与筛选、字段页的筛选都在 query 里，query 变化时复用页面。
  menu("0303", "03", "/collector/tasks", "collector-tasks", "采集任务", "collector/task-management/index", 1, {
    reuseOnQuery: true
  }),
  menu("0306", "03", "/data/sources", "data-sources", "数据源", "data/sources/index", 3),
  // 页签和所选标签在 query 里：query 变化时复用页面
  menu("0307", "03", "/data/subjects", "data-subjects", "数据对象", "data/subjects/index", 4, { reuseOnQuery: true }),
  menu("0308", "03", "/data/fields", "data-fields", "基础字段", "data/fields/index", 5, { reuseOnQuery: true }),

  directory("0240", "0", "/factor/overview", "factor-compute", "因子计算", 3, { svgIcon: "experiment", icon: "" }),
  menu("024001", "0240", "/factor/overview", "factor-overview", "因子总览", "factor/overview/index", 1),
  // 菜单路由会覆盖 route.ts 里的同名声明，reuseOnQuery 必须写在这里：query 变化时复用页面，不丢未进 URL 的筛选。
  menu("024002", "0240", "/factor/definitions", "factor-definitions", "因子定义", "factor/definitions/index", 2, {
    reuseOnQuery: true
  }),
  menu("024003", "0240", "/factor/tasks", "factor-tasks", "计算任务", "factor/task-management/index", 3, {
    reuseOnQuery: true
  }),

  directory("0250", "0", "/strategy/overview", "strategy", "策略管理", 4, { svgIcon: "mind-mapping", icon: "" }),
  menu("025001", "0250", "/strategy/overview", "strategy-overview", "策略总览", "strategy/overview/index", 1),
  menu("025002", "0250", "/strategy/definitions", "strategy-definitions", "策略定义", "strategy/definitions/index", 2, {
    reuseOnQuery: true
  }),
  menu("025003", "0250", "/strategy/tasks", "strategy-tasks", "策略运行", "strategy/workbench/index", 3, {
    reuseOnQuery: true
  }),

  directory("05", "0", "/trading/paper", "trading", "交易管理", 5, { svgIcon: "balance-inquiry", icon: "" }),
  // 三个环境各一个菜单，共用同一个列表页，meta.env 写明环境；菜单路由会覆盖 route.ts 里的同名声明，两处都要写。
  // 子页签（?list=）在 query 里，query 变化时复用页面。
  menu("0501", "05", "/trading/paper", "trading-paper", "模拟账户", "trading/accounts/index", 1, {
    reuseOnQuery: true,
    env: "paper"
  }),
  menu("0502", "05", "/trading/testnet", "trading-testnet", "测试账户", "trading/accounts/index", 2, {
    reuseOnQuery: true,
    env: "testnet"
  }),
  menu("0503", "05", "/trading/live", "trading-live", "实盘账户", "trading/accounts/index", 3, {
    reuseOnQuery: true,
    env: "prod"
  }),
  // 下架名单是全局的，不分环境和空间，单独一个菜单。
  menu("0504", "05", "/trading/blocked", "trading-blocked", "下架名单", "trading/blocked/index", 4),

  // 数据归档是一级菜单，排在交易之后、事件中心之前。
  directory("058", "0", "/archive/tasks", "archive", "数据归档", 6, { svgIcon: "folder-menu", icon: "" }),
  // 任务详情、页签、筛选都在 query 里，query 变化时复用页面。
  menu("05801", "058", "/archive/tasks", "archive-tasks", "归档任务", "archive/tasks/index", 1, { reuseOnQuery: true }),
  menu("05802", "058", "/archive/storage", "archive-storage", "归档存储", "archive/storage/index", 2),

  // 事件中心是一级菜单（只有这一页），排在数据归档之后、运维之前。页签（事件 / 订阅 / 执行记录）、执行记录的筛选和打开的记录
  // 都在 query 里（告警会直接带失败筛选跳进来），query 变化时复用页面。
  menu("055", "0", "/events", "events", "事件中心", "events/index", 7, { svgIcon: "bell", icon: "", reuseOnQuery: true }),

  directory("06", "0", "/ops/monitor", "ops", "资源与运维", 8, { svgIcon: "defend", icon: "" }),
  // 展开状态、页签、筛选、定位的主机都在 query 里：query 变化时复用页面（菜单与 route.ts 两处都写）
  menu("0607", "06", "/ops/monitor", "ops-monitor", "监控告警", "ops/monitor/index", 1, { reuseOnQuery: true }),
  menu("0601", "06", "/ops/hosts", "ops-hosts", "主机工作台", "ops/host-workbench/index", 3, { reuseOnQuery: true }),
  menu("0600", "06", "/ops/deployments", "ops-deployments", "服务", "ops/deployments/index", 2, {
    reuseOnQuery: true
  }),
  menu("0608", "06", "/ops/activity", "ops-activity", "活动日志", "ops/activity/index", 4),
  menu("0609", "06", "/ops/logs", "ops-logs", "重要日志", "ops/log-records/index", 5),

  directory("07", "0", "/settings/spaces", "settings", "系统设置", 9, { svgIcon: "set", icon: "" }),
  menu("0701", "07", "/settings/spaces", "settings-spaces", "空间管理", "settings/spaces/index", 1),
  menu("0702", "07", "/settings/secrets", "settings-secrets", "密钥管理", "settings/secrets/index", 2)
];
