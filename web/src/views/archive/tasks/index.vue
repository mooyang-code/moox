<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>归档任务</h2>
        <a-button type="primary" status="success" @click="openCreate()">
          <template #icon><icon-plus /></template>
          新建归档任务
        </a-button>
      </div>

      <WorkerLine
        :overview="overview"
        :loaded="mainLoaded"
        :error="mainError"
        :last-ok-at="lastOkAt"
        :tasks="tasks"
        :now-ms="nowMs"
        :host="workerHost"
        @retry="retryMain"
      />

      <UnarchivedList :items="unarchived" :error="unarchivedError" @create="openCreate" @retry="retryDatasets" />

      <TaskTable
        :tasks="tasks"
        :loaded="mainLoaded"
        :error="mainError"
        :unarchived-count="unarchived.length"
        :datasets-loaded="datasetsLoaded"
        :names="datasetNames"
        :zone="displayZone"
        :now-ms="nowMs"
        :busy="busy"
        :remote-enabled="remoteEnabled"
        @open="openTask"
        @run="runNow"
        @edit="openEdit"
        @backfill="openBackfill"
        @toggle="toggleTask"
        @remove="removeTask"
        @retry="retryMain"
      />
    </div>

    <TaskDetailDrawer
      :task="selectedTask"
      :name="selectedTask ? datasetNames[selectedTask.dataset_id || ''] : ''"
      :space-id="spaceId"
      :query="query"
      :zone="displayZone"
      :now-ms="nowMs"
      :busy="busy"
      :remote-enabled="remoteEnabled"
      :runs-refresh-key="runsRefreshKey"
      @update:query="setQuery"
      @close="setQuery({ task: '' })"
      @run="runNow"
      @edit="openEdit"
      @backfill="openBackfill"
      @toggle="toggleTask"
      @remove="removeTask"
      @changed="refreshAfterAction"
    />

    <TaskEditorDrawer
      v-model:visible="editor.visible"
      :mode="editor.mode"
      :task="editor.task"
      :preset-dataset-id="editor.presetDatasetId"
      :datasets="archivableList"
      :tasks="tasks"
      :zone="displayZone"
      :space-id="spaceId"
      :avoidance="overview?.avoidance"
      @created="onCreated"
      @updated="refreshAfterAction"
      @open-task="openTask"
      @open-backfill="openBackfill"
    />

    <BackfillDialog v-model:visible="backfill.visible" :task="backfill.task" :zone="displayZone" @started="onBackfillStarted" />
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message, Modal } from "@arco-design/web-vue";
import {
  deleteArchiveTask,
  getArchiveOverview,
  listArchiveTasks,
  runArchiveTaskNow,
  updateArchiveTask,
  type ArchiveTask,
  type GetArchiveOverviewRsp
} from "@/api/archive";
import type { Dataset } from "@/api/storage/types";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { usePolling } from "@/hooks/usePolling";
import { useSpaceStore } from "@/store/modules/space";
import { RequestGate } from "@/utils/request-gate";
import { formatRetention } from "@/utils/retention";
import {
  archivableDataset,
  archiveQueryFor,
  parseArchiveQuery,
  pauseConfirmText,
  unarchivedDatasets,
  type ArchiveQuery
} from "../shared/archive-model";
import { invalidateArchiveCoverage, listAllDatasets } from "../shared/archive-coverage";
import BackfillDialog from "./backfill-dialog.vue";
import TaskDetailDrawer from "./task-detail-drawer.vue";
import TaskEditorDrawer from "./task-editor-drawer.vue";
import TaskTable from "./task-table.vue";
import { useArchiveWorkerHost } from "../shared/use-archive-worker-host";
import UnarchivedList from "./unarchived-list.vue";
import WorkerLine from "./worker-line.vue";

/**
 * 数据归档 › 归档任务。状态都在 URL 里（任务详情、页签、筛选、页码、新建），不依赖页面缓存；
 * 读取失败显示失败，不显示“正常”；后台轮询带 silentError，错误由页面自己显示。
 */
const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const spaceId = computed(() => spaceStore.selectedSpaceId || "");
// 任务、运行、分区的时刻按当前空间的时区；执行器状态行按 UTC（在组件里）
const displayZone = useDisplayZone();

