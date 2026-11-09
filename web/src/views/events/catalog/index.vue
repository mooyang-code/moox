<template>
  <div class="catalog-tab">
    <p class="tab-intro">系统里会发生、可以订阅的事件。点“订阅”新建一条订阅：事件发生时通知你的接口。</p>
    <a-alert v-if="loadFailed" type="error" class="catalog-alert">
      事件目录读取失败
      <template #action><a-button size="mini" @click="reload">重试</a-button></template>
    </a-alert>
    <a-table
      row-key="event_name"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="events"
      :pagination="false"
      :scroll="{ x: 'max-content' }"
    >
      <template #columns>
        <a-table-column title="事件" :width="360">
          <template #cell="{ record }">
            <a-link @click="infoEvent = record">{{ record.title }}</a-link>
            <div class="muted mono">{{ record.event_name }}</div>
          </template>
        </a-table-column>
        <a-table-column title="说明" :width="320">
          <template #cell="{ record }">{{ record.description || "-" }}</template>
        </a-table-column>
        <a-table-column title="可限定的范围" :width="200">
          <template #cell="{ record }">{{ scopeText(record) }}</template>
        </a-table-column>
        <a-table-column title="订阅" :width="90" align="right">
          <template #cell="{ record }">
            <span v-if="rulesState !== 'ok'" class="muted">-</span>
            <a-link v-else-if="ruleCount(record)" href="#/events?tab=subscriptions">{{ ruleCount(record) }} 条</a-link>
            <span v-else class="muted">无</span>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="200" align="center" fixed="right">
          <template #cell="{ record }">
            <a-space :size="0">
              <a-button size="mini" type="text" @click="infoEvent = record">详情</a-button>
              <a-button size="mini" type="text" @click="subscribe(record)">订阅</a-button>
              <a-button v-if="record.event_name === DELIST_EVENT" size="mini" type="text" @click="delistVisible = true"
                >手工登记</a-button
              >
            </a-space>
          </template>
        </a-table-column>
      </template>
      <template #empty>{{ loading ? "" : loadFailed ? "事件目录读取失败" : "没有可订阅的事件" }}</template>
    </a-table>

    <a-drawer
      :visible="Boolean(infoEvent)"
      :title="infoEvent?.title"
      width="min(640px, 100vw)"
      :footer="false"
      @cancel="infoEvent = null"
    >
      <template v-if="infoEvent">
        <section v-if="infoEvent.event_name === DELIST_EVENT" class="delist-flow">
          <template v-for="item in DELIST_FLOW" :key="item.title">
            <h4>{{ item.title }}</h4>
            <p>{{ item.text }}</p>
          </template>
          <p class="muted">{{ systemRuleLine }}</p>
        </section>
        <EventInfo :event="infoEvent" />
      </template>
    </a-drawer>

    <DelistDialog
      v-model:visible="delistVisible"
      :system-action-enabled="systemActionEnabled"
      :system-rule-enabled="systemRuleEnabled"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { onShown } from "@/hooks/useOnShown";
import { useRouter } from "vue-router";
import type { SubscribableEvent } from "@/api/callback-dispatcher";
import DelistDialog from "../shared/delist-dialog.vue";
import EventInfo from "../shared/event-info.vue";
import { DELIST_EVENT, DELIST_FLOW } from "../shared/event-center-utils";
import { useEventCatalog } from "../shared/use-event-catalog";

defineOptions({ name: "EventCatalog" });

const router = useRouter();
const { events, rules, systemActionEnabled, systemRule, systemRuleState, systemRuleEnabled, loadEvents, loadRules } =
  useEventCatalog();
const rulesState = systemRuleState;
const loading = ref(false);
const loadFailed = ref(false);
const infoEvent = ref<SubscribableEvent | null>(null);
const delistVisible = ref(false);

// 系统订阅读取失败时写“读取失败”，不能当成已启用
const systemRuleLine = computed(() => {
  if (systemRuleState.value === "loading") return "系统订阅：读取中";
  if (systemRuleState.value === "failed") return "系统订阅：读取失败";
  if (!systemRule.value) return "没有找到系统订阅：发现的下架不会被处理";
  return `系统订阅「${systemRule.value.name}」：${systemRule.value.enabled ? "已启用" : "已停用"}`;
});

function scopeText(event: SubscribableEvent) {
  return event.scope_fields.map(field => field.title).join("、") || "不限";
}

function ruleCount(event: SubscribableEvent) {
  return rules.value.filter(rule => rule.event_name === event.event_name).length;
}

/** 到订阅页签新建一条订阅，触发事件预选为这一个。 */
function subscribe(event: SubscribableEvent) {
  void router.replace({ path: "/events", query: { tab: "subscriptions", new: event.event_name } });
}

async function reload() {
  loading.value = true;
  loadFailed.value = false;
  const [eventsOk] = await Promise.all([loadEvents(), loadRules()]);
  loadFailed.value = !eventsOk;
  loading.value = false;
}

// 页签在 keep-alive 里：每次切回都重读（订阅页签里停用了系统订阅、新建了订阅，这里的状态和条数要跟上）
onShown(() => void reload());
</script>

<style scoped>
.tab-intro {
  margin: 0 0 var(--moox-space-toolbar-table);
  color: var(--color-text-3);
  font-size: 13px;
}

.catalog-alert {
  margin-bottom: var(--moox-space-3);
}

.muted {
  color: var(--color-text-3);
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
}

.delist-flow {
  margin-bottom: var(--moox-space-4);
}

.delist-flow h4 {
  margin: 0 0 4px;
  font-size: 14px;
}

.delist-flow p {
  margin: 0 0 var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
  line-height: 1.7;
}
</style>
