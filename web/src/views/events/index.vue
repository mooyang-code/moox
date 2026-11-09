<template>
  <div class="moox-page event-center-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="事件中心" @change="onTabChange" />
      <section class="event-center-content">
        <keep-alive>
          <component :is="activeComponent" />
        </keep-alive>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import EventCatalog from "./catalog/index.vue";
import EventExecutions from "./executions/index.vue";
import EventSubscriptions from "./subscriptions/index.vue";
import { type EventCenterTab, eventCenterTab } from "./shared/event-center-utils";

defineOptions({ name: "EventCenter" });

// 先看系统里有哪些事件，再看订阅，最后是订阅每次被触发后的执行记录。
const tabs = [
  { key: "events", label: "事件" },
  { key: "subscriptions", label: "订阅" },
  { key: "executions", label: "执行记录" }
] as const;

const route = useRoute();
const router = useRouter();
const activeTab = computed<EventCenterTab>(() => eventCenterTab(route.query.tab));
const activeComponent = computed(
  () => ({ events: EventCatalog, subscriptions: EventSubscriptions, executions: EventExecutions })[activeTab.value]
);

/** 切页签只保留 tab：执行记录的筛选、订阅页的预选事件都属于各自的页签。 */
function onTabChange(value: string) {
  const tab = eventCenterTab(value);
  void router.replace({ path: "/events", query: tab === "events" ? {} : { tab } });
}
</script>

<style scoped>
.event-center-page > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.event-center-content {
  min-width: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
}
</style>
