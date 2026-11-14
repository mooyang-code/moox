/**
 * 数据归档页的纯函数：文案、校验、筛选、URL 状态。规则与 modules/archive 的后端实现同口径，见 docs/模块/归档.md。
 */
import type { LocationQuery, LocationQueryRaw } from "vue-router";
import type {
  ArchiveHealth,
  ArchiveObjectStore,
  ArchivePartition,
  ArchivePartitionStatus,
  ArchiveRemoteState,
  ArchiveRun,
  ArchiveRunKind,
  ArchiveRunStatus,
  ArchiveTask,
  ArchiveWorkerInfo,
  AvoidanceRules
} from "@/api/archive";
import type { ComponentDeployment, DeploymentHost } from "@/api/admin/types";
import { formatBytes } from "@/api/modules/host-monitor";
import type { Dataset } from "@/api/storage/types";
import { freqSeconds } from "@/views/factor/shared/health";
import { formatInstant, toInstantMs } from "@/utils/time-zone";

const DAY_SECONDS = 86_400;

/** uint64 经 protojson 是字符串：统一转成数字，读不出时为 0。 */
export function toNum(value: number | string | undefined | null): number {
  const n = Number(value ?? 0);
  return Number.isFinite(n) ? n : 0;
}

// ---- 文案 ----

const KIND_LABELS: Record<ArchiveRunKind, string> = {
  scheduled: "复核",
  manual: "立即运行",
  tick: "到点拉取",
  range: "补拉区间",
  import: "导入"
};
export const kindLabel = (kind?: string) => KIND_LABELS[kind as ArchiveRunKind] ?? (kind || "—");

const RUN_STATUS_LABELS: Record<ArchiveRunStatus, string> = {
  queued: "排队中",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  canceled: "已取消"
};
export const RUN_STATUSES = Object.keys(RUN_STATUS_LABELS) as ArchiveRunStatus[];
export const runStatusLabel = (status?: string) => RUN_STATUS_LABELS[status as ArchiveRunStatus] ?? (status || "—");
export const runActive = (run?: ArchiveRun) => run?.status === "queued" || run?.status === "running";

const HEALTH_LABELS: Record<ArchiveHealth, string> = { ok: "正常", warning: "需关注", error: "异常", paused: "已停用" };
export const healthLabel = (health?: string) => HEALTH_LABELS[health as ArchiveHealth] ?? (health || "—");

/** 状态文字的着色：和交易页 status-mark 一样只给文字上色。 */
export type Tone = "ok" | "warn" | "bad" | "muted";
export function healthTone(health?: string): Tone {
  if (health === "ok") return "ok";
  if (health === "warning") return "warn";
  if (health === "error") return "bad";
  return "muted";
}
export function runTone(status?: string): Tone {
  if (status === "succeeded") return "ok";
  if (status === "failed") return "bad";
  if (status === "queued" || status === "running") return "warn";
  return "muted";
}

const PARTITION_LABELS: Record<ArchivePartitionStatus, string> = { open: "暂存中", exported: "已导出", final: "已定稿" };
export const partitionStatusLabel = (status?: string) => PARTITION_LABELS[status as ArchivePartitionStatus] ?? (status || "—");

/** 远端副本状态；off 表示没启用对象存储，不显示。 */
export function remoteStateLabel(state?: ArchiveRemoteState | string, remoteKey?: string): string {
  switch (state) {
    case "verified":
      return "已校验";
    case "pending":
      return remoteKey ? "待上传（远端只有旧版本）" : "待上传";
    case "failed":
      return "失败";
    case "none":
      return "—";
    default:
      return "";
  }
}

export function partitionUnitText(unit?: string): string {
  if (unit === "day") return "按空间本地日";
  if (unit === "month") return "按月（data_time + 12 小时归月）";
  return unit || "—";
}

/** 频率是否按天及以上（每天拉取一次，拉取即复核）。 */
export function isDailyFreq(freq?: string): boolean {
  return freqSeconds(freq || "") >= DAY_SECONDS;
}

/** “拉取”列第一行：每收盘 N 根 · 下次 12:41 UTC；1d 及以上写“每天收盘后”；停用写“已停用”。下次拉取到分钟。 */
export function pullText(task: ArchiveTask, zone: string): string {
  if (task.status === "paused") return "已停用";
  const head = isDailyFreq(task.freq) ? "每天收盘后" : `每收盘 ${task.pull_every_bars || 1} 根`;
  return task.next_pull_at ? `${head} · 下次 ${formatInstant(task.next_pull_at, zone, "minute")}` : head;
}

