<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space class="rule-toolbar" wrap>
          <a-button type="primary" status="success" @click="onAdd">
            <template #icon><icon-plus /></template>
            <span>新建任务</span>
          </a-button>
          <a-input v-model="form.ruleId" placeholder="请输入规则ID" allow-clear />
          <a-select v-model="form.dataType" placeholder="请选择数据类型" style="width: 150px" allow-clear>
            <a-option v-for="config in dataTypeConfigs" :key="config.data_type" :value="config.data_type">
              {{ config.type_name }}
            </a-option>
          </a-select>
          <a-select v-model="form.dataSource" placeholder="请选择数据源" style="width: 150px" allow-clear>
            <a-option v-for="source in getSearchDataSourceOptions()" :key="source.value" :value="source.value">
              {{ source.label }}
            </a-option>
          </a-select>
          <a-button type="primary" @click="search">
            <template #icon><icon-search /></template>
            <span>查询</span>
          </a-button>
          <a-switch v-model="form.enabled" :checked-text="'启用'" :unchecked-text="'禁用'" @change="onEnabledChange" />
        </a-space>

        <a-table
          row-key="rule_id"
          size="small"
          :data="taskList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: '100%', y: '100%', minWidth: 1000 }"
          :pagination="paginationConfig"
          :row-selection="{ type: 'checkbox', showCheckedAll: true }"
          :selected-keys="selectedKeys"
          @select="select"
          @select-all="selectAll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="规则ID" data-index="rule_id" :width="150">
              <template #cell="{ record }">
                <a-link @click="onViewDetails(record)">{{ record.rule_id }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="数据类型" data-index="data_type" :width="120"></a-table-column>
            <a-table-column title="数据源" data-index="data_source" :width="120"></a-table-column>
            <a-table-column title="创建时间" :width="160">
              <template #cell="{ record }">
                {{ formatDateTime(record.create_time) }}
              </template>
            </a-table-column>
            <a-table-column title="修改时间" :width="160">
              <template #cell="{ record }">
                {{ formatDateTime(record.modify_time) }}
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="180" align="center" :fixed="'right'">
              <template #cell="{ record }">
                <a-space>
                  <a-button
                    :type="record.enabled === 'true' ? 'outline' : 'primary'"
                    :status="record.enabled === 'true' ? 'warning' : 'success'"
                    size="mini"
                    @click="handleEnableChange(record, record.enabled !== 'true')"
                  >
                    <template #icon>
                      <icon-close-circle v-if="record.enabled === 'true'" />
                      <icon-check-circle v-else />
                    </template>
                    <span>{{ record.enabled === "true" ? "禁用" : "启用" }}</span>
                  </a-button>
                  <a-button type="primary" size="mini" @click="onUpdate(record)">
                    <template #icon><icon-edit /></template>
                    <span>修改</span>
                  </a-button>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </a-spin>

    <!-- 新增/修改模态框 -->
    <a-modal
      v-model:visible="open"
      @close="afterClose"
      @cancel="afterClose"
      width="760px"
      :ok-loading="submitLoading"
      @before-ok="handleOk"
    >
      <template #title> {{ title }} </template>
      <a-form ref="formRef" auto-label-width :rules="rules" :model="addForm" layout="vertical">
        <a-row :gutter="16">
          <a-col v-if="title === '修改采集规则'" :span="12">
            <a-form-item field="rule_id" label="规则ID" validate-trigger="blur">
              <a-input v-model="addForm.rule_id" placeholder="留空自动生成" allow-clear :disabled="true" />
            </a-form-item>
          </a-col>
          <a-col :span="title === '修改采集规则' ? 12 : 24">
            <a-form-item field="data_type" label="数据类型" validate-trigger="blur">
              <a-select v-model="addForm.data_type" placeholder="请选择数据类型" @change="onDataTypeChange">
                <a-option v-for="config in dataTypeConfigs" :key="config.data_type" :value="config.data_type">
                  {{ config.type_name }}
                </a-option>
              </a-select>
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item field="data_source" label="数据源" validate-trigger="blur">
          <a-select v-model="addForm.data_source" placeholder="请选择数据源" :loading="loadingDataSources" allow-clear>
            <a-option v-for="source in dataSourceOptions" :key="source.value" :value="source.value">
              {{ source.label }}
            </a-option>
          </a-select>
        </a-form-item>

        <a-form-item label="产品类型" required>
          <a-radio-group v-model="marketValue" type="button">
            <a-radio value="spot">现货</a-radio>
            <a-radio value="swap">永续合约</a-radio>
          </a-radio-group>
        </a-form-item>

        <a-form-item label="Dataset" required>
          <a-select v-model="datasetIdValue" placeholder="请选择已激活的 Dataset" :loading="loadingDatasets" allow-search>
            <a-option v-for="dataset in availableDatasets" :key="dataset.dataset_id" :value="dataset.dataset_id">
              {{ dataset.name || dataset.dataset_id }} ({{ dataset.dataset_id }})
            </a-option>
          </a-select>
        </a-form-item>

        <a-form-item v-if="addForm.data_type === 'kline'" label="K线周期" required>
          <a-checkbox-group v-model="intervalsValue" :options="INTERVAL_OPTIONS"> </a-checkbox-group>
        </a-form-item>

        <a-form-item label="采集频率" required>
          <a-input v-model="scheduleIntervalValue" placeholder="例如 5m、1h、24h" allow-clear />
        </a-form-item>

        <a-form-item label="创建人">
          <a-input v-model="addForm.creator" readonly />
        </a-form-item>

        <a-form-item field="enabled" label="启用状态">
          <a-select v-model="addForm.enabled">
            <a-option value="true">启用</a-option>
            <a-option value="false">禁用</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 详情模态框 -->
    <a-modal v-model:visible="detailVisible" :footer="false" width="800px">
      <template #title>任务配置详情</template>
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="规则ID">{{ detailData.rule_id }}</a-descriptions-item>
        <a-descriptions-item label="数据类型">{{ detailData.data_type }}</a-descriptions-item>
        <a-descriptions-item label="数据源">{{ detailData.data_source }}</a-descriptions-item>
        <a-descriptions-item label="启用状态">
          <a-tag :color="detailData.enabled === 'true' ? 'green' : 'red'">
            {{ detailData.enabled === "true" ? "启用" : "禁用" }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="创建人">{{ detailData.creator || "-" }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(detailData.create_time || "") }}</a-descriptions-item>
        <a-descriptions-item label="修改时间">{{ formatDateTime(detailData.modify_time || "") }}</a-descriptions-item>
      </a-descriptions>

      <a-divider />

      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="采集参数">
          <pre>{{ formatJSON(detailData.collect_params || "") }}</pre>
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { callControl } from "@/api/admin/http";
import { listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { useUserInfoStore } from "@/store/modules/user-info";
import { storeToRefs } from "pinia";
import { buildCollectorRuleParams, type CollectorRuleInput } from "./collector-rule-params";

interface TaskConfig {
  id?: number;
  rule_id: string;
  space_id: string;
  data_type: string;
  data_source: string;
  collect_params: string;
  enabled: string;
  creator: string;
  create_time: string;
  modify_time: string;
}

interface DataTypeConfig {
  id: number;
  data_type: string;
  type_name: string;
  type_desc: string;
  data_source_options: Record<string, any>;
  sort_order: number;
  version: number;
  create_time: string;
  modify_time: string;
}

const loading = ref(false);
const submitLoading = ref(false);
const taskList = ref<TaskConfig[]>([]);
const selectedKeys = ref<string[]>([]);
const open = ref(false);
const title = ref("新建任务");
const formRef = ref();
const detailVisible = ref(false);
const detailData = ref<Partial<TaskConfig>>({});

const dataTypeConfigs = ref<DataTypeConfig[]>([]);
const marketValue = ref<CollectorRuleInput["market"]>("spot");
const datasetIdValue = ref("");
const intervalsValue = ref<string[]>([]);
const scheduleIntervalValue = ref("");

const dataSourceOptions = ref<{ label: string; value: string }[]>([]);
const loadingDataSources = ref(false);
const activeDatasets = ref<Dataset[]>([]);
const loadingDatasets = ref(false);

// Get Space store
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);

// Get user info store
const userInfoStore = useUserInfoStore();
const { account } = storeToRefs(userInfoStore);

const form = ref({
  ruleId: "",
  dataType: "",
  dataSource: "",
  enabled: true
});

const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showPageSize: true
});

