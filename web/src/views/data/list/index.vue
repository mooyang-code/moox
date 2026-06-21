<template>
  <div class="data-list-page">
    <div class="page-head">
      <h2>数据列表</h2>
      <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <a-tabs v-else v-model:active-key="activeTab">
      <a-tab-pane key="primary" title="主存读取">
        <a-tabs v-model:active-key="primaryKind" type="line">
          <a-tab-pane key="timeSeries" title="TimeSeries">
            <section class="query-panel">
              <a-form :model="timeSeriesForm" layout="vertical">
                <a-row :gutter="12">
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="Dataset">
                      <a-input v-model="timeSeriesForm.dataset_id" placeholder="dataset_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="Subject">
                      <a-input v-model="timeSeriesForm.subject_id" placeholder="subject_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="4">
                    <a-form-item label="Freq">
                      <a-input v-model="timeSeriesForm.freq" placeholder="1m" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="12" :lg="4">
                    <a-form-item label="开始时间">
                      <a-input v-model="timeSeriesForm.start_time" placeholder="RFC3339" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="12" :lg="4">
                    <a-form-item label="结束时间">
                      <a-input v-model="timeSeriesForm.end_time" placeholder="RFC3339" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="12" :lg="8">
                    <a-form-item label="Dimensions JSON">
                      <a-input v-model="timeSeriesForm.dimensions" placeholder='{"adjust":"none"}' />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="12" :lg="8">
                    <a-form-item label="列名">
                      <a-input v-model="timeSeriesForm.column_names" placeholder="close,volume" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="6" :lg="4">
                    <a-form-item label="顺序">
                      <a-select v-model="timeSeriesForm.order">
                        <a-option value="SORT_ORDER_ASC">升序</a-option>
                        <a-option value="SORT_ORDER_DESC">降序</a-option>
                      </a-select>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="6" :lg="4">
                    <a-form-item label="页大小">
                      <a-input-number v-model="timeSeriesForm.page_size" :min="1" :max="500" />
                    </a-form-item>
                  </a-col>
                </a-row>
                <a-button type="primary" :loading="loading" @click="queryPrimaryTimeSeries">
                  <template #icon><icon-search /></template>
                  查询
                </a-button>
              </a-form>
            </section>
          </a-tab-pane>

          <a-tab-pane key="record" title="Record">
            <section class="query-panel">
              <a-form :model="recordForm" layout="vertical">
                <a-row :gutter="12">
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="Dataset">
                      <a-input v-model="recordForm.dataset_id" placeholder="dataset_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="Record ID">
                      <a-input v-model="recordForm.record_id" placeholder="record_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="开始版本">
                      <a-input v-model="recordForm.start_version" placeholder="可留空" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="结束版本">
                      <a-input v-model="recordForm.end_version" placeholder="可留空" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="12" :lg="8">
                    <a-form-item label="列名">
                      <a-input v-model="recordForm.column_names" placeholder="title,body" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="6" :lg="4">
                    <a-form-item label="顺序">
                      <a-select v-model="recordForm.order">
                        <a-option value="SORT_ORDER_ASC">升序</a-option>
                        <a-option value="SORT_ORDER_DESC">降序</a-option>
                      </a-select>
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="6" :lg="4">
                    <a-form-item label="页大小">
                      <a-input-number v-model="recordForm.page_size" :min="1" :max="500" />
                    </a-form-item>
                  </a-col>
                </a-row>
                <a-button type="primary" :loading="loading" @click="queryPrimaryRecord">
                  <template #icon><icon-search /></template>
                  查询
                </a-button>
              </a-form>
            </section>
          </a-tab-pane>
        </a-tabs>
      </a-tab-pane>

      <a-tab-pane key="view" title="视图查询">
        <a-tabs v-model:active-key="viewKind" type="line">
          <a-tab-pane key="timeSeries" title="TimeSeries View">
            <section class="query-panel">
              <a-form :model="viewTimeSeriesForm" layout="vertical">
                <a-row :gutter="12">
                  <a-col :xs="24" :md="8" :lg="5">
                    <a-form-item label="View">
                      <a-input v-model="viewTimeSeriesForm.view_id" placeholder="view_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="5">
                    <a-form-item label="Dataset">
                      <a-input v-model="viewTimeSeriesForm.dataset_id" placeholder="dataset_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="5">
                    <a-form-item label="Subject">
                      <a-input v-model="viewTimeSeriesForm.subject_id" placeholder="subject_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="3">
                    <a-form-item label="Freq">
                      <a-input v-model="viewTimeSeriesForm.freq" placeholder="1m" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="3">
                    <a-form-item label="开始">
                      <a-input v-model="viewTimeSeriesForm.start_time" placeholder="RFC3339" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="3">
                    <a-form-item label="结束">
                      <a-input v-model="viewTimeSeriesForm.end_time" placeholder="RFC3339" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="12" :lg="8">
                    <a-form-item label="Dimensions JSON">
                      <a-input v-model="viewTimeSeriesForm.dimensions" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="12" :lg="8">
                    <a-form-item label="列名">
                      <a-input v-model="viewTimeSeriesForm.column_names" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="6" :lg="4">
                    <a-form-item label="页大小">
                      <a-input-number v-model="viewTimeSeriesForm.page_size" :min="1" :max="500" />
                    </a-form-item>
                  </a-col>
                </a-row>
                <a-button type="primary" :loading="loading" @click="queryViewTimeSeries">
                  <template #icon><icon-search /></template>
                  查询
                </a-button>
              </a-form>
            </section>
          </a-tab-pane>

          <a-tab-pane key="record" title="Record View">
            <section class="query-panel">
              <a-form :model="viewRecordForm" layout="vertical">
                <a-row :gutter="12">
                  <a-col :xs="24" :md="8" :lg="5">
                    <a-form-item label="View">
                      <a-input v-model="viewRecordForm.view_id" placeholder="view_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="5">
                    <a-form-item label="Dataset">
                      <a-input v-model="viewRecordForm.dataset_id" placeholder="dataset_id" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="5">
                    <a-form-item label="Record ID">
                      <a-input v-model="viewRecordForm.record_id" placeholder="可留空" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="5">
                    <a-form-item label="全文检索">
                      <a-input v-model="viewRecordForm.text_query" placeholder="关键词" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="12" :md="6" :lg="4">
                    <a-form-item label="页大小">
                      <a-input-number v-model="viewRecordForm.page_size" :min="1" :max="500" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="开始版本">
                      <a-input v-model="viewRecordForm.start_version" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="结束版本">
                      <a-input v-model="viewRecordForm.end_version" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :md="8" :lg="6">
                    <a-form-item label="列名">
                      <a-input v-model="viewRecordForm.column_names" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :lg="12">
                    <a-form-item label="Filters JSON">
                      <a-textarea v-model="viewRecordForm.filters" :auto-size="{ minRows: 3, maxRows: 6 }" />
                    </a-form-item>
                  </a-col>
                  <a-col :xs="24" :lg="12">
                    <a-form-item label="Sorts JSON">
                      <a-textarea v-model="viewRecordForm.sorts" :auto-size="{ minRows: 3, maxRows: 6 }" />
                    </a-form-item>
                  </a-col>
                </a-row>
                <a-button type="primary" :loading="loading" @click="queryViewRecord">
                  <template #icon><icon-search /></template>
                  查询
                </a-button>
              </a-form>
            </section>
          </a-tab-pane>
        </a-tabs>
      </a-tab-pane>
    </a-tabs>

    <section class="result-panel">
      <div class="result-head">
        <strong>查询结果</strong>
        <span>{{ resultRows.length }} 行</span>
      </div>
      <a-table
        row-key="id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="resultRows"
        :pagination="false"
        :scroll="{ x: 'max-content', y: 420 }"
      >
        <template #columns>
          <a-table-column title="Key" data-index="key" :width="320" />
          <a-table-column title="Version" data-index="version" :width="210" />
          <a-table-column title="Columns" :width="520">
            <template #cell="{ record }">
              <pre>{{ record.columns }}</pre>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { readRecordRows, readTimeSeriesRows } from '@/api/storage/access';
