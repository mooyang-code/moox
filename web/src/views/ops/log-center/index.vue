<template>
  <div class="moox-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="日志中心" @change="onTabChange" />
      <section class="logs-center-content">
        <keep-alive>
          <component :is="activeTab === 'activity' ? ActivityTab : ImportantTab" />
        </keep-alive>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import ActivityTab from "./activity-tab.vue";
import ImportantTab from "./important-tab.vue";

defineOptions({ name: "OpsLogs" });

// 先看平台上发生了什么（活动），再看各服务的错误和关键事件（重要日志）。
const tabs = [
  { key: "activity", label: "活动日志" },
  { key: "important", label: "重要日志" }
] as const;

const route = useRoute();
const router = useRouter();
// 只认 important，其余一律落到默认的活动日志
const activeTab = computed<"activity" | "important">(() => (route.query.tab === "important" ? "important" : "activity"));

function onTabChange(value: string) {
  void router.replace({ path: "/ops/logs", query: value === "important" ? { tab: "important" } : {} });
}
</script>

<style scoped>
.logs-center-content {
  min-width: 0;
  margin-top: var(--moox-space-3);
}
</style>
