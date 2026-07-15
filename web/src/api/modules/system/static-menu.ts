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

  directory("02", "0", "/data/sources", "data-assets", "data-assets", 2, { svgIcon: "folder-menu", icon: "" }),
  menu("0201", "02", "/data/sources", "data-sources", "data-sources", "data/sources/index", 1),
  menu("0202", "02", "/data/subjects", "data-subjects", "data-subjects", "data/subjects/index", 2),
  menu("0203", "02", "/data/fields", "data-fields", "data-fields", "data/fields/index", 3),

  directory("03", "0", "/collector/datasets", "compute-collector", "compute-collector", 3, { svgIcon: "functions", icon: "" }),
  menu("0305", "03", "/collector/datasets", "collector-datasets", "collector-datasets", "collector/datasets/index", 1),
  menu("0306", "03", "/collector/views", "collector-views", "collector-views", "collector/views/index", 2),
  menu("0303", "03", "/collector/rules", "collector-rules", "collector-rules", "collector/collector-rules/collector-rules", 3),
  menu("0304", "03", "/collector/tasks", "collector-tasks", "collector-tasks", "collector/task-instances/task-instances", 4),
  menu("0301", "03", "/collector/cloudnodes", "collector-cloudnodes", "collector-cloudnodes", "collector/cloud-node/cloud-node", 5),
  menu("0302", "03", "/collector/packages", "collector-packages", "collector-packages", "collector/cloud-node/function-package-manage", 6),

  directory("0240", "0", "/factor/definitions", "factor-compute", "factor-compute", 4, { svgIcon: "experiment", icon: "" }),
  menu("024001", "0240", "/factor/definitions", "factor-definitions", "factor-definitions", "factor/definitions/index", 1),
  menu("024002", "0240", "/factor/bindings", "factor-bindings", "factor-bindings", "factor/bindings/index", 2),
  menu("024004", "0240", "/factor/results", "factor-results", "factor-results", "factor/results/index", 3),

  directory("0250", "0", "/strategy/overview", "strategy", "strategy", 5, { svgIcon: "mind-mapping", icon: "" }),
  menu("025001", "0250", "/strategy/overview", "strategy-overview", "strategy-overview", "strategy/overview/index", 1),
  menu("025002", "0250", "/strategy/running", "strategy-running", "strategy-running", "strategy/running/index", 2),

  directory("05", "0", "/trading/accounts", "trading", "trading", 6, { svgIcon: "balance-inquiry", icon: "" }),
  menu("0501", "05", "/trading/accounts", "trading-accounts", "trading-accounts", "trading/account-overview/account-overview", 1),
  menu("0502", "05", "/trading/positions", "trading-positions", "trading-positions", "trading/position-detail/position-detail", 2),
  menu("0503", "05", "/trading/orders", "trading-orders", "trading-orders", "trading/trade-record/trade-record", 3),

  directory("06", "0", "/ops/hosts", "ops", "ops", 7, { svgIcon: "defend", icon: "" }),
  menu("0601", "06", "/ops/hosts", "ops-hosts", "ops-hosts", "ops/host-workbench/index", 1, { svgIcon: "computer", icon: "" }),
  menu("0600", "06", "/ops/services", "ops-services", "ops-services", "ops/service-management/index", 2, { svgIcon: "cloud-server", icon: "" }),
  directory("0606", "06", "/ops/storage/nodes", "ops-storage", "ops-storage", 3),
  menu("060601", "0606", "/ops/storage/nodes", "ops-storage-nodes", "ops-storage-nodes", "ops/storage/nodes", 1),
  menu("060602", "0606", "/ops/storage/routes", "ops-storage-routes", "ops-storage-routes", "ops/storage/routes", 2),
  menu("060603", "0606", "/ops/storage/archive", "ops-storage-archive", "ops-storage-archive", "ops/storage/archive", 3),

  directory("07", "0", "/settings/spaces", "settings", "settings", 8, { svgIcon: "set", icon: "" }),
  menu("0701", "07", "/settings/spaces", "settings-spaces", "settings-spaces", "settings/spaces/index", 1),
  menu("0702", "07", "/settings/secrets", "settings-secrets", "settings-secrets", "settings/secrets/index", 2),
  menu("0703", "07", "/settings/service-deployments", "settings-service-deployments", "settings-service-deployments", "settings/service-deployments/index", 3, { hide: true })
];
