<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="page-head__title">
          <h2>{{ props.pageTitle }}</h2>
          <a-tooltip
            v-model:popup-visible="bindingInfoVisible"
            content="数据集必须绑定一个 DataNode；首次激活后绑定永久锁定。激活前才可以更换 DataNode，系统不做数据迁移。
"
          >
            <button
              class="title-info"
              type="button"
              aria-label="数据集绑定规则说明"
              @focus="bindingInfoVisible = true"
              @blur="bindingInfoVisible = false"
            >
              <icon-info-circle />
            </button>
          </a-tooltip>
        </div>
        <a-space>
          <a-button type="primary" status="success" :disabled="!selectedSpaceId" @click="openCreate">
            <template #icon><icon-plus /></template>
            新增数据集
          </a-button>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

      <a-table
        v-else
        row-key="dataset_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="visibleRows"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="数据集ID" data-index="dataset_id" :width="180" />
          <a-table-column title="中文名" data-index="name" :width="180" />
          <a-table-column title="数据源" data-index="data_source_id" :width="150" />
          <a-table-column title="DataNode" :width="180">
            <template #cell="{ record }">
              <span>{{ dataNodeLabel(record.data_node_id) }}</span>
            </template>
          </a-table-column>
          <a-table-column title="数据形态" :width="130">
            <template #cell="{ record }">{{ optionLabel(dataKindOptions, record.data_kind) }}</template>
          </a-table-column>
          <a-table-column title="频率" :width="180">
            <template #cell="{ record }">{{ joinList(record.freqs) || "-" }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="修订/绑定" :width="130">
            <template #cell="{ record }">
              <span>r{{ record.revision ?? "-" }}</span>
              <a-tag v-if="record.binding_locked" size="small" color="blue" class="lock-tag">已锁定</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="360" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space wrap>
                <a-button size="mini" type="text" @click="openManage(record)">列/对象</a-button>
                <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
                <a-button v-if="canActivate(record)" size="mini" type="text" status="success" @click="openActivation(record)">
                  激活
                </a-button>
                <a-button v-if="canRebind(record)" size="mini" type="text" @click="openRebind(record)">更换节点</a-button>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>

    <a-modal v-model:visible="visible" data-testid="create-dataset-modal" width="760px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="dataset_id" label="数据集ID" required>
          <a-input v-model="form.dataset_id" :disabled="editing" placeholder="例如 kline" />
        </a-form-item>
        <a-form-item field="data_source_id" label="数据源ID" required>
          <a-select v-model="form.data_source_id" allow-search allow-create placeholder="选择或输入来源ID">
            <a-option v-for="item in dataSources" :key="item.data_source_id" :value="item.data_source_id">
              {{ item.name }} ({{ item.data_source_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="name" label="中文名" required>
          <a-input v-model="form.name" :max-length="10" show-word-limit placeholder="例如 现货K线" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
        </a-form-item>
        <a-form-item field="data_kind" label="数据形态" required>
          <a-select v-model="form.data_kind">
            <a-option v-for="item in dataKindOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="freqsText" label="频率">
          <a-input-tag v-model="freqTags" allow-clear placeholder="例如 1m、1h、1d" />
        </a-form-item>
        <a-form-item field="keep_duration" label="保留时长" required>
          <div class="keep-duration-field">
            <a-input v-model="form.keep_duration" placeholder="0 表示永久保存，例如 24h" />
            <span>被 View 使用时，Dataset 保留时长必须不小于 View 保留时长；0 表示永久保存。</span>
          </div>
        </a-form-item>
        <a-form-item v-if="!editing" field="data_node_id" label="DataNode" required>
          <a-select v-model="form.data_node_id" allow-search placeholder="选择 active DataNode">
            <a-option v-for="node in activeDataNodes" :key="node.node_id" :value="node.node_id">
              {{ node.name || node.node_id }} ({{ node.node_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-descriptions v-if="editing" :column="{ xs: 1, sm: 2 }" bordered>
          <a-descriptions-item label="当前 DataNode">{{ dataNodeLabel(form.data_node_id) }}</a-descriptions-item>
          <a-descriptions-item label="状态">{{ form.status }}</a-descriptions-item>
          <a-descriptions-item label="revision">{{ form.revision || "-" }}</a-descriptions-item>
          <a-descriptions-item label="绑定状态">{{ lockLabel(form.binding_locked) }}</a-descriptions-item>
        </a-descriptions>
      </a-form>
    </a-modal>

    <a-modal
      v-model:visible="activationVisible"
      data-testid="dataset-activation-modal"
      width="720px"
      :title="`激活数据集：${activationDataset?.dataset_id || ''}`"
      :ok-button-props="{ disabled: !activationReady || activationLoading }"
      @ok="confirmActivation"
    >
      <a-alert v-if="activationError" type="error" show-icon>{{ activationError }}</a-alert>
      <a-alert v-else-if="activationCheck && !activationCheck.ready" type="warning" show-icon>
        自检未通过，修复所有失败项后才能激活。
      </a-alert>
      <a-spin :loading="activationLoading" class="activation-checks">
        <a-empty v-if="!activationCheck && !activationLoading" description="正在准备激活自检" />
        <div v-for="item in activationCheck?.checks || []" :key="item.check_id" class="check-row">
          <div class="check-row__status">
            <icon-check-circle-fill v-if="item.ready" class="check-row__ok" />
            <icon-close-circle-fill v-else class="check-row__fail" />
          </div>
          <div class="check-row__body">
            <strong>{{ item.check_id }}</strong>
            <span>{{ item.summary }}</span>
          </div>
        </div>
      </a-spin>
    </a-modal>

    <a-modal
      v-model:visible="rebindVisible"
      data-testid="dataset-rebind-modal"
      width="560px"
      title="更换 Dataset DataNode"
      :ok-button-props="{ disabled: !rebindNodeId || rebindLoading }"
      @ok="confirmRebind"
    >
      <a-alert v-if="rebindDataset?.binding_locked" type="info" show-icon>
        Dataset 首次激活后绑定永久锁定，不能更换 DataNode，也不会发生数据迁移。
      </a-alert>
      <a-form v-else auto-label-width>
        <a-form-item label="当前 DataNode">{{ dataNodeLabel(rebindDataset?.data_node_id) }}</a-form-item>
        <a-form-item label="目标 DataNode" required>
          <a-select v-model="rebindNodeId" allow-search placeholder="选择 active DataNode">
            <a-option v-for="node in rebindNodes" :key="node.node_id" :value="node.node_id">
              {{ node.name || node.node_id }} ({{ node.node_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item label="revision">{{ rebindDataset?.revision ?? "-" }}</a-form-item>
        <a-alert v-if="!rebindNodes.length" type="warning" show-icon>没有可用的其他 active DataNode。</a-alert>
      </a-form>
      <a-alert v-if="rebindError" type="error" show-icon>{{ rebindError }}</a-alert>
    </a-modal>

    <a-drawer v-model:visible="manageVisible" width="920px" :footer="false">
      <template #title>
        <div class="manage-title">
          <span>数据集配置：{{ activeDataset?.dataset_id }}</span>
          <a-space>
            <a-button
              v-if="activeDataset && canActivate(activeDataset)"
              size="mini"
              type="text"
              status="success"
              @click="openActivation(activeDataset)"
            >
              激活
            </a-button>
            <a-button v-if="activeDataset && canRebind(activeDataset)" size="mini" type="text" @click="openRebind(activeDataset)">
              更换节点
            </a-button>
          </a-space>
        </div>
      </template>
      <a-alert v-if="activeDataset?.binding_locked" type="info" show-icon>
        当前 Dataset 已锁定 DataNode 绑定。锁定后不支持更换节点，系统不会迁移已有数据。
      </a-alert>
      <a-tabs default-active-key="columns">
        <a-tab-pane key="columns" title="列定义">
          <DatasetColumnPanel :space-id="selectedSpaceId" :dataset-id="activeDataset?.dataset_id || ''" />
        </a-tab-pane>
        <a-tab-pane key="subjects" title="对象绑定">
          <DatasetSubjectPanel :space-id="selectedSpaceId" :dataset-id="activeDataset?.dataset_id || ''" />
        </a-tab-pane>
      </a-tabs>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import {
  activateDataset,
  checkDatasetActivation,
  createDataset,
  listDataNodes,
  listDataSources,
  listDatasets,
  rebindDatasetDataNode,
  updateDataset
} from "@/api/storage/metadata";
import type { DataNode, DataSource, Dataset, DatasetActivationCheck, DatasetMutation } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import DatasetColumnPanel from "./components/dataset-column-panel.vue";
import DatasetSubjectPanel from "./components/dataset-subject-panel.vue";
import {
  applyPageResult,
  dataKindOptions,
  defaultPagination,
  formatTime,
  joinList,
  optionLabel,
  splitList,
  statusColor,
  validateChineseDisplayName,
  validateLowerSnakeId
} from "@/views/data/shared/metadata-utils";
import {
  datasetMatchesAttribution,
  mergeDatasetAttribution,
  type DatasetRole,
  type OwnerModule
} from "@/views/data/shared/module-attribution";
import { RequestGate } from "@/utils/request-gate";

defineOptions({ name: "DataDatasets" });

const props = withDefaults(
  defineProps<{
    pageTitle?: string;
    ownerModule?: OwnerModule;
    datasetRole?: DatasetRole;
    filterOwnerModules?: OwnerModule[];
    filterDatasetRoles?: DatasetRole[];
    includeUnowned?: boolean;
    managedBy?: string;
  }>(),
  {
    pageTitle: "数据集",
    ownerModule: undefined,
    datasetRole: undefined,
    filterOwnerModules: undefined,
    filterDatasetRoles: undefined,
    includeUnowned: false,
    managedBy: undefined
  }
);

type DatasetForm = Dataset & { freqsText?: string };

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<Dataset[]>([]);
const visibleRows = computed(() =>
  rows.value.filter(item =>
    datasetMatchesAttribution(item, {
      ownerModules: props.filterOwnerModules,
      datasetRoles: props.filterDatasetRoles,
      includeUnowned: props.includeUnowned
    })
  )
);
const hasAttributionFilter = computed(() =>
  Boolean(props.filterOwnerModules?.length || props.filterDatasetRoles?.length || props.includeUnowned)
);
const dataSources = ref<DataSource[]>([]);
const dataNodes = ref<DataNode[]>([]);
const activeDataNodes = computed(() => dataNodes.value.filter(item => item.status === "active"));
const loading = ref(false);
const initialized = ref(false);
const visible = ref(false);
const editing = ref(false);
const manageVisible = ref(false);
const activeDataset = ref<Dataset>();
const pagination = reactive(defaultPagination());
const datasetLoadGate = new RequestGate();
const activationGate = new RequestGate();
const rebindGate = new RequestGate();

const form = reactive<DatasetForm>({
  space_id: "",
  dataset_id: "",
  data_source_id: "",
  name: "",
  description: "",
  data_kind: "DATA_KIND_TIME_SERIES",
  freqs: [],
  freqsText: "",
  status: "disabled",
  data_node_id: "",
  keep_duration: "",
  binding_locked: false,
  revision: 0,
  attributes: {}
});

const freqTags = computed({
  get: () => form.freqs || [],
  set: (value: string[]) => {
    form.freqs = value;
  }
});

const modalTitle = computed(() => (editing.value ? "编辑数据集" : "新增数据集"));
const bindingInfoVisible = ref(false);
const activationVisible = ref(false);
const activationDataset = ref<Dataset>();
const activationCheck = ref<{ dataset_revision: number | string; checks: DatasetActivationCheck[]; ready: boolean }>();
const activationLoading = ref(false);
const activationError = ref("");
const activationReady = computed(() => Boolean(activationCheck.value?.ready));
const rebindVisible = ref(false);
const rebindDataset = ref<Dataset>();
const rebindNodeId = ref("");
const rebindLoading = ref(false);
const rebindError = ref("");
const rebindNodes = computed(() => activeDataNodes.value.filter(item => item.node_id !== rebindDataset.value?.data_node_id));

function queryValue(value: unknown) {
  return typeof value === "string" ? value : Array.isArray(value) ? String(value[0] || "") : "";
}

function errorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  const response = (error as { response?: { data?: { ret_info?: { msg?: string } } } } | undefined)?.response;
  return response?.data?.ret_info?.msg || fallback;
}

function dataNodeLabel(nodeId?: string) {
  if (!nodeId) return "-";
  const node = dataNodes.value.find(item => item.node_id === nodeId);
  return node ? `${node.name || node.node_id} (${node.node_id})` : nodeId;
}

function lockLabel(locked?: boolean) {
  return locked ? "已锁定，不能更换" : "未锁定，激活前可更换";
}

function canActivate(dataset: Dataset) {
  return dataset.status === "disabled";
}

function canRebind(dataset: Dataset) {
  return dataset.status === "disabled" && !dataset.binding_locked;
}

async function fetchDataSources(spaceId: string) {
  const rsp = await listDataSources({ space_id: spaceId, page: { page: 1, size: 200 } });
  return rsp.data_sources || [];
}

async function fetchDataNodes() {
  const nodes: DataNode[] = [];
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listDataNodes({ page: { page: pageNo, size: 500 } });
    for (const item of rsp.items || []) {
      if (item.node) nodes.push(item.node);
    }
    if (!rsp.page_result?.has_more || (rsp.items || []).length === 0) break;
  }
  return nodes;
}

async function listAllDatasets(spaceId: string) {
  const datasets: Dataset[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page: pageNo, size } });
    datasets.push(...(rsp.datasets || []));
    if (!rsp.page_result?.has_more || (rsp.datasets || []).length === 0) return datasets;
  }
}

async function load() {
  const token = datasetLoadGate.next();
  const spaceId = selectedSpaceId.value;
  if (!spaceId) {
    rows.value = [];
    dataSources.value = [];
    loading.value = false;
    return;
  }
  const isCurrent = () => datasetLoadGate.isCurrent(token) && selectedSpaceId.value === spaceId;
  const page = pagination.current;
  const pageSize = pagination.pageSize;
  loading.value = true;
  try {
    const [nextDataSources, nextDataNodes] = await Promise.all([fetchDataSources(spaceId), fetchDataNodes()]);
    if (!isCurrent()) return;
    dataSources.value = nextDataSources;
    dataNodes.value = nextDataNodes;
    const deepLinkDatasetId = queryValue(route.query.dataset_id);
    if (deepLinkDatasetId) {
      const nextRows = await listAllDatasets(spaceId);
      if (!isCurrent()) return;
      rows.value = nextRows;
      pagination.current = 1;
      pagination.total = rows.value.length;
    } else if (hasAttributionFilter.value) {
      const nextRows = await listAllDatasets(spaceId);
      if (!isCurrent()) return;
      rows.value = nextRows;
      pagination.total = visibleRows.value.length;
    } else {
      const rsp = await listDatasets({
        space_id: spaceId,
        page: { page, size: pageSize }
      });
      if (!isCurrent()) return;
      rows.value = rsp.datasets || [];
      applyPageResult(pagination, rsp.page_result);
    }
    if (isCurrent()) await consumeDeepLink();
  } finally {
    if (isCurrent()) loading.value = false;
  }
}

async function consumeDeepLink() {
  const datasetId = queryValue(route.query.dataset_id);
  if (!datasetId) return;
  const requestedSpaceId = queryValue(route.query.space_id);
  if (requestedSpaceId && !spaceStore.hasSpace(requestedSpaceId)) return;
  const target = rows.value.find(item => item.dataset_id === datasetId);
  if (!target) return;
  openManage(target);
  const query = { ...route.query };
  delete query.space_id;
  delete query.dataset_id;
  await router.replace({ query });
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    dataset_id: "",
    data_source_id: "",
    name: "",
    description: "",
    data_kind: "DATA_KIND_TIME_SERIES",
    freqs: [],
    freqsText: "",
    status: "disabled",
    data_node_id: "",
    keep_duration: "",
    binding_locked: false,
    revision: 0,
    attributes: {}
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Dataset) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    freqs: record.freqs || [],
    freqsText: joinList(record.freqs),
    keep_duration: record.keep_duration || "0",
    data_node_id: record.data_node_id || "",
    binding_locked: Boolean(record.binding_locked),
    revision: record.revision || 0
  });
  visible.value = true;
}

function openManage(record: Dataset) {
  activeDataset.value = record;
  manageVisible.value = true;
}

function buildDatasetPayload(spaceId: string): Dataset | DatasetMutation {
  const common = {
    space_id: spaceId,
    dataset_id: form.dataset_id,
    data_source_id: form.data_source_id,
    name: form.name,
    description: form.description,
    data_kind: form.data_kind,
    freqs: splitList(form.freqs),
    keep_duration: form.keep_duration.trim(),
    attributes: mergeDatasetAttribution(form.attributes, {
      ownerModule: props.ownerModule,
      datasetRole: props.datasetRole,
      managedBy: props.managedBy
    })
  };
  if (editing.value) {
    return form.status === "disabled" ? { ...common, status: "disabled" } : common;
  }
  return { ...common, data_node_id: form.data_node_id, status: "disabled" };
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.dataset_id || !form.data_source_id || !form.name || !form.data_kind) {
    Message.warning("请补全数据集ID、数据源、中文名和数据形态");
    return;
  }
  if (!editing.value && !form.data_node_id) {
    Message.warning("请选择 active DataNode");
    return;
  }
  if (!form.keep_duration.trim()) {
    Message.warning("请填写保留时长，0 表示永久保存");
    return;
  }
  const nameError = validateChineseDisplayName(form.name);
  if (nameError) {
    Message.warning(nameError);
    return;
  }
  const idError = validateLowerSnakeId(form.dataset_id, 20);
  if (idError) {
    Message.warning(`数据集${idError}`);
    return;
  }
  const payload = buildDatasetPayload(spaceId);
  if (editing.value) await updateDataset(payload);
  else await createDataset(payload as Dataset);
  Message.success("数据集已保存");
  visible.value = false;
  await load();
}

