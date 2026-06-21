<template>
  <div class="data-browse-page">
    <div class="page-head">
      <h2>数据浏览</h2>
      <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <template v-else>
      <section class="picker">
        <a-form layout="inline">
          <a-form-item label="数据来源">
            <a-radio-group v-model="source" type="button" @change="onSourceChange">
              <a-radio value="dataset">数据集</a-radio>
              <a-radio value="view">视图</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item :label="source === 'dataset' ? '选择数据集' : '选择视图'">
            <a-select
              v-model="objectId"
              allow-search
              :loading="metaLoading"
              :placeholder="source === 'dataset' ? '请选择数据集' : '请选择视图'"
              style="width: 280px"
              @change="onObjectChange"
            >
              <a-option v-for="opt in objectOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </a-option>
            </a-select>
          </a-form-item>
          <a-button type="text" :loading="metaLoading" @click="loadMeta">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
        </a-form>
        <div v-if="modeHint" class="mode-hint">{{ modeHint }}</div>
      </section>

      <section v-if="mode === 'time_series'" class="query-panel">
        <a-form :model="tsForm" layout="vertical">
          <a-row :gutter="12">
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="对象（subject_id）">
                <a-input v-model="tsForm.subject_id" placeholder="subject_id" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="4">
              <a-form-item label="周期">
                <a-input v-model="tsForm.freq" placeholder="1m" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="5">
              <a-form-item label="开始时间">
                <a-input v-model="tsForm.start_time" placeholder="2024-01-01T00:00:00Z" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="5">
              <a-form-item label="结束时间">
                <a-input v-model="tsForm.end_time" placeholder="2024-01-02T00:00:00Z" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="8">
              <a-form-item label="列名">
                <a-input v-model="tsForm.column_names" placeholder="close,volume" />
              </a-form-item>
            </a-col>
            <a-col v-if="source === 'dataset'" :xs="12" :md="6" :lg="4">
              <a-form-item label="排序">
                <a-select v-model="tsForm.order">
                  <a-option value="SORT_ORDER_ASC">升序</a-option>
                  <a-option value="SORT_ORDER_DESC">降序</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="6" :lg="4">
              <a-form-item label="每页条数">
                <a-input-number v-model="tsForm.page_size" :min="1" :max="500" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="8">
              <a-form-item label="维度（高级，JSON）">
                <a-input v-model="tsForm.dimensions" placeholder='{"adjust":"none"}' />
              </a-form-item>
            </a-col>
          </a-row>
          <a-button type="primary" :loading="loading" @click="runQuery">
            <template #icon><icon-search /></template>
            查询
          </a-button>
        </a-form>
      </section>

      <section v-else-if="mode === 'record'" class="query-panel">
        <a-form :model="recForm" layout="vertical">
          <a-row :gutter="12">
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item :label="source === 'dataset' ? '记录 ID' : '记录 ID（可留空）'">
                <a-input v-model="recForm.record_id" placeholder="record_id" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="5">
              <a-form-item label="开始版本">
                <a-input v-model="recForm.start_version" placeholder="可留空" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="5">
              <a-form-item label="结束版本">
                <a-input v-model="recForm.end_version" placeholder="可留空" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="8">
              <a-form-item label="列名">
                <a-input v-model="recForm.column_names" placeholder="title,body" />
              </a-form-item>
            </a-col>
            <a-col v-if="source === 'dataset'" :xs="12" :md="6" :lg="4">
              <a-form-item label="排序">
                <a-select v-model="recForm.order">
                  <a-option value="SORT_ORDER_ASC">升序</a-option>
                  <a-option value="SORT_ORDER_DESC">降序</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="6" :lg="4">
              <a-form-item label="每页条数">
                <a-input-number v-model="recForm.page_size" :min="1" :max="500" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-collapse v-if="source === 'view'" :default-active-key="[]">
            <a-collapse-item key="adv" header="高级检索">
              <a-row :gutter="12">
                <a-col :xs="24" :md="12" :lg="8">
                  <a-form-item label="全文检索">
                    <a-input v-model="viewRecForm.text_query" placeholder="关键词" />
                  </a-form-item>
                </a-col>
                <a-col :xs="24" :lg="8">
                  <a-form-item label="过滤（JSON 数组）">
                    <a-textarea v-model="viewRecForm.filters" :auto-size="{ minRows: 3, maxRows: 6 }" />
                  </a-form-item>
                </a-col>
                <a-col :xs="24" :lg="8">
                  <a-form-item label="排序（JSON 数组）">
                    <a-textarea v-model="viewRecForm.sorts" :auto-size="{ minRows: 3, maxRows: 6 }" />
                  </a-form-item>
                </a-col>
              </a-row>
            </a-collapse-item>
          </a-collapse>

          <a-button type="primary" :loading="loading" @click="runQuery" style="margin-top: 12px">
            <template #icon><icon-search /></template>
            查询
          </a-button>
        </a-form>
      </section>

      <a-empty v-else-if="mode === 'missing'" description="无法识别该对象的数据类型" />
      <a-empty v-else description="请选择要浏览的数据集或视图" />

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
            <a-table-column title="版本" data-index="version" :width="210" />
            <a-table-column title="列数据" :width="520">
              <template #cell="{ record }">
                <pre>{{ record.columns }}</pre>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { listDatasets, listViews } from '@/api/storage/metadata';
