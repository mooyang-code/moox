<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space class="task-toolbar" wrap>
          <a-button type="primary" status="success" @click="onAdd">
            <template #icon><icon-plus /></template>
            新建采集任务
          </a-button>
          <a-input
            v-model="filters.keyword"
            placeholder="搜索任务名称 / 任务 ID"
            allow-clear
            style="width: 220px"
            @press-enter="search"
            @clear="search"
          />
          <a-select v-model="filters.dataType" placeholder="数据类型" allow-clear style="width: 140px" @change="search">
            <a-option v-for="config in availableDataTypeConfigs" :key="config.data_type" :value="config.data_type">
              {{ config.type_name }}
            </a-option>
          </a-select>
          <a-select v-model="filters.enabled" placeholder="启用状态" style="width: 120px" @change="search">
            <a-option value="">全部</a-option>
            <a-option value="true">启用</a-option>
            <a-option value="false">禁用</a-option>
          </a-select>
        </a-space>

        <a-table
          row-key="task_id"
          size="small"
          :data="taskList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: 1250 }"
          :pagination="paginationConfig"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #empty>
            <a-empty :description="listEmptyDescription">
              <a-button v-if="hasActiveFilters" size="small" @click="clearFilters">清除筛选</a-button>
              <a-button v-else type="primary" status="success" size="small" @click="onAdd">新建采集任务</a-button>
            </a-empty>
          </template>
          <template #columns>
            <a-table-column title="任务名称" data-index="task_name" :width="240">
              <template #cell="{ record }">
                <a-link @click="onViewDetails(record)">{{ record.task_name || record.task_id }}</a-link>
                <ArchiveCoverageText :dataset-id="record.result?.dataset_id" />
              </template>
            </a-table-column>
            <a-table-column title="数据类型" :width="100">
              <template #cell="{ record }">{{ dataTypeLabel(record.data_type) }}</template>
            </a-table-column>
            <a-table-column title="数据范围标签" :width="220">
              <template #cell="{ record }">{{ taskTagSummary(record) }}</template>
            </a-table-column>
            <a-table-column title="来源 / 市场" :width="180">
              <template #cell="{ record }">{{ taskRouteSummary(record) }}</template>
            </a-table-column>
            <a-table-column title="频率" :width="90">
              <template #cell="{ record }">{{ taskFrequency(record) }}</template>
            </a-table-column>
            <a-table-column title="采集方式" :width="110">
              <template #cell="{ record }">{{ ingestLabel(record.ingest) }}</template>
            </a-table-column>
            <a-table-column title="合成来源" :width="200">
              <template #cell="{ record }">{{ resampleSourceSummary(record) }}</template>
            </a-table-column>
            <a-table-column title="启用状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.enabled ? 'green' : 'orange'">
                  {{ record.enabled ? "启用" : "禁用" }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="230" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space wrap>
                  <a-button
                    v-if="record.data_type === 'kline' || record.data_type === 'kline_resample'"
                    type="primary"
                    status="success"
                    size="mini"
                    @click="onAddResample(record)"
                  >
                    新建合成
                  </a-button>
                  <a-button type="primary" size="mini" :disabled="togglingTaskId === record.task_id" @click="onUpdate(record)">
                    <template #icon><icon-edit /></template>
                    修改
                  </a-button>
                  <a-dropdown trigger="click" position="br">
                    <a-button
                      size="mini"
                      :loading="togglingTaskId === record.task_id"
                      :disabled="deleteTarget?.task_id === record.task_id && deleteLoading"
                    >
                      <template #icon><icon-more /></template>
                    </a-button>
                    <template #content>
                      <a-doption :disabled="togglingTaskId === record.task_id" @click="confirmEnableChange(record)">
                        <span :class="record.enabled ? 'action-warning' : 'action-success'">
                          {{ record.enabled ? "禁用" : "启用" }}
                        </span>
                      </a-doption>
                      <a-doption class="action-danger" @click="openDelete(record)">删除</a-doption>
                      <a-doption v-if="record.data_type === 'kline_resample'" class="action-resample" @click="openJobs(record)">
                        历史合成
                      </a-doption>
                    </template>
                  </a-dropdown>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </a-spin>

    <TaskEditorDrawer v-model:visible="editorVisible" :request="editorRequest" @saved="getTaskList" />

    <TaskDetailDrawer
      v-model:visible="detailVisible"
      :task="detailData"
      @cancel="onDetailCancel"
      @results="openResultsFromDetail"
      @edit="editFromDetail"
      @jobs="jobsFromDetail"
      @locate="locateTask"
    />

    <a-modal
      v-model:visible="deleteVisible"
      :title="deleteTarget ? `删除「${deleteTarget.task_name || deleteTarget.task_id}」` : '删除采集任务'"
      ok-text="删除"
      :ok-button-props="{ status: deleteResultData ? 'danger' : 'warning' }"
      :ok-loading="deleteLoading"
      @before-ok="handleDeleteOk"
    >
      <a-alert type="warning" show-icon>
        删除任务会移除任务配置、实例和运行记录。结果数据默认保留；如需物理删除，请显式选择第二项。
      </a-alert>
      <p v-if="deleteDependents.length" class="delete-dependents">
        以它为源的合成任务：{{
          deleteDependents.map(item => item.task_name || item.task_id).join("、")
        }}。删除后它们收不到新的源数据。
      </p>
      <a-radio-group v-model="deleteResultData" direction="vertical" class="delete-options">
        <a-radio :value="false">删除任务，保留结果数据</a-radio>
        <a-radio :value="true">删除任务并物理删除结果数据</a-radio>
      </a-radio-group>
    </a-modal>

    <ResampleJobsDrawer
      v-if="jobsTarget"
      v-model:visible="jobsVisible"
      :space-id="selectedSpaceId || ''"
      :task-id="jobsTarget.taskId"
      :task-name="jobsTarget.taskName"
      :target-frequency="jobsTarget.targetFrequency"
    />
  </div>
</template>

<script setup lang="ts">
import { RequestError, reportError } from "@/api/http";
import { buildResultsQuery } from "@/views/collector/task-results/task-results-model";
import ArchiveCoverageText from "@/views/archive/shared/archive-coverage-text.vue";
import { computed, onActivated, onDeactivated, onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message, Modal } from "@arco-design/web-vue";
import { DeleteTask, DisableTask, GetTaskDetail, GetTaskList, UpdateTask } from "@/api/collector";
import {
  ingestLabel,
  normalizeCollectionTask,
  taskFrequency,
  type CollectionTaskRecord,
  type TaskEditorRequest
} from "./collection-task-params";
import { provideCollectionCatalog } from "./use-collection-catalog";
import ResampleJobsDrawer from "./resample-jobs-drawer.vue";
import TaskEditorDrawer from "./task-editor-drawer.vue";
import TaskDetailDrawer from "./task-detail-drawer.vue";

type EnabledFilter = "" | "true" | "false";

const catalog = provideCollectionCatalog();
const { selectedSpaceId, availableDataTypeConfigs } = catalog;
const { dataTypeLabel, taskTagSummary, taskRouteSummary, resampleSourceSummary } = catalog;

const loading = ref(false);
const taskList = ref<CollectionTaskRecord[]>([]);
// 列表页持有编辑、详情两个抽屉的显示状态：从详情切到修改时由这里关一个开一个。
const editorVisible = ref(false);
const editorRequest = ref<TaskEditorRequest>();
const detailVisible = ref(false);
const detailData = ref<CollectionTaskRecord>();
const deleteVisible = ref(false);
const deleteLoading = ref(false);
const deleteResultData = ref(false);
const deleteTarget = ref<CollectionTaskRecord>();
const jobsVisible = ref(false);
const jobsTarget = ref<{ taskId: string; taskName: string; targetFrequency: string }>();

const queryText = (value: unknown) => (typeof value === "string" ? value : "");
function normalizeEnabledFilter(value: unknown): EnabledFilter {
  return value === "true" || value === "false" ? value : "";
}

const route = useRoute();
const router = useRouter();

const filters = reactive<{
  keyword: string;
  dataType: string;
  enabled: EnabledFilter;
}>({
  keyword: queryText(route.query.keyword),
  dataType: queryText(route.query.dataType),
  enabled: normalizeEnabledFilter(route.query.enabled)
});

const pagination = reactive({
  current: Math.max(1, Number.parseInt(queryText(route.query.page), 10) || 1),
  pageSize: 10,
  total: 0
});

// 行内启停的确认与执行状态：菜单项点击先确认，请求期间锁定该行操作。
const togglingTaskId = ref("");

const hasActiveFilters = computed(() => Boolean(filters.keyword.trim() || filters.dataType || filters.enabled));
const listEmptyDescription = computed(() =>
  hasActiveFilters.value ? "没有匹配的任务，试试调整筛选条件" : "还没有采集任务，创建一个开始采集行情"
);

function clearFilters() {
  filters.keyword = "";
  filters.dataType = "";
  filters.enabled = "";
  search();
}

// ---- URL 状态 ----
// 本页签自己的参数：筛选、页码、打开的详情（detail，和因子页同名）。和别的页签共用同一个 query，只改自己的键；
// 页面在页签容器的 keep-alive 里，不可见时不写 URL，重新可见时按 URL 同步（URL 里没有就把内存状态写回去）。
const OWN_KEYS = ["keyword", "dataType", "enabled", "page", "detail"];
let pageActive = true;

function ownQuery(): Record<string, string> {
  const query: Record<string, string> = {};
  if (filters.keyword.trim()) query.keyword = filters.keyword.trim();
  if (filters.dataType) query.dataType = filters.dataType;
  if (filters.enabled) query.enabled = filters.enabled;
  if (pagination.current > 1) query.page = String(pagination.current);
  if (detailVisible.value && detailData.value?.task_id) query.detail = detailData.value.task_id;
  return query;
}

function syncListQuery() {
  if (!pageActive) return;
  const others = Object.fromEntries(Object.entries(route.query).filter(([key]) => !OWN_KEYS.includes(key)));
  const next = { ...others, ...ownQuery() };
  const current = Object.fromEntries(Object.entries(route.query).map(([key, value]) => [key, queryText(value)]));
  if (JSON.stringify(next, Object.keys(next).sort()) === JSON.stringify(current, Object.keys(current).sort())) return;
  void router.replace({ path: "/collector/tasks", query: next });
}

/** 按 URL 改筛选和页码，返回列表条件是否变了。 */
function readListQuery(): boolean {
  const next = {
    keyword: queryText(route.query.keyword),
    dataType: queryText(route.query.dataType),
    enabled: normalizeEnabledFilter(route.query.enabled),
    page: Math.max(1, Number.parseInt(queryText(route.query.page), 10) || 1)
  };
  const changed =
    next.keyword !== filters.keyword.trim() ||
    next.dataType !== filters.dataType ||
    next.enabled !== filters.enabled ||
    next.page !== pagination.current;
  Object.assign(filters, { keyword: next.keyword, dataType: next.dataType, enabled: next.enabled });
  pagination.current = next.page;
  return changed;
}

/** 按 URL 的 detail 开关详情抽屉；目标不在当前页时单独取。 */
function syncDetailFromQuery() {
  const id = queryText(route.query.detail);
  if (!id) {
    if (detailVisible.value) detailVisible.value = false;
    return;
  }
  if (detailVisible.value && detailData.value?.task_id === id) return;
  void openDetailById(id);
}

const onTasksTab = () => !route.query.tab || route.query.tab === "tasks";

watch(
  () => route.query,
  () => {
    if (!pageActive || !onTasksTab()) return;
    if (readListQuery()) void getTaskList();
    syncDetailFromQuery();
  }
);

// 首次挂载在页签容器的 keep-alive 里也会触发一次激活：初始状态已由 setup 和首次加载处理，这里跳过，
// 免得带 ?detail= 进来时列表还没回来就多取一次详情。之后每次切回来按 URL 同步，URL 里没有就写回。
let activatedOnce = false;
onActivated(() => {
  pageActive = true;
  if (!activatedOnce) {
    activatedOnce = true;
    return;
  }
  if (OWN_KEYS.some(key => route.query[key] !== undefined)) {
    if (readListQuery()) void getTaskList();
    syncDetailFromQuery();
  } else {
    syncListQuery();
  }
});
onDeactivated(() => {
  pageActive = false;
});

const paginationConfig = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  showTotal: true,
  showPageSize: true
}));

