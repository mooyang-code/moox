import type { EngineStatus, FactorSet, FactorSetInfo, RecalcJob } from "@/api/factor/types";
import { setHealth, type SetHealth } from "@/views/factor/shared/health";
import { memberStatusTag, periodStatusTag } from "@/views/factor/shared/status";

export interface RouteTarget {
  path: string;
  query: Record<string, string>;
}

const TASKS_PATH = "/factor/tasks";
const RECENT_FAILURE_MS = 24 * 3600 * 1000;

/** 跨页跳转：用 ?set= 传计算任务，?tab= 选 Tab，?detail= 打开详情抽屉。 */
export function cardLinks(setId: string) {
  const detail: RouteTarget = { path: TASKS_PATH, query: { detail: setId } };
  return {
    detail,
    results: { path: TASKS_PATH, query: { tab: "results", set: setId } } as RouteTarget,
    recalc: { path: TASKS_PATH, query: { tab: "recalc", set: setId } } as RouteTarget,
    factors: { ...detail, query: { ...detail.query } } as RouteTarget
  };
}

export interface StatItem {
  key: "engine" | "consumer" | "workers" | "queue" | "warmup" | "recalc";
  label: string;
  value: string;
  hint: string;
  tone: "ok" | "danger" | "muted" | "info";
}

export function statsStrip(engine: EngineStatus | null | undefined, runningRecalc: number): StatItem[] {
  const lanes = engine?.lanes ?? [];
  const queued = lanes.reduce((total, lane) => total + lane.queued, 0);
  const active = lanes.filter(lane => lane.active).length;
  return [
    engineStat(engine),
    {
      key: "consumer",
      label: "实时消费",
      value: engine ? (engine.consumer_running ? "运行中" : "已停止") : "-",
      hint: "周期事件消费",
      tone: engine ? (engine.consumer_running ? "ok" : "danger") : "muted"
    },
    {
      key: "workers",
      label: "Python Worker",
      value: engine ? `${engine.python_busy} / ${engine.python_workers}` : "-",
      hint: "忙碌 / 总数",
      tone: engine ? "info" : "muted"
    },
    {
      key: "queue",
      label: "计算队列",
      value: engine ? String(queued) : "-",
      hint: `${active} 个计算任务计算中`,
      tone: engine ? "info" : "muted"
    },
    warmupStat(engine),
    {
      key: "recalc",
      label: "进行中补算",
      value: String(runningRecalc),
      hint: "启用回填与手动补算",
      tone: runningRecalc ? "info" : "muted"
    }
  ];
}

/** 数据预热：引擎启动或计算任务变更后，对象载入回看窗口前的结果不对外提供。 */
function warmupStat(engine: EngineStatus | null | undefined): StatItem {
  const base = { key: "warmup" as const, label: "数据预热" };
  if (!engine) return { ...base, value: "-", hint: "引擎载入回看窗口", tone: "muted" };
  const lanes = engine.lanes ?? [];
  const warming = lanes.filter(lane => lane.warmup_state === "warming");
  if (!warming.length) return { ...base, value: "已就绪", hint: "因子结果正式对外提供", tone: "ok" };
  const warm = warming.reduce((total, lane) => total + (lane.warm_subjects ?? 0), 0);
  const expected = warming.reduce((total, lane) => total + (lane.expected_subjects ?? 0), 0);
  return {
    ...base,
    value: "预热中",
    hint: expected
      ? `${warming.length} 个计算任务 · ${warm} / ${expected} 个对象就绪`
      : `${warming.length} 个计算任务等待首个周期`,
    tone: "info"
  };
}

/** 计算引擎卡片：引擎跑在操作员本机，管理端只能通过心跳知道它的状态。 */
function engineStat(status: EngineStatus | null | undefined): StatItem {
  const engine = status?.engine;
  if (!engine?.engine_id) {
    return { key: "engine", label: "计算引擎", value: "未连接", hint: "尚未收到引擎心跳", tone: status ? "danger" : "muted" };
  }
  if (!engine.online) {
    return { key: "engine", label: "计算引擎", value: "离线", hint: `${engine.engine_id} · 心跳已中断`, tone: "danger" };
  }
  return {
    key: "engine",
    label: "计算引擎",
    value: "在线",
    hint: engine.catalog_in_sync ? engine.engine_id : `${engine.engine_id} · 引擎目录未同步`,
    tone: engine.catalog_in_sync ? "ok" : "info"
  };
}

