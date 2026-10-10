import type { CatalogComponent, DeployHost, DeployPlacement, HostGatewayStatus, HostRoute } from "@/api/admin/types";
import type { HealthComponent, HealthUnregistered } from "@/api/monitor";
import { formatTime, isAttention } from "@/views/ops/monitor/monitor-display";

/** 服务页签的状态筛选：全部、异常（需关注）、已停用。 */
export type ServiceFilter = "all" | "attention" | "disabled";

/** 组件行：部署表中的一个部署，合并组件目录、部署记录和 Monitor 的健康状态。 */
export interface DeploymentRow {
  key: string;
  hostId: string;
  componentId: string;
  name: string;
  enabled: boolean;
  protected: boolean;
  hostComponent: boolean;
  /** Monitor 的健康状态；Monitor 还没有这个部署时，启用的为 unknown、停用的为 disabled。 */
  healthStatus: string;
  healthReason: string;
  health?: HealthComponent;
  catalog?: CatalogComponent;
  placement: DeployPlacement;
  services: Array<{ path: string; port: number }>;
}

/** 主机分组：主机信息、主机网关状态、主机组件和业务组件。 */
export interface HostGroup {
  host: DeployHost;
  hostId: string;
  enabled: boolean;
  gatewayState: string;
  rows: DeploymentRow[];
  hostRows: DeploymentRow[];
  unregistered: HealthUnregistered[];
  attention: boolean;
}

export interface DeploymentSources {
  hosts: DeployHost[];
  placements: DeployPlacement[];
  catalog: CatalogComponent[];
  health: HealthComponent[];
  unregistered: HealthUnregistered[];
}

export interface ServiceFilterOptions {
  search: string;
  filter: ServiceFilter;
  showDisabled: boolean;
}

const GATEWAY_STATE_LABELS: Record<string, string> = {
  online: "在线",
  offline: "离线",
  never_reported: "从未上报",
  conflict: "重复实例"
};

/** 主机网关状态的中文名称。 */
export function gatewayStateText(state?: string) {
  return GATEWAY_STATE_LABELS[state || ""] || "未知";
}

/** 主机网关状态对应的标签颜色。 */
export function gatewayStateColor(state?: string) {
  switch (state) {
    case "online":
      return "green";
    case "offline":
    case "conflict":
      return "red";
    default:
      return "gray";
  }
}

/** 路由同步状态：已同步，或待同步（注明从何时起）；主机网关从未上报时无从判断。 */
export function syncText(gateway?: HostGatewayStatus) {
  if (!gateway || !gateway.state || gateway.state === "never_reported") return "—";
  if (gateway.synced) return "已同步";
  return gateway.out_of_sync_since ? `待同步（自 ${formatTime(gateway.out_of_sync_since)}）` : "待同步";
}

/** 主机网关是否需要关注：离线、重复实例，或在线但路由没有同步。 */
export function gatewayAttention(gateway?: HostGatewayStatus) {
  if (!gateway) return false;
  return gateway.state === "offline" || gateway.state === "conflict" || (gateway.state === "online" && !gateway.synced);
}

function compareHostIds(left: string, right: string) {
  if (left === right) return 0;
  if (left === "control") return -1;
  if (right === "control") return 1;
  return left.localeCompare(right);
}

/** 组件目录中的路由服务及端口。 */
export function catalogServices(component?: CatalogComponent) {
  return (component?.services || [])
    .filter(service => service.path)
    .map(service => ({ path: service.path || "", port: Number(service.port) || 0 }));
}

/** 把部署记录、组件目录和 Monitor 的健康状态合并成按主机分组的视图（control 在前，组件按目录顺序）。 */
export function buildHostGroups(sources: DeploymentSources): HostGroup[] {
  const catalogIndex = new Map<string, { component: CatalogComponent; order: number }>();
  sources.catalog.forEach((component, order) => {
    if (component.id) catalogIndex.set(component.id, { component, order });
  });
  const healthIndex = new Map<string, HealthComponent>();
  for (const item of sources.health) {
    healthIndex.set(`${item.host_id}:${item.component_id}`, item);
  }
  const groups = new Map<string, HostGroup>();
  for (const host of sources.hosts) {
    const hostId = host.host_id || "";
    if (!hostId) continue;
    groups.set(hostId, {
      host,
      hostId,
      enabled: host.status !== "disabled",
      gatewayState: host.gateway?.state || "never_reported",
      rows: [],
      hostRows: [],
      unregistered: sources.unregistered.filter(item => item.host_id === hostId),
      attention: gatewayAttention(host.gateway)
    });
  }
  for (const placement of sources.placements) {
    const group = groups.get(placement.host_id || "");
    if (!group || !placement.component_id) continue;
    const catalog = catalogIndex.get(placement.component_id)?.component;
    const health = healthIndex.get(`${group.hostId}:${placement.component_id}`);
    const enabled = placement.status !== "disabled" && group.enabled;
    const row: DeploymentRow = {
      key: `${group.hostId}:${placement.component_id}`,
      hostId: group.hostId,
      componentId: placement.component_id,
      name: catalog?.name || health?.name || placement.component_id,
      enabled: placement.status !== "disabled",
      protected: Boolean(placement.protected),
      hostComponent: Boolean(placement.host_component),
      healthStatus: health?.status || (enabled ? "unknown" : "disabled"),
      healthReason: health?.reason || (enabled ? "Monitor 还没有这个部署的检查结果" : "已停用"),
      health,
      catalog,
      placement,
      services: catalogServices(catalog)
    };
    if (row.hostComponent) group.hostRows.push(row);
    else group.rows.push(row);
    if (enabled && isAttention(row.healthStatus)) group.attention = true;
  }
  const order = (row: DeploymentRow) => catalogIndex.get(row.componentId)?.order ?? Number.MAX_SAFE_INTEGER;
  for (const group of groups.values()) {
    group.rows.sort((left, right) => order(left) - order(right) || left.componentId.localeCompare(right.componentId));
    group.hostRows.sort((left, right) => order(left) - order(right));
    if (group.unregistered.length) group.attention = true;
  }
  return [...groups.values()].sort((left, right) => compareHostIds(left.hostId, right.hostId));
}

