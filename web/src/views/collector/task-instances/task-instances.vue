<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <div class="task-toolbar">
          <a-space wrap class="task-filters">
            <a-input v-model="form.taskId" placeholder="目标任务 ID" allow-clear style="width: 190px" />
            <a-input v-model="form.datasetId" placeholder="目标 Dataset ID" allow-clear style="width: 210px" />
            <a-input v-model="form.functionName" placeholder="执行函数" allow-clear style="width: 200px" />
            <a-input v-model="form.symbol" placeholder="标的 ID" allow-clear style="width: 150px" />
            <a-select v-model="form.lastExecStatus" placeholder="Fetch 状态" style="width: 120px" allow-clear>
              <a-option :value="1">待执行</a-option>
              <a-option :value="2">成功</a-option>
              <a-option :value="3">失败</a-option>
            </a-select>
            <a-switch v-model="form.includeDeleted" :checked-text="'含删除'" :unchecked-text="'仅有效'" />
            <a-button type="primary" @click="search">
              <template #icon><icon-search /></template>
              查询
            </a-button>
          </a-space>
        </div>

        <a-table
          row-key="InstanceID"
          size="small"
          :data="instanceList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: 1600 }"
          :pagination="paginationConfig"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="实例 ID" data-index="InstanceID" :width="220">
              <template #cell="{ record }">
                <a-button class="task-id-button" type="text" @click="onViewDetails(record)">{{ record.InstanceID }}</a-button>
              </template>
            </a-table-column>
            <a-table-column title="Provider / 市场" :width="180">
              <template #cell="{ record }">{{ routeText(record) }}</template>
            </a-table-column>
            <a-table-column title="标的" data-index="SubjectID" :width="150">
              <template #cell="{ record }"
                ><a-tag color="arcoblue" size="small">{{ record.SubjectID || "-" }}</a-tag></template
              >
            </a-table-column>
            <a-table-column title="频率" data-index="Frequency" :width="90" />
            <a-table-column title="写入目标" :width="150" align="center">
              <template #cell="{ record }">
                <a-tooltip :content="targetSummaryTooltip(record.Targets)">
                  <a-tag :color="targetSummaryColor(record.Targets)" size="small">
                    {{ successfulTargetCount(record.Targets) }}/{{ record.Targets.length }} 成功
                  </a-tag>
                </a-tooltip>
              </template>
            </a-table-column>
            <a-table-column title="执行函数" data-index="FunctionName" :width="260">
              <template #cell="{ record }">
                <a-tooltip :content="record.FunctionName || '未分配'">
                  <span class="ellipsis-text">{{ record.FunctionName || "未分配" }}</span>
                </a-tooltip>
              </template>
            </a-table-column>
            <a-table-column title="Fetch 状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="getStatusColor(record.LastExecStatus)">{{
                  getStatusText(record.LastExecStatus)
                }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="数据类型" :width="100" align="center">
              <template #cell="{ record }"
                ><a-tag color="purple" size="small">{{ record.DataType || "-" }}</a-tag></template
              >
            </a-table-column>
            <a-table-column title="最后执行时间" :width="180">
              <template #cell="{ record }">{{ formatDateTime(record.LastExecTime) }}</template>
            </a-table-column>
            <a-table-column title="有效性" :width="80" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.IsDeleted ? 'red' : 'green'">{{ record.IsDeleted ? "无效" : "有效" }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="90" align="center" fixed="right">
              <template #cell="{ record }">
                <a-button type="primary" size="mini" @click="onViewDetails(record)"
                  ><template #icon><icon-eye /></template>查看</a-button
                >
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </a-spin>

    <a-modal v-model:visible="detailVisible" :footer="false" width="1100px">
      <template #title>任务实例详情</template>
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="实例 ID">{{ detailData.InstanceID }}</a-descriptions-item>
        <a-descriptions-item label="Run ID">{{ detailData.RunID || "-" }}</a-descriptions-item>
        <a-descriptions-item label="Provider">{{ detailData.Provider || "-" }}</a-descriptions-item>
        <a-descriptions-item label="市场类型">{{ detailData.MarketType || "-" }}</a-descriptions-item>
        <a-descriptions-item label="Source ID">{{ detailData.SourceID || "-" }}</a-descriptions-item>
        <a-descriptions-item label="数据类型">{{ detailData.DataType || "-" }}</a-descriptions-item>
        <a-descriptions-item label="标的 ID">{{ detailData.SubjectID || "-" }}</a-descriptions-item>
        <a-descriptions-item label="Provider Symbol">{{ detailData.ProviderSymbol || "-" }}</a-descriptions-item>
        <a-descriptions-item label="周期">{{ detailData.Frequency || "-" }}</a-descriptions-item>
        <a-descriptions-item label="目标数据时间">{{ formatDateTime(detailData.TargetDataTime) }}</a-descriptions-item>
        <a-descriptions-item label="Series Tag" :span="2"
          ><span class="mono-text">{{ detailData.SeriesTag || "-" }}</span></a-descriptions-item
        >
        <a-descriptions-item label="执行函数">{{ detailData.FunctionName || "未分配" }}</a-descriptions-item>
        <a-descriptions-item label="Fetch 状态">
          <a-tag :color="getStatusColor(detailData.LastExecStatus || 0)">{{
            getStatusText(detailData.LastExecStatus || 0)
          }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="Request Key" :span="2"
          ><span class="mono-text">{{ detailData.RequestKey || "-" }}</span></a-descriptions-item
        >
        <a-descriptions-item label="最后执行时间">{{ formatDateTime(detailData.LastExecTime) }}</a-descriptions-item>
        <a-descriptions-item label="修改时间">{{ formatDateTime(detailData.ModifyTime) }}</a-descriptions-item>
      </a-descriptions>

      <a-divider />
      <h3 class="target-title">写入目标明细</h3>
      <a-table
        :data="detailData.Targets || []"
        row-key="WriteTargetID"
        size="small"
        :pagination="false"
        :bordered="{ cell: true }"
        :scroll="{ x: 1050 }"
      >
        <template #columns>
          <a-table-column title="目标任务" :width="180">
            <template #cell="{ record: target }"
              ><span class="mono-text">{{ target.TaskID }}</span></template
            >
          </a-table-column>
          <a-table-column title="Dataset" :width="210">
            <template #cell="{ record: target }"
              ><span class="mono-text">{{ target.DatasetID }}</span></template
            >
          </a-table-column>
          <a-table-column title="View" :width="220">
            <template #cell="{ record: target }">
              <a-button type="text" size="mini" @click="openTargetResult(target)">{{ target.ViewID || "查看结果" }}</a-button>
            </template>
          </a-table-column>
          <a-table-column title="字段数" :width="80" align="center">
            <template #cell="{ record: target }">{{ target.OutputFields.length || "默认" }}</template>
          </a-table-column>
          <a-table-column title="写入状态" :width="110" align="center">
            <template #cell="{ record: target }"
              ><a-tag :color="targetStatusColor(target.Status)" size="small">{{
                targetStatusText(target.Status)
              }}</a-tag></template
            >
          </a-table-column>
          <a-table-column title="错误" :width="250">
            <template #cell="{ record: target }"
              ><span class="target-error">{{ target.LastError || "-" }}</span></template
            >
          </a-table-column>
        </template>
      </a-table>

      <a-divider />
      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="请求参数">
          <pre class="detail-json">{{ formatJSON(detailData.TaskParams || {}) }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="Fetch 结果">
          <pre class="detail-json">{{ formatJSON(detailData.Result || {}) }}</pre>
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRouter } from "vue-router";
import { storeToRefs } from "pinia";
import { callControl } from "@/api/admin/http";
import { useSpaceStore } from "@/store/modules/space";
import { taskInstancePaginationTotal, type TaskInstancePageResult } from "./task-instances-pagination";

interface TaskWriteTarget {
  WriteTargetID: string;
  TaskID: string;
  DatasetID: string;
  ViewID: string;
  OutputFields: string[];
  Status: string;
  LastError: string;
}

interface TaskInstance {
  InstanceID: string;
  RunID: string;
  RequestKey: string;
  Provider: string;
  ProviderSymbol: string;
  MarketType: string;
  DataType: string;
  SubjectID: string;
  Frequency: string;
  TargetDataTime: string;
  SourceID: string;
  SeriesTag: string;
  FunctionName: string;
  LastExecStatus: number;
  TaskParams: Record<string, any>;
  LastExecTime: string | null;
  Result: Record<string, any>;
  Targets: TaskWriteTarget[];
  IsDeleted: boolean;
  CreateTime: string;
  ModifyTime: string;
}

type RawTaskInstance = Partial<TaskInstance> & Record<string, any>;
type RawWriteTarget = Partial<TaskWriteTarget> & Record<string, any>;

const router = useRouter();
const loading = ref(false);
const instanceList = ref<TaskInstance[]>([]);
const detailVisible = ref(false);
const detailData = ref<Partial<TaskInstance>>({});
const form = ref({
  taskId: "",
  datasetId: "",
  functionName: "",
  symbol: "",
  lastExecStatus: null as number | null,
  includeDeleted: false
});
const pagination = ref({ current: 1, pageSize: 20, total: 0, showTotal: false, showPageSize: true });
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);