function replaceRow(dataset: Dataset) {
  const index = rows.value.findIndex(item => item.space_id === dataset.space_id && item.dataset_id === dataset.dataset_id);
  if (index >= 0) rows.value.splice(index, 1, dataset);
  if (activeDataset.value?.space_id === dataset.space_id && activeDataset.value?.dataset_id === dataset.dataset_id) {
    activeDataset.value = dataset;
  }
}

async function openActivation(record: Dataset) {
  activationDataset.value = record;
  activationCheck.value = undefined;
  activationError.value = "";
  activationVisible.value = true;
  await runActivationCheck();
}

async function runActivationCheck() {
  const dataset = activationDataset.value;
  if (!dataset) return;
  const token = activationGate.next();
  const isCurrent = () =>
    activationGate.isCurrent(token) &&
    selectedSpaceId.value === dataset.space_id &&
    activationDataset.value?.space_id === dataset.space_id &&
    activationDataset.value?.dataset_id === dataset.dataset_id;
  activationLoading.value = true;
  activationError.value = "";
  try {
    const rsp = await checkDatasetActivation({
      space_id: dataset.space_id,
      dataset_id: dataset.dataset_id
    });
    if (!isCurrent()) return;
    activationCheck.value = {
      dataset_revision: rsp.dataset_revision,
      checks: rsp.checks || [],
      ready: Boolean(rsp.ready)
    };
  } catch (error) {
    if (isCurrent()) activationError.value = errorMessage(error, "Dataset 激活自检失败");
  } finally {
    if (isCurrent()) activationLoading.value = false;
  }
}

