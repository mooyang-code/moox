import type { EngineStatus, FactorTaskInfo, RecalcJob } from "@/api/factor/types";
import { toInstantMs } from "@/utils/time-zone";

// 计算任务列表页顶部的引擎状态条与补算摘要。

const RECENT_FAILURE_MS = 24 * 3600 * 1000;

export interface StatItem {
  key: "engine" | "consumer" | "compute" | "queue" | "warmup" | "recalc";
  label: string;
  value: string;
  hint: string;
  tone: "ok" | "danger" | "muted" | "info";
}

/** 表达式执行器：计算线程数与 expr 语义版本；引擎与管理端版本不一致时说明二者没有一起升级，需要提示。 */
export function exprStat(engine: EngineStatus | null): StatItem {
  if (!engine) return { key: "compute", label: "表达式引擎", value: "-", hint: "计算线程 · expr 版本", tone: "muted" };
  const engineVersion = engine.engine_expr_version || "";
  const managerVersion = engine.manager_expr_version || "";
  const mismatch = Boolean(engineVersion && managerVersion && engineVersion !== managerVersion);
  return {
    key: "compute",
    label: "表达式引擎",
    value: `${engine.compute_workers ?? "-"} 线程`,
    hint: mismatch
      ? `版本不一致：引擎 ${engineVersion}，管理端 ${managerVersion}`
      : engineVersion
        ? `expr 版本 ${engineVersion}`
        : "计算线程",
    tone: mismatch ? "danger" : "info"
  };
}

export function statsStrip(status: EngineStatus | null | undefined, runningRecalc: number): StatItem[] {
  // 引擎离线或从未上报心跳时，管理端留着的只是最后一次快照，不能当作当前的运行状态展示。
  const engine = status?.engine?.online ? status : null;
  const runners = engine?.runners ?? [];
  const queued = runners.reduce((total, runner) => total + runner.queued, 0);
  const active = runners.filter(runner => runner.active).length;
  return [
    engineStat(status),
    {
      key: "consumer",
      label: "实时消费",
      value: engine ? (engine.consumer_running ? "运行中" : "已停止") : "-",
      hint: "周期事件消费",
      tone: engine ? (engine.consumer_running ? "ok" : "danger") : "muted"
    },
    exprStat(engine),
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
      hint: "启用后需要手动补算历史",
      tone: runningRecalc ? "info" : "muted"
    }
  ];
}

/** 数据预热：引擎启动或计算任务变更后，对象载入回看窗口前的结果不对外提供。 */
function warmupStat(engine: EngineStatus | null | undefined): StatItem {
  const base = { key: "warmup" as const, label: "数据预热" };
  if (!engine) return { ...base, value: "-", hint: "引擎载入回看窗口", tone: "muted" };
  const runners = engine.runners ?? [];
  const warming = runners.filter(runner => runner.warmup_state === "warming");
  if (!warming.length) return { ...base, value: "已就绪", hint: "因子结果正式对外提供", tone: "ok" };
  const warm = warming.reduce((total, runner) => total + (runner.warm_subjects ?? 0), 0);
  const expected = warming.reduce((total, runner) => total + (runner.expected_subjects ?? 0), 0);
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

/** 进行中补算只对已启用的计算任务扇出查询（ListRecalcJobs 必须带 task_id）。 */
export function recalcQueryTasks(sets: FactorTaskInfo[]) {
  return sets.filter(info => info.factor_task.status === "enabled").map(info => info.factor_task.task_id);
}

/** 读到的补算作业分成进行中的与近 24 小时失败的（按更新时间，读不出时间的不算）。 */
export function splitRecalcJobs(jobs: RecalcJob[], now = Date.now()) {
  return {
    running: jobs.filter(job => job.status === "queued" || job.status === "running"),
    recentFailed: jobs.filter(job => {
      if (job.status !== "failed") return false;
      const updated = toInstantMs(job.updated_at) ?? toInstantMs(job.created_at);
      return updated !== null && now - updated <= RECENT_FAILURE_MS;
    })
  };
}

/** 补算作业在补算页签里的地址。 */
export function recalcJobTarget(job: Pick<RecalcJob, "task_id" | "job_id">) {
  return { path: "/factor/tasks", query: { tab: "recalc", task: job.task_id, job: job.job_id } };
}
