<template>
  <div class="sync-page">
    <div class="page-head">
      <div>
        <h2>数据同步</h2>
        <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
      </div>
      <a-button :disabled="!selectedSpaceId" @click="loadDatasets">
        <template #icon><icon-refresh /></template>
        刷新数据集
      </a-button>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <template v-else>
      <section class="sync-form">
        <a-form :model="form" layout="vertical">
          <a-row :gutter="12">
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="本地文件">
                <input type="file" accept=".csv,text/csv" @change="onFileChange" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="4">
              <a-form-item label="格式">
                <a-select v-model="form.format">
                  <a-option value="auto">auto</a-option>
                  <a-option value="csv">csv</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="Dataset">
                <a-select v-model="form.dataset_id" allow-search placeholder="选择数据集" @change="onDatasetChange">
                  <a-option v-for="item in datasets" :key="item.dataset_id" :value="item.dataset_id">
                    {{ item.name }} ({{ item.dataset_id }})
                  </a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="4">
              <a-form-item label="页内批量">
                <a-input-number v-model="form.batch_size" :min="1" :max="1000" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-row v-if="isTimeSeriesDataset" :gutter="12">
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="Subject">
                <a-input v-model="form.subject_id" placeholder="固定 subject_id" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="4">
              <a-form-item label="Freq">
                <a-input v-model="form.freq" placeholder="1m" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="时间列">
                <a-input v-model="form.time_column" placeholder="data_time" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="12" :lg="8">
              <a-form-item label="Dimensions JSON">
                <a-input v-model="form.dimensions" placeholder="{}" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-row v-else :gutter="12">
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="Record ID 列">
                <a-input v-model="form.record_id_column" placeholder="record_id" />
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="8" :lg="6">
              <a-form-item label="Version 列">
                <a-input v-model="form.version_column" placeholder="可留空，服务端生成默认版本" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-space>
            <a-button type="primary" :loading="loading" @click="dryRun">
              <template #icon><icon-search /></template>
              Dry Run
            </a-button>
            <a-button status="success" :disabled="!canImport" :loading="loading" @click="importRows">
              <template #icon><icon-upload /></template>
              导入
            </a-button>
          </a-space>
        </a-form>
      </section>

      <a-alert v-if="errors.length" type="error" show-icon class="sync-alert">
        <template #title>校验失败</template>
        <div v-for="item in errors" :key="item">{{ item }}</div>
      </a-alert>

      <a-alert v-else-if="dryRunSummary" type="success" show-icon class="sync-alert">
        {{ dryRunSummary }}
      </a-alert>

      <section class="preview-panel">
        <div class="preview-head">
          <strong>文件预览</strong>
          <span>{{ parsedRows.length }} 行，{{ headers.length }} 列</span>
        </div>
        <a-table
          row-key="id"
          size="small"
          :bordered="{ cell: true }"
          :data="previewRows"
          :pagination="false"
          :scroll="{ x: 'max-content', y: 420 }"
        >
          <template #columns>
            <a-table-column v-for="header in headers" :key="header" :title="header" :data-index="header" :width="160" />
          </template>
        </a-table>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { writeRecordRows, writeTimeSeriesRows } from '@/api/storage/access';
import { listDatasetColumns, listDatasets } from '@/api/storage/metadata';
import type { ColumnValue, Dataset, DatasetColumn, FieldValueType, RecordRow, TimeSeriesRow, TypedValue } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import { isTimeSeriesDataKind } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DataSync' });

type CsvRow = Record<string, string>;

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const datasets = ref<Dataset[]>([]);
const columns = ref<DatasetColumn[]>([]);
const file = ref<File>();
const fileText = ref('');
const loading = ref(false);
const headers = ref<string[]>([]);
const parsedRows = ref<CsvRow[]>([]);
const errors = ref<string[]>([]);
const dryRunSummary = ref('');

const form = reactive({
  format: 'auto',
  dataset_id: '',
  subject_id: '',
  freq: '',
  time_column: 'data_time',
  dimensions: '{}',
  record_id_column: 'record_id',
  version_column: 'version',
  batch_size: 500,
});

