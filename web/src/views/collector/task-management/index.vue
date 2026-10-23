<template>
  <div class="moox-page collector-task-management-page" :class="{ 'collector-task-management-page--fill': fillHeight }">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="采集任务" @change="onTabChange" />

      <section class="task-management-content" :class="{ 'task-management-content--fill': fillHeight }">
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
import CollectionTasks from "@/views/collector/collection-tasks/collection-tasks.vue";
import RuntimeStatus from "@/views/collector/runtime-status/runtime-status.vue";
import BackfillJobs from "@/views/collector/backfill-jobs/index.vue";
import FunctionInstance from "@/views/collector/cloud-function/function-instance.vue";
import TaskResults from "@/views/collector/task-results/index.vue";

type CollectorTaskTab = "tasks" | "status" | "backfills" | "executors" | "results";

// 顺序与因子“计算任务 → 计算结果 → 补算”一致：先看任务和产出，再看运维（运行状态、执行器），补采放最后。
const tabs = [
  { key: "tasks", label: "采集任务" },
  { key: "results", label: "采集结果" },
  { key: "status", label: "运行状态" },
  { key: "executors", label: "执行器" },
  { key: "backfills", label: "补采" }
] as const;

const route = useRoute();
const router = useRouter();
// 当前 Tab 只由 URL 决定：点击时只改 URL，不先切换本地状态，避免新 Tab 在跳转完成前
// 按旧 query 写回 URL 而取消这次跳转。
const activeTab = computed<CollectorTaskTab>(() => normalizeTab(route.query.tab));

// 运行状态页占满可视高度，由函数列表在表格内部滚动。
const fillHeight = computed(() => activeTab.value === "status");

const activeComponent = computed(
  () =>
    ({
      tasks: CollectionTasks,
      status: RuntimeStatus,
      backfills: BackfillJobs,
      executors: FunctionInstance,
      results: TaskResults
    })[activeTab.value]
);

function normalizeTab(value: unknown): CollectorTaskTab {
  return value === "status" || value === "backfills" || value === "executors" || value === "results" ? value : "tasks";
}

// 切换 Tab 时只保留目标 Tab 自己的参数：采集结果保留所选任务，其余 Tab 的筛选参数（如补采的数据集、作业）不带过去。
function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  const resultTask = Array.isArray(route.query.resultTask) ? route.query.resultTask[0] : route.query.resultTask;
  const query = tab === "tasks" ? {} : tab === "results" && resultTask ? { tab, resultTask } : { tab };
  void router.replace({ path: "/collector/tasks", query });
}
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

/* 运行状态页自己管理滚动：外层固定为可视高度，内容区不滚，由函数列表在内部滚动。 */
.collector-task-management-page--fill > .moox-inner {
  height: 100%;
}

.task-management-content--fill {
  overflow-y: hidden;
}

.task-management-content--fill :deep(.moox-page) {
  height: 100%;
}

.task-management-content :deep(.moox-inner) {
  min-height: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
</style>
