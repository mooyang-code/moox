import { computed, onUnmounted, ref, watch, type Ref } from "vue";
import { useStrategyStore } from "@/store/modules/strategy";
import { deriveTargetState, type TargetState } from "@/views/strategy/model";
import { useNowClock } from "@/views/strategy/use-now-clock";

export type ResultScope = "session" | "all";

/**
 * 一个实例的「当前目标 + 历史结果」：读取、翻页、定时刷新与卸载清理。实例详情页与「运行结果」Tab 共用。
 * 数据存放在策略 store 里（同一时刻只有一个页面使用它）；instanceId 变化时清空旧实例并重新读取。
 */
export function useInstanceResults(
  instanceId: Ref<string>,
  options: {
    /** 关闭后定时刷新暂停（页面仍可手动刷新）。 */
    autoRefresh?: Ref<boolean>;
    /** 启停结果未知期间不判断目标状态。 */
    controlUncertain?: Ref<boolean>;
    /** 每次读取完成后调用（applied 为假表示被更新的读取取代）。 */
    onLoaded?: (applied: boolean) => void | Promise<void>;
    /** 换实例时调用，页面借此重置自己的状态。 */
    onReset?: () => void;
  } = {}
) {
  const store = useStrategyStore();
  const resultPage = ref(1);
  const resultScope = ref<ResultScope>("session");
  const resultPageSize = 20;
  // readAt 是最近一次成功读取实例与目标的时间。
  const readAt = ref("");
  // refreshing 只跟踪用户手动刷新；后台定时刷新不让按钮转圈。
  const refreshing = ref(false);
  // 目标有效期要随时间流逝重算：关闭自动刷新后页面不再请求数据，只读 Date.now() 的 computed 不会自己更新。
  const nowMs = useNowClock();

  // 还没有成功读到过这个实例时的加载：页面应显示“读取中”，而不是“无法确认”“暂无结果”。
  const initialLoading = computed(() => Boolean(instanceId.value) && !readAt.value && !store.detailError);

  const targetState = computed<TargetState>(() =>
    store.instance && store.instance.instance_id === instanceId.value && !options.controlUncertain?.value
      ? deriveTargetState(store.instance, store.targetSnapshot, nowMs.value)
      : "unknown"
  );

  async function load(): Promise<boolean> {
    if (!instanceId.value) return false;
    try {
      const applied = await store.loadInstanceDetail(
        instanceId.value,
        resultPage.value,
        resultPageSize,
        resultScope.value === "all"
      );
      if (applied) readAt.value = new Date().toLocaleTimeString("zh-CN", { hour12: false });
      await options.onLoaded?.(applied);
      return applied;
    } catch {
      // 错误由 store 记录并在页面上展示。
      return false;
    }
  }

  /** 用户手动刷新：按钮显示加载，直到这次读取结束。 */
  async function refresh(): Promise<boolean> {
    refreshing.value = true;
    try {
      return await load();
    } finally {
      refreshing.value = false;
    }
  }

  async function loadResults(page: number, scope: ResultScope) {
    resultPage.value = page;
    resultScope.value = scope;
    try {
      await store.loadResultPage(page, resultPageSize, scope === "all");
    } catch {
      // 翻页失败：表格仍是上一次成功读取的数据，页码与范围退回到它所属的那一页，否则分页器停在失败的页码上点不动。
      resultPage.value = store.loadedResultPage;
      resultScope.value = store.loadedResultAllHistory ? "all" : "session";
    }
  }

  function start() {
    store.stopPolling();
    store.clearDetail();
    resultPage.value = 1;
    resultScope.value = "session";
    readAt.value = "";
    options.onReset?.();
    if (!instanceId.value) return;
    void load();
    store.startPolling(() => (options.autoRefresh?.value === false ? undefined : load()));
  }

  watch(instanceId, (next, previous) => {
    if (next !== previous) start();
  });
  onUnmounted(() => {
    store.stopPolling();
    store.clearDetail();
  });

  return {
    store,
    resultPage,
    resultScope,
    resultPageSize,
    readAt,
    refreshing,
    initialLoading,
    targetState,
    load,
    refresh,
    loadResults,
    start
  };
}