async function confirmActivation() {
  const dataset = activationDataset.value;
  const check = activationCheck.value;
  if (!dataset || !check?.ready) return;
  const token = activationGate.next();
  const isCurrent = () =>
    activationGate.isCurrent(token) &&
    selectedSpaceId.value === dataset.space_id &&
    activationDataset.value?.space_id === dataset.space_id &&
    activationDataset.value?.dataset_id === dataset.dataset_id;
  activationLoading.value = true;
  activationError.value = "";
  try {
    const rsp = await activateDataset({
      space_id: dataset.space_id,
      dataset_id: dataset.dataset_id,
      expected_revision: check.dataset_revision
    });
    if (!isCurrent()) return;
    if (rsp.dataset) replaceRow(rsp.dataset);
    activationVisible.value = false;
    Message.success("数据集已激活，DataNode 绑定已锁定");
  } catch (error) {
    if (isCurrent()) activationError.value = errorMessage(error, "Dataset 激活失败");
  } finally {
    if (isCurrent()) activationLoading.value = false;
  }
}

function openRebind(record: Dataset) {
  if (!canRebind(record)) return;
  rebindGate.next();
  rebindDataset.value = record;
  rebindNodeId.value = rebindNodes.value[0]?.node_id || "";
  rebindError.value = "";
  rebindVisible.value = true;
}