/** “拉取”列第二行：复核规律。 */
export function reviewText(task: ArchiveTask): string {
  return isDailyFreq(task.freq) ? "拉取即复核" : "每小时第 34 分复核";
}

/** 复核间隔：3600 → 每小时，86400 → 每天。 */
export function intervalText(seconds?: number): string {
  if (seconds === 3600) return "每小时";
  if (seconds === DAY_SECONDS) return "每天";
  return seconds ? durationText(seconds) : "—";
}

/** 时长：45 秒、2 分 13 秒、1 小时 16 分、2 天 3 小时。 */
export function durationText(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s} 秒`;
  if (s < 3600) return `${Math.floor(s / 60)} 分${s % 60 ? ` ${s % 60} 秒` : ""}`;
  if (s < DAY_SECONDS) {
    const minutes = Math.floor((s % 3600) / 60);
    return `${Math.floor(s / 3600)} 小时${minutes ? ` ${minutes} 分` : ""}`;
  }
  const hours = Math.floor((s % DAY_SECONDS) / 3600);
  return `${Math.floor(s / DAY_SECONDS)} 天${hours ? ` ${hours} 小时` : ""}`;
}

/** 一次运行的耗时：结束了用结束 − 开始，运行中用现在 − 开始；没开始为 “—”。 */
export function runDurationText(run: ArchiveRun | undefined, nowMs: number): string {
  const start = toInstantMs(run?.started_at);
  if (start === null) return "—";
  const end = toInstantMs(run?.finished_at) ?? (run?.status === "running" ? nowMs : null);
  return end === null ? "—" : durationText((end - start) / 1000);
}

/** 导入运行的文件名：import_file 是 "<import_id>/<文件名>"，只显示文件名；不是导入运行时为空。 */
export function importFileText(run: Pick<ArchiveRun, "kind" | "import_file">): string {
  if (run.kind !== "import" || !run.import_file) return "";
  return run.import_file.slice(run.import_file.lastIndexOf("/") + 1);
}

/** 导入运行的写入方式。 */
export const importModeText = (run: Pick<ArchiveRun, "overwrite">) => (run.overwrite ? "覆盖已有" : "只补空缺");

/** 运行消息超过这么多字符时折叠，完整内容点开看（导入校验失败时最多带 20 条带行号的错误）。 */
export const RUN_MESSAGE_FOLD_CHARS = 120;

/** 折叠显示的运行消息：不超过上限时原样返回，folded 为假。 */
export function foldRunMessage(message?: string): { text: string; folded: boolean } {
  const chars = Array.from(message || "");
  if (chars.length <= RUN_MESSAGE_FOLD_CHARS) return { text: message || "", folded: false };
  return { text: `${chars.slice(0, RUN_MESSAGE_FOLD_CHARS).join("")}…`, folded: true };
}

/**
 * 完整运行消息按条分行：worker 用“；”连接多条错误与附加说明；
 * 导入校验失败的消息是“导入文件有 N 处错误，……：第 x 行：…；第 y 行：…”，开头的总述单独成行。
 */
export function runMessageLines(message?: string): string[] {
  const text = (message || "").replace(/^(导入文件有 \d+ 处错误[^：]*)：/, "$1\n");
  return text
    .split(/[；\n]/)
    .map(line => line.trim())
    .filter(Boolean);
}

/** 读取失败像是 archive-mgr 没部署（路由不存在、连接被拒）时的补充说明。 */
export function archiveUnavailableHint(error: string): string {
  return /404|not found|no route|unknown service|connection refused|econnrefused|不存在|找不到/i.test(error)
    ? "archive-mgr 是可选组件，没有部署时这里不可用"
    : "";
}

/** 分区“下载”按钮：只有远端副本是当前版本且已校验时可下载，否则给出置灰的原因。 */
export function downloadBlockedReason(partition: Pick<ArchivePartition, "remote_state" | "remote_error" | "remote_key">): string {
  switch (partition.remote_state) {
    case "verified":
      return "";
    case "pending":
      return partition.remote_key ? "远端只有旧版本，当前版本还没有上传或校验" : "远端副本还没有上传或校验";
    case "failed":
      return `远端副本上传失败${partition.remote_error ? `：${partition.remote_error}` : ""}`;
    case "none":
      return "还没有远端副本";
    default:
      return "执行器没有配置对象存储，只能下载远端副本";
  }
}

// ---- 执行器与存放位置 ----

/** 归档执行器在服务目录里的组件 ID。 */
export const ARCHIVE_WORKER_COMPONENT = "archive-worker";

export type WorkerHost =
  | { state: "ok"; host: DeploymentHost }
  | { state: "none" | "many"; host?: undefined }
  | { state: "unknown"; host?: undefined; hostId: string };

/**
 * 执行器所在主机：拓扑里唯一启用的 archive-worker 部署（部署和主机都启用）的主机，和 Admin 的归档转发同一口径。
 * 没有、多于一条、主机不在登记里分别给出状态，由页面写说明。
 */
export function resolveWorkerHost(deployments: ComponentDeployment[], hosts: DeploymentHost[]): WorkerHost {
  const byId = new Map(hosts.map(host => [host.host_id, host]));
  const enabled = deployments.filter(item => item.component_id === ARCHIVE_WORKER_COMPONENT && item.status === "enabled");
  const found = enabled.flatMap(item => {
    const host = byId.get(item.host_id);
    return host && host.status === "enabled" ? [host] : [];
  });
  if (found.length === 1) return { state: "ok", host: found[0] };
  if (found.length > 1) return { state: "many" };
  // 部署启用了但主机不在登记里（或主机停用）：只能写主机 ID
  return enabled.length ? { state: "unknown", hostId: enabled[0].host_id } : { state: "none" };
}

/** 主机的显示：名称（地址）。名称缺省时用主机 ID，地址缺省时用内网地址。 */
export function hostText(host: Pick<DeploymentHost, "host_id" | "label" | "address" | "private_address">): string {
  const name = host.label || host.host_id;
  const address = host.address || host.private_address || "";
  return address ? `${name}（${address}）` : name;
}

export function workerHostText(info: WorkerHost | null): string {
  if (!info) return "";
  if (info.state === "ok") return hostText(info.host);
  if (info.state === "unknown") return info.hostId;
  if (info.state === "many") return "拓扑里有多条启用的部署";
  return "拓扑里没有启用的部署";
}

export interface DiskStatus {
  /** “已用 137 GB / 共 1.8 TB，可用 1.6 TB” */
  text: string;
  /** 可用量低于下限：worker 已暂停领取新运行 */
  insufficient: boolean;
  /** insufficient 时的说明 */
  warning: string;
  /** 悬停说明：已用 + 可用小于总量的差额是 root 保留空间 */
  title: string;
}

/**
 * 磁盘状态。是否不足由页面判断：可用量（statfs Bavail）低于下限，和 worker 停止领取新运行用同一个数、同一个下限。
 * 下限为 0（没上报）时不判断。
 */
export function diskStatus(worker: ArchiveWorkerInfo | undefined): DiskStatus | null {
  const total = toNum(worker?.disk_total_bytes);
  if (!worker || total <= 0) return null;
  const used = toNum(worker.disk_used_bytes);
  const free = toNum(worker.disk_free_bytes);
  const minFree = toNum(worker.min_free_bytes);
  const insufficient = minFree > 0 && free < minFree;
  const reserved = total - used - free;
  const title = [
    `总量 ${formatBytes(total)}，已用 ${formatBytes(used)}，可用 ${formatBytes(free)}`,
    reserved > 0 ? `已用 + 可用比总量少 ${formatBytes(reserved)}，是文件系统给 root 的保留空间，普通进程用不了` : "",
    minFree > 0 ? `可用低于 ${formatBytes(minFree)} 时执行器暂停领取新运行` : ""
  ]
    .filter(Boolean)
    .join("\n");
  return {
    text: `已用 ${formatBytes(used)} / 共 ${formatBytes(total)}，可用 ${formatBytes(free)}`,
    insufficient,
    warning: insufficient ? `磁盘空间不足（可用低于下限 ${formatBytes(minFree)}），已暂停领取新运行` : "",
    title
  };
}

/** 内置 SeaweedFS 只听执行器本机 127.0.0.1:11473；没有“是否内置”字段，按地址判断。 */
export function isBuiltinObjectStore(endpoint?: string): boolean {
  return /^(https?:\/\/)?(127\.0\.0\.1|localhost):11473\/?$/i.test((endpoint || "").trim());
}

export function storageClassText(store: Pick<ArchiveObjectStore, "storage_class">): string {
  if (!store.storage_class) return "默认";
  return store.storage_class === "STANDARD_IA" ? "低频（STANDARD_IA）" : store.storage_class;
}

// ---- 避让时段 ----

/** "04:00" → 下一个检查时刻 ":04:25"（mgr 每分钟第 25 秒检查）。 */
function nextCheckAfter(mmss: string): string {
  const match = /^(\d{2}):(\d{2})$/.exec(mmss);
  if (!match) return "";
  let minute = Number(match[1]);
  if (Number(match[2]) > 25) minute = (minute + 1) % 60;
  return `:${String(minute).padStart(2, "0")}:25`;
}

export const hasHourQuiet = (rules: AvoidanceRules) => Boolean(rules.hour_quiet_from && rules.hour_quiet_until);

/** 每分钟的秒窗口：不限秒、每分钟只在第 25～55 秒读取、每 N 分钟的触发时刻前后不读取。 */
export function minuteWindowText(rules: AvoidanceRules): string {
  const every = rules.every_minutes || 0;
  const before = rules.before_seconds || 0;
  const after = rules.after_seconds || 0;
  if (every <= 0) return "不限秒";
  if (every === 1) return `每分钟只在第 ${after}～${60 - before} 秒读取`;
  return `每 ${every} 分钟的触发时刻前 ${before} 秒到后 ${after} 秒不读取`;
}

export function hourQuietText(rules: AvoidanceRules): string {
  return hasHourQuiet(rules) ? `每小时 :${rules.hour_quiet_from}～:${rules.hour_quiet_until} 不拉取` : "不避让整点";
}

/** 新建抽屉里的说明：拉取何时检查、整点窗口里的边界顺延到哪；没读到规则时为空。 */
export function avoidanceScheduleText(rules: AvoidanceRules | undefined): string {
  if (!rules) return "";
  const hour = hasHourQuiet(rules)
    ? `${hourQuietText(rules)}，这段时间的边界顺延到 ${nextCheckAfter(rules.hour_quiet_until || "")}`
    : "不避让整点，边界到了就拉取";
  return `每分钟第 25 秒检查；${hour}；执行器读取 Storage 时${minuteWindowText(rules)}。`;
}

/** 依据：computed_at 为空表示还没算出过，用的是启动默认值。 */
export function avoidanceBasisText(rules: AvoidanceRules): string {
  return rules.basis || (rules.computed_at ? "—" : "未取到采集频率，使用默认值");
}

// ---- 目录名 ----

const DIR_NAME_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

/** 目录名：小写字母、数字，用单个连字符分隔，最长 64 个字符；和后端同一规则。 */
export function dirNameError(name: string): string {
  if (!name) return "请填写目录名";
  if (name.length > 64) return "目录名最长 64 个字符";
  if (!DIR_NAME_PATTERN.test(name)) return "只能用小写字母、数字和单个连字符，不能以连字符开头或结尾";
  return "";
}

/**
 * 分区文件的路径预览：<空间>/<用途>/<目录名>/<年>/<年月日>.csv.gz（按月分区时文件名是年月），和 worker 的 RelPath 一致；
 * 给了归档根目录时写成绝对路径。
 */
export function partitionPathPreview(
  space: string,
  purpose: string,
  dirName: string,
  partitionUnit?: string,
  rootDir = ""
): string {
  const file = partitionUnit === "month" ? "<yyyymm>.csv.gz" : "<yyyymmdd>.csv.gz";
  const rel = [space || "<空间>", purpose || "<用途>", dirName || "<目录名>", "<yyyy>", file].join("/");
  const root = rootDir.replace(/\/+$/, "");
  return root ? `${root}/${rel}` : rel;
}

/** 占用这个目录名的已有任务（同空间、同用途、同目录名）。 */
export function dirNameOwner(tasks: ArchiveTask[], purpose: string, dirName: string): ArchiveTask | undefined {
  return tasks.find(task => task.purpose === purpose && task.dir_name === dirName);
}

// ---- 最新数据 ----

/** 最新数据的单元格：缺失值（null）与空字符串分开显示，时间按空间时区。 */
export function recentCellText(value: string | null, type: string, zone: string): string {
  if (value === null) return "—";
  if (value === "") return '""';
  return type === "time" ? formatInstant(value, zone) : value;
}

// ---- 数据集 ----

/** 时序数据集：data_kind 是枚举名或数字 2。 */
function isTimeSeries(dataset: Pick<Dataset, "data_kind">): boolean {
  return dataset.data_kind === "DATA_KIND_TIME_SERIES" || dataset.data_kind === 2;
}

/**
 * 应该归档的数据集：时序、active、有限的线上保留期。
 * 和 Monitor 的 ListArchivableDatasets（modules/monitor/internal/metrics/storage.go）同口径。
 */
export function archivableDataset(dataset: Dataset): boolean {
  const retention = (dataset.retention || "").trim();
  return (
    isTimeSeries(dataset) && (dataset.status || "").toLowerCase() === "active" && retention !== "" && retention !== "forever"
  );
}

/** 应该归档、但还没有任务的数据集。 */
export function unarchivedDatasets(datasets: Dataset[], tasks: ArchiveTask[]): Dataset[] {
  const archived = new Set(tasks.map(task => task.dataset_id));
  return datasets.filter(dataset => archivableDataset(dataset) && !archived.has(dataset.dataset_id));
}

/**
 * 状态行下方的提醒：和某个任务的健康原因完全相同的（“<dataset_id>：<health_reason>”）任务表已经显示，过滤掉；
 * 其余照常显示，例如“<dataset_id>：分区登记在 worker X，当前活动 worker 是 Y，运行不会派给它”不在健康原因里。
 */
export function systemAlerts(alerts: string[] | undefined, tasks: Pick<ArchiveTask, "dataset_id" | "health_reason">[]): string[] {
  const shown = new Set(tasks.filter(task => task.health_reason).map(task => `${task.dataset_id}：${task.health_reason}`));
  return (alerts || []).filter(alert => !shown.has(alert));
}

/** 分区预计导出时间：后端给的是“分区 2026-11-06：预计 2026-11-07T00:15:25Z 导出”，时刻按空间时区显示；读不懂时原样显示。 */
export function exportEstimateText(text: string, zone: string): string {
  const match = /^分区 (\S+)：预计 (\S+) 导出$/.exec(text.trim());
  if (!match) return text;
  const at = formatInstant(match[2], zone);
  return at === "—" ? text : `分区 ${match[1]} 预计 ${at} 导出`;
}

// ---- 校验 ----

/** 拉取间隔 N：1～1440 的整数，且“频率 × N”不超过 1 天；1d 及以上不校验（每天一次）。和后端 pullEveryBars 同口径。 */
export function pullEveryBarsError(freq: string, n: number): string {
  if (isDailyFreq(freq)) return "";
  if (!Number.isInteger(n) || n < 1 || n > 1440) return "拉取间隔要是 1～1440 的整数";
  const span = freqSeconds(freq);
  if (span <= 0) return `无法识别频率 ${freq}`;
  return span * n > DAY_SECONDS ? `频率 × 拉取间隔不能超过 1 天（${freq} 最多 ${Math.floor(DAY_SECONDS / span)} 根）` : "";
}

/** 回补起点：不能早于线上还保留的最早时刻，不能晚于现在；为空表示不指定。提示里的时间按空间时区。 */
export function backfillFromError(value: number | null, earliestMs: number | null, nowMs: number, zone: string): string {
  if (value === null) return "";
  if (earliestMs !== null && value < earliestMs)
    return `不能早于 ${formatInstant(earliestMs, zone, "minute")}（线上还保留的最早数据）`;
  if (value > nowMs) return "不能晚于现在";
  return "";
}

/** 补拉区间：起点早于终点，不早于下界（今天 − 线上保留期 + 1 天），终点不晚于现在。提示里的时间按空间时区。 */
export function backfillRangeError(
  range: [number, number] | null,
  earliestMs: number | null,
  nowMs: number,
  zone: string
): string {
  if (!range) return "请选择补拉区间";
  const [start, end] = range;
  if (end <= start) return "结束时间必须晚于开始时间";
  if (earliestMs !== null && start < earliestMs)
    return `开始不能早于 ${formatInstant(earliestMs, zone, "minute")}（线上还保留的最早数据）`;
  if (end > nowMs) return "结束不能晚于现在";
  return "";
}

/** 编辑时是否还能改回补起点：首次复核成功后 mgr 会清空它，worker 有了水位也不再读，改了没有效果。 */
export function showBackfillField(task: Pick<ArchiveTask, "last_success_at">): boolean {
  return !task.last_success_at;
}

/**
 * 停用多少天以内不会漏数据：和健康等级异常门槛同一公式（modules/archive/internal/service/health.go 的 evaluateHealth），
 * max(保留期 − 48 小时, 保留期 / 2)，向下取整到天；永久保留或读不出保留期时为 null。
 */
export function pauseSafeDays(retention?: string): number | null {
  const match = /^(\d+)h$/.exec((retention || "").trim());
  if (!match) return null;
  const hours = Number(match[1]);
  return Math.floor(Math.max(hours - 48, hours / 2) / 24);
}

export function pauseConfirmText(retention?: string, retentionText?: string): string {
  const days = pauseSafeDays(retention);
  const base = "停用后不再拉取和复核。";
  const risk =
    days === null
      ? ""
      : `线上只保留 ${retentionText || retention}：停用超过约 ${days} 天，最早的数据会在归档前被删除、无法补齐。`;
  return `${base}${risk}重新启用后会立即复核。`;
}

// ---- 分区预览 ----

/** 解析分区预览（表头加前 20 行的 CSV，RFC 4180：引号、引号内的逗号与换行、"" 转义）。不是完整的 CSV 时返回 null。 */
export function parsePreviewCsv(text: string): { header: string[]; rows: string[][] } | null {
  if (!text.trim()) return null;
  const records: string[][] = [];
  let field = "";
  let record: string[] = [];
  let quoted = false;
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i += 1;
        } else quoted = false;
      } else field += c;
      continue;
    }
    if (c === '"' && field === "") quoted = true;
    else if (c === ",") {
      record.push(field);
      field = "";
    } else if (c === "\n" || c === "\r") {
      if (c === "\r" && text[i + 1] === "\n") i += 1;
      record.push(field);
      records.push(record);
      record = [];
      field = "";
    } else field += c;
  }
  if (quoted) return null;
  if (field !== "" || record.length) {
    record.push(field);
    records.push(record);
  }
  const [header, ...rows] = records;
  if (!header?.length) return null;
  return { header, rows: rows.filter(row => row.length === header.length) };
}

// ---- URL 状态 ----

export type DetailTab = "runs" | "partitions" | "recent";
const DETAIL_TABS: DetailTab[] = ["runs", "partitions", "recent"];

export interface ArchiveQuery {
  /** 任务详情的 dataset_id */
  task: string;
  tab: DetailTab;
  /** 运行记录的状态筛选，空表示全部 */
  status: ArchiveRunStatus | "";
  /** 是否列出成功的到点拉取 */
  ticks: boolean;
  rpage: number;
  ppage: number;
  /** 分区起止月 YYYY-MM */
  from: string;
  to: string;
  /** 预览的分区键 */
  partition: string;
  /** 最新数据的标的 */
  subject: string;
  /** 打开新建抽屉并预选的数据集 */
  create: string;
}

const first = (value: LocationQuery[string]) => (Array.isArray(value) ? value[0] : value) ?? "";
const month = (value: string) => (/^\d{4}-(0[1-9]|1[0-2])$/.test(value) ? value : "");
const pageNo = (value: string) => (/^[1-9]\d*$/.test(value) ? Number(value) : 1);

export function parseArchiveQuery(query: LocationQuery): ArchiveQuery {
  const status = first(query.status);
  return {
    task: first(query.task),
    tab: (DETAIL_TABS as string[]).includes(first(query.tab)) ? (first(query.tab) as DetailTab) : "runs",
    status: (RUN_STATUSES as string[]).includes(status) ? (status as ArchiveRunStatus) : "",
    ticks: first(query.ticks) === "1",
    rpage: pageNo(first(query.rpage)),
    ppage: pageNo(first(query.ppage)),
    from: month(first(query.from)),
    to: month(first(query.to)),
    partition: first(query.partition),
    subject: first(query.subject),
    create: first(query.create)
  };
}

/** 序列化：默认值不写进 URL；没有打开任务详情时详情相关的参数都不写。 */
export function archiveQueryFor(state: Partial<ArchiveQuery>): LocationQueryRaw {
  const out: LocationQueryRaw = {};
  if (state.create) out.create = state.create;
  if (!state.task) return out;
  out.task = state.task;
  if (state.tab === "partitions") {
    out.tab = "partitions";
    if (state.from) out.from = state.from;
    if (state.to) out.to = state.to;
    if (state.ppage && state.ppage > 1) out.ppage = String(state.ppage);
    if (state.partition) out.partition = state.partition;
  } else if (state.tab === "recent") {
    out.tab = "recent";
    if (state.subject) out.subject = state.subject;
  } else {
    if (state.status) out.status = state.status;
    if (state.ticks) out.ticks = "1";
    if (state.rpage && state.rpage > 1) out.rpage = String(state.rpage);
  }
  return out;
}
