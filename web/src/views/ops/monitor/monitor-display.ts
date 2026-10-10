import type {
  HealthAlert,
  HealthComponent,
  HealthHost,
  HealthOverview,
  HealthDataStage,
  HealthTarget,
  WireInt64
} from "@/api/monitor";

/** 页面跳转的目标。 */
export interface PageTarget {
  path: string;
  query?: Record<string, string>;
}

/** 状态的展示文字：后端已经统一成六种状态，这里只做标签映射，不翻译原因。 */
const statusLabels: Record<string, string> = {
  healthy: "正常",
  degraded: "需关注",
  down: "异常",
  unknown: "未知",
  disabled: "已停用",
  unchecked: "不探测"
};

const statusColors: Record<string, string> = {
  healthy: "green",
  degraded: "orange",
  down: "red",
  unknown: "gray",
  disabled: "gray",
  unchecked: "arcoblue"
};

/** 状态的严重程度，越大越严重；用于排序。 */
const statusRanks: Record<string, number> = { down: 5, degraded: 4, unknown: 3, healthy: 2, unchecked: 1, disabled: 0 };

export function statusLabel(status?: string) {
  return statusLabels[status || ""] || "未知";
}

export function statusColor(status?: string) {
  return statusColors[status || ""] || "gray";
}

export function statusRank(status?: string) {
  return statusRanks[status || ""] ?? statusRanks.unknown;
}

export function isAttention(status?: string) {
  return status === "down" || status === "degraded";
}

export function severityLabel(severity?: string) {
  return severity === "warning" ? "警告" : "严重";
}

export function severityColor(severity?: string) {
  return severity === "warning" ? "orange" : "red";
}

const reporterLabels: Record<string, string> = {
  healthy: "正常上报",
  stale: "上报中断",
  never_reported: "从未上报"
};

export function reporterLabel(status?: string) {
  if (!status) return "不上报运行指标";
  return reporterLabels[status] || "状态未知";
}

const gatewayStateLabels: Record<string, string> = {
  online: "在线",
  offline: "离线",
  never_reported: "从未上报",
  conflict: "重复实例"
};

export function gatewayStateLabel(state?: string) {
  if (!state) return "未知";
  return gatewayStateLabels[state] || state;
}

const channelLabels: Record<string, string> = { wecom: "企业微信", feishu: "飞书" };

export function channelLabel(channelType?: string) {
  return channelLabels[channelType || ""] || channelType || "未配置";
}

export function formatTime(value?: string) {
  if (!value) return "暂无";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "暂无" : date.toLocaleString("zh-CN", { hour12: false });
}

/** 时长的中文写法，例如「1 小时 5 分钟」。 */
export function formatSeconds(totalSeconds: number) {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return minutes % 60 ? `${hours} 小时 ${minutes % 60} 分钟` : `${hours} 小时`;
  return hours % 24 ? `${Math.floor(hours / 24)} 天 ${hours % 24} 小时` : `${Math.floor(hours / 24)} 天`;
}

/** 从 since 到 now 的时长；since 为空或无效时返回空字符串。 */
export function formatSince(since: string | undefined, now: Date) {
  if (!since) return "";
  const start = new Date(since);
  if (Number.isNaN(start.getTime())) return "";
  return formatSeconds((now.getTime() - start.getTime()) / 1000);
}

export function int64Value(value?: WireInt64) {
  const parsed = typeof value === "string" ? Number(value) : value;
  return Number.isFinite(parsed) ? Number(parsed) : 0;
}

export function formatLag(value?: WireInt64) {
  const seconds = int64Value(value);
  return seconds > 0 ? formatSeconds(seconds) : "—";
}

export function formatPercent(value?: number) {
  return typeof value === "number" && Number.isFinite(value) ? `${value.toFixed(1)}%` : "—";
}

function componentName(components: HealthComponent[], componentId?: string) {
  return components.find(item => item.component_id === componentId)?.name || componentId || "";
}