import { queryTimeSeriesRows, searchRecordRows } from '@/api/storage/view';
import type { FilterExpr, RecordRow, SortOrder, SortSpec, TimeSeriesRow } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';

defineOptions({ name: 'DataList' });

interface ResultRow {
  id: string;
  key: string;
  version: string;
  columns: string;
}

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const activeTab = ref('primary');
const primaryKind = ref('timeSeries');
const viewKind = ref('timeSeries');
const loading = ref(false);
const resultRows = ref<ResultRow[]>([]);

const timeSeriesForm = reactive({
  dataset_id: '',
  subject_id: '',
  freq: '',
  dimensions: '{}',
  start_time: '',
  end_time: '',
  column_names: '',
  order: 'SORT_ORDER_ASC',
  page_size: 100,
});

const recordForm = reactive({
  dataset_id: '',
  record_id: '',
  start_version: '',
  end_version: '',
  column_names: '',
  order: 'SORT_ORDER_ASC',
  page_size: 100,
});

const viewTimeSeriesForm = reactive({
  view_id: '',
  dataset_id: '',
  subject_id: '',
  freq: '',
  dimensions: '{}',
  start_time: '',
  end_time: '',
  column_names: '',
  page_size: 100,
});

const viewRecordForm = reactive({
  view_id: '',
  dataset_id: '',
  record_id: '',
  start_version: '',
  end_version: '',
  text_query: '',
  filters: '[]',
  sorts: '[]',
  column_names: '',
  page_size: 100,
});

