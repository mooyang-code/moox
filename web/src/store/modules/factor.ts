import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { getFactorStatus, listFactorSets } from "@/api/factor";
import type { EngineStatus, FactorSetInfo } from "@/api/factor/types";
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

export const useFactorStore = defineStore("factorStore", () => {
  const sets = ref<FactorSetInfo[]>([]);
  const loading = ref(false);
  const loadError = ref("");
  const currentSetId = ref("");
  const spaceId = ref("");
  const engine = ref<EngineStatus | null>(null);
  const gate = new RequestGate();

  const current = computed(() => sets.value.find(item => item.factor_set.set_id === currentSetId.value));

  /** 加载某个空间的全部因子集；过期响应被丢弃，当前选择失效时回落到第一项。 */
  async function load(nextSpaceId: string, options: { silent?: boolean } = {}) {
    const token = gate.next();
    spaceId.value = nextSpaceId;
    if (!nextSpaceId) {
      sets.value = [];
      currentSetId.value = "";
      loading.value = false;
      loadError.value = "";
      return;
    }
    if (!options.silent) loading.value = true;
    try {
      const items = await fetchAllSets(nextSpaceId);
      if (!gate.isCurrent(token)) return;
      sets.value = items;
      loadError.value = "";
      if (!items.some(item => item.factor_set.set_id === currentSetId.value)) {
        currentSetId.value = items[0]?.factor_set.set_id || "";
      }
    } catch (error) {
      if (!gate.isCurrent(token)) return;
      loadError.value = error instanceof Error ? error.message : "因子集加载失败";
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
    engine.value = null;
    loading.value = false;
    loadError.value = "";
  }

  return { sets, loading, loadError, currentSetId, current, engine, load, reload, select, refreshEngine, reset };
});
