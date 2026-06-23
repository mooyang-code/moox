const baseMeta = {
  hide: false,
  disable: false,
  keepAlive: true,
  affix: false,
  link: "",
  iframe: false,
  roles: ["admin", "common"],
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
  extra: Record<string, unknown> = {},
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
  extra: Record<string, unknown> = {},
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

  directory("02", "0", "/data/overview", "data-assets", "data-assets", 2, { svgIcon: "folder-menu", icon: "" }),
  // 二级分组：数据建模
  directory("0210", "02", "/data/sources", "data-modeling", "data-modeling", 1),
  menu("021001", "0210", "/data/sources", "data-sources", "data-sources", "data/sources/index", 1),
  menu("021002", "0210", "/data/subjects", "data-subjects", "data-subjects", "data/subjects/index", 2),
  menu("021003", "0210", "/data/datasets", "data-datasets", "data-datasets", "data/datasets/index", 3),
  menu("021004", "0210", "/data/fields", "data-fields", "data-fields", "data/fields/index", 4),
  menu("021005", "0210", "/data/factors", "data-factors", "data-factors", "data/factors/index", 5),
  // 二级分组：查询视图
  directory("0220", "02", "/data/views", "data-views", "data-views", 2),
  menu("022001", "0220", "/data/views", "data-view-list", "data-view-list", "data/views/index", 1),
  menu("022002", "0220", "/data/view-browse", "data-view-browse", "data-view-browse", "data/view-browse/index", 2),
  // 二级分组：数据管理
  directory("0230", "02", "/data/overview", "data-mgmt", "data-mgmt", 3),
  menu("023001", "0230", "/data/overview", "data-overview", "data-overview", "data/overview/overview", 1),
  menu("023002", "0230", "/data/browse", "data-browse", "data-browse", "data/browse/index", 2),
  menu("023003", "0230", "/data/import", "data-import", "data-import", "data/import/index", 3),

  directory("03", "0", "/collector/functions", "compute-collector", "compute-collector", 3, { svgIcon: "functions", icon: "" }),
  menu("0301", "03", "/collector/functions", "collector-functions", "collector-functions", "collector/cloud-function/cloud-function", 1),
  menu("0302", "03", "/collector/packages", "collector-packages", "collector-packages", "collector/cloud-function/function-package-manage", 2),
  menu("0303", "03", "/collector/rules", "collector-rules", "collector-rules", "collector/collector-rules/collector-rules", 3),
  menu("0304", "03", "/collector/tasks", "collector-tasks", "collector-tasks", "collector/task-instances/task-instances", 4),

  directory("04", "0", "/strategy/list", "strategy", "strategy", 4, { svgIcon: "data-queries", icon: "" }),
  menu("0401", "04", "/strategy/list", "strategy-list", "strategy-list", "strategy/strategy-list/strategy-list", 1),

  directory("05", "0", "/trading/accounts", "trading", "trading", 5, { svgIcon: "balance-inquiry", icon: "" }),
  menu("0501", "05", "/trading/accounts", "trading-accounts", "trading-accounts", "trading/account-overview/account-overview", 1),
  menu("0502", "05", "/trading/positions", "trading-positions", "trading-positions", "trading/position-detail/position-detail", 2),
  menu("0503", "05", "/trading/orders", "trading-orders", "trading-orders", "trading/trade-record/trade-record", 3),

  directory("06", "0", "/ops/resource-monitor", "ops", "ops", 6, { svgIcon: "defend", icon: "" }),
  menu("0601", "06", "/ops/resource-monitor", "ops-resource-monitor", "ops-resource-monitor", "container/resource-monitor/resource-monitor", 1),
  menu("0602", "06", "/ops/service-status", "ops-service-status", "ops-service-status", "container/service-status/service-status", 2),
  menu("0603", "06", "/ops/ssh-hosts", "ops-ssh-hosts", "ops-ssh-hosts", "container/ssh-hosts/ssh-hosts", 3),
  menu("0604", "06", "/ops/ssh-terminal", "ops-ssh-terminal", "ops-ssh-terminal", "container/ssh-terminal/ssh-terminal", 4, { keepAlive: false }),
  menu("0605", "06", "/ops/ssh-sessions", "ops-ssh-sessions", "ops-ssh-sessions", "container/ssh-sessions/ssh-sessions", 5),
  directory("0606", "06", "/ops/storage/nodes", "ops-storage", "ops-storage", 6),
  menu("060601", "0606", "/ops/storage/nodes", "ops-storage-nodes", "ops-storage-nodes", "ops/storage/nodes", 1),
  menu("060602", "0606", "/ops/storage/routes", "ops-storage-routes", "ops-storage-routes", "ops/storage/routes", 2),
  menu("060603", "0606", "/ops/storage/archive", "ops-storage-archive", "ops-storage-archive", "ops/storage/archive", 3),

  directory("07", "0", "/settings/spaces", "settings", "settings", 7, { svgIcon: "set", icon: "" }),
  menu("0701", "07", "/settings/spaces", "settings-spaces", "settings-spaces", "settings/spaces/index", 1),
  menu("0702", "07", "/settings/permissions", "settings-permissions", "settings-permissions", "settings/permissions/index", 2)
];

export const permissionData: any[] = [];
