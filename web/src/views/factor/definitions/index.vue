<template>
  <div class="moox-page">
    <div class="moox-inner">
    <div class="page-head">
      <h2>因子计算</h2>
      <a-space wrap>
        <a-select v-model="filters.kind" allow-clear placeholder="类型" style="width: 150px" @change="reloadFirstPage">
          <a-option value="timeseries">timeseries</a-option>
          <a-option value="cross_section">cross_section</a-option>
        </a-select>
        <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 130px" @change="reloadFirstPage">
          <a-option value="enabled">enabled</a-option>
          <a-option value="disabled">disabled</a-option>
        </a-select>
        <a-button @click="reloadFirstPage">查询</a-button>
        <a-button type="primary" status="success" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增因子
        </a-button>
      </a-space>
    </div>

    <a-table
      row-key="factor_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="rows"
      :pagination="pagination"
      :scroll="{ x: 'max-content' }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="因子ID" data-index="factor_id" :width="150" />
        <a-table-column title="模块名" data-index="name" :width="130" />
        <a-table-column title="类型" data-index="kind" :width="120" />
        <a-table-column title="参数" data-index="params_json" :width="180" :ellipsis="true" :tooltip="true" />
        <a-table-column title="回看" data-index="lookback_bars" :width="90" />
        <a-table-column title="写回" data-index="writeback_bars" :width="90" />
        <a-table-column title="源码Hash" data-index="source_hash" :width="220" :ellipsis="true" :tooltip="true" />
        <a-table-column title="状态" :width="100">
          <template #cell="{ record }">
            <a-tag size="small" :color="factorStatusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="180" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
              <a-button size="mini" type="text" @click="toggleStatus(record)">
                {{ record.status === 'enabled' ? '禁用' : '启用' }}
              </a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    </div>

    <a-modal
      v-model:visible="visible"
      width="920px"
      :title="modalTitle"
      :align-center="false"
      :top="'40px'"
      :modal-style="{ maxWidth: 'calc(100vw - 32px)' }"
      :body-style="{ maxHeight: 'calc(100vh - 168px)', overflowY: 'auto', padding: '18px var(--moox-space-6) 14px' }"
      @ok="submit"
    >
      <a-form class="factor-form" :model="form" layout="vertical">
        <a-form-item field="factor_id" label="因子ID" required>
          <a-input v-model="form.factor_id" :disabled="editing" placeholder="bias" />
        </a-form-item>
        <a-form-item field="name" label="Python 模块名" required>
          <a-input v-model="form.name" placeholder="Bias" />
        </a-form-item>
        <a-form-item field="kind" label="类型">
          <a-select v-model="form.kind">
            <a-option value="timeseries">timeseries</a-option>
            <a-option value="cross_section">cross_section</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option value="enabled">enabled</a-option>
            <a-option value="disabled">disabled</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="lookback_bars" label="回看K线数" required>
          <a-input-number v-model="form.lookback_bars" :min="1" />
        </a-form-item>
        <a-form-item field="writeback_bars" label="尾部写回数" required>
          <a-input-number v-model="form.writeback_bars" :min="1" />
        </a-form-item>
        <a-form-item class="form-span-2" field="params_json" label="参数JSON" required>
          <a-textarea v-model="form.params_json" :auto-size="{ minRows: 3, maxRows: 6 }" placeholder="[20,96,288]" />
        </a-form-item>
        <a-form-item class="form-span-2" field="depends_json" label="依赖JSON">
          <a-textarea v-model="form.depends_json" :auto-size="{ minRows: 2, maxRows: 5 }" placeholder="[]" />
        </a-form-item>
        <a-form-item class="form-span-2" field="source_code" label="源码" required>
          <a-textarea class="code-editor" v-model="form.source_code" :auto-size="{ minRows: 16, maxRows: 28 }" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createFactorDef, listFactorDefs, setFactorStatus, updateFactorDef } from '@/api/factor';
import type { FactorDef } from '@/api/factor/types';
import { applyPageResult, defaultPagination, formatTime } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'FactorDefinitions' });

const rows = ref<FactorDef[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());
const filters = reactive({ kind: '', status: '' });

const form = reactive<FactorDef>({
  factor_id: '',
  name: '',
  kind: 'timeseries',
  source_code: '',
  params_json: '[20]',
  lookback_bars: 200,
  writeback_bars: 5,
  depends_json: '[]',
  status: 'disabled',
});

const modalTitle = computed(() => (editing.value ? '编辑因子' : '新增因子'));

async function load() {
  loading.value = true;
  try {
    const rsp = await listFactorDefs({
      kind: filters.kind || undefined,
      status: filters.status || undefined,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.factors || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function reloadFirstPage() {
  pagination.current = 1;
  load();
}

function resetForm() {
  Object.assign(form, {
    factor_id: '',
    name: '',
    kind: 'timeseries',
    source_code: [
      'def signal(*args):',
      '    df = args[0]',
      '    n = args[1]',
      '    factor_name = args[2]',
      "    df[factor_name] = df['close'].rolling(n, min_periods=1).mean()",
      '    return df',
      '',
    ].join('\n'),
    params_json: '[20]',
    lookback_bars: 200,
    writeback_bars: 5,
    depends_json: '[]',
    status: 'disabled',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: FactorDef) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    params_json: record.params_json || '[]',
    depends_json: record.depends_json || '[]',
  });
  visible.value = true;
}

async function submit() {
  if (!form.factor_id || !form.name || !form.source_code) {
    Message.warning('请补全因子ID、模块名和源码');
    return;
  }
  JSON.parse(form.params_json || '[]');
  JSON.parse(form.depends_json || '[]');
  const payload = { ...form };
  if (editing.value) await updateFactorDef(payload);
  else await createFactorDef(payload);
  Message.success('因子已保存');
  visible.value = false;
  await load();
}

async function toggleStatus(record: FactorDef) {
  const next = record.status === 'enabled' ? 'disabled' : 'enabled';
  await setFactorStatus(record.factor_id, next);
  Message.success('状态已更新');
  await load();
}

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  load();
}

function factorStatusColor(status?: string) {
  if (status === 'enabled') return 'green';
  if (status === 'disabled') return 'orange';
  return 'gray';
}

onMounted(load);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-toolbar-table);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.factor-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 18px;
}

.form-span-2 {
  grid-column: span 2;
}

.code-editor :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 1.55;
}

@media (max-width: 768px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .factor-form {
    grid-template-columns: 1fr;
  }

  .form-span-2 {
    grid-column: span 1;
  }
}
</style>
