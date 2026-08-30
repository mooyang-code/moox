import { ref } from "vue";
import { defineStore } from "pinia";
import {
  getRunner,
  getStrategy,
  listRunners,
  listStrategies,
  listStrategyResults,
  listStrategyTargets
} from "@/api/strategy";
import type { InstrumentTarget, Strategy, StrategyResult, StrategyRunner } from "@/api/strategy-types";

export const useStrategyStore = defineStore("strategy", () => {
  const strategies = ref<Strategy[]>([]);
  const runners = ref<StrategyRunner[]>([]);
  const runner = ref<StrategyRunner | null>(null);
  const strategy = ref<Strategy | null>(null);
  const results = ref<StrategyResult[]>([]);
  const targets = ref<InstrumentTarget[]>([]);
  const commandSequence = ref("0");
  const totalStrategies = ref(0);
  const totalRunners = ref(0);
  const loading = ref(false);
  const error = ref("");
  const poller = ref<ReturnType<typeof setInterval> | null>(null);

  async function loadStrategies(params: Parameters<typeof listStrategies>[0] = {}) {
    loading.value = true;
    error.value = "";
    try {
      const result = await listStrategies(params);
      strategies.value = result.items;
      totalStrategies.value = result.page.total;
    } catch (err) {
      error.value = err instanceof Error ? err.message : "策略列表加载失败";
    } finally {
      loading.value = false;
    }
  }

  async function loadRunners(params: Parameters<typeof listRunners>[0] = {}) {
    loading.value = true;
    error.value = "";
    try {
      const result = await listRunners(params);
      runners.value = result.items;
      totalRunners.value = result.page.total;
    } catch (err) {
      error.value = err instanceof Error ? err.message : "Runner 列表加载失败";
    } finally {
      loading.value = false;
    }
  }

  async function loadRunnerDetail(runnerId: string) {
    loading.value = true;
    error.value = "";
    try {
      const [runnerRsp, resultRsp, targetRsp] = await Promise.all([
        getRunner(runnerId),
        listStrategyResults(runnerId, { page: 1, page_size: 50 }),
        listStrategyTargets(runnerId)
      ]);
      runner.value = runnerRsp.runner;
      results.value = resultRsp.items;
      targets.value = targetRsp.targets;
      commandSequence.value = targetRsp.command_sequence;
      strategy.value = (await getStrategy(runnerRsp.runner.strategy_id)).strategy;
    } catch (err) {
      error.value = err instanceof Error ? err.message : "Runner 详情加载失败";
    } finally {
      loading.value = false;
    }
  }

  function startPolling(callback: () => void, interval = 15000) {
    stopPolling();
    poller.value = setInterval(callback, interval);
  }

  function stopPolling() {
    if (poller.value) clearInterval(poller.value);
    poller.value = null;
  }

  return {
    strategies,
    runners,
    runner,
    strategy,
    results,
    targets,
    commandSequence,
    totalStrategies,
    totalRunners,
    loading,
    error,
    loadStrategies,
    loadRunners,
    loadRunnerDetail,
    startPolling,
    stopPolling
  };
});
