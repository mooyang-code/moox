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

  directory("03", "0", "/data/sources", "compute-collector", "compute-collector", 2, {
    svgIcon: "functions",
    icon: ""
  }),
  menu("0306", "03", "/data/sources", "data-sources", "data-sources", "data/sources/index", 1),
  menu("0307", "03", "/data/subjects", "data-subjects", "data-subjects", "data/subjects/index", 2),
  menu("0308", "03", "/data/fields", "data-fields", "data-fields", "data/fields/index", 3),
  menu("0303", "03", "/collector/tasks", "collector-tasks", "collector-tasks", "collector/task-management/index", 4),

  directory("0240", "0", "/factor/overview", "factor-compute", "factor-compute", 3, { svgIcon: "experiment", icon: "" }),
  menu("024001", "0240", "/factor/overview", "factor-overview", "factor-overview", "factor/overview/index", 1),
  menu("024002", "0240", "/factor/definitions", "factor-definitions", "factor-definitions", "factor/definitions/index", 2),
  menu("024003", "0240", "/factor/tasks", "factor-tasks", "factor-tasks", "factor/task-management/index", 3),

  directory("0250", "0", "/strategy/running", "strategy", "strategy", 4, { svgIcon: "mind-mapping", icon: "" }),
  menu("025002", "0250", "/strategy/overview", "strategy-overview", "strategy-definitions", "strategy/overview/index", 1),
  menu("025001", "0250", "/strategy/running", "strategy-running", "strategy-running", "strategy/running/index", 2),
  menu("025003", "0250", "/strategy/replay", "strategy-replay", "strategy-replay", "strategy/replay/index", 3),

  directory("05", "0", "/trading/accounts", "trading", "trading", 5, { svgIcon: "balance-inquiry", icon: "" }),
  menu("0501", "05", "/trading/accounts", "trading-accounts", "trading-accounts", "trading/account-workbench/index", 1),
  menu(
    "0502",
    "05",
    "/trading/positions",
    "trading-positions",
    "trading-positions",
    "trading/position-detail/position-detail",
    3
  ),
  menu("0503", "05", "/trading/orders", "trading-orders", "trading-orders", "trading/trade-record/trade-record", 4),

  directory("06", "0", "/ops/monitor", "ops", "ops", 6, { svgIcon: "defend", icon: "" }),
  menu("0602", "06", "/ops/monitor", "ops-monitor", "ops-monitor", "ops/monitor/index", 1),
  menu("0601", "06", "/ops/hosts", "ops-hosts", "ops-hosts", "ops/host-workbench/index", 2),
  menu("0600", "06", "/ops/deployments", "ops-deployments", "ops-deployments", "ops/deployments/index", 3),
  menu("0606", "06", "/ops/storage/nodes", "ops-storage", "ops-storage", "ops/storage/index", 4),

  directory("07", "0", "/settings/spaces", "settings", "settings", 7, { svgIcon: "set", icon: "" }),
  menu("0701", "07", "/settings/spaces", "settings-spaces", "settings-spaces", "settings/spaces/index", 1),
  menu("0702", "07", "/settings/secrets", "settings-secrets", "settings-secrets", "settings/secrets/index", 2)
];
