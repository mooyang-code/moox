import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { getFactorStatus, listFactorSets } from "@/api/factor";
import type { EngineStatus, FactorSet, FactorSetInfo } from "@/api/factor/types";
import { listDatasets } from "@/api/storage/metadata";
import { RequestGate } from "@/utils/request-gate";

const PAGE_SIZE = 500;

async function fetchAllSets(spaceId: string) {
  const items: FactorSetInfo[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listFactorSets({ page: { page, size: PAGE_SIZE } });
    const batch = rsp.factor_sets || [];
    items.push(...batch);
    if (!rsp.page_result?.has_more || !batch.length) break;
  }
  return items.filter(item => item.factor_set?.space_id === spaceId);
}

async function fetchDatasetNames(spaceId: string) {
  const names: Record<string, string> = {};
  for (let page = 1; ; page += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page, size: PAGE_SIZE } });
    const batch = rsp.datasets || [];
    for (const dataset of batch) names[dataset.dataset_id] = dataset.name;
    if (!rsp.page_result?.has_more || !batch.length) break;
  }
  return names;
}

export const useFactorStore = defineStore("factorStore", () => {
  const sets = ref<FactorSetInfo[]>([]);
  const loading = ref(false);
  const loadError = ref("");
  const currentSetId = ref("");
  const spaceId = ref("");
  const engine = ref<EngineStatus | null>(null);
  const loadedSpaceId = ref("");
  const datasetNames = ref<Record<string, string>>({});
  const datasetNamesSpaceId = ref("");
  const gate = new RequestGate();

  const current = computed(() => sets.value.find(item => item.factor_set.set_id === currentSetId.value));

  /** 计算任务的展示名：数据集名称 · 频率；取不到名称时回落为 source_dataset_id。 */
  function setLabel(set: Pick<FactorSet, "source_dataset_id" | "freq">) {
    return `${datasetNames.value[set.source_dataset_id] || set.source_dataset_id} · ${set.freq}`;
  }

  /** 该空间的因子集是否已经成功加载过（页面重新挂载时据此跳过重置）。 */
  function isLoadedFor(nextSpaceId: string) {
    return Boolean(nextSpaceId) && loadedSpaceId.value === nextSpaceId;
  }

  /** 每个空间只加载一次数据集名称；失败不影响因子集加载，标签回落到 dataset_id。 */
  async function loadDatasetNames(nextSpaceId: string, token: number) {
    if (datasetNamesSpaceId.value === nextSpaceId) return;
    try {
      const names = await fetchDatasetNames(nextSpaceId);
      if (!gate.isCurrent(token)) return;
      datasetNames.value = names;
      datasetNamesSpaceId.value = nextSpaceId;
    } catch {
      // 名称只用于展示。
    }
  }

  /** 加载某个空间的全部因子集；过期响应被丢弃，当前选择失效时回落到第一项。 */
  async function load(nextSpaceId: string, options: { silent?: boolean } = {}) {
    const token = gate.next();
    spaceId.value = nextSpaceId;
    if (!nextSpaceId) {
      sets.value = [];
      currentSetId.value = "";
      loadedSpaceId.value = "";
      loading.value = false;
      loadError.value = "";
      return;
    }
    if (!options.silent) loading.value = true;
    try {
      const [items] = await Promise.all([fetchAllSets(nextSpaceId), loadDatasetNames(nextSpaceId, token)]);
      if (!gate.isCurrent(token)) return;
      sets.value = items;
      loadedSpaceId.value = nextSpaceId;
      loadError.value = "";
      if (!items.some(item => item.factor_set.set_id === currentSetId.value)) {
        currentSetId.value = items[0]?.factor_set.set_id || "";
      }
    } catch (error) {
      if (!gate.isCurrent(token)) return;
      loadError.value = error instanceof Error ? error.message : "计算任务加载失败";
    } finally {
      if (gate.isCurrent(token)) loading.value = false;
    }
  }

  function reload(options: { silent?: boolean } = { silent: true }) {
    return load(spaceId.value, options);
  }

  /** 尊重路由给出的 setId；不存在时保持 load 的回落结果。 */
  function select(setId: string) {
    if (setId && sets.value.some(item => item.factor_set.set_id === setId)) currentSetId.value = setId;
  }

  async function refreshEngine() {
    try {
      engine.value = await getFactorStatus();
    } catch {
      engine.value = null;
    }
  }

  function reset() {
    gate.next();
    spaceId.value = "";
    sets.value = [];
    currentSetId.value = "";
    loadedSpaceId.value = "";
    datasetNames.value = {};
    datasetNamesSpaceId.value = "";
    engine.value = null;
    loading.value = false;
    loadError.value = "";
  }

  return {
    sets,
    loading,
    loadError,
    currentSetId,
    current,
    engine,
    spaceId,
    datasetNames,
    setLabel,
    isLoadedFor,
    load,
    reload,
    select,
    refreshEngine,
    reset
  };
});