const query = computed(() => parseArchiveQuery(route.query));
function setQuery(patch: Partial<ArchiveQuery>) {
  void router.replace({ query: archiveQueryFor({ ...query.value, ...patch }) });
}

const overview = ref<GetArchiveOverviewRsp | null>(null);
const tasks = ref<ArchiveTask[]>([]);
const mainLoaded = ref(false);
const mainError = ref("");
const lastOkAt = ref<number | null>(null);
const datasets = ref<Dataset[]>([]);
const datasetsLoaded = ref(false);
const datasetsError = ref("");
const nowMs = ref(Date.now());
// 立即运行、补拉区间成功后加 1：抽屉已经开在运行记录页签时 URL 不变，靠它让运行记录重读
const runsRefreshKey = ref(0);
// 行内按钮按任务记“处理中”，不共用一个 busy
const busy = ref<Record<string, boolean>>({});
const mainGate = new RequestGate();
const datasetGate = new RequestGate();

const errorText = (error: unknown) => (error instanceof Error && error.message ? error.message : "请稍后重试");

async function loadMain() {
  if (!spaceId.value) return;
  const token = mainGate.next();
  try {
    const [nextOverview, list] = await Promise.all([
      getArchiveOverview({ silentError: true }),
      listArchiveTasks({ page: 1, size: 100 }, { silentError: true })
    ]);
    if (!mainGate.isCurrent(token)) return;
    overview.value = nextOverview;
    tasks.value = list.tasks || [];
    mainLoaded.value = true;
    mainError.value = "";
    lastOkAt.value = Date.now();
  } catch (error) {
    if (error instanceof StaleSpaceRequestError || !mainGate.isCurrent(token)) return;
    mainError.value = errorText(error);
  } finally {
    if (mainGate.isCurrent(token)) nowMs.value = Date.now();
  }
}

/** 当前空间的全部数据集（翻完所有页）：未归档判定、任务表的中文名、新建抽屉的下拉都用它。 */
async function loadDatasets() {
  const space = spaceId.value;
  if (!space) return;
  const token = datasetGate.next();
  datasetsError.value = "";
  try {
    const all = await listAllDatasets(space, { silentError: true });
    if (!datasetGate.isCurrent(token)) return;
    datasets.value = all;
    datasetsLoaded.value = true;
  } catch (error) {
    if (error instanceof StaleSpaceRequestError || !datasetGate.isCurrent(token)) return;
    datasetsError.value = errorText(error);
  }
}

const { tick } = usePolling(loadMain, 30_000, () => Boolean(spaceId.value));
// 执行器所在主机与空间无关，打开页面时读一次；读取失败时状态行不写主机，重试时一并重读
const { host: workerHost, load: loadWorkerHost } = useArchiveWorkerHost();
void loadWorkerHost();

function retryMain() {
  void loadMain();
  if (!workerHost.value) void loadWorkerHost();
}
function retryDatasets() {
  void loadDatasets();
  if (!mainLoaded.value) void loadMain();
}

watch(
  spaceId,
  (next, previous) => {
    // 切空间：清掉 URL 里属于旧空间的参数；第一次拿到空间（previous 为空）时保留深链接
    if (previous && next !== previous) void router.replace({ query: {} });
    overview.value = null;
    tasks.value = [];
    datasets.value = [];
    mainLoaded.value = false;
    datasetsLoaded.value = false;
    mainError.value = "";
    datasetsError.value = "";
    lastOkAt.value = null;
    busy.value = {};
    void loadMain();
    void loadDatasets();
  },
  { immediate: true }
);

const taskIds = computed(() => tasks.value.map(task => task.dataset_id || ""));
// worker 报告启用了对象存储时才显示远端副本的列
const remoteEnabled = computed(() => Boolean(overview.value?.worker?.object_store?.endpoint));
const datasetNames = computed(() => Object.fromEntries(datasets.value.map(item => [item.dataset_id, item.name || ""])));
const archivableList = computed(() => datasets.value.filter(item => archivableDataset(item, spaceId.value)));
const unarchived = computed(() =>
  mainLoaded.value && datasetsLoaded.value ? unarchivedDatasets(datasets.value, tasks.value, spaceId.value) : []
);
// 两路读取任一失败都不能当成“全部已归档”：首次读取任务失败也算
const unarchivedError = computed(() => datasetsError.value || (!mainLoaded.value ? mainError.value : ""));

