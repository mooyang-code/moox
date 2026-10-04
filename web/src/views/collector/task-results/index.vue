<template>
  <div class="moox-page task-results-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先选择空间</a-alert>
      <a-spin v-else :loading="loading">
        <a-alert v-if="loadError" type="error" show-icon>{{ loadError }}</a-alert>

        <a-alert v-if="detailError" type="warning" show-icon class="result-refresh-warning">
          {{ detailError }}<span v-if="activeLastStatusReadAt">；保留上次读取的结果状态（{{ activeLastStatusReadAt }}）</span>
        </a-alert>

        <a-empty v-if="!resultTabs.length && !detailError" description="暂无采集任务">
          <template #extra>
            <a-button type="primary" status="success" @click="goToTasks">新建采集任务</a-button>
          </template>
        </a-empty>

        <template v-else-if="resultTabs.length">
          <div class="result-toolbar">
            <span class="result-count">
              共 {{ pagination.total }} 个任务<span v-if="activeLastStatusReadAt">；状态读取：{{ activeLastStatusReadAt }}</span>
            </span>
            <a-button :loading="detailLoading" :disabled="!activeTaskId" @click="refreshActiveTask">刷新结果</a-button>
          </div>
          <a-tabs v-model:active-key="activeTaskId" type="rounded" size="medium" class="result-tabs" @change="onTaskChange">
            <a-tab-pane v-for="tab in resultTabs" :key="tab.taskId" :title="tab.title" />
          </a-tabs>

          <a-alert v-if="!detailError && activeState === 'stale'" type="info" show-icon class="result-refresh-warning">
            结果状态待刷新，当前展示的是最近一次读取的状态。
          </a-alert>

          <a-alert v-if="activeState === 'error'" type="error" show-icon>
            {{ activeTask?.last_error || "结果视图不可用，请检查任务配置或稍后重试。" }}
          </a-alert>
          <a-empty v-else-if="!detailError && (!activeViewReadable || activeViewIds.length === 0)" description="结果准备中，请稍后刷新">
            <template #extra>
              <a-button type="outline" @click="goToTaskDetail">查看任务详情</a-button>
            </template>
          </a-empty>
          <ViewBrowse
            v-else-if="activeViewReadable && activeViewIds.length > 0"
            embedded
            :view-ids="activeViewIds"
            :active-view-id="activeViewIds[0]"
            :refresh-key="viewRefreshKey"
            :view-owner-modules="['collector']"
            :view-roles="['collection_browse']"
            empty-description="结果准备中"
            empty-rows-description="任务已准备，尚未产生数据"
          />

          <a-pagination
            v-if="pagination.total > pagination.pageSize"
            :current="pagination.current"
            :page-size="pagination.pageSize"
            :total="pagination.total"
            size="small"
            @change="onPageChange"
          />
        </template>
      </a-spin>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { ControlRequestError } from "@/api/admin/http";
import { AuthSessionExpiredError } from "@/api/admin/auth-errors";
import { useRoute, useRouter } from "vue-router";
import { GetTaskDetail, GetTaskList, type CollectorTask } from "@/api/collector";
import { useSpaceStore } from "@/store/modules/space";
import ViewBrowse from "@/views/data/view-browse/index.vue";
import {
  buildResultsQuery,
  buildTaskResultTabs,
  getTaskResultState,
  resolveActiveTask,
  selectTaskIdFromQuery,
  type TaskResultState
} from "./task-results-model";

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const loading = ref(false);
const loadError = ref("");
const detailLoading = ref(false);
const detailError = ref("");
const lastStatusReadAtByTask = ref<Record<string, string>>({});
const readableViewIdsByTask = ref<Record<string, string>>({});
const viewRefreshKey = ref(0);
const tasks = ref<CollectorTask[]>([]);
const taskDetails = ref<Record<string, CollectorTask>>({});
const activeTaskId = ref(queryTaskId(route.query.resultTask));
const pagination = reactive({ current: 1, pageSize: 100, total: 0 });
let loadSequence = 0;
let detailSequence = 0;
let loadedSpaceId = "";