/** 告警对象的展示文字，例如「存储主服务 @ storage」「主机 storage」「dataset_x · 1m」。 */
export function targetLabel(target: HealthTarget | undefined, components: HealthComponent[] = []) {
  if (!target) return "";
  switch (target.kind) {
    case "component":
      return `${componentName(components, target.component_id)} @ ${target.host_id || "未知主机"}`;
    case "host":
      return `主机 ${target.host_id || "未知"}`;
    case "dataset":
      return [target.dataset_id, target.frequency].filter(Boolean).join(" · ");
    default:
      return target.space_id ? `业务检查 · ${target.space_id}` : "业务检查";
  }
}

export function targetKindLabel(kind?: string) {
  switch (kind) {
    case "component":
      return "组件";
    case "host":
      return "主机";
    case "dataset":
      return "数据集";
    default:
      return "业务";
  }
}

/** 告警「定位」的跳转：组件到服务部署页，数据集到数据页，主机到主机监控，业务检查到所属模块的页面。 */
export function locateRoute(alert: HealthAlert, hosts: HealthHost[] = []): PageTarget | undefined {
  const target = alert.target;
  switch (target?.kind) {
    case "component":
      return {
        path: "/ops/deployments",
        query: { tab: "services", host: target.host_id || "", component: target.component_id || "" }
      };
    case "host": {
      const agentId = hosts.find(item => item.host_id === target.host_id)?.agent_id;
      return { path: "/ops/hosts", query: agentId ? { tab: "monitor", agent: agentId } : { tab: "monitor" } };
    }
    case "dataset":
      return alert.stage === "factor" ? { path: "/factor/tasks" } : { path: "/collector/tasks", query: { tab: "results" } };
    default:
      return stageRoute(alert.stage);
  }
}

/** 数据链路阶段对应的页面。 */
export function stageRoute(stage?: string): PageTarget | undefined {
  switch (stage) {
    case "collect":
      return { path: "/collector/tasks" };
    case "storage":
      return { path: "/ops/storage/nodes" };
    case "factor":
      return { path: "/factor/tasks" };
    case "trade":
      return { path: "/trading/accounts" };
    default:
      return undefined;
  }
}

export interface MatrixRow {
  componentId: string;
  name: string;
  status: string;
  cells: Record<string, HealthComponent>;
}

export interface ComponentMatrix {
  hosts: string[];
  rows: MatrixRow[];
}

/** 组件 × 主机矩阵：只有部署了的格子才有内容；异常的组件排在前面。 */
export function buildComponentMatrix(components: HealthComponent[] = []): ComponentMatrix {
  const hosts = [...new Set(components.map(item => item.host_id || ""))].filter(Boolean).sort((left, right) => {
    if (left === "control") return -1;
    if (right === "control") return 1;
    return left.localeCompare(right);
  });
  const rows = new Map<string, MatrixRow>();
  for (const item of components) {
    const componentId = item.component_id || "";
    if (!componentId || !item.host_id) continue;
    const row = rows.get(componentId) || { componentId, name: item.name || componentId, status: "", cells: {} };
    row.cells[item.host_id] = item;
    if (!row.status || statusRank(item.status) > statusRank(row.status)) row.status = item.status || "unknown";
    rows.set(componentId, row);
  }
  const sorted = [...rows.values()].sort((left, right) => {
    const attention = Number(isAttention(right.status)) - Number(isAttention(left.status));
    if (attention !== 0) return attention;
    if (isAttention(left.status) && statusRank(left.status) !== statusRank(right.status)) {
      return statusRank(right.status) - statusRank(left.status);
    }
    return left.name.localeCompare(right.name, "zh-CN");
  });
  return { hosts, rows: sorted };
}

/** 页头的一句话总体状态。 */
export function overallSentence(overview: HealthOverview) {
  const alerts = overview.summary?.alerts || overview.alerts?.length || 0;
  const attention = overview.summary?.attention || 0;
  const unknown = overview.summary?.unknown || 0;
  if (alerts > 0) return `有 ${alerts} 条告警正在触发${attention ? `，${attention} 项需关注` : ""}`;
  if (attention > 0) return `没有正在触发的告警，但有 ${attention} 项需关注`;
  if (unknown > 0) return `一切正常，另有 ${unknown} 项暂无数据`;
  return "一切正常";
}

/** 数据链路阶段的数据集计数：总数和需关注的个数。 */
export function stageCounts(stage: HealthDataStage) {
  const datasets = stage.datasets || [];
  return { total: datasets.length, attention: datasets.filter(item => isAttention(item.status)).length };
}