const selectedDataset = computed(() => datasets.value.find((item) => item.dataset_id === form.dataset_id));
const isTimeSeriesDataset = computed(() => isTimeSeriesDataKind(selectedDataset.value?.data_kind));
const canImport = computed(() => parsedRows.value.length > 0 && errors.value.length === 0 && !!form.dataset_id);
const previewRows = computed(() =>
  parsedRows.value.slice(0, 50).map((row, index) => ({ id: String(index), ...row })),
);

async function loadDatasets() {
  if (!selectedSpaceId.value) {
    datasets.value = [];
    return;
  }
  const rsp = await listDatasets({ space_id: selectedSpaceId.value, page: { page: 1, size: 500 } });
  datasets.value = rsp.datasets || [];
}

async function loadColumns() {
  if (!selectedSpaceId.value || !form.dataset_id) {
    columns.value = [];
    return;
  }
  const rsp = await listDatasetColumns({
    space_id: selectedSpaceId.value,
    dataset_id: form.dataset_id,
    page: { page: 1, size: 1000 },
  });
  columns.value = rsp.columns || [];
}

async function onDatasetChange() {
  errors.value = [];
  dryRunSummary.value = '';
  await loadColumns();
}

async function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement;
  file.value = input.files?.[0] || undefined;
  fileText.value = file.value ? await file.value.text() : '';
  headers.value = [];
  parsedRows.value = [];
  errors.value = [];
  dryRunSummary.value = '';
}

function parseCsvLine(line: string) {
  const cells: string[] = [];
  let current = '';
  let quoted = false;
  for (let i = 0; i < line.length; i += 1) {
    const char = line[i];
    const next = line[i + 1];
    if (char === '"' && quoted && next === '"') {
      current += '"';
      i += 1;
    } else if (char === '"') {
      quoted = !quoted;
    } else if (char === ',' && !quoted) {
      cells.push(current.trim());
      current = '';
    } else {
      current += char;
    }
  }
  cells.push(current.trim());
  return cells;
}

function parseCsv(text: string) {
  const lines = text.split(/\r?\n/).filter((line) => line.trim());
  if (lines.length < 2) throw new Error('CSV 至少需要表头和一行数据');
  const parsedHeaders = parseCsvLine(lines[0]);
  const rows = lines.slice(1).map((line, lineIndex) => {
    const cells = parseCsvLine(line);
    if (cells.length !== parsedHeaders.length) {
      throw new Error(`第 ${lineIndex + 2} 行列数与表头不一致`);
    }
    return parsedHeaders.reduce<CsvRow>((acc, header, index) => {
      acc[header] = cells[index] || '';
      return acc;
    }, {});
  });
  return { headers: parsedHeaders, rows };
}

function parseDimensions() {
  if (!form.dimensions.trim()) return {};
  const parsed = JSON.parse(form.dimensions);
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error('Dimensions 必须是 JSON 对象');
  }
  return parsed as Record<string, string>;
}

function columnNameMap() {
  const map = new Map<string, DatasetColumn>();
  columns.value.forEach((column) => {
    map.set(column.column_name, column);
    (column.aliases || []).forEach((alias) => map.set(alias, column));
  });
  return map;
}

function keyColumns() {
  if (isTimeSeriesDataset.value) {
    return new Set([form.time_column]);
  }
  return new Set([form.record_id_column, form.version_column].filter(Boolean));
}

function validateHeaders() {
  const failures: string[] = [];
  const map = columnNameMap();
  const keys = keyColumns();
  headers.value.forEach((header) => {
    if (!map.has(header) && !keys.has(header)) {
      failures.push(`未注册字段：${header}`);
    }
  });
  columns.value
    .filter((column) => column.required)
    .forEach((column) => {
      const names = [column.column_name, ...(column.aliases || [])];
      if (!names.some((name) => headers.value.includes(name))) {
        failures.push(`缺少必填字段：${column.column_name}`);
      }
    });
  if (isTimeSeriesDataset.value && !headers.value.includes(form.time_column)) {
    failures.push(`CSV 缺少时间列：${form.time_column}`);
  }
  if (!isTimeSeriesDataset.value && !headers.value.includes(form.record_id_column)) {
    failures.push(`CSV 缺少 Record ID 列：${form.record_id_column}`);
  }
  return failures;
}

