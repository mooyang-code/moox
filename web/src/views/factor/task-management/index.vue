<template>
  <TitleTabsWorkbench
    :tabs="tabs"
    label="计算任务"
    :route-target="{ path: '/factor/tasks' }"
    :build-query="buildFactorTabQuery"
  />
</template>

<script setup lang="ts">
import type { LocationQuery } from "vue-router";
import TitleTabsWorkbench, { type WorkbenchTab } from "@/components/page-title-tabs/workbench.vue";
import ComputeTasks from "@/views/factor/compute-tasks/index.vue";
import FactorResults from "@/views/factor/results/index.vue";
import FactorRecalc from "@/views/factor/recalc/index.vue";
import { buildTabQuery, type ComputeTaskTab } from "@/views/factor/shared/use-factor-scope";

defineOptions({ name: "FactorTaskManagement" });

const tabs: readonly WorkbenchTab<ComputeTaskTab>[] = [
  { key: "tasks", label: "计算任务", component: ComputeTasks },
  { key: "results", label: "计算结果", component: FactorResults },
  { key: "recalc", label: "补算", component: FactorRecalc }
];

function buildFactorTabQuery(tab: string, query: LocationQuery) {
  return buildTabQuery(query, tab as ComputeTaskTab);
}
</script>