function selectEnabledFilter(): boolean | undefined {
  if (filters.enabled === "true") return true;
  if (filters.enabled === "false") return false;
  return undefined;
}

function onAdd() {
  editorRequest.value = { mode: "create" };
  editorVisible.value = true;
}

function onUpdate(record: CollectionTaskRecord) {
  editorRequest.value = { mode: "edit", record };
  editorVisible.value = true;
}

// 行操作“新建合成”：以这个任务作为源，打开新建表单。
function onAddResample(record: CollectionTaskRecord) {
  editorRequest.value = { mode: "resample", source: record };
  editorVisible.value = true;
}

// 标记最新一次列表请求：用户已离开的那页迟到的响应直接丢弃
let taskListGeneration = 0;

async function getTaskList() {
  const generation = ++taskListGeneration;
  const spaceId = selectedSpaceId.value;
  if (!spaceId) {
    taskList.value = [];
    pagination.total = 0;
    return;
  }
  loading.value = true;
  try {
    const response = await GetTaskList({
      space_id: spaceId,
      page: { page: pagination.current, size: pagination.pageSize },
      ...(filters.keyword.trim() ? { keyword: filters.keyword.trim() } : {}),
      ...(filters.dataType ? { data_type: filters.dataType } : {}),
      ...(selectEnabledFilter() === undefined ? {} : { enabled: selectEnabledFilter() })
    });
    if (generation !== taskListGeneration) return;
    taskList.value = (response.tasks || []).map(normalizeCollectionTask);
    pagination.total = Number(response.page_result?.total ?? taskList.value.length);
    if (pageActive && onTasksTab()) syncDetailFromQuery();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "获取任务列表失败");
  } finally {
    loading.value = false;
  }
}

