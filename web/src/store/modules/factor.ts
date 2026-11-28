import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { getFactorStatus, listAllFactorTasks } from "@/api/factor";
import type { EngineStatus, FactorTask, FactorTaskInfo } from "@/api/factor/types";
import { readAllPages } from "@/api/read-all-pages";
import { listDatasets } from "@/api/storage/metadata";
import { createLatestRequestGuard, type LatestRequest } from "@/utils/latest-request";

const PAGE_SIZE = 500;

async function fetchAllTasks(spaceId: string) {
  const items = await listAllFactorTasks();
  return items.filter(item => item.factor_task?.space_id === spaceId);
}

async function fetchDatasetNames(spaceId: string) {
  const datasets = await readAllPages(async page => {
    // 名称只用于展示，失败时回落到 dataset_id：一律静默（读取失败时每次刷新都会重试）
    const rsp = await listDatasets({ space_id: spaceId, page: { page, size: PAGE_SIZE } });
    return { items: rsp.datasets || [], hasMore: Boolean(rsp.page_result?.has_more) };
  });
  const names: Record<string, string> = {};
  for (const dataset of datasets) names[dataset.dataset_id] = dataset.name;
  return names;
}

export const useFactorStore = defineStore("factorStore", () => {
  const tasks = ref<FactorTaskInfo[]>([]);
  const loading = ref(false);
  const loadError = ref("");
  const currentTaskId = ref("");
  const spaceId = ref("");
  const engine = ref<EngineStatus | null>(null);
  const loadedSpaceId = ref("");
  const datasetNames = ref<Record<string, string>>({});
  const datasetNamesSpaceId = ref("");
  const gate = createLatestRequestGuard();

  const current = computed(() => tasks.value.find(item => item.factor_task.task_id === currentTaskId.value));

  /** 计算任务的展示名：任务名 · 频率；没有任务名时回落为数据集名称 · 频率，再回落为 source_dataset_id。 */
  function taskLabel(set: Pick<FactorTask, "source_dataset_id" | "frequency"> & Partial<Pick<FactorTask, "name">>) {
    const base = set.name || datasetNames.value[set.source_dataset_id] || set.source_dataset_id;
    return `${base} · ${set.frequency}`;
  }

  /** 该空间的计算任务是否已经成功加载过（页面重新挂载时据此跳过重置）。 */
  function isLoadedFor(nextSpaceId: string) {
    return Boolean(nextSpaceId) && loadedSpaceId.value === nextSpaceId;
  }

  /** 每个空间只加载一次数据集名称；失败不影响计算任务加载，标签回落到 dataset_id。 */
  async function loadDatasetNames(nextSpaceId: string, token: LatestRequest) {
    if (datasetNamesSpaceId.value === nextSpaceId) return;
    try {
      const names = await fetchDatasetNames(nextSpaceId);
      if (!token.isLatest()) return;
      datasetNames.value = names;
      datasetNamesSpaceId.value = nextSpaceId;
    } catch {
      // 名称只用于展示。
    }
  }

  /**
   * 加载某个空间的全部计算任务；过期响应被丢弃，当前选择失效时回落到第一项。
   * silent：后台刷新，不转加载态，失败记在 loadError 由页面显示。
   */
  async function load(nextSpaceId: string, options: { silent?: boolean } = {}) {
    const token = gate.begin();
    spaceId.value = nextSpaceId;
    if (!nextSpaceId) {
      tasks.value = [];
      currentTaskId.value = "";
      loadedSpaceId.value = "";
      loading.value = false;
      loadError.value = "";
      return;
    }
    if (!options.silent) loading.value = true;
    try {
      const [items] = await Promise.all([fetchAllTasks(nextSpaceId), loadDatasetNames(nextSpaceId, token)]);
      if (!token.isLatest()) return;
      tasks.value = items;
      loadedSpaceId.value = nextSpaceId;
      loadError.value = "";
      if (!items.some(item => item.factor_task.task_id === currentTaskId.value)) {
        currentTaskId.value = items[0]?.factor_task.task_id || "";
      }
    } catch (error) {
      if (!token.isLatest()) return;
      loadError.value = error instanceof Error ? error.message : "计算任务加载失败";
    } finally {
      if (token.isLatest()) loading.value = false;
    }
  }

  function reload(options: { silent?: boolean } = { silent: true }) {
    return load(spaceId.value, options);
  }

  /** 尊重路由给出的 taskId；不存在时保持 load 的回落结果。 */
  function select(taskId: string) {
    if (taskId && tasks.value.some(item => item.factor_task.task_id === taskId)) currentTaskId.value = taskId;
  }

  /** 引擎状态只用于展示，读不到时为空、不弹提示。 */
  async function refreshEngine() {
    try {
      engine.value = await getFactorStatus();
    } catch {
      engine.value = null;
    }
  }

  function reset() {
    gate.invalidate();
    spaceId.value = "";
    tasks.value = [];
    currentTaskId.value = "";
    loadedSpaceId.value = "";
    datasetNames.value = {};
    datasetNamesSpaceId.value = "";
    engine.value = null;
    loading.value = false;
    loadError.value = "";
  }

  return {
    tasks,
    loading,
    loadError,
    currentTaskId,
    current,
    engine,
    spaceId,
    datasetNames,
    taskLabel,
    isLoadedFor,
    load,
    reload,
    select,
    refreshEngine,
    reset
  };
});
