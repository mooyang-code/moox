import { reactive, ref } from "vue";
import { listInstances } from "@/api/strategy";
import type { StrategyInstance } from "@/api/strategy-types";
import { errorMessage } from "@/api/http";

/** 策略运行页的实例列表：分页读取、后台刷新与切空间清空。只在用到它的页面里存在，换页面即释放。 */
export function useInstanceList() {
  const instances = ref<StrategyInstance[]>([]);
  const totalInstances = ref(0);
  const instancesLoading = ref(false);
  const instancesError = ref("");
  let instancesRequest = 0;

  /** background：后台刷新，不转加载态、不先清错误。 */
  async function loadInstances(params: Parameters<typeof listInstances>[0] = {}, options: { background?: boolean } = {}) {
    const requestId = ++instancesRequest;
    if (!options.background) {
      instancesLoading.value = true;
      instancesError.value = "";
    }
    try {
      const result = await listInstances(params);
      if (requestId !== instancesRequest) return;
      instances.value = result.items;
      totalInstances.value = result.page.total;
      instancesError.value = "";
    } catch (err) {
      if (requestId === instancesRequest) instancesError.value = errorMessage(err, "策略实例加载失败");
      throw err;
    } finally {
      if (requestId === instancesRequest) instancesLoading.value = false;
    }
  }

  /** 切空间时清空实例列表并作废在途请求：新空间的请求失败时不能继续显示旧空间的实例。 */
  function clearInstances() {
    instancesRequest += 1;
    instances.value = [];
    totalInstances.value = 0;
    instancesLoading.value = false;
    instancesError.value = "";
  }

  return reactive({ instances, totalInstances, instancesLoading, instancesError, loadInstances, clearInstances });
}
