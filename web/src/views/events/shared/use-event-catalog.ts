import { computed, ref } from "vue";
import { listAllEventRules, listSubscribableEvents, type EventRule, type SubscribableEvent } from "@/api/callback-dispatcher";
import { reportControlError } from "@/api/admin/http";

/**
 * 事件目录、全部订阅与系统订阅的状态：事件页签和订阅页签都要用。
 * systemActionEnabled、systemRuleEnabled 为 null 表示还在读取或读取失败（不知道），不能当成已启用。
 */
export function useEventCatalog() {
  const events = ref<SubscribableEvent[]>([]);
  const systemActionEnabled = ref<boolean | null>(null);
  // 系统订阅从全部订阅里找，不只看当前页。
  const systemRule = ref<EventRule | null>(null);
  // 全部订阅（不分页），事件页签按事件数订阅条数
  const rules = ref<EventRule[]>([]);
  const systemRuleState = ref<"loading" | "ok" | "failed">("loading");
  const systemRuleEnabled = computed(() => systemRule.value?.enabled ?? null);

  // 进行中的目录请求：同时有几处要读（例如订阅页签首次挂载又带着预选事件）时只发一次
  let eventsRequest: Promise<boolean> | null = null;
  /** 读事件目录，返回是否读到（失败时已弹出提示）。 */
  function loadEvents(): Promise<boolean> {
    eventsRequest ??= (async () => {
      try {
        const catalog = await listSubscribableEvents();
        events.value = catalog.events;
        systemActionEnabled.value = catalog.availableSystemActions.includes("SYSTEM_ACTION_SELL_DELISTED_HOLDINGS") ? true : null;
        return true;
      } catch (error) {
        reportControlError(error);
        return false;
      } finally {
        eventsRequest = null;
      }
    })();
    return eventsRequest;
  }

  // 连续读取只认最后一次。
  let systemRuleSeq = 0;
  async function loadRules() {
    const seq = ++systemRuleSeq;
    try {
      const all = await listAllEventRules();
      if (seq !== systemRuleSeq) return;
      rules.value = all;
      systemRule.value = all.find(rule => rule.system) ?? null;
      systemRuleState.value = "ok";
    } catch {
      // 读不到就当作不知道；要不要提示由调用方决定。
      if (seq !== systemRuleSeq) return;
      rules.value = [];
      systemRule.value = null;
      systemRuleState.value = "failed";
    }
  }

  return { events, rules, systemActionEnabled, systemRule, systemRuleState, systemRuleEnabled, loadEvents, loadRules };
}
