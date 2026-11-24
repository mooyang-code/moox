import { onBeforeUnmount, ref, watch, type Ref } from "vue";
import { errorMessage } from "@/api/http";
import { getStrategyWithRequirement, validateStrategy } from "@/api/strategy";
import type { StrategyTaskRequirement } from "@/api/strategy-types";
import { createLatestRequestGuard } from "@/utils/latest-request";

/** 要读要求的策略：已保存的定义按 ID 读（GetStrategy），编辑中的 DSL 文本按 ValidateStrategy 推出。 */
export interface RequirementSource {
  strategyId?: string;
  dsl?: string;
}

/**
 * 建实例、试算、回放要绑定的因子任务，由后端按 DSL 推出（GetStrategy / ValidateStrategy 的 requirement）。
 * 来源变化时重新读取：按 ID 立即读，按 DSL 文本停下 400 毫秒后再读，避免每次按键都发请求；迟到的响应丢弃。
 */
export function useTaskRequirement(source: Ref<RequirementSource>) {
  const requirement = ref<StrategyTaskRequirement | null>(null);
  const loading = ref(false);
  const error = ref("");
  const guard = createLatestRequestGuard();
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function load(next: RequirementSource) {
    const request = guard.begin();
    loading.value = true;
    try {
      const loaded = next.strategyId
        ? (await getStrategyWithRequirement(next.strategyId)).requirement
        : (await validateStrategy(next.dsl || "")).requirement;
      if (!request.isLatest()) return;
      requirement.value = loaded;
      error.value = "";
    } catch (cause) {
      if (request.isLatest()) error.value = errorMessage(cause, "读取策略要绑定的任务失败");
    } finally {
      if (request.isLatest()) loading.value = false;
    }
  }

  function reload() {
    clearTimeout(timer);
    const next = source.value;
    if (!next.strategyId && !next.dsl?.trim()) {
      guard.invalidate();
      requirement.value = null;
      loading.value = false;
      error.value = "";
      return;
    }
    if (next.strategyId) void load(next);
    else timer = setTimeout(() => void load(next), 400);
  }

  watch(() => [source.value.strategyId, source.value.dsl], reload, { immediate: true });
  onBeforeUnmount(() => {
    clearTimeout(timer);
    guard.invalidate();
  });

  return { requirement, loading, error, reload };
}
