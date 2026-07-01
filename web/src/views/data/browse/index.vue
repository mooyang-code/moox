<template>
  <div class="data-browse-page">
    <div class="page-head">
      <div>
        <h2>数据浏览</h2>
      </div>
      <a-button :disabled="!selectedSpaceId" :loading="metaLoading || contextLoading" @click="loadMeta">
        <template #icon><icon-refresh /></template>
        刷新
      </a-button>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <a-spin v-else :loading="metaLoading">
      <a-empty v-if="datasets.length === 0" description="暂无数据集" />

      <template v-else>
        <section class="dataset-tabs-row">
          <a-tabs v-model:active-key="activeDatasetId" type="rounded" size="medium" class="dataset-tabs" @change="onDatasetChange">
            <a-tab-pane v-for="dataset in datasets" :key="dataset.dataset_id" :title="datasetDisplayName(dataset)" />
          </a-tabs>

          <a-tabs
            v-if="mode === 'time_series' && freqOptions.length > 0"
            v-model:active-key="activeFreq"
            type="line"
            size="medium"
            class="freq-tabs"
            @change="onFreqChange"
          >
            <a-tab-pane v-for="freq in freqOptions" :key="freq" :title="freq" />
          </a-tabs>
        </section>

        <section v-if="mode === 'time_series'" class="browse-shell">
          <aside class="data-id-pane">
            <div class="pane-head">
              <strong>数据ID</strong>
              <span>{{ filteredDataIds.length }} 个</span>
            </div>
            <a-input v-model="dataIdKeyword" allow-clear placeholder="搜索数据ID">
              <template #prefix><icon-search /></template>
            </a-input>
            <div class="data-id-list" :class="{ loading: contextLoading }">
              <a-spin v-if="contextLoading" :size="24" tip="加载中..." />
              <a-empty v-else-if="filteredDataIds.length === 0" description="暂无数据ID" />
              <button
                v-for="item in filteredDataIds"
                v-else
                :key="item.id"
                class="data-id-item"
                :class="{ active: item.id === activeDataId }"
                @click="selectDataId(item.id)"
              >
                {{ displayDataIdText(item) }}
              </button>
            </div>
          </aside>

          <main class="table-pane">
            <div class="table-toolbar">
              <div>
                <div class="dataset-title-line">
                  <strong>{{ activeDataId || '未选择数据ID' }}</strong>
                  <span v-if="activeFreq" class="inline-freq">/ {{ activeFreq }}</span>
                </div>
              </div>
              <a-button :disabled="!activeDataId" :loading="loading" @click="reloadRows">
                <template #icon><icon-refresh /></template>
                重新加载
              </a-button>
            </div>

            <a-table
              row-key="id"
              size="small"
              :bordered="{ cell: true }"
              :loading="loading"
              :data="tableRows"
              :pagination="tablePagination"
              :scroll="{ x: 'max-content', y: 430 }"
              @page-change="onPageChange"
              @page-size-change="onPageSizeChange"
            >
              <template #columns>
                <a-table-column title="序号" :width="72" align="center" fixed="left">
                  <template #cell="{ rowIndex }">{{ (pagination.current - 1) * pagination.pageSize + rowIndex + 1 }}</template>
                </a-table-column>
                <a-table-column title="数据ID" data-index="key" :width="180" fixed="left" />
                <a-table-column title="时间" data-index="version" :width="230" />
                <a-table-column
                  v-for="column in tableColumnNames"
                  :key="column"
                  :title="columnTitle(column)"
                  :width="dynamicColumnWidth(column)"
                  :ellipsis="true"
                  :tooltip="true"
                >
                  <template #cell="{ record }">{{ record.values[column] || '-' }}</template>
                </a-table-column>
                <a-table-column title="操作" :width="92" align="center" fixed="right">
                  <template #cell="{ record }">
                    <a-button type="text" size="mini" @click="openDetail(record)">查看</a-button>
                  </template>
                </a-table-column>
              </template>
            </a-table>
          </main>
        </section>

        <section v-else-if="mode === 'record'" class="record-table-pane">
          <div class="table-toolbar">
            <div>
              <strong>记录数据</strong>
              <span>{{ datasetDisplayName(currentDataset) }}</span>
            </div>
            <a-button :loading="loading" @click="reloadRows">
              <template #icon><icon-refresh /></template>
              重新加载
            </a-button>
          </div>

          <a-table
            row-key="id"
            size="small"
            :bordered="{ cell: true }"
            :loading="loading"
            :data="tableRows"
            :pagination="tablePagination"
            :scroll="{ x: 'max-content', y: 460 }"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #columns>
              <a-table-column title="序号" :width="72" align="center" fixed="left">
                <template #cell="{ rowIndex }">{{ (pagination.current - 1) * pagination.pageSize + rowIndex + 1 }}</template>
              </a-table-column>
              <a-table-column title="记录ID" data-index="key" :width="180" fixed="left" />
              <a-table-column title="版本" data-index="version" :width="160" />
              <a-table-column
                v-for="column in tableColumnNames"
                :key="column"
                :title="columnTitle(column)"
                :width="dynamicColumnWidth(column)"
                :ellipsis="true"
                :tooltip="true"
              >
                <template #cell="{ record }">{{ record.values[column] || '-' }}</template>
              </a-table-column>
              <a-table-column title="操作" :width="92" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-button type="text" size="mini" @click="openDetail(record)">查看</a-button>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </section>

        <a-empty v-else description="无法识别该数据集的数据类型" />
      </template>
    </a-spin>

    <a-modal v-model:visible="detailVisible" title="数据详情" width="820px" :footer="false">
      <div v-if="detailRow" class="detail-body">
        <a-descriptions :column="2" bordered>
          <a-descriptions-item :label="mode === 'time_series' ? '数据ID' : '记录ID'">{{ detailRow.key }}</a-descriptions-item>
          <a-descriptions-item :label="mode === 'time_series' ? '时间' : '版本'">{{ detailRow.version }}</a-descriptions-item>
        </a-descriptions>
        <a-table :data="detailColumns" :pagination="false" :bordered="{ cell: true }" size="small" class="detail-table">
          <template #columns>
            <a-table-column title="字段名" data-index="name" :width="220" />
            <a-table-column title="字段值" data-index="value" :ellipsis="true" :tooltip="true" />
          </template>
        </a-table>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import {
  listDatasetColumns,
  listDatasets,
  listDatasetSubjects,
  listFactors,
  listFields,
  listSubjects,
} from '@/api/storage/metadata';
import { readRecordRows, readTimeSeriesRows } from '@/api/storage/access';
import type { Dataset, DatasetColumn, Factor, Field, RecordRow, SortOrder } from '@/api/storage/types';
import { isTimeSeriesDataKind } from '@/views/data/shared/metadata-utils';
import { useSpaceStore } from '@/store/modules/space';
import {
  adaptiveColumnWidth,
  buildColumnLabels,
  buildSubjectDataIds,
  datasetDisplayName,
  displayDataIdText,
  recordRowsToTableRows,
  rowsToColumnNames,
  timeSeriesRowsToTableRows,
  type BrowseDataId,
  type BrowseTableRow,
} from './browse-utils';

