import type {
  CollectorDatasetStatus,
  CollectorFunctionStatus,
  GetCollectorStatusResponse,
  HealthLevel
} from "@/api/collector/runtime-status";

export { activityTimeText as localTimeText } from "@/components/activity-log/activity-log";

const levels: Record<HealthLevel, { text: string; color: string; icon: string }> = {
  ok: { text: "正常", color: "green", icon: "icon-check-circle" },
  warn: { text: "需关注", color: "orangered", icon: "icon-exclamation-circle" },
  error: { text: "异常", color: "red", icon: "icon-close-circle" }
};

export function levelInfo(level?: string) {
  return levels[(level as HealthLevel) || "ok"] || levels.ok;
}

export function levelRank(level?: string) {
  return level === "error" ? 2 : level === "warn" ? 1 : 0;
}

const functionStates: Record<string, { text: string; color: string }> = {
  applied: { text: "已生效", color: "green" },
  pending: { text: "下发中", color: "orangered" },
  retiring: { text: "退役中", color: "gray" }
};

export function functionStateInfo(state?: string) {
  return functionStates[state || ""] || { text: state || "未知", color: "gray" };
}

/** 数据集与频率的组合键，与后端的“数据集|频率”一致。 */
export function datasetKey(item: { dataset_id?: string; frequency?: string }) {
  return `${item.dataset_id || ""}|${item.frequency || ""}`;
}

/** 时长写成“N 分钟”“N 小时 M 分钟”“N 天”。 */
export function durationText(seconds?: number | string) {
  const minutes = Math.round(Number(seconds || 0) / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  if (minutes < 24 * 60)
    return minutes % 60 === 0 ? `${minutes / 60} 小时` : `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分钟`;
  return `${Math.floor(minutes / (24 * 60))} 天`;
}

/** 检查滞后的颜色：与后端阈值一致（超过 15 分钟需关注，超过 1 小时异常）。 */
export function lagColor(seconds?: number | string) {
  const value = Number(seconds || 0);
  if (value > 3600) return "rgb(var(--danger-6))";
  if (value > 900) return "rgb(var(--warning-6))";
  return "var(--color-text-2)";
}

export interface StatusSummary {
  functions: number;
  pendingFunctions: number;
  retiringFunctions: number;
  datasets: number;
  subjects: number;
  gapBars: number;
  gapSubjects: number;
}

export function statusSummary(status: GetCollectorStatusResponse): StatusSummary {
  const functions = status.functions || [];
  const datasets = status.datasets || [];
  return {
    functions: functions.length,
    pendingFunctions: functions.filter(item => item.state === "pending").length,
    retiringFunctions: functions.filter(item => item.state === "retiring").length,
    datasets: datasets.length,
    subjects: datasets.reduce((sum, item) => sum + (item.subject_count || 0), 0),
    gapBars: datasets.reduce((sum, item) => sum + (item.gap_bars_24h || 0), 0),
    gapSubjects: datasets.reduce((sum, item) => sum + (item.gap_subjects_24h || 0), 0)
  };
}

export interface FunctionFilters {
  keyword: string;
  region: string;
  market: string;
  abnormalOnly: boolean;
}

/** 数据集展示名：按“数据集|频率”或数据集 ID 查展示名，查不到时回退为原值。 */
export function datasetNameMap(datasets: CollectorDatasetStatus[]) {
  const names = new Map<string, string>();
  for (const item of datasets) {
    if (!item.dataset_name) continue;
    names.set(datasetKey(item), item.dataset_name);
    names.set(item.dataset_id || "", item.dataset_name);
  }
  return names;
}

export function datasetLabel(names: Map<string, string>, key: string) {
  return names.get(key) || key;
}

/** 函数筛选：关键字匹配函数名、服务的数据集或其展示名；“只看异常”只保留下发中与退役中的函数。 */
export function filterFunctions(
  functions: CollectorFunctionStatus[],
  filters: FunctionFilters,
  names = new Map<string, string>()
) {
  const keyword = filters.keyword.trim().toLowerCase();
  return functions.filter(item => {
    if (filters.region && item.region !== filters.region) return false;
    if (filters.market && item.market !== filters.market) return false;
    if (filters.abnormalOnly && (item.state || "applied") === "applied") return false;
    if (!keyword) return true;
    return (
      (item.name || "").toLowerCase().includes(keyword) ||
      (item.datasets || []).some(dataset => `${dataset} ${names.get(dataset) || ""}`.toLowerCase().includes(keyword))
    );
  });
}

/** 取函数的某个字段的去重取值，作为筛选下拉选项。 */
export function functionOptions(functions: CollectorFunctionStatus[], field: "region" | "market") {
  return [...new Set(functions.map(item => item[field] || "").filter(Boolean))].sort();
}

/** 数据集展开行里的函数：服务该数据集的函数名。 */
export function datasetFunctions(functions: CollectorFunctionStatus[], dataset: CollectorDatasetStatus) {
  const key = datasetKey(dataset);
  return functions.filter(item => (item.datasets || []).includes(key)).map(item => item.name || "");
}