function splitNames(value: string) {
  return value
    .split(/[,，\n]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function parseJsonObject(value: string, label: string) {
  if (!value.trim()) return {};
  const parsed = JSON.parse(value);
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(`${label} 必须是 JSON 对象`);
  }
  return parsed as Record<string, string>;
}

function parseJsonArray<T>(value: string, label: string) {
  if (!value.trim()) return [];
  const parsed = JSON.parse(value);
  if (!Array.isArray(parsed)) {
    throw new Error(`${label} 必须是 JSON 数组`);
  }
  return parsed as T[];
}

function validateTime(value: string, label: string) {
  if (!value) return;
  if (Number.isNaN(Date.parse(value))) {
    throw new Error(`${label} 必须是 RFC3339/RFC3339Nano 时间`);
  }
}

function requireInput(value: string, label: string) {
  if (!value.trim()) throw new Error(`请填写 ${label}`);
}

function mapTimeSeries(rows: TimeSeriesRow[]): ResultRow[] {
  return rows.map((row, index) => ({
    id: `ts-${index}-${row.key?.data_time || ''}`,
    key: `${row.key?.dataset_id || ''}/${row.key?.subject_id || ''}/${row.key?.freq || ''}`,
    version: row.key?.data_time || '-',
    columns: JSON.stringify(row.columns || [], null, 2),
  }));
}

function mapRecord(rows: RecordRow[]): ResultRow[] {
  return rows.map((row, index) => ({
    id: `record-${index}-${row.key?.version || ''}`,
    key: `${row.key?.dataset_id || ''}/${row.key?.record_id || ''}`,
    version: row.key?.version || '-',
    columns: JSON.stringify(row.columns || [], null, 2),
  }));
}

async function runQuery<T>(handler: () => Promise<T>) {
  loading.value = true;
  try {
    await handler();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '查询失败');
  } finally {
    loading.value = false;
  }
}