export interface MemberChip {
  factorId: string;
  label: string;
  color: string;
}

export interface TaskCard {
  setId: string;
  title: string;
  health: SetHealth;
  chips: MemberChip[];
  links: ReturnType<typeof cardLinks>;
}

function memberChips(info: FactorSetInfo): MemberChip[] {
  const states = new Map((info.last_run?.factors ?? []).map(state => [state.factor_id, state]));
  return info.members.map(member => {
    const state = states.get(member.factor_id);
    if (member.status === "disabled") return { factorId: member.factor_id, ...memberStatusTag("disabled") };
    if (!state) return { factorId: member.factor_id, label: "等待周期", color: "gray" };
    const tag = periodStatusTag(state.status);
    const failed = state.failed_subjects?.length ?? 0;
    return {
      factorId: member.factor_id,
      label: state.status === "degraded" && failed ? `${tag.label} · ${failed} 个对象失败` : tag.label,
      color: tag.color
    };
  });
}

export function taskCards(sets: FactorSetInfo[], labelOf: (set: FactorSet) => string): TaskCard[] {
  return sets.map(info => ({
    setId: info.factor_set.set_id,
    title: labelOf(info.factor_set),
    health: setHealth(info.factor_set, info.last_run),
    chips: memberChips(info),
    links: cardLinks(info.factor_set.set_id)
  }));
}

export type PendingKind = "failed" | "degraded" | "lagging" | "pending" | "recalc-failed";

export interface PendingItem {
  kind: PendingKind;
  setId: string;
  title: string;
  text: string;
  target: RouteTarget;
}

/** 「待处理」纯前端派生：计算任务健康 + 最近失败的补算。 */
export function pendingItems(
  sets: FactorSetInfo[],
  failedJobs: RecalcJob[],
  labelOf: (set: FactorSet) => string,
  now = Date.now()
) {
  const items: PendingItem[] = [];
  for (const info of sets) {
    const { set_id: setId } = info.factor_set;
    const title = labelOf(info.factor_set);
    const links = cardLinks(setId);
    const health = setHealth(info.factor_set, info.last_run);
    if (health.key === "failed") {
      items.push({ kind: "failed", setId, title, text: "最近周期计算失败", target: links.detail });
    } else if (health.key === "degraded") {
      const failed = info.last_run?.failed_subjects?.length ?? 0;
      items.push({
        kind: "degraded",
        setId,
        title,
        text: failed ? `降级，${failed} 个对象失败` : "最近周期降级",
        target: links.results
      });
    } else if (health.key === "lagging") {
      items.push({ kind: "lagging", setId, title, text: "结果落后超过 2 个频率周期", target: links.results });
    } else if (info.factor_set.status === "pending") {
      items.push({ kind: "pending", setId, title, text: "创建中或激活失败，可重试激活", target: links.detail });
    }
  }
  const labels = new Map(sets.map(info => [info.factor_set.set_id, labelOf(info.factor_set)]));
  for (const job of failedJobs) {
    if (job.status !== "failed") continue;
    const updated = Date.parse(job.updated_at || job.created_at);
    if (!Number.isFinite(updated) || now - updated > RECENT_FAILURE_MS) continue;
    items.push({
      kind: "recalc-failed",
      setId: job.set_id,
      title: labels.get(job.set_id) ?? job.set_id,
      text: job.error ? `补算失败：${job.error}` : "补算失败",
      target: { path: TASKS_PATH, query: { tab: "recalc", set: job.set_id, job: job.job_id } }
    });
  }
  return items;
}

/** 进行中补算只对已启用的计算任务扇出查询（ListRecalcJobs 必须带 set_id）。 */
export function recalcQuerySets(sets: FactorSetInfo[]) {
  return sets.filter(info => info.factor_set.status === "enabled").map(info => info.factor_set.set_id);
}

export function splitRecalcJobs(jobs: RecalcJob[]) {
  return {
    running: jobs.filter(job => job.status === "accepted" || job.status === "running"),
    failed: jobs.filter(job => job.status === "failed")
  };
}