async function confirmRebind() {
  const dataset = rebindDataset.value;
  const nodeId = rebindNodeId.value;
  if (!dataset || !nodeId || !canRebind(dataset)) return;
  const token = rebindGate.next();
  const isCurrent = () =>
    rebindGate.isCurrent(token) &&
    selectedSpaceId.value === dataset.space_id &&
    rebindDataset.value?.space_id === dataset.space_id &&
    rebindDataset.value?.dataset_id === dataset.dataset_id;
  rebindLoading.value = true;
  rebindError.value = "";
  try {
    const rsp = await rebindDatasetDataNode({
      space_id: dataset.space_id,
      dataset_id: dataset.dataset_id,
      data_node_id: nodeId,
      expected_revision: dataset.revision ?? 0
    });
    if (!isCurrent()) return;
    if (rsp.dataset) replaceRow(rsp.dataset);
    rebindVisible.value = false;
    Message.success("Dataset DataNode 已更换");
  } catch (error) {
    if (isCurrent()) rebindError.value = errorMessage(error, "Dataset DataNode 更换失败");
  } finally {
    if (isCurrent()) rebindLoading.value = false;
  }
}

function onPageChange(page: number) {
  pagination.current = page;
  void load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  void load();
}

function resetDatasetSpaceState() {
  activationGate.next();
  rebindGate.next();
  rows.value = [];
  dataSources.value = [];
  visible.value = false;
  editing.value = false;
  manageVisible.value = false;
  activeDataset.value = undefined;
  activationVisible.value = false;
  activationDataset.value = undefined;
  activationCheck.value = undefined;
  activationError.value = "";
  activationLoading.value = false;
  rebindVisible.value = false;
  rebindDataset.value = undefined;
  rebindNodeId.value = "";
  rebindError.value = "";
  rebindLoading.value = false;
}