async function queryPrimaryTimeSeries() {
  await runQuery(async () => {
    const space_id = spaceStore.requireSpaceId();
    requireInput(timeSeriesForm.dataset_id, 'Dataset');
    requireInput(timeSeriesForm.subject_id, 'Subject');
    requireInput(timeSeriesForm.freq, 'Freq');
    validateTime(timeSeriesForm.start_time, '开始时间');
    validateTime(timeSeriesForm.end_time, '结束时间');
    const rsp = await readTimeSeriesRows({
      keys: [{
        space_id,
        dataset_id: timeSeriesForm.dataset_id,
        subject_id: timeSeriesForm.subject_id,
        freq: timeSeriesForm.freq,
        dimensions: parseJsonObject(timeSeriesForm.dimensions, 'Dimensions'),
      }],
      time_range: { start_time: timeSeriesForm.start_time, end_time: timeSeriesForm.end_time },
      order: timeSeriesForm.order as SortOrder,
      column_names: splitNames(timeSeriesForm.column_names),
      page: { page: 1, size: timeSeriesForm.page_size },
    });
    resultRows.value = mapTimeSeries(rsp.rows || []);
  });
}

async function queryPrimaryRecord() {
  await runQuery(async () => {
    const space_id = spaceStore.requireSpaceId();
    requireInput(recordForm.dataset_id, 'Dataset');
    requireInput(recordForm.record_id, 'Record ID');
    const rsp = await readRecordRows({
      keys: [{ space_id, dataset_id: recordForm.dataset_id, record_id: recordForm.record_id }],
      version_range: { start_version: recordForm.start_version, end_version: recordForm.end_version },
      order: recordForm.order as SortOrder,
      column_names: splitNames(recordForm.column_names),
      page: { page: 1, size: recordForm.page_size },
    });
    resultRows.value = mapRecord(rsp.rows || []);
  });
}

async function queryViewTimeSeries() {
  await runQuery(async () => {
    const space_id = spaceStore.requireSpaceId();
    requireInput(viewTimeSeriesForm.view_id, 'View');
    requireInput(viewTimeSeriesForm.dataset_id, 'Dataset');
    requireInput(viewTimeSeriesForm.subject_id, 'Subject');
    requireInput(viewTimeSeriesForm.freq, 'Freq');
    validateTime(viewTimeSeriesForm.start_time, '开始时间');
    validateTime(viewTimeSeriesForm.end_time, '结束时间');
    const rsp = await queryTimeSeriesRows({
      space_id,
      view_id: viewTimeSeriesForm.view_id,
      keys: [{
        space_id,
        dataset_id: viewTimeSeriesForm.dataset_id,
        subject_id: viewTimeSeriesForm.subject_id,
        freq: viewTimeSeriesForm.freq,
        dimensions: parseJsonObject(viewTimeSeriesForm.dimensions, 'Dimensions'),
      }],
      time_range: { start_time: viewTimeSeriesForm.start_time, end_time: viewTimeSeriesForm.end_time },
      column_names: splitNames(viewTimeSeriesForm.column_names),
      page: { page: 1, size: viewTimeSeriesForm.page_size },
    });
    resultRows.value = mapTimeSeries(rsp.rows || []);
  });
}

async function queryViewRecord() {
  await runQuery(async () => {
    const space_id = spaceStore.requireSpaceId();
    requireInput(viewRecordForm.view_id, 'View');
    requireInput(viewRecordForm.dataset_id, 'Dataset');
    const rsp = await searchRecordRows({
      space_id,
      view_id: viewRecordForm.view_id,
      keys: viewRecordForm.record_id
        ? [{ space_id, dataset_id: viewRecordForm.dataset_id, record_id: viewRecordForm.record_id }]
        : [],
      text_query: viewRecordForm.text_query,
      version_range: { start_version: viewRecordForm.start_version, end_version: viewRecordForm.end_version },
      filters: parseJsonArray<FilterExpr>(viewRecordForm.filters, 'Filters'),
      sorts: parseJsonArray<SortSpec>(viewRecordForm.sorts, 'Sorts'),
      column_names: splitNames(viewRecordForm.column_names),
      page: { page: 1, size: viewRecordForm.page_size },
    });
    resultRows.value = mapRecord(rsp.rows || []);
  });
}
</script>

<style scoped>
.data-list-page {
  padding: 20px;
}

.page-head,
.result-head {
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

.page-head span,
.result-head span {
  color: var(--color-text-3);
}

.query-panel,
.result-panel {
  padding: 16px 0;
}

pre {
  max-width: 520px;
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
