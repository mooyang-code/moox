<template>
  <div class="moox-page event-center-page">
    <div class="moox-inner">
      <div class="event-center-head">
        <PageTitleTabs v-model="activeTab" :items="tabs" aria-label="事件中心" />
        <a-space v-if="activeTab === 'rules'">
          <a-button @click="rulesRef?.openDelist()">手工登记下架</a-button>
          <a-button type="primary" status="success" @click="rulesRef?.openCreate()">
            <template #icon><icon-plus /></template>
            新建规则
          </a-button>
        </a-space>
        <a-button v-else @click="executionsRef?.load()">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>

      <p class="event-center-intro">某类事件发生时，向你自己的接口发一条通知，或者由系统自动处理。每次处理都会留下执行记录。</p>

      <keep-alive>
        <EventRules v-if="activeTab === 'rules'" ref="rulesRef" />
        <EventExecutions v-else ref="executionsRef" />
      </keep-alive>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import EventRules from "./rules.vue";
import EventExecutions from "./executions.vue";

defineOptions({ name: "SettingsEvents" });

const tabs = [
  { key: "rules", label: "事件规则" },
  { key: "executions", label: "执行记录" }
] as const;

const activeTab = ref<(typeof tabs)[number]["key"]>("rules");
const rulesRef = ref<InstanceType<typeof EventRules> | null>(null);
const executionsRef = ref<InstanceType<typeof EventExecutions> | null>(null);
</script>

<style scoped>
.event-center-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-3);
}

.event-center-intro {
  margin: 0 0 var(--moox-space-toolbar-table);
  color: var(--color-text-3);
  font-size: 13px;
}
</style>
