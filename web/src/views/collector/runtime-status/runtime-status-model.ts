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

/** 巡检时间的短格式：今天只显示时分，其余显示月日时分（本地时间）。 */
export function inspectTimeText(value: string, now = new Date()) {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  const pad = (n: number) => String(n).padStart(2, "0");
  const time = `${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
  return parsed.toDateString() === now.toDateString() ? time : `${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${time}`;
}

/**
 * 自动巡检列的展示：正常时只说“查到哪里了”，不出现时长，避免被误读为数据延迟；
 * 只有超出预期（与后端阈值一致：超过 15 分钟落后、超过 1 小时已停）时才带上时长。
 */
export function inspectionInfo(record: { inspected_until?: string; inspect_lag_seconds?: number | string }, now = new Date()) {
  const lag = Number(record.inspect_lag_seconds || 0);
  const base = record.inspected_until ? `已查至 ${inspectTimeText(record.inspected_until, now)}` : "尚未巡检";
  if (lag > 3600) return { text: `${base} · 已停 ${durationText(lag)}`, color: "rgb(var(--danger-6))" };
  if (lag > 900) return { text: `${base} · 落后 ${durationText(lag)}`, color: "rgb(var(--warning-6))" };
  return { text: base, color: "var(--color-text-3)" };
}

/** 自动巡检列的说明。 */
export const INSPECTION_TIP =
  "自动巡检每 5 分钟检查一次已收盘的 K 线，发现缺口会自动补采。落后时，这段时间的缺口暂时不会被发现和补上，不代表数据缺失；数据是否齐全看“24h 实时缺口”和“历史缺口”。";

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

const WEEKDAY_NAMES: Record<string, string> = {
  "0": "日",
  "1": "一",
  "2": "二",
  "3": "三",
  "4": "四",
  "5": "五",
  "6": "六",
  "7": "日",
  SUN: "日",
  MON: "一",
  TUE: "二",
  WED: "三",
  THU: "四",
  FRI: "五",
  SAT: "六"
};

const isNumber = (value: string) => /^\d+$/.test(value);
const isAny = (value: string) => value === "*" || value === "?";
const stepOf = (value: string) => /^\*\/(\d+)$/.exec(value)?.[1];
const pad2 = (value: string) => value.padStart(2, "0");

/** 日期部分：每天 / 工作日 / 每周一、三 / 每月 1 日；无法表达时返回 undefined。 */
function cronDayText(dom: string, dow: string) {
  if (isAny(dom) && isAny(dow)) return "每天";
  if (isAny(dom)) {
    const upper = dow.toUpperCase();
    if (upper === "1-5" || upper === "MON-FRI") return "工作日";
    if (upper === "0,6" || upper === "6,0" || upper === "SAT,SUN" || upper === "SUN,SAT") return "周末";
    const days = upper.split(",").map(day => WEEKDAY_NAMES[day]);
    if (days.every(Boolean)) return `每周${days.join("、")}`;
    return undefined;
  }
  if (isAny(dow) && /^\d+(,\d+)*$/.test(dom)) return `每月 ${dom.split(",").join("、")} 日`;
  return undefined;
}

/**
 * 把 cron 表达式翻译成中文，翻译不了时原样返回。
 * 支持 5 段（分 时 日 月 周）、6 段（秒 分 时 日 月 周）与腾讯云 SCF 的 7 段（秒 分 时 日 月 周 年）。
 */
export function cronText(expr?: string) {
  const raw = (expr || "").trim();
  if (!raw) return "-";
  const parts = raw.split(/\s+/);
  if (parts.length < 5 || parts.length > 7) return raw;
  const [sec, min, hour, dom, mon, dow, year = "*"] = parts.length === 5 ? ["0", ...parts] : parts;
  if (!isAny(mon) || !isAny(year)) return raw;
  const day = cronDayText(dom, dow);
  if (!day) return raw;
  // 一天内多次触发时，“每天”可以省略，其余日期限定作为前缀
  const prefix = day === "每天" ? "" : `${day} `;
  const secondSuffix = isNumber(sec) && Number(sec) !== 0 ? `（第 ${Number(sec)} 秒）` : "";
  const hourRange = /^(\d+)-(\d+)$/.exec(hour);
  const hourScope = hourRange ? `${Number(hourRange[1])}–${Number(hourRange[2])} 点` : "";

  if (min === "*" && (isAny(hour) || hourRange)) {
    if (sec === "*") return `${prefix}${hourScope}每秒`;
    if (stepOf(sec)) return `${prefix}${hourScope}每 ${stepOf(sec)} 秒`;
    if (isNumber(sec)) return `${prefix}${hourScope}每分钟${secondSuffix}`;
    return raw;
  }
  if (!isNumber(sec)) return raw;
  if (stepOf(min) && (isAny(hour) || hourRange)) return `${prefix}${hourScope}每 ${stepOf(min)} 分钟${secondSuffix}`;
  if (!isNumber(min)) return raw;
  const minuteOfHour =
    Number(min) === 0 && Number(sec) === 0 ? "整点" : `第 ${Number(min)} 分${Number(sec) ? ` ${Number(sec)} 秒` : ""}`;
  if (isAny(hour)) return `${prefix}每小时${minuteOfHour}`;
  if (stepOf(hour)) return `${prefix}每 ${stepOf(hour)} 小时${minuteOfHour}`;
  if (/^\d+(,\d+)*$/.test(hour)) {
    const time = (h: string) => `${pad2(h)}:${pad2(min)}${Number(sec) ? `:${pad2(sec)}` : ""}`;
    return `${day} ${hour.split(",").map(time).join("、")}`;
  }
  return raw;
}
