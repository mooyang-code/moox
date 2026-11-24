import { reactive, ref } from "vue";
import { errorMessage, RequestError } from "@/api/http";
import { getInstance, getStrategy, listStrategyResults, listStrategyTargets } from "@/api/strategy";
import type { Strategy, StrategyInstance, StrategyResult, StrategyTargetSnapshot } from "@/api/strategy-types";

/** 定义的读取结果：loaded 已读取；missing 服务端确认不存在；error 读取失败（不等于已删除）。 */
export type StrategyDefinitionStatus = "unknown" | "loaded" | "missing" | "error";

function isNotFound(error: unknown): boolean {
  return error instanceof RequestError && error.hasCode(5, "NOT_FOUND");
}

/**
 * 一个策略实例的详情：实例、定义、最新目标与历史结果。实例详情页与「运行结果」Tab 各自持有一份（见 useInstanceResults）。
 * 历史结果的加载与错误独立于实例详情：结果读取失败不能算成“实例详情加载失败”，也不能挡住启停后的状态核对。
 * loadedResultPage/loadedResultAllHistory 是当前表格里数据所属的页与范围：翻页失败时页面据此把页码退回去。
 */
export function useInstanceDetail() {
  const instance = ref<StrategyInstance | null>(null);
  const strategy = ref<Strategy | null>(null);
  const strategyStatus = ref<StrategyDefinitionStatus>("unknown");
  const results = ref<StrategyResult[]>([]);
  const targetSnapshot = ref<StrategyTargetSnapshot | null>(null);
  const totalResults = ref(0);
  const detailLoading = ref(false);
  const detailError = ref("");
  const resultsLoading = ref(false);
  const resultsError = ref("");
  const loadedResultPage = ref(1);
  const loadedResultAllHistory = ref(false);
  let detailRequest = 0;
  let resultRequest = 0;

  /** 读取实例详情：实例 → 定义 → 最新目标；历史结果异步加载，不阻塞目标显示。 */
  async function loadInstanceDetail(
    instanceId: string,
    page = 1,
    pageSize = 20,
    allHistory = false,
    /** 后台轮询传 { silent: true }：不先清掉错误条，错误记在 detailError、resultsError 里由页面显示 */
    options: { silent?: boolean } = {}
  ) {
    const requestId = ++detailRequest;
    const resultRequestId = ++resultRequest;
    const hadInstance = instance.value?.instance_id === instanceId;
    detailLoading.value = true;
    // 后台轮询不先清掉错误条（否则每轮闪一下），读取成功后再清
    if (!options.silent) detailError.value = "";
    try {
      const current = await getInstance(instanceId);
      if (requestId !== detailRequest) return false;
      instance.value = current;
      detailError.value = "";
      // 定义读取失败不阻断实例详情，但要区分“已删除”与“读取失败”：只有服务端确认 NOT_FOUND 才算已删除。
      let definition: Strategy | null = null;
      let definitionStatus: StrategyDefinitionStatus = "loaded";
      try {
        definition = await getStrategy(current.strategy_id);
      } catch (err) {
        definitionStatus = isNotFound(err) ? "missing" : "error";
      }
      if (requestId !== detailRequest) return false;
      strategy.value = definition;
      strategyStatus.value = definitionStatus;
      const resultTask =
        current.session_id || allHistory
          ? listStrategyResults(instanceId, {
              session_id: allHistory ? undefined : current.session_id,
              page,
              page_size: pageSize
            })
          : Promise.resolve(null);
      // 首次读取该实例时显示结果加载态；后台轮询刷新不遮罩，失败时保留上次的数据并提示。
      const firstLoad = !hadInstance;
      if (firstLoad) resultsLoading.value = true;
      void resultTask
        .then(response => {
          if (requestId !== detailRequest || resultRequestId !== resultRequest) return;
          results.value = response?.items ?? [];
          totalResults.value = response?.page.total ?? 0;
          loadedResultPage.value = page;
          loadedResultAllHistory.value = allHistory;
          resultsError.value = "";
        })
        .catch(err => {
          if (requestId === detailRequest && resultRequestId === resultRequest)
            resultsError.value = errorMessage(err, "策略结果加载失败");
        })
        .finally(() => {
          if (requestId === detailRequest && resultRequestId === resultRequest) resultsLoading.value = false;
        });
      const targets = await listStrategyTargets(instanceId);
      if (requestId !== detailRequest) return false;
      targetSnapshot.value = targets;
      return true;
    } catch (err) {
      if (requestId === detailRequest) detailError.value = errorMessage(err, "实例详情加载失败");
      throw err;
    } finally {
      if (requestId === detailRequest) detailLoading.value = false;
    }
  }

  /** 读取历史结果的一页：失败时保留当前表格的数据，错误记在 resultsError，调用方按 loadedResultPage 退回页码。 */
  async function loadResultPage(page: number, pageSize = 20, allHistory = false) {
    if (!instance.value) return;
    if (!allHistory && !instance.value.session_id) {
      // 使在途的“全部历史”请求作废：迟到的响应不能把旧结果填进无会话的“本次启用”表格。
      resultRequest += 1;
      results.value = [];
      totalResults.value = 0;
      loadedResultPage.value = 1;
      loadedResultAllHistory.value = false;
      resultsLoading.value = false;
      resultsError.value = "";
      return;
    }
    const requestId = ++resultRequest;
    resultsLoading.value = true;
    resultsError.value = "";
    try {
      const response = await listStrategyResults(instance.value.instance_id, {
        page,
        page_size: pageSize,
        session_id: allHistory ? undefined : instance.value.session_id || undefined
      });
      if (requestId !== resultRequest) return;
      results.value = response.items;
      totalResults.value = response.page.total;
      loadedResultPage.value = page;
      loadedResultAllHistory.value = allHistory;
    } catch (err) {
      if (requestId === resultRequest) resultsError.value = errorMessage(err, "策略结果加载失败");
      throw err;
    } finally {
      if (requestId === resultRequest) resultsLoading.value = false;
    }
  }

  function clearDetail() {
    detailRequest += 1;
    resultRequest += 1;
    instance.value = null;
    strategy.value = null;
    strategyStatus.value = "unknown";
    results.value = [];
    targetSnapshot.value = null;
    totalResults.value = 0;
    detailError.value = "";
    resultsLoading.value = false;
    resultsError.value = "";
    loadedResultPage.value = 1;
    loadedResultAllHistory.value = false;
  }

  return reactive({
    instance,
    strategy,
    strategyStatus,
    results,
    targetSnapshot,
    totalResults,
    detailLoading,
    detailError,
    resultsLoading,
    resultsError,
    loadedResultPage,
    loadedResultAllHistory,
    loadInstanceDetail,
    loadResultPage,
    clearDetail
  });
}

export type InstanceDetail = ReturnType<typeof useInstanceDetail>;
