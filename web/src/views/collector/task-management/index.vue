<template>
  <div class="moox-page collector-task-management-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="采集任务" @change="onTabChange" />

      <section class="task-management-content">
        <keep-alive>
          <component :is="activeComponent" />
        </keep-alive>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import CollectionRules from "@/views/collector/collector-rules/collector-rules.vue";
import TaskInstances from "@/views/collector/task-instances/task-instances.vue";
import CloudNode from "@/views/collector/cloud-node/cloud-node.vue";
import TaskResults from "@/views/collector/task-results/index.vue";

type CollectorTaskTab = "tasks" | "instances" | "executors" | "results";

const tabs = [
  { key: "tasks", label: "采集任务" },
  { key: "instances", label: "任务实例" },
  { key: "executors", label: "执行器" },
  { key: "results", label: "采集结果" }
] as const;

const route = useRoute();
const router = useRouter();
const activeTab = ref<CollectorTaskTab>(normalizeTab(route.query.tab));

const activeComponent = computed(
  () =>
    ({
      tasks: CollectionRules,
      instances: TaskInstances,
      executors: CloudNode,
      results: TaskResults
    })[activeTab.value]
);

function normalizeTab(value: unknown): CollectorTaskTab {
  return value === "instances" || value === "executors" || value === "results" ? value : "tasks";
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  void router.replace({
    path: "/collector/tasks",
    query: { ...route.query, tab: tab === "tasks" ? undefined : tab }
  });
}

watch(
  () => route.query.tab,
  value => {
    const tab = normalizeTab(value);
    if (tab !== activeTab.value) activeTab.value = tab;
  }
);
</script>

<style scoped lang="scss">
.collector-task-management-page {
  height: 100%;
  min-height: 0;
}

.collector-task-management-page > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.task-management-content {
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
  overflow: auto;
}

.task-management-content :deep(.moox-page) {
  height: auto;
  min-height: 100%;
  padding: 0;
  overflow: visible;
  background: transparent;
}

.task-management-content :deep(.moox-inner) {
  min-height: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
</style>
