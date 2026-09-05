import { ref } from "vue";
import { defineStore } from "pinia";
import {
  getInstance,
  getRunner,
  getStrategy,
  listInstances,
  listRunners,
  listStrategies,
  listStrategyResults,
  listStrategyTargets
} from "@/api/strategy";
import type { InstrumentTarget, Strategy, StrategyInstance, StrategyResult, StrategyRunner } from "@/api/strategy-types";

export const useStrategyStore = defineStore("strategy", () => {
  const strategies = ref<Strategy[]>([]);
  const runners = ref<StrategyRunner[]>([]);
  const instances = ref<StrategyInstance[]>([]);
  const runner = ref<StrategyRunner | null>(null);
  const instance = ref<StrategyInstance | null>(null);
  const strategy = ref<Strategy | null>(null);
  const results = ref<StrategyResult[]>([]);
  const targets = ref<InstrumentTarget[]>([]);
  const commandSequence = ref("0");
  const totalStrategies = ref(0);
  const totalRunners = ref(0);
  const totalInstances = ref(0);
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

  async function loadInstances(params: Parameters<typeof listInstances>[0] = {}) {
    loading.value = true;
    error.value = "";
    try {
      const result = await listInstances(params);
      instances.value = result.items;
      totalInstances.value = result.page.total;
    } catch (err) {
      error.value = err instanceof Error ? err.message : "实例列表加载失败";
    } finally {
      loading.value = false;
    }
  }

  async function loadRunnerDetail(runnerId: string) {
    loading.value = true;
    error.value = "";
    instance.value = null;
    results.value = [];
    targets.value = [];
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

  async function loadInstanceDetail(instanceId: string) {
    loading.value = true;
    error.value = "";
    runner.value = null;
    results.value = [];
    targets.value = [];
    try {
      const instanceRsp = await getInstance(instanceId);
      const current = instanceRsp.instance;
      const [resultRsp, targetRsp, strategyRsp] = await Promise.all([
        listStrategyResults("", { instance_id: current.instance_id, session_id: current.session_id, page: 1, page_size: 50 }),
        listStrategyTargets("", current.instance_id),
        getStrategy(current.strategy_id)
      ]);
      instance.value = current;
      results.value = resultRsp.items;
      targets.value = targetRsp.targets;
      commandSequence.value = "";
      strategy.value = strategyRsp.strategy;
    } catch (err) {
      error.value = err instanceof Error ? err.message : "实例详情加载失败";
      throw err;
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
    instances,
    runner,
    instance,
    strategy,
    results,
    targets,
    commandSequence,
    totalStrategies,
    totalRunners,
    totalInstances,
    loading,
    error,
    loadStrategies,
    loadRunners,
    loadInstances,
    loadRunnerDetail,
    loadInstanceDetail,
    startPolling,
    stopPolling
  };
});