const paginationConfig = computed(() => ({
  ...pagination.value,
  onChange: (current: number) => {
    pagination.value.current = current;
    getTaskList();
  },
  onPageSizeChange: (pageSize: number) => {
    pagination.value.pageSize = pageSize;
    pagination.value.current = 1;
    getTaskList();
  }
}));

const addForm = ref({
  rule_id: "",
  space_id: "",
  data_type: "",
  data_source: "",
  collect_params: "{}",
  enabled: "true",
  creator: ""
});

const availableDatasets = computed(() => {
  if (!addForm.value.data_source) {
    return activeDatasets.value;
  }
  const matching = activeDatasets.value.filter(dataset => dataset.data_source_id === addForm.value.data_source);
  return matching.length > 0 ? matching : activeDatasets.value;
});

const rules = {
  data_type: [{ required: true, message: "请选择数据类型" }],
  data_source: [{ required: true, message: "请选择数据源" }]
};

// K线周期选项（常量，避免每次渲染重新创建）
const INTERVAL_OPTIONS = [
  { label: "1分钟", value: "1m" },
  { label: "3分钟", value: "3m" },
  { label: "5分钟", value: "5m" },
  { label: "15分钟", value: "15m" },
  { label: "30分钟", value: "30m" },
  { label: "1小时", value: "1h" },
  { label: "2小时", value: "2h" },
  { label: "4小时", value: "4h" },
  { label: "6小时", value: "6h" },
  { label: "12小时", value: "12h" },
  { label: "1天", value: "1d" },
  { label: "1周", value: "1w" },
  { label: "1月", value: "1M" }
];

