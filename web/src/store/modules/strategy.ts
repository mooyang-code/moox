import { ref } from "vue";
import { defineStore } from "pinia";
import { errorMessage } from "@/api/http";
import { deleteStrategy, listStrategies } from "@/api/strategy";
import type { Strategy } from "@/api/strategy-types";

/** 策略定义：定义页的分页列表与运行页、回放页共用的完整目录。实例列表与实例详情见 views/strategy 下的组合函数。 */
export const useStrategyStore = defineStore("strategy", () => {
  // strategies 是定义页当前页的分页列表；strategyCatalog 是完整目录（运行页、回放页的下拉与名称查找）。两者各有请求序号，
  // 迟到的分页响应不能覆盖完整目录。
  const strategies = ref<Strategy[]>([]);
  const strategyCatalog = ref<Strategy[]>([]);
  const totalStrategies = ref(0);
  const strategiesComplete = ref(false);
  // 加载与错误按资源分开：定义分页列表（listLoading/error）与完整目录互不覆盖。
  const listLoading = ref(false);
  const catalogLoading = ref(false);
  const catalogError = ref("");
  const error = ref("");
  let strategiesRequest = 0;
  let catalogRequest = 0;

  async function loadStrategies(params: Parameters<typeof listStrategies>[0] = {}) {
    const requestId = ++strategiesRequest;
    listLoading.value = true;
    error.value = "";
    try {
      const result = await listStrategies(params);
      if (requestId !== strategiesRequest) return;
      strategies.value = result.items;
      totalStrategies.value = result.page.total;
    } catch (err) {
      if (requestId === strategiesRequest) error.value = errorMessage(err, "策略定义加载失败");
      throw err;
    } finally {
      if (requestId === strategiesRequest) listLoading.value = false;
    }
  }

  async function loadAllStrategies(pageSize = 200) {
    const requestId = ++catalogRequest;
    catalogLoading.value = true;
    catalogError.value = "";
    try {
      const items: Strategy[] = [];
      let total = 0;
      for (let page = 1; ; page += 1) {
        const response = await listStrategies({ page, size: pageSize });
        if (requestId !== catalogRequest) return;
        items.push(...response.items);
        total = response.page.total;
        if (!response.items.length || (!response.page.has_more && items.length >= total)) break;
      }
      strategyCatalog.value = items;
      strategiesComplete.value = true;
    } catch (err) {
      if (requestId === catalogRequest) catalogError.value = errorMessage(err, "策略定义加载失败");
      throw err;
    } finally {
      if (requestId === catalogRequest) catalogLoading.value = false;
    }
  }

  /** 定义有新增或修改后作废完整目录：在途的目录请求不再写入，下次进入运行页、回放页时重新读取。 */
  function invalidateStrategyCatalog() {
    catalogRequest += 1;
    strategiesComplete.value = false;
    catalogLoading.value = false;
  }

  async function removeStrategy(strategyId: string) {
    await deleteStrategy(strategyId);
    strategies.value = strategies.value.filter(item => item.strategy_id !== strategyId);
    strategyCatalog.value = strategyCatalog.value.filter(item => item.strategy_id !== strategyId);
    totalStrategies.value = Math.max(0, totalStrategies.value - 1);
  }

  return {
    strategies,
    strategyCatalog,
    totalStrategies,
    strategiesComplete,
    listLoading,
    catalogLoading,
    catalogError,
    error,
    loadStrategies,
    loadAllStrategies,
    invalidateStrategyCatalog,
    removeStrategy
  };
});