const permanentTaskDetailErrorCodes = new Set<unknown>([2, 3, 5, "2", "3", "5", "NO_AUTH", "NO_PERMISSION", "NOT_FOUND"]);
class TaskDetailIdentityError extends Error {}

const resultTabs = computed(() => buildTaskResultTabs(tasks.value));
const activeTask = computed(() => resolveActiveTask(tasks.value, taskDetails.value, activeTaskId.value));
const activeViewIds = computed(() => (activeTask.value ? buildTaskResultTabs([activeTask.value])[0]?.viewIds || [] : []));
const activeState = computed<TaskResultState>(() => (activeTask.value ? getTaskResultState(activeTask.value) : "preparing"));
const activeLastStatusReadAt = computed(() => lastStatusReadAtByTask.value[activeTaskId.value] || "");
const activeViewReadable = computed(() => (activeState.value === "ready" || activeState.value === "stale") &&
  readableViewIdsByTask.value[activeTaskId.value] === activeViewIds.value.join("\u0000"));

function queryTaskId(value: unknown) {
  return Array.isArray(value) ? String(value[0] || "") : String(value || "");
}

function isPermanentTaskDetailError(error: unknown) {
  if (error instanceof TaskDetailIdentityError) return true;
  if (error instanceof AuthSessionExpiredError) return true;
  if (!(error instanceof ControlRequestError)) return false;
  const code = error.response?.ret_info?.code;
  return permanentTaskDetailErrorCodes.has(code);
}

function clearTaskResultCache(taskId: string) {
  const withoutTask = <T,>(items: Record<string, T>) => {
    const next = { ...items };
    delete next[taskId];
    return next;
  };
  taskDetails.value = withoutTask(taskDetails.value);
  readableViewIdsByTask.value = withoutTask(readableViewIdsByTask.value);
  lastStatusReadAtByTask.value = withoutTask(lastStatusReadAtByTask.value);
  tasks.value = tasks.value.filter(task => task.task_id !== taskId);
}

async function load(page = 1, options: { preserveSelection?: boolean; honorQuery?: boolean } = {}) {
  const spaceId = selectedSpaceId.value;
  const sequence = ++loadSequence;
  if (!spaceId) {
    tasks.value = [];
    taskDetails.value = {};
    lastStatusReadAtByTask.value = {};
    readableViewIdsByTask.value = {};
    pagination.current = 1;
    pagination.total = 0;
    activeTaskId.value = "";
    loadError.value = "";
    loading.value = false;
    return;
  }
  if (loadedSpaceId !== spaceId) {
    loadedSpaceId = spaceId;
    tasks.value = [];
    taskDetails.value = {};
    lastStatusReadAtByTask.value = {};
    readableViewIdsByTask.value = {};
    pagination.current = 1;
    activeTaskId.value = queryTaskId(route.query.resultTask);
  }

  loading.value = true;
  loadError.value = "";
  try {
    const response = await GetTaskList({
      space_id: spaceId,
      page: { page, size: pagination.pageSize }
    });
    if (sequence !== loadSequence || spaceId !== selectedSpaceId.value) return;
    const listTasks = response.tasks || [];
    tasks.value = listTasks;
    pagination.current = response.page?.page || page;
    pagination.total = response.page?.total ?? listTasks.length;
    const requestedTaskId = queryTaskId(route.query.resultTask);
    const currentTaskIsOnPage = listTasks.some(task => task.task_id === activeTaskId.value);
    const requestedTaskIsOnPage = listTasks.some(task => task.task_id === requestedTaskId);
    const nextTaskId = options.preserveSelection && currentTaskIsOnPage
      ? activeTaskId.value
      : options.honorQuery !== false && requestedTaskId && !requestedTaskIsOnPage
        ? requestedTaskId
        : selectTaskIdFromQuery(listTasks, options.honorQuery === false ? "" : requestedTaskId);
    activeTaskId.value = nextTaskId;
    await keepResultSelection(nextTaskId);
    if (nextTaskId) {
      if (!listTasks.some(task => task.task_id === nextTaskId) && requestedTaskId === nextTaskId) {
        void loadTaskDetail(nextTaskId, { includeInTabs: true });
      } else {
        void loadTaskDetail(nextTaskId);
      }
    }
  } catch (error) {
    if (sequence === loadSequence) {
      loadError.value = error instanceof Error ? error.message : "加载采集结果失败";
      Message.error(loadError.value);
    }
  } finally {
    if (sequence === loadSequence) loading.value = false;
  }
}

