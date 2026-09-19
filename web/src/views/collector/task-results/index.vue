<template>
  <div class="moox-page task-results-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先选择空间</a-alert>
      <template v-else>
        <a-tabs v-if="results.length" v-model:active-key="activeTaskId" type="rounded" @change="onTaskChange">
          <a-tab-pane v-for="item in results" :key="item.task_id" :title="item.task_name || item.task_id" />
        </a-tabs>
        <a-empty v-if="!loading && !results.length" description="暂无采集任务结果" />
        <a-spin v-else :loading="loading">
          <section v-if="activeTask" class="result-context">
            <div>
              <h3>{{ activeTask.task_name || activeTask.task_id }}</h3>
              <span>{{ resultStatusLabel(activeTask.result?.status) }}</span>
            </div>
            <a-tag :color="activeTask.enabled === false || activeTask.enabled === 'false' ? 'orange' : 'green'">
              {{ activeTask.enabled === false || activeTask.enabled === "false" ? "任务已停用" : "任务运行中" }}
            </a-tag>
          </section>
          <ViewBrowse
            v-if="activeTask?.result?.view_id"
            embedded
            :active-view-id="activeTask.result.view_id"
            :hide-technical-identity="true"
            :view-owner-modules="['collector']"
            :view-roles="['collection_browse']"
            empty-description="结果视图准备中"
            :auto-refresh-interval-ms="30000"
          />
          <a-empty v-else description="结果准备中，请稍后刷新" />
        </a-spin>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import { listCollectorTasks, type CollectorTask } from "@/api/collector";
import ViewBrowse from "@/views/data/view-browse/index.vue";

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const loading = ref(false);
const tasks = ref<CollectorTask[]>([]);
const activeTaskId = ref(String(route.query.resultTask || ""));
// Keep tasks whose result View is still being prepared visible. The backend
// owns the result identity before asynchronous resample preparation completes,
// so hiding these tasks would make the preparation/error states unreachable.
const results = computed(() => tasks.value);
const activeTask = computed(() => results.value.find(task => task.task_id === activeTaskId.value) || results.value[0]);
let loadSequence = 0;

async function load() {
  const spaceId = selectedSpaceId.value;
  const sequence = ++loadSequence;
  if (!spaceId) {
    tasks.value = [];
    activeTaskId.value = "";
    return;
  }
  loading.value = true;
  try {
    const nextTasks = await listCollectorTasks(spaceId);
    if (sequence !== loadSequence || spaceId !== selectedSpaceId.value) return;
    tasks.value = nextTasks;
    if (!results.value.some(task => task.task_id === activeTaskId.value)) activeTaskId.value = results.value[0]?.task_id || "";
  } catch (error) {
    if (sequence === loadSequence) {
      tasks.value = [];
      Message.error(error instanceof Error ? error.message : "加载采集结果失败");
    }
  } finally {
    if (sequence === loadSequence) loading.value = false;
  }
}

function onTaskChange(value: string | number) {
  activeTaskId.value = String(value);
  void router.replace({ path: "/collector/tasks", query: { tab: "results", resultTask: activeTaskId.value } });
}

function resultStatusLabel(status?: string) {
  if (status === "active") return "结果可用";
  if (status === "error") return "结果异常";
  return "结果准备中";
}

watch(selectedSpaceId, load, { immediate: true });
watch(
  () => route.query.resultTask,
  value => {
    if (value) activeTaskId.value = String(value);
  }
);
</script>

<style scoped>
.task-results-page {
  min-height: 100%;
}
.result-context {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: 12px 0;
  padding: 12px 16px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.result-context h3 {
  margin: 0 0 4px;
}
.result-context span {
  color: var(--color-text-3);
}
</style>
