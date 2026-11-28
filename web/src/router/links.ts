/**
 * 跨模块跳转链接集中定义：路由路径、命名路由与常用 query 键统一在此维护，
 * 各模块页面互相跳转时只引用这里的构造器，不再在业务文件里散落拼接路径字符串。
 */

export interface NamedRouteLink {
  name: string;
  params?: Record<string, string>;
  query?: Record<string, string>;
}

function withQuery(path: string, params?: Record<string, string | undefined>): string {
  if (!params) return path;
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value);
  }
  const query = search.toString();
  return query ? `${path}?${query}` : path;
}

/** 转为前端 hash 路由使用的 href（供原生 <a> 标签使用）。 */
export function hashHref(path: string): string {
  return `#${path}`;
}

/** 数据源管理页。 */
export function dataSourcesPath(): string {
  return "/data/sources";
}

/** 采集运行页（任务 / 结果 / 运行状态 / 补采）。 */
export function collectorTasksPath(
  options: {
    tab?: "tasks" | "results" | "status" | "backfills";
    detail?: string;
    dataset?: string;
    freq?: string;
  } = {}
): string {
  return withQuery("/collector/tasks", {
    tab: options.tab,
    detail: options.detail,
    dataset: options.dataset,
    freq: options.freq
  });
}

/** 采集任务详情抽屉的 hash 链接。 */
export function collectorTaskDetailHref(taskId: string): string {
  return hashHref(collectorTasksPath({ detail: taskId }));
}

/** 因子计算任务页（计算任务 / 计算结果 / 补算）。 */
export function factorTasksPath(
  options: {
    tab?: "tasks" | "results" | "recalc";
    task?: string;
    detail?: string;
  } = {}
): string {
  return withQuery("/factor/tasks", {
    tab: options.tab === "tasks" ? undefined : options.tab,
    task: options.task,
    detail: options.detail
  });
}

/** 因子计算任务页的 router.push 对象。 */
export function factorTasksLocation(options: { tab?: "tasks" | "results" | "recalc"; task?: string; detail?: string } = {}) {
  const query: Record<string, string> = {};
  if (options.tab && options.tab !== "tasks") query.tab = options.tab;
  if (options.task) query.task = options.task;
  if (options.detail) query.detail = options.detail;
  return { path: "/factor/tasks", query };
}

/** 因子计算任务详情抽屉的 hash 链接。 */
export function factorTaskDetailHref(taskId: string): string {
  return hashHref(factorTasksPath({ detail: taskId }));
}

/** 因子补算页签的 hash 链接。 */
export function factorTaskRecalcHref(taskId: string): string {
  return hashHref(factorTasksPath({ tab: "recalc", task: taskId }));
}

/** 因子定义列表页。 */
export function factorDefinitionsPath(): string {
  return "/factor/definitions";
}

/** 策略运行页（实例列表）。 */
export function strategyTasksPath(): string {
  return "/strategy/tasks";
}

/** 策略实例详情页的命名路由对象。 */
export function strategyInstanceDetailLink(instanceId: string): NamedRouteLink {
  return { name: "strategy-detail", params: { instanceId } };
}

/** 交易账户列表页（按环境菜单）。 */
export function tradingAccountsPath(
  env: "paper" | "testnet" | "live",
  options: { list?: "execution" } = {}
): string {
  return withQuery(`/trading/${env}`, { list: options.list });
}

/** 交易组合账户详情页的命名路由对象。 */
export function tradingAccountDetailLink(logicalAccountId: string): NamedRouteLink {
  return { name: "trading-account-detail", params: { logical_account_id: logicalAccountId } };
}

/** 交易组合账户详情页路径。 */
export function tradingAccountDetailPath(logicalAccountId: string): string {
  return `/trading/accounts/${encodeURIComponent(logicalAccountId)}`;
}

/** 交易下架名单与处理页。 */
export function tradingBlockedPath(): string {
  return "/trading/blocked";
}

/** 归档任务页（可选定位指定数据集）。 */
export function archiveTasksPath(datasetId?: string): string {
  return withQuery("/archive/tasks", { task: datasetId });
}

/** 监控告警页。 */
export function opsMonitorPath(): string {
  return "/ops/monitor";
}

/** 主机工作台页（列表 / 监控）。 */
export function opsHostsPath(options: { tab?: "hosts" | "monitor"; hostId?: string } = {}): string {
  return withQuery("/ops/hosts", {
    tab: options.tab,
    host_id: options.hostId
  });
}

/** 部署与服务页（服务 / 网关路由 / 服务拓扑）。 */
export function opsDeploymentsPath(
  options: { tab?: "services" | "routes" | "topology"; hostId?: string; componentId?: string } = {}
): string {
  return withQuery("/ops/deployments", {
    tab: options.tab,
    host_id: options.hostId,
    component_id: options.componentId
  });
}

/** 日志中心页（活动日志 / 重要日志）。 */
export function opsLogsPath(tab?: "activity" | "important"): string {
  return withQuery("/ops/logs", { tab });
}

/** 空间管理页。 */
export function settingsSpacesPath(): string {
  return "/settings/spaces";
}

/** 密钥管理页。 */
export function settingsSecretsPath(): string {
  return "/settings/secrets";
}