function rowMatchesSearch(row: DeploymentRow, keyword: string) {
  if (!keyword) return true;
  return [row.componentId, row.name, row.hostId, ...row.services.map(service => service.path)].some(value =>
    value.toLowerCase().includes(keyword)
  );
}

function rowMatchesFilter(row: DeploymentRow, group: HostGroup, options: ServiceFilterOptions) {
  const disabled = !row.enabled || !group.enabled;
  if (options.filter === "disabled") return disabled;
  if (disabled && !options.showDisabled) return false;
  if (options.filter === "attention") return !disabled && isAttention(row.healthStatus);
  return true;
}

/** 按搜索词、状态筛选和「显示已停用」过滤主机分组；没有匹配行的主机不显示（主机本身匹配搜索词时保留）。 */
export function filterHostGroups(groups: HostGroup[], options: ServiceFilterOptions): HostGroup[] {
  const keyword = options.search.trim().toLowerCase();
  const out: HostGroup[] = [];
  for (const group of groups) {
    if (!group.enabled && !options.showDisabled && options.filter !== "disabled") continue;
    const hostMatches = Boolean(keyword) && group.hostId.toLowerCase().includes(keyword);
    const rows = group.rows.filter(
      row => (hostMatches || rowMatchesSearch(row, keyword)) && rowMatchesFilter(row, group, options)
    );
    const hostRows = group.hostRows.filter(
      row => (hostMatches || rowMatchesSearch(row, keyword)) && rowMatchesFilter(row, group, options)
    );
    const unregistered = options.filter === "disabled" ? [] : group.unregistered;
    const groupAttention = options.filter === "attention" && group.attention;
    if (rows.length || hostRows.length || (hostMatches && options.filter === "all") || groupAttention) {
      out.push({ ...group, rows, hostRows, unregistered });
    }
  }
  return out;
}

/** 汇总：主机数、部署数、需关注的部署数、已停用的部署数。 */
export function summarizeGroups(groups: HostGroup[]) {
  let deployments = 0;
  let attention = 0;
  let disabled = 0;
  for (const group of groups) {
    for (const row of [...group.hostRows, ...group.rows]) {
      deployments += 1;
      if (!row.enabled || !group.enabled) disabled += 1;
      else if (isAttention(row.healthStatus)) attention += 1;
    }
  }
  return { hosts: groups.length, deployments, attention, disabled };
}

/** 组件目录中一个组件的调用方汇总（每个服务的方法数与放行的调用方）。 */
export function aclSummary(component?: CatalogComponent) {
  return (component?.services || []).map(service => {
    const callers = new Set<string>();
    for (const method of service.methods || []) {
      for (const caller of method.callers || []) callers.add(caller);
    }
    return {
      path: service.path || "",
      port: Number(service.port) || 0,
      methods: (service.methods || []).length,
      readOnly: (service.methods || []).filter(method => method.read_only).length,
      callers: [...callers].sort()
    };
  });
}

/** 上游地址中的端口，例如 127.0.0.1:11109 → 11109。 */
export function upstreamPort(address?: string) {
  const match = /:(\d+)$/.exec(address || "");
  return match ? Number(match[1]) : undefined;
}

/** 路由表的调用方选项（全部路由的调用方并集）。 */
export function routeCallers(routes: HostRoute[]) {
  const callers = new Set<string>();
  for (const route of routes) for (const caller of route.callers || []) callers.add(caller);
  return [...callers].sort();
}

/** 按调用方和方法名（包含匹配）过滤路由。 */
export function filterRoutes(routes: HostRoute[], caller: string, method: string) {
  const keyword = method.trim().toLowerCase();
  return routes.filter(route => {
    if (caller && !(route.callers || []).includes(caller)) return false;
    if (keyword && !(route.methods || []).some(name => name.toLowerCase().includes(keyword))) return false;
    return true;
  });
}

/** 字节数的简短写法。 */
export function formatBytes(value?: number | string) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes) || bytes <= 0) return "—";
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(bytes % (1 << 20) ? 1 : 0)} MiB`;
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(bytes % (1 << 10) ? 1 : 0)} KiB`;
  return `${bytes} B`;
}

/** 毫秒数的简短写法。 */
export function formatTimeout(value?: number | string) {
  const ms = Number(value);
  if (!Number.isFinite(ms) || ms <= 0) return "—";
  return ms % 1000 === 0 ? `${ms / 1000}s` : `${ms}ms`;
}
