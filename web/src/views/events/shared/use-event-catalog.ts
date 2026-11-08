import { computed, ref } from "vue";
import { listAllEventRules, listSubscribableEvents, type EventRule, type SubscribableEvent } from "@/api/callback-dispatcher";
import { reportControlError } from "@/api/admin/http";

/**
 * 事件目录与系统订阅的状态：事件订阅页和下架处理页都要用。
 * systemActionEnabled、systemRuleEnabled 为 null 表示还在读取或读取失败（不知道），不能当成已启用。
 */
export function useEventCatalog() {
  const events = ref<SubscribableEvent[]>([]);
  const systemActionEnabled = ref<boolean | null>(null);
  // 系统订阅从全部订阅里找，不只看当前页。
  const systemRule = ref<EventRule | null>(null);
  const systemRuleState = ref<"loading" | "ok" | "failed">("loading");
  const systemRuleEnabled = computed(() => systemRule.value?.enabled ?? null);

  async function loadEvents() {
    try {
      const catalog = await listSubscribableEvents();
      events.value = catalog.events;
      systemActionEnabled.value = catalog.availableSystemActions.includes("SYSTEM_ACTION_SELL_DELISTED_HOLDINGS") ? true : null;
    } catch (error) {
      reportControlError(error);
    }
  }

  // 连续读取只认最后一次。
  let systemRuleSeq = 0;
  async function loadSystemRule() {
    const seq = ++systemRuleSeq;
    try {
      const rules = await listAllEventRules();
      if (seq !== systemRuleSeq) return;
      systemRule.value = rules.find(rule => rule.system) ?? null;
      systemRuleState.value = "ok";
    } catch {
      // 读不到就当作不知道；要不要提示由调用方决定。
      if (seq !== systemRuleSeq) return;
      systemRule.value = null;
      systemRuleState.value = "failed";
    }
  }

  return { events, systemActionEnabled, systemRule, systemRuleState, systemRuleEnabled, loadEvents, loadSystemRule };
}