async function loadTaskDetail(taskId: string, options: { includeInTabs?: boolean } = {}) {
  const spaceId = selectedSpaceId.value;
  if (!spaceId || !taskId) return false;
  const sequence = ++detailSequence;
  detailLoading.value = true;
  detailError.value = "";
  try {
    const response = await GetTaskDetail({ space_id: spaceId, task_id: taskId });
    if (sequence !== detailSequence || spaceId !== selectedSpaceId.value || taskId !== activeTaskId.value) return false;
    const task = response.task;
    if (!task || task.task_id !== taskId) throw new TaskDetailIdentityError("任务结果身份与请求不一致");
    const state = getTaskResultState(task);
    if (state === "ready") {
      readableViewIdsByTask.value = {
        ...readableViewIdsByTask.value,
        [taskId]: buildTaskResultTabs([task])[0].viewIds.join("\u0000")
      };
    } else if (state !== "stale") {
      const remaining = { ...readableViewIdsByTask.value };
      delete remaining[taskId];
      readableViewIdsByTask.value = remaining;
    }
    taskDetails.value = { ...taskDetails.value, [taskId]: task };
    if (options.includeInTabs && !tasks.value.some(item => item.task_id === taskId)) {
      tasks.value = [...tasks.value, task];
    } else {
      tasks.value = tasks.value.map(item => item.task_id === taskId ? task : item);
    }
    lastStatusReadAtByTask.value = { ...lastStatusReadAtByTask.value, [taskId]: new Date().toLocaleTimeString() };
    return state === "ready";
  } catch (error) {
    if (sequence === detailSequence) {
      if (isPermanentTaskDetailError(error)) clearTaskResultCache(taskId);
      detailError.value = error instanceof Error ? error.message : "刷新结果状态失败";
    }
    return false;
  } finally {
    if (sequence === detailSequence) detailLoading.value = false;
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
  void loadTaskDetail(taskId);
}

function onPageChange(page: number) {
  pagination.current = page;
  void load(page, { honorQuery: false });
}

async function refreshActiveTask() {
  const taskId = activeTaskId.value;
  if (!taskId) return;
  const ownershipVerified = await loadTaskDetail(taskId);
  if (ownershipVerified && taskId === activeTaskId.value) viewRefreshKey.value += 1;
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
  () => [route.query.tab, route.query.resultTask] as const,
  ([tab, value], previous) => {
    if (queryTaskId(tab) !== "results" || !selectedSpaceId.value) return;
    const requested = queryTaskId(value);
    const enteredResultsTab = !previous || queryTaskId(previous[0]) !== "results";
    if (!requested) {
      const fallbackTaskId = resultTabs.value[0]?.taskId || "";
      activeTaskId.value = fallbackTaskId;
      detailError.value = "";
      if (!fallbackTaskId) return;
      void keepResultSelection(fallbackTaskId);
      void loadTaskDetail(fallbackTaskId);
      return;
    }
    if (requested === activeTaskId.value && !enteredResultsTab) return;
    activeTaskId.value = requested;
    if (resultTabs.value.some(item => item.taskId === requested)) {
      void loadTaskDetail(requested);
    } else {
      void loadTaskDetail(requested, { includeInTabs: true });
    }
  }
);
</script>

<style scoped>
.task-results-page {
  width: 100%;
  max-width: 100%;
  min-width: 0;
  min-height: 100%;
  overflow-x: hidden;
}

.task-results-page :deep(.arco-spin),
.task-results-page :deep(.arco-spin-children) {
  display: block;
  width: 100%;
  max-width: 100%;
  min-width: 0;
}

.result-tabs {
  min-width: 0;
  margin-bottom: var(--moox-space-3);
}

.result-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-2);
}

.result-count {
  color: var(--color-text-2);
}

.result-refresh-warning {
  margin-bottom: var(--moox-space-3);
}

.result-tabs :deep(.arco-tabs-content) {
  display: none;
}
</style>
