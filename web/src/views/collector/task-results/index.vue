<template>
  <div class="moox-page task-results-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先选择空间</a-alert>
      <a-spin v-else :loading="loading">
        <a-alert v-if="loadError" type="error" show-icon>{{ loadError }}</a-alert>

        <a-empty v-else-if="!resultTabs.length" description="暂无采集任务">
          <template #extra>
            <a-button type="primary" status="success" @click="goToTasks">新建采集任务</a-button>
          </template>
        </a-empty>

        <template v-else>
          <a-tabs v-model:active-key="activeTaskId" type="rounded" @change="onTaskChange">
            <a-tab-pane v-for="tab in resultTabs" :key="tab.taskId" :title="tab.title" />
          </a-tabs>

          <section v-if="activeTask" class="result-context">
            <div class="result-context__heading">
              <h3>{{ activeTask.task_name || activeTask.task_id }}</h3>
              <div class="result-context__tags">
                <a-tag :color="activeTask.enabled === false || activeTask.enabled === 'false' ? 'orange' : 'green'">
                  {{ taskStateLabel(activeTask) }}
                </a-tag>
                <a-tag :color="resultStateColor(activeState)">{{ resultStateLabel(activeState) }}</a-tag>
              </div>
            </div>
            <dl class="result-context__summary">
              <div>
                <dt>{{ activeTask.result?.data_kind === "record" ? "最近更新" : "最近数据时间" }}</dt>
                <dd>{{ lastDataTime(activeTask) }}</dd>
              </div>
              <div>
                <dt>覆盖范围</dt>
                <dd>{{ coverageText(activeTask) }}</dd>
              </div>
            </dl>
          </section>

          <a-alert v-if="activeState === 'error'" type="error" show-icon>
            {{ activeTask?.last_error || "结果视图不可用，请检查任务配置或稍后重试。" }}
          </a-alert>
          <a-empty v-else-if="activeState !== 'ready' || activeViewIds.length === 0" description="结果准备中，请稍后刷新">
            <template #extra>
              <a-button type="outline" @click="goToTaskDetail">查看任务详情</a-button>
            </template>
          </a-empty>
          <ViewBrowse
            v-else
            embedded
            :view-ids="activeViewIds"
            :active-view-id="activeViewIds[0]"
            :hide-technical-identity="true"
            :view-owner-modules="['collector']"
            :view-roles="['collection_browse']"
            empty-description="结果准备中"
            empty-rows-description="任务已准备，尚未产生数据"
            :auto-refresh-interval-ms="30000"
          />
        </template>
      </a-spin>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { GetTaskList, type CollectorTask } from "@/api/collector";
import { useSpaceStore } from "@/store/modules/space";
import ViewBrowse from "@/views/data/view-browse/index.vue";
import {
  buildResultsQuery,
  buildTaskResultTabs,
  getTaskResultState,
  lastDataTime,
  resultCoverage,
  resultStateLabel,
  selectTaskIdFromQuery,
  taskStateLabel,
  type TaskResultState
} from "./task-results-model";

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const loading = ref(false);
const loadError = ref("");
const tasks = ref<CollectorTask[]>([]);
const activeTaskId = ref(queryTaskId(route.query.resultTask));
let loadSequence = 0;

const resultTabs = computed(() => buildTaskResultTabs(tasks.value));
const activeTask = computed(
  () => resultTabs.value.find(tab => tab.taskId === activeTaskId.value)?.task || resultTabs.value[0]?.task
);
const activeViewIds = computed(() => (activeTask.value ? buildTaskResultTabs([activeTask.value])[0]?.viewIds || [] : []));
const activeState = computed<TaskResultState>(() => (activeTask.value ? getTaskResultState(activeTask.value) : "preparing"));

function queryTaskId(value: unknown) {
  return Array.isArray(value) ? String(value[0] || "") : String(value || "");
}

function resultStateColor(state: TaskResultState) {
  return state === "ready" ? "green" : state === "error" ? "red" : "orange";
}

function coverageText(task: CollectorTask) {
  const coverage = resultCoverage(task);
  if (coverage.start === "-" && coverage.end === "-") return "暂未提供";
  return `${coverage.start} 至 ${coverage.end}`;
}

async function load() {
  const spaceId = selectedSpaceId.value;
  const sequence = ++loadSequence;
  if (!spaceId) {
    tasks.value = [];
    activeTaskId.value = "";
    loadError.value = "";
    loading.value = false;
    return;
  }

  loading.value = true;
  loadError.value = "";
  try {
    const response = await GetTaskList({
      space_id: spaceId,
      page: { page: 1, size: 1000 }
    });
    if (sequence !== loadSequence || spaceId !== selectedSpaceId.value) return;
    tasks.value = response.tasks || [];
    const nextTaskId = selectTaskIdFromQuery(tasks.value, route.query.resultTask);
    activeTaskId.value = nextTaskId;
    await keepResultSelection(nextTaskId);
  } catch (error) {
    if (sequence === loadSequence) {
      tasks.value = [];
      loadError.value = error instanceof Error ? error.message : "加载采集结果失败";
      Message.error(loadError.value);
    }
  } finally {
    if (sequence === loadSequence) loading.value = false;
  }
}

async function keepResultSelection(taskId: string) {
  const requestedTaskId = queryTaskId(route.query.resultTask);
  if (requestedTaskId === taskId && route.query.tab === "results") return;
  await router.replace({ path: "/collector/tasks", query: buildResultsQuery(taskId) });
}

function onTaskChange(value: string | number) {
  const taskId = String(value);
  activeTaskId.value = taskId;
  void router.replace({ path: "/collector/tasks", query: buildResultsQuery(taskId) });
}

function goToTasks() {
  void router.replace({ path: "/collector/tasks", query: { tab: "tasks" } });
}

function goToTaskDetail() {
  void router.replace({
    path: "/collector/tasks",
    query: { tab: "tasks", ...(activeTaskId.value ? { taskId: activeTaskId.value } : {}) }
  });
}

watch(
  selectedSpaceId,
  () => {
    void load();
  },
  { immediate: true }
);

watch(
  () => route.query.resultTask,
  value => {
    const requested = queryTaskId(value);
    if (requested && resultTabs.value.some(tab => tab.taskId === requested)) {
      activeTaskId.value = requested;
    }
  }
);
</script>

<style scoped>
.task-results-page {
  min-height: 100%;
}

.result-context {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-4);
  margin: var(--moox-space-3) 0;
  padding: var(--moox-space-3) var(--moox-space-4);
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.result-context__heading {
  min-width: 220px;
}

.result-context h3 {
  margin: 0 0 var(--moox-space-2);
}

.result-context__tags {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
}

.result-context__summary {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-4);
  margin: 0;
}

.result-context__summary div {
  min-width: 150px;
}

.result-context__summary dt {
  color: var(--color-text-3);
  font-size: 12px;
}

.result-context__summary dd {
  margin: var(--moox-space-1) 0 0;
  color: var(--color-text-1);
}
</style>