const paginationConfig = computed(() => ({
  ...pagination.value
}));

function normalizeObject(value: any): Record<string, any> {
  if (!value) return {};
  if (typeof value === "object") return value;
  try {
    return JSON.parse(String(value));
  } catch {
    return { raw: String(value) };
  }
}

function normalizeStringArray(value: unknown): string[] {
  if (Array.isArray(value)) return value.map(item => String(item)).filter(Boolean);
  return [];
}

function normalizeWriteTarget(raw: RawWriteTarget): TaskWriteTarget {
  return {
    WriteTargetID: raw.WriteTargetID ?? raw.write_target_id ?? raw.writeTargetId ?? "",
    TaskID: raw.TaskID ?? raw.task_id ?? raw.taskId ?? "",
    DatasetID: raw.DatasetID ?? raw.dataset_id ?? raw.datasetId ?? "",
    ViewID: raw.ViewID ?? raw.view_id ?? raw.viewId ?? "",
    OutputFields: normalizeStringArray(raw.OutputFields ?? raw.output_fields),
    Status: String(raw.Status ?? raw.status ?? "pending"),
    LastError: String(raw.LastError ?? raw.last_error ?? "")
  };
}

function normalizeTaskInstance(raw: RawTaskInstance): TaskInstance {
  const rawTargets = raw.Targets ?? raw.targets ?? [];
  return {
    InstanceID: raw.InstanceID ?? raw.instance_id ?? raw.InstanceId ?? raw.instanceId ?? "",
    RunID: raw.RunID ?? raw.run_id ?? raw.runId ?? "",
    RequestKey: raw.RequestKey ?? raw.request_key ?? raw.requestKey ?? "",
    Provider: raw.Provider ?? raw.provider ?? "",
    ProviderSymbol: raw.ProviderSymbol ?? raw.provider_symbol ?? "",
    MarketType: raw.MarketType ?? raw.market_type ?? "",
    DataType: raw.DataType ?? raw.data_type ?? "",
    SubjectID: raw.SubjectID ?? raw.subject_id ?? "",
    Frequency: raw.Frequency ?? raw.frequency ?? "",
    TargetDataTime: raw.TargetDataTime ?? raw.target_data_time ?? "",
    SourceID: raw.SourceID ?? raw.source_id ?? "",
    SeriesTag: raw.SeriesTag ?? raw.series_tag ?? "",
    FunctionName: raw.FunctionName ?? raw.function_name ?? "",
    LastExecStatus: Number(raw.LastExecStatus ?? raw.last_exec_status ?? 0),
    TaskParams: normalizeObject(raw.TaskParams ?? raw.task_params),
    LastExecTime: raw.LastExecTime ?? raw.last_exec_time ?? null,
    Result: normalizeObject(raw.Result ?? raw.result),
    Targets: Array.isArray(rawTargets) ? rawTargets.map(item => normalizeWriteTarget(item as RawWriteTarget)) : [],
    IsDeleted: Boolean(raw.IsDeleted ?? raw.is_deleted ?? false),
    CreateTime: raw.CreateTime ?? raw.create_time ?? "",
    ModifyTime: raw.ModifyTime ?? raw.modify_time ?? ""
  };
}

