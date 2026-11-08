import { computed, onActivated, onDeactivated, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import type { FactorTaskInfo } from "@/api/factor/types";
import { usePolling } from "@/hooks/usePolling";
import { useFactorStore } from "@/store/modules/factor";
import { useSpaceStore } from "@/store/modules/space";

export type ComputeTaskTab = "tasks" | "results" | "recalc";

type QueryValue = string | undefined;
type QueryMap = Record<string, QueryValue>;

function queryString(value: unknown): string {
  const first = Array.isArray(value) ? value[0] : value;
  return typeof first === "string" ? first : "";
}

function compactQuery(query: QueryMap): Record<string, string> {
  const next: Record<string, string> = {};
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") next[key] = value;
  }
  return next;
}

/**
 * 切换「计算任务」标题 Tab 时的 query：保留 task 和因子条件 fc（切回结果页时条件还在，换任务时才清），默认 Tab 不写 tab；
 * detail 只属于「计算任务」Tab，job 只属于「补算」Tab，其余一律清掉。
 */
export function buildTabQuery(query: Record<string, unknown>, tab: ComputeTaskTab): Record<string, string> {
  const task = queryString(query.task);
  const detail = queryString(query.detail);
  const job = queryString(query.job);
  return compactQuery({
    tab: tab === "tasks" ? undefined : tab,
    task,
    fc: queryString(query.fc),
    detail: tab === "tasks" ? detail : undefined,
    job: tab === "recalc" ? job : undefined
  });
}

export interface FactorScopeOptions {
  /** 必选计算任务的页面（计算结果、补算）：?task= → store.currentTaskId → 第一个，并把结果写回 URL。 */
  requireTask?: boolean;
  /** 轮询；默认 10 秒，false 关闭。 */
  poll?: boolean;
  pollMs?: number;
  /** 和计算任务、引擎状态同一轮刷新的其他读取 */
  extraPoll?: () => Promise<unknown>;
}

/**
 * 因子页面共用的样板：空间切换加载、?task= 与 store 同步、轮询。
 * 因子页面声明了 reuseOnQuery，query 变化时组件不重建。只在空间 id 变化时 reset + load，同一空间重新挂载直接复用 store。
 * 计算任务页内部用 keep-alive 缓存页签：失活期间不跟随 ?task=，重新激活时按 URL 补同步一次。
 */
export function useFactorScope(options: FactorScopeOptions = {}) {
  const requireTask = options.requireTask ?? false;
  const route = useRoute();
  const router = useRouter();
  const store = useFactorStore();
  const spaceStore = useSpaceStore();
  const ownPath = route.path;
  const active = ref(true);

  const spaceId = computed(() => spaceStore.selectedSpaceId);
  const routeTaskId = computed(() => queryString(route.query.task));

  onActivated(() => {
    active.value = true;
    // 失活期间别处可能改了 ?task=（例如从任务详情点“查看结果”）；store 未加载时由空间 watch 负责。
    if (store.isLoadedFor(spaceId.value)) resolveSelection();
  });
  onDeactivated(() => {
    active.value = false;
  });

  function canWriteRoute() {
    return active.value && route.path === ownPath;
  }

  function navigate(query: QueryMap) {
    const merged = compactQuery({ ...(route.query as QueryMap), ...query });
    return router.replace({ path: route.path, query: merged });
  }

  function hasTask(taskId: string) {
    return Boolean(taskId) && store.tasks.some((item: FactorTaskInfo) => item.factor_task.task_id === taskId);
  }

  function resolveSelection() {
    if (!requireTask || !canWriteRoute()) return;
    const resolved = [routeTaskId.value, store.currentTaskId].find(hasTask) ?? store.tasks[0]?.factor_task.task_id ?? "";
    store.select(resolved);
    if (resolved !== routeTaskId.value) void navigate({ task: resolved || undefined });
  }

  async function reload() {
    await store.load(spaceId.value);
    void store.refreshEngine();
    resolveSelection();
  }

  watch(
    spaceId,
    async next => {
      if (store.isLoadedFor(next)) {
        void store.refreshEngine();
        resolveSelection();
        return;
      }
      store.reset();
      if (next) await reload();
    },
    { immediate: true }
  );

  watch(routeTaskId, value => {
    if (requireTask && canWriteRoute() && hasTask(value)) store.select(value);
  });

  // 计算任务变了：因子条件 fc 只属于原来的任务。selectTask 自己也会去掉 fc，但它的跳转还没生效时这里会用旧 query 合并，必须同样去掉。
  watch(
    () => store.currentTaskId,
    value => {
      if (requireTask && canWriteRoute() && value !== routeTaskId.value)
        void navigate({ task: value || undefined, fc: undefined });
    }
  );

  /** 切换计算任务：job 与因子条件 fc 都只属于原来的任务，一并清掉。 */
  function selectTask(taskId: string) {
    store.select(taskId);
    if (requireTask) void navigate({ task: taskId, job: undefined, fc: undefined });
  }

  if (options.poll !== false) {
    // 页面要一起刷新的其他数据（例如总览的补算作业）放进同一轮，不再各自开一个定时器
    usePolling(
      () => Promise.all([store.reload(), store.refreshEngine(), options.extraPoll?.()]),
      options.pollMs ?? 10_000,
      () => Boolean(spaceId.value)
    );
  }

  return { store, spaceId, routeTaskId, selectTask, navigate, reload };
}
