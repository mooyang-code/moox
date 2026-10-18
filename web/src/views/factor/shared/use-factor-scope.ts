import { computed, onActivated, onDeactivated, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import type { FactorSetInfo } from "@/api/factor/types";
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
 * 切换「计算任务」标题 Tab 时的 query：保留 set，默认 Tab 不写 tab；
 * detail 只属于「计算任务」Tab，job 只属于「补算」Tab，其余一律清掉。
 */
export function buildTabQuery(query: Record<string, unknown>, tab: ComputeTaskTab): Record<string, string> {
  const set = queryString(query.set);
  const detail = queryString(query.detail);
  const job = queryString(query.job);
  return compactQuery({
    tab: tab === "tasks" ? undefined : tab,
    set,
    detail: tab === "tasks" ? detail : undefined,
    job: tab === "recalc" ? job : undefined
  });
}

export interface FactorScopeOptions {
  /** 必选计算任务的页面（计算结果、补算）：?set= → store.currentSetId → 第一个，并把结果写回 URL。 */
  requireSet?: boolean;
  /** 轮询；默认 10 秒，false 关闭。 */
  poll?: boolean;
  pollMs?: number;
}

/**
 * 因子页面共用的样板：空间切换加载、?set= 与 store 同步、轮询。
 * 主区域是 keep-alive 且按 fullPath 重挂载，所以只在空间 id 变化时 reset + load，同一空间重新挂载直接复用 store。
 */
export function useFactorScope(options: FactorScopeOptions = {}) {
  const requireSet = options.requireSet ?? false;
  const route = useRoute();
  const router = useRouter();
  const store = useFactorStore();
  const spaceStore = useSpaceStore();
  const ownPath = route.path;
  const active = ref(true);

  const spaceId = computed(() => spaceStore.selectedSpaceId);
  const routeSetId = computed(() => queryString(route.query.set));

  onActivated(() => {
    active.value = true;
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

  function hasSet(setId: string) {
    return Boolean(setId) && store.sets.some((item: FactorSetInfo) => item.factor_set.set_id === setId);
  }

  function resolveSelection() {
    if (!requireSet || !canWriteRoute()) return;
    const resolved = [routeSetId.value, store.currentSetId].find(hasSet) ?? store.sets[0]?.factor_set.set_id ?? "";
    store.select(resolved);
    if (resolved !== routeSetId.value) void navigate({ set: resolved || undefined });
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

  watch(routeSetId, value => {
    if (requireSet && canWriteRoute() && hasSet(value)) store.select(value);
  });

  watch(
    () => store.currentSetId,
    value => {
      if (requireSet && canWriteRoute() && value !== routeSetId.value) void navigate({ set: value || undefined });
    }
  );

  function selectSet(setId: string) {
    store.select(setId);
    if (requireSet) void navigate({ set: setId, job: undefined });
  }

  if (options.poll !== false) {
    usePolling(
      () => Promise.all([store.reload(), store.refreshEngine()]),
      options.pollMs ?? 10_000,
      () => Boolean(spaceId.value)
    );
  }

  return { store, spaceId, routeSetId, selectSet, navigate, reload };
}