function typedValue(valueType: FieldValueType, raw: string): TypedValue {
  if (valueType === 'FIELD_VALUE_TYPE_INT' || valueType === 2) return { int_value: raw ? Number(raw) : 0 };
  if (valueType === 'FIELD_VALUE_TYPE_DOUBLE' || valueType === 3) return { double_value: raw ? Number(raw) : 0 };
  if (valueType === 'FIELD_VALUE_TYPE_BOOL' || valueType === 4) return { bool_value: raw === 'true' || raw === '1' };
  if (valueType === 'FIELD_VALUE_TYPE_TIME' || valueType === 5) return { time_value: raw };
  if (valueType === 'FIELD_VALUE_TYPE_JSON' || valueType === 6) return { json_value: raw || '{}' };
  return { string_value: raw };
}

function rowColumns(row: CsvRow): ColumnValue[] {
  const map = columnNameMap();
  const keys = keyColumns();
  return headers.value
    .filter((header) => !keys.has(header))
    .map((header) => {
      const column = map.get(header);
      if (!column) return undefined;
      return {
        column_name: column.column_name,
        value_type: column.value_type,
        value: typedValue(column.value_type, row[header]),
      } satisfies ColumnValue;
    })
    .filter(Boolean) as ColumnValue[];
}

async function dryRun() {
  errors.value = [];
  dryRunSummary.value = '';
  try {
    if (!fileText.value) throw new Error('请选择本地 CSV 文件');
    if (!form.dataset_id) throw new Error('请选择 Dataset');
    if (form.format !== 'auto' && form.format !== 'csv') throw new Error(`暂不支持格式：${form.format}`);
    if (isTimeSeriesDataset.value && (!form.subject_id || !form.freq)) throw new Error('TimeSeries 导入需要填写 Subject 和 Freq');
    await loadColumns();
    const parsed = parseCsv(fileText.value);
    headers.value = parsed.headers;
    parsedRows.value = parsed.rows;
    errors.value = validateHeaders();
    if (errors.value.length === 0) {
      dryRunSummary.value = `校验通过：${parsedRows.value.length} 行，${headers.value.length} 列`;
    }
  } catch (error) {
    errors.value = [error instanceof Error ? error.message : 'CSV 校验失败'];
  }
}

async function importRows() {
  await dryRun();
  if (!canImport.value) return;
  loading.value = true;
  try {
    const space_id = spaceStore.requireSpaceId();
    if (isTimeSeriesDataset.value) {
      const dimensions = parseDimensions();
      const rows: TimeSeriesRow[] = parsedRows.value.map((row) => ({
        key: {
          space_id,
          dataset_id: form.dataset_id,
          subject_id: form.subject_id,
          freq: form.freq,
          dimensions,
          data_time: row[form.time_column],
        },
        columns: rowColumns(row),
      }));
      await writeTimeSeriesRows(rows);
    } else {
      const rows: RecordRow[] = parsedRows.value.map((row) => ({
        key: {
          space_id,
          dataset_id: form.dataset_id,
          record_id: row[form.record_id_column],
          version: form.version_column ? row[form.version_column] : '',
        },
        columns: rowColumns(row),
      }));
      await writeRecordRows(rows);
    }
    Message.success(`导入完成：${parsedRows.value.length} 行`);
  } finally {
    loading.value = false;
  }
}

watch(selectedSpaceId, loadDatasets);
onMounted(loadDatasets);
</script>

<style scoped>
.sync-page {
  padding: 20px;
}

.page-head,
.preview-head {
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
.preview-head span {
  color: var(--color-text-3);
}

.sync-form,
.preview-panel {
  padding: 16px 0;
}

.sync-alert {
  margin: 12px 0;
}
</style>