const getStatusColor = (status: number) => ({ 1: "gray", 2: "green", 3: "red" })[status] || "gray";
const getStatusText = (status: number) => ({ 1: "待执行", 2: "成功", 3: "失败" })[status] || "未知";

function normalizedTargetStatus(status: string) {
  return String(status || "pending")
    .trim()
    .toLowerCase();
}
function targetStatusColor(status: string) {
  const value = normalizedTargetStatus(status);
  if (["succeeded", "success", "completed"].includes(value)) return "green";
  if (["failed", "error"].includes(value)) return "red";
  if (["retrying", "retry", "pending_retry"].includes(value)) return "orange";
  return "gray";
}
function targetStatusText(status: string) {
  const value = normalizedTargetStatus(status);
  if (["succeeded", "success", "completed"].includes(value)) return "成功";
  if (["failed", "error"].includes(value)) return "失败";
  if (["retrying", "retry", "pending_retry"].includes(value)) return "重试中";
  return "待写入";
}
function successfulTargetCount(targets: TaskWriteTarget[]) {
  return targets.filter(target => targetStatusColor(target.Status) === "green").length;
}
function targetSummaryColor(targets: TaskWriteTarget[]) {
  if (!targets.length) return "gray";
  if (targets.some(target => targetStatusColor(target.Status) === "red")) return "red";
  if (successfulTargetCount(targets) === targets.length) return "green";
  return "orange";
}
function targetSummaryTooltip(targets: TaskWriteTarget[]) {
  if (!targets.length) return "暂无写入目标";
  return targets.map(target => `${target.TaskID}: ${targetStatusText(target.Status)}`).join("；");
}
function routeText(record: TaskInstance) {
  return [record.Provider, record.MarketType].filter(Boolean).join(" / ") || "-";
}
const formatJSON = (value: any) => JSON.stringify(normalizeObject(value), null, 2);

