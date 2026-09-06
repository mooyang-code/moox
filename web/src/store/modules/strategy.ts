import { ref } from "vue";
import { defineStore } from "pinia";
import {
  getInstance,
  getStrategy,
  listInstances,
  listStrategies,
  listStrategyResults,
  listStrategyTargets,
  setInstanceEnabled
} from "@/api/strategy";
import type { InstrumentTarget, Strategy, StrategyInstance, StrategyResult, StrategyTargetSnapshot } from "@/api/strategy-types";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

export const useStrategyStore = defineStore("strategy", () => {
  const strategies = ref<Strategy[]>([]);
  const instances = ref<StrategyInstance[]>([]);
  const instance = ref<StrategyInstance | null>(null);
  const strategy = ref<Strategy | null>(null);
  const results = ref<StrategyResult[]>([]);
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
      let page = 1;
      let total = 0;
      do {
        const response = await listStrategies({ page, page_size: pageSize });
        if (requestId !== strategiesRequest) return;
        items.push(...response.items);
        total = response.page.total;
        if (!response.items.length || items.length >= total) break;
        page += 1;
      } while (true);
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

  async function loadInstanceDetail(instanceId: string, page = 1, pageSize = 20, allHistory = false) {
    const requestId = ++detailRequest;
    const resultRequestId = ++resultRequest;
    detailLoading.value = true;
    detailError.value = "";
    try {
      const instanceResponse = await getInstance(instanceId);
      if (requestId !== detailRequest) return false;
      const current = instanceResponse.instance;
      instance.value = current;
      const strategyResponse = await getStrategy(current.strategy_id);
      if (requestId !== detailRequest) return false;
      strategy.value = strategyResponse.strategy;
      const tasks: [Promise<StrategyTargetSnapshot | null>, Promise<{ items: StrategyResult[]; page: { total: number } }> | null] = [
        listStrategyTargets(instanceId),
        current.session_id || allHistory
          ? listStrategyResults(instanceId, { session_id: allHistory ? undefined : current.session_id, page, page_size: pageSize })
          : null
      ];
      const [targetResponse, resultResponse] = await Promise.all(tasks);
      if (requestId !== detailRequest) return false;
      targetSnapshot.value = targetResponse;
      if (resultRequestId === resultRequest) {
        results.value = resultResponse?.items ?? [];
        totalResults.value = resultResponse?.page.total ?? 0;
      }
      return true;
    } catch (err) {
      if (requestId === detailRequest) {
        detailError.value = errorMessage(err, "实例详情加载失败");
      }
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

  async function changeEnabled(instanceId: string, enabled: boolean) {
    operationLoading.value = true;
    try {
      await setInstanceEnabled(instanceId, enabled);
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
    instance.value = null;
    strategy.value = null;
    results.value = [];
    targetSnapshot.value = null;
    totalResults.value = 0;
    resultRequest += 1;
    detailError.value = "";
  }

  return {
    strategies,
    instances,
    instance,
    strategy,
    results,
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
    changeEnabled,
    startPolling,
    stopPolling,
    clearDetail
  };
});

export type StrategyTarget = InstrumentTarget;