// 获取数据源标签
const getDataSourceLabel = (value: string) => {
  const labels: { [key: string]: string } = {
    binance: "币安 (Binance)",
    okx: "OKX",
    huobi: "火币 (Huobi)",
    bybit: "Bybit",
    bitget: "Bitget",
    kucoin: "KuCoin",
    gate: "Gate.io",
    mexc: "MEXC",
    bitfinex: "Bitfinex",
    coinbase: "Coinbase",
    cryptonews: "CryptoNews",
    coindesk: "CoinDesk",
    cointelegraph: "Cointelegraph",
    decrypt: "Decrypt",
    theblock: "The Block",
    messari: "Messari",
    glassnode: "Glassnode",
    intoblock: "IntoTheBlock"
  };
  return labels[value] || value;
};

const toDataSourceOptions = (options: Array<{ label?: string; value?: string }>) =>
  options
    .filter(option => option.value)
    .map(option => ({
      label: option.label || getDataSourceLabel(option.value as string),
      value: option.value as string
    }));

const normalizeObject = (value: any): Record<string, any> => {
  if (!value) return {};
  if (typeof value === "object") return value;
  try {
    const parsed = JSON.parse(String(value));
    return parsed && typeof parsed === "object" ? parsed : { value: parsed };
  } catch {
    return { raw: String(value) };
  }
};

const normalizeTaskConfig = (raw: any): TaskConfig => ({
  id: raw.id,
  rule_id: raw.rule_id || "",
  space_id: raw.space_id || "",
  data_type: raw.data_type || "",
  data_source: raw.exchange || raw.data_source || "",
  collect_params: JSON.stringify(normalizeObject(raw.collect_params)),
  enabled: (raw.enabled ?? true) ? "true" : "false",
  creator: raw.creator || "",
  create_time: raw.create_time || "",
  modify_time: raw.modify_time || ""
});

const dataSourceOptionsFromConfig = (value: any) => {
  const config = normalizeObject(value);
  if (config.options && Array.isArray(config.options)) {
    return toDataSourceOptions(
      config.options.map((option: any) => ({
        label: option.label || getDataSourceLabel(option.value),
        value: option.value
      }))
    );
  }
  if (Array.isArray(value)) {
    return toDataSourceOptions(
      value.map((source: string) => ({
        label: getDataSourceLabel(source),
        value: source
      }))
    );
  }
  return [];
};

