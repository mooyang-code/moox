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
  menu("01", "0", "/home", "home", "home", "home/home", 1, { affix: true, svgIcon: "home", icon: "" }),

  // 最常用的“采集任务”排第一、作为目录落地页；数据源、数据对象、基础字段排在后面。
  directory("03", "0", "/collector/tasks", "compute-collector", "compute-collector", 2, {
    svgIcon: "functions",
    icon: ""
  }),
  // 目录下的菜单项会替换 route.ts 的同名路由（已在浏览器核对）：采集任务的页签与筛选、字段页的筛选都在 query 里，query 变化时复用页面。
  menu("0303", "03", "/collector/tasks", "collector-tasks", "collector-tasks", "collector/task-management/index", 1, {
    reuseOnQuery: true
  }),
  menu("0306", "03", "/data/sources", "data-sources", "data-sources", "data/sources/index", 3),
  // 页签和所选标签在 query 里：query 变化时复用页面
  menu("0307", "03", "/data/subjects", "data-subjects", "data-subjects", "data/subjects/index", 4, { reuseOnQuery: true }),
  menu("0308", "03", "/data/fields", "data-fields", "data-fields", "data/fields/index", 5, { reuseOnQuery: true }),

  directory("0240", "0", "/factor/overview", "factor-compute", "factor-compute", 3, { svgIcon: "experiment", icon: "" }),
  menu("024001", "0240", "/factor/overview", "factor-overview", "factor-overview", "factor/overview/index", 1),
  // 菜单路由会覆盖 route.ts 里的同名声明，reuseOnQuery 必须写在这里：query 变化时复用页面，不丢未进 URL 的筛选。
  menu("024002", "0240", "/factor/definitions", "factor-definitions", "factor-definitions", "factor/definitions/index", 2, {
    reuseOnQuery: true
  }),
  menu("024003", "0240", "/factor/tasks", "factor-tasks", "factor-tasks", "factor/task-management/index", 3, {
    reuseOnQuery: true
  }),

  directory("0250", "0", "/strategy/overview", "strategy", "strategy", 4, { svgIcon: "mind-mapping", icon: "" }),
  menu("025001", "0250", "/strategy/overview", "strategy-overview", "strategy-overview", "strategy/overview/index", 1),
  menu(
    "025002",
    "0250",
    "/strategy/definitions",
    "strategy-definitions",
    "strategy-definitions",
    "strategy/definitions/index",
    2,
    { reuseOnQuery: true }
  ),
  menu("025003", "0250", "/strategy/tasks", "strategy-tasks", "strategy-tasks", "strategy/workbench/index", 3, {
    reuseOnQuery: true
  }),

  directory("05", "0", "/trading/accounts", "trading", "trading", 5, { svgIcon: "balance-inquiry", icon: "" }),
  // 菜单路由会覆盖 route.ts 里的同名声明：环境页签用 ?env=，query 变化时复用页面。
  menu("0501", "05", "/trading/accounts", "trading-accounts", "trading-accounts", "trading/accounts/index", 1, {
    reuseOnQuery: true
  }),

  // 数据归档是一级菜单，排在交易之后、事件中心之前。
  directory("058", "0", "/archive/tasks", "archive", "archive", 6, { svgIcon: "folder-menu", icon: "" }),
  // 任务详情、页签、筛选都在 query 里，query 变化时复用页面。
  menu("05801", "058", "/archive/tasks", "archive-tasks", "archive-tasks", "archive/tasks/index", 1, { reuseOnQuery: true }),
  menu("05802", "058", "/archive/storage", "archive-storage", "archive-storage", "archive/storage/index", 2),

  // 事件中心是一级菜单（只有这一页），排在数据归档之后、运维之前。页签（事件 / 订阅 / 执行记录）、执行记录的筛选和打开的记录
  // 都在 query 里（告警会直接带失败筛选跳进来），query 变化时复用页面。
  menu("055", "0", "/events", "events", "events", "events/index", 7, { svgIcon: "bell", icon: "", reuseOnQuery: true }),

  directory("06", "0", "/ops/monitor", "ops", "ops", 8, { svgIcon: "defend", icon: "" }),
  // 展开状态、页签、筛选、定位的主机都在 query 里：query 变化时复用页面（菜单与 route.ts 两处都写）
  menu("0607", "06", "/ops/monitor", "ops-monitor", "ops-monitor", "ops/monitor/index", 1, { reuseOnQuery: true }),
  menu("0601", "06", "/ops/hosts", "ops-hosts", "ops-hosts", "ops/host-workbench/index", 3, { reuseOnQuery: true }),
  menu("0600", "06", "/ops/deployments", "ops-deployments", "ops-deployments", "ops/deployments/index", 2, {
    reuseOnQuery: true
  }),
  menu("0606", "06", "/ops/storage/nodes", "ops-storage", "ops-storage", "ops/storage/nodes", 4),
  menu("0608", "06", "/ops/activity", "ops-activity", "ops-activity", "ops/activity/index", 5),

  directory("07", "0", "/settings/spaces", "settings", "settings", 9, { svgIcon: "set", icon: "" }),
  menu("0701", "07", "/settings/spaces", "settings-spaces", "settings-spaces", "settings/spaces/index", 1),
  menu("0702", "07", "/settings/secrets", "settings-secrets", "settings-secrets", "settings/secrets/index", 2)
];
