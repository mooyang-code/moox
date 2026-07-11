<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <div class="page-head">
          <h2>任务实例</h2>
        </div>

        <section class="task-query-panel">
          <a-space wrap class="task-filter-row">
            <a-input v-model="form.taskId" placeholder="请输入任务ID" allow-clear style="width: 200px" />
            <a-input v-model="form.ruleId" placeholder="请输入规则ID" allow-clear style="width: 200px" />
            <a-input v-model="form.plannedExecNode" placeholder="计划节点" allow-clear style="width: 150px" />
            <a-input v-model="form.lastExecNode" placeholder="最后执行节点" allow-clear style="width: 150px" />
            <a-input v-model="form.symbol" placeholder="请输入交易标的" allow-clear style="width: 150px" />
            <a-select placeholder="执行状态" v-model="form.lastExecStatus" style="width: 120px" allow-clear>
              <a-option :value="1">待执行</a-option>
              <a-option :value="2">执行中</a-option>
              <a-option :value="3">成功</a-option>
              <a-option :value="4">部分失败</a-option>
              <a-option :value="5">失败</a-option>
            </a-select>
            <a-switch v-model="form.includeDeleted" :checked-text="'含删除'" :unchecked-text="'仅有效'" />
            <a-button type="primary" @click="search">
              <template #icon><icon-search /></template>
              <span>查询</span>
            </a-button>
            <a-button @click="reset">
              <template #icon><icon-refresh /></template>
              <span>重置</span>
            </a-button>
          </a-space>
        </section>

        <section class="task-result-pane">
          <a-table
            row-key="TaskID"
            size="small"
            :data="instanceList"
            :bordered="{ cell: true }"
            :loading="loading"
            :scroll="{ x: 1810, y: 500 }"
            :pagination="paginationConfig"
            :row-selection="{ type: 'checkbox', showCheckedAll: true }"
            :selected-keys="selectedKeys"
            @select="select"
            @select-all="selectAll"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #columns>
              <a-table-column title="任务ID" data-index="TaskID" :width="200">
                <template #cell="{ record }">
                  <a-link @click="onViewDetails(record)">{{ record.TaskID }}</a-link>
                </template>
              </a-table-column>
              <a-table-column title="规则ID" data-index="RuleID" :width="190">
                <template #cell="{ record }">
                  <a-tooltip :content="record.RuleID">
                    <span class="ellipsis-text">{{ record.RuleID }}</span>
                  </a-tooltip>
                </template>
              </a-table-column>
              <a-table-column title="交易标的" data-index="Symbol" :width="180">
                <template #cell="{ record }">
                  <a-tag color="arcoblue" size="small">{{ record.Symbol }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="市场" data-index="Market" :width="150" />
              <a-table-column title="工具类型" data-index="InstrumentType" :width="110" />
              <a-table-column title="统一数据集" data-index="DatasetID" :width="150" />
              <a-table-column title="周期" data-index="Interval" :width="90" />
              <a-table-column title="计划节点" data-index="PlannedExecNode" :width="160">
                <template #cell="{ record }">
                  <a-tooltip :content="record.PlannedExecNode">
                    <span class="ellipsis-text">{{ record.PlannedExecNode }}</span>
                  </a-tooltip>
                </template>
              </a-table-column>
              <a-table-column title="最后执行节点" data-index="LastExecNode" :width="300">
                <template #cell="{ record }">
                  <a-tooltip :content="getLastExecNode(record)">
                    <span class="ellipsis-text">{{ getLastExecNode(record) }}</span>
                  </a-tooltip>
                </template>
              </a-table-column>
              <a-table-column title="执行状态" :width="100" align="center">
                <template #cell="{ record }">
                  <a-tag bordered size="small" :color="getStatusColor(record.LastExecStatus)">
                    {{ getStatusText(record.LastExecStatus) }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="有效性" :width="80" align="center">
                <template #cell="{ record }">
                  <a-tag bordered size="small" :color="record.IsDeleted ? 'red' : 'green'">
                    {{ record.IsDeleted ? "无效" : "有效" }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="数据类型" :width="100" align="center">
                <template #cell="{ record }">
                  <a-tag color="purple" size="small">{{ record.DataType || "-" }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="最后执行时间" :width="180">
                <template #cell="{ record }">
                  {{ formatDateTime(record.LastExecTime) }}
                </template>
              </a-table-column>
              <a-table-column title="任务创建时间" :width="180">
                <template #cell="{ record }">
                  {{ formatDateTime(record.CreateTime) }}
                </template>
              </a-table-column>
              <a-table-column title="操作" :width="100" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-space>
                    <a-button type="text" size="mini" @click="onViewDetails(record)">查看</a-button>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </section>
      </div>
    </a-spin>

    <!-- 详情模态框 -->
    <a-modal v-model:visible="detailVisible" :footer="false" width="900px">
      <template #title>任务实例详情</template>
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="任务ID">{{ detailData.TaskID }}</a-descriptions-item>
        <a-descriptions-item label="规则ID">{{ detailData.RuleID }}</a-descriptions-item>
        <a-descriptions-item label="市场">{{ detailData.Market || "-" }}</a-descriptions-item>
        <a-descriptions-item label="工具类型">{{ detailData.InstrumentType || "-" }}</a-descriptions-item>
        <a-descriptions-item label="数据类型">{{ detailData.DataType || "-" }}</a-descriptions-item>
        <a-descriptions-item label="周期">{{ detailData.Interval || "-" }}</a-descriptions-item>
        <a-descriptions-item label="数据集">{{ detailData.DatasetID || "-" }}</a-descriptions-item>
        <a-descriptions-item label="标的ID">{{ detailData.SubjectID || "-" }}</a-descriptions-item>
        <a-descriptions-item label="计划节点">{{ detailData.PlannedExecNode }}</a-descriptions-item>
        <a-descriptions-item label="最后执行节点">{{ getLastExecNode(detailData) }}</a-descriptions-item>
        <a-descriptions-item label="交易标的">
          <a-tag color="arcoblue">{{ detailData.Symbol }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="执行状态">
          <a-tag :color="getStatusColor(detailData.LastExecStatus || 0)">
            {{ getStatusText(detailData.LastExecStatus || 0) }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="有效性">
          <a-tag :color="detailData.IsDeleted ? 'red' : 'green'">
            {{ detailData.IsDeleted ? "无效" : "有效" }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="最后执行时间">{{ formatDateTime(detailData.LastExecTime) }}</a-descriptions-item>
        <a-descriptions-item label="任务创建时间">{{ formatDateTime(detailData.CreateTime) }}</a-descriptions-item>
        <a-descriptions-item label="修改时间">{{ formatDateTime(detailData.ModifyTime) }}</a-descriptions-item>
      </a-descriptions>

      <a-divider />

      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="任务参数">
          <pre class="detail-json">{{ formatJSON(detailData.TaskParams || {}) }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="执行结果">
          <pre class="detail-json">{{ formatJSON(detailData.Result || {}) }}</pre>
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { callControl } from "@/api/admin/http";
import { useSpaceStore } from "@/store/modules/space";
import { storeToRefs } from "pinia";

interface TaskInstance {
  TaskID: string;
  RuleID: string;
  Exchange: string;
  Market: string;
  DataType: string;
  DatasetID: string;
  SubjectID: string;
  Symbol: string;
  Interval: string;
  InstrumentType: string;
  PlannedExecNode: string; // v2.0: 计划执行节点
  LastExecNode: string; // v2.0: 最后执行节点
  LastExecStatus: number; // v2.0: 最后执行状态
  TaskParams: Record<string, any>;
  LastExecTime: string | null;
  Result: Record<string, any>;
  IsDeleted: boolean;
  CreateTime: string;
  ModifyTime: string;
}

type RawTaskInstance = Partial<TaskInstance> & Record<string, any>;

type TaskInstanceRecord = Partial<TaskInstance>;

const loading = ref(false);
const instanceList = ref<TaskInstance[]>([]);
const selectedKeys = ref<string[]>([]);
const detailVisible = ref(false);
const detailData = ref<Partial<TaskInstance>>({});

const form = ref({
  taskId: "",
  ruleId: "",
  plannedExecNode: "", // v2.0: 计划节点
  lastExecNode: "", // v2.0: 执行节点
  symbol: "",
  lastExecStatus: null as number | null, // v2.0: 执行状态
  includeDeleted: false
});

const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showPageSize: true
});

// Get Space store
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);

const paginationConfig = computed(() => ({
  ...pagination.value,
  onChange: (current: number) => {
    pagination.value.current = current;
    getInstanceList();
  },
  onPageSizeChange: (pageSize: number) => {
    pagination.value.pageSize = pageSize;
    pagination.value.current = 1;
    getInstanceList();
  }
}));

const getStatusColor = (status: number) => {
  const colors: { [key: number]: string } = {
    1: "gray", // 待执行
    2: "blue", // 执行中
    3: "green", // 成功
    4: "orange", // 部分失败
    5: "red" // 失败
  };
  return colors[status] || "gray";
};

const getStatusText = (status: number) => {
  const texts: { [key: number]: string } = {
    1: "待执行",
    2: "执行中",
    3: "成功",
    4: "部分失败",
    5: "失败"
  };
  return texts[status] || "未知";
};

const getLastExecNode = (record: TaskInstanceRecord) => {
  if (!record) return "-";
  return record.LastExecNode || "-";
};

const normalizeTaskInstance = (raw: RawTaskInstance): TaskInstance => {
  const lastExecStatus = raw.LastExecStatus ?? raw.last_exec_status ?? 0;
  const params = normalizeObject(raw.TaskParams ?? raw.task_params);
  return {
    TaskID: raw.TaskID ?? raw.task_id ?? "",
    RuleID: raw.RuleID ?? raw.rule_id ?? "",
    Exchange: raw.Exchange ?? raw.exchange ?? "",
    Market: raw.Market ?? raw.market ?? params.market_id ?? "",
    DataType: raw.DataType ?? raw.data_type ?? "",
    DatasetID: raw.DatasetID ?? raw.dataset_id ?? params.unified_dataset_id ?? "",
    SubjectID: raw.SubjectID ?? raw.subject_id ?? "",
    Symbol: raw.Symbol ?? raw.symbol ?? "",
    Interval: raw.Interval ?? raw.interval ?? params.frequency ?? "",
    InstrumentType: raw.InstrumentType ?? raw.instrument_type ?? params.instrument_type ?? "",
    PlannedExecNode: raw.PlannedExecNode ?? raw.planned_exec_node ?? "",
    LastExecNode: raw.LastExecNode ?? raw.last_exec_node ?? "",
    LastExecStatus: Number(lastExecStatus),
    TaskParams: params,
    LastExecTime: raw.LastExecTime ?? raw.last_exec_time ?? null,
    Result: normalizeObject(raw.Result ?? raw.result),
    IsDeleted: Boolean(raw.IsDeleted ?? raw.is_deleted ?? false),
    CreateTime: raw.CreateTime ?? raw.create_time ?? "",
    ModifyTime: raw.ModifyTime ?? raw.modify_time ?? ""
  };
};

const normalizeObject = (value: any): Record<string, any> => {
  if (!value) return {};
  if (typeof value === "object") return value;
  try {
    return JSON.parse(String(value));
  } catch {
    return { raw: String(value) };
  }
};

const formatJSON = (value: any) => JSON.stringify(normalizeObject(value), null, 2);

// 格式化时间为本地时间格式
const formatDateTime = (dateTime: string | null | undefined) => {
  if (!dateTime) return "-";

  try {
    const date = new Date(dateTime);
    // 检查日期是否有效
    if (isNaN(date.getTime())) {
      return "-";
    }

    // 格式化日期为 YYYY-MM-DD HH:mm:ss
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    const hours = String(date.getHours()).padStart(2, "0");
    const minutes = String(date.getMinutes()).padStart(2, "0");
    const seconds = String(date.getSeconds()).padStart(2, "0");

    return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`;
  } catch {
    return "-";
  }
};

const select = (list: string[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? instanceList.value.map(el => el.TaskID) : [];
};

const onPageChange = (current: number) => {
  pagination.value.current = current;
  getInstanceList();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  getInstanceList();
};

const search = () => {
  pagination.value.current = 1;
  getInstanceList();
};

const reset = () => {
  form.value = {
    taskId: "",
    ruleId: "",
    plannedExecNode: "", // v2.0: 计划节点
    lastExecNode: "", // v2.0: 执行节点
    symbol: "",
    lastExecStatus: null, // v2.0: 执行状态
    includeDeleted: false
  };
  getInstanceList();
};

const getInstanceList = async () => {
  const spaceId = selectedSpaceId.value || "";
  if (!spaceId) {
    instanceList.value = [];
    pagination.value.total = 0;
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const filter: any = {
      space_id: spaceId,
      page: {
        page: pagination.value.current,
        size: pagination.value.pageSize
      }
    };

    if (form.value.taskId) filter.task_id = form.value.taskId;
    if (form.value.ruleId) filter.rule_id = form.value.ruleId;
    if (form.value.plannedExecNode) filter.planned_exec_node = form.value.plannedExecNode;
    if (form.value.lastExecNode) filter.last_exec_node = form.value.lastExecNode;
    if (form.value.symbol) filter.symbol = form.value.symbol;
    if (form.value.lastExecStatus !== null) filter.last_exec_status = form.value.lastExecStatus;
    if (form.value.includeDeleted) filter.include_deleted = true;

    const data = await callControl<{ filter: typeof filter }, { instances?: RawTaskInstance[]; page?: { total?: number } }>(
      "collectmgr",
      "GetTaskInstanceList",
      { filter }
    );
    instanceList.value = (data.instances || []).map(normalizeTaskInstance);
    pagination.value.total = Number(data.page?.total) || (data.instances ? data.instances.length : 0);
  } catch (error) {
    console.error("获取任务实例列表失败:", error);
    Message.error("获取任务实例列表失败");
  } finally {
    loading.value = false;
  }
};

const onViewDetails = (record: TaskInstance) => {
  detailData.value = record;
  detailVisible.value = true;
};

// Watch for Space changes
watch(selectedSpaceId, () => {
  getInstanceList();
});

onMounted(() => {
  getInstanceList();
});
</script>

<style scoped>
.moox-page :deep(.arco-spin) {
  display: block;
  width: 100%;
  min-width: 0;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.task-query-panel {
  margin-bottom: 12px;
}

.task-filter-row {
  width: 100%;
}

.task-result-pane {
  width: 100%;
  min-width: 0;
  max-width: 100%;
  box-sizing: border-box;
  padding: 12px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.task-result-pane :deep(.arco-pagination) {
  margin-top: 12px;
}

.ellipsis-text {
  display: block;
  width: 100%;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.params-preview {
  margin: 0;
  font-family: monospace;
  font-size: 12px;
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
  max-width: 400px;
  max-height: 300px;
  overflow: auto;
  white-space: pre-wrap;
  word-wrap: break-word;
}

.detail-json {
  margin: 0;
  font-family: monospace;
  font-size: 12px;
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
  max-height: 200px;
  overflow: auto;
  white-space: pre-wrap;
  word-wrap: break-word;
}
</style>