import { readRecordRows, readTimeSeriesRows } from '@/api/storage/access';
import { queryTimeSeriesRows, searchRecordRows } from '@/api/storage/view';
import type { Dataset, FilterExpr, RecordRow, SortOrder, SortSpec, TimeSeriesRow, View } from '@/api/storage/types';
import { resolveViewRebuildKind, splitList } from '@/views/data/shared/metadata-utils';
import { useSpaceStore } from '@/store/modules/space';

defineOptions({ name: 'DataBrowse' });

interface ResultRow {
  id: string;
  key: string;
  version: string;
  columns: string;
}

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);

const source = ref<'dataset' | 'view'>('dataset');
const objectId = ref('');
const datasets = ref<Dataset[]>([]);
const views = ref<View[]>([]);
const metaLoading = ref(false);
const loading = ref(false);
const resultRows = ref<ResultRow[]>([]);

const tsForm = reactive({
  subject_id: '',
  freq: '',
  start_time: '',
  end_time: '',
  dimensions: '{}',
  column_names: '',
  order: 'SORT_ORDER_ASC',
  page_size: 100,
});

const recForm = reactive({
  record_id: '',
  start_version: '',
  end_version: '',
  column_names: '',
  order: 'SORT_ORDER_ASC',
  page_size: 100,
});

const viewRecForm = reactive({
  text_query: '',
  filters: '[]',
  sorts: '[]',
});

const objectOptions = computed(() => {
  if (source.value === 'dataset') {
    return datasets.value.map((item) => ({
      value: item.dataset_id,
      label: `${item.name || item.dataset_id} (${item.dataset_id})`,
    }));
  }
  return views.value.map((item) => ({
    value: item.view_id,
    label: `${item.name || item.view_id} (${item.view_id})`,
  }));
});

const currentView = computed(() => views.value.find((item) => item.view_id === objectId.value));

const mode = computed<'none' | 'time_series' | 'record' | 'missing'>(() => {
  if (!objectId.value) return 'none';
  if (source.value === 'dataset') {
    return resolveViewRebuildKind(datasets.value, objectId.value);
  }
  const view = currentView.value;
  if (!view) return 'missing';
  return resolveViewRebuildKind(datasets.value, view.primary_dataset_id);
});

const modeHint = computed(() => {
  const subject = source.value === 'dataset' ? '该数据集' : '该视图';
  if (mode.value === 'time_series') return `${subject}为时序数据`;
  if (mode.value === 'record') return `${subject}为记录数据`;
  if (mode.value === 'missing') return '无法识别该对象的数据类型';
  return '';
});

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

async function loadMeta() {
  const space_id = spaceStore.selectedSpaceId;
  if (!space_id) return;
  metaLoading.value = true;
  try {
    const [dsRsp, viewRsp] = await Promise.all([
      listDatasets({ space_id, page: { page: 1, size: 200 } }),
      listViews({ space_id, page: { page: 1, size: 200 } }),
    ]);
    datasets.value = dsRsp.datasets || [];
    views.value = viewRsp.views || [];
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载数据资产失败');
  } finally {
    metaLoading.value = false;
  }
}