watch(selectedSpaceId, () => {
  if (!initialized.value) return;
  pagination.current = 1;
  resetDatasetSpaceState();
  void load();
});

onMounted(async () => {
  await spaceStore.loadSpaces();
  const requestedSpaceId = queryValue(route.query.space_id);
  if (requestedSpaceId && spaceStore.hasSpace(requestedSpaceId)) {
    spaceStore.setSelectedSpace(requestedSpaceId);
  }
  await load();
  initialized.value = true;
});

onBeforeUnmount(() => {
  datasetLoadGate.next();
  activationGate.next();
  rebindGate.next();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}

.page-head__title {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 6px;
  flex-wrap: wrap;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.title-info {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  color: var(--color-text-3);
  background: transparent;
  border: 0;
  border-radius: 4px;
  cursor: help;
}

.title-info:focus-visible {
  outline: 2px solid rgb(var(--primary-6));
  outline-offset: 2px;
}

.lock-tag {
  margin-left: 6px;
}

.keep-duration-field {
  display: flex;
  width: 100%;
  flex-direction: column;
  gap: 4px;
}

.keep-duration-field span {
  color: var(--color-text-3);
  font-size: 12px;
  line-height: 18px;
}

.manage-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
}

.check-row {
  display: flex;
  gap: 10px;
  padding: 10px 0;
  border-bottom: 1px solid var(--color-border-2);
}

.check-row:last-child {
  border-bottom: 0;
}

.check-row__status {
  flex: 0 0 auto;
  padding-top: 2px;
}

.check-row__ok {
  color: rgb(var(--green-6));
}

.check-row__fail {
  color: rgb(var(--red-6));
}

.check-row__body {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.check-row__body span {
  overflow-wrap: anywhere;
  color: var(--color-text-2);
}

@media (max-width: 600px) {
  .page-head {
    align-items: flex-start;
  }

  .page-head__title {
    flex: 1 1 100%;
  }

  .page-head > :last-child {
    flex: 0 0 auto;
  }

  .manage-title {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