defineOptions({ name: 'DataBrowse' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);

const activeDatasetId = ref('');
const datasets = ref<Dataset[]>([]);
const datasetColumns = ref<DatasetColumn[]>([]);
const fields = ref<Field[]>([]);
const factors = ref<Factor[]>([]);
const dataIds = ref<BrowseDataId[]>([]);
const activeDataId = ref('');
const activeFreq = ref('');
const dataIdKeyword = ref('');
const tableRows = ref<BrowseTableRow[]>([]);
const tableColumnNames = ref<string[]>([]);
const detailRow = ref<BrowseTableRow>();
const detailVisible = ref(false);
const metaLoading = ref(false);
const contextLoading = ref(false);
const loading = ref(false);

const pagination = reactive({
  current: 1,
  pageSize: 50,
  total: 0,
});

const currentDataset = computed(() => datasets.value.find((item) => item.dataset_id === activeDatasetId.value));

const mode = computed<'none' | 'time_series' | 'record' | 'missing'>(() => {
  const dataset = currentDataset.value;
  if (!activeDatasetId.value) return 'none';
  if (!dataset) return 'missing';
  return isTimeSeriesDataKind(dataset.data_kind) ? 'time_series' : 'record';
});

const freqOptions = computed(() => currentDataset.value?.freqs || []);

const filteredDataIds = computed(() => {
  const keyword = dataIdKeyword.value.trim().toLowerCase();
  if (!keyword) return dataIds.value;
  return dataIds.value.filter((item) => item.id.toLowerCase().includes(keyword) || item.name.toLowerCase().includes(keyword));
});

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  showTotal: true,
  showPageSize: true,
  showJumper: true,
  hideOnSinglePage: false,
  pageSizeOptions: [20, 50, 100, 200],
}));

const preferredColumnNames = computed(() => datasetColumns.value.map((item) => item.column_name).filter(Boolean));

const columnLabels = computed(() => buildColumnLabels(datasetColumns.value, fields.value, factors.value));

const detailColumns = computed(() => {
  const row = detailRow.value;
  if (!row) return [];
  const names = rowsToColumnNames([rowToSyntheticRecord(row)], tableColumnNames.value);
  return names.map((name) => ({ name: columnTitle(name), value: row.values[name] || '-' }));
});