function onSourceChange() {
  objectId.value = '';
  resultRows.value = [];
}

function onObjectChange() {
  resultRows.value = [];
}

async function runQuery() {
  if (mode.value === 'none') {
    Message.warning('请选择要浏览的数据集或视图');
    return;
  }
  if (mode.value === 'missing') {
    Message.error('无法识别该对象的数据类型');
    return;
  }
  loading.value = true;
  try {
    const space_id = spaceStore.requireSpaceId();

    if (source.value === 'dataset' && mode.value === 'time_series') {
      requireInput(tsForm.subject_id, '对象（subject_id）');
      requireInput(tsForm.freq, '周期');
      validateTime(tsForm.start_time, '开始时间');
      validateTime(tsForm.end_time, '结束时间');
      const rsp = await readTimeSeriesRows({
        keys: [{
          space_id,
          dataset_id: objectId.value,
          subject_id: tsForm.subject_id,
          freq: tsForm.freq,
          dimensions: parseJsonObject(tsForm.dimensions, '维度'),
        }],
        time_range: { start_time: tsForm.start_time, end_time: tsForm.end_time },
        order: tsForm.order as SortOrder,
        column_names: splitList(tsForm.column_names),
        page: { page: 1, size: tsForm.page_size },
      });
      resultRows.value = mapTimeSeries(rsp.rows || []);
      return;
    }

    if (source.value === 'dataset' && mode.value === 'record') {
      requireInput(recForm.record_id, '记录 ID');
      const rsp = await readRecordRows({
        keys: [{ space_id, dataset_id: objectId.value, record_id: recForm.record_id }],
        version_range: { start_version: recForm.start_version, end_version: recForm.end_version },
        order: recForm.order as SortOrder,
        column_names: splitList(recForm.column_names),
        page: { page: 1, size: recForm.page_size },
      });
      resultRows.value = mapRecord(rsp.rows || []);
      return;
    }

    const view = currentView.value;
    if (!view) {
      Message.error('无法识别该视图');
      return;
    }

    if (mode.value === 'time_series') {
      requireInput(tsForm.subject_id, '对象（subject_id）');
      requireInput(tsForm.freq, '周期');
      validateTime(tsForm.start_time, '开始时间');
      validateTime(tsForm.end_time, '结束时间');
      const rsp = await queryTimeSeriesRows({
        space_id,
        view_id: view.view_id,
        keys: [{
          space_id,
          dataset_id: view.primary_dataset_id,
          subject_id: tsForm.subject_id,
          freq: tsForm.freq,
          dimensions: parseJsonObject(tsForm.dimensions, '维度'),
        }],
        time_range: { start_time: tsForm.start_time, end_time: tsForm.end_time },
        column_names: splitList(tsForm.column_names),
        page: { page: 1, size: tsForm.page_size },
      });
      resultRows.value = mapTimeSeries(rsp.rows || []);
      return;
    }

    const rsp = await searchRecordRows({
      space_id,
      view_id: view.view_id,
      keys: recForm.record_id
        ? [{ space_id, dataset_id: view.primary_dataset_id, record_id: recForm.record_id }]
        : [],
      text_query: viewRecForm.text_query,
      version_range: { start_version: recForm.start_version, end_version: recForm.end_version },
      filters: parseJsonArray<FilterExpr>(viewRecForm.filters, '过滤'),
      sorts: parseJsonArray<SortSpec>(viewRecForm.sorts, '排序'),
      column_names: splitList(recForm.column_names),
      page: { page: 1, size: recForm.page_size },
    });
    resultRows.value = mapRecord(rsp.rows || []);
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '查询失败');
  } finally {
    loading.value = false;
  }
}

onMounted(loadMeta);
watch(selectedSpaceId, () => {
  objectId.value = '';
  resultRows.value = [];
  loadMeta();
});
</script>

<style scoped>
.data-browse-page {
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

.picker {
  padding: 8px 0 4px;
}

.mode-hint {
  margin-top: 8px;
  font-size: 13px;
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