// ---- 任务详情（?task=） ----
const selectedTask = computed(() =>
  query.value.task ? tasks.value.find(task => task.dataset_id === query.value.task) : undefined
);
watch([() => query.value.task, mainLoaded, tasks], () => {
  if (query.value.task && mainLoaded.value && !selectedTask.value) {
    Message.warning("任务不存在或不在当前空间");
    setQuery({ task: "" });
  }
});

function openTask(datasetId: string) {
  setQuery({ task: datasetId, tab: "runs", create: "" });
}

// ---- 新建 / 编辑（?create=） ----
const editor = reactive({
  visible: false,
  mode: "create" as "create" | "edit",
  task: null as ArchiveTask | null,
  presetDatasetId: ""
});
function openCreate(datasetId = "") {
  Object.assign(editor, { visible: true, mode: "create", task: null, presetDatasetId: datasetId });
}
function openEdit(task: ArchiveTask) {
  Object.assign(editor, { visible: true, mode: "edit", task, presetDatasetId: "" });
}
watch([() => query.value.create, mainLoaded], ([create, loaded]) => {
  if (!create || !loaded) return;
  // 已有任务时改为打开它的详情
  if (taskIds.value.includes(create)) setQuery({ create: "", task: create, tab: "runs" });
  else {
    openCreate(create);
    setQuery({ create: "" });
  }
});
async function onCreated(datasetId: string) {
  invalidateArchiveCoverage();
  // 先重读任务表再打开详情：否则详情找不到新任务，会被当成“任务不存在”关掉
  await loadMain();
  openTask(datasetId);
}

// ---- 补拉区间 ----
const backfill = reactive({ visible: false, task: null as ArchiveTask | null });
function openBackfill(task: ArchiveTask) {
  Object.assign(backfill, { visible: true, task });
}
function onBackfillStarted(datasetId: string) {
  void tick();
  setQuery({ task: datasetId, tab: "runs" });
  runsRefreshKey.value += 1;
}

// ---- 行操作 ----
async function withBusy(datasetId: string, action: () => Promise<void>) {
  busy.value = { ...busy.value, [datasetId]: true };
  try {
    await action();
  } finally {
    busy.value = Object.fromEntries(Object.entries(busy.value).filter(([key]) => key !== datasetId));
  }
}

function refreshAfterAction() {
  void tick();
}

async function runNow(task: ArchiveTask) {
  const id = task.dataset_id || "";
  await withBusy(id, async () => {
    try {
      await runArchiveTaskNow(id);
    } catch {
      return;
    }
    Message.success("已排队");
    setQuery({ task: id, tab: "runs" });
    runsRefreshKey.value += 1;
    void tick();
  });
}

const displayName = (task: ArchiveTask) => datasetNames.value[task.dataset_id || ""] || task.dataset_id || "";

function toggleTask(task: ArchiveTask) {
  const id = task.dataset_id || "";
  if (task.status === "paused") {
    void withBusy(id, async () => {
      try {
        await updateArchiveTask({ dataset_id: id, status: "enabled" });
      } catch {
        return;
      }
      Message.success("已启用，下一次检查立即复核");
      void tick();
    });
    return;
  }
  Modal.confirm({
    title: `停用「${displayName(task)}」的归档？`,
    content: pauseConfirmText(task.retention, formatRetention(task.retention)),
    okText: "停用",
    okButtonProps: { status: "warning" },
    cancelText: "取消",
    onBeforeOk: async () => {
      try {
        await updateArchiveTask({ dataset_id: id, status: "paused" });
      } catch {
        return false;
      }
      Message.success("已停用");
      void tick();
      return true;
    }
  });
}

function removeTask(task: ArchiveTask) {
  const id = task.dataset_id || "";
  Modal.confirm({
    title: `删除「${displayName(task)}」的归档任务？`,
    content: "删除后这个数据集不再归档，线上数据超过保留期会被删除。",
    okText: "删除",
    okButtonProps: { status: "danger" },
    cancelText: "取消",
    onBeforeOk: async () => {
      try {
        await deleteArchiveTask(id);
      } catch {
        return false;
      }
      Message.success("已删除");
      invalidateArchiveCoverage();
      if (query.value.task === id) setQuery({ task: "" });
      void tick();
      return true;
    }
  });
}
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}
</style>