// 获取搜索用的数据源选项
const getSearchDataSourceOptions = () => {
  if (!form.value.dataType) {
    return [];
  }

  // 查找选中的数据类型配置
  const selectedConfig = dataTypeConfigs.value.find(config => config.data_type === form.value.dataType);
  if (!selectedConfig || !selectedConfig.data_source_options) {
    return [];
  }

  return dataSourceOptionsFromConfig(selectedConfig.data_source_options);
};

const formatJSON = (value: any) => JSON.stringify(normalizeObject(value), null, 2);

// 格式化时间为本地时间格式
const formatDateTime = (dateTime: string) => {
  if (!dateTime) return "-";

  try {
    const date = new Date(dateTime);
    // 检查日期是否有效
    if (isNaN(date.getTime())) {
      return dateTime; // 如果转换失败，返回原始值
    }

    // 格式化日期为 YYYY-MM-DD HH:mm:ss
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    const hours = String(date.getHours()).padStart(2, "0");
    const minutes = String(date.getMinutes()).padStart(2, "0");
    const seconds = String(date.getSeconds()).padStart(2, "0");

    return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`;
  } catch (error) {
    console.error("格式化时间失败:", error);
    return dateTime;
  }
};

const select = (list: string[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? taskList.value.map(el => el.rule_id) : [];
};

const onPageChange = (current: number) => {
  pagination.value.current = current;
  getTaskList();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  getTaskList();
};

const search = () => {
  pagination.value.current = 1;
  getTaskList();
};

const onEnabledChange = () => {
  // 当启用状态开关变化时，重新查询列表
  search();
};

const getTaskList = async () => {
  const spaceId = selectedSpaceId.value || "";
  if (!spaceId) {
    taskList.value = [];
    pagination.value.total = 0;
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const params: any = {
      space_id: spaceId,
      page: {
        page: pagination.value.current,
        size: pagination.value.pageSize
      }
    };

    if (form.value.ruleId) params.rule_id = form.value.ruleId;
    if (form.value.dataType) params.data_type = form.value.dataType;
    if (form.value.dataSource) params.exchange = form.value.dataSource;
    if (form.value.enabled !== null) params.enabled = form.value.enabled;

    const data = await callControl<typeof params, { rules?: any[]; page?: { total?: number } }>(
      "collectmgr",
      "GetTaskRuleList",
      params
    );
    taskList.value = (data.rules || []).map(normalizeTaskConfig);
    pagination.value.total = Number(data.page?.total) || (data.rules ? data.rules.length : 0);
  } catch (error) {
    console.error("获取任务列表失败:", error);
    Message.error("获取任务列表失败");
  } finally {
    loading.value = false;
  }
};

// 获取数据类型配置
const getDataTypeConfigs = async () => {
  try {
    const data = await callControl<Record<string, never>, { configs?: DataTypeConfig[] }>("collectmgr", "GetDataTypeConfigs", {});
    dataTypeConfigs.value = data.configs || [];
  } catch (error) {
    console.error("获取数据类型配置失败:", error);
    Message.error("获取数据类型配置失败");
  }
};

const loadDataSourceOptionsForType = (dataType: string) => {
  loadingDataSources.value = true;
  const config = dataTypeConfigs.value.find(item => item.data_type === dataType);
  dataSourceOptions.value = config ? dataSourceOptionsFromConfig(config.data_source_options) : [];
  loadingDataSources.value = false;
};

const loadActiveDatasets = async () => {
  const spaceId = selectedSpaceId.value || "";
  if (!spaceId) {
    activeDatasets.value = [];
    return;
  }
  loadingDatasets.value = true;
  try {
    const datasets: Dataset[] = [];
    for (let page = 1; ; page += 1) {
      const response = await listDatasets({
        space_id: spaceId,
        status: "active",
        page: { page, size: 500 }
      });
      datasets.push(...(response.datasets || []).filter(dataset => dataset.status === "active"));
      if (!response.page_result?.has_more || (response.datasets || []).length === 0) break;
    }
    activeDatasets.value = datasets;
  } catch (error) {
    console.error("获取 Dataset 失败:", error);
    activeDatasets.value = [];
    Message.error("获取 Dataset 失败");
  } finally {
    loadingDatasets.value = false;
  }
};

const resetRuleFields = () => {
  marketValue.value = "spot";
  datasetIdValue.value = "";
  intervalsValue.value = [];
  scheduleIntervalValue.value = "";
};

const isRecord = (value: unknown): value is Record<string, any> =>
  Boolean(value) && typeof value === "object" && !Array.isArray(value);

const parseStrictRuleParams = (raw: string): CollectorRuleInput => {
  const params: unknown = JSON.parse(raw || "{}");
  if (!isRecord(params) || !isRecord(params.source) || !isRecord(params.collector)) {
    throw new Error("规则参数不是当前支持的结构");
  }
  if (!isRecord(params.target) || !isRecord(params.schedule)) {
    throw new Error("规则参数不是当前支持的结构");
  }

  const source = params.source;
  const collector = params.collector;
  const target = params.target;
  const schedule = params.schedule;
  if (
    "objects" in params ||
    "inst_type" in params ||
    "inst_types" in params ||
    "job_type" in target ||
    "timezone" in schedule ||
    "intervals" in schedule
  ) {
    throw new Error("规则包含已删除字段，请按新结构重新创建");
  }

  const dataType = collector.data_type;
  const exchange = String(collector.exchange || "").trim();
  const market = collector.market;
  const datasetId = String(target.dataset_id || "").trim();
  const scheduleInterval = String(schedule.interval || "").trim();
  const intervals = Array.isArray(collector.intervals)
    ? collector.intervals.map((interval: unknown) => String(interval || "").trim()).filter(Boolean)
    : [];
  if (dataType !== "kline" && dataType !== "symbol") {
    throw new Error("规则数据类型无效");
  }
  if (!exchange || (market !== "spot" && market !== "swap") || !datasetId || !scheduleInterval) {
    throw new Error("规则缺少必填参数");
  }
  if (dataType === "kline") {
    if (source.kind !== "dataset_subjects" || source.dataset_id !== datasetId || intervals.length === 0) {
      throw new Error("K线规则参数无效");
    }
  } else if (source.kind !== "none") {
    throw new Error("标的规则参数无效");
  }

  return { dataType, exchange, market, datasetId, intervals, scheduleInterval };
};

const onAdd = () => {
  title.value = "新建采集规则";
  addForm.value = {
    rule_id: "",
    space_id: selectedSpaceId.value || "",
    data_type: "",
    data_source: "",
    collect_params: "{}",
    enabled: "true",
    creator: account.value.user.userName || ""
  };
  resetRuleFields();
  dataSourceOptions.value = [];
  open.value = true;
};

const onUpdate = (record: TaskConfig) => {
  let input: CollectorRuleInput;
  try {
    input = parseStrictRuleParams(record.collect_params);
  } catch (error) {
    console.error("解析现有采集参数失败:", error);
    Message.error(error instanceof Error ? error.message : "解析现有采集参数失败");
    return;
  }

  title.value = "修改采集规则";
  addForm.value = {
    ...record,
    data_type: input.dataType,
    data_source: input.exchange
  };
  marketValue.value = input.market;
  datasetIdValue.value = input.datasetId;
  intervalsValue.value = input.intervals;
  scheduleIntervalValue.value = input.scheduleInterval;
  loadDataSourceOptionsForType(input.dataType);
  open.value = true;
};

const onDataTypeChange = (value: string) => {
  addForm.value.data_type = value;
  addForm.value.data_source = "";
  datasetIdValue.value = "";
  intervalsValue.value = [];
  loadDataSourceOptionsForType(value);
};

const afterClose = () => {
  formRef.value?.clearValidate();
  open.value = false;
};

const handleOk = async (): Promise<boolean> => {
  try {
    if (!addForm.value.data_type) {
      Message.error("请选择数据类型");
      return false;
    }

    if (!addForm.value.data_source) {
      Message.error("请选择数据源");
      return false;
    }
    const spaceId = addForm.value.space_id || selectedSpaceId.value || "";
    if (!spaceId) {
      Message.error("请选择空间");
      return false;
    }

    if (addForm.value.data_type !== "kline" && addForm.value.data_type !== "symbol") {
      Message.error("不支持的数据类型");
      return false;
    }

    const collectParams = buildCollectorRuleParams({
      dataType: addForm.value.data_type,
      exchange: addForm.value.data_source,
      market: marketValue.value,
      datasetId: datasetIdValue.value,
      intervals: intervalsValue.value,
      scheduleInterval: scheduleIntervalValue.value
    });
    addForm.value.collect_params = JSON.stringify(collectParams);

    const requestData: any = {
      space_id: spaceId,
      data_type: addForm.value.data_type,
      exchange: addForm.value.data_source,
      collect_params: collectParams,
      enabled: addForm.value.enabled !== "false",
      creator: addForm.value.creator || account.value.user?.userName || ""
    };

    if (title.value.includes("修改") && addForm.value.rule_id) {
      requestData.rule_id = addForm.value.rule_id;
    }

    const method = title.value.includes("新建") ? "CreateTaskRule" : "UpdateTaskRule";

    submitLoading.value = true;
    const payload = title.value.includes("新建")
      ? { rule: requestData }
      : { space_id: requestData.space_id, rule_id: requestData.rule_id, rule: requestData };
    const data = await callControl<typeof payload, { rule_id?: string }>("collectmgr", method, payload);
    if (title.value.includes("新建")) {
      const ruleId = data.rule_id || "未知";
      Message.success(`创建成功，规则ID：${ruleId}`);
    } else {
      Message.success("更新成功");
    }
    getTaskList();
    return true;
  } catch (error) {
    if (error instanceof Error && error.message) {
      Message.error(error.message);
    } else {
      Message.error(title.value.includes("新建") ? "创建失败，请检查网络连接" : "更新失败，请检查网络连接");
    }
    return false;
  } finally {
    submitLoading.value = false;
  }
};

const handleEnableChange = async (record: TaskConfig, value: boolean) => {
  try {
    const spaceId = record.space_id || selectedSpaceId.value || "";
    if (!spaceId) {
      Message.error("请选择空间");
      return;
    }
    const rule = {
      space_id: spaceId,
      rule_id: record.rule_id,
      data_type: record.data_type,
      exchange: record.data_source,
      collect_params: normalizeObject(record.collect_params),
      enabled: value,
      creator: record.creator || account.value.user?.userName || ""
    };
    await callControl<Record<string, any>, Record<string, never>>("collectmgr", "UpdateTaskRule", {
      space_id: spaceId,
      rule_id: record.rule_id,
      rule
    });
    Message.success("状态更新成功");
    getTaskList();
  } catch (error) {
    console.error("状态更新失败:", error);
    Message.error("状态更新失败");
  }
};

const onViewDetails = (record: TaskConfig) => {
  detailData.value = record;
  detailVisible.value = true;
};

watch(selectedSpaceId, () => {
  datasetIdValue.value = "";
  getTaskList();
  loadActiveDatasets();
});

watch(
  () => form.value.dataType,
  newDataType => {
    // 当数据类型变化时，清空数据源选择
    if (newDataType) {
      form.value.dataSource = "";
    }
  }
);

onMounted(() => {
  getTaskList();
  getDataTypeConfigs();
  loadActiveDatasets();
});
</script>

<style scoped>
.rule-toolbar {
  margin-bottom: var(--moox-space-toolbar-table);
}

.moox-inner {
  min-height: 100%;
}

.moox-inner :deep(.arco-table) {
  margin-top: 0;
}

pre {
  margin: 0;
  font-family: monospace;
  font-size: 12px;
  background: #f5f5f5;
  padding: var(--moox-space-2);
  border-radius: 4px;
  max-height: 200px;
  overflow: auto;
}

:deep(.arco-checkbox-group) {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
}

:deep(.arco-checkbox-group .arco-checkbox) {
  margin-right: 0;
}

:deep(.arco-input-tag) {
  min-height: 32px;
}
</style>