function search() {
  pagination.current = 1;
  syncListQuery();
  void getTaskList();
}

function onPageChange(current: number) {
  pagination.current = current;
  syncListQuery();
  void getTaskList();
}

function onPageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize;
  pagination.current = 1;
  syncListQuery();
  void getTaskList();
}

// 启停会立刻影响数据采集，先确认再执行；执行期间该行操作保持加载态。
function confirmEnableChange(record: CollectionTaskRecord) {
  const enabled = !record.enabled;
  Modal.confirm({
    title: enabled ? "启用采集任务" : "禁用采集任务",
    content: enabled
      ? `启用「${record.task_name || record.task_id}」后将从下一采集周期恢复采集，确认启用？`
      : `禁用「${record.task_name || record.task_id}」后不再采集新周期数据，已采集数据与结果保留。确认禁用？`,
    okText: enabled ? "启用" : "禁用",
    cancelText: "取消",
    onOk: () => handleEnableChange(record, enabled)
  });
}

async function handleEnableChange(record: CollectionTaskRecord, enabled: boolean) {
  const spaceId = record.space_id || selectedSpaceId.value;
  if (!spaceId) {
    Message.error("请先选择空间");
    return;
  }
  togglingTaskId.value = record.task_id;
  try {
    if (!enabled) {
      await DisableTask({ space_id: spaceId, task_id: record.task_id });
    } else {
      await UpdateTask({
        space_id: spaceId,
        task_id: record.task_id,
        task: {
          task_id: record.task_id,
          task_name: record.task_name,
          description: record.description,
          enabled: true
        }
      });
    }
    Message.success("状态更新成功");
    await getTaskList();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "状态更新失败");
  } finally {
    togglingTaskId.value = "";
  }
}