async function loadMeta() {
  const space_id = spaceStore.selectedSpaceId;
  if (!space_id) return;
  metaLoading.value = true;
  try {
    const page = { page: 1, size: 1000 };
    const [dsRsp, fieldRsp, factorRsp] = await Promise.all([
      listDatasets({ space_id, page }),
      listFields({ space_id, page }),
      listFactors({ space_id, page }),
    ]);
    datasets.value = dsRsp.datasets || [];
    fields.value = fieldRsp.fields || [];
    factors.value = factorRsp.factors || [];
    ensureSelectedDataset();
    await loadDatasetContext();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载数据集失败');
  } finally {
    metaLoading.value = false;
  }
}

function ensureSelectedDataset() {
  if (!datasets.value.length) {
    activeDatasetId.value = '';
    return;
  }
  if (!activeDatasetId.value || !datasets.value.some((item) => item.dataset_id === activeDatasetId.value)) {
    activeDatasetId.value = datasets.value[0].dataset_id;
  }
}

async function onDatasetChange() {
  clearBrowseState();
  await loadDatasetContext();
}

async function onFreqChange() {
  pagination.current = 1;
  await reloadRows();
}

function clearBrowseState() {
  datasetColumns.value = [];
  dataIds.value = [];
  activeDataId.value = '';
  activeFreq.value = '';
  dataIdKeyword.value = '';
  tableRows.value = [];
  tableColumnNames.value = [];
  pagination.current = 1;
  pagination.total = 0;
}

async function loadDatasetContext() {
  const space_id = spaceStore.selectedSpaceId;
  const dataset_id = activeDatasetId.value;
  if (!space_id || !dataset_id || mode.value === 'none' || mode.value === 'missing') return;

  contextLoading.value = true;
  try {
    await loadColumns(space_id, dataset_id);
    if (mode.value === 'time_series') {
      await loadTimeSeriesDataIds(space_id, dataset_id);
      if (!activeFreq.value || !freqOptions.value.includes(activeFreq.value)) {
        activeFreq.value = freqOptions.value[0] || '';
      }
      if (dataIds.value.length > 0) {
        activeDataId.value = dataIds.value[0].id;
      }
    }
    await reloadRows();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载浏览数据失败');
  } finally {
    contextLoading.value = false;
  }
}

async function loadColumns(space_id: string, dataset_id: string) {
  const rsp = await listDatasetColumns({ space_id, dataset_id, page: { page: 1, size: 1000 } });
  datasetColumns.value = rsp.columns || [];
}

async function loadTimeSeriesDataIds(space_id: string, dataset_id: string) {
  const [bindRsp, subjectRsp] = await Promise.all([
    listDatasetSubjects({ space_id, dataset_id, page: { page: 1, size: 1000 } }),
    listSubjects({ space_id, page: { page: 1, size: 1000 } }),
  ]);
  dataIds.value = buildSubjectDataIds(bindRsp.dataset_subjects || [], subjectRsp.subjects || []);
}

async function selectDataId(dataId: string) {
  activeDataId.value = dataId;
  pagination.current = 1;
  await reloadRows();
}

async function reloadRows() {
  tableColumnNames.value = preferredColumnNames.value;
  if (mode.value === 'time_series') {
    if (!activeDataId.value) {
      tableRows.value = [];
      pagination.total = 0;
      return;
    }
    await loadTimeSeriesRows();
    return;
  }
  if (mode.value === 'record') {
    await loadRecordRows();
  }
}

