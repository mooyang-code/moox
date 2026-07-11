import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  getStrategyOverview,
  getStrategyPerformance,
  listRunningStrategies,
  listStrategyRuns,
  pauseBinding,
  resumeBinding,
  setExecutionMode,
} from '@/api/strategy';
import type { PerformanceSource, RunningStrategySummary, StrategyOverview, StrategyPerformance, StrategyRun } from '@/api/strategy-types';

export const useStrategyStore = defineStore('strategy', () => {
  const rows = ref<RunningStrategySummary[]>([]);
  const total = ref(0);
  const loading = ref(false);
  const error = ref('');
  const overview = ref<StrategyOverview | null>(null);
  const runs = ref<StrategyRun[]>([]);
  const performance = ref<StrategyPerformance | null>(null);
  const performanceSource = ref<PerformanceSource>('paper');
  const poller = ref<ReturnType<typeof setInterval> | null>(null);

  const hasRows = computed(() => rows.value.length > 0);

  async function loadRunning(params: Parameters<typeof listRunningStrategies>[0] = {}) {
    loading.value = true;
    error.value = '';
    try {
      const result = await listRunningStrategies(params);
      rows.value = result.items;
      total.value = result.page.total ?? 0;
    } catch (err) {
      error.value = err instanceof Error ? err.message : '策略列表加载失败';
    } finally {
      loading.value = false;
    }
  }

  async function loadOverview(bindingId: string) {
    loading.value = true;
    error.value = '';
    try {
      overview.value = await getStrategyOverview(bindingId);
      const runsResult = await listStrategyRuns(bindingId, { page: 1, page_size: 20 });
      runs.value = runsResult.items;
    } catch (err) {
      error.value = err instanceof Error ? err.message : '策略详情加载失败';
    } finally {
      loading.value = false;
    }
  }

  async function loadPerformance(bindingId: string, source = performanceSource.value) {
    performanceSource.value = source;
    try {
      performance.value = await getStrategyPerformance(bindingId, source);
    } catch (err) {
      error.value = err instanceof Error ? err.message : '策略表现加载失败';
    }
  }

  function startPolling(callback: () => void, interval = 15000) {
    stopPolling();
    poller.value = setInterval(callback, interval);
  }

  function stopPolling() {
    if (poller.value) {
      clearInterval(poller.value);
      poller.value = null;
    }
  }

  async function pause(bindingId: string, reason: string) {
    return pauseBinding(bindingId, reason, crypto.randomUUID());
  }

  async function resume(bindingId: string, reason: string) {
    return resumeBinding(bindingId, reason, crypto.randomUUID());
  }

  async function changeMode(bindingId: string, mode: string, reason: string) {
    return setExecutionMode(bindingId, mode, reason, crypto.randomUUID());
  }

  return { rows, total, loading, error, overview, runs, performance, performanceSource, hasRows, loadRunning, loadOverview, loadPerformance, startPolling, stopPolling, pause, resume, changeMode };
});