function onViewDetails(record: CollectionTaskRecord) {
  detailData.value = record;
  detailVisible.value = true;
  syncListQuery();
}

let detailRequestToken = 0;
/** 按任务 ID 打开详情：先在当前页和空间的全部任务里找，找不到再单独取。 */
async function openDetailById(taskId: string) {
  const local =
    taskList.value.find(task => task.task_id === taskId) || catalog.sourceTasks.value.find(task => task.task_id === taskId);
  if (local) {
    onViewDetails(local);
    return;
  }
  const spaceId = selectedSpaceId.value;
  if (!spaceId) return;
  const token = ++detailRequestToken;
  try {
    const response = await GetTaskDetail({ space_id: spaceId, task_id: taskId });
    if (token !== detailRequestToken || spaceId !== selectedSpaceId.value) return;
    if (response.task) onViewDetails(normalizeCollectionTask(response.task));
  } catch (error) {
    // 取不到（已删除或不在当前空间）：去掉 URL 里的 detail；只提示网络错误，业务错误不另提示
    if (!(error instanceof RequestError && error.response)) reportError(error);
    if (token === detailRequestToken) syncListQuery();
  }
}

// 用户关掉详情时去掉 URL 里的 detail。不用 watch：切空间时程序关掉抽屉，不能在那时按旧 query 写回 URL
function onDetailCancel() {
  detailVisible.value = false;
  syncListQuery();
}