async function loadTimeSeriesRows() {
  const space_id = spaceStore.requireSpaceId();
  const dataset_id = activeDatasetId.value;
  if (!dataset_id || !activeFreq.value) return;

  loading.value = true;
  try {
    const rsp = await readTimeSeriesRows({
      keys: [{
        space_id,
        dataset_id,
        subject_id: activeDataId.value,
        freq: activeFreq.value,
        dimensions: {},
      }],
      order: 'SORT_ORDER_DESC' as SortOrder,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    const rows = rsp.rows || [];
    tableRows.value = timeSeriesRowsToTableRows(rows);
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    pagination.total = rsp.page_result?.total || rows.length;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载时序数据失败');
  } finally {
    loading.value = false;
  }
}

async function loadRecordRows() {
  const space_id = spaceStore.requireSpaceId();
  const dataset_id = activeDatasetId.value;
  if (!dataset_id) return;

  loading.value = true;
  try {
    const rsp = await readRecordRows({
      keys: [{ space_id, dataset_id, record_id: '' }],
      order: 'SORT_ORDER_DESC' as SortOrder,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    const rows = rsp.rows || [];
    tableRows.value = recordRowsToTableRows(rows);
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    pagination.total = rsp.page_result?.total || rows.length;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载记录数据失败');
  } finally {
    loading.value = false;
  }
}

async function onPageChange(page: number) {
  pagination.current = page;
  await reloadRows();
}

async function onPageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize;
  pagination.current = 1;
  await reloadRows();
}

function columnTitle(columnName: string) {
  return columnLabels.value[columnName] || columnName;
}

function dynamicColumnWidth(columnName: string) {
  return adaptiveColumnWidth(columnName, columnTitle(columnName), tableRows.value);
}

function openDetail(row: BrowseTableRow) {
  detailRow.value = row;
  detailVisible.value = true;
}

function rowToSyntheticRecord(row: BrowseTableRow): RecordRow {
  return {
    key: { space_id: '', dataset_id: '', record_id: row.key, version: row.version },
    columns: Object.keys(row.values).map((name) => ({
      column_name: name,
      value_type: 'FIELD_VALUE_TYPE_STRING',
      value: { string_value: row.values[name] },
    })),
  };
}

onMounted(loadMeta);
watch(selectedSpaceId, () => {
  activeDatasetId.value = '';
  clearBrowseState();
  loadMeta();
});
</script>

<style scoped>
.data-browse-page {
  box-sizing: border-box;
  width: 100%;
  height: 100%;
  min-width: 0;
  padding: 20px 20px 72px;
  overflow-y: auto;
}

.data-browse-page :deep(.arco-spin) {
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
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
}

.pane-head span,
.record-table-pane .table-toolbar span {
  color: var(--color-text-3);
}

.dataset-tabs-row {
  display: flex;
  align-items: flex-end;
  gap: 18px;
  min-width: 0;
  margin-bottom: 12px;
}

.dataset-tabs {
  min-width: 0;
  flex: 1;
}

.dataset-tabs :deep(.arco-tabs-content),
.freq-tabs :deep(.arco-tabs-content) {
  display: none;
}

.freq-tabs {
  flex: 0 0 auto;
}

.freq-tabs :deep(.arco-tabs-tab) {
  min-height: 26px;
  padding: 4px 10px;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}

.freq-tabs :deep(.arco-tabs-tab-active) {
  background: rgb(var(--primary-1));
  border-color: rgb(var(--primary-5));
}

.browse-shell {
  display: grid;
  grid-template-columns: minmax(200px, 240px) minmax(0, 1fr);
  align-items: start;
  gap: 16px;
  min-height: 500px;
  height: calc(100vh - 246px);
}

.data-id-pane,
.table-pane,
.record-table-pane {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.data-id-pane {
  display: flex;
  flex-direction: column;
  height: calc(100vh - 246px);
  min-height: 560px;
  max-height: 760px;
  overflow: hidden;
  padding: 12px;
}

.pane-head,
.table-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.pane-head {
  margin-bottom: 10px;
}

.data-id-list {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 4px;
  height: 0;
  min-height: 0;
  margin-top: 10px;
  overflow: auto;
  overscroll-behavior: contain;
}

.data-id-list.loading {
  align-items: center;
  justify-content: center;
}

.data-id-item {
  flex: 0 0 auto;
  width: 100%;
  min-height: 36px;
  padding: 8px 9px;
  overflow: hidden;
  color: var(--color-text-1);
  font-weight: 500;
  line-height: 20px;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 4px;
  transition: background-color 0.16s ease, border-color 0.16s ease;
}

.data-id-item:hover {
  background: var(--color-fill-2);
}

.data-id-item.active {
  background: rgb(var(--primary-1));
  border-color: rgb(var(--primary-5));
}

.table-pane,
.record-table-pane {
  padding: 12px;
}

.table-toolbar {
  margin-bottom: 12px;
}

.table-toolbar > div > strong,
.table-toolbar > div > span:not(.inline-freq) {
  display: block;
}

.table-toolbar > div > span:not(.inline-freq) {
  margin-top: 2px;
  color: var(--color-text-3);
  font-size: 12px;
}

.dataset-title-line {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}

.dataset-title-line strong {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.table-toolbar .inline-freq {
  flex: 0 0 auto;
  color: var(--color-text-3);
  font-size: 12px;
}

.detail-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.detail-table {
  margin-top: 4px;
}

@media (max-width: 960px) {
  .dataset-tabs-row {
    align-items: stretch;
    flex-direction: column;
  }

  .browse-shell {
    grid-template-columns: 1fr;
  }

  .data-id-pane {
    height: clamp(520px, 68vh, 720px);
    min-height: 520px;
  }
}
</style>