function formatDateTime(dateTime: string | null | undefined) {
  if (!dateTime) return "-";
  const date = new Date(dateTime);
  if (Number.isNaN(date.getTime())) return "-";
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function onPageChange(current: number) {
  pagination.value.current = current;
  void getInstanceList();
}
function onPageSizeChange(pageSize: number) {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  void getInstanceList();
}
function search() {
  pagination.value.current = 1;
  void getInstanceList();
}

async function getInstanceList() {
  const spaceId = selectedSpaceId.value || "";
  if (!spaceId) {
    instanceList.value = [];
    pagination.value.total = 0;
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const filter: Record<string, any> = {
      space_id: spaceId,
      page: { page: pagination.value.current, size: pagination.value.pageSize }
    };
    if (form.value.taskId) filter.task_id = form.value.taskId;
    if (form.value.datasetId) filter.dataset_id = form.value.datasetId;
    if (form.value.functionName) filter.function_name = form.value.functionName;
    if (form.value.symbol) filter.subject_id = form.value.symbol;
    if (form.value.lastExecStatus !== null) filter.last_exec_status = form.value.lastExecStatus;
    if (form.value.includeDeleted) filter.include_deleted = true;

    const data = await callControl<{ filter: typeof filter }, { instances?: RawTaskInstance[]; page?: TaskInstancePageResult }>(
      "collectmgr",
      "GetTaskInstanceList",
      { filter }
    );
    instanceList.value = (data.instances || []).map(normalizeTaskInstance);
    pagination.value.total = taskInstancePaginationTotal(
      pagination.value.current,
      pagination.value.pageSize,
      instanceList.value.length,
      data.page
    );
  } catch (error) {
    console.error("获取任务实例列表失败:", error);
    Message.error("获取任务实例列表失败");
  } finally {
    loading.value = false;
  }
}

function onViewDetails(record: TaskInstance) {
  detailData.value = record;
  detailVisible.value = true;
}
function openTargetResult(target: TaskWriteTarget) {
  detailVisible.value = false;
  void router.push({ path: "/collector/tasks", query: { tab: "results", resultTask: target.TaskID } });
}

watch(selectedSpaceId, () => void getInstanceList());
onMounted(() => void getInstanceList());
</script>

<style scoped>
.moox-page :deep(.arco-spin) {
  display: block;
  width: 100%;
  min-width: 0;
}
.task-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}
.task-filters {
  flex: 1 1 auto;
}
.task-id-button {
  display: block;
  width: 100%;
  max-width: 100%;
  height: auto;
  overflow: hidden;
  padding: 0;
  color: #165dff;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ellipsis-text {
  display: block;
  width: 100%;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mono-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  word-break: break-all;
}
.target-title {
  margin: 0 0 var(--moox-space-2);
  font-size: 14px;
}
.target-error {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.detail-json {
  margin: 0;
  max-height: 200px;
  overflow: auto;
  padding: var(--moox-space-3);
  border-radius: 4px;
  background: #f5f5f5;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
  word-wrap: break-word;
}
</style>