/** 详情抽屉底部的直达入口：去采集结果页签看这个任务的结果（去掉本页的 detail，其余参数保留）。 */
function openResultsFromDetail(record: CollectionTaskRecord) {
  detailVisible.value = false;
  const others = Object.fromEntries(Object.entries(route.query).filter(([key]) => key !== "detail"));
  void router.replace({ path: "/collector/tasks", query: { ...others, ...buildResultsQuery(record.task_id) } });
}

function editFromDetail(record: CollectionTaskRecord) {
  onDetailCancel();
  onUpdate(record);
}

function jobsFromDetail(record: CollectionTaskRecord) {
  onDetailCancel();
  openJobs(record);
}

// 详情里点「依赖它的合成任务」：打开该任务的详情（不在当前页时单独取）。
function locateTask(record: CollectionTaskRecord) {
  void openDetailById(record.task_id);
}

// 要删除的任务被哪些合成任务当作源：删除确认里列出来
const deleteDependents = computed(() => catalog.dependentsOf(deleteTarget.value));

function openDelete(record: CollectionTaskRecord) {
  deleteTarget.value = record;
  deleteResultData.value = false;
  deleteVisible.value = true;
}

async function handleDeleteOk(): Promise<boolean> {
  const record = deleteTarget.value;
  const spaceId = record?.space_id || selectedSpaceId.value;
  if (!record || !spaceId) {
    Message.error("请先选择空间");
    return false;
  }
  deleteLoading.value = true;
  try {
    await DeleteTask({
      space_id: spaceId,
      task_id: record.task_id,
      delete_result_data: deleteResultData.value
    });
    Message.success(deleteResultData.value ? "任务及结果数据已删除" : "任务已删除，结果数据已保留");
    deleteVisible.value = false;
    await getTaskList();
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除任务失败");
    return false;
  } finally {
    deleteLoading.value = false;
  }
}

function openJobs(record: CollectionTaskRecord) {
  jobsTarget.value = { taskId: record.task_id, taskName: record.task_name, targetFrequency: taskFrequency(record) };
  jobsVisible.value = true;
}

watch(selectedSpaceId, () => {
  catalog.resetForSpace();
  // 筛选、详情属于旧空间：只重置内存状态，URL 由页签容器统一清掉。
  filters.keyword = "";
  filters.dataType = "";
  filters.enabled = "";
  pagination.current = 1;
  detailRequestToken += 1;
  detailVisible.value = false;
  // 空间初始化完成或切换后重新加载；数据类型配置同样按空间授权，不能只在挂载时请求一次。
  if (selectedSpaceId.value) void catalog.loadDataTypeConfigs();
  void getTaskList();
  void catalog.loadSourceTasks();
  void catalog.loadTags();
});

onMounted(() => {
  // 空间尚未初始化时请求会被 Console 拒绝；初始化完成后由上面的空间监听补载。
  if (!selectedSpaceId.value) return;
  void getTaskList();
  void catalog.loadDataTypeConfigs();
  void catalog.loadSourceTasks();
  void catalog.loadTags();
});
</script>

<style scoped>
/* a-spin 默认 inline-block，会被宽表格撑开，导致右侧（含固定的操作列）被外层裁掉。 */
.moox-page :deep(.arco-spin) {
  display: block;
  width: 100%;
  min-width: 0;
}

.task-toolbar {
  margin-bottom: var(--moox-space-toolbar-table);
}

.moox-inner {
  min-height: 100%;
}

/* 「⋯」菜单里启停与删除的语义色 */
.action-warning {
  color: rgb(var(--orange-6));
}

.action-success {
  color: rgb(var(--green-6));
}

.action-danger {
  color: rgb(var(--red-6));
}

/* 「历史合成」：合成专属入口，与「新建合成」呼应，菜单里排在最后 */
.action-resample {
  color: rgb(var(--arcoblue-6));
}

.delete-options {
  margin-top: var(--moox-space-4);
}

.delete-dependents {
  margin: var(--moox-space-3) 0 0;
  color: var(--color-text-2);
}
</style>
