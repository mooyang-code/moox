<template>
  <div class="moox-page factor-task-management-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="计算任务" @change="onTabChange" />

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
import ComputeTasks from "@/views/factor/compute-tasks/index.vue";
import FactorResults from "@/views/factor/results/index.vue";
import FactorRecalc from "@/views/factor/recalc/index.vue";
import { buildTabQuery, type ComputeTaskTab } from "@/views/factor/shared/use-factor-scope";

defineOptions({ name: "FactorTaskManagement" });

const tabs = [
  { key: "tasks", label: "计算任务" },
  { key: "results", label: "计算结果" },
  { key: "recalc", label: "补算" }
] as const;

const route = useRoute();
const router = useRouter();
const activeTab = ref<ComputeTaskTab>(normalizeTab(route.query.tab));

const activeComponent = computed(
  () =>
    ({
      tasks: ComputeTasks,
      results: FactorResults,
      recalc: FactorRecalc
    })[activeTab.value]
);

function normalizeTab(value: unknown): ComputeTaskTab {
  return value === "results" || value === "recalc" ? value : "tasks";
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  void router.replace({ path: "/factor/tasks", query: buildTabQuery(route.query, tab) });
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
.factor-task-management-page {
  height: 100%;
  min-height: 0;
}

.factor-task-management-page > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.task-management-content {
  min-width: 0;
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
  overflow-x: hidden;
  overflow-y: auto;
}

.task-management-content :deep(.moox-page) {
  height: auto;
  min-height: 100%;
  max-width: 100%;
  padding: 0;
  overflow-x: hidden;
  overflow-y: visible;
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
