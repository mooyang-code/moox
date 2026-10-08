import { ref } from "vue";
import { defineStore } from "pinia";
import {
  deleteInstance,
  deleteStrategy,
  getInstance,
  getStrategy,
  getStrategyResult,
  listInstances,
  listStrategies,
  listStrategyResults,
  listStrategyTargets,
  setInstanceEnabled
} from "@/api/strategy";
import type {
  Strategy,
  StrategyInstance,
  StrategyResult,
  StrategyResultDetail,
  StrategyTargetSnapshot
} from "@/api/strategy-types";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

export const useStrategyStore = defineStore("strategy", () => {
  const strategies = ref<Strategy[]>([]);
  const instances = ref<StrategyInstance[]>([]);
  const instance = ref<StrategyInstance | null>(null);
  const strategy = ref<Strategy | null>(null);
  const results = ref<StrategyResult[]>([]);
  const resultDetails = ref<Record<string, StrategyResultDetail>>({});
  const targetSnapshot = ref<StrategyTargetSnapshot | null>(null);
  const totalStrategies = ref(0);
  const strategiesComplete = ref(false);
  const totalInstances = ref(0);
  const totalResults = ref(0);
  const listLoading = ref(false);
  const detailLoading = ref(false);
  const error = ref("");
  const detailError = ref("");
  const operationLoading = ref(false);
  const poller = ref<ReturnType<typeof setInterval> | null>(null);
  let pollingBusy = false;
  let detailRequest = 0;
  let resultRequest = 0;
  let strategiesRequest = 0;
  let instancesRequest = 0;

  async function loadStrategies(params: Parameters<typeof listStrategies>[0] = {}) {
    const requestId = ++strategiesRequest;
    listLoading.value = true;
    error.value = "";
    try {
      const result = await listStrategies(params);
      if (requestId !== strategiesRequest) return;
      strategies.value = result.items;
      totalStrategies.value = result.page.total;
      strategiesComplete.value = result.items.length >= result.page.total;
    } catch (err) {
      if (requestId === strategiesRequest) error.value = errorMessage(err, "策略定义加载失败");
      throw err;
    } finally {
      if (requestId === strategiesRequest) listLoading.value = false;
    }
  }

  async function loadAllStrategies(pageSize = 200) {
    const requestId = ++strategiesRequest;
    listLoading.value = true;
    error.value = "";
    try {
      const items: Strategy[] = [];
      let total = 0;
      for (let page = 1; ; page += 1) {
        const response = await listStrategies({ page, page_size: pageSize });
        if (requestId !== strategiesRequest) return;
        items.push(...response.items);
        total = response.page.total;
        if (!response.items.length || items.length >= total) break;
      }
      strategies.value = items;
      totalStrategies.value = total || items.length;
      strategiesComplete.value = true;
    } catch (err) {
      if (requestId === strategiesRequest) error.value = errorMessage(err, "策略定义加载失败");
      throw err;
    } finally {
      if (requestId === strategiesRequest) listLoading.value = false;
    }
  }

  async function loadInstances(params: Parameters<typeof listInstances>[0] = {}) {
    const requestId = ++instancesRequest;
    listLoading.value = true;
    error.value = "";
    try {
      const result = await listInstances(params);
      if (requestId !== instancesRequest) return;
      instances.value = result.items;
      totalInstances.value = result.page.total;
    } catch (err) {
      if (requestId === instancesRequest) error.value = errorMessage(err, "策略实例加载失败");
      throw err;
    } finally {
      if (requestId === instancesRequest) listLoading.value = false;
    }
  }

  /** 读取实例详情：实例 → 定义 → 最新目标；历史结果异步加载，不阻塞目标显示。 */
  async function loadInstanceDetail(instanceId: string, page = 1, pageSize = 20, allHistory = false) {
    const requestId = ++detailRequest;
    const resultRequestId = ++resultRequest;
    detailLoading.value = true;
    detailError.value = "";
    try {
      const current = await getInstance(instanceId);
      if (requestId !== detailRequest) return false;
      instance.value = current;
      const definition = await getStrategy(current.strategy_id).catch(() => null);
      if (requestId !== detailRequest) return false;
      strategy.value = definition;
      const resultTask =
        current.session_id || allHistory
          ? listStrategyResults(instanceId, {
              session_id: allHistory ? undefined : current.session_id,
              page,
              page_size: pageSize
            })
          : Promise.resolve(null);
      void resultTask
        .then(response => {
          if (requestId !== detailRequest || resultRequestId !== resultRequest) return;
          results.value = response?.items ?? [];
          totalResults.value = response?.page.total ?? 0;
        })
        .catch(err => {
          if (requestId === detailRequest && resultRequestId === resultRequest)
            detailError.value = errorMessage(err, "策略结果加载失败");
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

  async function loadResultPage(page: number, pageSize = 20, allHistory = false) {
    if (!instance.value) return;
    if (!allHistory && !instance.value.session_id) {
      results.value = [];
      totalResults.value = 0;
      detailError.value = "";
      return;
    }
    const requestId = ++resultRequest;
    detailError.value = "";
    try {
      const response = await listStrategyResults(instance.value.instance_id, {
        page,
        page_size: pageSize,
        session_id: allHistory ? undefined : instance.value.session_id || undefined
      });
      if (requestId !== resultRequest) return;
      results.value = response.items;
      totalResults.value = response.page.total;
    } catch (err) {
      if (requestId === resultRequest) detailError.value = errorMessage(err, "策略结果加载失败");
      throw err;
    }
  }

  /** 读取一个结果的解释明细；同一结果只请求一次。 */
  async function loadResultDetail(resultId: string): Promise<StrategyResultDetail> {
    const cached = resultDetails.value[resultId];
    if (cached) return cached;
    const detail = await getStrategyResult(resultId);
    resultDetails.value = { ...resultDetails.value, [resultId]: detail };
    return detail;
  }

  async function changeEnabled(instanceId: string, enabled: boolean) {
    operationLoading.value = true;
    try {
      return await setInstanceEnabled(instanceId, enabled);
    } finally {
      operationLoading.value = false;
    }
  }

  async function removeInstance(instanceId: string) {
    operationLoading.value = true;
    try {
      await deleteInstance(instanceId);
      instances.value = instances.value.filter(item => item.instance_id !== instanceId);
      totalInstances.value = Math.max(0, totalInstances.value - 1);
    } finally {
      operationLoading.value = false;
    }
  }

  async function removeStrategy(strategyId: string) {
    operationLoading.value = true;
    try {
      await deleteStrategy(strategyId);
      strategies.value = strategies.value.filter(item => item.strategy_id !== strategyId);
      totalStrategies.value = Math.max(0, totalStrategies.value - 1);
    } finally {
      operationLoading.value = false;
    }
  }

  function startPolling(callback: () => void | Promise<void>, interval = 10000) {
    stopPolling();
    poller.value = setInterval(async () => {
      if (pollingBusy || (typeof document !== "undefined" && document.visibilityState !== "visible")) return;
      pollingBusy = true;
      try {
        await callback();
      } finally {
        pollingBusy = false;
      }
    }, interval);
  }

  function stopPolling() {
    if (poller.value) clearInterval(poller.value);
    poller.value = null;
    pollingBusy = false;
  }

  function clearDetail() {
    detailRequest += 1;
    resultRequest += 1;
    instance.value = null;
    strategy.value = null;
    results.value = [];
    resultDetails.value = {};
    targetSnapshot.value = null;
    totalResults.value = 0;
    detailError.value = "";
  }

  return {
    strategies,
    instances,
    instance,
    strategy,
    results,
    resultDetails,
    targetSnapshot,
    totalStrategies,
    strategiesComplete,
    totalInstances,
    totalResults,
    listLoading,
    detailLoading,
    operationLoading,
    error,
    detailError,
    loadStrategies,
    loadAllStrategies,
    loadInstances,
    loadInstanceDetail,
    loadResultPage,
    loadResultDetail,
    changeEnabled,
    removeInstance,
    removeStrategy,
    startPolling,
    stopPolling,
    clearDetail
  };
});
